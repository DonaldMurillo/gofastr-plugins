package monaco

import (
	"encoding/json"
	"net/http"
	"strings"

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

// demoLanguages is the language picker's option set (mirrors the basic-languages
// the frame bundles). Order is display order.
var demoLanguages = []string{
	"typescript", "javascript", "go", "python", "json",
	"markdown", "html", "css", "sql", "yaml", "shell", "plaintext",
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
		sel := ""
		if l == initLang {
			sel = " selected"
		}
		opts.WriteString("<option value=\"" + l + "\"" + sel + ">" + l + "</option>")
	}
	samplesJSON, _ := json.Marshal(demoSamples)
	diffJSON, _ := json.Marshal(map[string]string{"original": diffOriginal, "modified": diffModified})

	mount := Mount(MountConfig{DocID: defaultDocID, Doc: docJSON})
	return render.HTML(strings.NewReplacer(
		"{{TOKENS}}", tokens,
		"{{SAVE_URL}}", SaveURL,
		"{{LANG_OPTIONS}}", opts.String(),
		"{{INIT_LANG}}", initLang,
		"{{SAMPLES}}", string(samplesJSON),
		"{{DIFF}}", string(diffJSON),
		"{{MOUNT}}", string(mount),
		"{{BROKER}}", pluginhost.BrokerScriptURL,
		"{{CONFIG}}", ConfigScriptURL,
		"{{ADAPTER}}", AdapterScriptURL,
	).Replace(demoPage))
}

const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Monaco — Showcase</title>
<style>
{{TOKENS}}
*{box-sizing:border-box}
body{margin:0;font-family:var(--font-body,system-ui,sans-serif);background:var(--color-background);color:var(--color-text)}
header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:12px 20px;border-bottom:1px solid var(--color-border)}
header h1{font-size:16px;margin:0}
.hgroup{display:flex;gap:8px}
.btn{font:inherit;font-size:13px;padding:6px 11px;border:1px solid var(--color-border);border-radius:8px;background:var(--color-surface);color:var(--color-text);cursor:pointer;line-height:1}
.btn:hover{border-color:var(--color-primary)}
.btn.toggle.active{background:var(--color-primary);color:var(--color-primary-fg,#fff);border-color:var(--color-primary)}
.btn.sq{padding:6px 9px}
.toolbar{display:flex;flex-wrap:wrap;align-items:center;gap:8px;padding:10px 20px;border-bottom:1px solid var(--color-border);background:var(--color-surface-soft,transparent);position:sticky;top:0;z-index:20}
.ctl{display:inline-flex;align-items:center;gap:6px;font-size:12px;color:var(--color-text-muted)}
.ctl select{font:inherit;font-size:13px;padding:5px 8px;border:1px solid var(--color-border);border-radius:8px;background:var(--color-surface);color:var(--color-text)}
.sep{width:1px;height:22px;background:var(--color-border)}
.ctl.font #font-val{min-width:20px;text-align:center;font-variant-numeric:tabular-nums;color:var(--color-text)}
main{max-width:1000px;margin:0 auto;padding:20px}
.hint{color:var(--color-text-muted);font-size:13px;margin:0 0 14px}
form{margin:0}
.saverow{display:flex;align-items:center;gap:10px;margin-top:14px}
#save-status{font-size:13px;color:var(--color-text-muted)}
/* Tooltips */
[data-tip]{position:relative}
[data-tip]:hover::after,[data-tip]:focus-visible::after{
  content:attr(data-tip);position:absolute;left:50%;top:calc(100% + 8px);transform:translateX(-50%);
  white-space:nowrap;background:var(--color-text);color:var(--color-background);font-size:11px;
  padding:5px 8px;border-radius:6px;pointer-events:none;z-index:50;box-shadow:0 4px 14px rgba(0,0,0,.25)}
/* Modal */
.modal-backdrop{display:none;position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:60;align-items:center;justify-content:center;padding:20px}
.modal-backdrop.open{display:flex}
.modal{width:min(520px,100%);background:var(--color-surface);border:1px solid var(--color-border);border-radius:14px;padding:18px;box-shadow:0 20px 60px rgba(0,0,0,.35)}
.modal-head{display:flex;align-items:center;justify-content:space-between;margin-bottom:8px}
.modal-head strong{font-size:15px}
.modal .muted{color:var(--color-text-muted);font-size:13px;margin:0 0 10px}
.settings-grid{display:grid;grid-template-columns:1fr 1fr;gap:10px 18px;margin:6px 0 4px}
.srow{display:flex;align-items:center;justify-content:space-between;gap:10px;font-size:14px}
.srow.switch{justify-content:flex-start;cursor:pointer}
.srow.switch input{width:16px;height:16px;accent-color:var(--color-primary);cursor:pointer}
.srow select,.srow input[type=number]{font:inherit;font-size:13px;padding:5px 8px;border:1px solid var(--color-border);border-radius:8px;background:var(--color-surface);color:var(--color-text)}
.srow input[type=number]{width:72px}
#cfg-json{margin:0;padding:12px;border-radius:10px;background:var(--color-surface-soft,#f6f8fa);border:1px solid var(--color-border);font-family:var(--font-mono,monospace);font-size:12px;line-height:1.5;overflow:auto;max-height:300px}
.modal-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:14px}
</style>
</head>
<body>
<header>
  <h1>Monaco — Showcase</h1>
  <div class="hgroup">
    <button type="button" class="btn" id="settings" data-tip="View the live plugin config">⚙ Settings</button>
    <button type="button" class="btn" id="fui-scheme-toggle" data-tip="Toggle light / dark">Toggle theme</button>
  </div>
</header>

<div class="toolbar" role="toolbar" aria-label="Editor controls">
  <label class="ctl">Language
    <select id="lang" data-tip="Set the syntax highlighting language">{{LANG_OPTIONS}}</select>
  </label>
  <button type="button" class="btn" id="sample" data-tip="Replace the buffer with a sample for this language">Load sample</button>
  <span class="sep"></span>
  <button type="button" class="btn toggle" data-opt="readOnly" data-tip="Make the editor read-only">Read-only</button>
  <button type="button" class="btn toggle" data-opt="minimap" data-tip="Show the minimap">Minimap</button>
  <button type="button" class="btn toggle" data-opt="wordWrap" data-tip="Wrap long lines">Word wrap</button>
  <button type="button" class="btn toggle active" data-opt="lineNumbers" data-tip="Show line numbers">Line numbers</button>
  <button type="button" class="btn toggle" id="diff" data-tip="Compare two versions side by side">Diff view</button>
  <span class="sep"></span>
  <span class="ctl font">Font
    <button type="button" class="btn sq" id="font-dec" data-tip="Smaller">A&minus;</button>
    <span id="font-val">14</span>
    <button type="button" class="btn sq" id="font-inc" data-tip="Larger">A+</button>
  </span>
</div>

<main>
  <p class="hint">A sandboxed Monaco editor, reconfigured live over the postMessage bridge. Toggle options above, switch languages, load samples, or flip on a diff view — the frame applies each change in place.</p>
  <form method="post" action="{{SAVE_URL}}" id="fui-demo-form">
    {{MOUNT}}
    <div class="saverow"><button type="submit" class="btn">Save</button> <span id="save-status" role="status" aria-live="polite"></span></div>
  </form>
</main>

<div class="modal-backdrop" id="modal" aria-hidden="true">
  <div class="modal" role="dialog" aria-modal="true" aria-label="Editor settings">
    <div class="modal-head"><strong>Editor settings</strong><button type="button" class="btn sq" id="modal-close" data-tip="Close">&times;</button></div>
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
    <div class="modal-actions"><button type="button" class="btn" id="reset">Reset to defaults</button></div>
  </div>
</div>

<script>
(function () {
  var SAMPLES = {{SAMPLES}};
  var DIFF = {{DIFF}};
  var DEFAULTS = { language: "{{INIT_LANG}}", readOnly: false, minimap: false, wordWrap: false, lineNumbers: true, fontSize: 14, diff: null };
  var cfg = JSON.parse(JSON.stringify(DEFAULTS));

  // Theme toggle: flip data-color-scheme; the broker observes it and re-bridges
  // tokens to the frame (unchanged from the minimal demo).
  var html = document.documentElement;
  var themeBtn = document.getElementById('fui-scheme-toggle');
  themeBtn.addEventListener('click', function () {
    var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
    html.dataset.colorScheme = next;
    themeBtn.textContent = next === 'dark' ? 'Light theme' : 'Toggle theme';
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

  function syncUI() {
    BOOL_OPTS.forEach(function (opt) {
      var on = !!cfg[opt];
      var tb = document.querySelector('.btn.toggle[data-opt="' + opt + '"]');
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
  document.querySelectorAll('.btn.toggle[data-opt]').forEach(function (b) {
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
  form.addEventListener('submit', function (e) {
    e.preventDefault();
    var code = (form.querySelector('input[name=code]') || {}).value || '';
    var l = (form.querySelector('input[name=language]') || {}).value || 'plaintext';
    if (!code) { status.textContent = 'Nothing to save yet.'; return; }
    status.textContent = 'Saving…';
    fetch(form.getAttribute('action'), {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ docId: 'demo', code: code, language: l, schemaVersion: 'monaco-v1' })
    }).then(function (r) { status.textContent = r.ok ? 'Saved ✓' : 'Save failed'; })
      .catch(function () { status.textContent = 'Save failed'; });
  });

  syncUI();
})();
</script>
<script src="{{BROKER}}"></script>
<script src="{{CONFIG}}"></script>
<script src="{{ADAPTER}}"></script>
</body>
</html>`
