package scanner

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// ZxingVersion is the decoder bundled into the frame. It is stated on the demo
// page, and TestDemoPageStatesTheBundledDecoderVersion requires it to match
// js/package.json — mermaid's page shipped a version twelve releases stale
// because nothing checks prose.
const ZxingVersion = "0.23.0"

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme is the shared showcase palette: a warm near-black surface ladder
// and one amber accent, identical to every other demo page here so they read
// as one product.
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
// demo-page shell (docs/demo-page-design.md).
//
// The %s slots, in document order: color scheme, theme tokens CSS, zxing
// version, mount marker HTML, zxing version again, plugin version, broker URL,
// config.js URL, adapter URL. The shell CSS is spliced in AFTER formatting
// ({{SHELL_CSS}}) because it contains literal "%" that Sprintf must never see
// as format verbs.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Scanner — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / scanner</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/scanner" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>The frame cannot reach your camera.<br>It reads the code anyway.</h2>
    <p class="lead">An opaque-origin frame is refused the camera outright — <code>getUserMedia</code> fails there with <code>SecurityError</code>, and an <code>allow="camera"</code> attribute does not change it. So this page keeps the camera, where the permission prompt belongs, and sends pixels down to the cage. The decoder runs inside, with <code>connect-src 'none'</code>: it can read your barcode and it cannot tell anyone what it read.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">sandbox=&quot;allow-scripts&quot;</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">zxing %s, bundled · no wasm</span>
      <span class="badge">2 grants: scan:decode · theme:read</span>
    </p>
  </section>

  <section class="editor-card" aria-label="Scanner frame">
    <div class="editor-chrome" aria-hidden="true">
      <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
      <span class="editor-title">decoder.cage</span>
      <span class="editor-mode">sandboxed iframe</span>
    </div>
    %s
  </section>
  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint">the camera runs on THIS page</span>
      <span class="hint">only pixels cross the bridge</span>
      <span class="hint">the decode happens in the frame</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">the camera runs on this page</span>
      <span class="hint">the decode happens in the frame</span>
    </p>
    <p class="controls">
      <button type="button" class="fui-btn fui-btn-primary" id="scan-camera">Use camera</button>
      <button type="button" class="fui-btn" id="scan-sample">Scan a sample</button>
      <label class="fui-btn" for="scan-file">Scan an image
        <input type="file" id="scan-file" accept="image/*" class="visually-hidden">
      </label>
    </p>
  </div>

  <div class="readout" aria-label="What the host received">
    <p class="readout-label">what came back across the bridge</p>
    <p class="readout-text" id="scan-result" role="status" aria-live="polite">nothing decoded yet</p>
    <p class="readout-meta">
      <span class="pill" id="scan-format">—</span>
      <span id="scan-decoder">decoder —</span>
      <span class="sep" aria-hidden="true">·</span>
      <span id="scan-timing">—</span>
      <span class="sep" aria-hidden="true">·</span>
      <span class="camera-state" id="scan-camera-state">camera idle</span>
    </p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🔒 A device the cage cannot have</h3>
      <p><code>getUserMedia</code> in an <code>allow-scripts</code> frame fails with <code>SecurityError: Invalid security origin</code>, and adding <code>allow="camera"</code> changes nothing — only <code>allow-same-origin</code> works, which this platform bans. Measured on both engines before any of this was built.</p>
    </article>
    <article class="card">
      <h3>📤 So the host captures instead</h3>
      <p>This page owns the <code>MediaStream</code>, so the permission prompt is against an origin you can read. Each frame becomes grayscale luminance and crosses the bridge as bytes. One frame is in flight at a time; the frame acks every one, decoded or not.</p>
    </article>
    <article class="card">
      <h3>🔀 Two decoders, both tested</h3>
      <p>The platform's <code>BarcodeDetector</code> where the engine has one, zxing %s where it does not — Safari, Firefox and Chromium on Linux. Native goes first for correctness, not speed: zxing's port cannot read some valid QR codes. The e2e forces both paths on every engine.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · scanner %s · <a href="/">all plugins →</a></p>
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

  function demo() { return window.__gofastrScannerDemo; }
  var resultEl = document.getElementById('scan-result');
  var formatEl = document.getElementById('scan-format');
  var decoderEl = document.getElementById('scan-decoder');
  var timingEl = document.getElementById('scan-timing');
  var camEl = document.getElementById('scan-camera-state');
  var camBtn = document.getElementById('scan-camera');

  // The readout is the page's own measurement, polled from the mirrors the
  // adapter sets — the same values the e2e asserts, so what a visitor reads
  // and what the suite checks cannot drift apart.
  var lastSeen = null;
  function tick() {
    var f = document.querySelector('.editor-card iframe');
    if (!f) return;
    var r = f.__scannerLastResult;
    if (r && r.text && r.text !== lastSeen) {
      lastSeen = r.text;
      resultEl.textContent = r.text;
      formatEl.textContent = r.format || '—';
      formatEl.className = 'pill pill-live';
      decoderEl.textContent = 'decoder ' + (r.via || '—');
      timingEl.textContent = Math.round(r.decodeMs) + ' ms';
    }
    var state = f.__scannerCameraState || 'idle';
    camEl.textContent = 'camera ' + state + (f.__scannerCameraError ? ' (' + f.__scannerCameraError + ')' : '');
    camEl.className = 'camera-state' + (state === 'denied' || state === 'unsupported' ? ' is-denied' : '');
    camBtn.textContent = state === 'live' ? 'Stop camera' : 'Use camera';
  }
  setInterval(tick, 200);
  tick();

  camBtn.addEventListener('click', function () {
    var f = document.querySelector('.editor-card iframe');
    var live = f && f.__scannerCameraState === 'live';
    // A refused camera is a state this page renders, not an error it throws:
    // most desktops have no camera and every CI runner denies it.
    if (live) { demo().stopCamera(); } else { demo().startCamera().catch(function () {}); }
  });
  document.getElementById('scan-sample').addEventListener('click', function () {
    demo().scanSample().catch(function () {});
  });
  document.getElementById('scan-file').addEventListener('change', function (e) {
    var file = e.target.files && e.target.files[0];
    if (file) demo().scanImageFile(file).catch(function () {});
  });
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with the other demo
// shells so the pages are visibly the same product. The rules after it are
// this page's own: the control row, the decoded-result readout, and a taller
// frame on phones.
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

.controls { display: flex; flex-wrap: wrap; align-items: center; gap: var(--spacing-md, 8px); }
.readout { display: grid; gap: 6px; margin: var(--spacing-lg, 16px) 2px 0; }
.readout-label { margin: 0; font-size: var(--text-xs, .75rem); text-transform: uppercase; letter-spacing: .08em; color: var(--color-text-subtle, #71717A); }
.sep { color: var(--color-text-subtle, #71717A); }
.readout-text { font-family: var(--font-mono, monospace); font-size: var(--text-lg, 1.125rem); font-weight: 600; color: var(--color-text, #18181B); word-break: break-all; min-height: 1.6em; }
.readout-meta { display: flex; flex-wrap: wrap; gap: var(--spacing-md, 8px); align-items: center; font-size: var(--text-xs, .75rem); color: var(--color-text-muted, #52525B); font-family: var(--font-mono, monospace); }
.pill { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-full, 9999px); padding: 2px 10px; }
.pill-live { border-color: transparent; background: var(--color-primary, #e0a040); color: var(--color-primary-fg, #fff); font-weight: 600; }
.camera-state { font-size: var(--text-xs, .75rem); color: var(--color-text-muted, #52525B); font-family: var(--font-mono, monospace); }
.camera-state.is-denied { color: var(--color-danger, #d1242f); }
.visually-hidden { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0; }
@media (max-width: 560px) { .editor-card iframe { height: 460px !important; } }
`

// renderDemo builds the self-contained demo page.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then this instance's config.js (publishes
	// __gofastrScannerConfig — the formats and capture rate), then the adapter,
	// which owns the camera and merges that config into what it registers.
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		demoTheme().CSSCustomProperties(),
		ZxingVersion,
		string(Mount(MountConfig{})),
		ZxingVersion,
		Version,
		pluginhost.BrokerScriptURL,
		ConfigScriptURL,
		AdapterScriptURL,
	)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}
