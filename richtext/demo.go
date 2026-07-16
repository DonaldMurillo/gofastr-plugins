package richtext

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr-plugins/richtext/ssr"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoTheme returns the framework [style.DefaultTheme] with a populated
// DarkColors palette, so the demo page's light/dark toggle exercises the
// framework's data-color-scheme mechanism end to end. CSSCustomProperties()
// emits the :root{…} block INCLUDING the :root[data-color-scheme="dark"]
// overrides (protocol-v1.md §7).
// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" — the gofastr examples/site is a dark, amber-accented page,
// and this demo matches it; the toggle (and no-JS read view) can force light.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme mirrors the palette of the framework's own showcase site
// (gofastr/examples/site — the "v2" design): a warm near-black surface ladder
// with a single amber accent, expressed in oklch. The amber accent carries into
// both schemes; the full warm-dark ladder is the DarkColors override, which the
// page shows by default (schemeFromCookie). Token-driven, so the whole shell +
// the bridged editor pick these up with no per-element CSS.
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	// One amber accent, in both schemes (examples/site theme.go).
	t.Colors.Primary = style.Color{Name: "primary", Value: "oklch(0.82 0.155 78)"}
	t.Colors.PrimaryFg = style.Color{Name: "primary-fg", Value: "oklch(0.14 0.005 75)"}
	t.Colors.Accent = style.Color{Name: "accent", Value: "oklch(0.82 0.155 78)"}
	// System font stack (examples/site ships no web font under its CSP).
	t.Fonts.Body = style.Font{Name: "body", Value: "-apple-system, BlinkMacSystemFont, Inter, 'Segoe UI', system-ui, sans-serif"}
	t.Fonts.Heading = style.Font{Name: "heading", Value: "-apple-system, BlinkMacSystemFont, Inter, 'Segoe UI', system-ui, sans-serif"}
	t.Fonts.Mono = style.Font{Name: "mono", Value: "ui-monospace, SFMono-Regular, 'JetBrains Mono', Menlo, Consolas, monospace"}
	// Tighter radii ladder (examples/site uses 4/6/10).
	t.Radii.SM = style.Radius{Name: "sm", Value: 4}
	t.Radii.MD = style.Radius{Name: "md", Value: 6}
	t.Radii.LG = style.Radius{Name: "lg", Value: 10}
	// Warm near-black surface ladder + amber accent (examples/site theme.go).
	t.DarkColors = map[string]string{
		"background":    "oklch(0.135 0.005 75)",
		"surface":       "oklch(0.17 0.006 75)",
		"surface-soft":  "oklch(0.21 0.007 75)",
		"border":        "oklch(0.28 0.006 75)",
		"border-strong": "oklch(0.38 0.008 75)",
		"text":          "oklch(0.96 0.006 80)",
		"text-muted":    "oklch(0.78 0.008 75)",
		"text-subtle":   "oklch(0.66 0.010 70)",
		"primary":       "oklch(0.82 0.155 78)",
		"primary-fg":    "oklch(0.14 0.005 75)",
		"accent":        "oklch(0.82 0.155 78)",
		"code-surface":  "oklch(0.21 0.007 75)",
		"code-text":     "oklch(0.96 0.006 80)",
		"code-border":   "oklch(0.28 0.006 75)",
		// Translucent amber selection so highlighted text stays legible on dark.
		"selection": "oklch(0.82 0.155 78 / 0.30)",
	}
	return t
}

// welcomeDoc is the block-JSON document shown on first load (before anything
// has been saved). It doubles as a feature tour: headings, marks, callout,
// task list, toggle, quote — so the page demonstrates the editor instead of
// opening on an empty paragraph.
const welcomeDoc = `{"type":"doc","content":[
{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Welcome 👋"}]},
{"type":"paragraph","content":[
  {"type":"text","text":"This is a "},
  {"type":"text","marks":[{"type":"strong"}],"text":"Notion-class block editor"},
  {"type":"text","text":" running as a "},
  {"type":"text","marks":[{"type":"em"}],"text":"genuinely third-party"},
  {"type":"text","text":" plugin — isolated in a sandboxed frame, talking to this page only through a capability-scoped protocol."}
]},
{"type":"callout","attrs":{"variant":"info","icon":null},"content":[
  {"type":"paragraph","content":[
    {"type":"text","text":"Try it: type "},
    {"type":"text","marks":[{"type":"code"}],"text":"/"},
    {"type":"text","text":" for the block menu, select text for the formatting toolbar, or hover a block and drag the ⋮⋮ handle to reorder."}
  ]}
]},
{"type":"task_list","content":[
  {"type":"task_item","attrs":{"checked":true},"content":[{"type":"paragraph","content":[{"type":"text","text":"Ship the editor"}]}]},
  {"type":"task_item","attrs":{"checked":true},"content":[{"type":"paragraph","content":[{"type":"text","text":"Prove the isolation boundary (p99 keystroke ≤ 16 ms)"}]}]},
  {"type":"task_item","attrs":{"checked":false},"content":[{"type":"paragraph","content":[{"type":"text","text":"Break something and tell us"}]}]}
]},
{"type":"toggle","attrs":{"open":false},"content":[
  {"type":"toggle_summary","content":[{"type":"text","text":"What lives in the block model?"}]},
  {"type":"content","content":[{"type":"paragraph","content":[{"type":"text","text":"Headings, lists, task lists, quotes, code blocks, dividers, tables, callouts, toggles, columns, images, links, and colored text — block-JSON is canonical, markdown is the export."}]}]}
]},
{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"Everything you type autosaves — reload the page and it comes back."}]}]}
]}`

// demoPage is the self-contained HTML document served at DemoURL. It runs with
// NO UIHost: the theme tokens are inlined in <style>, the mount marker holds
// the editor iframe, and BOTH host scripts are included directly — the generic
// platform broker (pluginhost.js) first, then this plugin's adapter
// (host/broker.js). The inline <script> (toggle + submit guard) is acceptable
// ON THIS DEMO PAGE ONLY — the broker/adapter and editor stay CSP-clean and
// same-origin-script only.
//
// The %s slots are: theme tokens CSS, form action (SaveURL), mount marker
// HTML, platform broker script URL, adapter script URL.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Rich Text — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / richtext</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink is-active" href="/" aria-current="page">Sandboxed</a>
    <a class="navlink" href="/__gofastr/plugin/richtext/trusted">Trusted</a>
    <a class="navlink" href="/__gofastr/plugin/richtext/read?doc=demo">Read&nbsp;view</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>A Notion-class editor.<br>Genuinely sandboxed.</h2>
    <p class="lead">The bundle below runs in an <strong>opaque-origin iframe</strong> — it cannot read this page's cookies, storage, or DOM. Its only channel is a versioned, capability-scoped protocol. The design tokens you see crossed the boundary the same way.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">sandbox=&quot;allow-scripts&quot;</span>
      <span class="badge">p99 keystroke 8.6 ms</span>
      <span class="badge">block-JSON canonical</span>
      <span class="badge">autosaves</span>
    </p>
  </section>

  <form method="post" action="%s" id="fui-demo-form">
    <section class="editor-card">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title">demo.doc</span>
        <span class="editor-mode">sandboxed iframe</span>
      </div>
      %s
    </section>
    <div class="under-editor">
      <p class="hints hints-fine">
        <span class="hint"><kbd>/</kbd> blocks</span>
        <span class="hint"><kbd>⌘B</kbd> bold</span>
        <span class="hint"><kbd>⌘S</kbd> save</span>
        <span class="hint"><kbd>⌥↑↓</kbd> move block</span>
        <span class="hint">⋮⋮ drag to reorder</span>
      </p>
      <p class="hints hints-touch">
        <span class="hint">Type <kbd>/</kbd> for blocks</span>
        <span class="hint">Select text to format</span>
        <span class="hint">Long-press ⋮⋮ to drag</span>
      </p>
      <p class="save-row"><button type="submit" class="fui-btn fui-btn-primary">Save</button><span class="save-status" id="save-status" role="status" aria-live="polite"></span></p>
    </div>
  </form>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🔒 Secure by default</h3>
      <p>No <code>allow-same-origin</code>, ever — the frame's origin is opaque. Cookies, storage, CSRF token, host DOM: all unreachable, enforced by the browser, not by review.</p>
    </article>
    <article class="card">
      <h3>🎛 Capability-scoped</h3>
      <p>The editor holds exactly four grants — <code>document:read</code>, <code>document:write</code>, <code>upload:images</code>, <code>theme:read</code> — in the same <code>resource:verb</code> grammar as the framework's auth scopes.</p>
    </article>
    <article class="card">
      <h3>📦 Portable</h3>
      <p>Block-JSON is the source of truth; markdown exports alongside. The <a href="/__gofastr/plugin/richtext/read?doc=demo">read view</a> is server-rendered Go — your content works with JavaScript off.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · richtext %s · <a href="/__gofastr/plugin/richtext/trusted">compare the trusted (frameless) mount →</a></p>
</footer>
<script>
(function () {
  var html = document.documentElement;
  var btn = document.getElementById('fui-scheme-toggle');
  if (btn) {
    btn.addEventListener('click', function () {
      var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
      html.dataset.colorScheme = next;
      document.cookie = 'fui-color-scheme=' + next + '; path=/; max-age=31536000; samesite=lax';
    });
  }
  // Explicit Save: the editor autosaves over the broker RPC every ~2s, and the
  // broker mirrors the doc into the hidden fields on every change — so the
  // button POSTs those mirrored values and reports the result. (A production
  // host would trigger the frame's requestSave instead; the demo has no handle
  // to the frame, so it persists the already-synced mirror.)
  var form = document.getElementById('fui-demo-form');
  var status = document.getElementById('save-status');
  function setStatus(t, ok) { if (status) { status.textContent = t; status.className = 'save-status' + (ok === false ? ' is-err' : ''); } }
  if (form) form.addEventListener('submit', function (e) {
    e.preventDefault();
    var json = (form.querySelector('input[name=body_json]') || {}).value || '';
    var md = (form.querySelector('input[name=body_md]') || {}).value || '';
    if (!json) { setStatus('Nothing to save yet — start typing.', false); return; }
    setStatus('Saving…');
    fetch(form.getAttribute('action'), {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ docId: 'demo', doc: JSON.parse(json), markdown: md, schemaVersion: 'richtext-v1' })
    }).then(function (r) { setStatus(r.ok ? 'Saved ✓' : 'Save failed', r.ok); })
      .catch(function () { setStatus('Save failed', false); });
  });
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the shared page chrome for both demo pages (framed +
// trusted). Token-driven throughout — the theme toggle restyles everything.
const demoShellCSS = `
* { box-sizing: border-box; }
:root { --demo-measure: 66rem; --demo-gutter: clamp(20px, 5vw, 32px); }
body { margin: 0; font-family: var(--font-body, 'Inter', system-ui, sans-serif); background: var(--color-background, #F9FAFB); color: var(--color-text, #18181B); line-height: 1.6; -webkit-font-smoothing: antialiased; text-rendering: optimizeLegibility; }
/* Header: full-bleed band with a bottom border, inner content centered to the
   same measure as main — the framework's ui-site-header shape. */
header { position: sticky; top: 0; z-index: 60; display: flex; align-items: center; justify-content: space-between; gap: var(--spacing-md, 8px); padding-block: clamp(12px, 3vw, 18px); padding-inline: max(var(--demo-gutter), calc((100% - var(--demo-measure)) / 2)); border-bottom: 1px solid var(--color-border, #E4E4E7); background: color-mix(in srgb, var(--color-background, #F9FAFB) 85%, transparent); backdrop-filter: blur(8px); }
.brand { display: flex; align-items: center; gap: var(--spacing-md, 8px); min-width: 0; }
.brand-mark { width: 24px; height: 24px; flex: none; border-radius: var(--radii-md, 8px); background: linear-gradient(135deg, var(--color-primary, #e0a040), color-mix(in srgb, var(--color-primary, #e0a040) 45%, var(--color-text))); box-shadow: 0 2px 8px color-mix(in srgb, var(--color-primary, #e0a040) 45%, transparent); }
header h1 { font-size: var(--text-sm, .875rem); margin: 0; font-weight: 650; letter-spacing: -0.01em; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.brand-dim { color: var(--color-text-subtle, #71717A); font-weight: 500; }
nav { display: flex; align-items: center; gap: var(--spacing-sm, 4px); flex: none; }
.navlink { font-size: var(--text-sm, .875rem); text-decoration: none; color: var(--color-text-muted, #52525B); padding: 6px 12px; border-radius: var(--radii-full, 9999px); line-height: 1.2; }
.navlink:hover { color: var(--color-text, #18181B); background: var(--color-surface-soft, #F4F4F5); }
.navlink.is-active { color: var(--color-text, #18181B); background: var(--color-surface-soft, #F4F4F5); font-weight: 600; }
main { width: 100%; max-width: var(--demo-measure); margin: 0 auto; padding: clamp(32px, 5vw, 56px) var(--demo-gutter) var(--spacing-md, 8px); }
/* Hero: the framework's contained-hero typography (clamp size, -0.02em tracking,
   a comfortable measure). */
.hero { margin: 0 0 clamp(28px, 4vw, 44px); }
.hero h2 { font-size: clamp(2rem, 4vw, 2.75rem); line-height: 1.1; margin: 0 0 var(--spacing-lg, 16px); letter-spacing: -0.02em; font-weight: 700; max-width: 20ch; }
.lead { color: var(--color-text-muted, #52525B); font-size: var(--text-lg, 1.125rem); max-width: 60ch; line-height: 1.7; margin: 0 0 var(--spacing-lg, 16px); }
.badges { display: flex; flex-wrap: wrap; gap: var(--spacing-md, 8px); margin: 0; }
.badge { font-size: var(--text-xs, .75rem); font-family: var(--font-mono, monospace); color: var(--color-text-muted, #52525B); border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-full, 9999px); padding: 4px 12px; background: var(--color-surface, #fff); }
.badge-primary { color: var(--color-primary-fg, #fff); background: var(--color-primary, #e0a040); border-color: transparent; }
.editor-card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); overflow: hidden; background: var(--color-surface, #fff); box-shadow: var(--shadow-md, 0 4px 6px -1px rgba(0,0,0,.10), 0 2px 4px -1px rgba(0,0,0,.06)); }
.editor-chrome { display: flex; align-items: center; gap: 7px; padding: var(--spacing-md, 8px) var(--spacing-lg, 16px); border-bottom: 1px solid var(--color-border, #E4E4E7); background: var(--color-surface-soft, #F4F4F5); }
.dot { width: 10px; height: 10px; border-radius: 50%; opacity: .9; }
.dot-r { background: #ff5f57; } .dot-y { background: #febc2e; } .dot-g { background: #28c840; }
.editor-title { margin-left: var(--spacing-md, 8px); font-size: var(--text-xs, .75rem); font-family: var(--font-mono, monospace); color: var(--color-text-muted, #52525B); }
.editor-mode { margin-left: auto; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-full, 9999px); padding: 2px 10px; }
.under-editor { display: flex; align-items: center; justify-content: space-between; gap: var(--spacing-lg, 16px); flex-wrap: wrap; margin: var(--spacing-lg, 16px) 2px clamp(28px, 4vw, 40px); }
.under-editor p { margin: 0; }
.hints.hints-touch { display: none; }
@media (pointer: coarse) { .hints.hints-fine { display: none; } .hints.hints-touch { display: flex; } }
.hints { display: flex; flex-wrap: wrap; gap: var(--spacing-lg, 16px); color: var(--color-text-muted, #52525B); font-size: var(--text-xs, .75rem); }
.hint { display: inline-flex; align-items: center; gap: 5px; }
kbd { font-family: var(--font-mono, monospace); font-size: var(--text-xs, .75rem); border: 1px solid var(--color-border, #E4E4E7); border-bottom-width: 2px; border-radius: var(--radii-sm, 4px); padding: 1px 6px; background: var(--color-surface, #fff); color: var(--color-text, #18181B); }
.fui-btn { font: inherit; font-size: var(--text-sm, .875rem); font-weight: 500; padding: 8px 16px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); cursor: pointer; line-height: 1.2; transition: background 150ms ease, border-color 150ms ease; }
.fui-btn:hover { background: var(--color-surface-soft, #F4F4F5); border-color: var(--color-border-strong, #A1A1AA); }
.fui-btn-primary { background: var(--color-primary, #e0a040); color: var(--color-primary-fg, #fff); border-color: transparent; font-weight: 600; }
.fui-btn-primary:hover { filter: brightness(1.08); background: var(--color-primary, #e0a040); border-color: transparent; }
.save-row { display: flex; align-items: center; gap: var(--spacing-lg, 16px); }
.save-status { font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); }
.save-status.is-err { color: var(--color-danger, #d1242f); }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: var(--spacing-lg, 16px); margin: var(--spacing-sm, 4px) 0 clamp(32px, 5vw, 48px); }
.card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); padding: var(--spacing-xl, 24px); background: var(--color-surface, #fff); box-shadow: var(--shadow-sm, 0 1px 2px 0 rgba(0,0,0,.05)); }
.card h3 { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-base, 1rem); font-weight: 650; }
.card p { margin: 0; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); line-height: 1.6; }
.card a { color: var(--color-primary, #e0a040); }
code { font-family: var(--font-mono, monospace); font-size: .92em; background: var(--color-surface-soft, #F4F4F5); padding: 1px .35em; border-radius: var(--radii-sm, 4px); }
footer { border-top: 1px solid var(--color-border, #E4E4E7); margin-top: var(--spacing-md, 8px); }
footer p { max-width: var(--demo-measure); margin: 0 auto; padding: var(--spacing-xl, 24px) var(--demo-gutter); font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
footer a { color: var(--color-text-muted, #52525B); }
.optout-banner { display: flex; gap: var(--spacing-md, 8px); align-items: baseline; border: 1px solid color-mix(in srgb, var(--color-primary, #e0a040) 35%, var(--color-border)); background: color-mix(in srgb, var(--color-primary, #e0a040) 8%, var(--color-surface)); border-radius: var(--radii-lg, 12px); padding: var(--spacing-lg, 16px); margin: 0 0 clamp(20px, 3vw, 28px); font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); }
.optout-banner strong { color: var(--color-text, #18181B); }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
}
`

// readPage is the standalone SSR read view, dressed in the same shell as the
// demo pages. The read stylesheet is INLINED (ssr.ReadCSS) because this page
// ships no gofastr runtime — the registry's auto-load never fires here. The
// %s slots are: theme tokens CSS, read-view CSS, rendered document HTML,
// plugin version.
const readPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Rich Text — Read View</title>
<style>
%s
{{SHELL_CSS}}
.read-card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); background: var(--color-surface, #fff); padding: clamp(20px, 3vw, 36px); box-shadow: var(--shadow-md, 0 4px 6px -1px rgba(0,0,0,.10), 0 2px 4px -1px rgba(0,0,0,.06)); margin-bottom: var(--spacing-2xl, 32px); }
%s
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / richtext</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Sandboxed</a>
    <a class="navlink" href="/__gofastr/plugin/richtext/trusted">Trusted</a>
    <a class="navlink is-active" href="/__gofastr/plugin/richtext/read?doc=demo" aria-current="page">Read&nbsp;view</a>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>The no-JS read view.</h2>
    <p class="lead">This is the same document, rendered <strong>entirely in Go</strong> from the canonical block-JSON — no editor bundle, no runtime, works with JavaScript disabled. Same tokens, same look.</p>
  </section>
  <article class="read-card">
%s
  </article>
</main>
<footer>
  <p>gofastr-plugins · richtext %s · server-rendered by <code>richtext/ssr</code> · <a href="/">edit this document →</a></p>
</footer>
</body>
</html>`

// NOTE deliberately NO <script> on this page — the read view's invariant is
// "real content with JavaScript off" and readview_test.go pins it. The theme
// follows the host's data-color-scheme; there is no client toggle here.

// renderReadPage dresses the SSR-rendered document in the shared demo shell.
func (p *Plugin) renderReadPage(r *http.Request, body render.HTML) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	page := fmt.Sprintf(readPage, schemeFromCookie(r), tokens, ssr.ReadCSS(), string(body), Version)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}

// renderDemo builds the self-contained demo page. The last-saved doc (if any)
// is server-rendered into the mount marker so a reload round-trips the JSON;
// first-ever load gets the welcome/feature-tour document instead of an empty
// paragraph.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	doc, ok := p.LoadDoc(r.Context(), defaultDocID)
	if !ok {
		doc = welcomeDoc
	}
	mount := Mount(MountConfig{DocID: defaultDocID, Doc: doc})
	// The shell CSS contains literal "%" (color-mix percentages), so it is
	// spliced AFTER formatting — Sprintf must never see it as format input.
	page := fmt.Sprintf(demoPage, schemeFromCookie(r), tokens, SaveURL, string(mount), Version, pluginhost.BrokerScriptURL, BrokerScriptURL)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}
