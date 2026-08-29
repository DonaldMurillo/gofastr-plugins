package mermaid

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" — the gofastr showcase pages are dark, amber-accented, and
// this demo matches them. Same cookie and default as richtext's and datagrid's
// demos, so the pages open in the same scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// NOTE: this demo's tokens are the same warm-amber palette as richtext's, but
// expressed as EXACT sRGB hex equivalents of the oklch values rather than
// oklch itself: the mermaid/monaco bundles PARSE bridged token values in
// JavaScript to compute their own theme colors, and their parsers reject
// oklch outright ("Unsupported color format"/"Illegal value for token
// color"), which killed the first render on this page. Browsers render the
// hex equivalents pixel-identically, so the page still matches the product.
// demoTheme mirrors the palette of the framework's own showcase site
// (gofastr/examples/site — the "v2" design), identical to richtext's demo
// theme so the demo pages read as one product: a warm near-black surface
// ladder with a single amber accent, expressed in oklch. The amber accent
// carries into both schemes; the full warm-dark ladder is the DarkColors
// override, which the page shows by default (schemeFromCookie). Token-driven,
// so the whole shell plus the bridged diagram pick these up with no
// per-element CSS.
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	// One amber accent, in both schemes (examples/site theme.go).
	t.Colors.Primary = style.Color{Name: "primary", Value: "#FBB636"}
	t.Colors.PrimaryFg = style.Color{Name: "primary-fg", Value: "#0A0907"}
	t.Colors.Accent = style.Color{Name: "accent", Value: "#FBB636"}
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
		"background":    "#090806",
		"surface":       "#110F0D",
		"surface-soft":  "#1A1815",
		"border":        "#2B2826",
		"border-strong": "#45423E",
		"text":          "#F4F1ED",
		"text-muted":    "#BAB7B2",
		"text-subtle":   "#96918C",
		"primary":       "#FBB636",
		"primary-fg":    "#0A0907",
		"accent":        "#FBB636",
		"code-surface":  "#1A1815",
		"code-text":     "#F4F1ED",
		"code-border":   "#2B2826",
		// Translucent amber selection so highlighted text stays legible on dark.
		"selection": "#FBB6364D",
	}
	return t
}

// demoPage is the self-contained HTML document served at DemoURL, on the
// shared demo-page shell (docs/demo-page-design.md; class-for-class with
// richtext's and datagrid's demos). It runs with NO UIHost: the theme tokens
// are inlined in <style>, the mount marker holds the diagram iframe, and the
// host scripts are included directly — the generic platform broker
// (pluginhost.js) first, then this plugin's adapter (host/adapter.js). The
// inline <script> (toggle + submit guard) is acceptable ON THIS DEMO PAGE
// ONLY — the broker/adapter and frame stay CSP-clean and same-origin-script
// only.
//
// The %s slots are: color scheme, theme tokens CSS, form action (SaveURL),
// mount marker HTML, plugin version, platform broker script URL, adapter
// script URL. The shell CSS is spliced in AFTER formatting ({{SHELL_CSS}})
// because it contains literal "%" (color-mix percentages) that Sprintf must
// never see as format verbs.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Mermaid — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / mermaid</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/mermaid" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>Diagrams from plain text.<br>The renderer lives in a cage.</h2>
    <p class="lead">The pane below is a full Mermaid editor in an <strong>opaque-origin iframe</strong>. Edit the source on the left and the preview re-renders from it, live — that round trip is the whole product. The frame cannot read this page's cookies, storage or DOM; its only channel out is the capability-scoped postMessage bridge, and everything you type autosaves across it.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">sandbox=&quot;allow-scripts&quot;</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">mermaid 11.16.1, bundled</span>
      <span class="badge">3 grants: document:read · document:write · theme:read</span>
    </p>
  </section>

  <form method="post" action="%s" id="fui-demo-form">
    <section class="editor-card" aria-label="Mermaid editor frame">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title">flowchart.mmd</span>
        <span class="editor-mode">sandboxed iframe</span>
      </div>
      %s
    </section>
    <div class="under-editor">
      <p class="hints hints-fine">
        <span class="hint">edit the source — the preview re-renders</span>
        <span class="hint">autosaves every ~2s over the bridge</span>
        <span class="hint">reload — the diagram comes back</span>
      </p>
      <p class="hints hints-touch">
        <span class="hint">Edit the source — the diagram re-renders</span>
        <span class="hint">Autosaves as you type</span>
      </p>
      <p class="save-row"><button type="submit" class="fui-btn fui-btn-primary">Save</button><span class="save-status" id="save-status" role="status" aria-live="polite"></span></p>
    </div>
  </form>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🔒 An opaque origin</h3>
      <p><code>sandbox="allow-scripts"</code> with no <code>allow-same-origin</code>: the frame's origin is null, so cookies, storage and the host DOM are unreachable by construction — the e2e suite asserts all three probes on every run.</p>
    </article>
    <article class="card">
      <h3>🎫 Three grants, no more</h3>
      <p><code>document:read</code>, <code>document:write</code>, <code>theme:read</code> — the same <code>resource:verb</code> grammar as the framework's auth scopes. There is no upload path; the diagram is the entire document.</p>
    </article>
    <article class="card">
      <h3>📴 Offline by construction</h3>
      <p>mermaid 11.16.1 is compiled into the frame as one IIFE, and the framed CSP says <code>connect-src 'none'</code> — no CDN, no font, no telemetry beacon. Diagrams render with the network unplugged.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · mermaid %s · <a href="/">all plugins →</a></p>
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
  // The diagram autosaves over the broker RPC every ~2s (the broker mirrors the
  // source into the hidden field on change); the button POSTs that mirror.
  var form = document.getElementById('fui-demo-form');
  var status = document.getElementById('save-status');
  function setStatus(t, ok) { if (status) { status.textContent = t; status.className = 'save-status' + (ok === false ? ' is-err' : ''); } }
  if (form) form.addEventListener('submit', function (e) {
    e.preventDefault();
    var src = (form.querySelector('input[name=diagram_source]') || {}).value || '';
    if (!src) { setStatus('Nothing to save yet.', false); return; }
    setStatus('Saving…');
    fetch(form.getAttribute('action'), {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ docId: 'demo', source: src, schemaVersion: 'mermaid-v1' })
    }).then(function (r) { setStatus(r.ok ? 'Saved ✓' : 'Save failed', r.ok); })
      .catch(function () { setStatus('Save failed', false); });
  });
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with richtext's demo
// shell so the pages are visibly the same product. Token-driven throughout —
// the theme toggle restyles everything, and the bridged tokens re-theme the
// diagram inside the frame.
const demoShellCSS = `
* { box-sizing: border-box; }
:root { --demo-measure: 66rem; --demo-gutter: clamp(20px, 5vw, 32px); }
body { margin: 0; font-family: var(--font-body, 'Inter', system-ui, sans-serif); background: var(--color-background, #F9FAFB); color: var(--color-text, #18181B); line-height: 1.6; -webkit-font-smoothing: antialiased; text-rendering: optimizeLegibility; }
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
.hero { margin: 0 0 clamp(28px, 4vw, 44px); }
.hero h2 { font-size: clamp(2rem, 4vw, 2.75rem); line-height: 1.1; margin: 0 0 var(--spacing-lg, 16px); letter-spacing: -0.02em; font-weight: 700; max-width: 24ch; }
.lead { color: var(--color-text-muted, #52525B); font-size: var(--text-lg, 1.125rem); max-width: 60ch; line-height: 1.7; margin: 0 0 var(--spacing-lg, 16px); }
.badges { display: flex; flex-wrap: wrap; gap: var(--spacing-md, 8px); margin: 0; }
.badge { font-size: var(--text-xs, .75rem); font-family: var(--font-mono, monospace); color: var(--color-text-muted, #52525B); border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-full, 9999px); padding: 4px 12px; background: var(--color-surface, #fff); }
.badge-primary { color: var(--color-primary-fg, #fff); background: var(--color-primary, #e0a040); border-color: transparent; }
.editor-card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); overflow: hidden; background: var(--color-surface, #fff); box-shadow: var(--shadow-md, 0 4px 6px -1px rgba(0,0,0,.10), 0 2px 4px -1px rgba(0,0,0,.06)); }
.editor-card iframe { display: block; }
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
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
  /* Keep the editor usable on a phone: shorter frame, the page still resolves
     with the strip and cards stacked below. !important is required — the
     broker pins the iframe height with an inline style. */
  .editor-card iframe { height: 380px !important; }
}
`

// defaultDiagram is the diagram shown on first-ever load (before anything has
// been saved) — the same job richtext's welcomeDoc does: the page must
// demonstrate the render, not open on an empty textarea. It draws the
// architecture it runs on.
const defaultDiagram = "graph LR\n  Host[Host page] -->|postMessage| Cage[Opaque-origin frame]\n  Cage -->|renders| SVG[SVG diagram]\n"

// renderDemo builds the self-contained demo page. The last-saved diagram (if
// any) is seeded into the mount marker so a reload round-trips the source;
// first-ever load gets the default diagram instead of an empty pane.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	source, _ := p.LoadDoc(r.Context(), defaultDocID)
	if source == "" {
		source = defaultDiagram
	}
	b, _ := json.Marshal(map[string]string{"source": source})
	doc := string(b)
	mount := Mount(MountConfig{DocID: defaultDocID, Doc: doc, MinHeight: "420px"})
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		tokens,
		SaveURL,
		string(mount),
		Version,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
	)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}
