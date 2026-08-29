package monaco

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" — the gofastr showcase pages are dark, amber-accented, and
// this demo matches them. Same cookie and default as richtext's, datagrid's
// and mermaid's demos, so the pages open in the same scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// NOTE: this demo's tokens are the same warm-amber palette as richtext's, but
// expressed as EXACT sRGB hex equivalents of the oklch values rather than
// oklch itself: the mermaid/monaco bundles PARSE bridged token values in
// JavaScript to compute their own theme colors, and their parsers reject
// oklch outright ("Unsupported color format"/"Illegal value for token
// color"), which killed the first render on this page. Browsers render the
// hex equivalents pixel-identically, so the page still matches the product.
// demoTheme mirrors the palette of the framework's own showcase site
// (gofastr/examples/site — the "v2" design), identical to richtext's demo
// theme so the demo pages read as one product: a warm near-black surface
// ladder with a single amber accent, expressed in oklch. The amber accent
// carries into both schemes; the full warm-dark ladder is the DarkColors
// override, which the page shows by default (schemeFromCookie). Token-driven,
// so the whole shell plus the bridged editor pick these up with no
// per-element CSS.
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	// One amber accent, in both schemes (examples/site theme.go).
	t.Colors.Primary = style.Color{Name: "primary", Value: "#FBB636"}
	t.Colors.PrimaryFg = style.Color{Name: "primary-fg", Value: "#0A0907"}
	t.Colors.Accent = style.Color{Name: "accent", Value: "#FBB636"}
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
		// Translucent amber selection so highlighted text stays legible on dark.
		"selection": "#FBB6364D",
	}
	return t
}

// demoLanguages is the language picker's option set (mirrors the basic-languages
// the frame bundles). Order is display order.
var demoLanguages = []string{
	"typescript", "javascript", "go", "python", "json",
	"markdown", "html", "css", "sql", "yaml", "shell", "plaintext",
}

// demoExt maps a picker language to the filename extension the window chrome
// shows (demo.<ext>). The chrome title follows the live language via syncUI.
var demoExt = map[string]string{
	"typescript": "ts", "javascript": "js", "go": "go", "python": "py",
	"json": "json", "markdown": "md", "html": "html", "css": "css",
	"sql": "sql", "yaml": "yaml", "shell": "sh", "plaintext": "txt",
}

// demoSamples is the code loaded by the "Load sample" control per language.
var demoSamples = map[string]string{
	"typescript": "interface User {\n  id: number;\n  name: string;\n  roles: string[];\n}\n\nexport function greet(u: User): string {\n  const admin = u.roles.includes(\"admin\");\n  return `Hello ${u.name}` + (admin ? \" (admin)\" : \"\");\n}\n",
	"javascript": "const fib = (n) => (n < 2 ? n : fib(n - 1) + fib(n - 2));\nconsole.log([...Array(10)].map((_, i) => fib(i)));\n",
	"go":         "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfor i, s := range []string{\"a\", \"b\", \"c\"} {\n\t\tfmt.Printf(\"%d=%s\\n\", i, s)\n\t}\n}\n",
	"python":     "from dataclasses import dataclass\n\n@dataclass\nclass Point:\n    x: float\n    y: float\n\n    def dist(self) -> float:\n        return (self.x ** 2 + self.y ** 2) ** 0.5\n\nprint(Point(3, 4).dist())\n",
	"json":       "{\n  \"name\": \"gofastr-plugins\",\n  \"version\": \"0.1.0\",\n  \"plugins\": [\"richtext\", \"mermaid\", \"monaco\", \"tour\"],\n  \"sandboxed\": true\n}\n",
	"markdown":   "# Monaco showcase\n\n- **Bold**, _italic_, `code`\n- [a link](https://example.com)\n\n> A blockquote.\n\n```go\nfmt.Println(\"fenced\")\n```\n",
	"html":       "<!doctype html>\n<html>\n  <head><title>Hi</title></head>\n  <body>\n    <h1 class=\"title\">Hello</h1>\n  </body>\n</html>\n",
	"css":        ".card {\n  display: grid;\n  gap: 12px;\n  padding: 18px;\n  border-radius: 14px;\n  background: var(--surface, #fff);\n}\n",
	"sql":        "SELECT u.name, count(o.id) AS orders\nFROM users u\nLEFT JOIN orders o ON o.user_id = u.id\nWHERE u.active = true\nGROUP BY u.name\nORDER BY orders DESC;\n",
	"yaml":       "service: gofastr\nplugins:\n  - name: monaco\n    sandboxed: true\n  - name: tour\n    trusted: true\n",
	"shell":      "#!/usr/bin/env bash\nset -euo pipefail\nfor f in *.go; do\n  echo \"vet: $f\"\ndone\n",
	"plaintext":  "Plain text — no tokenizer.\nType anything here.\n",
}

// diffOriginal / diffModified feed the "Diff view" control.
const diffOriginal = "function total(items) {\n  let sum = 0;\n  for (const it of items) sum += it.price;\n  return sum;\n}\n"
const diffModified = "function total(items) {\n  return items\n    .filter((it) => it.inStock)\n    .reduce((sum, it) => sum + it.price, 0);\n}\n"

func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	docJSON, ok := p.LoadDoc(r.Context(), defaultDocID)
	initLang := "typescript"
	if !ok {
		// No saved doc: seed a showcase sample so the editor is never blank.
		b, _ := json.Marshal(savedDoc{Code: demoSamples["typescript"], Language: "typescript"})
		docJSON = string(b)
	} else if _, l := codeLanguageFromDoc(json.RawMessage(docJSON)); l != "" {
		initLang = l
	}

	var opts strings.Builder
	for _, l := range demoLanguages {
		opts.WriteString(`<option value="` + l + `">` + l + `</option>`)
	}
	samplesJSON, _ := json.Marshal(demoSamples)
	diffJSON, _ := json.Marshal(map[string]string{"original": diffOriginal, "modified": diffModified})

	ext := demoExt[initLang]
	if ext == "" {
		ext = "txt"
	}

	mount := Mount(MountConfig{DocID: defaultDocID, Doc: docJSON, MinHeight: "480px"})
	return render.HTML(strings.NewReplacer(
		"{{SCHEME}}", schemeFromCookie(r),
		"{{TOKENS}}", tokens,
		"{{SAVE_URL}}", SaveURL,
		"{{LANG_OPTIONS}}", opts.String(),
		"{{INIT_LANG}}", initLang,
		"{{INIT_EXT}}", ext,
		"{{SAMPLES}}", string(samplesJSON),
		"{{DIFF}}", string(diffJSON),
		"{{MOUNT}}", string(mount),
		"{{BROKER}}", pluginhost.BrokerScriptURL,
		"{{CONFIG}}", ConfigScriptURL,
		"{{ADAPTER}}", AdapterScriptURL,
		"{{VERSION}}", Version,
		"{{SHELL_CSS}}", demoShellCSS,
	).Replace(demoPage))
}

// demoPage is the self-contained HTML document served at DemoURL, on the
// shared demo-page shell (docs/demo-page-design.md; class-for-class with
// richtext's, datagrid's and mermaid's demos). It runs with NO UIHost: the
// theme tokens are inlined in <style>, the mount marker holds the editor
// iframe, and the host scripts are included directly — the generic platform
// broker (pluginhost.js) first, then this instance's config.js, then the
// adapter. The inline <script> (toggle + reconfigure bridge + submit guard)
// is acceptable ON THIS DEMO PAGE ONLY — the broker/adapter and frame stay
// CSP-clean and same-origin-script only.
//
// Placeholders are {{SCHEME}}, {{TOKENS}}, {{SAVE_URL}}, {{LANG_OPTIONS}},
// {{INIT_LANG}}/{{INIT_EXT}}, {{SAMPLES}}, {{DIFF}}, {{MOUNT}}, {{BROKER}},
// {{CONFIG}}, {{ADAPTER}}, {{VERSION}}, {{SHELL_CSS}}. It uses
// strings.NewReplacer (NOT fmt.Sprintf) because the shell CSS carries literal
// "%" characters (color-mix percentages) that Sprintf would misread.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="{{SCHEME}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Monaco — Sandboxed Demo</title>
<style>
{{TOKENS}}
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / monaco</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/monaco" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="settings" data-tip="View the live plugin config">⚙ Settings</button>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>The VS Code editor.<br>No workers, no origin, no network.</h2>
    <p class="lead">Monaco 0.52.2 boots inside an <strong>opaque-origin iframe</strong> with zero web workers — the cage forbids worker construction, so highlighting runs on the main thread. Every control on the strip below reconfigures the live editor over the postMessage bridge: flip a toggle and watch the frame apply it in place, without reloading.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">workers: none</span>
      <span class="badge">sandbox=&quot;allow-scripts&quot;</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">monaco 0.52.2 · 12 languages</span>
    </p>
  </section>

  <form method="post" action="{{SAVE_URL}}" id="fui-demo-form">
    <section class="editor-card" aria-label="Monaco editor frame">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title" id="editor-title">demo.{{INIT_EXT}}</span>
        <span class="editor-mode">sandboxed iframe</span>
      </div>
      {{MOUNT}}
    </section>
  </form>

  <div class="toolbar" role="toolbar" aria-label="Editor controls">
    <label class="ctl">Language
      <select id="lang" data-tip="Set the syntax highlighting language">{{LANG_OPTIONS}}</select>
    </label>
    <button type="button" class="fui-btn" id="sample" data-tip="Replace the buffer with a sample for this language">Load sample</button>
    <span class="sep"></span>
    <button type="button" class="fui-btn toggle" data-opt="readOnly" data-tip="Make the editor read-only">Read-only</button>
    <button type="button" class="fui-btn toggle" data-opt="minimap" data-tip="Show the minimap">Minimap</button>
    <button type="button" class="fui-btn toggle" data-opt="wordWrap" data-tip="Wrap long lines">Word wrap</button>
    <button type="button" class="fui-btn toggle active" data-opt="lineNumbers" data-tip="Show line numbers">Line numbers</button>
    <button type="button" class="fui-btn toggle" id="diff" data-tip="Compare two versions side by side">Diff view</button>
    <span class="sep"></span>
    <span class="ctl font">Font
      <button type="button" class="fui-btn sq" id="font-dec" data-tip="Smaller">A&minus;</button>
      <span id="font-val">14</span>
      <button type="button" class="fui-btn sq" id="font-inc" data-tip="Larger">A+</button>
    </span>
  </div>

  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint"><kbd>⌘S</kbd> save (in-frame)</span>
      <span class="hint">typing autosaves every ~2s over the bridge</span>
      <span class="hint">switch the language — highlighting follows live</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Tap a toggle — the editor applies it live</span>
      <span class="hint">Autosaves as you type</span>
    </p>
    <p class="save-row"><button type="submit" form="fui-demo-form" class="fui-btn fui-btn-primary">Save</button><span class="save-status" id="save-status" role="status" aria-live="polite"></span></p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>⚙️ Worker-free by design</h3>
      <p>Under <code>sandbox="allow-scripts"</code> without <code>allow-same-origin</code> the frame's origin is opaque and worker construction is restricted — so Monaco boots on monarch tokenizers, on the main thread, verified in both WebKit and Chromium. Workers remain an opt-in (<code>config.workers</code>) that falls back rather than throwing.</p>
    </article>
    <article class="card">
      <h3>🧱 One monolithic bundle</h3>
      <p>A dynamic <code>import()</code> is a CORS-mode module fetch an opaque origin can never satisfy, so there is no code splitting: <code>editor.js</code> is multi-megabyte, served at its own route and deliberately outside the core-ui runtime budget.</p>
    </article>
    <article class="card">
      <h3>🔌 Reconfigured over the bridge</h3>
      <p>Language, font size, read-only, minimap, word wrap, line numbers and a side-by-side diff mode all travel as <code>reconfigure</code> events and are applied in place. The ⚙ Settings dialog shows the exact config object the frame receives.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · monaco {{VERSION}} · <a href="/">all plugins →</a></p>
</footer>

<div class="modal-backdrop" id="modal" aria-hidden="true">
  <div class="modal" role="dialog" aria-modal="true" aria-label="Editor settings">
    <div class="modal-head"><strong>Editor settings</strong><button type="button" class="fui-btn sq" id="modal-close" data-tip="Close">&times;</button></div>
    <div class="settings-grid">
      <label class="srow">Language <select id="m-lang">{{LANG_OPTIONS}}</select></label>
      <label class="srow">Font size <input type="number" id="m-fontSize" min="8" max="32" step="1" value="14"></label>
      <label class="srow switch"><input type="checkbox" id="m-readOnly"> Read-only</label>
      <label class="srow switch"><input type="checkbox" id="m-minimap"> Minimap</label>
      <label class="srow switch"><input type="checkbox" id="m-wordWrap"> Word wrap</label>
      <label class="srow switch"><input type="checkbox" id="m-lineNumbers"> Line numbers</label>
      <label class="srow switch"><input type="checkbox" id="m-diff"> Diff view</label>
    </div>
    <p class="muted" style="margin-top:14px">Live config sent to the sandboxed frame:</p>
    <pre id="cfg-json"></pre>
    <div class="modal-actions"><button type="button" class="fui-btn" id="reset">Reset to defaults</button></div>
  </div>
</div>

<script>
(function () {
  var SAMPLES = {{SAMPLES}};
  var DIFF = {{DIFF}};
  var DEFAULTS = { language: "{{INIT_LANG}}", readOnly: false, minimap: false, wordWrap: false, lineNumbers: true, fontSize: 14, diff: null };
  var cfg = JSON.parse(JSON.stringify(DEFAULTS));

  // Theme toggle: flip data-color-scheme and persist it; the broker observes
  // the flip and re-bridges tokens to the frame.
  var html = document.documentElement;
  var themeBtn = document.getElementById('fui-scheme-toggle');
  themeBtn.addEventListener('click', function () {
    var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
    html.dataset.colorScheme = next;
    document.cookie = 'fui-color-scheme=' + next + '; path=/; max-age=31536000; samesite=lax';
  });

  // --- reconfigure bridge: post host->plugin events straight to the frame -----
  var seq = 0;
  function frameWin() {
    var f = document.querySelector('#fui-demo-form iframe') || document.querySelector('iframe');
    return f && f.contentWindow ? f.contentWindow : null;
  }
  function send(method, params) {
    var w = frameWin();
    if (!w) return;
    w.postMessage({ v: 1, id: 'demo-' + (++seq), type: 'event', src: 'host', method: method, params: params || {} }, '*');
  }

  // --- one source of truth: cfg. syncUI() reflects it into EVERY control (the
  // toolbar buttons AND the modal's form controls), so the settings panel is
  // fully interactive and always agrees with the toolbar. ---------------------
  var BOOL_OPTS = ["readOnly", "minimap", "wordWrap", "lineNumbers"];
  var cfgJson = document.getElementById('cfg-json');
  var langSel = document.getElementById('lang');
  var fontVal = document.getElementById('font-val');
  var diffBtn = document.getElementById('diff');
  var titleEl = document.getElementById('editor-title');
  var EXT = { typescript: 'ts', javascript: 'js', go: 'go', python: 'py', json: 'json', markdown: 'md', html: 'html', css: 'css', sql: 'sql', yaml: 'yaml', shell: 'sh', plaintext: 'txt' };

  function syncUI() {
    BOOL_OPTS.forEach(function (opt) {
      var on = !!cfg[opt];
      var tb = document.querySelector('.fui-btn.toggle[data-opt="' + opt + '"]');
      if (tb) { tb.classList.toggle('active', on); tb.setAttribute('aria-pressed', String(on)); }
      var m = document.getElementById('m-' + opt);
      if (m) m.checked = on;
    });
    var diffOn = !!cfg.diff;
    diffBtn.classList.toggle('active', diffOn); diffBtn.setAttribute('aria-pressed', String(diffOn));
    var md = document.getElementById('m-diff'); if (md) md.checked = diffOn;
    langSel.value = cfg.language;
    var ml = document.getElementById('m-lang'); if (ml) ml.value = cfg.language;
    fontVal.textContent = String(cfg.fontSize);
    var mf = document.getElementById('m-fontSize'); if (mf) mf.value = String(cfg.fontSize);
    if (titleEl) titleEl.textContent = 'demo.' + (EXT[cfg.language] || 'txt');
    cfgJson.textContent = JSON.stringify(cfg, null, 2);
  }

  // Mutations: change state, push the patch to the frame, re-sync every control.
  function setOpt(opt, val) { cfg[opt] = val; var p = {}; p[opt] = val; send('reconfigure', p); syncUI(); }
  function setLang(v) { cfg.language = v; send('reconfigure', { language: v }); syncUI(); }
  function setFont(n) { cfg.fontSize = Math.max(8, Math.min(32, n)); send('reconfigure', { fontSize: cfg.fontSize }); syncUI(); }
  function setDiff(on) {
    cfg.diff = on ? { original: DIFF.original, modified: DIFF.modified, language: cfg.language } : null;
    send('reconfigure', { diff: cfg.diff }); syncUI();
  }
  function loadSample() { send('reconfigure', { code: SAMPLES[cfg.language] || '', language: cfg.language }); }
  function reset() {
    cfg = JSON.parse(JSON.stringify(DEFAULTS));
    send('reconfigure', { language: cfg.language, readOnly: cfg.readOnly, minimap: cfg.minimap, wordWrap: cfg.wordWrap, lineNumbers: cfg.lineNumbers, fontSize: cfg.fontSize, diff: null });
    syncUI();
  }

  // Toolbar controls.
  document.querySelectorAll('.fui-btn.toggle[data-opt]').forEach(function (b) {
    b.addEventListener('click', function () { var o = b.getAttribute('data-opt'); setOpt(o, !cfg[o]); });
  });
  langSel.addEventListener('change', function () { setLang(langSel.value); });
  diffBtn.addEventListener('click', function () { setDiff(!cfg.diff); });
  document.getElementById('sample').addEventListener('click', loadSample);
  document.getElementById('font-dec').addEventListener('click', function () { setFont(cfg.fontSize - 1); });
  document.getElementById('font-inc').addEventListener('click', function () { setFont(cfg.fontSize + 1); });

  // Modal controls — the settings panel is CONFIGURABLE, not just a readout.
  BOOL_OPTS.forEach(function (opt) {
    var m = document.getElementById('m-' + opt);
    if (m) m.addEventListener('change', function () { setOpt(opt, m.checked); });
  });
  var mDiff = document.getElementById('m-diff');
  if (mDiff) mDiff.addEventListener('change', function () { setDiff(mDiff.checked); });
  var mLang = document.getElementById('m-lang');
  if (mLang) mLang.addEventListener('change', function () { setLang(mLang.value); });
  var mFont = document.getElementById('m-fontSize');
  if (mFont) mFont.addEventListener('change', function () { setFont(parseInt(mFont.value, 10) || cfg.fontSize); });

  // Settings modal open/close.
  var modal = document.getElementById('modal');
  function openModal() { syncUI(); modal.classList.add('open'); modal.setAttribute('aria-hidden', 'false'); }
  function closeModal() { modal.classList.remove('open'); modal.setAttribute('aria-hidden', 'true'); }
  document.getElementById('settings').addEventListener('click', openModal);
  document.getElementById('modal-close').addEventListener('click', closeModal);
  modal.addEventListener('click', function (e) { if (e.target === modal) closeModal(); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') closeModal(); });
  document.getElementById('reset').addEventListener('click', reset);

  // Save (POST the broker-mirrored hidden fields).
  var form = document.getElementById('fui-demo-form');
  var status = document.getElementById('save-status');
  function setStatus(t, ok) { status.textContent = t; status.className = 'save-status' + (ok === false ? ' is-err' : ''); }
  // The Save button sits outside the form (the affordance strip is not form
  // markup), so wire it to the same submit path explicitly.
  document.querySelector('.save-row .fui-btn-primary').addEventListener('click', function () {
    var code = (form.querySelector('input[name=code]') || {}).value || '';
    var l = (form.querySelector('input[name=language]') || {}).value || 'plaintext';
    if (!code) { setStatus('Nothing to save yet.', false); return; }
    setStatus('Saving…');
    fetch(form.getAttribute('action'), {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ docId: 'demo', code: code, language: l, schemaVersion: 'monaco-v1' })
    }).then(function (r) { setStatus(r.ok ? 'Saved ✓' : 'Save failed', r.ok); })
      .catch(function () { setStatus('Save failed', false); });
  });

  syncUI();
})();
</script>
<script src="{{BROKER}}"></script>
<script src="{{CONFIG}}"></script>
<script src="{{ADAPTER}}"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with richtext's demo
// shell so the pages are visibly the same product, plus the toolbar and
// settings-dialog styles this showcase needs. Token-driven throughout — the
// theme toggle restyles everything, and the bridged tokens re-theme the
// editor inside the frame.
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
/* Toolbar: the affordance strip directly under the frame. */
.toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--spacing-md, 8px); margin: var(--spacing-lg, 16px) 2px 0; padding: var(--spacing-md, 8px) var(--spacing-lg, 16px); border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); background: var(--color-surface, #fff); box-shadow: var(--shadow-sm, 0 1px 2px 0 rgba(0,0,0,.05)); }
.ctl { display: inline-flex; align-items: center; gap: 6px; font-size: var(--text-xs, .75rem); color: var(--color-text-muted, #52525B); }
.ctl select { font: inherit; font-size: var(--text-sm, .875rem); padding: 6px 10px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); }
.sep { width: 1px; height: 22px; background: var(--color-border, #E4E4E7); }
.ctl.font #font-val { min-width: 20px; text-align: center; font-variant-numeric: tabular-nums; color: var(--color-text, #18181B); }
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
.fui-btn.sq { padding: 6px 9px; }
/* Toggled controls: the amber accent, tinted not filled, so the strip stays calm. */
.fui-btn.toggle.active, .fui-btn.toggle[aria-pressed="true"] { background: color-mix(in srgb, var(--color-primary, #e0a040) 14%, var(--color-surface, #fff)); border-color: var(--color-primary, #e0a040); color: var(--color-text, #18181B); font-weight: 600; }
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
/* Tooltips */
[data-tip] { position: relative; }
[data-tip]:hover::after, [data-tip]:focus-visible::after {
  content: attr(data-tip); position: absolute; left: 50%; top: calc(100% + 8px); transform: translateX(-50%);
  white-space: nowrap; background: var(--color-text, #18181B); color: var(--color-background, #F9FAFB); font-size: 11px;
  padding: 5px 8px; border-radius: var(--radii-md, 6px); pointer-events: none; z-index: 50; box-shadow: 0 4px 14px rgba(0,0,0,.25); }
/* Settings modal */
.modal-backdrop { display: none; position: fixed; inset: 0; background: rgba(0,0,0,.45); z-index: 60; align-items: center; justify-content: center; padding: 20px; }
.modal-backdrop.open { display: flex; }
.modal { width: min(520px, 100%); background: var(--color-surface, #fff); border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 14px); padding: 18px; box-shadow: 0 20px 60px rgba(0,0,0,.35); }
.modal-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.modal-head strong { font-size: 15px; }
.modal .muted { color: var(--color-text-muted, #52525B); font-size: 13px; margin: 0 0 10px; }
.settings-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 18px; margin: 6px 0 4px; }
.srow { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 14px; }
.srow.switch { justify-content: flex-start; cursor: pointer; }
.srow.switch input { width: 16px; height: 16px; accent-color: var(--color-primary, #e0a040); cursor: pointer; }
.srow select, .srow input[type=number] { font: inherit; font-size: 13px; padding: 5px 8px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); }
.srow input[type=number] { width: 72px; }
#cfg-json { margin: 0; padding: 12px; border-radius: var(--radii-md, 10px); background: var(--color-surface-soft, #F4F4F5); border: 1px solid var(--color-border, #E4E4E7); font-family: var(--font-mono, monospace); font-size: 12px; line-height: 1.5; overflow: auto; max-height: 300px; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
  .settings-grid { grid-template-columns: 1fr; }
  /* Keep the editor usable on a phone: shorter frame, the page still resolves
     with the strip and cards stacked below. !important is required — the
     broker pins the iframe height with an inline style. */
  .editor-card iframe { height: 420px !important; }
}
`
