package pdf

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// PdfjsVersion is the pdf.js release compiled into the frame bundle, and
// PdfLibVersion the writer used to rebuild a redacted document. Both are stated
// on the demo page, and TestDemoPageStatesTheBundledLibraryVersions requires
// them to match js/package.json — mermaid's page shipped a version twelve
// releases stale because nothing checks prose.
const (
	PdfjsVersion  = "6.2.108"
	PdfLibVersion = "1.17.1"
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
// and one amber accent, identical to richtext's and mermaid's demo themes so
// the pages read as one product. It replaces this page's old indigo, which came
// from nowhere in the design system, and it re-themes the viewer inside the
// frame too — the toolbar and the Export button take their colours from the
// bridged tokens, which is why the old page had a purple button on an amber
// site (docs/demo-page-design.md names that button by way of example).
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
// demo-page shell (docs/demo-page-design.md), class-for-class with richtext's
// and mermaid's demos.
//
// The %s slots, in document order: color scheme, theme tokens CSS, pdf.js
// version, pdf-lib version, mount marker HTML, pdf.js version again, pdf-lib
// version again, plugin version, platform broker URL, this instance's
// config.js URL, adapter URL. Passing the mount one position early puts the
// iframe inside the fact chips and every test still passes, so
// TestDemoPageMountsInsideTheEditorCard asserts the shape.
//
// The shell CSS is spliced in AFTER formatting ({{SHELL_CSS}}) because it
// contains literal "%" (color-mix percentages) that Sprintf must never see as
// format verbs — the trap that broke this file once already.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr PDF — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / pdf</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/pdf" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>Redaction that deletes.<br>In a frame with no network.</h2>
    <p class="lead">Draw a box over something on page 1 and press <strong>Apply redaction</strong>. The page is rasterized, the document rebuilt without the original content, and the result checked before it is handed back — a black rectangle drawn over selectable text is not redaction, and this does not ship one. The viewer runs under <code>connect-src 'none'</code>, so the code editing your document has nowhere to send it.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">sandbox=&quot;allow-scripts&quot;</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">pdf.js %s · pdf-lib %s</span>
      <span class="badge">3 grants: document:read · document:write · theme:read</span>
    </p>
  </section>

  <section class="editor-card" aria-label="PDF viewer frame">
    <div class="editor-chrome" aria-hidden="true">
      <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
      <span class="editor-title">sample.pdf</span>
      <span class="editor-mode">sandboxed iframe</span>
    </div>
    %s
  </section>
  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint">drag on the page to mark a region</span>
      <span class="hint">Apply redaction rebuilds the file</span>
      <span class="hint">Export downloads it over the bridge</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Drag to mark a region</span>
      <span class="hint">Apply redaction rebuilds the file</span>
    </p>
    <p class="proof" id="pdf-host-status" role="status" aria-live="polite"><span class="proof-dot" aria-hidden="true"></span><span id="pdf-host-status-text">waiting for the frame…</span></p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🔒 An opaque origin</h3>
      <p><code>sandbox="allow-scripts"</code> with no <code>allow-same-origin</code>: the frame's origin is null, so cookies, storage and this page's DOM are unreachable by construction. The document arrives over the postMessage bridge and produced files leave the same way.</p>
    </article>
    <article class="card">
      <h3>📴 Nowhere to send it</h3>
      <p>The framed CSP says <code>connect-src 'none'</code>. pdf.js %s and pdf-lib %s are compiled into the frame as one bundle — no CDN, no worker URL, no telemetry. A viewer that cannot open a socket cannot leak the document it is showing.</p>
    </article>
    <article class="card">
      <h3>🗜 Checked, not trusted</h3>
      <p>Redaction rasterizes the page and rebuilds the document, then the server verifies the result before returning it: the covered text must be gone from the extracted content, not merely painted over. The e2e suite asserts that on every run.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · pdf %s · <a href="/">all plugins →</a></p>
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
  // The live proof line. The adapter mirrors the frame's render result onto the
  // iframe element (__pdf*); this reports it where a visitor can see it, so the
  // page's central claim is the page's own measurement rather than a sentence
  // someone typed. Polled, because the mirrors are set from bridge events.
  var row = document.getElementById('pdf-host-status');
  var text = document.getElementById('pdf-host-status-text');
  function tick() {
    var f = document.querySelector('iframe');
    if (!f || !row || !text) return;
    if (f.__pdfError) {
      row.className = 'proof is-err';
      text.textContent = 'render error: ' + f.__pdfError;
      return;
    }
    if (f.__pdfRendered) {
      row.className = 'proof is-live';
      text.textContent = 'page 1 of ' + f.__pdfPageCount + ' rendered · ' +
        Number(f.__pdfNonWhitePixels || 0).toLocaleString('en-US') + ' inked px · pdf.js ' + f.__pdfPdfjsVersion;
      return;
    }
    if (f.__pdfReady) { text.textContent = 'frame ready — rendering…'; }
  }
  setInterval(tick, 250);
  tick();
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with richtext's and
// mermaid's demo shells so the pages are visibly the same product. The rules
// after it are this page's only additions: the live render readout that sits
// where other pages put a save button, and a taller frame on phones.
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

.proof { display: flex; align-items: center; gap: 8px; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); font-family: var(--font-mono, monospace); }
.proof-dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--color-text-subtle, #71717A); }
.proof.is-live .proof-dot { background: var(--color-primary, #e0a040); box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-primary, #e0a040) 25%, transparent); }
.proof.is-err { color: var(--color-danger, #d1242f); }
.proof.is-err .proof-dot { background: var(--color-danger, #d1242f); box-shadow: none; }
@media (max-width: 560px) {
  /* Override the shell's 380px phone height. This viewer's chrome is three
     stacked toolbars — annotation tools, the redaction panel, then paging and
     search — which at 380px leaves a demo page whose plugin area shows only
     buttons and not one line of the document. Found by screenshot, which is
     the only thing that finds it. */
  .editor-card iframe { height: 680px !important; }
}
`

// renderDemo builds the self-contained demo page.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	// The ?doc= query param is the SEAM the scanned-document regression tests
	// use: they point the plugin at a fixture via WithSource and load
	// /pdf?doc=<fixture-id> so the mount marker carries that id and the adapter
	// fetches /doc/<fixture-id>. Without ?doc= the demo mounts the default
	// "demo" id, which the default source resolves to the embedded sample.
	docID := r.URL.Query().Get("doc")
	if docID == "" {
		docID = defaultDocID
	}
	mount := Mount(MountConfig{DocID: docID})
	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then this instance's config.js (publishes
	// __gofastrPdfConfig — the mode + redact DPI the frame reads from
	// init.config), then the adapter (registers with the broker, merging the
	// config global into the manifest config it registers).
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		demoTheme().CSSCustomProperties(),
		PdfjsVersion,
		PdfLibVersion,
		string(mount),
		PdfjsVersion,
		PdfLibVersion,
		Version,
		pluginhost.BrokerScriptURL,
		ConfigScriptURL,
		AdapterScriptURL,
	)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}
