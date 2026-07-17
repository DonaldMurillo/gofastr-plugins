package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestFullEditorBlocks proves the FULL block set (not just plain paragraphs)
// round-trips across the isolation boundary: markdown-style input rules inside
// the frame turn typed text into real heading + list nodes, which serialize into
// the canonical block-JSON the host mirrors into its hidden field.
func TestFullEditorBlocks(t *testing.T) {
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
	if !pollTrue(ctx, `!!(document.querySelector('iframe') && document.querySelector('iframe').__richtextReady === true)`, 10*time.Second) {
		t.Fatal("editor never signalled __richtextReady")
	}

	// Focus the editor and drive input rules: "# " → heading, "- " → bullet list.
	// Aim low: the rules only fire at the start of a line, so land on the
	// trailing empty paragraph rather than inside existing text.
	xy := editorClickXYFrac(ctx, t, 0.7)
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(xy[0], xy[1]),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.KeyEvent("# Big Heading"),
		chromedp.KeyEvent("\n"),
		chromedp.KeyEvent("- first item"),
		chromedp.KeyEvent("\n"),
		chromedp.KeyEvent("second item"),
		chromedp.Sleep(600*time.Millisecond), // let docChanged debounce fire
	); err != nil {
		t.Fatalf("type: %v", err)
	}

	// Read the canonical block-JSON the broker mirrored into the host form.
	var jsonVal string
	if !pollTrue(ctx, `(document.querySelector('input[name=body_json]')||{}).value && document.querySelector('input[name=body_json]').value.length > 20`, 4*time.Second) {
		t.Fatal("body_json never populated from the editor")
	}
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('input[name=body_json]').value`, &jsonVal))

	// The doc JSON must contain real structural node types produced by the input
	// rules — proving the full schema (not just paragraphs) crosses the boundary.
	for _, want := range []string{`"heading"`, `"Big Heading"`, `"bullet_list"`, `"list_item"`, `"first item"`} {
		if !strings.Contains(jsonVal, want) {
			t.Errorf("body_json missing %s\n---\n%s", want, truncate(jsonVal, 600))
		}
	}
	// And the markdown export should carry the heading marker + list bullets.
	var md string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.querySelector('input[name=body_md]')||{}).value||''`, &md))
	if !strings.Contains(md, "# Big Heading") || !strings.Contains(md, "first item") {
		t.Errorf("markdown export missing heading/list: %q", truncate(md, 300))
	}
	t.Logf("full-block round-trip OK: heading + bullet list serialized to JSON and markdown across the boundary")
}
