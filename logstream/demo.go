package logstream

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoStreamHeight is the iframe height the demo page mounts at: a terminal
// wants to BE the page's argument (~24 rows at 12px mono plus the toolbar),
// so the page resolves without dead space instead of stopping 400px after a
// squat little window.
const demoStreamHeight = "560px"

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" — the gofastr showcase pages are dark, amber-accented,
// and this demo matches them; the toggle can force light. Same cookie and
// default as richtext's and datagrid's demos, so the pages open in the same
// scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme mirrors the palette of the framework's own showcase site
// (gofastr/examples/site — the "v2" design), identical to richtext's and
// datagrid's demo themes so every demo page reads as one product: a warm
// near-black surface ladder with a single amber accent, expressed in oklch.
// The amber accent carries into both schemes; the full warm-dark ladder is
// the DarkColors override, which the page shows by default
// (schemeFromCookie). Token-driven, so the whole shell plus the bridged
// terminal pick these up with no per-element CSS.
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

// demoPage is the self-contained HTML document served at DemoURL. It runs
// with NO UIHost: the theme tokens are inlined in <style>, the mount marker
// holds the terminal iframe, and BOTH host scripts are included directly —
// the generic platform broker (pluginhost.js) first, then this plugin's
// adapter (host/adapter.js). The inline <script> (toggle + controls + live
// readout) is acceptable ON THIS DEMO PAGE ONLY — the broker/adapter and the
// frame stay CSP-clean and same-origin-script only, the same allowance the
// richtext and datagrid demos make.
//
// The %s slots are: color scheme, theme tokens CSS, mount marker HTML,
// plugin version, platform broker script URL, adapter script URL. The shell
// CSS, the control row and the readout script are spliced in AFTER
// formatting ({{SHELL_CSS}} / {{CONTROL_ROW}} / {{READOUT_JS}}) because they
// contain literal "%" that Sprintf must never see as format verbs.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Log Stream — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / logstream</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/logstream" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>The host pushes.<br>Overflow you can see.</h2>
    <p class="lead">The terminal below runs in an opaque-origin iframe that cannot fetch anything — <code>connect-src 'none'</code>. So the host pushes log lines it was never asked for, and the frame acknowledges each batch it renders. When the source outruns the render loop, the host drops from the oldest end of its buffer, counts, and the frame shows the gap — switch the source to <strong>Flood</strong> and watch the dropped counter climb.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">stream:read + theme:read</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">4-batch ack window</span>
      <span class="badge">10,000-line scrollback bound</span>
    </p>
  </section>

  <section class="editor-card" aria-label="Log stream frame">
    <div class="editor-chrome" aria-hidden="true">
      <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
      <span class="editor-title">api-gateway.log</span>
      <span class="editor-mode">sandboxed iframe</span>
    </div>
    %s
  </section>

  {{CONTROL_ROW}}

  <section class="proof" aria-label="Live bridge telemetry">
    <p class="proof-title"><span class="proof-dot" aria-hidden="true"></span>Bridge telemetry — live</p>
    <div class="proof-grid">
      <div class="proof-stat proof-stat-lead">
        <p class="proof-number"><span id="ls-live-lps">—</span><span class="proof-of">lines/s</span></p>
        <p class="proof-label">crossing the bridge right now</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="ls-live-delivered">—</span></p>
        <p class="proof-label">lines delivered</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="ls-live-dropped" class="proof-danger">—</span></p>
        <p class="proof-label">dropped, visibly</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="ls-live-scrollback">—</span><span class="proof-of">/ 10,000</span></p>
        <p class="proof-label">scrollback held by the frame</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="ls-live-inflight">—</span><span class="proof-of">/ 4</span></p>
        <p class="proof-label">unacked batches in flight</p>
      </div>
    </div>
  </section>
  <p class="proof-note">Straight from the adapter's bridge mirrors. The frame renders one batch per ~16&nbsp;ms tick (~60/s); Calm produces 5 lines/s, Flood 6,000 — the difference is dropped at the host and marked in the terminal.</p>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>⇄ Push, not pull</h3>
      <p>Every other plugin here answers requests. This frame never asks: the host streams NDJSON from <code>GET /stream</code> and pushes unsolicited <code>streamBatch</code> events. The only capability is <code>stream:read</code> — there is no write surface at all.</p>
    </article>
    <article class="card">
      <h3>✂ Overflow is labelled</h3>
      <p>The host keeps ≤4 unacked batches in flight and a 2,000-line buffer; past that it drops the <em>oldest</em> lines and the count rides with the next batch. The terminal renders <code>⋯ N lines dropped ⋯</code> — never a silent gap.</p>
    </article>
    <article class="card">
      <h3>💽 The frame forgets</h3>
      <p>Scrollback is capped at 10,000 lines — a frame that never forgets would eventually hold everything the host ever sent. The ack carries the live depth against the cap, so the counter above is the frame's own accounting, not a promise.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · logstream %s · <a href="/">all plugins →</a></p>
</footer>
<script>
{{READOUT_JS}}
</script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoControlRow is the affordance strip: keyboard hints, the stream
// controls (Pause/Resume always; Calm/Flood only when the host wired a
// control route — the producer belongs to the host app), and a live status
// region. Spliced in post-Sprintf because the readout script beside it
// contains literal "%".
const demoControlRow = `  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint">type in the frame's search box — <kbd>Enter</kbd> jumps</span>
      <span class="hint"><kbd>Esc</kbd> clears the search</span>
      <span class="hint">scroll the terminal — it is a real scrollback</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Search scrolls back to matches</span>
      <span class="hint">Scroll the terminal</span>
    </p>
    <p class="save-row">
      <button type="button" class="fui-btn" id="ls-btn-pause">Pause</button>
      <button type="button" class="fui-btn fui-btn-primary" id="ls-btn-calm" aria-pressed="true">Calm</button>
      <button type="button" class="fui-btn" id="ls-btn-fast" aria-pressed="false">Flood</button>
      <span class="save-status" id="ls-control-status" role="status" aria-live="polite">streaming · calm</span>
    </p>
  </div>`

// demoReadoutJS is the demo page's inline script: the theme toggle (persisted
// to the same cookie richtext/datagrid use), the stream controls, and the
// live bridge readout. The readout polls the adapter's mirrors on the IFRAME
// ELEMENT — the opaque frame is unreadable from the host page, so those
// mirrors (delivered, dropped, in-flight, and the frame-published ack stats)
// are the only live channel. Spliced in post-Sprintf because it contains
// literal "%".
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

  var elL = document.getElementById('ls-live-lps');
  var elD = document.getElementById('ls-live-delivered');
  var elX = document.getElementById('ls-live-dropped');
  var elS = document.getElementById('ls-live-scrollback');
  var elF = document.getElementById('ls-live-inflight');
  var status = document.getElementById('ls-control-status');
  var paused = false;
  var rate = 'calm';
  var CONTROL_URL = {{CONTROL_URL}};

  function fmt(n) { return n.toLocaleString('en-US'); }
  function dash(el, v) { if (el) el.textContent = v === null ? '—' : fmt(v); }

  // 1s rolling delivery rate: sample delivered every tick, keep the sample
  // from ~1s ago, divide the delta by the elapsed seconds.
  var lastSample = null;
  function lps(delivered) {
    var now = Date.now();
    if (delivered === null) return null;
    if (lastSample && now - lastSample.t >= 900) {
      var v = Math.round((delivered - lastSample.d) / ((now - lastSample.t) / 1000));
      lastSample = { t: now, d: delivered };
      return Math.max(v, 0);
    }
    if (!lastSample) lastSample = { t: now, d: delivered };
    return null;
  }

  function tick() {
    var f = document.querySelector('.editor-card iframe');
    if (!f) return;
    var d = typeof f.__logstreamDelivered === 'number' ? f.__logstreamDelivered : null;
    var x = typeof f.__logstreamDropped === 'number' ? f.__logstreamDropped : null;
    var inflight = typeof f.__logstreamInFlight === 'number' ? f.__logstreamInFlight : null;
    var stats = f.__logstreamStats || null;
    var r = lps(d);
    if (elL && r !== null) elL.textContent = fmt(r);
    dash(elD, d);
    dash(elX, x);
    if (elX) elX.classList.toggle('is-dropping', !!x && x > 0);
    dash(elS, stats ? stats.scrollback : null);
    dash(elF, inflight);
  }
  tick();
  setInterval(tick, 250);

  function setStatus() {
    if (!status) return;
    status.textContent = paused ? ('paused · ' + rate) : ('streaming · ' + rate);
    var calm = document.getElementById('ls-btn-calm');
    var fast = document.getElementById('ls-btn-fast');
    // The active rate carries the primary look; the buttons also toggle the
    // ARIA pressed state so assistive tech sees the same thing the eye does.
    if (calm) {
      calm.classList.toggle('fui-btn-primary', rate === 'calm');
      calm.setAttribute('aria-pressed', rate === 'calm' ? 'true' : 'false');
    }
    if (fast) {
      fast.classList.toggle('fui-btn-primary', rate === 'fast');
      fast.setAttribute('aria-pressed', rate === 'fast' ? 'true' : 'false');
    }
  }

  var pauseBtn = document.getElementById('ls-btn-pause');
  if (pauseBtn) {
    pauseBtn.addEventListener('click', function () {
      paused = !paused;
      pauseBtn.textContent = paused ? 'Resume' : 'Pause';
      document.dispatchEvent(new CustomEvent(paused ? 'logstream:pause' : 'logstream:resume'));
      setStatus();
    });
  }

  function rateBtn(id, value) {
    var b = document.getElementById(id);
    if (!b || !CONTROL_URL) return;
    b.addEventListener('click', function () {
      fetch(CONTROL_URL, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ rate: value })
      }).then(function (r) { if (r.ok) { rate = value; setStatus(); } })
        .catch(function () { if (status) status.textContent = 'rate switch failed'; });
    });
  }
  rateBtn('ls-btn-calm', 'calm');
  rateBtn('ls-btn-fast', 'fast');
})();`

// demoShellCSS is the page chrome, kept class-for-class with richtext's and
// datagrid's demo shells so the pages are visibly the same product, plus the
// logstream's own proof strip (five counters) and control row. Token-driven
// throughout — the theme toggle restyles everything, and the bridged tokens
// re-theme the terminal inside the frame.
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
/* Proof strip: the backpressure claim, live. The numbers are the point of
   the plugin, so they get display type (tabular, clamped) rather than
   footnote grey — the amber tint keys the panel to the accent without
   shouting. The dropped counter flips to danger red the moment it is
   non-zero: watching that happen is the whole argument. */
.proof { margin: var(--spacing-lg, 16px) 0 0; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); background: color-mix(in srgb, var(--color-primary, #e0a040) 4%, var(--color-surface, #fff)); padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); }
.proof-title { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-xs, .75rem); font-weight: 600; letter-spacing: .08em; text-transform: uppercase; color: var(--color-text-subtle, #71717A); display: flex; align-items: center; gap: var(--spacing-md, 8px); }
.proof-dot { width: 8px; height: 8px; flex: none; border-radius: 50%; background: var(--color-success, #166534); animation: proof-pulse 2.4s ease-in-out infinite; }
@keyframes proof-pulse { 0%, 100% { opacity: 1; } 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .proof-dot { animation: none; } }
.proof-grid { display: grid; grid-template-columns: 1.7fr 1fr 1fr 1.2fr 1.1fr; gap: var(--spacing-md, 8px) var(--spacing-lg, 16px); }
.proof-number { margin: 0; font-size: clamp(1.25rem, 2vw, 1.6rem); font-weight: 700; letter-spacing: -0.02em; line-height: 1.1; font-variant-numeric: tabular-nums; color: var(--color-text, #18181B); }
.proof-stat-lead .proof-number { font-size: clamp(1.75rem, 3.5vw, 2.5rem); }
.proof-of { font-size: .45em; font-weight: 600; color: var(--color-text-muted, #52525B); letter-spacing: 0; margin-left: .4em; white-space: nowrap; }
.proof-label { margin: 4px 0 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); line-height: 1.5; }
.proof-danger.is-dropping { color: var(--color-danger, #d1242f); }
.proof-note { margin: var(--spacing-md, 8px) 2px 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
.under-editor { display: flex; align-items: center; justify-content: space-between; gap: var(--spacing-lg, 16px); flex-wrap: wrap; margin: var(--spacing-lg, 16px) 2px 0; }
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
.save-row { display: flex; align-items: center; gap: var(--spacing-md, 8px); flex-wrap: wrap; }
.save-status { font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); min-width: 12ch; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: var(--spacing-lg, 16px); margin: var(--spacing-xl, 24px) 0 clamp(32px, 5vw, 48px); }
.card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); padding: var(--spacing-xl, 24px); background: var(--color-surface, #fff); box-shadow: var(--shadow-sm, 0 1px 2px 0 rgba(0,0,0,.05)); }
.card h3 { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-base, 1rem); font-weight: 650; }
.card p { margin: 0; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); line-height: 1.6; }
.card code, .lead code { font-family: var(--font-mono, monospace); font-size: .92em; background: var(--color-surface-soft, #F4F4F5); padding: 1px .35em; border-radius: var(--radii-sm, 4px); }
footer { border-top: 1px solid var(--color-border, #E4E4E7); margin-top: var(--spacing-md, 8px); }
footer p { max-width: var(--demo-measure); margin: 0 auto; padding: var(--spacing-xl, 24px) var(--demo-gutter); font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
footer a { color: var(--color-text-muted, #52525B); }
@media (max-width: 900px) { .proof-grid { grid-template-columns: 1fr 1fr; } .proof-stat-lead { grid-column: 1 / -1; } }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
  /* Keep the terminal usable on a phone: shorter frame, the page still
     resolves with the proof strip and cards stacked below. !important is
     required — the broker pins the iframe height with an inline style. */
  .editor-card iframe { height: 420px !important; }
  .proof-grid { grid-template-columns: 1fr 1fr; }
}

`

func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	mount := Mount(MountConfig{DocID: defaultDocID, MinHeight: demoStreamHeight})

	// The control row always carries Pause/Resume (host-side adapter state).
	// Calm/Flood appear only when the host wired a control route; without
	// one the buttons would 404 and the demo would look broken.
	controlURL := "null"
	controlRow := demoControlRow
	if p.demoControlURL == "" {
		controlRow = strings.Replace(controlRow,
			`      <button type="button" class="fui-btn fui-btn-primary" id="ls-btn-calm" aria-pressed="true">Calm</button>`+"\n", "", 1)
		controlRow = strings.Replace(controlRow,
			`      <button type="button" class="fui-btn" id="ls-btn-fast" aria-pressed="false">Flood</button>`+"\n", "", 1)
		controlRow = strings.Replace(controlRow, "streaming · calm", "streaming · host-owned rate", 1)
	} else {
		controlURL = `"` + p.demoControlURL + `"`
	}

	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then the adapter (registers with the broker and
	// opens the NDJSON stream once the frame says ready).
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		tokens,
		string(mount),
		Version,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
	)
	page = strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1)
	page = strings.Replace(page, "{{CONTROL_ROW}}", controlRow, 1)
	page = strings.Replace(page, "{{READOUT_JS}}",
		strings.Replace(demoReadoutJS, "{{CONTROL_URL}}", controlURL, 1), 1)
	return render.HTML(page)
}
