package geomap

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultTileProviders is the built-in keyless, public, single-host tile
// providers. All use {z}/{x}/{y} (no {s} subdomain) so the proxy can fetch a
// single upstream URL per tile. Hosts override/extend with WithTileProviders.
//
// This map IS the SSRF guard: the upstream host is never client-controlled.
// The handler only ever interpolates validated integers into one of these
// fixed templates, so a request for /tiles/..%2F..%2F or a non-allowlisted host
// cannot reach an attacker-chosen upstream.
func defaultTileProviders() map[string]string {
	return map[string]string{
		"osm":         "https://tile.openstreetmap.org/{z}/{x}/{y}.png",
		"carto-light": "https://basemaps.cartocdn.com/light_all/{z}/{x}/{y}.png",
		"carto-dark":  "https://basemaps.cartocdn.com/dark_all/{z}/{x}/{y}.png",
	}
}

// tilePlaceholderRe matches a URL template containing literal {z}, {x}, {y}
// placeholders (in any order). A provider template missing any one of them
// cannot render tiles and is rejected at construction.
var tilePlaceholderRe = regexp.MustCompile(`\{z\}.*\{x\}.*\{y\}|\{x\}.*\{y\}.*\{z\}|(\{z\}.*\{y\}.*\{x\})|(\{x\}.*\{z\}.*\{y\})|(\{y\}.*\{x\}.*\{z\})|(\{y\}.*\{z\}.*\{x\})`)

// validateTileTemplate rejects a provider URL template that does not contain
// all three of {z}/{x}/{y}. It is the construction-time guard that makes a bad
// WithTileProviders entry fail loud (panic in New) rather than 500ing at first
// tile request.
func validateTileTemplate(tpl string) error {
	if !strings.Contains(tpl, "{z}") || !strings.Contains(tpl, "{x}") || !strings.Contains(tpl, "{y}") {
		return fmt.Errorf("tile template must contain literal {z}, {x}, {y} placeholders; got %q", tpl)
	}
	return nil
}

const (
	// defaultTileCacheEntries caps the in-memory tile cache. Bounded so e2e /
	// demo pan-and-zoom can't grow it without limit (respects tile-server
	// policy). An LRU eviction keeps the hottest tiles resident.
	defaultTileCacheEntries = 256
	// tileClientTimeout caps the upstream fetch (DoS / slow-tile-server guard).
	tileClientTimeout = 10 * time.Second
	// tileMaxBody caps a single upstream tile response (a PNG tile is small;
	// a huge response is upstream abuse or an attack).
	tileMaxBody = 8 << 20 // 8 MiB
	// tileMaxZoom is the highest zoom the proxy serves. Beyond this OSM/carto
	// have no tiles; rejecting up-front avoids a useless upstream round-trip.
	tileMaxZoom = 22
)

// tileCache is a small bounded LRU keyed by provider/z/x/y. The hot path is
// panning/zooming the same map, where many tile requests repeat within a
// session. The cache is in-memory and per-Plugin-instance; persistence across
// process restarts is a non-goal.
type tileCache struct {
	cap  int
	mu   sync.Mutex
	m    map[string]*cacheEntry
	head *cacheEntry // most-recent
	tail *cacheEntry // least-recent (eviction candidate)
}

type cacheEntry struct {
	key  string
	data []byte
	ct   string
	prev *cacheEntry
	next *cacheEntry
}

func newTileCache(cap int) *tileCache {
	if cap < 1 {
		cap = 1
	}
	return &tileCache{cap: cap, m: make(map[string]*cacheEntry, cap)}
}

func (c *tileCache) get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return nil, "", false
	}
	c.moveToFront(e)
	return e.data, e.ct, true
}

func (c *tileCache) put(key string, data []byte, ct string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		e.data, e.ct = data, ct
		c.moveToFront(e)
		return
	}
	e := &cacheEntry{key: key, data: data, ct: ct}
	c.m[key] = e
	c.pushFront(e)
	for len(c.m) > c.cap {
		c.evictTail()
	}
}

func (c *tileCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

func (c *tileCache) pushFront(e *cacheEntry) {
	e.prev, e.next = nil, c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *tileCache) moveToFront(e *cacheEntry) {
	if c.head == e {
		return
	}
	c.unlink(e)
	c.pushFront(e)
}

func (c *tileCache) unlink(e *cacheEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (c *tileCache) evictTail() {
	if c.tail == nil {
		return
	}
	delete(c.m, c.tail.key)
	c.unlink(c.tail)
}

// tileClient is the upstream fetcher. Overridable in tests via the
// p.tileFetch field (kept unexported; tests in the same package set it).
type tileFetcher interface {
	Do(*http.Request) (*http.Response, error)
}

// handleTiles implements GET TilesRoutePattern. It is the same-origin proxy
// the opaque-origin frame loads its raster tiles from. The provider name is
// looked up in the allowlist (SSRF guard), z/x/y are validated as integers in
// range, and the upstream URL is interpolated ONLY from those validated
// integers — never from a raw client string. Responses are cached in a bounded
// LRU to respect tile-server policy under e2e / demo pan-and-zoom load.
func (p *Plugin) handleTiles(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	zs, xs, ys := r.PathValue("z"), r.PathValue("x"), r.PathValue("y")

	upstream, ok := p.tileProviders[provider]
	if !ok {
		// Unknown provider: do NOT make an upstream request. 404 (not 400) —
		// a non-allowlisted provider is a client error Leaflet surfaces as a
		// failed tile, identical to a 404 image.
		http.NotFound(w, r)
		return
	}
	z, err := strconv.Atoi(zs)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_Z", "z must be an integer")
		return
	}
	x, err := strconv.Atoi(xs)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_X", "x must be an integer")
		return
	}
	y, err := strconv.Atoi(ys)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_Y", "y must be an integer")
		return
	}
	if z < 0 || z > tileMaxZoom {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_Z", fmt.Sprintf("z out of range 0..%d", tileMaxZoom))
		return
	}
	maxXY := (1 << uint(z)) - 1
	if z > 0 && (x < 0 || x > maxXY || y < 0 || y > maxXY) {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_TILE", fmt.Sprintf("x/y out of range for z=%d", z))
		return
	}

	cacheKey := fmt.Sprintf("%s/%d/%d/%d", provider, z, x, y)
	if data, ct, ok := p.tileCache.get(cacheKey); ok {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		setTileCORP(w)
		w.Header().Set("X-Geomap-Cache", "HIT")
		_, _ = w.Write(data)
		return
	}

	// Interpolate ONLY the validated integers into the fixed template — never
	// raw client strings. This is the SSRF invariant.
	url := strings.NewReplacer("{z}", strconv.Itoa(z), "{x}", strconv.Itoa(x), "{y}", strconv.Itoa(y)).Replace(upstream)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, "upstream URL", http.StatusBadGateway)
		return
	}
	// OSM usage policy requires a valid User-Agent; anonymous requests may be
	// throttled or 403'd. We set it on every upstream request.
	req.Header.Set("User-Agent", "gofastr-plugins-geomap/0.1")
	// Be a polite proxy: pass through an Accept that matches raster tiles, but
	// DO NOT forward client headers wholesale (no Cookie, no Authorization — a
	// sandboxed frame has none anyway, and we never want to leak host authn to
	// a third-party tile server).
	req.Header.Set("Accept", "image/png,image/*;q=0.8,*/*;q=0.5")

	fetcher := p.tileFetcherFor(r.Context())
	resp, err := fetcher.Do(req)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Surface the upstream status verbatim (404/403/429/5xx) so Leaflet
		// treats the tile as failed without us having to synthesize one.
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "upstream status", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, tileMaxBody))
	if err != nil {
		http.Error(w, "upstream read", http.StatusBadGateway)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || !strings.HasPrefix(ct, "image/") {
		ct = "image/png"
	}
	p.tileCache.put(cacheKey, body, ct)

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	setTileCORP(w)
	w.Header().Set("X-Geomap-Cache", "MISS")
	// Defend against the frame attempting to embed this response somewhere
	// unexpected: it is only ever an <img> source same-origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

// setTileCORP marks the tile response loadable by the OPAQUE-origin plugin
// frame. The frame runs sandbox="allow-scripts" without allow-same-origin, so
// its document is a "null" origin; a tile fetched from this (concrete) host
// origin is therefore CROSS-origin to the frame. GoFastr's global security
// middleware defaults Cross-Origin-Resource-Policy to same-origin, which the
// browser enforces by BLOCKING the null-origin frame's <img> load
// (ERR_BLOCKED_BY_RESPONSE.NotSameOrigin) — a gray, tile-less map. The framed
// asset server sets this same header on the frame bundle for exactly this
// reason; the tile proxy is a separately-registered route, so it must set it
// itself. cross-origin is safe here: a raster tile is public map data, carries
// no cookies/authn, and is only ever used as an <img> source.
func setTileCORP(w http.ResponseWriter) {
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
}

// tileFetcherFor returns the upstream client. It is the package-level
// default (with timeout + redirect cap) unless a test has injected one via
// p.tileClient.
func (p *Plugin) tileFetcherFor(ctx context.Context) tileFetcher {
	if p.tileClient != nil {
		return p.tileClient
	}
	return defaultTileClient
}

// defaultTileClient caps upstream latency and follows a couple of redirects
// (CDN hosts like cartocdn redirect to the cached object). It is package-level
// and reused across requests (connection pooling).
var defaultTileClient = &http.Client{
	Timeout: tileClientTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("geomap: stopped after %d redirects", len(via))
		}
		// Re-assert the UA on each redirect hop (some clients drop it).
		req.Header.Set("User-Agent", "gofastr-plugins-geomap/0.1")
		return nil
	},
}
