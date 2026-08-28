package chart

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
// initialized (same harness shape as mermaid's; the in-memory store needs
// no DB).
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "chart-test"}))
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
		{ChartHTMLURL, "text/html; charset=utf-8"},
		{ChartJSURL, "text/javascript; charset=utf-8"},
		{ChartCSSURL, "text/css; charset=utf-8"},
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

// TestFramedAssetsCarryHeaderRelaxation proves the framed chart assets keep
// the platform's frame-ancestors/CORP relaxation (the Safari-follows-spec
// CSP gotcha this repo has been burned by before).
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{ChartHTMLURL, ChartJSURL, ChartCSSURL} {
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

	// The host-page adapter is a NON-framed script: no CORP relaxation.
	resp, err := http.Get(srv.URL + AdapterScriptURL)
	if err != nil {
		t.Fatalf("GET adapter: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got == "cross-origin" {
		t.Error("host-page adapter must NOT be CORP cross-origin")
	}
}

// TestDemoPageContainsSSRAndMount pins the demo surface: the SSR chart is
// server-rendered INTO the page (not fetched), the mount marker and hidden
// field follow it, and the broker + adapter load in order.
func TestDemoPageContainsSSRAndMount(t *testing.T) {
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
		"--color-",                  // bridged theme tokens
		`data-fui-chart-ssr`,        // the SSR wrapper
		`class="gofastr-chart-svg"`, // server-rendered SVG
		`data-axis="x"`,             // SSR agreement hooks
		`data-axis="y"`,
		`data-fui-plugin="chart"`,                    // generic mount marker
		`data-fui-plugin-for="chart_spec"`,           // hidden-field wiring
		`name="chart_spec"`,                          // the hidden input itself
		`<script src="` + pluginhost.BrokerScriptURL, // generic broker first
		`<script src="` + AdapterScriptURL,           // chart adapter second
		`data-color-scheme="light"`,                  // deterministic default scheme
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
	// The SSR wrapper must precede the mount marker — the adapter hides
	// marker.previousElementSibling when the frame is ready.
	if i, j := strings.Index(page, `data-fui-chart-ssr`), strings.Index(page, `data-fui-plugin="chart"`); i > j {
		t.Errorf("SSR wrapper at %d must precede mount marker at %d", i, j)
	}
}

// TestMountSSREscapesHostileSpec proves the whole Mount path is
// injection-safe: a series name carrying <script> and quotes lands in the
// SSR SVG text, the aria-label, and the marker's doc attribute — all
// escaped.
func TestMountSSREscapesHostileSpec(t *testing.T) {
	spec := `{"schemaVersion":"chart-v1","type":"scatter","title":"T<ta> & \"q\"","series":[` +
		`{"name":"<script>alert('x')</script> \"n\"","points":[{"x":0,"y":0},{"x":1,"y":1}]}]}`
	out := string(Mount(MountConfig{DocID: "demo", Spec: []byte(spec)}))

	for _, needle := range []string{"<script", `="` + "<script"} {
		if strings.Contains(out, needle) {
			t.Errorf("unescaped %q leaked into Mount output", needle)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;", "&quot;"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected escaped entity %s in Mount output", want)
		}
	}
	if !strings.Contains(out, `data-fui-chart-ssr`) || !strings.Contains(out, `data-fui-plugin="chart"`) {
		t.Error("Mount output missing wrapper or marker")
	}
}

func TestSaveRoundTripDevGrant(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	spec := map[string]any{
		"schemaVersion": "chart-v1",
		"type":          "bar",
		"title":         "Round trip",
		"series":        []any{map[string]any{"name": "s", "points": []any{map[string]any{"x": 0, "y": 2}, map[string]any{"x": 1, "y": 5}}}},
	}
	payload, err := json.Marshal(map[string]any{"docId": "demo", "doc": spec, "schemaVersion": "chart-v1"})
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
	var back struct {
		SchemaVersion string `json:"schemaVersion"`
		Type          string `json:"type"`
	}
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("saved doc is not JSON: %v", err)
	}
	if back.SchemaVersion != "chart-v1" || back.Type != "bar" {
		t.Errorf("saved doc = %+v", back)
	}
	// The saved doc must render through Mount without hitting the error
	// branch (that is the point of storing the normalized form).
	mount := string(Mount(MountConfig{Spec: got}))
	if strings.Contains(mount, "gofastr-chart-error") {
		t.Error("saved doc fails to SSR-render")
	}
}

// TestSaveRejectsInvalidSpec: the write path validates — an unknown chart
// type must be rejected 400/E_BAD_SPEC, not stored.
func TestSaveRejectsInvalidSpec(t *testing.T) {
	p := New(WithDevGrantAll())
	payload := `{"docId":"demo","doc":{"type":"pie","series":[{"name":"x","points":[{"x":0,"y":0}]}]},"schemaVersion":"chart-v1"}`
	req := httptest.NewRequest(http.MethodPost, SaveURL, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	p.handleSave(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "E_BAD_SPEC" {
		t.Errorf("error=%q want E_BAD_SPEC", body.Error)
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

// TestSaveDeniedWithoutCapability proves the gate is wired into the route.
func TestSaveDeniedWithoutCapability(t *testing.T) {
	p := New() // enforcing

	payload := `{"docId":"demo","doc":null,"schemaVersion":"chart-v1"}`
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

// TestMountInvalidSpecDoesNotBreakThePage: a garbage saved spec renders a
// visible error note (escaped), not a panic or a blank mount.
func TestMountInvalidSpecDoesNotBreakThePage(t *testing.T) {
	out := string(Mount(MountConfig{Spec: []byte(`{"type":"nope"}`)}))
	if !strings.Contains(out, "gofastr-chart-error") {
		t.Errorf("invalid spec should render the error note, got: %s", out)
	}
	if strings.Contains(out, "<script") {
		t.Error("error note leaked markup")
	}
	empty := string(Mount(MountConfig{}))
	if !strings.Contains(empty, "gofastr-chart-empty") {
		t.Error("empty spec should render the empty note")
	}
}
