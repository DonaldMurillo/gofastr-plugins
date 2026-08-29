package calendar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoCalendarHeight is the iframe height the demo page mounts at: the week
// view's 24-hour grid IS the page's argument, so the frame is tall enough to
// read a working day at a glance without the page ending in dead space.
const demoCalendarHeight = "680px"

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" and shares the richtext/datagrid cookie, so all the
// demo pages open in the same scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme mirrors the framework showcase palette — identical to the
// richtext and datagrid demos so the three pages read as one product: warm
// near-black ladder, single amber accent, oklch throughout, token-driven.
// The status tones are restated lighter for dark (chips sit on the dark
// ladder, where the framework's light-tuned values go illegible).
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	t.Colors.Primary = style.Color{Name: "primary", Value: "oklch(0.82 0.155 78)"}
	t.Colors.PrimaryFg = style.Color{Name: "primary-fg", Value: "oklch(0.14 0.005 75)"}
	t.Colors.Accent = style.Color{Name: "accent", Value: "oklch(0.82 0.155 78)"}
	t.Fonts.Body = style.Font{Name: "body", Value: "-apple-system, BlinkMacSystemFont, Inter, 'Segoe UI', system-ui, sans-serif"}
	t.Fonts.Heading = style.Font{Name: "heading", Value: "-apple-system, BlinkMacSystemFont, Inter, 'Segoe UI', system-ui, sans-serif"}
	t.Fonts.Mono = style.Font{Name: "mono", Value: "ui-monospace, SFMono-Regular, 'JetBrains Mono', Menlo, Consolas, monospace"}
	t.Radii.SM = style.Radius{Name: "sm", Value: 4}
	t.Radii.MD = style.Radius{Name: "md", Value: 6}
	t.Radii.LG = style.Radius{Name: "lg", Value: 10}
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
		"selection":     "oklch(0.82 0.155 78 / 0.30)",
		"success":       "oklch(0.76 0.14 158)",
		"warning":       "oklch(0.82 0.12 80)",
		"danger":        "oklch(0.72 0.15 25)",
	}
	return t
}

// demoPage is the self-contained HTML document served at DemoURL: no UIHost,
// tokens inlined in <style>, the mount marker holding the frame, and the
// host scripts included directly — platform broker first, then the adapter
// (no config.js: this plugin has no handler-gated optional capabilities).
// The inline <script> (toggle + jump buttons + live readout) is acceptable
// ON THIS DEMO PAGE ONLY, the same allowance the richtext and datagrid
// demos make.
//
// The %s slots are: color scheme, theme tokens CSS, form action (SaveURL),
// mount marker HTML, plugin version, broker script URL, adapter script URL.
// The shell CSS and the readout script splice in AFTER formatting
// ({{SHELL_CSS}} / {{READOUT_JS}}) because both contain literal "%" that
// Sprintf must never see as format verbs.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Calendar — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / calendar</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/calendar" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>No calendar library.<br>Go owns the clocks.</h2>
    <p class="lead">The calendar below is written from scratch and runs in an opaque-origin iframe that cannot fetch anything. Recurrence, timezones and conflicts are answered by this page's Go process: the frame receives occurrences already resolved to explicit instants, and when you drag an event it sends an <em>intent</em> — the server decides what the drag actually means.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">0 npm dependencies</span>
      <span class="badge">sandbox="allow-scripts"</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">RRULE · TZ · conflicts in Go</span>
    </p>
  </section>

  <form method="post" action="%s" id="fui-demo-form">
    <section class="editor-card" aria-label="Calendar frame">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title">march-2026.dst — America/New_York</span>
        <span class="editor-mode">sandboxed iframe</span>
      </div>
      %s
    </section>
  </form>

  <section class="proof" aria-label="Server resolution, live">
    <p class="proof-title"><span class="proof-dot" aria-hidden="true"></span>Server resolution — live</p>
    <div class="proof-grid">
      <div class="proof-stat proof-stat-lead">
        <p class="proof-number"><span id="cal-req">—</span><span class="proof-of">requested</span></p>
        <p class="proof-label">the wall-clock delta the frame dragged</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="cal-wall">—</span><span class="proof-of">wall result</span></p>
        <p class="proof-label">how far the wall clock actually moved</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="cal-elapsed">—</span><span class="proof-of">elapsed</span></p>
        <p class="proof-label">real time between the two starts</p>
      </div>
    </div>
    <p class="proof-note" id="cal-note">Drag an event across the Sunday Mar 8 line — the 02:00 that never happens — and watch these three numbers disagree.</p>
    <p class="proof-note" id="cal-zone"></p>
  </section>

  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint"><kbd>←→↑↓</kbd> move focus / move event</span>
      <span class="hint"><kbd>Enter</kbd> open an event</span>
      <span class="hint"><kbd>M</kbd> <kbd>W</kbd> <kbd>D</kbd> switch views</span>
      <span class="hint"><kbd>T</kbd> today</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Tap an event to open it</span>
      <span class="hint">Drag an event to move it</span>
      <span class="hint">Pinch or use the buttons to change week</span>
    </p>
    <p class="jump-row">
      <button type="button" class="fui-btn" data-jump="2026-03-08">⤒ Spring forward · Sun Mar 8, 2026</button>
      <button type="button" class="fui-btn" data-jump="2026-11-01">⤓ Fall back · Sun Nov 1, 2026</button>
      <span class="save-status" id="cal-status" aria-live="polite"></span>
    </p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🔁 Recurrence is a server answer</h3>
      <p>The frame never receives an RRULE — it cannot mis-expand a rule it is never told. Go expands <code>DAILY</code>, <code>WEEKLY</code>, <code>MONTHLY</code> with <code>COUNT</code>/<code>UNTIL</code> and <code>BYDAY</code>, wall-clock anchored, and rejects everything else with <code>E_RRULE_UNSUPPORTED</code> instead of approximating it.</p>
    </article>
    <article class="card">
      <h3>🕓 Timezones have a policy</h3>
      <p>Series are wall-anchored: a daily 09:00 stays 09:00 across a DST change, and its instants shift 23h/25h between straddling days. A wall time inside a gap is carried by its pre-transition offset; an ambiguous hour resolves to its first occurrence. Drag onto one and the readout above shows the disagreement.</p>
    </article>
    <article class="card">
      <h3>⚡ Conflicts are computed, not guessed</h3>
      <p>Overlap detection runs in Go over resolved instants — all-day events included, same-series instances exempt — on every window and after every move. The frame styles what the server sends down; it never decides what a conflict is.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · calendar %s · <a href="/">all plugins →</a></p>
</footer>
<script>
{{READOUT_JS}}
</script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with the richtext
// and datagrid demo shells so the pages are visibly the same product, plus
// this demo's jump-button row. Token-driven throughout.
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
/* Proof strip: the three deltas are the point of the plugin, so they get
   display type and the amber-tinted panel — same treatment as datagrid's
   bridge telemetry. */
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
.proof-note { margin: var(--spacing-md, 8px) 2px 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
.proof-note strong { color: var(--color-text, #18181B); }
.under-editor { display: flex; align-items: center; justify-content: space-between; gap: var(--spacing-lg, 16px); flex-wrap: wrap; margin: var(--spacing-lg, 16px) 2px clamp(28px, 4vw, 40px); }
.under-editor p { margin: 0; }
.hints.hints-touch { display: none; }
@media (pointer: coarse) { .hints.hints-fine { display: none; } .hints.hints-touch { display: flex; } .jump-row { justify-content: center; } }
.hints { display: flex; flex-wrap: wrap; gap: var(--spacing-lg, 16px); color: var(--color-text-muted, #52525B); font-size: var(--text-xs, .75rem); }
.hint { display: inline-flex; align-items: center; gap: 5px; }
kbd { font-family: var(--font-mono, monospace); font-size: var(--text-xs, .75rem); border: 1px solid var(--color-border, #E4E4E7); border-bottom-width: 2px; border-radius: var(--radii-sm, 4px); padding: 1px 6px; background: var(--color-surface, #fff); color: var(--color-text, #18181B); }
.fui-btn { font: inherit; font-size: var(--text-sm, .875rem); font-weight: 500; padding: 8px 16px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); cursor: pointer; line-height: 1.2; transition: background 150ms ease, border-color 150ms ease; }
.fui-btn:hover { background: var(--color-surface-soft, #F4F4F5); border-color: var(--color-border-strong, #A1A1AA); }
.jump-row { display: flex; align-items: center; gap: var(--spacing-md, 8px); flex-wrap: wrap; }
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
  /* Keep the calendar usable on a phone: shorter frame, views still scroll
     internally. !important — the broker pins the iframe height inline. */
  .editor-card iframe { height: 560px !important; }
}
`

// demoReadoutJS is the demo page's inline script: the theme toggle, the DST
// jump buttons (through the adapter's published helper — the host page
// cannot reach into the opaque frame), and the live readout polling the
// adapter's mirrors on the IFRAME ELEMENT. Contains literal "%" (percent
// formatting), so it splices in post-Sprintf.
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

  // Jump-to-DST controls: the adapter exposes window.__gofastrCalendar.jump
  // (a host-to-frame event); nothing here touches the frame directly.
  function jump(date) {
    var api = window.__gofastrCalendar;
    if (api && typeof api.jump === 'function') {
      api.jump(date);
      var st = document.getElementById('cal-status');
      if (st) st.textContent = 'jumped to ' + date;
    }
  }
  var jumpBtns = document.querySelectorAll('[data-jump]');
  for (var i = 0; i < jumpBtns.length; i++) {
    (function (b) {
      b.addEventListener('click', function () { jump(b.getAttribute('data-jump')); });
    })(jumpBtns[i]);
  }

  function fmtDelta(min) {
    if (min === null || typeof min !== 'number') return '—';
    var sign = min < 0 ? '−' : '+';
    var m = Math.abs(min);
    if (m % 60 === 0) return sign + (m / 60) + 'h';
    return sign + Math.floor(m / 60) + 'h' + String(m % 60).padStart(2, '0');
  }

  function tick() {
    var f = document.querySelector('iframe');
    if (!f) return;
    var mv = f.__calendarLastMove;
    if (mv) {
      var req = document.getElementById('cal-req');
      var wall = document.getElementById('cal-wall');
      var el = document.getElementById('cal-elapsed');
      var note = document.getElementById('cal-note');
      var zone = document.getElementById('cal-zone');
      if (req) req.textContent = fmtDelta(mv.requestedWallMinutes);
      if (wall) wall.textContent = fmtDelta(mv.actualWallMinutes);
      if (el) el.textContent = fmtDelta(mv.elapsedMinutes);
      if (note && mv.note) note.innerHTML = '<strong>' + String(mv.note).replace(/&/g, '&amp;').replace(/</g, '&lt;') + '</strong>';
      if (zone) zone.textContent = mv.title + ' · ' + mv.zone + ' · ' + (mv.zoneAbbr || '') + ' (UTC' + (mv.offsetMinutes === 0 ? '' : (mv.offsetMinutes > 0 ? '+' : '−') + String(Math.abs(mv.offsetMinutes / 60)).replace(/\\.0$/, '') + ':00' + '') + ') after the move';
    }
  }
  tick();
  setInterval(tick, 250);
})();`

// renderDemo builds the self-contained demo page. The mount's initial doc
// is the last-saved view state when one exists (LoadDoc), else the
// host-declared demo doc ([WithDemoDoc]).
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	docJSON, ok := p.LoadDoc(r.Context(), defaultDocID)
	if !ok {
		doc := Doc{SchemaVersion: SchemaVersion, View: View{Date: "2026-03-08", Mode: "week"}}
		if p.demoDoc != nil {
			doc = *p.demoDoc
		}
		if b, err := json.Marshal(doc); err == nil {
			docJSON = string(b)
		}
	}
	mount := Mount(MountConfig{DocID: defaultDocID, Doc: docJSON, MinHeight: demoCalendarHeight})
	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then the adapter (registers with it).
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		tokens,
		SaveURL,
		string(mount),
		Version,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
	)
	page = strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1)
	page = strings.Replace(page, "{{READOUT_JS}}", demoReadoutJS, 1)
	return render.HTML(page)
}
