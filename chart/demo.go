package chart

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/chart/ssr"
	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// schemeFromCookie reads the persisted color scheme (set by the demo
// toggle, same cookie as the richtext demo). Defaults to "light" — pinned
// by TestDemoPageContainsSSRAndMount, which asserts the served page carries
// data-color-scheme="light" on a fresh request.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "dark" {
		return "dark"
	}
	return "light"
}

// demoTheme mirrors the palette of the framework's showcase site (the same
// ladder richtext's demo uses), expressed in HEX rather than oklch for one
// plugin-specific reason: the frame's series palette (chart/js/src/
// palette.ts) parses the bridged token values to mix further series slots,
// and it parses #rrggbb. Token-driven either way — the whole shell, the
// SSR chart classes, and the bridged frame all read these values.
//
// The amber primary is the design system's accent and the FIRST series
// color; --color-info (a blue far from amber in hue) is the second, so two
// series are told apart at 1px.
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	t.Colors.Primary = style.Color{Name: "primary", Value: "#D97706"}
	t.Colors.PrimaryFg = style.Color{Name: "primary-fg", Value: "#FFFFFF"}
	t.Colors.Accent = style.Color{Name: "accent", Value: "#D97706"}
	t.Colors.Info = style.Color{Name: "info", Value: "#2563EB"}
	t.DarkColors = map[string]string{
		"background":    "#131311",
		"surface":       "#1B1B18",
		"surface-soft":  "#242422",
		"text":          "#F2F1EC",
		"text-muted":    "#C6C4BC",
		"text-subtle":   "#A6A49B",
		"border":        "#35352F",
		"border-strong": "#4A4A43",
		"primary":       "#F5B041",
		"primary-fg":    "#131311",
		"accent":        "#F5B041",
		"info":          "#8AB4F8",
		"success":       "#7CC79C",
		"danger":        "#F08A8A",
		"warning":       "#E8B45A",
		"selection":     "rgba(245,176,65,0.30)",
	}
	return t
}

// demoSpec is the baseline chart the demo page mounts when nothing has been
// saved yet: two series, a title, axis captions, a legend — everything the
// no-JS test needs to see in the SSR output. The tick counts are chosen so
// the d3 algorithm lands x ticks on WHOLE weeks (0…5, one per week — the
// data is weeks 0 to 5, a 0.5-week tick would be wrong on its face) and
// keeps the y axis at six labels for a 420px plot instead of sixteen.
var demoSpec = ssr.Spec{
	SchemaVersion: ssr.SchemaVersion,
	Type:          ssr.TypeLine,
	Title:         "Weekly signups",
	Axes: ssr.Axes{
		X: ssr.Axis{Label: "week", TickCount: new(6)},
		Y: ssr.Axis{Label: "signups", TickCount: new(5)},
	},
	Series: []ssr.Series{
		{Name: "Product", Points: mustPoints(0, 120, 1, 180, 2, 165, 3, 240, 4, 280, 5, 340)},
		{Name: "Referral", Points: mustPoints(0, 40, 1, 55, 2, 90, 3, 85, 4, 140, 5, 190)},
	},
}

func ptr(n int) *int { return &n }

func mustPoints(xy ...float64) []ssr.Point {
	out := make([]ssr.Point, 0, len(xy)/2)
	for i := 0; i+1 < len(xy); i += 2 {
		out = append(out, ssr.Point{X: &xy[i], Y: &xy[i+1]})
	}
	return out
}

// demoPage is the self-contained HTML document served at DemoURL, dressed
// in the same shell as the richtext demo (docs/demo-page-design.md is the
// standard; richtext/demo.go is the reference). It runs with NO UIHost:
// theme tokens are inlined in <style>, the mount marker holds the SSR
// chart + the sandboxed frame, and both host scripts are included
// directly. The inline <script> is acceptable ON THIS DEMO PAGE ONLY.
//
// The %s slots are: theme tokens CSS, color scheme, form action (SaveURL),
// the type-switcher buttons (aria-pressed from the saved spec's type), the
// mount marker HTML (SSR svg + marker), the platform broker script URL,
// and this plugin's adapter script URL.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Chart — One Spec, Two Renderers</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / chart</span></h1>
  </div>
  <nav aria-label="Demo">
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>One chart spec.<br>Two renderers, zero drift.</h2>
    <p class="lead">Everything below is rendered twice from the same <code>chart-v1</code> JSON: a pure-Go SVG on the server, and an Observable Plot chart inside a sandboxed iframe on the client. Same ticks, same extents, same series — enforced by test, not aspiration. Turn JavaScript off and the server's chart is still the page.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">one spec · two renderers</span>
      <span class="badge">3,026 d3 tick cases, bit-for-bit</span>
      <span class="badge">works with JavaScript off</span>
      <span class="badge">≤ 12 series · 10,000 points</span>
    </p>
  </section>

  <form method="post" action="%s" id="fui-demo-form">
    <section class="editor-card" aria-label="Chart">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title">demo.chart-v1</span>
        <span class="editor-mode">sandboxed plot frame</span>
      </div>
      <div class="editor-body">
        %s
        <div class="ssr-caption" id="ssr-caption" hidden>
          <strong>Server-rendered SVG</strong> — pure Go, zero scripts. With JavaScript off this is the entire page; the hydrated frame above it shows the same tick labels and extents.
        </div>
      </div>
    </section>
    <div class="under-editor">
      <div class="chart-controls">
        <div class="seg" role="group" aria-label="Chart type" id="type-switcher">%s</div>
        <button type="button" class="fui-btn" id="reveal-ssr" aria-expanded="false" aria-controls="ssr-caption">Reveal server SVG</button>
        <span class="control-status" id="type-status" role="status" aria-live="polite"></span>
      </div>
      <p class="save-row">
        <button type="submit" class="fui-btn fui-btn-primary">Save</button>
        <span class="save-status" id="save-status" role="status" aria-live="polite"></span>
      </p>
    </div>
    <details class="spec-editor">
      <summary>Edit spec JSON</summary>
      <p class="spec-editor-row">
        <textarea id="chart-spec" spellcheck="false" aria-label="Chart spec JSON"></textarea>
      </p>
      <p class="spec-editor-row">
        <button type="button" class="fui-btn" id="apply-spec">Apply</button>
        <span class="save-status" id="apply-status" role="status" aria-live="polite"></span>
      </p>
    </details>
    <noscript>
      <style>.chart-controls { display: none; }</style>
      <p class="js-off-note">JavaScript is off: this chart is the pure-Go server render — exactly what the frame above would hydrate from.</p>
    </noscript>
  </form>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🎯 Ticks agree, bit-for-bit</h3>
      <p><code>chart/ssr/ticks.go</code> is a line-for-line port of d3-array 3.2.4's tick algorithm. A 3,026-domain ground-truth sweep recorded from real d3 replays on every <code>go test</code>; the e2e suite then asserts the server SVG and the hydrated frame print identical tick labels across four awkward ranges.</p>
    </article>
    <article class="card">
      <h3>🛑 Failure degrades to content</h3>
      <p>If the frame fails to boot, the adapter un-hides the server chart — a frame failure costs the tooltips, not the page. Reload with JavaScript off (or press <strong>Reveal server SVG</strong>): same chart, same design tokens, zero scripts.</p>
    </article>
    <article class="card">
      <h3>🔒 Sandboxed, capability-scoped</h3>
      <p>The Plot bundle runs under <code>sandbox="allow-scripts"</code> in an opaque-origin iframe — no cookies, no storage, no host DOM. It holds three grants, <code>document:read</code>, <code>document:write</code>, <code>theme:read</code>, and the spec caps at 12 series and 10,000 points.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · chart %s · <a href="/">see the richtext demo →</a></p>
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
  var SAVE = %q;
  var form = document.getElementById('fui-demo-form');
  var status = document.getElementById('save-status');
  function specValue() {
    var input = form && form.querySelector('input[name=chart_spec]');
    return input ? input.value : '';
  }
  function postSpec(spec, onOK, onBadJSON, onFail) {
    var doc = null;
    try { doc = JSON.parse(spec); } catch (err) { onBadJSON(); return; }
    fetch(SAVE, {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ docId: 'demo', doc: doc, schemaVersion: 'chart-v1' })
    }).then(function (r) { r.ok ? onOK(r) : onFail(); })
      .catch(onFail);
  }
  if (form) form.addEventListener('submit', function (e) {
    e.preventDefault();
    var spec = specValue();
    if (!spec) { if (status) status.textContent = 'Nothing to save yet.'; return; }
    if (status) status.textContent = 'Saving…';
    postSpec(spec,
      function () { if (status) status.textContent = 'Saved ✓'; },
      function () { if (status) status.textContent = 'Not valid JSON.'; },
      function () { if (status) status.textContent = 'Save failed.'; });
  });

  // Type switcher: re-save the current spec with a new type, then reload —
  // the server re-renders the SSR chart and the frame hydrates the new
  // spec, which is exactly the write path the e2e journey drives.
  var seg = document.getElementById('type-switcher');
  var tstatus = document.getElementById('type-status');
  if (seg) seg.addEventListener('click', function (e) {
    var b = e.target.closest('button[data-type]');
    if (!b || b.getAttribute('aria-pressed') === 'true') return;
    var spec = specValue();
    var doc = null;
    try { doc = JSON.parse(spec); } catch (err) { doc = null; }
    if (!doc) { if (tstatus) tstatus.textContent = 'No spec to switch.'; return; }
    doc.type = b.getAttribute('data-type');
    Array.prototype.forEach.call(seg.querySelectorAll('button'), function (x) { x.disabled = true; });
    if (tstatus) tstatus.textContent = 'Rendering ' + doc.type + '…';
    postSpec(JSON.stringify(doc),
      function () { window.location.reload(); },
      function () { if (tstatus) tstatus.textContent = 'Not valid JSON.'; },
      function () {
        Array.prototype.forEach.call(seg.querySelectorAll('button'), function (x) { x.disabled = false; });
        if (tstatus) tstatus.textContent = 'Switch failed.';
      });
  });

  // Reveal the server SVG next to the hydrated frame — the proof, shown
  // rather than claimed. The adapter hid the wrapper when the frame went
  // ready; this flips it back on demand.
  var reveal = document.getElementById('reveal-ssr');
  var caption = document.getElementById('ssr-caption');
  var wrap = document.querySelector('.gofastr-chart-ssr');
  if (reveal && wrap) reveal.addEventListener('click', function () {
    var show = wrap.hidden;
    wrap.hidden = !show;
    if (caption) caption.hidden = !show;
    reveal.setAttribute('aria-expanded', show ? 'true' : 'false');
    reveal.textContent = show ? 'Hide server SVG' : 'Reveal server SVG';
  });

  var ta = document.getElementById('chart-spec');
  var apply = document.getElementById('apply-spec');
  var applyStatus = document.getElementById('apply-status');
  if (ta) ta.value = specValue();
  if (apply) apply.addEventListener('click', function () {
    var spec = ta.value;
    if (!spec) { if (applyStatus) applyStatus.textContent = 'Empty spec.'; return; }
    if (applyStatus) applyStatus.textContent = 'Applying…';
    postSpec(spec,
      function () { window.location.reload(); },
      function () { if (applyStatus) applyStatus.textContent = 'Not valid JSON.'; },
      function () { if (applyStatus) applyStatus.textContent = 'Invalid spec (rejected by the server).'; });
  });
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, matched to richtext's demo shell (the
// house style) plus the chart-specific pieces: the type switcher, the
// server-SVG compare caption, and the editor-body ordering that keeps the
// SSR svg below the hydrated frame when revealed. Token-driven throughout.
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
main { width: 100%; max-width: var(--demo-measure); margin: 0 auto; padding: clamp(32px, 5vw, 56px) var(--demo-gutter) var(--spacing-md, 8px); }
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
/* editor-body: hydrated frame first, compare caption + SSR svg under it
   when revealed (flex order — DOM order is fixed by the adapter's
   previousElementSibling handoff). */
.editor-body { display: flex; flex-direction: column; }
.editor-body > [data-fui-plugin] { order: 1; }
.editor-body > .gofastr-chart-ssr { order: 3; padding: var(--spacing-md, 8px) var(--spacing-lg, 16px) var(--spacing-lg, 16px); border-top: 1px dashed var(--color-border-strong, #A1A1AA); background: var(--color-surface, #fff); }
.ssr-caption { order: 2; margin: 0 var(--spacing-lg, 16px); padding: var(--spacing-sm, 4px) 0 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); border-top: 1px dashed var(--color-border-strong, #A1A1AA); padding-top: var(--spacing-md, 8px); }
.ssr-caption strong { color: var(--color-text-muted, #52525B); }
.gofastr-chart-empty, .gofastr-chart-error { margin: var(--spacing-lg, 16px); padding: var(--spacing-lg, 16px); font-size: var(--text-sm, .875rem); border-radius: var(--radii-md, 8px); }
.gofastr-chart-empty { color: var(--color-text-muted, #52525B); border: 1px dashed var(--color-border, #E4E4E7); }
.gofastr-chart-error { color: var(--color-danger, #d1242f); border: 1px solid var(--color-danger, #d1242f); }
.under-editor { display: flex; align-items: center; justify-content: space-between; gap: var(--spacing-lg, 16px); flex-wrap: wrap; margin: var(--spacing-lg, 16px) 2px clamp(28px, 4vw, 40px); }
.under-editor p { margin: 0; }
.chart-controls { display: flex; align-items: center; gap: var(--spacing-md, 8px); flex-wrap: wrap; }
.seg { display: inline-flex; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); overflow: hidden; background: var(--color-surface, #fff); box-shadow: var(--shadow-sm, 0 1px 2px 0 rgba(0,0,0,.05)); }
.seg button { font: inherit; font-size: var(--text-sm, .875rem); padding: 8px 14px; border: 0; border-right: 1px solid var(--color-border, #E4E4E7); background: transparent; color: var(--color-text-muted, #52525B); cursor: pointer; line-height: 1.2; transition: background 150ms ease, color 150ms ease; }
.seg button:last-child { border-right: 0; }
.seg button:hover { background: var(--color-surface-soft, #F4F4F5); color: var(--color-text, #18181B); }
.seg button[aria-pressed="true"] { background: color-mix(in srgb, var(--color-primary, #e0a040) 16%, var(--color-surface, #fff)); color: var(--color-text, #18181B); font-weight: 600; }
.seg button:disabled { opacity: .55; cursor: wait; }
.control-status { font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); }
.fui-btn { font: inherit; font-size: var(--text-sm, .875rem); font-weight: 500; padding: 8px 16px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); cursor: pointer; line-height: 1.2; transition: background 150ms ease, border-color 150ms ease; }
.fui-btn:hover { background: var(--color-surface-soft, #F4F4F5); border-color: var(--color-border-strong, #A1A1AA); }
.fui-btn-primary { background: var(--color-primary, #e0a040); color: var(--color-primary-fg, #fff); border-color: transparent; font-weight: 600; }
.fui-btn-primary:hover { filter: brightness(1.08); background: var(--color-primary, #e0a040); border-color: transparent; }
.save-row { display: flex; align-items: center; gap: var(--spacing-lg, 16px); }
.save-status { font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); }
.save-status.is-err { color: var(--color-danger, #d1242f); }
.js-off-note { display: flex; gap: var(--spacing-md, 8px); align-items: baseline; border: 1px solid color-mix(in srgb, var(--color-primary, #e0a040) 35%, var(--color-border)); background: color-mix(in srgb, var(--color-primary, #e0a040) 8%, var(--color-surface)); border-radius: var(--radii-lg, 12px); padding: var(--spacing-lg, 16px); margin: 0 0 clamp(28px, 4vw, 40px); font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); }
.spec-editor { margin: calc(-1 * var(--spacing-lg, 16px)) 0 clamp(28px, 4vw, 40px); }
/* The disclosure is a control, so it is shaped like one. Left as bare text it
   read as a stray bullet orphaned in the gap between the plot card and the
   feature cards. */
.spec-editor summary { cursor: pointer; color: var(--color-text-muted, #52525B); font-size: var(--text-sm, .875rem); padding: 6px 12px; user-select: none; list-style: none; display: inline-flex; align-items: center; gap: 6px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); }
.spec-editor summary::-webkit-details-marker { display: none; }
.spec-editor summary::before { content: "▸"; font-size: .8em; color: var(--color-text-subtle, #71717A); transition: transform 150ms ease; display: inline-block; }
.spec-editor[open] summary::before { transform: rotate(90deg); }
.spec-editor summary:hover { color: var(--color-text, #18181B); border-color: color-mix(in srgb, var(--color-primary, #e0a040) 45%, var(--color-border)); }
.spec-editor summary:focus-visible { outline: 2px solid var(--color-primary, #e0a040); outline-offset: 2px; }
.spec-editor[open] summary { color: var(--color-text, #18181B); font-weight: 600; }
.spec-editor-row { margin: var(--spacing-md, 8px) 0 0; display: flex; gap: var(--spacing-md, 8px); align-items: flex-start; flex-wrap: wrap; }
textarea#chart-spec { flex: 1 1 32rem; min-height: 9em; font-family: var(--font-mono, monospace); font-size: 12px; line-height: 1.5; padding: var(--spacing-md, 8px) var(--spacing-lg, 16px); border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); resize: vertical; }
textarea#chart-spec:focus-visible { outline: 2px solid var(--color-primary, #e0a040); outline-offset: 1px; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: var(--spacing-lg, 16px); margin: var(--spacing-sm, 4px) 0 clamp(32px, 5vw, 48px); }
.card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); padding: var(--spacing-xl, 24px); background: var(--color-surface, #fff); box-shadow: var(--shadow-sm, 0 1px 2px 0 rgba(0,0,0,.05)); }
.card h3 { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-base, 1rem); font-weight: 650; }
.card p { margin: 0; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); line-height: 1.6; }
code { font-family: var(--font-mono, monospace); font-size: .92em; background: var(--color-surface-soft, #F4F4F5); padding: 1px .35em; border-radius: var(--radii-sm, 4px); }
footer { border-top: 1px solid var(--color-border, #E4E4E7); margin-top: var(--spacing-md, 8px); }
footer p { max-width: var(--demo-measure); margin: 0 auto; padding: var(--spacing-xl, 24px) var(--demo-gutter); font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
footer a { color: var(--color-text-muted, #52525B); }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .under-editor { justify-content: center; }
  .chart-controls { justify-content: center; }
}
`

// chartTypes is the switcher's button set, in spec-order.
var chartTypes = []struct {
	Type  string
	Label string
}{
	{string(ssr.TypeLine), "Line"},
	{string(ssr.TypeBar), "Bar"},
	{string(ssr.TypeArea), "Area"},
	{string(ssr.TypeScatter), "Scatter"},
}

func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	spec, ok := p.LoadDoc(r.Context(), defaultDocID)
	if !ok {
		spec = mustJSON(demoSpec)
	}
	mount := Mount(MountConfig{DocID: defaultDocID, Spec: spec})

	// The switcher's pressed state comes from the SAVED spec's type, so a
	// reload after a switch lands on the right button.
	cur := ssr.TypeLine
	if parsed, err := ssr.ParseSpec(spec); err == nil {
		cur = parsed.Type
	}
	buttons := ""
	for _, ct := range chartTypes {
		pressed := "false"
		if ct.Type == string(cur) {
			pressed = "true"
		}
		buttons += fmt.Sprintf(
			`<button type="button" data-type="%s" aria-pressed="%s">%s</button>`,
			ct.Type, pressed, ct.Label)
	}

	page := fmt.Sprintf(demoPage, schemeFromCookie(r), tokens, SaveURL, string(mount),
		buttons, Version, SaveURL, pluginhost.BrokerScriptURL, AdapterScriptURL)
	page = replaceShellCSS(page)
	return render.HTML(page)
}

// replaceShellCSS swaps the {{SHELL_CSS}} placeholder for the shell
// stylesheet. The placeholder indirection keeps the %s count of demoPage
// honest (the shell CSS contains % signs that Sprintf would need escaped).
func replaceShellCSS(page string) string {
	return strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1)
}

func mustJSON(s ssr.Spec) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		panic("chart: marshal demo spec: " + err.Error())
	}
	return b
}
