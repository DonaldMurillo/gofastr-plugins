package ssr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/gofastr/core/render"
)

// Render turns a canonical ProseMirror doc (as map[string]any, e.g. the result
// of json.Unmarshal of the stored blob) into design-token HTML for the no-JS
// read view. Pure and deterministic: the same doc always yields the same bytes.
func Render(doc map[string]any) render.HTML {
	blocks := renderBlocks(contentOf(doc))
	return readStyle.WrapHTML(render.Tag("div", map[string]string{"class": "richtext-read"}, blocks...))
}

// RenderJSON is a convenience over Render that accepts a raw JSON string.
func RenderJSON(docJSON string) (render.HTML, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(docJSON), &doc); err != nil {
		return "", fmt.Errorf("ssr: invalid doc JSON: %w", err)
	}
	return Render(doc), nil
}

// ─── Blocks ──────────────────────────────────────────────────────────

// renderBlocks renders a slice of nodes as top-level block HTML. Text nodes
// encountered here (an unusual but possible position) render as inline text.
func renderBlocks(nodes []any) []render.HTML {
	out := make([]render.HTML, 0, len(nodes))
	for _, n := range nodes {
		node, ok := n.(map[string]any)
		if !ok {
			continue
		}
		if nodeType(node) == "text" {
			out = append(out, renderMarks(textOf(node), marksOf(node)))
			continue
		}
		if h, ok := renderBlock(node); ok {
			out = append(out, h)
		}
	}
	return out
}

// renderBlock renders a single block node. The bool is false when the node
// produces no output (e.g. an image with a dropped src) and should be skipped.
func renderBlock(node map[string]any) (render.HTML, bool) {
	switch nodeType(node) {
	case "paragraph":
		return render.Tag("p", alignAttrs(node), renderInline(contentOf(node))), true
	case "heading":
		return render.Tag("h"+headingLevel(node), alignAttrs(node), renderInline(contentOf(node))), true
	case "blockquote":
		return render.Tag("blockquote", nil, renderBlocks(contentOf(node))...), true
	case "code_block":
		return renderCodeBlock(node), true
	case "divider":
		return render.VoidTag("hr", nil), true
	case "bullet_list":
		return render.Tag("ul", nil, renderListItems(contentOf(node))...), true
	case "ordered_list":
		attrs := map[string]string{}
		if start := attrInt(node, "start", 1); start != 1 {
			attrs["start"] = strconv.Itoa(start)
		}
		return render.Tag("ol", attrs, renderListItems(contentOf(node))...), true
	case "list_item":
		return render.Tag("li", nil, renderUnwrapped(node)), true
	case "task_list":
		return render.Tag("ul", map[string]string{"class": "richtext-task-list"}, renderTaskItems(contentOf(node))...), true
	case "task_item":
		return render.Tag("li", map[string]string{"class": "richtext-task-item"}, renderTaskItem(node)...), true
	case "callout":
		return renderCallout(node), true
	case "toggle":
		return renderToggle(node), true
	case "toggle_summary":
		// Standalone (unexpected) summary: render its inline content.
		return render.Tag("div", nil, renderInline(contentOf(node))), true
	case "content":
		// Structural wrapper (e.g. a toggle body). Unwrap to its blocks.
		return render.Join(renderBlocks(contentOf(node))...), true
	case "columns":
		return renderColumns(node), true
	case "column":
		return render.Tag("div", map[string]string{"class": "richtext-column"}, renderBlocks(contentOf(node))...), true
	case "image":
		return renderImage(node)
	case "table":
		return renderTable(node), true
	default:
		// Forward-compatible: an unknown block renders its content (or nothing).
		kids := contentOf(node)
		if len(kids) == 0 {
			return "", false
		}
		return render.Join(renderBlocks(kids)...), true
	}
}

func renderCodeBlock(node map[string]any) render.HTML {
	attrs := map[string]string{}
	lang := strings.TrimSpace(attrString(node, "language"))
	if lang != "" {
		attrs["class"] = "language-" + sanitizeLanguageClass(lang)
	}
	// code_block text carries no marks; concatenate raw text fields, then split
	// into highlighted spans (falling back to a single escaped run for plain /
	// unsupported languages). Highlighting mirrors the editor decorations so the
	// read view and the live editor look the same.
	return render.Tag("pre", nil, render.Tag("code", attrs, highlightHTML(codeBlockText(node), lang)...))
}

// highlightHTML splits code into escaped default runs interleaved with
// <span class="hl-<type>"> token runs, using the shared tokenizer. Every run —
// token or default — is escaped via render.Text, so the output is injection-safe
// regardless of the code's content. Unknown/plain languages yield one escaped
// text node (identical to the pre-highlighting behaviour).
func highlightHTML(code, lang string) []render.HTML {
	toks := highlightCode(code, lang)
	if len(toks) == 0 {
		return []render.HTML{render.Text(code)}
	}
	out := make([]render.HTML, 0, len(toks)*2+1)
	pos := 0
	for _, t := range toks {
		if t.From > pos {
			out = append(out, render.Text(code[pos:t.From]))
		}
		out = append(out, render.Tag("span", map[string]string{"class": "hl-" + string(t.Type)}, render.Text(code[t.From:t.To])))
		pos = t.To
	}
	if pos < len(code) {
		out = append(out, render.Text(code[pos:]))
	}
	return out
}

func renderListItems(items []any) []render.HTML {
	out := make([]render.HTML, 0, len(items))
	for _, it := range items {
		node, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if nodeType(node) != "list_item" {
			continue
		}
		out = append(out, render.Tag("li", nil, renderUnwrapped(node)))
	}
	return out
}

func renderTaskItems(items []any) []render.HTML {
	out := make([]render.HTML, 0, len(items))
	for _, it := range items {
		node, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if nodeType(node) != "task_item" {
			continue
		}
		// Each item keeps its <li class="richtext-task-item"> wrapper — the
		// checkbox/text row layout is keyed off it (dropping it stacked the
		// checkbox above the text; caught by the dogfood shots).
		out = append(out, render.Tag("li", map[string]string{"class": "richtext-task-item"}, renderTaskItem(node)...))
	}
	return out
}

func renderTaskItem(node map[string]any) []render.HTML {
	checked := attrBool(node, "checked", false)
	cbAttrs := map[string]string{
		"type":     "checkbox",
		"disabled": "",
	}
	if checked {
		cbAttrs["checked"] = ""
		cbAttrs["aria-checked"] = "true"
	} else {
		cbAttrs["aria-checked"] = "false"
	}
	return []render.HTML{
		render.VoidTag("input", cbAttrs),
		render.Tag("div", nil, renderUnwrapped(node)),
	}
}

func renderCallout(node map[string]any) render.HTML {
	variant := normalizeVariant(attrString(node, "variant"))
	attrs := map[string]string{
		"class":        "richtext-callout richtext-callout--" + variant,
		"data-variant": variant,
	}
	body := renderBlocks(contentOf(node))
	if icon := strings.TrimSpace(attrString(node, "icon")); icon != "" {
		// Icon is rendered text (emoji/symbol); escape it.
		iconHTML := render.Tag("span", map[string]string{"class": "richtext-callout__icon", "aria-hidden": "true"}, render.Text(icon))
		body = append([]render.HTML{iconHTML}, body...)
	}
	return render.Tag("div", attrs, body...)
}

func renderToggle(node map[string]any) render.HTML {
	attrs := map[string]string{"class": "richtext-toggle"}
	if attrBool(node, "open", false) {
		attrs["open"] = ""
	}
	var summary render.HTML
	var body []render.HTML
	summarySet := false
	for _, c := range contentOf(node) {
		cn, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if nodeType(cn) == "toggle_summary" {
			summary = render.Tag("summary", map[string]string{"class": "richtext-toggle__summary"}, renderInline(contentOf(cn)))
			summarySet = true
			continue
		}
		if h, ok := renderBlock(cn); ok {
			body = append(body, h)
		}
	}
	if !summarySet {
		summary = render.Tag("summary", map[string]string{"class": "richtext-toggle__summary"}, render.Text("Details"))
	}
	return render.Tag("details", attrs, summary, render.Tag("div", map[string]string{"class": "richtext-toggle__body"}, body...))
}

func renderColumns(node map[string]any) render.HTML {
	count := attrInt(node, "count", 2)
	if count != 3 {
		count = 2
	}
	var cols []render.HTML
	for _, c := range contentOf(node) {
		cn, ok := c.(map[string]any)
		if !ok || nodeType(cn) != "column" {
			continue
		}
		cols = append(cols, render.Tag("div", map[string]string{"class": "richtext-column"}, renderBlocks(contentOf(cn))...))
	}
	attrs := map[string]string{"class": "richtext-columns", "data-count": strconv.Itoa(count)}
	return render.Tag("div", attrs, cols...)
}

func renderImage(node map[string]any) (render.HTML, bool) {
	src := sanitizeURL(attrString(node, "src"))
	if src == "" {
		// Untrusted/unsupported scheme (e.g. javascript:) — drop entirely.
		return "", false
	}
	attrs := map[string]string{"src": src}
	if alt := attrString(node, "alt"); alt != "" {
		attrs["alt"] = alt
	} else {
		attrs["alt"] = ""
	}
	if title := attrString(node, "title"); title != "" {
		attrs["title"] = title
	}
	if w := attrInt(node, "width", 0); w > 0 {
		attrs["width"] = strconv.Itoa(w)
	}
	return render.VoidTag("img", attrs), true
}

func renderTable(node map[string]any) render.HTML {
	var thead, tbody []render.HTML
	inHeader := true
	for _, r := range contentOf(node) {
		rn, ok := r.(map[string]any)
		if !ok || nodeType(rn) != "table_row" {
			continue
		}
		row := renderTableRow(rn)
		if inHeader && rowIsAllHeader(rn) {
			thead = append(thead, row)
		} else {
			inHeader = false
			tbody = append(tbody, row)
		}
	}
	var children []render.HTML
	if len(thead) > 0 {
		children = append(children, render.Tag("thead", nil, thead...))
	}
	if len(tbody) == 0 && len(thead) > 0 {
		// A header-only table still needs a body for valid layout.
		children = append(children, render.Tag("tbody", nil))
	} else if len(tbody) > 0 {
		children = append(children, render.Tag("tbody", nil, tbody...))
	}
	return render.Tag("table", map[string]string{"class": "richtext-table"}, children...)
}

func renderTableRow(node map[string]any) render.HTML {
	var cells []render.HTML
	for _, c := range contentOf(node) {
		cn, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t := nodeType(cn)
		if t != "table_header" && t != "table_cell" {
			continue
		}
		cells = append(cells, renderCell(cn, t == "table_header"))
	}
	return render.Tag("tr", nil, cells...)
}

func renderCell(node map[string]any, header bool) render.HTML {
	tag := "td"
	if header {
		tag = "th"
	}
	attrs := map[string]string{}
	if cs := attrInt(node, "colspan", 1); cs > 1 {
		attrs["colspan"] = strconv.Itoa(cs)
	}
	if rs := attrInt(node, "rowspan", 1); rs > 1 {
		attrs["rowspan"] = strconv.Itoa(rs)
	}
	if cw := colwidthAttr(node); cw != "" {
		attrs["data-colwidth"] = cw
	}
	return render.Tag(tag, attrs, renderUnwrapped(node))
}

// ─── Inline + marks ──────────────────────────────────────────────────

// renderInline renders inline content (text + inline nodes), applying marks.
func renderInline(nodes []any) render.HTML {
	parts := make([]render.HTML, 0, len(nodes))
	for _, n := range nodes {
		node, ok := n.(map[string]any)
		if !ok {
			continue
		}
		switch nodeType(node) {
		case "text":
			parts = append(parts, renderMarks(textOf(node), marksOf(node)))
		default:
			// Unknown inline node: render its inline content if any.
			parts = append(parts, renderInline(contentOf(node)))
		}
	}
	return render.Join(parts...)
}

// renderMarks wraps the (already-escaped) text in its marks, applied in the
// canonical order: link outermost → strong → em → underline → strike → code →
// textColor → bgColor innermost (schema-v1 §2). Unknown marks are ignored.
func renderMarks(text string, marks []any) render.HTML {
	escaped := render.Text(text)
	// Apply innermost-first: build a stable order, lowest depth applied first.
	ordered := make([]map[string]any, 0, len(marks))
	for _, m := range marks {
		mark, ok := m.(map[string]any)
		if !ok {
			continue
		}
		ordered = append(ordered, mark)
	}
	sortMarksByDepth(ordered)
	h := escaped
	for _, mark := range ordered {
		h = wrapMark(mark, h)
	}
	return h
}

// markDepth orders marks for inside-out wrapping: higher = applied first
// (innermost). Marks outside the canonical set get a neutral depth so unknown
// marks wrap just inside link/strong but never disrupt the known order.
func markDepth(t string) int {
	switch t {
	case "bgColor":
		return 8
	case "textColor":
		return 7
	case "code":
		return 6
	case "strike":
		return 5
	case "underline":
		return 4
	case "em":
		return 3
	case "strong":
		return 2
	case "link":
		return 1 // outermost
	default:
		return 9 // unknown: innermost-ish, content still rendered
	}
}

// sortMarksByDepth orders marks so the innermost (highest depth) wrap first.
// Ties (e.g. duplicate marks) keep their original relative order for stability.
func sortMarksByDepth(marks []map[string]any) {
	sort.SliceStable(marks, func(i, j int) bool {
		return markDepth(nodeType(marks[i])) > markDepth(nodeType(marks[j]))
	})
}

func wrapMark(mark map[string]any, inner render.HTML) render.HTML {
	switch nodeType(mark) {
	case "strong":
		return render.Tag("strong", nil, inner)
	case "em":
		return render.Tag("em", nil, inner)
	case "code":
		return render.Tag("code", nil, inner)
	case "strike":
		return render.Tag("del", nil, inner)
	case "underline":
		return render.Tag("u", nil, inner)
	case "link":
		href := sanitizeURL(attrString(mark, "href"))
		if href == "" {
			return inner // unsafe scheme dropped; keep the text
		}
		attrs := map[string]string{"href": href, "rel": "noopener noreferrer"}
		if isExternalURL(href) {
			attrs["target"] = "_blank"
		}
		if title := attrString(mark, "title"); title != "" {
			attrs["title"] = title
		}
		return render.Tag("a", attrs, inner)
	case "textColor":
		slot := normalizeColorSlot(attrString(mark, "color"))
		if slot == "" {
			return inner
		}
		return render.Tag("span", map[string]string{"style": "color:var(--richtext-fg-" + slot + ",var(" + fgFallback[slot] + "))"}, inner)
	case "bgColor":
		slot := normalizeColorSlot(attrString(mark, "color"))
		if slot == "" {
			return inner
		}
		// Dark ink on the light highlight tint in both schemes (parity with the
		// editor's bgColor toDOM — see schema.ts).
		return render.Tag("span", map[string]string{"style": "background-color:var(--richtext-bg-" + slot + ",var(" + bgFallback[slot] + "));color:var(--richtext-bg-ink,#1b1f24)"}, inner)
	}
	return inner
}

// ─── Sanitization & validation ───────────────────────────────────────

// sanitizeURL allows http, https, mailto, and relative URLs and drops every
// other scheme (javascript:, data:, vbscript:, file:, …). Returns "" for
// anything disallowed so callers can drop the attribute/node entirely.
func sanitizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "http:"),
		strings.HasPrefix(lower, "https:"),
		strings.HasPrefix(lower, "mailto:"):
		return s
	}
	// A colon before the first slash indicates another scheme → drop.
	if ci := strings.IndexByte(s, ':'); ci >= 0 {
		si := strings.IndexByte(s, '/')
		if si < 0 || ci < si {
			return ""
		}
	}
	// Relative path, query, fragment, or protocol-relative (//host).
	return s
}

// isExternalURL reports whether a (already-sanitized) URL points off-site
// over http(s), in which case the anchor gets target="_blank".
func isExternalURL(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// sanitizeLanguageClass keeps a code-fence language class safe for use in a
// class="language-X" attribute: only letters, digits, +, -, and . survive.
func sanitizeLanguageClass(lang string) string {
	var b strings.Builder
	for _, r := range lang {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '+', r == '-', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeColorSlot lowercases and validates against the schema-v1 §3 palette.
// Returns "" for anything outside the set; callers then drop the mark.
func normalizeColorSlot(name string) string {
	slot := strings.ToLower(strings.TrimSpace(name))
	if _, ok := colorSlots[slot]; ok {
		return slot
	}
	return ""
}

// normalizeVariant clamps a callout variant to the documented set, defaulting
// to "info".
func normalizeVariant(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "info", "warn", "success", "danger", "note":
		return strings.ToLower(strings.TrimSpace(v))
	}
	return "info"
}

// ─── Helpers (JSON map access) ───────────────────────────────────────

func nodeType(n map[string]any) string {
	if t, ok := n["type"].(string); ok {
		return t
	}
	return ""
}

func contentOf(n map[string]any) []any {
	if c, ok := n["content"].([]any); ok {
		return c
	}
	return nil
}

func textOf(n map[string]any) string {
	if t, ok := n["text"].(string); ok {
		return t
	}
	return ""
}

func marksOf(n map[string]any) []any {
	if m, ok := n["marks"].([]any); ok {
		return m
	}
	return nil
}

func attrsOf(n map[string]any) map[string]any {
	if a, ok := n["attrs"].(map[string]any); ok {
		return a
	}
	return nil
}

// alignAttrs mirrors the editor's paragraph/heading toDOM: a non-left `align`
// attr becomes an inline text-align style (left emits nothing, keeping parity).
func alignAttrs(n map[string]any) map[string]string {
	a := attrString(n, "align")
	switch a {
	case "center", "right", "justify":
		return map[string]string{"style": "text-align:" + a}
	}
	return nil
}

func attrString(n map[string]any, key string) string {
	a := attrsOf(n)
	if a == nil {
		return ""
	}
	v, ok := a[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func attrBool(n map[string]any, key string, def bool) bool {
	a := attrsOf(n)
	if a == nil {
		return def
	}
	if v, ok := a[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func attrInt(n map[string]any, key string, def int) int {
	a := attrsOf(n)
	if a == nil {
		return def
	}
	v, ok := a[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case float64:
		return int(x) // json.Unmarshal produces float64 for all numbers
	case int:
		return x
	case int64:
		return int(x)
	}
	return def
}

func headingLevel(node map[string]any) string {
	lvl := attrInt(node, "level", 1)
	if lvl < 1 {
		lvl = 1
	}
	if lvl > 6 {
		lvl = 6
	}
	return strconv.Itoa(lvl)
}

// codeBlockText concatenates the raw text of a code_block's text children.
// code_block content carries no marks, so concatenation is lossless.
func codeBlockText(node map[string]any) string {
	var b strings.Builder
	for _, c := range contentOf(node) {
		cn, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if nodeType(cn) == "text" {
			b.WriteString(textOf(cn))
		}
	}
	return b.String()
}

// renderUnwrapped renders a container that conventionally holds a single
// paragraph directly as inline content; multi-block content renders as blocks.
func renderUnwrapped(node map[string]any) render.HTML {
	kids := contentOf(node)
	if len(kids) == 1 {
		if pn, ok := kids[0].(map[string]any); ok && nodeType(pn) == "paragraph" {
			return renderInline(contentOf(pn))
		}
	}
	return render.Join(renderBlocks(kids)...)
}

// colwidthAttr renders a cell's colwidth ([int]|null) as a comma-joined
// data-colwidth attribute, or "" when absent/malformed. It is a passthrough for
// hydration/future styling — the read view uses automatic table layout.
func colwidthAttr(node map[string]any) string {
	a := attrsOf(node)
	if a == nil {
		return ""
	}
	cw, ok := a["colwidth"].([]any)
	if !ok || len(cw) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cw))
	for _, w := range cw {
		switch x := w.(type) {
		case float64:
			parts = append(parts, strconv.Itoa(int(x)))
		case int:
			parts = append(parts, strconv.Itoa(x))
		default:
			return "" // malformed → omit rather than emit garbage
		}
	}
	return strings.Join(parts, ",")
}

// rowIsAllHeader reports whether every cell in a table_row is a table_header.
func rowIsAllHeader(node map[string]any) bool {
	cells := contentOf(node)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		cn, ok := c.(map[string]any)
		if !ok || nodeType(cn) != "table_header" {
			return false
		}
	}
	return true
}
