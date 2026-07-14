package main

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestClickAnywhereToType reproduces a real user's interaction: click in the
// LOWER part of the empty editor (not the top text line) and type. Regression
// guard for the bug where the .ProseMirror editable didn't fill the box, so
// clicking below the first line missed it and nothing focused → couldn't type.
func TestClickAnywhereToType(t *testing.T) {
	app, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	resetDemoDoc(t, srv.URL)
	ctx, cancel := newChrome(t)
	defer cancel()

	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/")); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if !pollTrue(ctx, `!!(document.querySelector('iframe') && document.querySelector('iframe').__wysiwygReady === true)`, 10*time.Second) {
		t.Fatal("editor never ready")
	}
	// Let the autosize settle so the iframe height matches the editable, and
	// scroll the editor into view — the demo page has a hero above it, so the
	// frame's bottom half can start below the viewport fold.
	_ = chromedp.Run(ctx, chromedp.Sleep(500*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('iframe').scrollIntoView({block:'center'})`, nil),
		chromedp.Sleep(200*time.Millisecond))

	// Click near the BOTTOM of the editor iframe — where a user would click in
	// the empty area, NOT on the first line.
	var xy []float64
	if err := evalJSON(ctx, `(()=>{const r=document.querySelector('iframe').getBoundingClientRect();return [r.x + r.width/2, r.y + r.height - 40];})()`, &xy); err != nil {
		t.Fatalf("rect: %v", err)
	}
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(xy[0], xy[1]),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.KeyEvent("typing works from a low click"),
		chromedp.Sleep(600*time.Millisecond),
	); err != nil {
		t.Fatalf("click/type: %v", err)
	}

	if !pollTrue(ctx,
		`(document.querySelector('input[name=body_json]')||{}).value && document.querySelector('input[name=body_json]').value.indexOf('typing works from a low click') !== -1`,
		4*time.Second) {
		var got string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.querySelector('input[name=body_json]')||{}).value||''`, &got))
		t.Errorf("clicking the empty editor area did not focus it — could not type. body_json=%q", truncate(got, 200))
	}
}
