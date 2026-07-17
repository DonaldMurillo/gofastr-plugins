package richtext

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr/core/render"
)

// trustedDemoPage is the frameless demo served at TrustedDemoURL when the host
// opts out of the sandbox via WithTrustedMount (DECISIONS.md "secure by
// default, opt out"). Differences from the framed demo:
//
//   - NO iframe, NO broker: editor-inline.js exposes
//     window.__gofastrRichText.mountTrusted and the small glue script below is
//     the whole host adapter (init, hidden-field sync, save POST, upload POST).
//   - NO token bridge: the editor's scoped stylesheet reads the page's CSS
//     custom properties natively, so the theme toggle restyles the editor with
//     zero plugin traffic (capabilities therefore omit theme:read).
//   - The editor bundle runs with FULL page access — that is the opt-out.
//
// The %s slots are: theme tokens CSS, scoped editor stylesheet URL, save URL,
// initial doc JSON (HTML-safe, "null" when unsaved), plugin version, inline
// bundle URL.
const trustedDemoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Rich Text — Trusted Mount Demo</title>
<style>
%s
{{SHELL_CSS}}
.gofastr-richtext-trusted { min-height: 220px; }
</style>
<link rel="stylesheet" href="%s">
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / richtext</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Sandboxed</a>
    <a class="navlink is-active" href="/__gofastr/plugin/richtext/trusted" aria-current="page">Trusted</a>
    <a class="navlink" href="/__gofastr/plugin/richtext/read?doc=demo">Read&nbsp;view</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>Same editor.<br>No cage.</h2>
    <p class="lead">This page mounts the editor <strong>in-page — no iframe</strong>. Theme tokens are inherited natively via CSS, overlays float over the page like any native menu, and saves POST straight from here. It edits the same document as the sandboxed demo.</p>
  </section>

  <p class="optout-banner"><span aria-hidden="true">⚠️</span><span><strong>This is the explicit opt-out.</strong> The bundle runs with full page access — reserved for plugins the app owner compiles in and vouches for (<code>richtext.WithTrustedMount()</code>, host-side only, never a default).</span></p>

  <form method="post" action="%s" id="fui-demo-form">
    <section class="editor-card">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title">demo.doc</span>
        <span class="editor-mode">trusted · in-page</span>
      </div>
      <div class="gofastr-richtext-trusted">
        <div id="trusted-editor" class="richtext-root"></div>
      </div>
    </section>
    <input type="hidden" name="body_json" value="">
    <input type="hidden" name="body_md" value="">
    <div class="under-editor">
      <p class="hints">
        <span class="hint"><kbd>/</kbd> blocks</span>
        <span class="hint"><kbd>⌘B</kbd> bold</span>
        <span class="hint"><kbd>⌘S</kbd> save</span>
        <span class="hint">menus float — nothing pushes the page</span>
      </p>
      <p><button type="submit" class="fui-btn fui-btn-primary">Save</button></p>
    </div>
  </form>

  <section class="grid" aria-label="Trade-offs">
    <article class="card">
      <h3>⚡ What you gain</h3>
      <p>Native token inheritance (watch the theme toggle), overlays that float over the page, no frame-resize plumbing, simpler debugging.</p>
    </article>
    <article class="card">
      <h3>🧨 What you give up</h3>
      <p>The browser-enforced boundary. A compromised dependency in the bundle can reach this page's session — you are vouching for the whole dependency tree.</p>
    </article>
    <article class="card">
      <h3>🔁 Same contract</h3>
      <p>Identical protocol envelopes over a direct-call transport — a plugin written for the sandbox mounts trusted without code changes.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · richtext %s · <a href="/">back to the sandboxed (default) mount →</a></p>
</footer>
<script type="application/json" id="richtext-trusted-doc">%s</script>
<script src="%s"></script>
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
  var form = document.getElementById('fui-demo-form');
  if (form) form.addEventListener('submit', function (e) { e.preventDefault(); });

  var SAVE_URL = form ? form.getAttribute('action') : '/__gofastr/plugin/richtext/save';
  var UPLOAD_URL = '/__gofastr/plugin/richtext/upload';
  var docEl = document.getElementById('richtext-trusted-doc');
  var initialDoc = null;
  try { initialDoc = JSON.parse(docEl ? docEl.textContent || 'null' : 'null'); } catch (e) { initialDoc = null; }

  function field(name) { return form ? form.querySelector('input[name="' + name + '"]') : null; }

  var api = window.__gofastrRichText.mountTrusted(document.getElementById('trusted-editor'), {
    onEvent: function (method, params) {
      params = params || {};
      switch (method) {
        case 'ready':
          window.__richtextTrustedReady = true;
          window.__richtextTrustedProbes = params.probes || null;
          break;
        case 'docChanged':
          var j = field('body_json');
          var m = field('body_md');
          if (j) j.value = params.doc != null ? JSON.stringify(params.doc) : '';
          if (m) m.value = params.markdown != null ? String(params.markdown) : '';
          break;
        case 'save':
          fetch(SAVE_URL, {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ docId: 'demo', doc: params.doc, markdown: params.markdown, schemaVersion: 'richtext-v1' })
          })['catch'](function () {});
          break;
        case 'requestUpload':
          fetch(UPLOAD_URL, {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'X-Upload-Name': params.name || '', 'X-Upload-Type': params.type || '', 'Content-Type': params.type || 'application/octet-stream' },
            body: params.bytes
          }).then(function (r) { return r.json(); }).then(function (data) {
            api.event('uploadResult', data && data.url
              ? { reqId: params.reqId, url: data.url }
              : { reqId: params.reqId, error: (data && data.error) || 'E_UPLOAD' });
          })['catch'](function (err) {
            api.event('uploadResult', { reqId: params.reqId, error: (err && err.message) || 'E_UPLOAD' });
          });
          break;
        // resize / metric / focusChanged / themeApplied: nothing to do in-page.
      }
    }
  });
  window.__richtextTrustedApi = api;

  api.init({
    doc: initialDoc,
    markdown: '',
    schemaVersion: 'richtext-v1',
    // theme:read deliberately absent — tokens are inherited via CSS in-page.
    capabilities: ['document:read', 'document:write', 'upload:images']
  });
})();
</script>
</body>
</html>`

// renderTrustedDemo builds the frameless demo page. The last-saved doc (if
// any) is embedded as JSON so a reload round-trips it — both demos share the
// same persisted "demo" doc; first-ever load gets the welcome document.
func (p *Plugin) renderTrustedDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	docJSON, ok := p.LoadDoc(r.Context(), defaultDocID)
	if !ok || strings.TrimSpace(docJSON) == "" {
		docJSON = welcomeDoc
	}
	// A "</script>"-bearing doc must not terminate the JSON script block.
	docJSON = strings.ReplaceAll(docJSON, "</", `<\/`)
	// Placeholder splice AFTER formatting for the "%"-bearing shell CSS
	// (see renderDemo).
	page := fmt.Sprintf(trustedDemoPage, schemeFromCookie(r), tokens, ScopedCSSURL, SaveURL, Version, docJSON, InlineJSURL)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}
