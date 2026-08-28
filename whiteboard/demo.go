package whiteboard

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoBoardHeight is the iframe height the demo page mounts at: the board IS
// the page's argument, so it gets room to draw while the proof strip and
// cards still land above the fold on a laptop.
const demoBoardHeight = "560px"

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" — the gofastr showcase pages are dark, amber-accented,
// and this demo matches them; the toggle can force light. Same cookie and
// default as richtext/datagrid's demos, so the pages open in the same scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme mirrors the palette of the framework's own showcase site
// (gofastr/examples/site — the "v2" design), identical to richtext's and
// datagrid's demo themes so the demo pages read as one product: a warm
// near-black surface ladder with a single amber accent, expressed in oklch.
// The amber accent carries into both schemes; the full warm-dark ladder is
// the DarkColors override, which the page shows by default.
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
	}
	return t
}

// demoPage is the self-contained HTML document served at DemoURL. It runs
// with NO UIHost: the theme tokens are inlined in <style>, the mount marker
// holds the board iframe, and the host scripts are included directly — the
// generic platform broker (pluginhost.js) first, then this instance's
// config.js, then the adapter. The inline <script> (toggle + live bridge
// readout + the drop/reconnect control) is acceptable ON THIS DEMO PAGE ONLY
// — the broker/adapter and the frame stay CSP-clean and same-origin-script
// only, the same allowance the richtext/datagrid demos make.
//
// The %s slots are: color scheme, theme tokens CSS, mount marker HTML,
// plugin version, platform broker script URL, config script URL, adapter
// script URL. The shell CSS and the readout script are spliced in AFTER
// formatting ({{SHELL_CSS}} / {{READOUT_JS}}) because both contain literal
// "%" that Sprintf must never see as format verbs.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Whiteboard — Sandboxed Collaborative Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / whiteboard</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/whiteboard" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>Two people, one board.<br>No socket in the cage.</h2>
    <p class="lead">The board below runs in an opaque-origin iframe whose CSP forbids every connection — <code>connect-src 'none'</code>. It collaborates anyway: each stroke is a Yjs CRDT update, an opaque binary blob that crosses the postMessage bridge, and <strong>this page's</strong> Go process relays it to every other browser. Open the board in a second window and watch them converge.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">connect-src 'none'</span>
      <span class="badge">sandbox="allow-scripts"</span>
      <span class="badge">Yjs CRDT updates</span>
      <span class="badge">capability sync:room</span>
    </p>
  </section>

  <section class="editor-card" aria-label="Whiteboard frame">
    <div class="editor-chrome" aria-hidden="true">
      <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
      <span class="editor-title">board·demo</span>
      <span class="editor-mode">sandboxed iframe</span>
    </div>
    %s
  </section>

  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint">drag on the board to draw</span>
      <span class="hint">🧽 erase — deletes converge too</span>
      <span class="hint">cursors from other windows appear live</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Drag to draw</span>
      <span class="hint">Erase deletes converge too</span>
      <span class="hint">Other windows' cursors appear live</span>
    </p>
    <p class="save-row">
      <button type="button" class="fui-btn" id="wb-open-peer">Open second window</button>
      <button type="button" class="fui-btn fui-btn-primary" id="wb-drop">Drop connection</button>
      <span class="save-status" id="wb-room-status" role="status" aria-live="polite">connecting…</span>
    </p>
  </div>

  <section class="proof" aria-label="Live relay telemetry">
    <p class="proof-title"><span class="proof-dot" aria-hidden="true"></span>Relay telemetry — live from the host side of the bridge</p>
    <div class="proof-grid">
      <div class="proof-stat proof-stat-lead">
        <p class="proof-number"><span id="wb-live-strokes">—</span></p>
        <p class="proof-label">strokes converged on this board</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="wb-live-participants">—</span></p>
        <p class="proof-label">participants in the room</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="wb-live-sent">—</span></p>
        <p class="proof-label">update bytes sent via the host relay</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="wb-live-recv">—</span></p>
        <p class="proof-label">update bytes received via the host relay</p>
      </div>
    </div>
  </section>
  <p class="proof-note">Every number above is traffic <strong>this page</strong> relayed over its own connection. The frame's network is <code>connect-src 'none'</code> — the strokes reaching the other window did not travel through it. Drop the connection, draw offline in both windows, reconnect: the CRDT merges both sides and nobody's strokes lose.</p>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🔒 The frame never connects</h3>
      <p>The framed CSP pins <code>connect-src 'none'</code> — no fetch, no XHR, no WebSocket, no EventSource, ever. Updates leave as opaque <code>ArrayBuffer</code> blobs over the versioned postMessage bridge, and the host owns the only network leg. Collaboration without weakening the cage by one directive.</p>
    </article>
    <article class="card">
      <h3>🔀 Offline edits converge</h3>
      <p>Yjs updates are order-insensitive: apply them in any order and every replica reaches the same board. The reconnect handshake publishes the frame's full state and replays the room's, so the drop-connection control above is a convergence demo, not a reset button.</p>
    </article>
    <article class="card">
      <h3>🎭 Identity is the host's</h3>
      <p>The frame is untrusted, so the room hub assigns each participant an opaque id and a colour — and nothing else. Presence carries no names; the colour you draw in is the colour the host gave you. That is an isolation property, not a styling choice.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · whiteboard %s · <a href="/">all plugins →</a></p>
</footer>
<script>
{{READOUT_JS}}
</script>
<script src="%s"></script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with richtext's and
// datagrid's demo shells so the pages are visibly the same product, plus the
// proof strip. Token-driven throughout — the theme toggle restyles
// everything, and the bridged tokens re-theme the board inside the frame.
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
.lead { color: var(--color-text-muted, #52525B); font-size: var(--text-lg, 1.125rem); max-width: 62ch; line-height: 1.7; margin: 0 0 var(--spacing-lg, 16px); }
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
/* Proof strip: the relay claim, live. The numbers are the point of the
   plugin, so they get display type — the amber tint keys the panel to the
   accent without shouting. */
.proof { margin: var(--spacing-lg, 16px) 0 0; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); background: color-mix(in srgb, var(--color-primary, #e0a040) 4%, var(--color-surface, #fff)); padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); }
.proof-title { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-xs, .75rem); font-weight: 600; letter-spacing: .08em; text-transform: uppercase; color: var(--color-text-subtle, #71717A); display: flex; align-items: center; gap: var(--spacing-md, 8px); }
.proof-dot { width: 8px; height: 8px; flex: none; border-radius: 50%; background: var(--color-success, #166534); animation: proof-pulse 2.4s ease-in-out infinite; }
@keyframes proof-pulse { 0%, 100% { opacity: 1; } 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .proof-dot { animation: none; } }
.proof-grid { display: grid; grid-template-columns: 1.5fr 1fr 1fr 1fr; gap: var(--spacing-md, 8px) var(--spacing-xl, 24px); }
.proof-number { margin: 0; font-size: clamp(1.25rem, 2vw, 1.6rem); font-weight: 700; letter-spacing: -0.02em; line-height: 1.1; font-variant-numeric: tabular-nums; color: var(--color-text, #18181B); }
.proof-stat-lead .proof-number { font-size: clamp(1.75rem, 3.5vw, 2.5rem); }
.proof-label { margin: 4px 0 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); line-height: 1.5; }
.proof-note { margin: var(--spacing-md, 8px) 2px 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); max-width: 80ch; }
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
.save-row { display: flex; align-items: center; gap: var(--spacing-lg, 16px); flex-wrap: wrap; }
.save-status { font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); }
.save-status.is-off { color: var(--color-warning, #854D0E); font-weight: 600; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: var(--spacing-lg, 16px); margin: var(--spacing-sm, 4px) 0 clamp(32px, 5vw, 48px); }
.card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); padding: var(--spacing-xl, 24px); background: var(--color-surface, #fff); box-shadow: var(--shadow-sm, 0 1px 2px 0 rgba(0,0,0,.05)); }
.card h3 { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-base, 1rem); font-weight: 650; }
.card p { margin: 0; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); line-height: 1.6; }
.card a { color: var(--color-primary, #e0a040); }
code { font-family: var(--font-mono, monospace); font-size: .92em; background: var(--color-surface-soft, #F4F4F5); padding: 1px .35em; border-radius: var(--radii-sm, 4px); }
footer { border-top: 1px solid var(--color-border, #E4E4E7); margin-top: var(--spacing-md, 8px); }
footer p { max-width: var(--demo-measure); margin: 0 auto; padding: var(--spacing-xl, 24px) var(--demo-gutter); font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
footer a { color: var(--color-text-muted, #52525B); }
@media (max-width: 900px) { .proof-grid { grid-template-columns: 1fr 1fr; gap: var(--spacing-md, 8px); } }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
  .proof-grid { grid-template-columns: 1fr; }
  /* Keep the board usable on a phone: shorter frame, the page still resolves
     with the proof strip and cards stacked below. !important is required —
     the broker pins the iframe height with an inline style. */
  .editor-card iframe { height: 420px !important; }
}
`

// demoReadoutJS is the demo page's inline script: the theme toggle (persisted
// to the same cookie richtext/datagrid use), the live relay readout, and the
// drop/reconnect + second-window controls. The readout polls the adapter's
// mirrors on the IFRAME ELEMENT — the opaque frame is unreadable from the
// host page, so those mirrors are the only live channel. Spliced in
// post-Sprintf because it contains literal "%".
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

  // The strongest thing this page can show: drop the connection, draw
  // offline in both windows, reconnect, and watch the CRDT merge.
  var dropBtn = document.getElementById('wb-drop');
  var statusEl = document.getElementById('wb-room-status');
  function fmt(n) { return (n || 0).toLocaleString('en-US'); }
  function renderControl() {
    var st = window.__gofastrWhiteboardDemo && window.__gofastrWhiteboardDemo.state();
    if (!st) return;
    if (st.connected) {
      dropBtn.textContent = 'Drop connection';
      dropBtn.classList.add('fui-btn-primary');
      statusEl.textContent = 'room: ' + (st.pid || '?') + ' · ' + st.participants + ' online';
      statusEl.className = 'save-status';
    } else {
      dropBtn.textContent = 'Reconnect';
      dropBtn.classList.remove('fui-btn-primary');
      statusEl.textContent = 'offline — keep drawing; reconnect merges';
      statusEl.className = 'save-status is-off';
    }
  }
  if (dropBtn) {
    dropBtn.addEventListener('click', function () {
      var d = window.__gofastrWhiteboardDemo;
      if (!d) return;
      var st = d.state();
      if (st && st.connected) d.disconnect(); else d.reconnect();
      renderControl();
    });
  }
  var peerBtn = document.getElementById('wb-open-peer');
  if (peerBtn) {
    peerBtn.addEventListener('click', function () {
      window.open(location.pathname, '_blank', 'width=1180,height=860');
    });
  }

  var elS = document.getElementById('wb-live-strokes');
  var elP = document.getElementById('wb-live-participants');
  var elSe = document.getElementById('wb-live-sent');
  var elR = document.getElementById('wb-live-recv');
  function tick() {
    var f = document.querySelector('iframe');
    if (f) {
      if (elS) elS.textContent = typeof f.__wbStrokes === 'number' ? fmt(f.__wbStrokes) : '—';
      if (elP) elP.textContent = typeof f.__wbParticipants === 'number' && f.__wbParticipants > 0 ? fmt(f.__wbParticipants) : (f.__wbConnected ? '1' : '—');
      if (elSe) elSe.textContent = f.__wbSent ? fmt(f.__wbSent.bytes) + ' B' : '—';
      if (elR) elR.textContent = f.__wbRecv ? fmt(f.__wbRecv.bytes) + ' B' : '—';
    }
    renderControl();
  }
  tick();
  setInterval(tick, 250);
})();`

// renderDemo builds the self-contained demo page. The board's persistence is
// the ROOM HUB's (not a form round-trip), so the mount carries only the room
// id; a reload rejoins the same room and the hub replays the board. A
// ?room=<slug> query picks the room (shared-board links, per-test rooms);
// anything but [a-zA-Z0-9-_]{1,64} falls back to the default room.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	mount := Mount(MountConfig{DocID: demoRoomFromQuery(r), MinHeight: demoBoardHeight})
	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then this instance's config.js (publishes
	// __gofastrWhiteboardConfig — whether the hub was wired), then the
	// adapter (registers with the broker and opens the room).
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		tokens,
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

// demoRoomFromQuery reads ?room= with a conservative charset: the value
// becomes an HTML-escaped marker attribute AND a hub map key, so it accepts
// only slug characters and a bounded length, falling back to the default.
func demoRoomFromQuery(r *http.Request) string {
	room := r.URL.Query().Get("room")
	if len(room) > 64 {
		return defaultDocID
	}
	for _, c := range room {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return defaultDocID
		}
	}
	if room == "" {
		return defaultDocID
	}
	return room
}
