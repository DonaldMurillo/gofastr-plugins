package sqlnotebook

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// SQLiteVersion is the SQLite release compiled into the embedded wasm, and
// SqlJsVersion the sql.js release the two dist files came from. Both are
// stated on the demo page, and TestDemoPageStatesTheBundledLibraryVersions
// requires them to match the lockfile and the wasm's own version string —
// mermaid's page shipped a version twelve releases stale because nothing
// checks prose.
const (
	SQLiteVersion = "3.49.1"
	SqlJsVersion  = "1.14.2"
)

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark", like every other demo page here, so the gallery and the
// plugin pages open in the same scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme is the shared showcase palette: a warm near-black surface ladder
// and one amber accent, identical to richtext's, mermaid's and pdf's demo
// themes so the pages read as one product.
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	t.Colors.Primary = style.Color{Name: "primary", Value: "#FBB636"}
	t.Colors.PrimaryFg = style.Color{Name: "primary-fg", Value: "#0A0907"}
	t.Colors.Accent = style.Color{Name: "accent", Value: "#FBB636"}
	t.Fonts.Body = style.Font{Name: "body", Value: "-apple-system, BlinkMacSystemFont, Inter, 'Segoe UI', system-ui, sans-serif"}
	t.Fonts.Heading = style.Font{Name: "heading", Value: "-apple-system, BlinkMacSystemFont, Inter, 'Segoe UI', system-ui, sans-serif"}
	t.Fonts.Mono = style.Font{Name: "mono", Value: "ui-monospace, SFMono-Regular, 'JetBrains Mono', Menlo, Consolas, monospace"}
	t.Radii.SM = style.Radius{Name: "sm", Value: 4}
	t.Radii.MD = style.Radius{Name: "md", Value: 6}
	t.Radii.LG = style.Radius{Name: "lg", Value: 10}
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
		"selection":     "#FBB6364D",
	}
	return t
}

// demoPage is the self-contained HTML document served at DemoURL, on the shared
// demo-page shell (docs/demo-page-design.md), class-for-class with richtext's,
// mermaid's and pdf's demos.
//
// The %s slots, in document order: color scheme, theme tokens CSS, SQLite
// version, sql.js version, mount marker HTML, SQLite version again, sql.js
// version again, plugin version, platform broker URL, adapter URL. Passing
// the mount one position early puts the iframe inside the fact chips and
// every test still passes, so TestDemoPageMountsInsideTheEditorCard asserts
// the shape.
//
// The shell CSS is spliced in AFTER formatting ({{SHELL_CSS}}) because it
// contains literal "%" (color-mix percentages) that Sprintf must never see as
// format verbs — the trap that broke pdf's demo.go once already.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr SQL Notebook - Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / sqlnotebook</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/sqlnotebook" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>A real SQL engine.<br>In a frame with no network.</h2>
    <p class="lead">Type a query and press Enter. It executes on SQLite compiled to WebAssembly, inside a frame whose policy says <code>connect-src 'none'</code>: the code holding your data cannot open a socket, read a cookie, or reach this page. Even the engine arrives as raw bytes over postMessage, because the frame is forbidden from fetching the one file it is made of.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">&apos;wasm-unsafe-eval&apos;</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">SQLite %s · sql.js %s</span>
      <span class="badge">results cap: 500 rows</span>
    </p>
  </section>

  <section class="editor-card" aria-label="SQL notebook frame">
    <div class="editor-chrome" aria-hidden="true">
      <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
      <span class="editor-title">notebook.sql</span>
      <span class="editor-mode">sandboxed iframe</span>
    </div>
    %s
  </section>
  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint">type a query and press Enter</span>
      <span class="hint"><kbd>Shift</kbd>+<kbd>Enter</kbd> for a newline</span>
      <span class="hint">results cap at 500 rows</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Type a query and run it</span>
      <span class="hint">Results cap at 500 rows</span>
    </p>
    <p class="proof" id="sqlnb-host-status" role="status" aria-live="polite"><span class="proof-dot" aria-hidden="true"></span><span id="sqlnb-host-status-text">waiting for the frame…</span></p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🧊 One keyword wider</h3>
      <p><code>&apos;wasm-unsafe-eval&apos;</code> lets the frame compile WebAssembly and nothing else: <code>eval()</code> stays an <code>EvalError</code>, cookies and storage stay unreachable, the origin stays opaque. It is the narrowest tier that can hold an engine, and this is the first plugin here that needs it.</p>
    </article>
    <article class="card">
      <h3>📴 Nowhere to send it</h3>
      <p>The frame cannot fetch its own engine, so the host page fetches <code>sql-wasm.wasm</code> and hands the bytes over the bridge. Queries and results cross the same bridge, and there is no other way out: no fetch, no XHR, no WebSocket, no worker.</p>
    </article>
    <article class="card">
      <h3>⏱ Measured, not promised</h3>
      <p>SQLite %s (sql.js %s) initialises in 28 ms on chromium and 26 ms on webkit inside the cage. Results are capped at 500 rows with a <code>truncated</code> flag, so a runaway query cannot flood the bridge.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · sqlnotebook %s · <a href="/">all plugins →</a></p>
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
  // The live proof line. The adapter mirrors the bridge onto the iframe
  // element and window.__sqlnbDebug; this reports it where a visitor can see
  // it, so the page's central claim is the page's own measurement rather
  // than a sentence someone typed. Polled, because the mirrors are set from
  // bridge events. Mirrors used (the adapter's contract): __sqlnbReady
  // (broker handshake), __sqlnbEngineReady + __sqlnbSqliteVersion (frame's
  // sqlnb/ready), __sqlnbDebug.wasmBytes / .ready.ms / .fetchError.
  var row = document.getElementById('sqlnb-host-status');
  var text = document.getElementById('sqlnb-host-status-text');
  function tick() {
    var f = document.querySelector('iframe');
    var dbg = window.__sqlnbDebug;
    if (!f || !row || !text) return;
    if (dbg && dbg.fetchError) {
      row.className = 'proof is-err';
      text.textContent = 'engine delivery failed: ' + dbg.fetchError;
      return;
    }
    if (f.__sqlnbEngineReady) {
      row.className = 'proof is-live';
      var ms = dbg && dbg.ready ? dbg.ready.ms : 0;
      var bytes = dbg && dbg.wasmBytes ? Number(dbg.wasmBytes).toLocaleString('en-US') : '0';
      text.textContent = 'SQLite ' + f.__sqlnbSqliteVersion + ' ready in ' + ms + ' ms · engine ' + bytes + ' bytes via postMessage';
      return;
    }
    if (f.__sqlnbReady) {
      text.textContent = 'frame ready, delivering engine bytes…';
    }
  }
  setInterval(tick, 250);
  tick();
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with richtext's,
// mermaid's and pdf's demo shells so the pages are visibly the same product.
// The rules after it are this page's only additions: the live engine readout
// and a frame height that keeps the query editor plus a few result rows
// visible on a phone.
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
  /* Keep the notebook usable on a phone: the frame holds a query editor plus
     a result grid, so it needs more than the shell default. !important is
     required because the broker pins the iframe height with an inline style. */
  .editor-card iframe { height: 520px !important; }
}

.proof { display: flex; align-items: center; gap: 8px; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); font-family: var(--font-mono, monospace); }
.proof-dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--color-text-subtle, #71717A); }
.proof.is-live .proof-dot { background: var(--color-primary, #e0a040); box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-primary, #e0a040) 25%, transparent); }
.proof.is-err { color: var(--color-danger, #d1242f); }
.proof.is-err .proof-dot { background: var(--color-danger, #d1242f); box-shadow: none; }
`

// renderDemo builds the self-contained demo page.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	// The marker carries this instance's seed (WithSeed / DefaultSeed) as
	// its data-fui-plugin-doc, JSON-encoded; the adapter decodes it and
	// hands it to the frame in sqlnb/init, so the notebook opens on the
	// seeded tables.
	mount := Mount(MountConfig{Seed: p.seed})
	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then the adapter (registers with it). There is
	// no config script — the seed rides the marker, not a global.
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		demoTheme().CSSCustomProperties(),
		SQLiteVersion,
		SqlJsVersion,
		string(mount),
		SQLiteVersion,
		SqlJsVersion,
		Version,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
	)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}
