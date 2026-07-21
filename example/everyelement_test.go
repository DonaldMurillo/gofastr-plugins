package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// A 1x1 red PNG.
const tinyPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// everyElementDoc is a canonical block-JSON doc exercising EVERY node + mark in
// schema-v1 — used to visually verify each one renders.
var everyElementDoc = `{"type":"doc","content":[
{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"H1 Heading"}]},
{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"H2 Heading"}]},
{"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"H3 Heading"}]},
{"type":"heading","attrs":{"level":4},"content":[{"type":"text","text":"H4 Heading"}]},
{"type":"heading","attrs":{"level":5},"content":[{"type":"text","text":"H5 Heading"}]},
{"type":"heading","attrs":{"level":6},"content":[{"type":"text","text":"H6 Heading"}]},
{"type":"paragraph","content":[
  {"type":"text","marks":[{"type":"strong"}],"text":"bold "},
  {"type":"text","marks":[{"type":"em"}],"text":"italic "},
  {"type":"text","marks":[{"type":"underline"}],"text":"underline "},
  {"type":"text","marks":[{"type":"strike"}],"text":"strike "},
  {"type":"text","marks":[{"type":"code"}],"text":"code "},
  {"type":"text","marks":[{"type":"link","attrs":{"href":"https://example.com","title":null}}],"text":"link "},
  {"type":"text","marks":[{"type":"textColor","attrs":{"color":"red"}}],"text":"red "},
  {"type":"text","marks":[{"type":"bgColor","attrs":{"color":"yellow"}}],"text":"highlight"}
]},
{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"A block quote."}]}]},
{"type":"code_block","attrs":{"language":"javascript"},"content":[{"type":"text","text":"const answer = 42;"}]},
{"type":"divider"},
{"type":"bullet_list","content":[
  {"type":"list_item","content":[{"type":"paragraph","content":[{"type":"text","text":"Bullet one"}]}]},
  {"type":"list_item","content":[{"type":"paragraph","content":[{"type":"text","text":"Bullet two"}]}]}
]},
{"type":"ordered_list","attrs":{"start":1},"content":[
  {"type":"list_item","content":[{"type":"paragraph","content":[{"type":"text","text":"First"}]}]},
  {"type":"list_item","content":[{"type":"paragraph","content":[{"type":"text","text":"Second"}]}]}
]},
{"type":"task_list","content":[
  {"type":"task_item","attrs":{"checked":true},"content":[{"type":"paragraph","content":[{"type":"text","text":"Done task"}]}]},
  {"type":"task_item","attrs":{"checked":false},"content":[{"type":"paragraph","content":[{"type":"text","text":"Todo task"}]}]}
]},
{"type":"callout","attrs":{"variant":"info","icon":null},"content":[{"type":"paragraph","content":[{"type":"text","text":"Info callout"}]}]},
{"type":"callout","attrs":{"variant":"warn","icon":null},"content":[{"type":"paragraph","content":[{"type":"text","text":"Warning callout"}]}]},
{"type":"callout","attrs":{"variant":"success","icon":null},"content":[{"type":"paragraph","content":[{"type":"text","text":"Success callout"}]}]},
{"type":"callout","attrs":{"variant":"danger","icon":null},"content":[{"type":"paragraph","content":[{"type":"text","text":"Danger callout"}]}]},
{"type":"toggle","attrs":{"open":true},"content":[
  {"type":"toggle_summary","content":[{"type":"text","text":"Toggle summary"}]},
  {"type":"content","content":[{"type":"paragraph","content":[{"type":"text","text":"Toggle body content."}]}]}
]},
{"type":"columns","attrs":{"count":2},"content":[
  {"type":"column","content":[{"type":"paragraph","content":[{"type":"text","text":"Left column"}]}]},
  {"type":"column","content":[{"type":"paragraph","content":[{"type":"text","text":"Right column"}]}]}
]},
{"type":"table","content":[
  {"type":"table_row","content":[
    {"type":"table_header","attrs":{"colspan":1,"rowspan":1,"colwidth":null},"content":[{"type":"paragraph","content":[{"type":"text","text":"Name"}]}]},
    {"type":"table_header","attrs":{"colspan":1,"rowspan":1,"colwidth":null},"content":[{"type":"paragraph","content":[{"type":"text","text":"Role"}]}]}
  ]},
  {"type":"table_row","content":[
    {"type":"table_cell","attrs":{"colspan":1,"rowspan":1,"colwidth":null},"content":[{"type":"paragraph","content":[{"type":"text","text":"Ada"}]}]},
    {"type":"table_cell","attrs":{"colspan":1,"rowspan":1,"colwidth":null},"content":[{"type":"paragraph","content":[{"type":"text","text":"Engineer"}]}]}
  ]}
]},
{"type":"image","attrs":{"src":"` + tinyPNG + `","alt":"tiny","title":null,"width":48}},
{"type":"paragraph","content":[{"type":"text","text":"— end —"}]}
]}`

// TestEveryElement loads a doc with every node/mark into the editor and the SSR
// read view and screenshots both. Opt-in via SHOTS=1. Also asserts the editor
// ACCEPTED the doc (round-tripped every node type back through the boundary), so
// a rejected/lossy doc fails even without a human looking at the pixels.
func TestEveryElement(t *testing.T) {
	// Screenshots are opt-in (SHOTS_DIR); the round-trip + frame-grew assertions
	// below run every time — they guard the toggle-toDOM crash and the resize
	// px-unit bug that this pass uncovered.
	dir := os.Getenv("SHOTS_DIR")
	app, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// Persist the comprehensive doc so the demo page server-renders it into the
	// editor and the read view renders it too. The raw doc is valid JSON already
	// (newlines are just inter-token whitespace); do NOT strip whitespace, or
	// spaces inside text values ("H1 Heading") would collapse.
	payload := `{"docId":"demo","doc":` + everyElementDoc + `,"markdown":"","schemaVersion":"richtext-v1"}`
	resp, err := http.Post(srv.URL+"/__gofastr/plugin/richtext/save", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d", resp.StatusCode)
	}

	ctx, cancel := newChrome(t)
	defer cancel()

	// --- Editor render ---
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/richtext")); err != nil {
		t.Fatal(err)
	}
	if !pollTrue(ctx, `!!(document.querySelector('iframe') && document.querySelector('iframe').__richtextReady === true)`, 10*time.Second) {
		t.Fatal("editor never ready")
	}
	_ = chromedp.Run(ctx, chromedp.Sleep(1200*time.Millisecond))

	// body_json only populates on an edit (docChanged), so nudge the doc: click
	// into it and type a space, then the editor re-serializes the WHOLE doc — if
	// it had rejected any block, that block would be absent from the JSON.
	xy := editorClickXY(ctx, t, 62)
	_ = chromedp.Run(ctx, chromedp.MouseClickXY(xy[0], xy[1]), chromedp.Sleep(120*time.Millisecond),
		chromedp.KeyEvent(" "), chromedp.Sleep(600*time.Millisecond))
	var js string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.querySelector('input[name=body_json]')||{}).value||''`, &js))
	nodeTypes := []string{"heading", "blockquote", "code_block", "divider", "bullet_list", "ordered_list",
		"task_list", "task_item", "callout", "toggle", "toggle_summary", "columns", "column",
		"table", "table_header", "table_cell", "image"}
	marks := []string{"strong", "em", "underline", "strike", "\"code\"", "link", "textColor", "bgColor"}
	missing := []string{}
	for _, n := range append(nodeTypes, marks...) {
		if !strings.Contains(js, n) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("editor did NOT round-trip these node/mark types (rejected or lost): %v\n---body_json---\n%s", missing, js)
	} else {
		t.Logf("editor accepted and round-tripped all %d node types + %d marks", len(nodeTypes), len(marks))
	}

	// Frame-grew guard: this doc is ~1500px tall; the iframe must auto-size past
	// its 240px initial height (regression guard for the resize px-unit bug that
	// pinned the frame at 240px and clipped long docs).
	var frameH float64
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('iframe').getBoundingClientRect().height`, &frameH))
	if frameH < 600 {
		t.Errorf("iframe did not auto-size to the tall doc: height=%.0fpx (want > 600) — resize/autosize is broken", frameH)
	} else {
		t.Logf("iframe auto-sized to %.0fpx for the full doc", frameH)
	}

	if dir == "" {
		return // screenshots are opt-in via SHOTS_DIR
	}
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 92)); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(dir+"/every-editor.png", buf, 0o644)
	t.Logf("wrote every-editor.png (%d bytes)", len(buf))

	var buf2 []byte
	if err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/__gofastr/plugin/richtext/read?doc=demo"),
		chromedp.Sleep(800*time.Millisecond),
		chromedp.FullScreenshot(&buf2, 92),
	); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(dir+"/every-ssr.png", buf2, 0o644)
	t.Logf("wrote every-ssr.png (%d bytes)", len(buf2))
}
