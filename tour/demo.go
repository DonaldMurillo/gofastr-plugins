package tour

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoTheme is a minimal token set so the demo page renders with light + dark
// palettes without depending on a UIHost. Mirrors mermaid's demo theme.
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	t.DarkColors = map[string]string{
		"background":    "#0B0B0E",
		"surface":       "#18181B",
		"surface-soft":  "#1F1F23",
		"text":          "#F4F4F5",
		"text-muted":    "#A1A1AA",
		"text-subtle":   "#71717A",
		"border":        "#27272A",
		"border-strong": "#3F3F46",
		"primary":       "#6366F1",
		"primary-fg":    "#FFFFFF",
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

// demoOptions style the demo tour (a purple accent) to show tour-level config.
var demoOptions = TourOptions{Accent: "#7c3aed"}

const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Tour - Demo</title>
<style>
%s
body { margin: 0; font-family: var(--font-body, system-ui, sans-serif); background: var(--color-background); color: var(--color-text); }
header { display: flex; align-items: center; justify-content: space-between; padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); border-bottom: 1px solid var(--color-border); }
main { max-width: 880px; margin: 0 auto; padding: var(--spacing-xl, 24px); }
h1 { font-size: var(--font-size-xl, 1.25rem); margin: 0; }
p.lead { color: var(--color-text-muted); }
button.fui-btn { font: inherit; padding: var(--spacing-sm, 4px) var(--spacing-md, 8px); border: 1px solid var(--color-border); border-radius: var(--radii-md, 8px); background: var(--color-surface); color: var(--color-text); cursor: pointer; }
.card { margin-top: var(--spacing-xl, 24px); padding: var(--spacing-lg, 16px); border: 1px solid var(--color-border); border-radius: var(--radii-md, 8px); background: var(--color-surface); }
code { font-family: var(--font-mono, monospace); background: var(--color-surface-soft); padding: 0 .25em; border-radius: var(--radii-sm, 4px); }
.hidden-panel { display: none; }
.hidden-panel.open { display: block; margin-top: 12px; }
/* Host-page styles for the server-rendered custom step content — because the
   tour bubble lives in this page's DOM (trusted plugin), these apply to it. */
.tour-custom-title { margin: .1em 0 .4em; font-size: 15px; }
.tour-custom-list { margin: .3em 0 .5em; padding-left: 1.15em; }
.tour-custom-list li { margin: .18em 0; }
</style>
</head>
<body>
<header>
  <h1 id="demo-title">Tour - Demo</h1>
  <button type="button" class="fui-btn" id="fui-scheme-toggle">Toggle theme</button>
</header>
<main>
  <p class="lead">This page loads the trusted host-page tour runtime and registers a demo tour with three steps. The tour spotlights real DOM elements on the page.</p>
  <div class="card" id="demo-card">
    <p>Each step cuts a hole around its target element. Try resizing the window or scrolling — the tooltip repositions itself.</p>
    <p>Keyboard: <code>→</code>/<code>Enter</code> next, <code>←</code> back, <code>Esc</code> skip.</p>
  </div>
  <p style="margin-top: 24px;">
    <button type="button" class="fui-btn" id="demo-run">Run tour</button>
    <button type="button" class="fui-btn" id="demo-restart">Restart tour</button>
    <button type="button" class="fui-btn" id="demo-reveal">Advanced &#9662;</button>
  </p>
  <div class="hidden-panel" id="demo-panel">
    <div class="card"><button type="button" class="fui-btn" id="demo-buried">A buried setting</button></div>
  </div>
</main>
<script>
(function () {
  var html = document.documentElement;
  var btn = document.getElementById('fui-scheme-toggle');
  if (btn) btn.addEventListener('click', function () {
    var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
    html.dataset.colorScheme = next;
    btn.textContent = next === 'dark' ? 'Light theme' : 'Toggle theme';
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
  if (reveal && panel) reveal.addEventListener('click', function () { panel.classList.toggle('open'); });
  // Auto-start the demo tour once on first load (skips if already seen).
  whenReady(function () { window.gofastrTour.autoRun('demo'); });
})();
</script>
<script src="%s"></script>
</body>
</html>`

// renderDemo interpolates the demo page: bridged tokens, then the runtime
// script URL (the runtime loads its own stylesheet on init). The demo tour's
// steps are registered server-side via WithTour in the demo caller; if none
// was registered, renderDemo registers a built-in so the page is functional
// out of the box.
func (p *Plugin) renderDemo(_ *http.Request) render.HTML {
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

	return render.HTML(fmt.Sprintf(demoPage, tokens, TourJSURL))
}
