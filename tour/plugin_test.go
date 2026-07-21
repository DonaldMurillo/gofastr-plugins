package tour

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
// initialized. Mirrors the mermaid/richtext harness; the in-memory seen store
// needs no DB.
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	opts = append([]Option{WithTour("welcome", []Step{
		{Selector: "#a", Title: "A", Body: "a body", Placement: PlacementAuto},
		{Selector: "#b", Title: "B", Body: "b body", Placement: PlacementTop},
	})}, opts...)
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "tour-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

func TestGetTourReturnsRegisteredSteps(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + ToursBaseURL + "/welcome")
	if err != nil {
		t.Fatalf("GET tour: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type=%q want application/json", ct)
	}
	var got Tour
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, body)
	}
	if got.ID != "welcome" {
		t.Errorf("id=%q want welcome", got.ID)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps=%d want 2", len(got.Steps))
	}
	if got.Steps[0].Selector != "#a" || got.Steps[1].Placement != PlacementTop {
		t.Errorf("unexpected steps: %+v", got.Steps)
	}
}

// Per-step actions (reveal buried targets), custom HTML content, and tour-level
// options must round-trip through GET /tours/{id} so the runtime receives them.
func TestActionsOptionsAndContentRoundTrip(t *testing.T) {
	steps := []Step{{
		Selector:  "#buried",
		Title:     "t",
		Body:      "b",
		Placement: PlacementTop,
		Before:    []Action{{Type: "click", Selector: "#open"}, {Type: "wait", Selector: "#buried"}},
		After:     []Action{{Type: "click", Selector: "#open"}},
		HTML:      "<b>hi</b>",
		ClassName: "step-x",
	}}
	app, _ := newTestApp(t,
		WithDevGrantAll(),
		WithTour("adv", steps),
		WithTourOptions("adv", TourOptions{Accent: "#7c3aed", ClassName: "tour-x"}),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + ToursBaseURL + "/adv")
	if err != nil {
		t.Fatalf("GET tour: %v", err)
	}
	defer resp.Body.Close()
	var got Tour
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Steps) != 1 {
		t.Fatalf("steps=%d want 1", len(got.Steps))
	}
	s := got.Steps[0]
	if len(s.Before) != 2 || s.Before[0].Type != "click" || s.Before[0].Selector != "#open" {
		t.Errorf("before actions did not round-trip: %+v", s.Before)
	}
	if len(s.After) != 1 || s.After[0].Type != "click" {
		t.Errorf("after actions did not round-trip: %+v", s.After)
	}
	if s.HTML != "<b>hi</b>" || s.ClassName != "step-x" {
		t.Errorf("custom content did not round-trip: html=%q class=%q", s.HTML, s.ClassName)
	}
	if got.Options == nil || got.Options.Accent != "#7c3aed" || got.Options.ClassName != "tour-x" {
		t.Errorf("options did not round-trip: %+v", got.Options)
	}
	// Order independence: WithTour after WithTourOptions must keep the options.
	p2 := New(WithTourOptions("z", TourOptions{Accent: "#111"}), WithTour("z", []Step{{Selector: "#a", Title: "a", Body: "b"}}))
	tz, ok := p2.Tour("z")
	if !ok || tz.Options == nil || tz.Options.Accent != "#111" || len(tz.Steps) != 1 {
		t.Errorf("WithTour after WithTourOptions lost data: %+v", tz)
	}
}

func TestGetTourUnknownReturns404(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + ToursBaseURL + "/does-not-exist")
	if err != nil {
		t.Fatalf("GET tour: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "E_NOT_FOUND" {
		t.Errorf("error=%q want E_NOT_FOUND", body.Error)
	}
}

func TestSeenRoundTrip(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// Before: not seen.
	resp, err := http.Get(srv.URL + SeenURL + "?tourId=welcome")
	if err != nil {
		t.Fatalf("GET seen: %v", err)
	}
	var pre struct {
		Seen bool `json:"seen"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pre); err != nil {
		t.Fatalf("decode pre: %v", err)
	}
	resp.Body.Close()
	if pre.Seen {
		t.Fatal("tour reported seen before marking")
	}

	// Mark seen.
	markResp, err := http.Post(srv.URL+SeenURL, "application/json", strings.NewReader(`{"tourId":"welcome"}`))
	if err != nil {
		t.Fatalf("POST seen: %v", err)
	}
	markResp.Body.Close()
	if markResp.StatusCode != http.StatusOK {
		t.Fatalf("mark status=%d", markResp.StatusCode)
	}

	// After: seen.
	afterResp, err := http.Get(srv.URL + SeenURL + "?tourId=welcome")
	if err != nil {
		t.Fatalf("GET seen after: %v", err)
	}
	var post struct {
		Seen bool `json:"seen"`
	}
	if err := json.NewDecoder(afterResp.Body).Decode(&post); err != nil {
		t.Fatalf("decode post: %v", err)
	}
	afterResp.Body.Close()
	if !post.Seen {
		t.Error("tour not seen after POST /seen")
	}

	// The in-memory store should agree.
	storeSeen, err := p.seen.IsSeen(context.Background(), "welcome")
	if err != nil {
		t.Fatalf("store.IsSeen: %v", err)
	}
	if !storeSeen {
		t.Error("in-memory seen store did not record completion")
	}
}

// TestSeenDeniedWithoutCapability proves the gate is wired into POST /seen: a
// token-authenticated request whose scopes lack tour:write is rejected with
// 403 + E_CAPABILITY_DENIED (mirror of mermaid's TestSaveDeniedWithoutCapability).
func TestSeenDeniedWithoutCapability(t *testing.T) {
	p := New(WithTour("gated", []Step{{Selector: "#x", Title: "X", Body: "y"}}))

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	req := httptest.NewRequest(http.MethodPost, SeenURL, strings.NewReader(`{"tourId":"gated"}`)).WithContext(deniedCtx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	p.handleMarkSeen(rr, req)

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

// TestCapabilityGate proves the auth.HasScope reuse: a token-authenticated
// request whose scopes do not grant the capability is denied, while
// WithDevGrantAll short-circuits the gate. Mirrors mermaid's TestCapabilityGate.
func TestCapabilityGate(t *testing.T) {
	enforcing := New(WithTour("g", []Step{{Selector: "#x", Title: "X", Body: "y"}}))
	granted := New(WithDevGrantAll(), WithTour("g", []Step{{Selector: "#x", Title: "X", Body: "y"}}))

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	deniedReq := httptest.NewRequest(http.MethodGet, ToursBaseURL+"/g", nil).WithContext(deniedCtx)

	if enforcing.allow(deniedReq, "tour:read") {
		t.Error("enforcing plugin should DENY a non-granting token")
	}
	if !granted.allow(deniedReq, "tour:read") {
		t.Error("WithDevGrantAll should ALLOW regardless of token scopes")
	}
}

// TestInitServesAssetsWithCorrectContentTypes verifies tour.js and tour.css
// are served with the right types and NON-framed headers (no CORP relaxation,
// no framing-CSP) — this is a trusted host-page plugin, not a sandboxed one.
func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{TourJSURL, "text/javascript; charset=utf-8"},
		{TourCSSURL, "text/css; charset=utf-8"},
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

// TestAssetsAreNonFramed asserts tour.js and tour.css are served as NON-framed
// host-page assets: they MUST NOT carry the CORP cross-origin relaxation that
// framed (sandboxed-iframe) assets get (see richtext's
// TestFramedAssetsCarryHeaderRelaxation for the framed counterpart). The tour
// is a trusted host-page plugin, so its assets are same-origin and CSP-clean.
func TestAssetsAreNonFramed(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{TourJSURL, TourCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got == "cross-origin" {
			t.Errorf("%s: must NOT be CORP cross-origin (host-page asset)", path)
		}
		// The framed-asset CSP keys sub-resource loading to the explicit origin
		// and includes a sandbox directive; non-framed assets are served plain.
		csp := resp.Header.Get("Content-Security-Policy")
		if strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: must NOT carry the framed sandbox CSP: %q", path, csp)
		}
		if strings.Contains(csp, "frame-ancestors http") {
			t.Errorf("%s: must NOT carry the framed-ancestor CSP relaxation: %q", path, csp)
		}
	}
}

// TestDemoPageLoadsRuntime proves the demo page wires the runtime script and
// references the bridged theme tokens.
func TestDemoPageLoadsRuntime(t *testing.T) {
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
	for _, want := range []string{
		"--color-",                   // bridged theme tokens
		`<script src="` + TourJSURL,  // runtime script
		`data-color-scheme="light"`,  // deterministic default scheme
		`id="demo-title"`,            // demo tour target element
		`window.gofastrTour.autoRun`, // demo trigger
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

// TestEmptyTourIsRejected asserts WithTour(id, []) fails loudly at Init time
// rather than rendering a step-less walk-through at runtime. The framework
// recovers panics from plugin Init and converts them to errors, so the test
// asserts the returned error rather than recovering a panic itself.
func TestEmptyTourIsRejected(t *testing.T) {
	p := New(WithTour("empty", nil))
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "tour-test"}))
	app.RegisterPlugin(p)
	err := app.InitPlugins()
	if err == nil {
		t.Fatal("expected InitPlugins to reject a step-less tour")
	}
	// Framework wraps the panic as "plugin init panicked (panic type string)";
	// the test only needs to assert Init failed loudly.
}

// TestRegisterBrokerRouteInteractions sanity-checks UIHostOption is callable
// and yields a non-nil option (it is a thin wrapper around WithExtraScripts).
func TestUIHostOption(t *testing.T) {
	opt := UIHostOption()
	if opt == nil {
		t.Fatal("UIHostOption returned nil")
	}
	// Smoke-test: applying it to a UIHost is exercised by the framework; here
	// we only assert the option exists.
	_ = pluginhost.BrokerScriptURL
}
