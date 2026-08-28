package imageedit

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoEditorHeight is the iframe height the demo page mounts at: tall enough
// that the editor IS the page's argument (toolbar + the sample at readable
// scale), so the page resolves without dead space under a short frame.
const demoEditorHeight = "560px"

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
// datagrid's demo themes so all demo pages read as one product: a warm
// near-black surface ladder with a single amber accent, expressed in oklch.
// Token-driven, so the whole shell plus the bridged editor pick these up
// with no per-element CSS.
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
		// Status tones for the frame's redact button, restated lighter for
		// the dark ladder.
		"success": "oklch(0.76 0.14 158)",
		"warning": "oklch(0.82 0.12 80)",
		"danger":  "oklch(0.72 0.15 25)",
	}
	return t
}

// demoPage is the self-contained HTML document served at DemoURL. It runs
// with NO UIHost: the theme tokens are inlined in <style>, the mount marker
// holds the editor iframe, and the host scripts are included directly — the
// generic platform broker (pluginhost.js) first, then this instance's
// config.js, then the adapter. The inline <script> (toggle + live proof
// readout) is acceptable ON THIS DEMO PAGE ONLY — the broker/adapter and the
// frame stay CSP-clean and same-origin-script only, the same allowance the
// richtext and datagrid demos make.
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
<title>GoFastr Image Editor — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / imageedit</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/imageedit" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>Crop, annotate, redact.<br>The server decides what's true.</h2>
    <p class="lead">The editor below runs in an opaque-origin iframe whose CSP forbids every fetch — <code>connect-src 'none'</code>. The image bytes arrive over the postMessage bridge; the frame previews an operation list; and every export is re-rendered by Go from that list, stripped of EXIF, and checked — a redacted region must be <em>gone from the bytes</em> before anything is released. Redact the token on the sample card and export to watch it happen.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">connect-src 'none'</span>
      <span class="badge">sandbox="allow-scripts"</span>
      <span class="badge">doc = ops, never pixels</span>
      <span class="badge">EXIF stripped by re-encode</span>
      <span class="badge">crop → rotate → annotate → redact</span>
    </p>
  </section>

  <form method="post" action="%s" id="fui-demo-form">
    <section class="editor-card" aria-label="Image editor frame">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title">field-report-042.png</span>
        <span class="editor-mode">sandboxed iframe</span>
      </div>
      %s
    </section>
  </form>

  <section class="proof" aria-label="Live proof">
    <p class="proof-title"><span class="proof-dot" aria-hidden="true"></span>Preview vs server — live</p>
    <div class="proof-pair">
      <figure class="proof-fig">
        <img id="ie-proof-preview" alt="The frame's own 1:1 render of the operation list" src="" hidden>
        <figcaption>frame preview <span class="proof-cap-dim">(canvas, in the cage)</span></figcaption>
      </figure>
      <figure class="proof-fig">
        <img id="ie-proof-export" alt="The server-rendered export" src="" hidden>
        <figcaption>server render <span class="proof-cap-dim">(Go, from the doc)</span></figcaption>
      </figure>
      <div class="proof-stats">
        <p class="proof-number"><span id="ie-proof-match">—</span></p>
        <p class="proof-label">sampled pixels identical<br><span id="ie-proof-dims"></span></p>
        <p class="proof-number proof-number-sm"><span id="ie-proof-token">—</span></p>
        <p class="proof-label">token pixels after export<br><span class="proof-cap-dim">(black = redacted)</span></p>
        <p class="proof-number proof-number-sm"><span id="ie-proof-bytes">—</span></p>
        <p class="proof-label">export size + digest</p>
      </div>
    </div>
    <pre id="ie-proof-doc" class="proof-doc" aria-label="Live operation list">—</pre>
    <p class="proof-note">The operation list above is the document — it updates as you edit. Click Export and the panel samples both renders at 200 deterministic points and diffs them here; the agreement you can see is the same property the Go and e2e tests assert.</p>
  </section>

  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint"><kbd>drag</kbd> rect / arrow / redaction</span>
      <span class="hint"><kbd>click</kbd> place text</span>
      <span class="hint"><kbd>↻</kbd> rotate 90°</span>
      <span class="hint"><kbd>↶</kbd> undo</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Drag to draw</span>
      <span class="hint">Tap to place text</span>
      <span class="hint">Rotate with ↻</span>
    </p>
    <p class="save-row"><span class="save-status">operation list autosaves over the bridge · export re-renders in Go</span></p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>剪刀 Pixels never cross as the document</h3>
      <p>The canonical doc is <code>{src, crop, rotate, annotations[], redactions[]}</code> in image coordinates — schema <code>imageedit-v1</code>. The frame mirrors it to a hidden field like every plugin here; the bitmap itself is resolved host-side by <code>GET /img/{id}</code> and relayed as an <code>ArrayBuffer</code>.</p>
    </article>
    <article class="card">
      <h3>🧾 Go renders the saved result</h3>
      <p>Export POSTs only the doc. The server decodes, applies <code>crop → rotate → annotate → redact</code> with the standard library, strips EXIF by full re-encode, enforces the <code>16 MiB / 24 MP / 8192px</code> caps at the header stage, and re-encodes — so a lying client cannot change what gets stored.</p>
    </article>
    <article class="card">
      <h3>▮ Redaction is verified, not painted</h3>
      <p>Before any bytes are released, Go walks every redaction rect in the OUTPUT and requires every pixel to equal the fill — a mapped wrong or content covered-but-present fails the export with <code>E_REDACT_VERIFY</code>. The counter above reads the token region after export: all black, or the export never happened.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · imageedit %s · <a href="/">all plugins →</a></p>
</footer>
<script>
{{READOUT_JS}}
</script>
<script src="%s"></script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with the richtext and
// datagrid demo shells so the pages are visibly the same product, plus the
// imageedit proof panel. Token-driven throughout — the theme toggle restyles
// everything, and the bridged tokens re-theme the editor inside the frame.
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
/* Proof panel: preview vs server, live. The numbers are the point of the
   plugin, so they get display type and the pair sits side by side. */
.proof { margin: var(--spacing-lg, 16px) 0 0; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); background: color-mix(in srgb, var(--color-primary, #e0a040) 4%, var(--color-surface, #fff)); padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); }
.proof-title { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-xs, .75rem); font-weight: 600; letter-spacing: .08em; text-transform: uppercase; color: var(--color-text-subtle, #71717A); display: flex; align-items: center; gap: var(--spacing-md, 8px); }
.proof-dot { width: 8px; height: 8px; flex: none; border-radius: 50%; background: var(--color-success, #166534); animation: proof-pulse 2.4s ease-in-out infinite; }
@keyframes proof-pulse { 0%, 100% { opacity: 1; } 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .proof-dot { animation: none; } }
.proof-pair { display: grid; grid-template-columns: 1fr 1fr 0.9fr; gap: var(--spacing-lg, 16px); align-items: start; }
.proof-fig { margin: 0; }
/* [hidden] must win over the display rule below, or the export slot renders a
   broken-image icon before the first export and the page looks broken on
   arrival. An element-level [hidden] rule loses to a class selector, so scope
   it to the figure. */
.proof-fig img[hidden] { display: none; }
.proof-fig img { display: block; width: 100%; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 6px); background: var(--color-surface, #fff); }
.proof-fig figcaption { margin-top: 6px; font-size: var(--text-xs, .75rem); color: var(--color-text-muted, #52525B); font-weight: 600; }
.proof-cap-dim { color: var(--color-text-subtle, #71717A); font-weight: 400; }
.proof-stats { display: flex; flex-direction: column; gap: 2px; }
.proof-number { margin: 0; font-size: clamp(1.4rem, 2.4vw, 2rem); font-weight: 700; letter-spacing: -0.02em; line-height: 1.1; font-variant-numeric: tabular-nums; color: var(--color-text, #18181B); }
.proof-number-sm { font-size: clamp(1rem, 1.6vw, 1.25rem); }
.proof-label { margin: 2px 0 var(--spacing-md, 8px); font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); line-height: 1.5; }
.proof-doc { margin: var(--spacing-md, 8px) 0 0; padding: var(--spacing-md, 8px) var(--spacing-lg, 16px); border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 6px); background: var(--color-code-surface, #F4F4F5); color: var(--color-code-text, #18181B); font-family: var(--font-mono, monospace); font-size: var(--text-xs, .75rem); line-height: 1.5; overflow: auto; max-height: 220px; white-space: pre-wrap; word-break: break-all; }
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
@media (max-width: 860px) { .proof-pair { grid-template-columns: 1fr 1fr; } .proof-stats { grid-column: 1 / -1; flex-direction: row; flex-wrap: wrap; gap: var(--spacing-xl, 24px); } }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
  .proof-pair { grid-template-columns: 1fr; }
  /* Keep the editor usable on a phone: shorter frame. !important is required —
     the broker pins the iframe height with an inline style. */
  .editor-card iframe { height: 440px !important; }
}
`

// demoReadoutJS is the demo page's inline script: the theme toggle (persisted
// to the same cookie richtext/datagrid use) and the live proof readout. The
// readout polls the adapter's mirrors on the IFRAME ELEMENT — the opaque
// frame is unreadable from the host page, so those mirrors (live doc JSON,
// the frame's own preview data URL, the last export facts) are the only
// live channel. On a fresh export it loads both renders into canvases and
// samples 200 deterministic points so the preview-vs-server agreement is
// something the visitor can SEE. Spliced in post-Sprintf because it contains
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

  var elDoc = document.getElementById('ie-proof-doc');
  var elPrev = document.getElementById('ie-proof-preview');
  var elExp = document.getElementById('ie-proof-export');
  var elMatch = document.getElementById('ie-proof-match');
  var elDims = document.getElementById('ie-proof-dims');
  var elToken = document.getElementById('ie-proof-token');
  var elBytes = document.getElementById('ie-proof-bytes');

  // The sample's secret-token rect, as known server-side (source coords) —
  // the demo displays the redaction outcome at the same place the Go
  // verifier checks it.
  var TOKEN = { x: %d, y: %d, w: %d, h: %d };

  function frame() { return document.querySelector('iframe'); }
  function prettyDoc(json) {
    try {
      var d = JSON.parse(json);
      return JSON.stringify(d, null, 2);
    } catch (e) {
      return json || '—';
    }
  }

  function tick() {
    var f = frame();
    if (!f) return;
    if (elDoc && typeof f.__imageeditDoc === 'string') {
      var pretty = prettyDoc(f.__imageeditDoc);
      if (elDoc.textContent !== pretty) elDoc.textContent = pretty;
    }
    var p = f.__imageeditPreview;
    if (p && p.dataUrl && elPrev && elPrev.getAttribute('src') !== p.dataUrl) {
      elPrev.src = p.dataUrl;
      elPrev.hidden = false;
    }
    var e = f.__imageeditLastExport;
    if (e && e.url && elExp && elExp.getAttribute('src') !== e.url) {
      elExp.src = e.url;
      elExp.hidden = false;
      compare(f);
    }
  }

  // Load an image URL into an offscreen canvas and hand back its pixels.
  function pixels(src, cb) {
    var img = new Image();
    img.onload = function () {
      var c = document.createElement('canvas');
      c.width = img.naturalWidth; c.height = img.naturalHeight;
      var cx = c.getContext('2d', { willReadFrequently: true });
      cx.drawImage(img, 0, 0);
      cb(cx.getImageData(0, 0, c.width, c.height));
    };
    img.onerror = function () { cb(null); };
    img.src = src;
  }

  // Sample both renders at deterministic points and report the agreement.
  function compare(f) {
    var p = f.__imageeditPreview;
    var e = f.__imageeditLastExport;
    if (!p || !p.dataUrl || !e || !e.url) return;
    pixels(p.dataUrl, function (a) {
      pixels(e.url, function (b) {
        if (!a || !b) { if (elMatch) elMatch.textContent = 'n/a'; return; }
        if (elDims) elDims.textContent = a.width + '×' + a.height + ' vs ' + b.width + '×' + b.height;
        if (a.width !== b.width || a.height !== b.height) {
          if (elMatch) { elMatch.textContent = 'dims differ'; elMatch.style.color = 'var(--color-danger, #c02e22)'; }
          return;
        }
        // 200 deterministic sample points (golden-ratio spacing, both axes).
        var pts = [], PHI = 0.6180339887498949;
        for (var i = 0; i < 200; i++) {
          var fx = ((i + 1) * PHI) %% 1, fy = ((i * 7 + 3) * PHI) %% 1;
          pts.push([Math.floor(fx * a.width), Math.floor(fy * a.height)]);
        }
        var same = 0;
        for (var k = 0; k < pts.length; k++) {
          var ia = (pts[k][1] * a.width + pts[k][0]) * 4;
          var ib = (pts[k][1] * b.width + pts[k][0]) * 4;
          if (Math.abs(a.data[ia] - b.data[ib]) <= 2 &&
              Math.abs(a.data[ia+1] - b.data[ib+1]) <= 2 &&
              Math.abs(a.data[ia+2] - b.data[ib+2]) <= 2) same++;
        }
        if (elMatch) {
          elMatch.textContent = same + '/' + pts.length;
          elMatch.style.color = same === pts.length
            ? 'var(--color-success, #166534)'
            : 'var(--color-danger, #c02e22)';
        }
        // Token census in the EXPORT: how many pixels in the token rect are
        // pure black. Before a redaction this is ~0; after one it is all.
        var black = 0, total = 0;
        for (var yy = TOKEN.y; yy < TOKEN.y + TOKEN.h; yy += 2) {
          for (var xx = TOKEN.x; xx < TOKEN.x + TOKEN.w; xx += 2) {
            if (xx >= b.width || yy >= b.height) continue;
            var ib2 = (yy * b.width + xx) * 4;
            total++;
            if (b.data[ib2] < 16 && b.data[ib2+1] < 16 && b.data[ib2+2] < 16) black++;
          }
        }
        if (elToken) {
          elToken.textContent = total > 0 ? Math.round(black / total * 100) + '%%' : '—';
          elToken.style.color = total > 0 && black === total
            ? 'var(--color-success, #166534)'
            : 'var(--color-text, #18181B)';
        }
        if (elBytes && e.sha256) {
          elBytes.textContent = e.bytes.toLocaleString('en-US') + ' B';
          elBytes.title = 'sha256:' + e.sha256.slice(0, 16) + '…';
        }
      });
    });
  }

  tick();
  setInterval(tick, 250);
})();`

// renderDemo builds the self-contained demo page. The mount's initial doc is
// the last-saved operation list when one exists (LoadDoc), else a doc that
// names the sample image with no operations.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	docJSON, ok := p.LoadDoc(r.Context(), defaultDocID)
	if !ok {
		docJSON = fmt.Sprintf(`{"schemaVersion":%q,"src":{"kind":"id","ref":%q},"rotate":0,"annotations":[],"redactions":[]}`,
			SchemaVersion, defaultDocID)
	}
	mount := Mount(MountConfig{DocID: defaultDocID, Doc: docJSON, MinHeight: demoEditorHeight})
	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then this instance's config.js (publishes
	// __gofastrImageeditConfig — whether upload:images was wired), then the
	// adapter (registers with the broker, merging the config global into the
	// capabilities it registers).
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
	readout := fmt.Sprintf(demoReadoutJS,
		SampleTokenRect().X, SampleTokenRect().Y, SampleTokenRect().W, SampleTokenRect().H)
	page = strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1)
	page = strings.Replace(page, "{{READOUT_JS}}", readout, 1)
	return render.HTML(page)
}
