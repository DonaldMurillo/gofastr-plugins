package pdf

import (
	"fmt"
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

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

const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PDF viewer — SPIKE demo</title>
<style>
%s
body { margin: 0; font-family: var(--font-body, system-ui, sans-serif); background: var(--color-background); color: var(--color-text); }
header { display: flex; align-items: center; justify-content: space-between; padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); border-bottom: 1px solid var(--color-border); }
main { max-width: 900px; margin: 0 auto; padding: var(--spacing-xl, 24px); }
h1 { font-size: var(--font-size-xl, 1.25rem); margin: 0; }
p.lead { color: var(--color-text-muted); }
code { font-family: var(--font-mono, monospace); background: var(--color-surface-soft); padding: 0 .25em; border-radius: var(--radii-sm, 4px); }
button.fui-btn { font: inherit; padding: var(--spacing-sm, 4px) var(--spacing-md, 8px); border: 1px solid var(--color-border); border-radius: var(--radii-md, 8px); background: var(--color-surface); color: var(--color-text); cursor: pointer; }
</style>
</head>
<body>
<header>
  <h1>PDF viewer — SPIKE</h1>
  <button type="button" class="fui-btn" id="fui-scheme-toggle">Toggle theme</button>
</header>
<main>
  <p class="lead">An opaque-origin sandboxed iframe runs <code>pdf.js</code> worker-free on the main thread under <code>connect-src 'none'</code>. The host fetches the sample PDF same-origin and forwards the bytes over the <code>postMessage</code> bridge; the frame fetches nothing. Watch this console — the spike asserts ZERO CSP violations and ZERO console errors.</p>
  <p class="lead" id="pdf-host-status" role="status" aria-live="polite">Waiting for frame…</p>
  %s
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
  // Surface frame→host events on the demo page for eyeballing. The iframe
  // element carries the __pdf* mirrors the adapter sets; poll once mounted.
  var status = document.getElementById('pdf-host-status');
  var frame0 = document.querySelector('iframe[data-fui-plugin-frame]');
  function tick() {
    var f = frame0;
    if (!f) f = document.querySelector('iframe');
    if (f && f.__pdfRendered) {
      status.textContent = 'Rendered page 1 of ' + f.__pdfPageCount +
        ' (' + f.__pdfNonWhitePixels + ' inked px, pdf.js ' + f.__pdfPdfjsVersion + ').';
    } else if (f && f.__pdfError) {
      status.textContent = 'Render error: ' + f.__pdfError;
    } else if (f && f.__pdfReady) {
      status.textContent = 'Frame ready; rendering…';
    }
  }
  setInterval(tick, 250);
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	// The ?doc= query param is the SEAM the scanned-document regression tests
	// use: they point the plugin at a fixture via WithSource and load
	// /pdf?doc=<fixture-id> so the mount marker carries that id and the adapter
	// fetches /doc/<fixture-id>. Without ?doc= the demo mounts the default
	// "demo" id, which the default source resolves to the embedded sample.
	tokens := demoTheme().CSSCustomProperties()
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
	return render.HTML(fmt.Sprintf(demoPage,
		tokens,
		string(mount),
		pluginhost.BrokerScriptURL,
		ConfigScriptURL,
		AdapterScriptURL,
	))
}
