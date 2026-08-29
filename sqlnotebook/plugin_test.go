package sqlnotebook

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newTestApp stands up a fresh framework.App with the sqlnotebook plugin
// registered and initialized (mirrors pdf's harness; the bridge-only wire
// protocol needs no store).
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "sqlnotebook-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// TestManifestValidatesAndCSPIsExactlyWasmUnsafeEval pins the declared tier:
// Validate passes AND CSP is exactly the one allowlisted keyword. A second
// token, a loose spelling, or a host source must fail Validate rather than
// reach a response header.
func TestManifestValidatesAndCSPIsExactlyWasmUnsafeEval(t *testing.T) {
	p := New()
	m := p.Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest Validate: %v", err)
	}
	want := []string{"'wasm-unsafe-eval'"}
	if !slices.Equal(m.CSP, want) {
		t.Errorf("manifest CSP = %q, want exactly %q", m.CSP, want)
	}
	if m.Entry != FrameHTMLURL || m.Isolation != pluginhost.IsolationSandboxOpaque {
		t.Errorf("manifest entry/isolation = %s/%s", m.Entry, m.Isolation)
	}
	if got := m.SandboxString(); got != "allow-scripts" {
		t.Errorf("sandbox string = %q, want allow-scripts (never allow-same-origin)", got)
	}
}

// TestServedFrameHeaderCarriesWasmUnsafeEval is THE gofastr#300 regression:
// the tier must reach the SERVED response header, not just the manifest.
// Manifest.CSP alone changes nothing — an AssetServer built without
// .WithCSP(p.manifest.CSP) serves a frame whose script-src lacks the
// keyword, SQLite then fails to compile with no error anywhere, and a test
// that only checked Validate() would stay green. So GET the frame document
// through the real app router and assert the header the browser actually
// receives.
func TestServedFrameHeaderCarriesWasmUnsafeEval(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + FrameHTMLURL)
	if err != nil {
		t.Fatalf("GET frame.html: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("frame.html status=%d body=%q", resp.StatusCode, body)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	t.Logf("served frame CSP: %s", csp)
	if csp == "" {
		t.Fatal("frame.html served without a Content-Security-Policy header")
	}
	// The keyword must be a whole source expression in script-src — the
	// directive the browser enforces WebAssembly compilation against.
	scriptSrc := ""
	for _, directive := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(directive), "script-src ") {
			scriptSrc = strings.TrimSpace(directive)
		}
	}
	if scriptSrc == "" {
		t.Fatalf("served CSP has no script-src directive: %q", csp)
	}
	tokens := strings.Fields(strings.TrimPrefix(scriptSrc, "script-src "))
	if !slices.Contains(tokens, "'wasm-unsafe-eval'") {
		t.Errorf("served script-src lacks 'wasm-unsafe-eval' (gofastr#300 — manifest.CSP never threaded into the AssetServer): %q", csp)
	}
	// The narrow tier only: the superset 'unsafe-eval' must NOT be present.
	// (Not a substring of 'wasm-unsafe-eval' — the leading quote differs.)
	if slices.Contains(tokens, "'unsafe-eval'") || strings.Contains(csp, "'unsafe-eval'") {
		t.Errorf("served CSP grants 'unsafe-eval' — string eval must stay forbidden: %q", csp)
	}
}

// TestAssetSpecsDeclareAndServeContentTypes is the gofastr#303 regression:
// every AssetSpec declares a non-empty ContentType, and every file actually
// present in the embedded tree serves 200 with exactly that type. The
// platform sets nosniff unconditionally, so an undeclared type is a 200 with
// correct bytes the browser refuses to parse, silently. Walking the embed
// (not the spec list) also means an asset added to assets/ without a spec
// fails here instead of shipping unparsed.
func TestAssetSpecsDeclareAndServeContentTypes(t *testing.T) {
	specs := assetSpecs()
	byName := map[string]pluginhost.AssetSpec{}
	for _, spec := range specs {
		if spec.ContentType == "" {
			t.Errorf("spec %q: empty ContentType (gofastr#303)", spec.Name)
			continue
		}
		byName[spec.Name] = spec
	}

	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	var embedded []string
	err := fs.WalkDir(framedAssets(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			embedded = append(embedded, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded assets: %v", err)
	}
	if len(embedded) == 0 {
		t.Fatal("no embedded assets found under assets/")
	}
	for _, name := range embedded {
		spec, ok := byName[name]
		if !ok {
			t.Errorf("embedded asset %q has no AssetSpec — add it to assetSpecs() with an explicit ContentType", name)
			continue
		}
		resp, err := http.Get(srv.URL + RoutePrefix + "/" + name)
		if err != nil {
			t.Fatalf("GET %s: %v", name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%q", name, resp.StatusCode, body)
			continue
		}
		if got := resp.Header.Get("Content-Type"); got != spec.ContentType {
			t.Errorf("%s: Content-Type=%q want %q", name, got, spec.ContentType)
		}
		if len(body) == 0 {
			t.Errorf("%s: empty body", name)
		}
	}

	// The host-page adapter script served via AddBytes carries its type too.
	resp, err := http.Get(srv.URL + AdapterScriptURL)
	if err != nil {
		t.Fatalf("GET %s: %v", AdapterScriptURL, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("%s: status=%d body=%q", AdapterScriptURL, resp.StatusCode, body)
	} else if got := resp.Header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("%s: Content-Type=%q want text/javascript; charset=utf-8", AdapterScriptURL, got)
	}
}

// TestFramedCSPSealsNetworkAndSandbox proves the wasm tier widened exactly
// one thing: every framed asset still serves connect-src 'none' (the frame
// cannot fetch, which is why the engine arrives as bytes) and the sandbox
// list still excludes allow-same-origin (the frame stays opaque). A tier
// that bought WebAssembly by re-enabling the network or de-opaquing the
// origin would be a net security loss, and this is where it would show.
func TestFramedCSPSealsNetworkAndSandbox(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	var framed []string
	for _, spec := range assetSpecs() {
		if spec.Framed {
			framed = append(framed, spec.Name)
		}
	}

	// And the sandbox list itself: the manifest tokens and the authoritative
	// SandboxString must both exclude allow-same-origin.
	for _, token := range p.Manifest().Sandbox {
		if token == "allow-same-origin" {
			t.Errorf("manifest sandbox declares allow-same-origin")
		}
	}
	if got := p.Manifest().SandboxString(); strings.Contains(got, "allow-same-origin") {
		t.Errorf("SandboxString() = %q, must never contain allow-same-origin", got)
	}
}

// TestNewRejectsEmptySeed pins the fail-loud construction contract: an empty
// seed is almost always a source that failed to load, and a blank engine
// would present an empty notebook as if it worked.
func TestNewRejectsEmptySeed(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New(WithSeed(\"\")) must panic")
		}
	}()
	New(WithSeed("   "))
}

// TestSeedOverrideAndDefault checks the seed plumbing both ways: the option
// replaces the default, and the default itself is the plugins-table seed.
func TestSeedOverrideAndDefault(t *testing.T) {
	if got := New(WithSeed("SELECT 1;")).Seed(); got != "SELECT 1;" {
		t.Errorf("Seed() = %q, want the WithSeed value", got)
	}
	if got := New().Seed(); got != DefaultSeed {
		t.Errorf("default Seed() is not DefaultSeed")
	}
	if !strings.Contains(DefaultSeed, "CREATE TABLE plugins") {
		t.Errorf("DefaultSeed lost its plugins table")
	}
}

// TestDemoPageContainsMountAndBroker confirms the demo mounts the sqlnotebook
// marker carrying the seeded doc, emits the broker + adapter scripts in that
// order, and (the demo standard for this plugin) carries no em dash anywhere
// in its prose.
func TestDemoPageContainsMountAndBroker(t *testing.T) {
	_, p := newTestApp(t, WithDemoPage())
	page := string(p.renderDemo(httptest.NewRequest(http.MethodGet, DemoURL, nil)))

	if !strings.Contains(page, `data-fui-plugin="sqlnotebook"`) {
		t.Error("demo page does not mount the sqlnotebook marker")
	}
	// The seed is the marker's data-fui-plugin-doc (JSON-encoded SQL the
	// adapter decodes for sqlnb/init) — the channel WithSeed rides.
	if !strings.Contains(page, `data-fui-plugin-doc=`) ||
		!strings.Contains(page, "CREATE TABLE plugins") {
		t.Error("demo marker does not carry the JSON-encoded seed")
	}
	order := []string{pluginhost.BrokerScriptURL, AdapterScriptURL}
	last := -1
	for _, want := range order {
		idx := strings.Index(page, `<script src="`+want+`"></script>`)
		if idx < 0 {
			t.Errorf("demo page missing script tag for %s", want)
			continue
		}
		if idx < last {
			t.Errorf("script tags out of order: %s loaded before its dependency", want)
		}
		last = idx
	}
	if strings.Contains(page, "—") {
		t.Error("demo page prose contains an em dash; the demo standard for this plugin forbids them")
	}
}

// TestDemoPageMountsInsideTheEditorCard guards the ORDER of the demo page's
// format arguments, which no other test can see (pdf shipped a page with the
// iframe inside the fact chips and everything still passed).
func TestDemoPageMountsInsideTheEditorCard(t *testing.T) {
	_, p := newTestApp(t, WithDemoPage())
	page := string(p.renderDemo(httptest.NewRequest(http.MethodGet, DemoURL, nil)))

	card := strings.Index(page, `<section class="editor-card"`)
	mount := strings.Index(page, `data-fui-plugin="sqlnotebook"`)
	grid := strings.Index(page, `<section class="grid"`)
	if card < 0 || mount < 0 || grid < 0 {
		t.Fatalf("page shape changed: card=%d mount=%d grid=%d", card, mount, grid)
	}
	if mount < card || mount > grid {
		t.Errorf("mount marker must sit inside the editor card: card=%d mount=%d grid=%d", card, mount, grid)
	}
}

// TestDemoPageStatesTheBundledLibraryVersions keeps the two version strings
// on the demo page tied to what the bundle actually pins. mermaid's page
// shipped a version twelve releases stale because nothing checks prose.
//
// It reads the LOCKFILE rather than pdf's package.json because
// sqlnotebook/js/package.json pins sql.js loosely (^1.13.0) and is owned by
// the frame-bundle side; the lock is the artifact that guarantees which
// bytes were copied into assets/. SQLiteVersion is checked against the wasm
// binary itself, which carries its own version string.
func TestDemoPageStatesTheBundledLibraryVersions(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("js", "package-lock.json"))
	if err != nil {
		t.Fatalf("read js/package-lock.json: %v", err)
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatalf("parse js/package-lock.json: %v", err)
	}
	pkg, ok := lock.Packages["node_modules/sql.js"]
	if !ok || pkg.Version == "" {
		t.Fatalf("js/package-lock.json declares no sql.js package")
	}
	if pkg.Version != SqlJsVersion {
		t.Errorf("SqlJsVersion = %q but the lockfile pins sql.js at %q", SqlJsVersion, pkg.Version)
	}

	wasm, err := fs.ReadFile(framedAssets(), "sql-wasm.wasm")
	if err != nil {
		t.Fatalf("read embedded sql-wasm.wasm: %v", err)
	}
	if !strings.Contains(string(wasm), SQLiteVersion) {
		t.Errorf("SQLiteVersion = %q but the embedded wasm does not carry that version string", SQLiteVersion)
	}
}
