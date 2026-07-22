package geomap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newTestApp stands up a fresh framework.App with the plugin registered and
// initialized (mirrors monaco/mermaid/richtext's harness; the in-memory store
// needs no DB).
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "geomap-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{MapHTMLURL, "text/html; charset=utf-8"},
		{MapJSURL, "text/javascript; charset=utf-8"},
		{MapCSSURL, "text/css; charset=utf-8"},
		{AdapterScriptURL, "text/javascript; charset=utf-8"},
		{ConfigScriptURL, "text/javascript; charset=utf-8"},
	}
	for _, c := range cases {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%q", c.path, resp.StatusCode, body)
		}
		if got := resp.Header.Get("Content-Type"); got != c.wantCT {
			t.Errorf("%s: Content-Type=%q want %q", c.path, got, c.wantCT)
		}
		if len(body) == 0 {
			t.Errorf("%s: empty body", c.path)
		}
	}
}

// TestFramedAssetsCarryHeaderRelaxation proves the platform AssetServer carries
// the framing/CORP/CSP relaxation that lets the host frame its OWN map document
// and lets the opaque frame fetch its JS/CSS (DECISIONS.md Phase-0 gotcha #1).
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{MapHTMLURL, MapJSURL, MapCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors http") {
			t.Errorf("%s: CSP frame-ancestors must permit host origin: %q", path, csp)
		}
		if strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP must NOT carry frame-ancestors 'none': %q", path, csp)
		}
		if strings.Contains(csp, "script-src 'self'") {
			t.Errorf("%s: framed CSP must not use 'self' for scripts (opaque frame): %q", path, csp)
		}
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
			t.Errorf("%s: CORP=%q want cross-origin", path, got)
		}
	}

	// Host-page scripts are NON-framed: they must NOT carry CORP cross-origin.
	for _, path := range []string{AdapterScriptURL, ConfigScriptURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got == "cross-origin" {
			t.Errorf("%s: host-page script must NOT be CORP cross-origin", path)
		}
	}

	resp, err := http.Get(srv.URL + pluginhost.BrokerScriptURL)
	if err != nil {
		t.Fatalf("GET pluginhost broker: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("pluginhost broker status=%d", resp.StatusCode)
	}
}

func TestDemoPageContainsTokensMarkerAndBroker(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("demo status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("demo Content-Type=%q", ct)
	}
	for _, want := range []string{
		"--color-",
		`data-fui-plugin="map"`,
		`data-fui-plugin-for="map_doc"`,
		`<script src="` + pluginhost.BrokerScriptURL,
		`<script src="` + ConfigScriptURL,
		`<script src="` + AdapterScriptURL,
		`data-color-scheme="light"`,
		`data-label="Tokyo"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

// TestConfigScriptReflectsOptions proves Go With* options reach the frame: the
// host-page config.js the plugin serves is marshaled from the instance's
// MapConfig, so the adapter (and thus init.config) carries the compiled-in
// defaults.
func TestConfigScriptReflectsOptions(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithCenter(40.7128, -74.006), WithZoom(11), WithProvider("carto-dark"))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + ConfigScriptURL)
	if err != nil {
		t.Fatalf("GET config.js: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	got := string(body)
	for _, want := range []string{
		"window.__gofastrMapConfig = ",
		`"lat":40.7128`,
		`"zoom":11`,
		`"provider":"carto-dark"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.js missing %q; body=%q", want, got)
		}
	}
}

// TestSaveRoundTripDevGrant proves the canonical {lat,lng,zoom,markers} doc
// round-trips through the in-memory store, AND asserts the wire format uses the
// lowercase json tags the frame's deriveDoc reads. This is the exact regression
// that bit the monaco savedDoc: a Go↔Go round-trip passes even with capitalized
// struct keys, so the lowercase shape must be asserted explicitly.
func TestSaveRoundTripDevGrant(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	markers := []mapMarker{{ID: "m1", Lat: 40.7128, Lng: -74.006, Label: "NYC"}}
	payload, err := json.Marshal(map[string]any{
		"docId":         "demo",
		"doc":           map[string]any{"lat": 40.7128, "lng": -74.006, "zoom": 11.0, "markers": markers},
		"lat":           40.7128,
		"lng":           -74.006,
		"zoom":          11.0,
		"markers":       markers,
		"schemaVersion": "map-v1",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	resp, err := http.Post(srv.URL+SaveURL, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d", resp.StatusCode)
	}

	docJSON, ok := p.LoadDoc(context.Background(), "demo")
	if !ok {
		t.Fatal("LoadDoc: doc missing after save")
	}
	var got mapDoc
	if err := json.Unmarshal([]byte(docJSON), &got); err != nil {
		t.Fatalf("LoadDoc returned non-canonical JSON: %v (%q)", err, docJSON)
	}
	if got.Lat != 40.7128 || got.Lng != -74.006 || got.Zoom != 11 || len(got.Markers) != 1 || got.Markers[0].ID != "m1" {
		t.Errorf("LoadDoc round-trip mismatch: %+v", got)
	}
	// Wire format: lowercase {lat,lng,zoom,markers}. Untagged fields would emit
	// {Lat,Lng,Zoom,Markers} and the frame would silently mount an EMPTY map.
	if !strings.Contains(docJSON, `"lat"`) || !strings.Contains(docJSON, `"lng"`) ||
		!strings.Contains(docJSON, `"zoom"`) || !strings.Contains(docJSON, `"markers"`) {
		t.Errorf("LoadDoc JSON must use lowercase lat/lng/zoom/markers keys (frame contract); got %q", docJSON)
	}
	if strings.Contains(docJSON, `"Lat"`) || strings.Contains(docJSON, `"Lng"`) ||
		strings.Contains(docJSON, `"Zoom"`) || strings.Contains(docJSON, `"Markers"`) {
		t.Errorf("LoadDoc JSON leaks capitalized struct keys; the frame reads lowercase; got %q", docJSON)
	}
}

// TestSaveConflictMapsTo409 mirrors monaco's TestSaveConflictMapsTo409: a save
// handler returning ErrConflict (bare or wrapped) surfaces as 409/E_CONFLICT,
// the one status the adapter relays to the frame as a distinct saveResult; any
// other error stays a generic 500/E_SAVE.
func TestSaveConflictMapsTo409(t *testing.T) {
	postSave := func(t *testing.T, saveErr error) (int, string) {
		t.Helper()
		app, _ := newTestApp(t, WithDevGrantAll(), WithSaveHandler(
			func(context.Context, SaveRequest) error { return saveErr },
		))
		srv := httptest.NewServer(app.Router())
		defer srv.Close()

		payload := `{"docId":"demo","doc":{"lat":1,"lng":2,"zoom":3,"markers":[]},"lat":1,"lng":2,"zoom":3,"markers":[],"schemaVersion":"map-v1"}`
		resp, err := http.Post(srv.URL+SaveURL, "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("POST save: %v", err)
		}
		defer resp.Body.Close()
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body.Error
	}

	t.Run("bare ErrConflict", func(t *testing.T) {
		if status, code := postSave(t, ErrConflict); status != http.StatusConflict || code != "E_CONFLICT" {
			t.Errorf("got status=%d code=%q, want 409/E_CONFLICT", status, code)
		}
	})
	t.Run("wrapped ErrConflict", func(t *testing.T) {
		wrapped := fmt.Errorf("doc %q changed under the map: %w", "demo", ErrConflict)
		if status, code := postSave(t, wrapped); status != http.StatusConflict || code != "E_CONFLICT" {
			t.Errorf("got status=%d code=%q, want 409/E_CONFLICT", status, code)
		}
	})
	t.Run("other error stays 500", func(t *testing.T) {
		if status, code := postSave(t, fmt.Errorf("disk full")); status != http.StatusInternalServerError || code != "E_SAVE" {
			t.Errorf("got status=%d code=%q, want 500/E_SAVE", status, code)
		}
	})
}

// TestCapabilityGate proves the auth.HasScope reuse: a token-authenticated
// request whose scopes do not grant the capability is denied, while
// WithDevGrantAll short-circuits the gate.
func TestCapabilityGate(t *testing.T) {
	enforcing := New()
	granted := New(WithDevGrantAll())

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	deniedReq := httptest.NewRequest(http.MethodPost, SaveURL, nil).WithContext(deniedCtx)

	if enforcing.allow(deniedReq, "document:write") {
		t.Error("enforcing plugin should DENY a non-granting token")
	}
	if !granted.allow(deniedReq, "document:write") {
		t.Error("WithDevGrantAll should ALLOW regardless of token scopes")
	}
}

// TestSaveDeniedWithoutCapability proves the gate is wired into the route.
func TestSaveDeniedWithoutCapability(t *testing.T) {
	p := New() // enforcing (no devGrantAll)

	payload := `{"docId":"demo","doc":null,"lat":0,"lng":0,"zoom":0,"markers":[],"schemaVersion":"map-v1"}`
	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	req := httptest.NewRequest(http.MethodPost, SaveURL, strings.NewReader(payload)).WithContext(deniedCtx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	p.handleSave(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "E_CAPABILITY_DENIED" {
		t.Errorf("error=%q want E_CAPABILITY_DENIED", body.Error)
	}
}

// stubFetcher is a test stand-in for the upstream *http.Client. Recording the
// requests lets us assert exactly which upstream URL the proxy hit (or that it
// hit none at all, for the SSRF / validation cases). The mutex guards the
// recorded-reqs slice so concurrent requests under -race stay clean.
type stubFetcher struct {
	mu       sync.Mutex
	reqs     []*http.Request
	resp     *http.Response
	respErr  error
}

func (s *stubFetcher) Do(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	if s.respErr != nil {
		return nil, s.respErr
	}
	if s.resp != nil {
		return s.resp, nil
	}
	// Default: a tiny PNG-shaped body so the proxy can copy it through.
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"image/png"}},
		Body:       io.NopCloser(strings.NewReader(string(png))),
	}, nil
}

func newTileTestApp(t *testing.T, fetcher tileFetcher) (*framework.App, *Plugin, *stubFetcher) {
	t.Helper()
	stub := &stubFetcher{}
	if fetcher == nil {
		fetcher = stub
	}
	p := New(WithDevGrantAll())
	p.tileClient = fetcher
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "geomap-tile-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p, stub
}

// TestTileProxyRejectsUnknownProvider is the SSRF guard: a request for an
// unknown provider must 404 and MUST NOT make any upstream request. The
// allowlist is the entire SSRF defence — the upstream host is never
// client-controlled.
func TestTileProxyRejectsUnknownProvider(t *testing.T) {
	app, _, stub := newTileTestApp(t, nil)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + RoutePrefix + "/tiles/evil-host/5/10/15")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown provider: status=%d want 404", resp.StatusCode)
	}
	if len(stub.reqs) != 0 {
		t.Errorf("unknown provider made %d upstream requests (SSRF leak); want 0", len(stub.reqs))
		if u := stub.reqs[0].URL.String(); strings.Contains(u, "evil-host") {
			t.Errorf("upstream URL leaked client value: %s", u)
		}
	}
}

// TestTileProxyRejectsBadCoords proves non-integer and out-of-range z/x/y are
// rejected (400) and make no upstream request. The validated integers are the
// ONLY values interpolated into the upstream template.
func TestTileProxyRejectsBadCoords(t *testing.T) {
	app, _, stub := newTileTestApp(t, nil)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct {
		name, path string
	}{
		{"non-int z", "/tiles/osm/abc/10/15"},
		{"non-int x", "/tiles/osm/5/xx/15"},
		{"non-int y", "/tiles/osm/5/10/yy"},
		{"z too big", "/tiles/osm/99/10/15"},
		{"x out of range", "/tiles/osm/3/9999/5"},
		{"y out of range", "/tiles/osm/3/5/9999"},
	}
	for _, c := range cases {
		stub.reqs = nil
		resp, err := http.Get(srv.URL + RoutePrefix + c.path)
		if err != nil {
			t.Fatalf("%s: GET: %v", c.name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status=%d body=%q want 400", c.name, resp.StatusCode, body)
		}
		if len(stub.reqs) != 0 {
			t.Errorf("%s: made %d upstream requests; want 0", c.name, len(stub.reqs))
		}
	}
}

// TestTileProxyValidatesCoordinatesAreOnlyUpstreamInput proves the validated
// integers are the only thing interpolated into the upstream URL: a request
// for /tiles/osm/5/10/15 reaches the upstream OSM template at exactly that
// z/x/y. The second hit is served from the bounded LRU cache (no new upstream).
func TestTileProxyValidatesCoordinatesAreOnlyUpstreamInput(t *testing.T) {
	app, _, stub := newTileTestApp(t, nil)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + RoutePrefix + "/tiles/osm/5/10/15")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type=%q want image/png", got)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "max-age=") {
		t.Errorf("Cache-Control=%q want max-age", got)
	}
	// CORP MUST be cross-origin: the tile is loaded by the OPAQUE-origin plugin
	// frame (a "null" origin), so the framework's default same-origin CORP would
	// make the browser BLOCK the <img> with ERR_BLOCKED_BY_RESPONSE.NotSameOrigin
	// — a gray, tile-less map. This asserts the header survives the full app
	// router / global middleware on the cache-MISS path.
	if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Errorf("MISS Cross-Origin-Resource-Policy=%q want cross-origin (opaque frame would be CORP-blocked)", got)
	}
	if len(stub.reqs) != 1 {
		t.Fatalf("upstream requests=%d want 1", len(stub.reqs))
	}
	const wantUpstream = "https://tile.openstreetmap.org/5/10/15.png"
	if got := stub.reqs[0].URL.String(); got != wantUpstream {
		t.Errorf("upstream URL=%q want %q", got, wantUpstream)
	}
	if ua := stub.reqs[0].Header.Get("User-Agent"); !strings.Contains(ua, "gofastr-plugins-geomap") {
		t.Errorf("User-Agent=%q must identify the proxy (OSM usage policy)", ua)
	}

	// A second request for the same tile hits the cache (no new upstream req).
	resp2, err := http.Get(srv.URL + RoutePrefix + "/tiles/osm/5/10/15")
	if err != nil {
		t.Fatalf("GET2: %v", err)
	}
	resp2.Body.Close()
	if len(stub.reqs) != 1 {
		t.Errorf("cached tile made another upstream request; want 1 total, got %d", len(stub.reqs))
	}
	if got := resp2.Header.Get("X-Geomap-Cache"); got != "HIT" {
		t.Errorf("X-Geomap-Cache=%q want HIT", got)
	}
	// The cache-HIT path must set CORP cross-origin too (same opaque-frame
	// reason as the MISS path above).
	if got := resp2.Header.Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Errorf("HIT Cross-Origin-Resource-Policy=%q want cross-origin", got)
	}
}

// TestTileCacheBound proves the LRU cache is bounded: inserting more than cap
// entries evicts the least-recently-used, so the cache can never grow
// unbounded under e2e / demo pan-and-zoom load.
func TestTileCacheBound(t *testing.T) {
	const cap = 4
	c := newTileCache(cap)
	total := cap * 3
	for i := range total {
		c.put(fmt.Sprintf("k%d", i), []byte{byte(i)}, "image/png")
		// put() must never exceed the cap.
		if c.Len() > cap {
			t.Fatalf("after put %d: Len=%d exceeds cap=%d", i, c.Len(), cap)
		}
	}
	if c.Len() != cap {
		t.Errorf("Len=%d want %d", c.Len(), cap)
	}
	// The last `cap` keys we inserted must be present; earlier ones evicted.
	for i := range total {
		_, _, ok := c.get(fmt.Sprintf("k%d", i))
		wantOK := i >= total-cap
		if ok != wantOK {
			t.Errorf("k%d: present=%v want %v", i, ok, wantOK)
		}
	}
}

// TestTileCacheLRUAccessPromotes proves a get() on an entry moves it to the
// head so it survives eviction over less-recently-touched entries.
func TestTileCacheLRUAccessPromotes(t *testing.T) {
	const cap = 3
	c := newTileCache(cap)
	c.put("a", []byte{1}, "image/png")
	c.put("b", []byte{2}, "image/png")
	c.put("c", []byte{3}, "image/png")
	// Touch "a"; then insert one more. "b" should be the one evicted.
	if _, _, ok := c.get("a"); !ok {
		t.Fatal("get(a) missing")
	}
	c.put("d", []byte{4}, "image/png")
	for _, k := range []string{"a", "c", "d"} {
		if _, _, ok := c.get(k); !ok {
			t.Errorf("%s should be resident after LRU promotion+insertion", k)
		}
	}
	if _, _, ok := c.get("b"); ok {
		t.Errorf("b should have been evicted (LRU)")
	}
}

// TestTileProxyConcurrency proves the cache + handler are safe under concurrent
// requests (the LRU mutex is the whole guarantee). Run with -race to catch any
// shared-state slip.
func TestTileProxyConcurrency(t *testing.T) {
	app, _, _ := newTileTestApp(t, nil)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	const workers = 16
	const per = 25
	var hits int64
	var errors int64
	done := make(chan struct{}, workers)
	for w := range workers {
		go func(seed int) {
			defer func() { done <- struct{}{} }()
		for i := range per {
			// z=3 → valid x,y are 0..7; stay in that range so the proxy never
			// 400s on out-of-range (we are testing concurrency, not validation).
			const z = 3
			x := (seed + i) % 8
			y := (seed*3 + i) % 8
			resp, err := http.Get(srv.URL + RoutePrefix + fmt.Sprintf("/tiles/osm/%d/%d/%d", z, x, y))
			if err != nil {
				atomic.AddInt64(&errors, 1)
				continue
			}
			if resp.StatusCode == http.StatusOK {
				atomic.AddInt64(&hits, 1)
			} else {
				atomic.AddInt64(&errors, 1)
			}
			resp.Body.Close()
		}
		}(w)
	}
	for range workers {
		<-done
	}
	if errors > 0 {
		t.Errorf("concurrent tile requests: %d errors / %d hits", errors, hits)
	}
}

// TestWithTileProvidersValidatesTemplates proves a bad template panics at
// construction (fail-loud) rather than 500ing on first request.
func TestWithTileProvidersValidatesTemplates(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on bad template, got none")
		}
	}()
	New(WithTileProviders(map[string]string{
		"broken": "https://example.com/no-placeholders",
	}))
}

// TestWithTileProvidersExtendsAllowlist proves a custom template reaches the
// frame's tile proxy and the upstream is fetched from the configured host.
func TestWithTileProvidersExtendsAllowlist(t *testing.T) {
	stub := &stubFetcher{}
	p := New(WithDevGrantAll(), WithTileProviders(map[string]string{
		"custom": "https://my-tiles.example.com/{z}/{x}/{y}.png",
	}), WithProvider("custom"))
	p.tileClient = stub
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "geomap-custom-tile"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + RoutePrefix + "/tiles/custom/4/8/6")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if len(stub.reqs) != 1 {
		t.Fatalf("upstream requests=%d want 1", len(stub.reqs))
	}
	if got := stub.reqs[0].URL.String(); got != "https://my-tiles.example.com/4/8/6.png" {
		t.Errorf("upstream URL=%q", got)
	}
}

// TestNewPanicsOnUnknownDefaultProvider proves the default-provider sanity
// check fails loud when a host wires a non-allowlisted provider.
func TestNewPanicsOnUnknownDefaultProvider(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on unknown default provider, got none")
		}
	}()
	New(WithProvider("does-not-exist"))
}
