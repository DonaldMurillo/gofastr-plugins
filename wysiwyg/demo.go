package wysiwyg

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr-plugins/wysiwyg/ssr"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoTheme returns the framework [style.DefaultTheme] with a populated
// DarkColors palette, so the demo page's light/dark toggle exercises the
// framework's data-color-scheme mechanism end to end. CSSCustomProperties()
// emits the :root{…} block INCLUDING the :root[data-color-scheme="dark"]
// overrides (protocol-v1.md §7).
// schemeFromCookie reads the persisted color scheme (set by the demo toggle),
// defaulting to "light". Lets the no-JS read view honor the user's choice
// server-side instead of flash-banging back to light.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "dark" {
		return "dark"
	}
	return "light"
}

func demoTheme() style.Theme {
	t := style.DefaultTheme()
	t.DarkColors = map[string]string{
		"background":    "#0B0B0E",
		"surface":       "#18181B",
		"surface-soft":  "#1F1F23",
		"text":          "#F4F4F5",
		"text-muted":    "#A1A1AA",
		"text-subtle":   "#71717A",
		"border":        "#27272A",
		"border-strong": "#3F3F46",
		"primary":       "#6366F1",
		"primary-fg":    "#FFFFFF",
		// Alpha selection so dark-mode selected text stays legible (a solid
		// light-blue left white text at ~1.4:1); the light palette's solid
		// value is fine on dark-on-light, but dark mode needs its own.
		"selection": "rgba(99,102,241,0.45)",
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
<title>GoFastr WYSIWYG — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / wysiwyg</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink is-active" href="/" aria-current="page">Sandboxed</a>
    <a class="navlink" href="/__gofastr/plugin/wysiwyg/trusted">Trusted</a>
    <a class="navlink" href="/__gofastr/plugin/wysiwyg/read?doc=demo">Read&nbsp;view</a>
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
      <p>Block-JSON is the source of truth; markdown exports alongside. The <a href="/__gofastr/plugin/wysiwyg/read?doc=demo">read view</a> is server-rendered Go — your content works with JavaScript off.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · wysiwyg %s · <a href="/__gofastr/plugin/wysiwyg/trusted">compare the trusted (frameless) mount →</a></p>
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
      body: JSON.stringify({ docId: 'demo', doc: JSON.parse(json), markdown: md, schemaVersion: 'wysiwyg-v1' })
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
body { margin: 0; font-family: var(--font-body, system-ui, sans-serif); background: var(--color-background); color: var(--color-text); line-height: 1.55; }
header { position: sticky; top: 0; z-index: 60; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px clamp(16px, 4vw, 32px); border-bottom: 1px solid var(--color-border); background: color-mix(in srgb, var(--color-background) 86%, transparent); backdrop-filter: blur(8px); }
.brand { display: flex; align-items: center; gap: 10px; min-width: 0; }
.brand-mark { width: 22px; height: 22px; flex: none; border-radius: 7px; background: linear-gradient(135deg, var(--color-primary, #6366F1), color-mix(in srgb, var(--color-primary, #6366F1) 45%, var(--color-text))); box-shadow: 0 2px 8px color-mix(in srgb, var(--color-primary, #6366F1) 45%, transparent); }
header h1 { font-size: 15px; margin: 0; font-weight: 700; letter-spacing: .01em; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.brand-dim { color: var(--color-text-muted); font-weight: 500; }
nav { display: flex; align-items: center; gap: 6px; flex: none; }
.navlink { font-size: 13px; text-decoration: none; color: var(--color-text-muted); padding: 6px 10px; border-radius: 999px; }
.navlink:hover { color: var(--color-text); background: var(--color-surface-soft); }
.navlink.is-active { color: var(--color-text); background: var(--color-surface-soft); font-weight: 600; }
main { max-width: 920px; margin: 0 auto; padding: clamp(20px, 4vw, 40px) clamp(16px, 4vw, 32px) 8px; }
.hero { margin: 8px 0 26px; }
.hero h2 { font-size: clamp(26px, 4.5vw, 40px); line-height: 1.12; margin: 0 0 12px; letter-spacing: -0.02em; }
.lead { color: var(--color-text-muted); max-width: 62ch; margin: 0 0 14px; }
.badges { display: flex; flex-wrap: wrap; gap: 8px; margin: 0; }
.badge { font-size: 12px; font-family: var(--font-mono, monospace); color: var(--color-text-muted); border: 1px solid var(--color-border); border-radius: 999px; padding: 4px 10px; background: var(--color-surface); }
.badge-primary { color: var(--color-primary-fg, #fff); background: var(--color-primary, #6366F1); border-color: transparent; }
.editor-card { border: 1px solid var(--color-border); border-radius: 14px; overflow: hidden; background: var(--color-surface); box-shadow: 0 1px 2px rgba(0,0,0,.06), 0 12px 32px -18px rgba(0,0,0,.25); }
.editor-chrome { display: flex; align-items: center; gap: 7px; padding: 9px 14px; border-bottom: 1px solid var(--color-border); background: var(--color-surface-soft); }
.dot { width: 11px; height: 11px; border-radius: 50%; opacity: .9; }
.dot-r { background: #ff5f57; } .dot-y { background: #febc2e; } .dot-g { background: #28c840; }
.editor-title { margin-left: 8px; font-size: 12px; font-family: var(--font-mono, monospace); color: var(--color-text-muted); }
.editor-mode { margin-left: auto; font-size: 11px; color: var(--color-text-muted); border: 1px solid var(--color-border); border-radius: 999px; padding: 2px 9px; }
.under-editor { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin: 12px 2px 30px; }
.under-editor p { margin: 0; }
.hints.hints-touch { display: none; }
@media (pointer: coarse) { .hints.hints-fine { display: none; } .hints.hints-touch { display: flex; } }
.hints { display: flex; flex-wrap: wrap; gap: 12px; color: var(--color-text-muted); font-size: 12.5px; }
.hint { display: inline-flex; align-items: center; gap: 5px; }
kbd { font-family: var(--font-mono, monospace); font-size: 11px; border: 1px solid var(--color-border); border-bottom-width: 2px; border-radius: 5px; padding: 1px 6px; background: var(--color-surface); }
.fui-btn { font: inherit; font-size: 13.5px; padding: 7px 14px; border: 1px solid var(--color-border); border-radius: 9px; background: var(--color-surface); color: var(--color-text); cursor: pointer; }
.fui-btn:hover { background: var(--color-surface-soft); }
.fui-btn-primary { background: var(--color-primary, #6366F1); color: var(--color-primary-fg, #fff); border-color: transparent; font-weight: 600; }
.fui-btn-primary:hover { filter: brightness(1.08); background: var(--color-primary, #6366F1); }
.save-row { display: flex; align-items: center; gap: 12px; }
.save-status { font-size: 13px; color: var(--color-text-muted); }
.save-status.is-err { color: var(--color-danger, #d1242f); }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(230px, 1fr)); gap: 14px; margin: 4px 0 36px; }
.card { border: 1px solid var(--color-border); border-radius: 12px; padding: 16px 18px; background: var(--color-surface); }
.card h3 { margin: 0 0 8px; font-size: 14.5px; }
.card p { margin: 0; font-size: 13.5px; color: var(--color-text-muted); }
.card a { color: var(--color-primary, #6366F1); }
code { font-family: var(--font-mono, monospace); font-size: .92em; background: var(--color-surface-soft); padding: 0 .3em; border-radius: 4px; }
footer { border-top: 1px solid var(--color-border); margin-top: 8px; }
footer p { max-width: 920px; margin: 0 auto; padding: 16px clamp(16px, 4vw, 32px); font-size: 12.5px; color: var(--color-text-muted); }
footer a { color: var(--color-text-muted); }
.optout-banner { display: flex; gap: 10px; align-items: baseline; border: 1px solid color-mix(in srgb, var(--color-primary, #6366F1) 35%, var(--color-border)); background: color-mix(in srgb, var(--color-primary, #6366F1) 8%, var(--color-surface)); border-radius: 12px; padding: 12px 16px; margin: 0 0 22px; font-size: 13.5px; color: var(--color-text-muted); }
.optout-banner strong { color: var(--color-text); }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 8px; }
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
<title>GoFastr WYSIWYG — Read View</title>
<style>
%s
{{SHELL_CSS}}
.read-card { border: 1px solid var(--color-border); border-radius: 14px; background: var(--color-surface); padding: clamp(18px, 3vw, 34px); box-shadow: 0 1px 2px rgba(0,0,0,.06), 0 12px 32px -18px rgba(0,0,0,.25); margin-bottom: 32px; }
%s
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / wysiwyg</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Sandboxed</a>
    <a class="navlink" href="/__gofastr/plugin/wysiwyg/trusted">Trusted</a>
    <a class="navlink is-active" href="/__gofastr/plugin/wysiwyg/read?doc=demo" aria-current="page">Read&nbsp;view</a>
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
  <p>gofastr-plugins · wysiwyg %s · server-rendered by <code>wysiwyg/ssr</code> · <a href="/">edit this document →</a></p>
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
