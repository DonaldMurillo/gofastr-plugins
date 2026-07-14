// Package ssr renders canonical WYSIWYG block-JSON (a ProseMirror doc, schema
// version "wysiwyg-v1") into design-token HTML for the no-JS first paint.
//
// It is the server-side dual of the in-frame ProseMirror editor (Worker D):
// both implement the single schema in docs/design/schema-v1.md. Where the
// editor runs untrusted JS in a sandboxed iframe, this renderer is pure,
// deterministic Go — it walks the stored JSON and emits semantic, token-styled
// HTML so a read view shows real content and stays SEO-safe before hydration
// swaps in the editor.
//
// The package owns only its own presentation:
//
//   - a registered stylesheet (core-ui/registry "wysiwyg-read") supplies every
//     block style as var(--*) tokens, auto-loaded on first paint, and
//   - the only inline styles are per-node color-slot assignments of the form
//     style="color:var(--wysiwyg-fg-<slot>,var(--color-<token>))" — token refs
//     only, never a literal color (schema-v1 §3, Hard Rule 7).
//
// Unknown node or mark types never cause a failure: their content is rendered
// (or nothing, when there is none), so the read view stays forward-compatible
// with a newer editor schema.
//
// # Wiring (integration worker)
//
// A host read-view route renders stored JSON in three lines — the stylesheet
// auto-loads via the registered "wysiwyg-read" marker, no manual <link>:
//
//	docJSON, _ := store.Load(ctx, id)
//	html, _ := ssr.RenderJSON(docJSON)
//	render.RespondHTML(w, r, html)
//
// Render never panics on malformed input; RenderJSON returns an error only when
// the JSON itself is unparseable.
package ssr
