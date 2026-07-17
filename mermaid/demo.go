package mermaid

import (
	"encoding/json"
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
<title>Mermaid - Demo</title>
<style>
%s
body { margin: 0; font-family: var(--font-body, system-ui, sans-serif); background: var(--color-background); color: var(--color-text); }
header { display: flex; align-items: center; justify-content: space-between; padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); border-bottom: 1px solid var(--color-border); }
main { max-width: 880px; margin: 0 auto; padding: var(--spacing-xl, 24px); }
h1 { font-size: var(--font-size-xl, 1.25rem); margin: 0; }
p.lead { color: var(--color-text-muted); }
code { font-family: var(--font-mono, monospace); background: var(--color-surface-soft); padding: 0 .25em; border-radius: var(--radii-sm, 4px); }
button.fui-btn { font: inherit; padding: var(--spacing-sm, 4px) var(--spacing-md, 8px); border: 1px solid var(--color-border); border-radius: var(--radii-md, 8px); background: var(--color-surface); color: var(--color-text); cursor: pointer; }
</style>
</head>
<body>
<header>
  <h1>Mermaid - Demo</h1>
  <button type="button" class="fui-btn" id="fui-scheme-toggle">Toggle theme</button>
</header>
<main>
  <p class="lead">This page mounts the sandboxed Mermaid diagram iframe, bridges the framework design tokens across the boundary, and persists saves via the plugin save RPC. The toggle flips <code>data-color-scheme</code> on <code>&lt;html&gt;</code>; the host broker observes it and re-sends tokens.</p>
  <form method="post" action="%s" id="fui-demo-form">
    %s
    <p><button type="submit" class="fui-btn">Save</button> <span id="save-status" role="status" aria-live="polite"></span></p>
  </form>
</main>
<script>
(function () {
  var html = document.documentElement;
  var btn = document.getElementById('fui-scheme-toggle');
  if (btn) {
    btn.addEventListener('click', function () {
      var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
      html.dataset.colorScheme = next;
      btn.textContent = next === 'dark' ? 'Light theme' : 'Toggle theme';
    });
  }
  // The diagram autosaves over the broker RPC every ~2s (the broker mirrors the
  // source into the hidden field on change); the button POSTs that mirror.
  var form = document.getElementById('fui-demo-form');
  var status = document.getElementById('save-status');
  if (form) form.addEventListener('submit', function (e) {
    e.preventDefault();
    var src = (form.querySelector('input[name=diagram_source]') || {}).value || '';
    if (!src) { if (status) status.textContent = 'Nothing to save yet.'; return; }
    if (status) status.textContent = 'Saving…';
    fetch(form.getAttribute('action'), {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ docId: 'demo', source: src, schemaVersion: 'mermaid-v1' })
    }).then(function (r) { if (status) status.textContent = r.ok ? 'Saved ✓' : 'Save failed'; })
      .catch(function () { if (status) status.textContent = 'Save failed'; });
  });
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	source, _ := p.LoadDoc(r.Context(), defaultDocID)
	doc := ""
	if source != "" {
		b, _ := json.Marshal(map[string]string{"source": source})
		doc = string(b)
	}
	mount := Mount(MountConfig{DocID: defaultDocID, Doc: doc})
	return render.HTML(fmt.Sprintf(demoPage, tokens, SaveURL, string(mount), pluginhost.BrokerScriptURL, AdapterScriptURL))
}
