package scanner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "scanner-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// fullTestApp wires the demo page so every route exists. There is no source
// option to wire — the scanner has no host data route at all; pixels arrive
// over the bridge from the privileged host adapter.
func fullTestApp(t *testing.T) (*framework.App, *Plugin) {
	t.Helper()
	return newTestApp(t, WithDevGrantAll(), WithDemoPage())
}

// demoPlugin is fullTestApp when only the rendered page is under test.
func demoPlugin(t *testing.T) *Plugin {
	t.Helper()
	_, p := fullTestApp(t)
	return p
}

// --- assets -----------------------------------------------------------------

func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{FrameHTMLURL, "text/html; charset=utf-8"},
		{ScanJSURL, "text/javascript; charset=utf-8"},
		{ScanCSSURL, "text/css; charset=utf-8"},
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
		if resp.Header.Get("Content-Type") != c.wantCT {
			t.Errorf("%s: content-type=%q want %q", c.path, resp.Header.Get("Content-Type"), c.wantCT)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%s", c.path, resp.StatusCode, body)
		}
	}
}

// TestFramedAssetsCarryHeaderRelaxation proves the platform AssetServer
// carries the framing/CORP/CSP relaxation that lets the host frame its OWN
// scanner document, AND that the fixed framedCSP is the isolation contract
// the cage depends on:
//
//   - connect-src 'none' — the decoder never fetches, so an exfiltration
//     path simply does not exist;
//   - sandbox allow-scripts — the document is sandboxes even on a top-level
//     load;
//   - script-src is the EXPLICIT request origin, never the 'self' keyword.
//     'self' matches the protected resource's origin, which for an
//     opaque-origin frame is null, NOT the host origin that served scan.js —
//     Safari follows the spec and refuses the frame's own script (measured;
//     Chrome loads it anyway, which is why the bug is engine-specific).
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	origin := strings.TrimSuffix(srv.URL, "/")
	if origin == "" {
		t.Fatalf("could not derive test origin from %q", srv.URL)
	}
	for _, path := range []string{FrameHTMLURL, ScanJSURL, ScanCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d", path, resp.StatusCode)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "connect-src 'none'") {
			t.Errorf("%s: framed CSP missing connect-src 'none': %q", path, csp)
		}
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: framed CSP missing sandbox allow-scripts: %q", path, csp)
		}
		if !strings.Contains(csp, "script-src "+origin) {
			t.Errorf("%s: framed CSP script-src must name the explicit origin %q: %q", path, origin, csp)
		}
		if strings.Contains(csp, "'self'") {
			t.Errorf("%s: framed CSP must never carry 'self' (opaque-origin frames resolve it to null; Safari blocks the frame's own script): %q", path, csp)
		}
		if resp.Header.Get("Cross-Origin-Resource-Policy") == "" {
			t.Errorf("%s: missing CORP relaxation", path)
		}
	}
}

// The demo route exists when asked for and serves as a top-level document.
// Content-agnostic on purpose: the demo page itself is scanner/demo.go,
// owned by the coordinator — this only pins the route the plugin registers.
func TestDemoRouteServes(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("demo status=%d body=%s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("demo content-type=%q", ct)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("demo CSP missing the plain app policy: %q", csp)
	}
}

// Without WithDemoPage the route must be GONE, not broken.
func TestDemoRouteAbsentWithoutOption(t *testing.T) {
	app, _ := newTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("demo status=%d, want 404 without WithDemoPage", resp.StatusCode)
	}
}

// --- config.js ----------------------------------------------------------------

// parseConfigScript extracts the JSON object config.js publishes. It is a
// standalone same-origin script of the exact shape
// `window.__gofastrScannerConfig = {...};\n`, so the assignment prefix and
// trailing semicolon are peeled off before decoding.
func parseConfigScript(t *testing.T, body string) frameConfig {
	t.Helper()
	const prefix = "window.__gofastrScannerConfig = "
	if !strings.HasPrefix(body, prefix) {
		t.Fatalf("config.js does not publish the config global: %q", body)
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(body, prefix), ";\n")
	var cfg frameConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		t.Fatalf("config.js body is not one JSON object: %v (%q)", err, trimmed)
	}
	return cfg
}

func TestConfigScriptCarriesConfiguredFormatsAndRate(t *testing.T) {
	app, _ := newTestApp(t,
		WithFormats("EAN_13", "QR_CODE", "QR_CODE"), // duplicate collapses
		WithScanRate(2),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + ConfigScriptURL)
	if err != nil {
		t.Fatalf("GET config.js: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	cfg := parseConfigScript(t, string(raw))
	want := []string{"EAN_13", "QR_CODE"} // sorted, deduped
	if strings.Join(cfg.Formats, ",") != strings.Join(want, ",") {
		t.Errorf("config.js formats=%v, want %v", cfg.Formats, want)
	}
	if cfg.ScanRateHz != 2 {
		t.Errorf("config.js scanRateHz=%d, want 2", cfg.ScanRateHz)
	}
}

// The no-option instance must publish a COMPLETE config, never an empty
// one: the frame should never have to guess a default the host already
// decided.
func TestConfigScriptDefaultsAreComplete(t *testing.T) {
	app, _ := newTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + ConfigScriptURL)
	if err != nil {
		t.Fatalf("GET config.js: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	cfg := parseConfigScript(t, string(raw))
	if len(cfg.Formats) == 0 {
		t.Error("config.js carried no formats")
	}
	if cfg.ScanRateHz != defaultScanRate {
		t.Errorf("config.js scanRateHz=%d, want default %d", cfg.ScanRateHz, defaultScanRate)
	}
	for _, f := range cfg.Formats {
		if !knownFormats[f] {
			t.Errorf("config.js advertises unknown format %q", f)
		}
	}
}

// --- the gate ----------------------------------------------------------------

// TestCapabilityGate proves both gate sides at the unit level: an enforcing
// plugin denies a token whose scopes lack the capability, a scoped token
// carrying it passes, the wildcard grammar implies it, and WithDevGrantAll
// short-circuits the gate. There is no host route wired to the gate today —
// the scanner's only crossings are bridge events — so this is the level the
// contract lives at.
func TestCapabilityGate(t *testing.T) {
	enforcing := New()
	granted := New(WithDevGrantAll())

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	allowedCtx := auth.WithTokenScopes(context.Background(), []string{"scan:decode"})

	deniedReq := httptest.NewRequest(http.MethodGet, FrameHTMLURL, nil).WithContext(deniedCtx)
	if enforcing.allow(deniedReq, CapScanDecode) {
		t.Error("enforcing plugin should DENY a non-granting token")
	}
	allowedReq := httptest.NewRequest(http.MethodGet, FrameHTMLURL, nil).WithContext(allowedCtx)
	if !enforcing.allow(allowedReq, CapScanDecode) {
		t.Error("enforcing plugin should ALLOW a scan:decode token")
	}
	wildReq := httptest.NewRequest(http.MethodGet, FrameHTMLURL, nil).
		WithContext(auth.WithTokenScopes(context.Background(), []string{"scan:*"}))
	if !enforcing.allow(wildReq, CapScanDecode) {
		t.Error("scan:* wildcard token should imply scan:decode under the scope grammar")
	}
	if !granted.allow(deniedReq, CapScanDecode) {
		t.Error("WithDevGrantAll should ALLOW regardless of token scopes")
	}
}

// --- construction ------------------------------------------------------------

// Fail loud at construction, not silently at mount: a typo'd format never
// decodes anything and an out-of-range rate is a pacing bug, so both panic.
func TestNewPanicsOnBadConfig(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		{"unknown format", []Option{WithFormats("QR_CODES")}, "unknown barcode format"},
		{"rate too low", []Option{WithScanRate(0 - 1)}, "scan rate out of range"},
		{"rate too high", []Option{WithScanRate(31)}, "scan rate out of range"},
	}
	for _, c := range cases {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s: New should panic", c.name)
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, c.want) {
					t.Errorf("%s: panic message = %q, want it to mention %q", c.name, msg, c.want)
				}
			}()
			New(c.opts...)
		}()
	}
}

// TestManifestInvariants pins the platform contract the registry tests also
// enforce from plugins.json: opaque sandbox, no allow-same-origin, schema,
// and the scanner's own shape (460px viewport, titled frame).
func TestManifestInvariants(t *testing.T) {
	_, p := fullTestApp(t)
	m := p.Manifest()
	if m.Isolation != pluginhost.IsolationSandboxOpaque {
		t.Fatalf("isolation=%q", m.Isolation)
	}
	if got := m.SandboxString(); got != "allow-scripts" {
		t.Fatalf("sandbox=%q, want allow-scripts only", got)
	}
	if m.Schema != SchemaVersion {
		t.Fatalf("schema=%q", m.Schema)
	}
	if m.Entry != FrameHTMLURL {
		t.Fatalf("entry=%q", m.Entry)
	}
	if m.MinHeight != "460px" {
		t.Fatalf("minHeight=%q, want 460px", m.MinHeight)
	}
	if m.Title != "Barcode scanner" {
		t.Fatalf("title=%q", m.Title)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := strings.Join(p.Capabilities(), ",")
	if got != "scan:decode,theme:read" {
		t.Fatalf("capabilities=%q, want scan:decode,theme:read", got)
	}
}

// The format vocabulary is the wire contract shared with the frame and the
// adapter's narrowing table; it must stay sorted, unique, and non-empty.
func TestSupportedFormatsVocabulary(t *testing.T) {
	formats := SupportedFormats()
	if len(formats) == 0 {
		t.Fatal("no supported formats")
	}
	seen := map[string]bool{}
	for i, f := range formats {
		if f == "" {
			t.Fatalf("formats[%d] is empty", i)
		}
		if seen[f] {
			t.Fatalf("duplicate format %q", f)
		}
		seen[f] = true
		if i > 0 && formats[i-1] > f {
			t.Fatalf("formats not sorted: %v", formats)
		}
	}
	if !seen["QR_CODE"] {
		t.Error("QR_CODE missing from the supported vocabulary")
	}
}

// The adapter is served verbatim from the embedded host/ directory; pin the
// surface the demo page and e2e suite drive, so a rename here fails Go-side
// rather than as a mystery undefined in the browser.
func TestAdapterExposesDemoSurface(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + AdapterScriptURL)
	if err != nil {
		t.Fatalf("GET adapter.js: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	js := string(raw)
	for _, want := range []string{
		"window.__gofastrScannerDemo",
		"startCamera",
		"stopCamera",
		"scanSample",
		"scanImageFile",
		"lastResult",
		"__scannerReady",
		"__scannerCameraState",
		"__scannerFramesSent",
		"__scannerStats",
		"__scannerLastResult",
		"scanFrame",
		"frameDone",
		"scanResult",
		"scanStats",
		"__gofastrScannerConfig",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("adapter.js missing %q", want)
		}
	}
	// The one-slot flow control is the bridge's load-bearing invariant; pin
	// the constant by name so it cannot be silently widened away.
	if !strings.Contains(js, "inFlight !== 0") {
		t.Error("adapter.js lost its one-frame-in-flight guard")
	}
	// Grayscale, not RGBA: the measured zxing failure mode lives here.
	if !strings.Contains(js, "GRAYSCALE LUMINANCE") {
		t.Error("adapter.js lost the grayscale-not-RGBA contract comment")
	}
	// The registered entry must be the route the Go side actually serves.
	u, err := url.Parse(FrameHTMLURL)
	if err != nil {
		t.Fatalf("parse entry: %v", err)
	}
	if !strings.Contains(js, `"`+u.Path+`"`) {
		t.Errorf("adapter.js does not reference the served entry %q", u.Path)
	}
}

// TestDemoPageStatesTheBundledDecoderVersion ties the version on the demo page
// to what the bundle actually pins. mermaid's page shipped a version twelve
// releases stale because nothing checks prose; every plugin here that names a
// library version now carries this guard.
func TestDemoPageStatesTheBundledDecoderVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("js", "package.json"))
	if err != nil {
		t.Fatalf("read js/package.json: %v", err)
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse js/package.json: %v", err)
	}
	pinned := pkg.Dependencies["@zxing/library"]
	if pinned == "" {
		pinned = pkg.DevDependencies["@zxing/library"]
	}
	if pinned == "" {
		t.Fatal("js/package.json declares no @zxing/library dependency")
	}
	if strings.ContainsAny(pinned, "^~*") {
		t.Fatalf("@zxing/library is pinned loosely (%q); the demo page cannot state a version the build does not guarantee", pinned)
	}
	if ZxingVersion != pinned {
		t.Errorf("ZxingVersion = %q but js/package.json pins @zxing/library at %q", ZxingVersion, pinned)
	}

	body := string(demoPlugin(t).renderDemo(httptest.NewRequest(http.MethodGet, DemoURL, nil)))
	if want := "zxing " + pinned; !strings.Contains(body, want) {
		t.Errorf("demo page never states %q", want)
	}
}

// TestDemoPageMountsInsideTheEditorCard guards the ORDER of the demo page's
// format arguments, which nothing else can see: pdf's page shipped with the
// mount one position early, rendering the iframe inside the fact-chip row while
// every test stayed green, because the frame works perfectly wherever it lands.
func TestDemoPageMountsInsideTheEditorCard(t *testing.T) {
	body := string(demoPlugin(t).renderDemo(httptest.NewRequest(http.MethodGet, DemoURL, nil)))

	marker := strings.Index(body, "data-fui-plugin=")
	if marker < 0 {
		t.Fatal("demo page has no mount marker at all")
	}
	chrome := strings.Index(body, `class="editor-chrome"`)
	if chrome < 0 {
		t.Fatal("demo page has no editor chrome; the mount is supposed to sit in a card with a title bar")
	}
	if marker < chrome {
		t.Errorf("the mount marker (offset %d) comes BEFORE the editor chrome (offset %d): the format "+
			"arguments are out of order and the frame is rendering somewhere other than its card", marker, chrome)
	}
	// Every control the e2e drives must exist, or the journeys fail with a
	// selector error that reads like a broken plugin rather than a moved id.
	for _, id := range []string{"scan-camera", "scan-sample", "scan-file", "scan-result", "scan-camera-state"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("demo page is missing #%s, which the e2e journeys drive", id)
		}
	}
}
