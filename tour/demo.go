package tour

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" — the gofastr showcase pages are dark, amber-accented, and
// this demo matches them. Same cookie and default as richtext's, datagrid's,
// mermaid's, monaco's, pdf's and geomap's demos, so the pages open in the same
// scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme mirrors the palette of the framework's own showcase site
// (gofastr/examples/site — the "v2" design), identical to richtext's demo
// theme so the demo pages read as one product: a warm near-black surface
// ladder with a single amber accent, expressed in oklch. The amber accent
// carries into both schemes; the full warm-dark ladder is the DarkColors
// override, which the page shows by default (schemeFromCookie). Token-driven,
// so the whole shell plus the tour bubble pick these up with no per-element
// CSS.
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

// demoTour is the tour the demo page triggers via window.gofastrTour.run().
// Its selectors reference elements on the demo page itself.
var demoTour = []Step{
	{Selector: "#demo-title", Title: "Welcome", Body: "A guided tour running as a trusted host-page plugin — it spotlights real DOM elements on the page.", Placement: PlacementBottom},
	{Selector: "#demo-card", Title: "Spotlight", Body: "Each step dims the page and cuts a hole around its target. The tooltip anchors to whichever side has room.", Placement: PlacementRight},
	// Reaching BURIED UI: the before-actions open the collapsed panel and wait for
	// the target to appear before spotlighting it; the after-action closes it again.
	{
		Selector:  "#demo-buried",
		Title:     "Reaching buried UI",
		Body:      "This control lives in a panel that starts collapsed. The step's before-action clicked “Advanced” to open it, then waited for this element — so tours never dead-end on a hidden target.",
		Placement: PlacementTop,
		Before:    []Action{{Type: "click", Selector: "#demo-reveal"}, {Type: "wait", Selector: "#demo-buried"}},
		After:     []Action{{Type: "click", Selector: "#demo-reveal"}},
	},
	// Custom content rendered by a SERVER-SIDE component: customStepBody() below
	// composes the bubble's HTML in Go, it rides along in the tour JSON, and the
	// runtime mounts it. The host page's CSS (see .tour-* rules) styles it
	// natively — a perk of the trusted host-page model.
	{
		Selector:  "#demo-run",
		Placement: PlacementTop,
		HTML:      customStepBody(),
	},
}

// customStepBody is a small server-side "component": it composes a step's bubble
// content from data (a heading + a list) and returns HTML. This is the
// backend-component → step-content path — render on the server, mount in the
// browser. Swap render.HTML for a gofastr template/component the same way.
func customStepBody() string {
	kinds := []string{
		`an <code>html</code> string (server- or JS-defined)`,
		`a live <code>content</code> node (from JS)`,
		`a <code>render(el)</code> function (from JS)`,
	}
	var items strings.Builder
	for _, k := range kinds {
		items.WriteString("<li>" + k + "</li>")
	}
	return string(render.HTML(fmt.Sprintf(
		`<h3 class="tour-custom-title">Rendered by a Go component</h3>`+
			`<p>This bubble's content was composed <strong>server-side</strong> and mounted in the page. Content can be:</p>`+
			`<ul class="tour-custom-list">%s</ul>`+
			`<p>Click <em>Run tour</em> or call <code>restart('demo')</code> to replay.</p>`,
		items.String())))
}

// demoOptions style the demo tour, to show tour-level config exists. The
// accent is a CSS var reference, not a hex: the runtime sets
// --gofastr-tour-accent on .gofastr-tour-root and every rule reads it through
// var(), so a token reference resolves against THIS page's bridged palette —
// the option is demonstrated (inspect the root's style attribute) without
// hardcoding a colour that matches nothing else in the project.
var demoOptions = TourOptions{Accent: "var(--color-primary)"}

// demoPage is the self-contained HTML document served at DemoURL, on the
// shared demo-page shell (docs/demo-page-design.md; class-for-class with
// richtext's and datagrid's demos) — with the badge and copy stating the
// honest isolation posture: this is a TRUSTED host-page plugin, not a
// sandboxed frame. The "app" inside the window chrome is the surface the
// tour walks; every selector the demo tour targets lives on this page.
//
// The %s slots are: color scheme, theme tokens CSS, plugin version, runtime
// script URL. The shell CSS is spliced in AFTER formatting ({{SHELL_CSS}})
// because it contains literal "%" (color-mix percentages) that Sprintf must
// never see as format verbs.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Tour — Trusted Host-Page Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / tour</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/tour" aria-current="page">Trusted</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>A tour that drives<br>the real page — this one.</h2>
    <p class="lead">The tour this page runs spotlights <strong>live DOM elements you can inspect</strong>, not screenshots of them: dim the page, cut a hole around the target, anchor a stepped bubble to it. It must run in the host page to reach the DOM — that is the trade the badge states — and everything it needs is two grants: <code>tour:read</code> and <code>tour:write</code>.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">trusted host page</span>
      <span class="badge">4 steps · this page</span>
      <span class="badge">grants: tour:read · tour:write</span>
      <span class="badge">seen-state: localStorage + server</span>
    </p>
  </section>

  <section class="editor-card" aria-label="Demo surface the tour walks">
    <div class="editor-chrome">
      <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
      <span class="editor-title" id="demo-title">account-settings.html</span>
      <span class="editor-mode">trusted host page</span>
    </div>
    <div class="demo-app">
      <p class="demo-app-note">This panel is the demo surface: an ordinary chunk of host-page DOM standing in for your app's settings screen. The tour's four steps walk it — the title bar above, the card below, a control buried in the collapsed panel, and the Run button on the strip under this card.</p>
      <div class="demo-card" id="demo-card">
        <p>Each step cuts a hole around its target element. Try resizing the window or scrolling — the tooltip repositions itself.</p>
        <p>Keyboard: <code>→</code>/<code>Enter</code> next, <code>←</code> back, <code>Esc</code> skip.</p>
      </div>
      <div class="demo-advanced">
        <button type="button" class="fui-btn" id="demo-reveal" aria-expanded="false" aria-controls="demo-panel">Advanced &#9662;</button>
        <div class="hidden-panel" id="demo-panel">
          <div class="demo-card"><button type="button" class="fui-btn" id="demo-buried">A buried setting</button></div>
        </div>
      </div>
    </div>
  </section>

  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint"><kbd>→</kbd>/<kbd>Enter</kbd> next</span>
      <span class="hint"><kbd>←</kbd> back</span>
      <span class="hint"><kbd>Esc</kbd> skip</span>
      <span class="hint">auto-runs once per visitor</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Tap Next / Back in the bubble</span>
      <span class="hint">Auto-runs once per visitor</span>
    </p>
    <p class="save-row">
      <button type="button" class="fui-btn fui-btn-primary" id="demo-run">Run tour</button>
      <button type="button" class="fui-btn" id="demo-restart">Restart tour</button>
      <span class="save-status" id="tour-status" role="status" aria-live="polite">Idle</span>
    </p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🎯 The real DOM, not a screenshot</h3>
      <p>The spotlight cuts a hole around a <em>live</em> element — resize or scroll while a step is open and the bubble repositions. Targets are plain selectors or live element refs, and step 3 below proves the point by targeting a control that starts hidden.</p>
    </article>
    <article class="card">
      <h3>⛏️ Reaches buried UI</h3>
      <p>Steps carry <code>Before</code>/<code>After</code> actions: click, wait, navigate. Step 3 opens the collapsed <em>Advanced</em> panel, waits for its control to exist, spotlights it, and closes the panel on the way out — so a tour never dead-ends on a hidden target.</p>
    </article>
    <article class="card">
      <h3>🖨️ Server-composed content</h3>
      <p>Step bodies can be HTML authored in Go and mounted by the runtime — step 4's bubble was composed server-side and is styled by this page's own CSS, a perk of the trusted model. Completion persists to <code>localStorage</code> and to the host, so a finished tour never auto-runs twice.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · tour %s · <a href="/">all plugins →</a></p>
</footer>
<script>
(function () {
  var html = document.documentElement;
  var btn = document.getElementById('fui-scheme-toggle');
  if (btn) btn.addEventListener('click', function () {
    var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
    html.dataset.colorScheme = next;
    document.cookie = 'fui-color-scheme=' + next + '; path=/; max-age=31536000; samesite=lax';
  });
  function whenReady(fn) {
    if (window.gofastrTour && window.gofastrTour.run) fn();
    else setTimeout(function () { whenReady(fn); }, 30);
  }
  var run = document.getElementById('demo-run');
  var restart = document.getElementById('demo-restart');
  if (run) run.addEventListener('click', function () { whenReady(function () { window.gofastrTour.autoRun('demo'); }); });
  if (restart) restart.addEventListener('click', function () { whenReady(function () { window.gofastrTour.restart('demo'); }); });
  // The collapsible "Advanced" panel the buried-target step opens via a before-action.
  var reveal = document.getElementById('demo-reveal');
  var panel = document.getElementById('demo-panel');
  if (reveal && panel) reveal.addEventListener('click', function () {
    var open = panel.classList.toggle('open');
    reveal.setAttribute('aria-expanded', String(open));
  });
  // Live status: the tour overlay's presence in <body> IS the state. Write
  // ONLY on change — this observer watches subtree childList on <body>, and a
  // textContent write inside it re-fires the observer; assigning even an
  // unchanged value replaces the text node, so an unconditional write loops
  // the microtask queue forever and starves everything after tour.js.
  var status = document.getElementById('tour-status');
  var lastStatus = null;
  if (status) {
    var sync = function () {
      var t = document.querySelector('.gofastr-tour-root') ? 'Tour running — Esc to skip.' : 'Idle';
      if (t !== lastStatus) { lastStatus = t; status.textContent = t; }
    };
    if (typeof MutationObserver !== 'undefined') {
      new MutationObserver(sync).observe(document.body, { childList: true, subtree: true });
    }
    sync();
  }
  // Auto-start the demo tour once on first load (skips if already seen).
  whenReady(function () { window.gofastrTour.autoRun('demo'); });
})();
</script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with richtext's demo
// shell so the pages are visibly the same product, plus the demo-surface
// styles this page needs. Token-driven throughout — the theme toggle restyles
// everything, and the tour runtime reads the same tokens for its bubble.
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
.editor-chrome { display: flex; align-items: center; gap: 7px; padding: var(--spacing-md, 8px) var(--spacing-lg, 16px); border-bottom: 1px solid var(--color-border, #E4E4E7); background: var(--color-surface-soft, #F4F4F5); }
.dot { width: 10px; height: 10px; border-radius: 50%; opacity: .9; }
.dot-r { background: #ff5f57; } .dot-y { background: #febc2e; } .dot-g { background: #28c840; }
.editor-title { margin-left: var(--spacing-md, 8px); font-size: var(--text-xs, .75rem); font-family: var(--font-mono, monospace); color: var(--color-text-muted, #52525B); }
.editor-mode { margin-left: auto; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-full, 9999px); padding: 2px 10px; }
/* Demo surface: the chunk of "app" the tour walks, inside the chrome card. */
.demo-app { padding: clamp(20px, 3vw, 32px); display: flex; flex-direction: column; gap: var(--spacing-lg, 16px); }
.demo-app-note { margin: 0; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); max-width: 65ch; }
.demo-card { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface-soft, #F4F4F5); padding: var(--spacing-lg, 16px); }
.demo-card p { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); }
.demo-card p:last-child { margin-bottom: 0; }
.demo-advanced { display: flex; flex-direction: column; gap: var(--spacing-md, 8px); align-items: flex-start; }
.hidden-panel { display: none; }
.hidden-panel.open { display: block; }
.hidden-panel .demo-card { margin-top: 0; }
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
/* Host-page styles for the server-rendered custom step content — because the
   tour bubble lives in this page's DOM (trusted plugin), these apply to it. */
.tour-custom-title { margin: .1em 0 .4em; font-size: 15px; }
.tour-custom-list { margin: .3em 0 .5em; padding-left: 1.15em; }
.tour-custom-list li { margin: .18em 0; }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
}
`

// renderDemo interpolates the demo page: bridged tokens, then the runtime
// script URL (the runtime loads its own stylesheet on init). The demo tour's
// steps are registered server-side via WithTour in the demo caller; if none
// was registered, renderDemo registers a built-in so the page is functional
// out of the box.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()

	// Ensure the "demo" tour the page auto-runs exists, so the page works out of
	// the box even when the caller registered no tours of its own.
	p.mu.Lock()
	if _, ok := p.tours["demo"]; !ok {
		p.tourOrder = append(p.tourOrder, "demo")
		o := demoOptions
		p.tours["demo"] = Tour{ID: "demo", Steps: append([]Step{}, demoTour...), Options: &o}
	}
	p.mu.Unlock()

	page := fmt.Sprintf(demoPage, schemeFromCookie(r), tokens, Version, TourJSURL)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}
