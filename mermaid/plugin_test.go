package mermaid

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
// initialized (mirrors wysiwyg's test harness; the in-memory store needs no DB).
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "mermaid-test"}))
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
		{DiagramHTMLURL, "text/html; charset=utf-8"},
		{DiagramJSURL, "text/javascript; charset=utf-8"},
		{DiagramCSSURL, "text/css; charset=utf-8"},
		{AdapterScriptURL, "text/javascript; charset=utf-8"},
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
// the framing/CORP/CSP relaxation that lets the host frame its OWN diagram
// document and lets the opaque frame fetch its JS/CSS (DECISIONS.md Phase-0
// gotcha #1).
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{DiagramHTMLURL, DiagramJSURL, DiagramCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		// Framed CSP uses the explicit origin, not 'self' (null for the opaque frame).
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

	// The host-page adapter is a NON-framed script: it must NOT carry the CORP
	// cross-origin relaxation (same-origin fetch by the host page).
	resp, err := http.Get(srv.URL + AdapterScriptURL)
	if err != nil {
		t.Fatalf("GET adapter: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got == "cross-origin" {
		t.Error("host-page adapter must NOT be CORP cross-origin")
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
		`data-fui-plugin="mermaid"`,                  // generic mount marker
		`data-fui-plugin-for="diagram_source"`,       // mermaid-specific hidden-field wiring
		`<script src="` + pluginhost.BrokerScriptURL, // generic platform broker (loaded first)
		`<script src="` + AdapterScriptURL,           // mermaid adapter (loaded second)
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

	source := "graph TD\n    A --> B"
	// Build the payload with the JSON encoder so the newline in `source` is
	// escaped correctly (a raw newline inside a JSON string is invalid JSON).
	payload, err := json.Marshal(map[string]any{
		"docId":         "demo",
		"doc":           map[string]string{"source": source},
		"source":        source,
		"schemaVersion": "mermaid-v1",
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

	got, ok := p.LoadDoc(context.Background(), "demo")
	if !ok {
		t.Fatal("LoadDoc: doc missing after save")
	}
	if got != source {
		t.Errorf("LoadDoc=%q want %q", got, source)
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
	if !granted.allow(deniedReq, "document:write") {
		t.Error("WithDevGrantAll should ALLOW regardless of token scopes")
	}
}

// TestSaveDeniedWithoutCapability proves the gate is wired into the route: a
// token-authenticated request whose scopes lack document:write is rejected
// with 403 + E_CAPABILITY_DENIED.
func TestSaveDeniedWithoutCapability(t *testing.T) {
	p := New() // enforcing (no devGrantAll)

	payload := `{"docId":"demo","doc":null,"source":"","schemaVersion":"mermaid-v1"}`
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
