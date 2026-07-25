package geomap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newTestApp stands up a fresh framework.App with the plugin registered and
// initialized (mirrors monaco/mermaid/richtext/tour's harness; the in-memory
// store needs no DB).
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

// TestInitServesAssetsWithCorrectContentTypes verifies map.js and map.css are
// served with the right types, NON-framed headers, and that the body is exactly
// the embedded build bytes.
func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct {
		path, wantCT string
		want         []byte
	}{
		{MapJSURL, "text/javascript; charset=utf-8", mapJSBytes},
		{MapCSSURL, "text/css; charset=utf-8", mapCSSBytes},
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
		if !bytes.Equal(body, c.want) {
			t.Errorf("%s: body does not match the embedded bytes (got %d bytes, want %d)", c.path, len(body), len(c.want))
		}
	}
}

// TestAssetsAreNonFramed asserts map.js and map.css are served as NON-framed
// host-page assets: they MUST NOT carry the CORP cross-origin relaxation that
// framed (sandboxed-iframe) assets get (the old geomap build did). This is a
// trusted host-page plugin now, so its assets are same-origin and CSP-clean —
// the mirror of tour's TestAssetsAreNonFramed.
func TestAssetsAreNonFramed(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{MapJSURL, MapCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got == "cross-origin" {
			t.Errorf("%s: must NOT be CORP cross-origin (host-page asset)", path)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: must NOT carry the framed sandbox CSP: %q", path, csp)
		}
		if strings.Contains(csp, "frame-ancestors http") {
			t.Errorf("%s: must NOT carry the framed-ancestor CSP relaxation: %q", path, csp)
		}
	}
}

// TestDemoPageHostPageCSP proves the demo page responds 200 with the host-page
// CSP that lets MapLibre fetch OpenFreeMap vector tiles and spawn its blob
// worker — the whole reason this plugin left the opaque sandbox.
func TestDemoPageHostPageCSP(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("demo status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("demo Content-Type=%q", ct)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"https://tiles.openfreemap.org", "worker-src blob:", "connect-src 'self' https://tiles.openfreemap.org"} {
		if !strings.Contains(csp, want) {
			t.Errorf("demo CSP missing %q; got %q", want, csp)
		}
	}
}

// TestDemoPageWiresRuntime proves the demo page wires map.js + map.css and
// renders the inline host-page mount + the Tokyo card the flyTo e2e clicks.
func TestDemoPageWiresRuntime(t *testing.T) {
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
	for _, want := range []string{
		"--color-", // bridged theme tokens
		`<link rel="stylesheet" href="` + MapCSSURL, // overlay stylesheet
		`<script src="` + MapJSURL,                  // runtime script
		`data-color-scheme="light"`,                 // deterministic default scheme
		`data-fui-geomap`,                           // inline host-page mount element
		`data-label="Tokyo"`,                        // flyTo e2e target card
		`input[name="map_doc"]`,                     // canonical doc hidden field (e2e reads it)
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

// TestMountRendersHostPageMarker proves Mount renders the plain host-page mount
// (NOT the platform broker iframe marker), the hidden doc field, and that
// data-config carries the instance's configured style.
func TestMountRendersHostPageMarker(t *testing.T) {
	p := New(WithDevGrantAll(), WithStyle("positron"))
	out := string(p.Mount(MountConfig{}))
	for _, want := range []string{
		`data-fui-geomap`,
		`data-config="`,
		`&quot;style&quot;:&quot;positron&quot;`, // configured style survives into data-config (HTML-escaped)
		`data-save-url="/__gofastr/plugin/map/save"`,
		`input type="hidden" name="map_doc"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Mount output missing %q; got %s", want, out)
		}
	}
	// The sandboxed-iframe broker marker must NOT appear — this is a host-page plugin.
	for _, mustNot := range []string{`data-fui-plugin="map"`, `data-fui-plugin-docid`} {
		if strings.Contains(out, mustNot) {
			t.Errorf("Mount output must NOT contain the broker marker %q; got %s", mustNot, out)
		}
	}
}

// TestMountConfigCarriesControlFlags pins the data-config contract for the
// controls map.js renders from config alone: geolocate + scale default ON,
// clustering defaults OFF with its tuning values, and the opt-outs actually
// reach the wire. A silently-dropped flag here shows up as a missing control in
// the browser with nothing in any log.
func TestMountConfigCarriesControlFlags(t *testing.T) {
	defaults := string(New().Mount(MountConfig{}))
	for _, want := range []string{
		`&quot;geolocate&quot;:true`,
		`&quot;scale&quot;:true`,
		`&quot;cluster&quot;:false`,
		`&quot;clusterRadius&quot;:50`,
		`&quot;clusterMaxZoom&quot;:14`,
		`&quot;searchURL&quot;:&quot;&quot;`, // no search unless opted in
	} {
		if !strings.Contains(defaults, want) {
			t.Errorf("default Mount config missing %q; got %s", want, defaults)
		}
	}

	tuned := string(New(
		WithoutGeolocateControl(),
		WithoutScaleControl(),
		WithClustering(),
		WithClusterRadius(80),
		WithClusterMaxZoom(9),
	).Mount(MountConfig{}))
	for _, want := range []string{
		`&quot;geolocate&quot;:false`,
		`&quot;scale&quot;:false`,
		`&quot;cluster&quot;:true`,
		`&quot;clusterRadius&quot;:80`,
		`&quot;clusterMaxZoom&quot;:9`,
	} {
		if !strings.Contains(tuned, want) {
			t.Errorf("tuned Mount config missing %q; got %s", want, tuned)
		}
	}
}

// TestSaveRoundTripDevGrant proves the canonical {lat,lng,zoom,markers} doc
// round-trips through the in-memory store, AND asserts the wire format uses the
// lowercase json tags map.js reads. This is the exact regression that bit the
// monaco savedDoc: a Go↔Go round-trip passes even with capitalized struct keys.
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
	if !strings.Contains(docJSON, `"lat"`) || !strings.Contains(docJSON, `"lng"`) ||
		!strings.Contains(docJSON, `"zoom"`) || !strings.Contains(docJSON, `"markers"`) {
		t.Errorf("LoadDoc JSON must use lowercase lat/lng/zoom/markers keys; got %q", docJSON)
	}
	if strings.Contains(docJSON, `"Lat"`) || strings.Contains(docJSON, `"Lng"`) ||
		strings.Contains(docJSON, `"Zoom"`) || strings.Contains(docJSON, `"Markers"`) {
		t.Errorf("LoadDoc JSON leaks capitalized struct keys; got %q", docJSON)
	}
}

// TestSaveConflictMapsTo409 mirrors monaco: a save handler returning ErrConflict
// (bare or wrapped) surfaces as 409/E_CONFLICT; any other error stays 500/E_SAVE.
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

// TestNewRejectsBadStyleConfig proves the New() style sanity checks fail loud at
// construction: an empty Style or an empty style-switcher entry panics rather
// than shipping a map that can never resolve a style.
func TestNewRejectsBadStyleConfig(t *testing.T) {
	t.Run("empty style", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic on empty Style, got none")
			}
		}()
		New(WithMapConfig(MapConfig{Style: "", Styles: []string{"liberty"}}))
	})
	t.Run("empty style-switcher entry", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic on empty Styles entry, got none")
			}
		}()
		New(WithStyles("liberty", ""))
	})
}

// TestUIHostOption sanity-checks UIHostOption is callable and yields a non-nil
// option. It is a thin wrapper around uihost.WithExtraScripts(MapJSURL); the
// framework's own uihost_test proves WithExtraScripts injects <script src> tags
// before </body>, so asserting the exact URL here would duplicate that harness.
func TestUIHostOption(t *testing.T) {
	opt := UIHostOption()
	if opt == nil {
		t.Fatal("UIHostOption returned nil")
	}
}
