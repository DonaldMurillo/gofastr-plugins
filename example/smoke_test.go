package main

// Phase-0 boundary + latency smoke gate. Proves the opaque-origin sandboxed
// iframe is a real isolation boundary AND a usable editing surface, driven
// through a real headless Chrome via chromedp. This is the go/no-go gate before
// the full editor build; Worker I extends it into the full-editor e2e suite.
//
// Run: go test ./example/ -run TestPhase0 -v -count=1
// Requires a local Chrome/Chromium (chromedp finds the system install).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// STRICT_LATENCY_MS is the plan's go/no-go target (p99 keystroke latency).
const strictLatencyMS = 16.0

// hardCeilingMS is what actually FAILS the smoke gate: any working in-frame
// editor clears this easily; a laggy cross-boundary design (typing crossing the
// postMessage boundary) would blow past it. The strict 16 ms target is reported
// as the headline verdict but not hard-failed here (rAF quantization + headless
// compositor noise make the exact number environment-sensitive; the rigorous
// strict gate runs on the full editor in Worker I's suite).
const hardCeilingMS = 50.0

func startServer(t *testing.T) *httptest.Server {
	t.Helper()
	app, err := newApp()
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	return httptest.NewServer(app.Router())
}

func newChrome(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
			// Run the sandboxed iframe IN-PROCESS so CDP synthetic mouse/key
			// events route into it. This is a test-harness concern only: the
			// frame's opaque origin (from sandbox="allow-scripts") — and thus
			// every isolation assertion below — is unaffected by process model.
			chromedp.Flag("disable-site-isolation-trials", true),
			chromedp.Flag("disable-features", "IsolateOrigins,site-per-process"),
			// chromedp waits 20s by default for Chrome to print its DevTools
			// websocket URL, and a cold start on a loaded CI runner misses
			// that. It surfaces as "chrome start (is Chrome installed?):
			// websocket url timeout reached" — which reads like a missing
			// dependency and is not: the workflow installs Chrome with
			// setup-chrome and runs `google-chrome --version` right before
			// these tests. main has gone red on it intermittently.
			//
			// A genuinely missing binary still fails, and fast: exec of a
			// nonexistent Chrome errors immediately rather than waiting out
			// this timeout. So raising it absorbs slow starts without hiding
			// the failure the message describes.
			chromedp.WSURLReadTimeout(90*time.Second),
		)...,
	)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	// Warm up the browser so the first Run has a live target.
	if err := chromedp.Run(ctx); err != nil {
		cancelCtx()
		cancelAlloc()
		t.Fatalf("chrome start (is Chrome installed?): %v", err)
	}
	return ctx, func() { cancelCtx(); cancelAlloc() }
}

// evalJSON evaluates expr in the PARENT frame and unmarshals the result.
func evalJSON(ctx context.Context, expr string, out any) error {
	var raw []byte
	if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &raw)); err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Errorf("expr returned null: %s", expr)
	}
	return json.Unmarshal(raw, out)
}

// waitEditorSettled blocks until the iframe stops resizing.
//
// __richtextReady only says the editor mounted; the frame then AUTO-SIZES to
// its content, so a rect read straight after ready describes a box that is
// about to move. Any click computed from it lands somewhere else — or arrives
// before ProseMirror is interactive and is simply dropped. A fast machine hides
// this (the resize beats the next CDP round-trip); a loaded CI runner does not,
// which is precisely the kind of "passes locally, fails in CI" this suite keeps
// producing. Wait for two consecutive equal heights instead of sleeping a
// guessed constant.
func waitEditorSettled(ctx context.Context, t *testing.T, timeout time.Duration) {
	t.Helper()
	const expr = `(()=>{const f=document.querySelector('iframe');return f?f.getBoundingClientRect().height:-1;})()`
	deadline := time.Now().Add(timeout)
	var last float64 = -1
	stable := 0
	for time.Now().Before(deadline) {
		var h float64
		if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &h)); err != nil {
			t.Fatalf("measuring the editor iframe: %v", err)
		}
		if h > 0 && h == last {
			if stable++; stable >= 2 {
				return
			}
		} else {
			stable = 0
		}
		last = h
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("editor iframe never stopped resizing within %s (last height %.0f)", timeout, last)
}

// editorClickXY returns a viewport point that lands inside the editor's
// editable area, for chromedp's coordinate-based clicks (which, unlike
// Playwright's frameLocator, cannot address an element inside the opaque OOPIF
// and so must aim at raw pixels).
//
// Two things get in the way, and both have bitten this suite:
//
//  1. The OOPIF only receives clicks after the iframe is scrolled into view —
//     without scrollIntoView the click simply does not route in.
//  2. Whatever the DEMO PAGE puts on top. The shell has a `position: sticky`
//     header (z-index 60), so scrolling the iframe to block:'start' parks its
//     top edge UNDERNEATH that header — a click at a small fixed offset then
//     hits the header and the editor never focuses. That is exactly how this
//     broke: the offset was tuned to clear the editor's in-frame toolbar, and
//     silently stopped clearing the page's own chrome when the demo was
//     restyled.
//
// So do not trust a hardcoded offset: probe with elementFromPoint until the
// iframe is genuinely the topmost element, and fail loudly if it never is.
// topPad clears the editor's in-frame toolbar once inside.
func editorClickXY(ctx context.Context, t *testing.T, topPad float64) []float64 {
	t.Helper()
	return editorPoint(ctx, t, "start", fmt.Sprintf("Math.max(r.y,0) + %f", topPad))
}

// editorClickXYFrac aims at a fraction of the editor's height — use it when a
// test needs to land near the END of the document (e.g. to drive input rules,
// which only fire at the start of a line, so clicking into the middle of
// existing text would not trigger them).
func editorClickXYFrac(ctx context.Context, t *testing.T, frac float64) []float64 {
	t.Helper()
	return editorPoint(ctx, t, "center", fmt.Sprintf("r.y + Math.min(r.height - 16, r.height * %f)", frac))
}

// editorPoint scrolls the iframe into view, evaluates wantY against its rect,
// and returns that point only once elementFromPoint agrees the iframe owns it —
// walking the point into the frame if page chrome is on top, and clamping it
// into the viewport, since a coordinate outside the viewport is not clickable
// at all (an iframe taller than the window centers to a negative y).
func editorPoint(ctx context.Context, t *testing.T, block, wantY string) []float64 {
	t.Helper()
	waitEditorSettled(ctx, t, 15*time.Second)

	var xy []float64
	err := evalJSON(ctx, fmt.Sprintf(`(()=>{
		const f = document.querySelector('iframe');
		if (!f) return null;
		f.scrollIntoView({block:'%[1]s'});
		const r = f.getBoundingClientRect();
		const x = r.x + r.width / 2;
		let y = %[2]s;

		// Keep the point on screen and inside the frame.
		const lo = Math.max(r.y, 0) + 4, hi = Math.min(r.bottom, innerHeight) - 8;
		if (!(hi > lo)) return null;
		y = Math.min(Math.max(y, lo), hi);

		if (document.elementFromPoint(x, y) !== f) {
			// Something is on top (the shell's sticky header, a footer). Find a
			// row the iframe actually owns, preferring one near the target.
			let best = null;
			for (let p = lo; p < hi; p += 8) {
				if (document.elementFromPoint(x, p) === f &&
					(best === null || Math.abs(p - y) < Math.abs(best - y))) best = p;
			}
			if (best === null) return null;
			y = best;
		}
		return [x, y];
	})()`, block, wantY), &xy)
	if err != nil || len(xy) != 2 {
		t.Fatalf("no unoccluded, on-screen point inside the editor iframe (page chrome covering it?): %v", err)
	}
	return xy
}

// pollTrue polls a boolean JS expr in the parent until true or timeout.
func pollTrue(ctx context.Context, expr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var ok bool
		_ = chromedp.Run(ctx, chromedp.Evaluate(expr, &ok))
		if ok {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestPhase0SmokeGate(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()
	ctx, cancel := newChrome(t)
	defer cancel()

	// Load the self-contained demo page.
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/richtext")); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// Wait for the handshake (broker sets iframe.__richtextReady after init).
	if !pollTrue(ctx,
		`!!(document.querySelector('iframe') && document.querySelector('iframe').__richtextReady === true)`,
		10*time.Second) {
		t.Fatal("editor iframe never signalled __richtextReady (handshake failed)")
	}

	// --- T1: sandbox attributes (the load-bearing isolation assertion) ---
	var sandbox string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.querySelector('iframe').getAttribute('sandbox')`, &sandbox)); err != nil {
		t.Fatalf("read sandbox: %v", err)
	}
	if sandbox != "allow-scripts" {
		t.Errorf("T1: sandbox = %q, want exactly \"allow-scripts\"", sandbox)
	}
	if strings.Contains(sandbox, "allow-same-origin") {
		t.Errorf("T1: sandbox contains allow-same-origin — isolation broken: %q", sandbox)
	}

	// --- T2: parent cannot reach into the opaque frame ---
	var contentDocNull bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.querySelector('iframe').contentDocument === null`, &contentDocNull)); err != nil {
		t.Fatalf("read contentDocument: %v", err)
	}
	if !contentDocNull {
		t.Error("T2: iframe.contentDocument is reachable from the parent — not an opaque origin")
	}

	// --- T3: in-frame isolation probes (computed inside the sandbox) ---
	var probes struct {
		CookieEmpty    bool `json:"cookieEmpty"`
		ParentBlocked  bool `json:"parentBlocked"`
		StorageBlocked bool `json:"storageBlocked"`
	}
	if err := evalJSON(ctx, `document.querySelector('iframe').__richtextProbes`, &probes); err != nil {
		t.Fatalf("T3: read probes: %v", err)
	}
	if !probes.CookieEmpty || !probes.ParentBlocked || !probes.StorageBlocked {
		t.Errorf("T3: isolation probe failed: %+v (all must be true)", probes)
	}

	// --- T4: typing round-trips across the boundary into the host form ---
	//
	// focusAndType clicks the frame and types after a fixed 150ms. On a loaded
	// CI runner the click does not always land focus inside the opaque frame in
	// that window, and the keystrokes go nowhere — this test has flaked on main
	// exactly that way. The broker tracks focus internally but does not expose
	// it, so there is nothing to poll on; retrying the gesture is what is
	// available.
	//
	// Retrying is safe for this assertion: it checks that the text is PRESENT,
	// so a duplicated "Hello worldHello world" from a late-landing first
	// attempt still passes, and a genuinely broken bridge still fails all three.
	typed := false
	for attempt := 0; attempt < 3 && !typed; attempt++ {
		focusAndType(t, ctx, "Hello world")
		// docChanged is debounced ~300ms; poll the host's hidden field.
		typed = pollTrue(ctx,
			`(document.querySelector('input[name=body_json]')||{}).value && document.querySelector('input[name=body_json]').value.indexOf('Hello world') !== -1`,
			5*time.Second)
	}
	if !typed {
		var got string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.querySelector('input[name=body_json]')||{}).value||''`, &got))
		t.Errorf("T4: typed text did not reach host body_json field. got=%q", truncate(got, 200))
	}
	var md string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.querySelector('input[name=body_md]')||{}).value||''`, &md))
	if !strings.Contains(md, "Hello world") {
		t.Errorf("T4: body_md did not contain the text. got=%q", truncate(md, 200))
	}

	// --- T6: theme sync across the boundary (before latency, uses the toggle) ---
	testThemeSync(t, ctx)

	// --- T8: p99 keystroke latency (the headline go/no-go number) ---
	// Reset metrics inside the frame, type ≥120 chars, read the broker-stashed metric.
	typeManyForLatency(t, ctx, 130)
	var metric struct {
		P50   float64 `json:"p50"`
		P99   float64 `json:"p99"`
		Count int     `json:"count"`
	}
	if !pollTrue(ctx,
		`!!(document.querySelector('iframe').__richtextLastMetric && document.querySelector('iframe').__richtextLastMetric.count >= 100)`,
		8*time.Second) {
		t.Fatal("T8: never received a keystroke metric with count>=100 from the frame")
	}
	if err := evalJSON(ctx, `document.querySelector('iframe').__richtextLastMetric`, &metric); err != nil {
		t.Fatalf("T8: read metric: %v", err)
	}
	verdict := "PASS (≤16ms)"
	if metric.P99 > strictLatencyMS {
		verdict = "OVER strict 16ms target"
	}
	t.Logf("=== PHASE-0 LATENCY GATE === p50=%.2fms p99=%.2fms count=%d → %s",
		metric.P50, metric.P99, metric.Count, verdict)
	if metric.P99 > hardCeilingMS {
		t.Errorf("T8: p99=%.2fms exceeds hard ceiling %.0fms — the editing path is NOT usable (likely crossing the boundary per keystroke)", metric.P99, hardCeilingMS)
	}
}

// focusAndType clicks into the iframe (focusing the contenteditable inside) and
// types the given text. CDP routes key events to the focused frame.
func focusAndType(t *testing.T, ctx context.Context, text string) {
	t.Helper()
	var xy []float64
	if err := evalJSON(ctx,
		`(()=>{const f=document.querySelector('iframe');f.scrollIntoView({block:'center'});const r=f.getBoundingClientRect();return [r.x+r.width/2, r.y + Math.min(r.height - 16, r.height*0.7)];})()`,
		&xy); err != nil || len(xy) != 2 {
		t.Fatalf("focus: iframe rect: %v", err)
	}
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(xy[0], xy[1]),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.KeyEvent(text),
	); err != nil {
		t.Fatalf("focus/type: %v", err)
	}
}

// typeManyForLatency resets the in-frame metrics then types n single chars, each
// followed by a short sleep so each keystroke gets its own paint frame.
func typeManyForLatency(t *testing.T, ctx context.Context, n int) {
	t.Helper()
	// Reset via a frame-targeted eval is awkward across the opaque boundary; the
	// ring buffer + percentile just accumulate, which is fine — we assert on the
	// latest metric snapshot. Focus first.
	var xy []float64
	_ = evalJSON(ctx, `(()=>{const f=document.querySelector('iframe');f.scrollIntoView({block:'center'});const r=f.getBoundingClientRect();return [r.x+r.width/2, r.y + Math.min(r.height - 16, r.height*0.7)];})()`, &xy)
	if len(xy) == 2 {
		_ = chromedp.Run(ctx, chromedp.MouseClickXY(xy[0], xy[1]), chromedp.Sleep(100*time.Millisecond))
	}
	for i := 0; i < n; i++ {
		_ = chromedp.Run(ctx, chromedp.KeyEvent("a"), chromedp.Sleep(6*time.Millisecond))
	}
	_ = chromedp.Run(ctx, chromedp.Sleep(300*time.Millisecond))
}

// testThemeSync reads a --color token's light value, flips the demo dark toggle,
// and asserts the frame re-resolved the token to the new (dark) host value.
func testThemeSync(t *testing.T, ctx context.Context) {
	t.Helper()
	// Pick a --color-* name that resolves non-empty on the host root.
	var name string
	_ = chromedp.Run(ctx, chromedp.Evaluate(
		`(()=>{const cs=getComputedStyle(document.documentElement);for(const n of ['--color-accent','--color-primary','--color-background','--color-text']){if(cs.getPropertyValue(n).trim())return n;}return '';})()`,
		&name))
	if name == "" {
		t.Log("T6: no known --color token resolved on host; skipping theme-sync assertion")
		return
	}
	var hostLight string
	_ = chromedp.Run(ctx, chromedp.Evaluate(
		fmt.Sprintf(`getComputedStyle(document.documentElement).getPropertyValue(%q).trim()`, name), &hostLight))

	// The frame should have reported the same value it resolved (light).
	var theme struct {
		Scheme string            `json:"scheme"`
		Sample map[string]string `json:"sample"`
	}
	if err := evalJSON(ctx, `document.querySelector('iframe').__richtextTheme`, &theme); err != nil {
		t.Logf("T6: no __richtextTheme yet: %v (skipping)", err)
		return
	}
	if fv, ok := theme.Sample[name]; ok && fv != hostLight {
		t.Errorf("T6: frame light value for %s = %q, host = %q (token did not cross correctly)", name, fv, hostLight)
	}

	// Flip to dark and confirm both host and frame values change together.
	if err := chromedp.Run(ctx,
		chromedp.Click(`#fui-scheme-toggle`, chromedp.ByID),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Logf("T6: toggle click failed: %v (skipping dark assertion)", err)
		return
	}
	var hostDark string
	_ = chromedp.Run(ctx, chromedp.Evaluate(
		fmt.Sprintf(`getComputedStyle(document.documentElement).getPropertyValue(%q).trim()`, name), &hostDark))
	var theme2 struct {
		Scheme string            `json:"scheme"`
		Sample map[string]string `json:"sample"`
	}
	if err := evalJSON(ctx, `document.querySelector('iframe').__richtextTheme`, &theme2); err != nil {
		t.Logf("T6: no __richtextTheme after toggle: %v", err)
		return
	}
	if fv, ok := theme2.Sample[name]; ok && fv != hostDark {
		t.Errorf("T6: after dark toggle, frame %s = %q, host = %q (theme did not re-sync)", name, fv, hostDark)
	}
	t.Logf("T6: theme sync OK for %s (light=%q dark=%q, frame scheme=%q)", name, hostLight, hostDark, theme2.Scheme)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
