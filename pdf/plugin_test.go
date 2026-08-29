package pdf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newTestApp stands up a fresh framework.App with the pdf plugin registered and
// initialized (mirrors mermaid/monaco's harness; the in-memory store needs no DB).
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "pdf-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// TestInitServesAssetsWithCorrectContentTypes pins the served Content-Type of
// every asset (frame document + sub-resources, host adapter, sample PDF).
func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{ViewerHTMLURL, "text/html; charset=utf-8"},
		{ViewerJSURL, "text/javascript; charset=utf-8"},
		{ViewerCSSURL, "text/css; charset=utf-8"},
		{AdapterScriptURL, "text/javascript; charset=utf-8"},
		{ConfigScriptURL, "text/javascript; charset=utf-8"},
		{SamplePDFURL, "application/pdf"},
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
// the framing/CORP/CSP relaxation that lets the host frame its OWN viewer
// document and lets the opaque frame fetch its JS/CSS (DECISIONS.md Phase-0
// gotcha #1), AND that the fixed framedCSP carries connect-src 'none' + sandbox
// allow-scripts — the load-bearing isolation directives this spike targets.
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{ViewerHTMLURL, ViewerJSURL, ViewerCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		// Framed CSP keys sub-resource loading to the explicit origin, not 'self'.
		if !strings.Contains(csp, "frame-ancestors http") {
			t.Errorf("%s: CSP frame-ancestors must permit host origin: %q", path, csp)
		}
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: framed CSP must carry `sandbox allow-scripts`: %q", path, csp)
		}
		if strings.Contains(csp, "allow-same-origin") {
			t.Errorf("%s: framed CSP sandbox must NEVER allow-same-origin: %q", path, csp)
		}
		if !strings.Contains(csp, "connect-src 'none'") {
			t.Errorf("%s: framed CSP must forbid network egress (connect-src 'none'): %q", path, csp)
		}
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
			t.Errorf("%s: CORP=%q want cross-origin", path, got)
		}
	}

	// Host-page adapter + sample PDF are NON-framed: no CORP relaxation.
	for _, path := range []string{AdapterScriptURL, SamplePDFURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got == "cross-origin" {
			t.Errorf("%s: host-page asset must NOT be CORP cross-origin", path)
		}
	}

	// The generic platform broker route is served by pluginhost.RegisterBrokerRoute.
	resp, err := http.Get(srv.URL + pluginhost.BrokerScriptURL)
	if err != nil {
		t.Fatalf("GET pluginhost broker: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("pluginhost broker status=%d", resp.StatusCode)
	}
}

// TestDemoPageContainsMountAndBroker confirms the demo mounts the pdf marker and
// emits the broker + adapter scripts.
func TestDemoPageContainsMountAndBroker(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET %s: %v", DemoURL, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	for _, want := range []string{
		`data-fui-plugin="pdf"`,
		`src="` + pluginhost.BrokerScriptURL + `"`,
		`src="` + AdapterScriptURL + `"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

// --- chromedp e2e -----------------------------------------------------------

// consoleCapture is a thread-safe collector for browser-side console + log
// entries. CSP violations surface as `log` entries (source security/violation);
// JS console calls surface as `runtime.EventConsoleAPICalled`.
type consoleCapture struct {
	mu       sync.Mutex
	messages []string // formatted "type: text" for console calls
	errors   []string // level==error console calls
	entries  []log.Entry
	cspViol  []string
	except   []string
}

func (c *consoleCapture) handle(ev interface{}) {
	switch e := ev.(type) {
	case *runtime.EventConsoleAPICalled:
		text := remoteArgsToText(e.Args)
		line := fmt.Sprintf("console.%s: %s", e.Type, text)
		c.mu.Lock()
		c.messages = append(c.messages, line)
		if e.Type == "error" {
			c.errors = append(c.errors, text)
		}
		c.mu.Unlock()
	case *log.EventEntryAdded:
		c.mu.Lock()
		c.entries = append(c.entries, *e.Entry)
		// CSP violations: source security or violation, OR text mentions
		// Content-Security-Policy. These are the spike's load-bearing signals.
		if e.Entry.Source == log.SourceSecurity || e.Entry.Source == log.SourceViolation ||
			strings.Contains(strings.ToLower(e.Entry.Text), "content security policy") ||
			strings.Contains(e.Entry.Text, "CSP") {
			c.cspViol = append(c.cspViol, fmt.Sprintf("[%s/%s] %s", e.Entry.Source, e.Entry.Level, e.Entry.Text))
		}
		c.mu.Unlock()
	case *runtime.EventExceptionThrown:
		text := ""
		if e.ExceptionDetails != nil {
			text = e.ExceptionDetails.Text
			if e.ExceptionDetails.Exception != nil && e.ExceptionDetails.Exception.Description != "" {
				text = e.ExceptionDetails.Exception.Description
			}
		}
		c.mu.Lock()
		c.except = append(c.except, text)
		c.mu.Unlock()
	}
}

// newChrome mirrors example/smoke_test.go's allocator flags. The two
// disable-features flags are REQUIRED to drive the opaque OOPIF with chromedp's
// synthetic events (the frame's opaque origin is unaffected — harness-only).
func newChrome(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-site-isolation-trials", true),
			chromedp.Flag("disable-features", "IsolateOrigins,site-per-process"),
			chromedp.WSURLReadTimeout(90*time.Second),
			// Same reason as example/smoke_test.go and posthog/e2e_test.go:
			// chromedp waits 20s by default for Chrome to print its DevTools
			// websocket URL, and a cold start on a loaded CI runner misses it.
			// It surfaces as "chrome start (is Chrome installed?)", which reads
			// like a missing dependency and is not — the workflow installs
			// Chrome and runs `google-chrome --version` immediately before.
			// A genuinely missing binary still fails fast.
		)...,
	)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(ctx); err != nil {
		cancelCtx()
		cancelAlloc()
		t.Fatalf("chrome start (is Chrome installed?): %v", err)
	}
	return ctx, func() { cancelCtx(); cancelAlloc() }
}

func remoteArgsToText(args []*runtime.RemoteObject) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if a == nil {
			continue
		}
		if a.Value != nil {
			var v interface{}
			if json.Unmarshal(a.Value, &v) == nil {
				b.WriteString(fmt.Sprintf("%v", v))
				continue
			}
		}
		if a.Description != "" {
			b.WriteString(a.Description)
		} else if a.Type != "" {
			b.WriteString(string(a.Type))
		}
	}
	return b.String()
}

// TestRenderInOpaqueFrame is the spike's go/no-go: pdf.js renders inside the
// opaque-origin sandboxed iframe under the fixed framed CSP with ZERO console
// errors and ZERO CSP violations, the bridged bytes produce a non-blank canvas,
// and the extracted text layer contains the secret.
func TestRenderInOpaqueFrame(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	ctx, cancel := newChrome(t)
	defer cancel()
	cap := &consoleCapture{}
	chromedp.ListenTarget(ctx, cap.handle)

	// Enable the Log domain so CSP/security entries arrive as log.EventEntryAdded.
	if err := chromedp.Run(ctx, log.Enable()); err != nil {
		t.Fatalf("log.Enable: %v", err)
	}

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+DemoURL)); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// Wait for the frame to finish rendering page 1 (iframe.__pdfRendered),
	// OR an error mirror to short-circuit with a clear failure.
	ready := waitTrue(ctx, t, `(function(){
		var f = document.querySelector('iframe');
		if (!f) return null;
		if (f.__pdfError) return { error: f.__pdfError };
		return { ready: !!f.__pdfRendered };
	})()`, 25*time.Second)
	if ready == nil {
		t.Fatal("timed out waiting for iframe readiness; no error mirror either")
	}
	if e, ok := ready["error"].(string); ok && e != "" {
		t.Fatalf("frame reported renderError before rendering: %s\nconsole messages:\n%s",
			e, strings.Join(cap.snapshotMessages(), "\n"))
	}
	if _, ok := ready["ready"].(bool); !ok || !ready["ready"].(bool) {
		t.Fatalf("frame did not reach __pdfRendered=true; got %v\nconsole:\n%s",
			ready, strings.Join(cap.snapshotMessages(), "\n"))
	}

	// Read the mirrored stats off the iframe element in the parent.
	var stats struct {
		Text         string  `json:"text"`
		PageCount    float64 `json:"pageCount"`
		NonBlank     bool    `json:"nonBlank"`
		NonWhitePx   float64 `json:"nonWhitePixels"`
		PdfjsVersion string  `json:"pdfjsVersion"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(function(){var f=document.querySelector('iframe');return {
			text:f.__pdfText, pageCount:f.__pdfPageCount, nonBlank:f.__pdfNonBlank,
			nonWhitePixels:f.__pdfNonWhitePixels, pdfjsVersion:f.__pdfPdfjsVersion};})()`,
		&stats)); err != nil {
		t.Fatalf("read stats: %v", err)
	}

	// (a) page-1 canvas is non-blank.
	if !stats.NonBlank || stats.NonWhitePx == 0 {
		t.Errorf("canvas reported blank: nonBlank=%v nonWhitePixels=%v", stats.NonBlank, stats.NonWhitePx)
	} else {
		t.Logf("canvas non-blank: %.0f non-white pixels (pdf.js %s, %g pages)",
			stats.NonWhitePx, stats.PdfjsVersion, stats.PageCount)
	}
	if stats.PageCount < 2 {
		t.Errorf("expected multi-page PDF, got pageCount=%v", stats.PageCount)
	}

	// (a1) THE isolation guarantee: the frame's own probes (mirrored on
	//      __pdfProbes by the adapter) must report cookie / parent / storage
	//      access ALL blocked. Without this assertion a future change that
	//      de-opaques the frame (a stray allow-same-origin, a sandbox leak)
	//      silently goes green — the whole cage would be an untested
	//      assumption. This is the single most important check in the package.
	var probes struct {
		CookieEmpty    bool `json:"cookieEmpty"`
		ParentBlocked  bool `json:"parentBlocked"`
		StorageBlocked bool `json:"storageBlocked"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(function(){var f=document.querySelector('iframe');return f.__pdfProbes||{};})()`,
		&probes)); err != nil {
		t.Fatalf("read isolation probes: %v", err)
	}
	if !probes.CookieEmpty || !probes.ParentBlocked || !probes.StorageBlocked {
		t.Errorf("isolation probes must all be blocked: cookieEmpty=%v parentBlocked=%v storageBlocked=%v — a probe reading host state means the frame is no longer opaque-origin isolated",
			probes.CookieEmpty, probes.ParentBlocked, probes.StorageBlocked)
	} else {
		t.Logf("isolation probes: cookieEmpty=%v parentBlocked=%v storageBlocked=%v (opaque-origin guarantee holds)",
			probes.CookieEmpty, probes.ParentBlocked, probes.StorageBlocked)
	}

	// (b) the extracted text layer contains the secret string.
	if !strings.Contains(stats.Text, "SPIKE_SECRET_ALPHA") {
		t.Errorf("text layer missing SPIKE_SECRET_ALPHA; got %q", truncate(stats.Text, 200))
	} else {
		t.Logf("text layer contains SPIKE_SECRET_ALPHA (len=%d)", len(stats.Text))
	}

	// (c) ZERO console errors and ZERO CSP violation reports. Give the browser a
	// brief settle so any trailing violation (e.g. a late referrer-policy notice)
	// lands before the assertion.
	time.Sleep(500 * time.Millisecond)
	csp := cap.snapshotCSP()
	if len(csp) > 0 {
		t.Errorf("expected ZERO CSP violations, got %d:\n%s", len(csp), strings.Join(csp, "\n"))
	}
	errs := cap.snapshotErrors()
	if len(errs) > 0 {
		t.Errorf("expected ZERO console.error entries, got %d:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	exc := cap.snapshotExceptions()
	if len(exc) > 0 {
		t.Errorf("expected ZERO uncaught exceptions, got %d:\n%s", len(exc), strings.Join(exc, "\n"))
	}
	t.Logf("console messages (%d total): %s", len(cap.snapshotMessages()),
		truncate(strings.Join(cap.snapshotMessages(), " | "), 400))
	// (d) Empirically confirm what the sandbox blocks (download/print/clipboard).
	// None of these work in-frame under sandbox="allow-scripts" alone — they must
	// be HOST capabilities over the bridge (the brief's finding #5).
	var caps struct {
		HasPrint       bool     `json:"hasPrint"`
		ClipboardWrite string   `json:"clipboardWrite"`
		Allowed        []string `json:"allowedFeatures"`
		Origin         string   `json:"origin"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(function(){var f=document.querySelector('iframe');return f.__pdfCaps||null;})()`,
		&caps)); err != nil {
		t.Logf("read caps: %v", err)
	}
	t.Logf("frame caps: hasPrint=%v clipboardWrite=%q origin=%q", caps.HasPrint, caps.ClipboardWrite, caps.Origin)
	// NOTE: caps.Origin is NOT asserted == "null" here. Under chromedp's
	// disable-site-isolation flags (REQUIRED to drive the OOPIF), Chrome reports
	// the frame's location.origin as the EMBEDDER origin — a harness artifact.
	// The real opaque-origin guarantee is proven by the isolation probes
	// (parentBlocked/storageBlocked/cookieEmpty, mirrored on __pdfProbes) and by
	// the framed CSP carrying `sandbox allow-scripts` with no allow-same-origin.
	//
	// clipboard-write is a real Permissions Policy feature. The sandbox does NOT
	// delegate it (no allow- token), so it must be ABSENT from allowedFeatures —
	// and writeText must NOT succeed. This is the load-bearing signal that
	// download/print/clipboard must be HOST capabilities over the bridge.
	for _, feat := range []string{"clipboard-write"} {
		if slices.Contains(caps.Allowed, feat) {
			t.Errorf("Permissions Policy delegated %q into the sandbox — expected it blocked", feat)
		}
	}
	if caps.ClipboardWrite == "ok" {
		t.Errorf("clipboard.writeText succeeded inside the sandbox — expected it blocked")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// waitTrue polls a JS expression that returns null / {ready:false} until it
// returns {ready:true} or an {error:...} short-circuit, or timeout. Returns the
// last decoded object (map[string]interface{}) or nil on timeout.
func waitTrue(ctx context.Context, t *testing.T, expr string, timeout time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var raw interface{}
		if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &raw)); err != nil {
			t.Fatalf("poll evaluate: %v", err)
		}
		if m, ok := raw.(map[string]interface{}); ok && m != nil {
			if e, has := m["error"]; has && e != nil {
				return m
			}
			if r, ok := m["ready"].(bool); ok && r {
				return m
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}

func (c *consoleCapture) snapshotMessages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.messages...)
}
func (c *consoleCapture) snapshotErrors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.errors...)
}
func (c *consoleCapture) snapshotCSP() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.cspViol...)
}
func (c *consoleCapture) snapshotExceptions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.except...)
}
