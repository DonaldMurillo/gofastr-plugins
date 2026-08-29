package monaco

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newTestApp stands up a fresh framework.App with the plugin registered and
// initialized (mirrors mermaid/richtext's harness; the in-memory store needs no
// DB). Build with GOWORK=off against the pinned gofastr module.
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "monaco-test"}))
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
// the framing/CORP/CSP relaxation that lets the host frame its OWN editor
// document and lets the opaque frame fetch its JS/CSS (DECISIONS.md Phase-0
// gotcha #1).
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
	}

	// The host-page adapter and config script are NON-framed scripts: they must
	// NOT carry the CORP cross-origin relaxation (same-origin fetch by the host).
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
		`data-fui-plugin="monaco"`,                   // generic mount marker (dispatches to monaco adapter)
		`data-fui-plugin-for="code,language"`,        // monaco-specific hidden-field wiring
		`<script src="` + pluginhost.BrokerScriptURL, // generic platform broker (loaded first)
		`<script src="` + ConfigScriptURL,            // monaco config script (Go options -> frame)
		`<script src="` + AdapterScriptURL,           // monaco adapter (loaded last)
		`data-color-scheme="light"`,                  // deterministic default scheme
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

// TestConfigScriptReflectsOptions proves Go With* options reach the frame: the
// host-page config.js the plugin serves is marshaled from the instance's
// EditorConfig, so the adapter (and thus init.config) carries the compiled-in
// defaults.
func TestConfigScriptReflectsOptions(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithLanguage("go"), WithMinimap(), WithFontSize(18))
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
		"window.__gofastrMonacoConfig = ",
		`"language":"go"`,
		`"minimap":true`,
		`"fontSize":18`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.js missing %q; body=%q", want, got)
		}
	}
}

func TestSaveRoundTripDevGrant(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	code := "package main\n\nfunc main() { println(\"hi\") }"
	payload, err := json.Marshal(map[string]any{
		"docId":         "demo",
		"doc":           map[string]string{"code": code, "language": "go"},
		"code":          code,
		"language":      "go",
		"schemaVersion": "monaco-v1",
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
	var got savedDoc
	if err := json.Unmarshal([]byte(docJSON), &got); err != nil {
		t.Fatalf("LoadDoc returned non-canonical JSON: %v (%q)", err, docJSON)
	}
	if got.Code != code || got.Language != "go" {
		t.Errorf("LoadDoc={code:%q language:%q} want code:%q language:go", got.Code, got.Language, code)
	}
	// The WIRE format must be lowercase {code, language}: LoadDoc's JSON goes into
	// the mount marker and the frame's deriveDoc reads o.code / o.language. A
	// Go↔Go round-trip (above) passes even with capitalized struct keys, so assert
	// the wire shape explicitly — untagged fields marshal {Code, Language} and the
	// editor silently mounts EMPTY on load (regression caught by the monaco e2e).
	if !strings.Contains(docJSON, `"code"`) || !strings.Contains(docJSON, `"language"`) {
		t.Errorf("LoadDoc JSON must use lowercase code/language keys (frame contract); got %q", docJSON)
	}
	if strings.Contains(docJSON, `"Code"`) || strings.Contains(docJSON, `"Language"`) {
		t.Errorf("LoadDoc JSON leaks capitalized struct keys; the frame reads lowercase; got %q", docJSON)
	}
}

// TestSaveConflictMapsTo409 mirrors richtext's TestSaveConflictMapsTo409: a save
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

		payload := `{"docId":"demo","doc":{"code":"x","language":"plaintext"},"code":"x","language":"plaintext","schemaVersion":"monaco-v1"}`
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
		wrapped := fmt.Errorf("doc %q changed under the editor: %w", "demo", ErrConflict)
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

	payload := `{"docId":"demo","doc":null,"code":"","language":"","schemaVersion":"monaco-v1"}`
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

// The demo page states the bundled monaco version as prose, so nothing notices
// when a dependency bump leaves it behind. Read the version out of the bundle's
// package.json and require the page to agree. (mermaid carries the same guard;
// its page shipped a stale version for exactly this reason.)
func TestDemoPageStatesTheBundledMonacoVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("js", "package.json"))
	if err != nil {
		t.Fatalf("read js/package.json: %v", err)
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse js/package.json: %v", err)
	}
	version := pkg.Dependencies["monaco-editor"]
	if version == "" {
		t.Fatal("js/package.json declares no monaco-editor dependency")
	}
	if strings.ContainsAny(version, "^~*") {
		t.Fatalf("monaco-editor is pinned loosely (%q); the demo page cannot state a version the build does not guarantee", version)
	}

	_, p := newTestApp(t)
	body := string(p.renderDemo(httptest.NewRequest(http.MethodGet, "/monaco", nil)))
	if want := "monaco " + version; !strings.Contains(body, want) {
		var stale []string
		for _, m := range regexp.MustCompile(`monaco \d+\.\d+\.\d+[\w.-]*`).FindAllString(body, -1) {
			if m != "monaco "+Version {
				stale = append(stale, m)
			}
		}
		t.Errorf("demo page never names %q\nstale library versions still on the page: %v", want, stale)
	}
}
