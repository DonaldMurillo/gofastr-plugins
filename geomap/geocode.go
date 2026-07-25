package geomap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Place search. The browser NEVER calls a geocoder directly — map.js hits
// [GeocodeURL] on the host's own origin and this file proxies upstream. That
// buys three things a browser-side call cannot have:
//
//   - A policy-compliant, identifying User-Agent. Nominatim (the default
//     upstream) REQUIRES one and blocks anonymous traffic; a browser cannot set
//     it, and a fetch from a page would send the user's UA + Referer instead.
//   - A server-side rate limit. Nominatim's usage policy caps absolute traffic
//     at 1 request/second per application — that is an application-wide budget,
//     which only the server can enforce. Per-browser throttling cannot.
//   - Caching, which the same policy explicitly asks for.
//
// It also keeps the host page CSP at `connect-src 'self'` — no third-party
// origin has to be allowlisted for search to work.
//
// The public geocoder is a courtesy service run on donated hardware. Anything
// beyond light interactive use should point [WithGeocodeEndpoint] at a
// self-hosted Nominatim / Photon, or replace the lookup wholesale with
// [WithGeocoder].

const (
	// defaultGeocodeEndpoint is the public Nominatim search endpoint. See the
	// usage policy: https://operations.osmfoundation.org/policies/nominatim/
	defaultGeocodeEndpoint = "https://nominatim.openstreetmap.org/search"

	// geocodeMinInterval is the floor between two UPSTREAM requests, process-wide
	// per plugin instance. Nominatim's policy is "absolute maximum of 1 request
	// per second"; we hold exactly that.
	geocodeMinInterval = time.Second

	// geocodeMaxWait is how long a request will queue for a rate-limit slot
	// before we give up and return 429. Without a ceiling a burst would pile up
	// goroutines each sleeping longer than the last.
	geocodeMaxWait = 3 * time.Second

	// geocodeTimeout bounds the upstream call.
	geocodeTimeout = 6 * time.Second

	// geocodeCacheTTL is how long a query's results are reused. Place geocoding
	// is extremely cache-friendly (results move on a scale of months).
	geocodeCacheTTL = 10 * time.Minute

	// geocodeCacheMax caps the cache so a query-spamming client cannot grow it
	// without bound. On overflow the cache is dropped wholesale — this is a hot
	// path guard, not an eviction policy worth a heap.
	geocodeCacheMax = 512

	// geocodeMaxQuery is the longest accepted query. Nominatim ignores anything
	// longer anyway, and it caps what we forward.
	geocodeMaxQuery = 200

	// geocodeMaxResponse bounds what we read back from the upstream, so a
	// misbehaving or hostile endpoint cannot stream us out of memory. Five
	// geocoder hits are a few KiB; a MiB is already generous.
	geocodeMaxResponse = 1 << 20

	// geocodeLimit is how many hits we request/return.
	geocodeLimit = 5
)

// errGeocodeBusy is returned when a request would have to wait longer than
// [geocodeMaxWait] for a rate-limit slot. handleGeocode maps it to 429.
var errGeocodeBusy = errors.New("geomap: geocoder busy")

// GeocodeResult is one place-search hit. The json tags are the wire contract
// map.js reads (label/lat/lng) — the search control renders `label` and flies to
// lat/lng.
type GeocodeResult struct {
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
}

// Geocoder resolves a free-text place query to candidate results. Return an
// empty slice (not an error) for "no matches" — an error means the lookup
// itself failed and is surfaced to the browser as a 502.
type Geocoder func(ctx context.Context, query string) ([]GeocodeResult, error)

type geoCacheEntry struct {
	results []GeocodeResult
	expires time.Time
}

// WithSearch enables the place-search control and registers [GeocodeURL].
// Without any of the options below it proxies the public Nominatim endpoint
// under that service's usage policy (identifying User-Agent, 1 req/s, cached).
// Read the policy before pointing a production app at the public instance:
// https://operations.osmfoundation.org/policies/nominatim/
func WithSearch() Option {
	return func(p *Plugin) { p.searchEnabled = true }
}

// WithGeocoder replaces the lookup entirely and implies [WithSearch]. Use it to
// plug in a commercial geocoder, an internal place index, or a fixed dataset
// (which is how the example app keeps its e2e run offline). When set, none of
// the Nominatim machinery — endpoint, User-Agent, rate limit — is used; caching
// still applies.
func WithGeocoder(fn Geocoder) Option {
	return func(p *Plugin) {
		if fn != nil {
			p.geocoder = fn
			p.searchEnabled = true
		}
	}
}

// WithGeocodeEndpoint points the built-in Nominatim proxy at a different
// Nominatim-compatible search endpoint (a self-hosted instance, or a mirror).
// The endpoint is fixed at configuration time and only the `q` parameter is
// user-controlled, so this is not an SSRF surface. Implies [WithSearch].
func WithGeocodeEndpoint(endpoint string) Option {
	return func(p *Plugin) {
		if endpoint != "" {
			p.geocodeEndpoint = endpoint
			p.searchEnabled = true
		}
	}
}

// WithGeocodeUserAgent sets the User-Agent sent upstream. Nominatim's usage
// policy REQUIRES a header that identifies your application and gives them a way
// to contact you — set this to something like
// "acme-maps/1.4 (+https://acme.example/contact)" for any real deployment.
// Implies [WithSearch].
func WithGeocodeUserAgent(ua string) Option {
	return func(p *Plugin) {
		if ua != "" {
			p.geocodeUA = ua
			p.searchEnabled = true
		}
	}
}

// initSearch resolves the search configuration after all options have run. It is
// called by New. When search is off it is a no-op, so a plugin that never opts in
// carries no geocoder state and registers no route.
func (p *Plugin) initSearch() {
	if !p.searchEnabled {
		return
	}
	if p.geocodeEndpoint == "" {
		p.geocodeEndpoint = defaultGeocodeEndpoint
	}
	if p.geocodeUA == "" {
		// A generic fallback so the default path is not anonymous. Hosts SHOULD
		// override it with WithGeocodeUserAgent — the policy wants a contactable
		// application identity, and this one identifies the plugin, not the app.
		p.geocodeUA = "gofastr-plugins-geomap/" + Version + " (+https://github.com/DonaldMurillo/gofastr-plugins)"
	}
	if p.geocodeClient == nil {
		p.geocodeClient = &http.Client{Timeout: geocodeTimeout}
	}
	if p.geocoder == nil {
		p.geocoder = p.nominatimGeocode
	}
	if p.geoCache == nil {
		p.geoCache = make(map[string]geoCacheEntry)
	}
	// Search is egress the host explicitly turned on; the gate must be able to
	// pass. See CapGeocode.
	for _, c := range p.capabilities {
		if c == CapGeocode {
			p.defaultConfig.SearchURL = GeocodeURL
			return
		}
	}
	p.capabilities = append(p.capabilities, CapGeocode)
	p.defaultConfig.SearchURL = GeocodeURL
}

// handleGeocode implements GET [GeocodeURL]?q=<query>. It responds
// {"results":[{label,lat,lng}, …]} — the shape map.js's search control reads.
func (p *Plugin) handleGeocode(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapGeocode) {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "missing q")
		return
	}
	if len(q) > geocodeMaxQuery {
		q = q[:geocodeMaxQuery]
	}

	if cached, ok := p.geocodeCacheGet(q); ok {
		writeJSON(w, http.StatusOK, map[string]any{"results": cached})
		return
	}

	results, err := p.geocoder(r.Context(), q)
	if err != nil {
		if errors.Is(err, errGeocodeBusy) {
			// Tell the client to back off rather than pretending there were no
			// matches — "no results" and "we throttled you" are different answers.
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, http.StatusTooManyRequests, "E_RATE_LIMITED", "geocoder rate limit")
			return
		}
		if errors.Is(err, context.Canceled) {
			// The browser navigated away mid-search; nothing to report.
			return
		}
		writeJSONError(w, http.StatusBadGateway, "E_GEOCODE", err.Error())
		return
	}
	if results == nil {
		results = []GeocodeResult{}
	}
	if len(results) > geocodeLimit {
		results = results[:geocodeLimit]
	}
	p.geocodeCachePut(q, results)
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (p *Plugin) geocodeCacheGet(q string) ([]GeocodeResult, bool) {
	key := strings.ToLower(q)
	p.geoMu.Lock()
	defer p.geoMu.Unlock()
	e, ok := p.geoCache[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.results, true
}

func (p *Plugin) geocodeCachePut(q string, results []GeocodeResult) {
	key := strings.ToLower(q)
	p.geoMu.Lock()
	defer p.geoMu.Unlock()
	if p.geoCache == nil {
		p.geoCache = make(map[string]geoCacheEntry)
	}
	if len(p.geoCache) >= geocodeCacheMax {
		p.geoCache = make(map[string]geoCacheEntry, geocodeCacheMax)
	}
	p.geoCache[key] = geoCacheEntry{results: results, expires: time.Now().Add(geocodeCacheTTL)}
}

// reserveUpstreamSlot enforces the 1-request-per-second upstream budget across
// all callers. It hands out slots in the future rather than sleeping under the
// lock, so N concurrent searches queue at 1s spacing instead of serializing on a
// held mutex. A slot further out than geocodeMaxWait is refused outright.
func (p *Plugin) reserveUpstreamSlot(ctx context.Context) error {
	p.geoMu.Lock()
	now := time.Now()
	slot := p.geoNext
	if slot.Before(now) {
		slot = now
	}
	wait := slot.Sub(now)
	if wait > geocodeMaxWait {
		p.geoMu.Unlock()
		return errGeocodeBusy
	}
	p.geoNext = slot.Add(geocodeMinInterval)
	p.geoMu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// nominatimGeocode is the default lookup: a rate-limited, identified proxy call
// to a Nominatim-compatible endpoint.
func (p *Plugin) nominatimGeocode(ctx context.Context, query string) ([]GeocodeResult, error) {
	if err := p.reserveUpstreamSlot(ctx); err != nil {
		return nil, err
	}

	endpoint, err := url.Parse(p.geocodeEndpoint)
	if err != nil {
		return nil, err
	}
	q := endpoint.Query()
	q.Set("q", query)
	q.Set("format", "jsonv2")
	q.Set("limit", strconv.Itoa(geocodeLimit))
	q.Set("addressdetails", "0")
	endpoint.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(ctx, geocodeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.geocodeUA)
	req.Header.Set("Accept", "application/json")

	res, err := p.geocodeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("geocoder upstream status " + strconv.Itoa(res.StatusCode))
	}

	// Nominatim returns lat/lon as STRINGS, not numbers — decoding them into
	// float64 fields silently yields zeros (a pin in the Gulf of Guinea).
	var raw []struct {
		DisplayName string `json:"display_name"`
		Name        string `json:"name"`
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, geocodeMaxResponse)).Decode(&raw); err != nil {
		return nil, err
	}

	out := make([]GeocodeResult, 0, len(raw))
	for _, r := range raw {
		lat, errLat := strconv.ParseFloat(r.Lat, 64)
		lng, errLng := strconv.ParseFloat(r.Lon, 64)
		if errLat != nil || errLng != nil {
			continue
		}
		label := r.DisplayName
		if label == "" {
			label = r.Name
		}
		if label == "" {
			label = query
		}
		out = append(out, GeocodeResult{Label: label, Lat: lat, Lng: lng})
	}
	return out, nil
}
