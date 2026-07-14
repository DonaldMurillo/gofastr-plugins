package wysiwyg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newTestApp stands up a fresh framework.App with the plugin registered and
// initialized (no DB needed — the module store falls back to in-memory).
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "wysiwyg-test"}))
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
		{EditorHTMLURL, "text/html; charset=utf-8"},
		{EditorJSURL, "text/javascript; charset=utf-8"},
		{EditorCSSURL, "text/css; charset=utf-8"},
		{BrokerScriptURL, "text/javascript; charset=utf-8"},
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

// TestFramedAssetsCarryHeaderRelaxation proves the platform AssetServer (now
// serving wysiwyg's framed assets) carries the framing/CORP/CSP relaxation
// that lets the host frame its OWN editor and lets the opaque frame fetch its
// JS/CSS — the client-side isolation contract (DECISIONS.md Phase-0 gotcha #1).
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{EditorHTMLURL, EditorJSURL, EditorCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		// Framed CSP keys sub-resource loading to the explicit origin, not 'self'
		// (which is null for the opaque frame — Safari refuses the frame's own JS/CSS).
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
		// Framing is permitted by CSP frame-ancestors 'self' SUPERSEDING the
		// X-Frame-Options:DENY the global middleware emits (DECISIONS.md Phase-0
		// gotcha #1) — the load-bearing guarantee asserted above. XFO itself is
		// not removed (a buffering middleware re-emits it), so it is not asserted.
	}

	// The host-page broker adapter is a NON-framed script: it must NOT carry the
	// CORP cross-origin relaxation (same-origin fetch by the host page).
	resp, err := http.Get(srv.URL + BrokerScriptURL)
	if err != nil {
		t.Fatalf("GET broker: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got == "cross-origin" {
		t.Error("host-page broker must NOT be CORP cross-origin")
	}

	// The generic platform broker route is served by pluginhost.RegisterBrokerRoute.
	resp2, err := http.Get(srv.URL + pluginhost.BrokerScriptURL)
	if err != nil {
		t.Fatalf("GET pluginhost broker: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("pluginhost broker status=%d", resp2.StatusCode)
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
		"--color-",                                   // bridged theme tokens
		`data-fui-plugin="wysiwyg"`,                  // generic mount marker (dispatches to wysiwyg adapter)
		`data-fui-plugin-for="body_json,body_md"`,    // wysiwyg-specific hidden-field wiring
		`<script src="` + pluginhost.BrokerScriptURL, // generic platform broker (loaded first)
		`<script src="` + BrokerScriptURL,            // wysiwyg adapter (loaded second)
		`data-color-scheme="light"`,                  // deterministic default scheme
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

func TestSaveRoundTripDevGrant(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	doc := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hi"}]}]}`
	payload := `{"docId":"demo","doc":` + doc + `,"markdown":"hi","schemaVersion":"wysiwyg-v1"}`

	resp, err := http.Post(srv.URL+SaveURL, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d", resp.StatusCode)
	}

	got, ok := p.LoadDoc(context.Background(), "demo")
	if !ok {
		t.Fatal("LoadDoc: doc missing after save")
	}
	if got != doc {
		t.Errorf("LoadDoc=%q want %q", got, doc)
	}
}

func TestUploadReturnsDataUrl(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+UploadURL, strings.NewReader(string(png)))
	req.Header.Set("X-Upload-Name", "tiny.png")
	req.Header.Set("X-Upload-Type", "image/png")
	req.Header.Set("Content-Type", "image/png")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status=%d", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode upload resp: %v", err)
	}
	if !strings.HasPrefix(out.URL, "data:image/png;base64,") {
		t.Errorf("upload url=%q", out.URL)
	}
}

// TestCapabilityGate proves the auth.HasScope reuse: a token-authenticated
// request whose scopes do not grant the capability is denied, while
// WithDevGrantAll short-circuits the gate.
func TestCapabilityGate(t *testing.T) {
	enforcing := New() // no WithDevGrantAll
	granted := New(WithDevGrantAll())

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	deniedReq := httptest.NewRequest(http.MethodPost, SaveURL, nil).WithContext(deniedCtx)

	if enforcing.allow(deniedReq, "document:write") {
		t.Error("enforcing plugin should DENY a non-granting token")
	}
	if enforcing.allow(deniedReq, "upload:images") {
		t.Error("enforcing plugin should DENY upload for a non-granting token")
	}
	if !granted.allow(deniedReq, "document:write") {
		t.Error("WithDevGrantAll should ALLOW regardless of token scopes")
	}
}

// TestSaveDeniedWithoutCapability proves the gate is wired into the route: a
// token-authenticated request whose scopes lack document:write is rejected
// with 403 + E_CAPABILITY_DENIED. The handler is invoked directly (with a
// scoped context) because a client-side context would not survive the wire.
func TestSaveDeniedWithoutCapability(t *testing.T) {
	p := New() // enforcing (no devGrantAll)

	payload := `{"docId":"demo","doc":null,"markdown":"","schemaVersion":"wysiwyg-v1"}`
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
