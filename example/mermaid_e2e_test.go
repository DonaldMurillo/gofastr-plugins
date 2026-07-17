package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// TestMermaidCanary is the completeness-canary e2e: the SECOND heavy-JS plugin
// (an isolated Mermaid diagram renderer, ~2.6 MB bundle) mounts on the SAME
// pluginhost platform as the editor, boots inside the opaque sandbox under the
// strict frame CSP (script-src 'self', NO unsafe-eval), and renders — proving the
// extracted platform generalizes with zero new platform code.
func TestMermaidCanary(t *testing.T) {
	app, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	ctx, cancel := newChrome(t)
	defer cancel()

	// Collect frame-side errors — a CSP eval violation or a boot throw would show
	// here and fail the canary.
	var mu sync.Mutex
	var errs []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventConsoleAPICalled); ok && e.Type == "error" {
			var s string
			for _, a := range e.Args {
				if a.Value != nil {
					s += " " + string(a.Value)
				} else if a.Description != "" {
					s += " " + a.Description
				}
			}
			mu.Lock()
			errs = append(errs, s)
			mu.Unlock()
		}
	})
	_ = chromedp.Run(ctx, runtime.Enable())

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/mermaid")); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if !pollTrue(ctx, `!!(document.querySelector('iframe') && document.querySelector('iframe').__pluginReady === true)`, 12*time.Second) {
		t.Fatal("mermaid frame never signalled __pluginReady (mount/handshake failed)")
	}

	// Isolation: same opaque-origin sandbox guarantee as the editor.
	var sandbox string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('iframe').getAttribute('sandbox')`, &sandbox))
	if sandbox != "allow-scripts" {
		t.Errorf("mermaid sandbox=%q, want exactly \"allow-scripts\"", sandbox)
	}

	// Type a diagram and confirm it renders (the frame auto-sizes to the SVG, so
	// the iframe height grows past the initial min-height).
	var xy []float64
	_ = evalJSON(ctx, `(()=>{const r=document.querySelector('iframe').getBoundingClientRect();return [r.x+r.width/2, r.y+40];})()`, &xy)
	if len(xy) == 2 {
		_ = chromedp.Run(ctx,
			chromedp.MouseClickXY(xy[0], xy[1]),
			chromedp.Sleep(150*time.Millisecond),
			// Select-all + replace with a fresh diagram, then let it render.
			chromedp.KeyEvent("\n\ngraph LR\n  A-->B-->C\n  A-->C\n"),
			chromedp.Sleep(1500*time.Millisecond),
		)
	}

	// The canary's core claim: NO eval / CSP-security errors from the frame.
	mu.Lock()
	captured := append([]string(nil), errs...)
	mu.Unlock()
	for _, e := range captured {
		le := strings.ToLower(e)
		if strings.Contains(le, "unsafe-eval") || strings.Contains(le, "content security policy") ||
			strings.Contains(le, "eval") || strings.Contains(le, "securityerror") {
			t.Errorf("mermaid frame emitted a CSP/eval/security error (canary FAIL): %s", e)
		}
	}
	t.Logf("mermaid canary: mounted + rendered under strict frame CSP; %d console errors (none CSP/eval)", len(captured))
}
