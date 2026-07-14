package ssr

import (
	"github.com/DonaldMurillo/gofastr/core-ui/registry"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
)

// readStyle is the registered handle for the SSR read-view stylesheet.
// The runtime auto-loads its CSS the first time a data-fui-comp="wysiwyg-read"
// marker appears in the rendered page (see core-ui/ARCHITECTURE.md "Component
// CSS"), so a host route simply returns ssr.Render(doc) and the styling arrives
// with first paint — no manual <link> wiring.
var readStyle = registry.RegisterStyle("wysiwyg-read", readCSS)

// ─── Color slots (schema-v1 §3) ──────────────────────────────────────
//
// textColor/bgColor marks store a named slot, NOT hex. The renderer emits the
// slot as an inline var() assignment (the only inline-style exception):
//
//	<span style="color:var(--wysiwyg-fg-blue,var(--color-primary))">
//
// The first var is the host-supplied slot (the host token bridge, protocol §7,
// must define --wysiwyg-fg-<name> at :root); the second is a framework-token
// fallback so the read view still reads well when the host has not bridged the
// full palette. Both refs are tokens — never a literal color in the inline
// style. Anything outside this set is dropped (the mark is ignored, text kept).
var colorSlots = map[string]struct{}{
	"default": {},
	"gray":    {},
	"brown":   {},
	"orange":  {},
	"yellow":  {},
	"green":   {},
	"blue":    {},
	"purple":  {},
	"pink":    {},
	"red":     {},
}

// fgFallback maps a color slot to the framework text-token the inline var()
// chain falls back to when the host has not supplied --wysiwyg-fg-<slot>.
var fgFallback = map[string]string{
	"default": "--color-text",
	"gray":    "--color-text-subtle",
	"brown":   "--color-text-muted",
	"orange":  "--color-warning",
	"yellow":  "--color-warning",
	"green":   "--color-success",
	"blue":    "--color-primary",
	"purple":  "--color-primary",
	"pink":    "--color-primary",
	"red":     "--color-danger",
}

// bgFallback maps a color slot to the framework surface-token the inline var()
// chain falls back to for highlight (bgColor) marks.
var bgFallback = map[string]string{
	"default": "--color-surface-soft",
	"gray":    "--color-surface-soft",
	"brown":   "--color-surface-soft",
	"orange":  "--color-warning",
	"yellow":  "--color-warning",
	"green":   "--color-success",
	"blue":    "--color-primary",
	"purple":  "--color-primary",
	"pink":    "--color-primary",
	"red":     "--color-danger",
}

// ReadCSS returns the read-view stylesheet for hosts that render the read
// view on a page WITHOUT the gofastr runtime (the registry auto-load needs
// data-fui-comp scanning; a bare page must inline this instead). Same sheet
// the registry serves.
func ReadCSS() string {
	return readCSS(style.DefaultTheme())
}

func readCSS(_ style.Theme) string {
	// All styling is token-only: every value is a var(--*) with a hex literal
	// only as the deepest fallback (the framework's own convention — see
	// framework/ui/markdown.go). Zero bespoke design here; this sheet reads the
	// host palette by construction.
	return `[data-fui-comp="wysiwyg-read"] {
  /* Plugin-local color-slot tokens — SAME defaults as the editor stylesheet,
     so textColor/bgColor marks resolve identically here (without these the
     inline var() chains fell through to framework tokens and, e.g., yellow
     highlight rendered as the strong warning orange). Hosts override by
     defining --wysiwyg-* higher up. */
  --wysiwyg-fg-default: var(--color-text);
  --wysiwyg-fg-gray: #6a737d;
  --wysiwyg-fg-brown: #95602c;
  --wysiwyg-fg-orange: #bc4c00;
  --wysiwyg-fg-yellow: #b08800;
  --wysiwyg-fg-green: #1a7f37;
  --wysiwyg-fg-blue: #2f6feb;
  --wysiwyg-fg-purple: #8250df;
  --wysiwyg-fg-pink: #bf3989;
  --wysiwyg-fg-red: #d1242f;
  --wysiwyg-bg-default: transparent;
  --wysiwyg-bg-gray: #eaeef2;
  --wysiwyg-bg-brown: #f5dcc0;
  --wysiwyg-bg-orange: #ffd8b5;
  --wysiwyg-bg-yellow: #fff1a8;
  --wysiwyg-bg-green: #d4f4dd;
  --wysiwyg-bg-blue: #cfe3ff;
  --wysiwyg-bg-purple: #eadcff;
  --wysiwyg-bg-pink: #ffd9ec;
  --wysiwyg-bg-red: #ffd3d3;
  display: block;
  color: var(--color-text, #18181B);
  /* Match the editor's stack (it resolves --font-sans, never bridged, to the
     system stack) so "same tokens, same look" actually holds between the two. */
  font-family: var(--font-sans, var(--font-body, system-ui, -apple-system, sans-serif));
  font-size: var(--text-base, 1rem);
  line-height: var(--leading, 1.6);
  text-wrap: pretty;
}

/* Flow rhythm — comfortable paragraph spacing, no leading gap. */
[data-fui-comp="wysiwyg-read"] > * { margin-block: 0; }
[data-fui-comp="wysiwyg-read"] > * + * { margin-block-start: 1.15em; }
[data-fui-comp="wysiwyg-read"] > :first-child { margin-block-start: 0; }

/* Headings — section separation above, tight binding below. */
[data-fui-comp="wysiwyg-read"] h1,
[data-fui-comp="wysiwyg-read"] h2,
[data-fui-comp="wysiwyg-read"] h3,
[data-fui-comp="wysiwyg-read"] h4,
[data-fui-comp="wysiwyg-read"] h5,
[data-fui-comp="wysiwyg-read"] h6 {
  color: var(--color-text, #18181B);
  line-height: 1.25;
  text-wrap: balance;
}
[data-fui-comp="wysiwyg-read"] h1 { font-size: var(--text-3xl, 1.9rem);  font-weight: 700; letter-spacing: -0.014em; margin-block: 0 0.5em; }
[data-fui-comp="wysiwyg-read"] h2 { font-size: var(--text-2xl, 1.45rem); font-weight: 700; letter-spacing: -0.01em;  margin-block: 2.6em 0.55em; }
[data-fui-comp="wysiwyg-read"] h3 { font-size: var(--text-lg, 1.18rem);  font-weight: 650; margin-block: 1.9em 0.45em; }
[data-fui-comp="wysiwyg-read"] h4 { font-size: var(--text-base, 1rem);   font-weight: 650; margin-block: 1.5em 0.35em; }
[data-fui-comp="wysiwyg-read"] h5 { font-size: var(--text-sm, 0.92rem);  font-weight: 650; margin-block: 1.3em 0.3em; }
[data-fui-comp="wysiwyg-read"] h6 { font-size: var(--text-sm, 0.88rem);  font-weight: 650; margin-block: 1.2em 0.3em; }
[data-fui-comp="wysiwyg-read"] h2 + h3,
[data-fui-comp="wysiwyg-read"] h3 + h4,
[data-fui-comp="wysiwyg-read"] h4 + h5,
[data-fui-comp="wysiwyg-read"] h5 + h6 { margin-block-start: 0.9em; }
[data-fui-comp="wysiwyg-read"] > :first-child:is(h1,h2,h3,h4,h5,h6) { margin-block-start: 0; }

/* Inline marks. */
[data-fui-comp="wysiwyg-read"] a {
  color: var(--color-primary, #4F46E5);
  text-decoration: underline;
  text-underline-offset: 0.18em;
  text-decoration-thickness: from-font;
}
[data-fui-comp="wysiwyg-read"] strong { font-weight: 650; color: var(--color-text, #18181B); }
[data-fui-comp="wysiwyg-read"] code {
  font-family: var(--font-mono, ui-monospace, monospace);
  background: var(--color-surface-soft, #F4F4F5);
  color: var(--color-text, #18181B);
  padding: 0.12em 0.4em;
  border-radius: var(--radii-sm, 4px);
  font-size: 0.875em;
}

/* Lists. */
[data-fui-comp="wysiwyg-read"] ul,
[data-fui-comp="wysiwyg-read"] ol { padding-inline-start: 1.5em; }
[data-fui-comp="wysiwyg-read"] li { margin-block: 0; }
[data-fui-comp="wysiwyg-read"] li + li { margin-block-start: 0.4em; }
[data-fui-comp="wysiwyg-read"] li > ul,
[data-fui-comp="wysiwyg-read"] li > ol { margin-block-start: 0.4em; }
[data-fui-comp="wysiwyg-read"] li::marker { color: var(--color-text-subtle, #71717A); }
/* A paragraph unwrapped into a list item keeps its measure. */
[data-fui-comp="wysiwyg-read"] li > p { margin-block: 0; }

/* Code blocks — framed surface, same palette as the framework's code blocks. */
[data-fui-comp="wysiwyg-read"] pre {
  margin-block-start: 1.5em;
  padding: var(--spacing-md, 12px) var(--spacing-lg, 20px);
  /* Parity with the editor's code block: token surface in BOTH schemes, not a
     fixed dark panel (the read view looked nothing like the editor). */
  background: var(--color-surface-soft, #f6f8fa);
  color: var(--color-text, #1b1f24);
  border-radius: var(--radii-md, 8px);
  overflow-x: auto;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: var(--text-sm, 0.85rem);
  line-height: 1.65;
  tab-size: 2;
}
[data-fui-comp="wysiwyg-read"] pre code {
  background: transparent;
  color: inherit;
  padding: 0;
  font-size: inherit;
}

/* Blockquote. */
[data-fui-comp="wysiwyg-read"] blockquote {
  /* Parity with the editor's quote: a left rule, not a boxed panel. */
  margin: var(--spacing-sm, 8px) 0;
  padding: var(--spacing-xs, 4px) var(--spacing-md, 12px);
  border-left: 3px solid var(--color-border, #d7dde3);
  color: var(--color-text-muted, #5b6470);
}
[data-fui-comp="wysiwyg-read"] blockquote > :first-child { margin-block-start: 0; }

/* Divider. */
[data-fui-comp="wysiwyg-read"] hr {
  border: 0;
  border-block-start: 1px solid var(--color-border, #E4E4E7);
  margin-block: 2.75em;
}

/* Tables. */
[data-fui-comp="wysiwyg-read"] .wysiwyg-table {
  border-collapse: collapse;
  width: 100%;
  margin-block-start: 1.5em;
  font-size: 0.95em;
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-table th,
[data-fui-comp="wysiwyg-read"] .wysiwyg-table td {
  padding: 0.5em 0.85em;
  border-block-end: 1px solid var(--color-border, #E4E4E7);
  text-align: start;
  vertical-align: top;
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-table thead th {
  font-weight: 650;
  color: var(--color-text, #18181B);
  border-block-end-width: 2px;
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-table tbody tr:last-child td,
[data-fui-comp="wysiwyg-read"] .wysiwyg-table tbody tr:last-child th { border-block-end: 0; }

/* Images. */
[data-fui-comp="wysiwyg-read"] img {
  max-width: 100%;
  height: auto;
  border-radius: var(--radii-md, 8px);
}

/* Callouts (admonitions) — variant sets the accent rail + tint. */
[data-fui-comp="wysiwyg-read"] .wysiwyg-callout {
  padding: 0.9em 1.1em;
  border: 1px solid var(--color-border, #E4E4E7);
  border-inline-start: 3px solid var(--color-text-subtle, #71717A);
  border-radius: var(--radii-md, 8px);
  background: var(--color-surface-soft, #F4F4F5);
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-callout > :first-child { margin-block-start: 0; }
[data-fui-comp="wysiwyg-read"] .wysiwyg-callout__icon { margin-inline-end: 0.4em; }
[data-fui-comp="wysiwyg-read"] .wysiwyg-callout--info    { border-inline-start-color: var(--color-primary, #4F46E5); }
[data-fui-comp="wysiwyg-read"] .wysiwyg-callout--note    { border-inline-start-color: var(--color-text-subtle, #71717A); }
[data-fui-comp="wysiwyg-read"] .wysiwyg-callout--warn    { border-inline-start-color: var(--color-warning, #D97706); }
[data-fui-comp="wysiwyg-read"] .wysiwyg-callout--success { border-inline-start-color: var(--color-success, #16A34A); }
[data-fui-comp="wysiwyg-read"] .wysiwyg-callout--danger  { border-inline-start-color: var(--color-danger, #DC2626); }

/* Task lists — checkbox + label inline. */
[data-fui-comp="wysiwyg-read"] .wysiwyg-task-list { list-style: none; padding-inline-start: 0; }
[data-fui-comp="wysiwyg-read"] .wysiwyg-task-item {
  display: flex;
  align-items: flex-start;
  gap: var(--spacing-sm, 0.5rem);
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-task-item > input[type="checkbox"] {
  flex: none;
  margin-block-start: 0.4em;
  accent-color: var(--color-primary, #4F46E5);
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-task-item > div { flex: 1 1 auto; min-width: 0; }

/* Toggle — native <details>/<summary>, no JS. */
[data-fui-comp="wysiwyg-read"] .wysiwyg-toggle { padding: 0.5em 0; }
[data-fui-comp="wysiwyg-read"] .wysiwyg-toggle > summary {
  cursor: pointer;
  font-weight: 650;
  color: var(--color-text, #18181B);
  list-style: revert;
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-toggle > summary::-webkit-details-marker { display: revert; }
[data-fui-comp="wysiwyg-read"] .wysiwyg-toggle__body { margin-block-start: 0.6em; }
[data-fui-comp="wysiwyg-read"] .wysiwyg-toggle__body > :first-child { margin-block-start: 0; }

/* Columns — token grid; collapses to a single column on narrow viewports. */
[data-fui-comp="wysiwyg-read"] .wysiwyg-columns {
  display: grid;
  gap: var(--spacing-lg, 1.25rem);
  margin-block-start: 1.5em;
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-columns[data-count="2"] { grid-template-columns: repeat(2, 1fr); }
[data-fui-comp="wysiwyg-read"] .wysiwyg-columns[data-count="3"] { grid-template-columns: repeat(3, 1fr); }
@media (max-width: 640px) {
  [data-fui-comp="wysiwyg-read"] .wysiwyg-columns { grid-template-columns: 1fr; }
}
[data-fui-comp="wysiwyg-read"] .wysiwyg-column > :first-child { margin-block-start: 0; }

/* Color marks — the inline var() chain does the work; keep them inline-flowing
   and ensure nested code/links still inherit sensibly. */
[data-fui-comp="wysiwyg-read"] span[style] { background-clip: padding-box; }
`
}
