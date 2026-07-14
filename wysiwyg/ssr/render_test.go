package ssr

import (
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr/core/render"
)

// mustContain fails the test if out does not contain every needle.
func mustContain(t *testing.T, out render.HTML, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if !strings.Contains(string(out), needle) {
			t.Errorf("output missing %q\n--- got ---\n%s", needle, string(out))
		}
	}
}

// mustNotContain fails the test if out contains needle.
func mustNotContain(t *testing.T, out render.HTML, needle string) {
	t.Helper()
	if strings.Contains(string(out), needle) {
		t.Errorf("output unexpectedly contains %q\n--- got ---\n%s", needle, string(out))
	}
}

// text builds a marked text node.
func text(t string, marks ...map[string]any) map[string]any {
	n := map[string]any{"type": "text", "text": t}
	if len(marks) > 0 {
		ms := make([]any, len(marks))
		for i, m := range marks {
			ms[i] = m
		}
		n["marks"] = ms
	}
	return n
}

// node builds a node with a type, optional attrs and content.
func node(typ string, attrs map[string]any, content ...map[string]any) map[string]any {
	n := map[string]any{"type": typ}
	if attrs != nil {
		n["attrs"] = attrs
	}
	if len(content) > 0 {
		c := make([]any, len(content))
		for i, x := range content {
			c[i] = x
		}
		n["content"] = c
	}
	return n
}

func doc(blocks ...map[string]any) map[string]any {
	c := make([]any, len(blocks))
	for i, b := range blocks {
		c[i] = b
	}
	return map[string]any{"type": "doc", "content": c}
}

func inline(ns ...map[string]any) []any {
	out := make([]any, len(ns))
	for i, n := range ns {
		out[i] = n
	}
	return out
}

// --- Representative doc: every block type --------------------------------

func TestRenderFullDoc(t *testing.T) {
	d := doc(
		node("heading", map[string]any{"level": 2}, node("paragraph", nil, text("Title"))),
		node("paragraph", nil, text("A paragraph with "), text("bold", map[string]any{"type": "strong"}),
			text(" and "), text("link", map[string]any{"type": "link", "attrs": map[string]any{"href": "https://example.com"}}), text(".")),
		node("blockquote", nil, node("paragraph", nil, text("quoted"))),
		node("code_block", map[string]any{"language": "go"}, text("func main() {}")),
		node("divider", nil),
		node("bullet_list", nil,
			node("list_item", nil, node("paragraph", nil, text("one"))),
			node("list_item", nil, node("paragraph", nil, text("two")))),
		node("ordered_list", map[string]any{"start": 3},
			node("list_item", nil, node("paragraph", nil, text("three")))),
		node("task_list", nil,
			node("task_item", map[string]any{"checked": true}, node("paragraph", nil, text("done"))),
			node("task_item", map[string]any{"checked": false}, node("paragraph", nil, text("todo")))),
		node("callout", map[string]any{"variant": "warn", "icon": "⚠"},
			node("paragraph", nil, text("careful"))),
		node("toggle", map[string]any{"open": true},
			node("toggle_summary", nil, text("summary line")),
			node("content", nil, node("paragraph", nil, text("body")))),
		node("columns", map[string]any{"count": 2},
			node("column", nil, node("paragraph", nil, text("left"))),
			node("column", nil, node("paragraph", nil, text("right")))),
		node("image", map[string]any{"src": "https://example.com/a.png", "alt": "pic", "title": "T", "width": 100}),
		node("table", nil,
			node("table_row", nil,
				node("table_header", map[string]any{"colspan": 2}, node("paragraph", nil, text("H")))),
			node("table_row", nil,
				node("table_cell", nil, node("paragraph", nil, text("c1"))),
				node("table_cell", nil, node("paragraph", nil, text("c2"))))),
	)
	out := Render(d)

	mustContain(t, out, `data-fui-comp="wysiwyg-read"`)
	mustContain(t, out, "<h2>Title</h2>")
	mustContain(t, out, "<strong>bold</strong>")
	mustContain(t, out, `<a href="https://example.com" rel="noopener noreferrer" target="_blank">link</a>`)
	mustContain(t, out, "<blockquote>")
	mustContain(t, out, `<pre><code class="language-go">func main() {}</code></pre>`)
	mustContain(t, out, "<hr")
	mustContain(t, out, "<ul>", "<li>one</li>")
	mustContain(t, out, `<ol start="3">`)
	mustContain(t, out, `class="wysiwyg-task-list"`)
	mustContain(t, out, `aria-checked="true" checked="" disabled="" type="checkbox"`)
	mustContain(t, out, `class="wysiwyg-callout wysiwyg-callout--warn" data-variant="warn"`)
	mustContain(t, out, `<details class="wysiwyg-toggle" open="">`)
	mustContain(t, out, "<summary ", "summary line", "body")
	mustContain(t, out, `class="wysiwyg-columns" data-count="2"`)
	mustContain(t, out, `<img alt="pic" src="https://example.com/a.png" title="T" width="100">`)
	mustContain(t, out, `<table class="wysiwyg-table">`)
	mustContain(t, out, "<thead>", `<th colspan="2">H</th>`)
	mustContain(t, out, "<tbody>", "<td>c1</td>", "<td>c2</td>")
}

// --- XSS: text + attributes are escaped ----------------------------------

func TestRenderEscapesText(t *testing.T) {
	d := doc(node("paragraph", nil, text("<script>alert(1)</script> & \"quotes\" 'apos'")))
	out := Render(d)
	mustNotContain(t, out, "<script>")
	mustContain(t, out, "&lt;script&gt;alert(1)&lt;/script&gt;")
	mustContain(t, out, "&amp;")
	mustContain(t, out, "&quot;")
	mustContain(t, out, "&#39;")
}

func TestRenderEscapesImageAttrs(t *testing.T) {
	d := doc(node("image", map[string]any{
		"src":   "https://e.com/x.png",
		"alt":   `"><img src=x onerror=alert(1)>`,
		"title": `"break"`,
	}))
	out := Render(d)
	// The injected tag is fully escaped inside the alt attribute — no real
	// second <img> tag and no live onerror attribute handler is created.
	mustContain(t, out, "&lt;img src=x onerror=alert(1)&gt;")
	if c := strings.Count(string(out), "<img"); c != 1 {
		t.Errorf("expected exactly one real <img> tag, got %d\n%s", c, string(out))
	}
	mustContain(t, out, "&quot;break&quot;")
}

// --- Sanitization: dangerous href/src dropped ----------------------------

func TestRenderDropsJavascriptHref(t *testing.T) {
	d := doc(node("paragraph", nil,
		text("safe", map[string]any{"type": "link", "attrs": map[string]any{"href": "javascript:alert(1)"}}),
		text("ok", map[string]any{"type": "link", "attrs": map[string]any{"href": "https://good.example.com"}}),
	))
	out := Render(d)
	mustNotContain(t, out, "javascript:")
	// The unsafe link's text survives, but no anchor wraps it.
	mustContain(t, out, "safe")
	mustContain(t, out, `<a href="https://good.example.com`)
}

func TestRenderDropsOtherSchemes(t *testing.T) {
	for _, scheme := range []string{"data:text/html,<x>", "vbscript:foo", "file:///etc/passwd"} {
		d := doc(node("image", map[string]any{"src": scheme}))
		out := Render(d)
		mustNotContain(t, out, "<img")
	}
}

func TestRenderAllowsRelativeHref(t *testing.T) {
	for _, href := range []string{"/path", "#frag", "?q=1", "page.html", "mailto:a@b.com"} {
		d := doc(node("paragraph", nil, text("x", map[string]any{"type": "link", "attrs": map[string]any{"href": href}})))
		out := Render(d)
		mustContain(t, out, `href="`+strings.SplitN(href, ":", 2)[0])
	}
}

// --- Unknown node: graceful passthrough ----------------------------------

func TestRenderUnknownNodePassthrough(t *testing.T) {
	d := doc(
		node("future_block", nil, node("paragraph", nil, text("from a newer schema"))),
		node("paragraph", nil, text("after")),
	)
	out := Render(d)
	// Unknown block renders its content; nothing panics.
	mustContain(t, out, "from a newer schema")
	mustContain(t, out, "after")
	mustNotContain(t, out, "future_block")
}

func TestRenderUnknownMarkIgnored(t *testing.T) {
	d := doc(node("paragraph", nil, text("kept", map[string]any{"type": "sparkle", "attrs": map[string]any{"x": 1}})))
	out := Render(d)
	mustContain(t, out, "kept")
}

// --- Color slots: valid token ref, invalid dropped -----------------------

func TestRenderColorSlots(t *testing.T) {
	d := doc(node("paragraph", nil,
		text("blue", map[string]any{"type": "textColor", "attrs": map[string]any{"color": "blue"}}),
		text("hl", map[string]any{"type": "bgColor", "attrs": map[string]any{"color": "yellow"}}),
		text("bad", map[string]any{"type": "textColor", "attrs": map[string]any{"color": "#ff0000"}}),
	))
	out := Render(d)
	mustContain(t, out, `style="color:var(--wysiwyg-fg-blue,var(--color-primary))"`)
	// Highlight carries its own dark ink so it stays legible on the light
	// tint in dark mode too (parity with the editor's bgColor toDOM).
	mustContain(t, out, `style="background-color:var(--wysiwyg-bg-yellow,var(--color-warning));color:var(--wysiwyg-bg-ink,#1b1f24)"`)
	// Invalid slot (raw hex) is dropped — text kept, no style attribute.
	mustContain(t, out, "bad")
	mustNotContain(t, out, "#ff0000")
	mustNotContain(t, out, "--wysiwyg-fg-#")
}

// --- Mark ordering: link outermost, code innermost -----------------------

func TestRenderMarkOrdering(t *testing.T) {
	d := doc(node("paragraph", nil, text("x",
		map[string]any{"type": "textColor", "attrs": map[string]any{"color": "red"}},
		map[string]any{"type": "code"},
		map[string]any{"type": "strong"},
		map[string]any{"type": "link", "attrs": map[string]any{"href": "https://x.example.com"}},
	)))
	out := Render(d)
	s := string(out)
	// Expect: <a ...><strong><code><span style=color...>x</span></code></strong></a>
	aIdx := strings.Index(s, "<a ")
	strongIdx := strings.Index(s, "<strong>")
	codeIdx := strings.Index(s, "<code>")
	spanIdx := strings.Index(s, "<span style=")
	if !(aIdx < strongIdx && strongIdx < codeIdx && codeIdx < spanIdx) {
		t.Errorf("mark nesting wrong: a=%d strong=%d code=%d span=%d\n%s", aIdx, strongIdx, codeIdx, spanIdx, s)
	}
}

// --- code_block escapes + no marks applied -------------------------------

func TestRenderCodeBlockEscapes(t *testing.T) {
	d := doc(node("code_block", map[string]any{"language": "<x>"}, text("a < b & c")))
	out := Render(d)
	mustContain(t, out, `class="language-x"`) // angle brackets stripped from lang class
	mustContain(t, out, "a &lt; b &amp; c")
}

// --- table: body-only when no header row, colwidth passthrough -----------

func TestRenderTableNoHeader(t *testing.T) {
	d := doc(node("table", nil,
		node("table_row", nil,
			node("table_cell", map[string]any{"colwidth": []any{float64(100), float64(200)}}, node("paragraph", nil, text("c"))),
		),
	))
	out := Render(d)
	mustNotContain(t, out, "<thead>")
	mustContain(t, out, "<tbody>")
	mustContain(t, out, `data-colwidth="100,200"`)
	mustContain(t, out, ">c</td>")
}

// --- RenderJSON: parse + render, and error on bad JSON -------------------

func TestRenderJSON(t *testing.T) {
	j := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hi"}]}]}`
	out, err := RenderJSON(j)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, out, "<p>hi</p>")
}

func TestRenderJSONBadInput(t *testing.T) {
	if _, err := RenderJSON("{not json"); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --- Empty / minimal doc --------------------------------------------------

func TestRenderEmptyDoc(t *testing.T) {
	out := Render(map[string]any{"type": "doc", "content": []any{}})
	mustContain(t, out, `class="wysiwyg-read"`)
	mustContain(t, out, "data-fui-comp")
}

// --- Determinism: same input → identical bytes ---------------------------

func TestRenderDeterministic(t *testing.T) {
	d := doc(node("paragraph", nil,
		text("b", map[string]any{"type": "strong"}),
		text("a", map[string]any{"type": "link", "attrs": map[string]any{"href": "/x", "title": "t"}}),
	))
	a := string(Render(d))
	b := string(Render(d))
	if a != b {
		t.Fatalf("non-deterministic output\nA=%s\nB=%s", a, b)
	}
}

// --- callout variant defaulting ------------------------------------------

func TestRenderCalloutDefaultVariant(t *testing.T) {
	d := doc(node("callout", nil, node("paragraph", nil, text("x"))))
	out := Render(d)
	mustContain(t, out, "wysiwyg-callout--info")
	mustContain(t, out, `data-variant="info"`)
}
