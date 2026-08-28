package datagrid

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoGridHeight is the iframe height the demo page mounts at: tall enough
// that the grid IS the page's argument (~12 rows visible at AG Grid's 42px
// row height plus toolbar and header), so the page resolves without dead
// space instead of stopping 400px after a 430px grid.
const demoGridHeight = "600px"

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" — the gofastr showcase pages are dark, amber-accented,
// and this demo matches them; the toggle can force light. Same cookie and
// default as richtext's demo, so the two pages open in the same scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme mirrors the palette of the framework's own showcase site
// (gofastr/examples/site — the "v2" design), identical to richtext's demo
// theme so both demo pages read as one product: a warm near-black surface
// ladder with a single amber accent, expressed in oklch. The amber accent
// carries into both schemes; the full warm-dark ladder is the DarkColors
// override, which the page shows by default (schemeFromCookie). Token-driven,
// so the whole shell plus the bridged grid pick these up with no per-element
// CSS. The status-tone overrides exist because the frame renders its status
// pills from --color-success/-warning/-danger: the framework's light values
// are tuned for tinted chips on light surfaces and go illegible on the dark
// ladder, so the dark scheme re-states them lighter.
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
		// Translucent amber selection so selected text stays legible on dark.
		"selection": "oklch(0.82 0.155 78 / 0.30)",
		// Status tones for the frame's pills, restated lighter for dark.
		"success": "oklch(0.76 0.14 158)",
		"warning": "oklch(0.82 0.12 80)",
		"danger":  "oklch(0.72 0.15 25)",
	}
	return t
}

// demoPage is the self-contained HTML document served at DemoURL. It runs
// with NO UIHost: the theme tokens are inlined in <style>, the mount marker
// holds the grid iframe, and the host scripts are included directly — the
// generic platform broker (pluginhost.js) first, then this instance's
// config.js, then the adapter. The inline <script> (toggle + live bridge
// readout) is acceptable ON THIS DEMO PAGE ONLY — the broker/adapter and the
// frame stay CSP-clean and same-origin-script only, the same allowance the
// richtext demo makes.
//
// The %s slots are: color scheme, theme tokens CSS, form action (SaveURL),
// mount marker HTML, plugin version, platform broker script URL, config
// script URL, adapter script URL. The shell CSS and the readout script are
// spliced in AFTER formatting ({{SHELL_CSS}} / {{READOUT_JS}}) because both
// contain literal "%" (color-mix percentages, the readout's percent figure)
// that Sprintf must never see as format verbs — the richtext demo's pattern.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Data Grid — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / datagrid</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/datagrid" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>A 100,000-row grid.<br>The frame never holds the table.</h2>
    <p class="lead">The grid below runs in an opaque-origin iframe whose CSP forbids every fetch — <code>connect-src 'none'</code>. Sorting, filtering and paging run in this page's Go process, and rows cross the postMessage bridge one page at a time, 500 at most. Scroll it and watch the counter: the table never crosses, only pages do.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">connect-src 'none'</span>
      <span class="badge">sandbox="allow-scripts"</span>
      <span class="badge">100,000 deterministic rows</span>
      <span class="badge">500-row page ceiling</span>
    </p>
  </section>

  <form method="post" action="%s" id="fui-demo-form">
    <section class="editor-card" aria-label="Data grid frame">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title">ledger-100k.csv</span>
        <span class="editor-mode">sandboxed iframe</span>
      </div>
      %s
    </section>
  </form>

  <section class="proof" aria-label="Live bridge telemetry">
    <p class="proof-title"><span class="proof-dot" aria-hidden="true"></span>Bridge telemetry — live</p>
    <div class="proof-grid">
      <div class="proof-stat proof-stat-lead">
        <p class="proof-number"><span id="dg-live-delivered">—</span><span class="proof-of">/ 100,000</span></p>
        <p class="proof-label">rows delivered over the bridge <span class="proof-pct" id="dg-live-pct"></span></p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="dg-live-maxpage">—</span><span class="proof-of">/ 500</span></p>
        <p class="proof-label">largest single page delivered</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="dg-live-blocks">—</span><span class="proof-of">/ <span id="dg-live-blockcap">25</span></span></p>
        <p class="proof-label">blocks resident in the frame</p>
      </div>
    </div>
  </section>
  <p class="proof-note">Straight from the adapter's bridge mirrors — scroll the grid and watch the first number move. The block cap is ⌈2,500 / pageSize⌉, so the frame evicts as fast as it loads.</p>

  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint"><kbd>Enter</kbd> apply filter</span>
      <span class="hint"><kbd>2×click</kbd> edit cell</span>
      <span class="hint">click a header — the sort runs in Go</span>
      <span class="hint">drag the scrollbar — pages arrive as you scroll</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Tap a header to sort</span>
      <span class="hint">Double-tap a cell to edit</span>
      <span class="hint">Scroll — pages arrive from the host</span>
    </p>
    <p class="save-row"><span class="save-status">view state autosaves over the bridge · CSV export runs in the host</span></p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🗄 The table never crosses</h3>
      <p>Every <code>requestRows</code> window is capped at 500 rows; beyond that the host's <code>POST /rows</code> answers <code>400 E_PAGE_TOO_LARGE</code>. A jump-scroll to row 50,000 delivers a few hundred rows — the counter above is the receipt.</p>
    </article>
    <article class="card">
      <h3>🧠 The frame forgets</h3>
      <p>AG Grid's cache is unlimited by default; this frame caps it at <code>⌈2,500 / pageSize⌉</code> blocks — 25 at the default page size — so what you scroll past is evicted, not hoarded. The resident number above is AG Grid's own <code>getCacheBlockState()</code>.</p>
    </article>
    <article class="card">
      <h3>📤 Export runs in the host</h3>
      <p>The sandbox grants no <code>allow-downloads</code>, so the frame cannot save a file. CSV is paged through the Go rows source 5,000 rows per chunk, and the host page clicks the download URL — only that URL crosses back.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · datagrid %s · <a href="/">all plugins →</a></p>
</footer>
<script>
{{READOUT_JS}}
</script>
<script src="%s"></script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with richtext's demo
// shell so the two pages are visibly the same product, plus the datagrid's
// own proof strip. Token-driven throughout — the theme toggle restyles
// everything, and the bridged tokens re-theme the grid inside the frame.
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
/* Proof strip: the volume claim, live. The numbers are the point of the
   plugin, so they get display type (tabular, clamped) rather than footnote
   grey — the amber tint keys the panel to the accent without shouting. */
.proof { margin: var(--spacing-lg, 16px) 0 0; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); background: color-mix(in srgb, var(--color-primary, #e0a040) 4%, var(--color-surface, #fff)); padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); }
.proof-title { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-xs, .75rem); font-weight: 600; letter-spacing: .08em; text-transform: uppercase; color: var(--color-text-subtle, #71717A); display: flex; align-items: center; gap: var(--spacing-md, 8px); }
.proof-dot { width: 8px; height: 8px; flex: none; border-radius: 50%; background: var(--color-success, #166534); animation: proof-pulse 2.4s ease-in-out infinite; }
@keyframes proof-pulse { 0%, 100% { opacity: 1; } 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .proof-dot { animation: none; } }
.proof-grid { display: grid; grid-template-columns: 1.5fr 1fr 1fr; gap: var(--spacing-md, 8px) var(--spacing-xl, 24px); }
.proof-number { margin: 0; font-size: clamp(1.25rem, 2vw, 1.6rem); font-weight: 700; letter-spacing: -0.02em; line-height: 1.1; font-variant-numeric: tabular-nums; color: var(--color-text, #18181B); }
.proof-stat-lead .proof-number { font-size: clamp(1.75rem, 3.5vw, 2.5rem); }
.proof-of { font-size: .45em; font-weight: 600; color: var(--color-text-muted, #52525B); letter-spacing: 0; margin-left: .4em; white-space: nowrap; }
.proof-label { margin: 4px 0 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); line-height: 1.5; }
.proof-pct { color: color-mix(in oklab, var(--color-primary, #e0a040) 60%, var(--color-text, #18181B) 40%); font-weight: 600; }
.proof-note { margin: var(--spacing-md, 8px) 2px 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
.under-editor { display: flex; align-items: center; justify-content: space-between; gap: var(--spacing-lg, 16px); flex-wrap: wrap; margin: var(--spacing-lg, 16px) 2px clamp(28px, 4vw, 40px); }
.under-editor p { margin: 0; }
.hints.hints-touch { display: none; }
@media (pointer: coarse) { .hints.hints-fine { display: none; } .hints.hints-touch { display: flex; } }
.hints { display: flex; flex-wrap: wrap; gap: var(--spacing-lg, 16px); color: var(--color-text-muted, #52525B); font-size: var(--text-xs, .75rem); }
.hint { display: inline-flex; align-items: center; gap: 5px; }
kbd { font-family: var(--font-mono, monospace); font-size: var(--text-xs, .75rem); border: 1px solid var(--color-border, #E4E4E7); border-bottom-width: 2px; border-radius: var(--radii-sm, 4px); padding: 1px 6px; background: var(--color-surface, #fff); color: var(--color-text, #18181B); }
.fui-btn { font: inherit; font-size: var(--text-sm, .875rem); font-weight: 500; padding: 8px 16px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); cursor: pointer; line-height: 1.2; transition: background 150ms ease, border-color 150ms ease; }
.fui-btn:hover { background: var(--color-surface-soft, #F4F4F5); border-color: var(--color-border-strong, #A1A1AA); }
.save-row { display: flex; align-items: center; gap: var(--spacing-lg, 16px); }
.save-status { font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: var(--spacing-lg, 16px); margin: var(--spacing-sm, 4px) 0 clamp(32px, 5vw, 48px); }
.card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); padding: var(--spacing-xl, 24px); background: var(--color-surface, #fff); box-shadow: var(--shadow-sm, 0 1px 2px 0 rgba(0,0,0,.05)); }
.card h3 { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-base, 1rem); font-weight: 650; }
.card p { margin: 0; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); line-height: 1.6; }
.card a { color: var(--color-primary, #e0a040); }
code { font-family: var(--font-mono, monospace); font-size: .92em; background: var(--color-surface-soft, #F4F4F5); padding: 1px .35em; border-radius: var(--radii-sm, 4px); }
footer { border-top: 1px solid var(--color-border, #E4E4E7); margin-top: var(--spacing-md, 8px); }
footer p { max-width: var(--demo-measure); margin: 0 auto; padding: var(--spacing-xl, 24px) var(--demo-gutter); font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
footer a { color: var(--color-text-muted, #52525B); }
@media (max-width: 720px) { .proof-grid { grid-template-columns: 1fr; gap: var(--spacing-md, 8px); } }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
  /* Keep the grid usable on a phone: shorter frame, the page still resolves
     with the proof strip and cards stacked below. !important is required —
     the broker pins the iframe height with an inline style. */
  .editor-card iframe { height: 480px !important; }
}
`

// demoReadoutJS is the demo page's inline script: the theme toggle (persisted
// to the same cookie richtext uses) and the live bridge readout. The readout
// polls the adapter's mirrors on the IFRAME ELEMENT — the opaque frame is
// unreadable from the host page, so those mirrors (rows delivered, largest
// page, and the frame-published cache-block state) are the only live channel.
// Spliced in post-Sprintf because it contains literal "%".
const demoReadoutJS = `(function () {
  var html = document.documentElement;
  var btn = document.getElementById('fui-scheme-toggle');
  if (btn) {
    btn.addEventListener('click', function () {
      var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
      html.dataset.colorScheme = next;
      document.cookie = 'fui-color-scheme=' + next + '; path=/; max-age=31536000; samesite=lax';
    });
  }

  var ROWS = 100000;
  var elD = document.getElementById('dg-live-delivered');
  var elP = document.getElementById('dg-live-pct');
  var elM = document.getElementById('dg-live-maxpage');
  var elB = document.getElementById('dg-live-blocks');
  var elC = document.getElementById('dg-live-blockcap');
  function fmt(n) { return n.toLocaleString('en-US'); }
  function dash(el, v) { if (el) el.textContent = v === null ? '—' : fmt(v); }
  function tick() {
    var f = document.querySelector('iframe');
    if (!f) return;
    var d = typeof f.__datagridRowsDelivered === 'number' ? f.__datagridRowsDelivered : null;
    var m = typeof f.__datagridMaxRowsDelivered === 'number' ? f.__datagridMaxRowsDelivered : null;
    var c = f.__datagridCacheBlocks || null;
    dash(elD, d);
    dash(elM, m);
    dash(elB, c ? c.count : null);
    if (elC && c && c.cap) elC.textContent = fmt(c.cap);
    if (elP) {
      elP.textContent = d && d > 0
        ? (d / ROWS * 100).toFixed(1) + '% of the table'
        : '';
    }
  }
  tick();
  setInterval(tick, 250);
})();`

// renderDemo builds the self-contained demo page. The mount's initial doc is
// the last-saved view state when one exists (LoadDoc), else the host-declared
// demo doc ([WithDemoDoc]) — the plugin itself owns no column schema.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	docJSON, ok := p.LoadDoc(r.Context(), defaultDocID)
	if !ok && p.demoDoc != nil {
		if b, err := json.Marshal(*p.demoDoc); err == nil {
			docJSON = string(b)
		}
	}
	mount := Mount(MountConfig{DocID: defaultDocID, Doc: docJSON, MinHeight: demoGridHeight})
	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then this instance's config.js (publishes
	// __gofastrDatagridConfig — whether data:write / data:export were
	// wired), then the adapter (registers with the broker, merging the
	// config global into the capabilities it registers).
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		tokens,
		SaveURL,
		string(mount),
		Version,
		pluginhost.BrokerScriptURL,
		ConfigScriptURL,
		AdapterScriptURL,
	)
	page = strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1)
	page = strings.Replace(page, "{{READOUT_JS}}", demoReadoutJS, 1)
	return render.HTML(page)
}
