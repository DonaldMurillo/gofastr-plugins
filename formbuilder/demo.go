package formbuilder

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/registry"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"
)

// demoBuilderHeight is the iframe height the design page mounts at: tall
// enough that the builder's palette + list + property panel are all visible,
// so the page resolves without dead space.
const demoBuilderHeight = "620px"

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
// Defaults to "dark" — the gofastr showcase pages are dark, amber-accented,
// and this demo matches them; the toggle can force light. Same cookie and
// default as richtext/datagrid's demos, so the pages open in the same scheme.
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme mirrors the palette of the framework's own showcase site
// (gofastr/examples/site — the "v2" design), identical to richtext/datagrid's
// demo theme so every demo page reads as one product: a warm near-black
// surface ladder with a single amber accent, expressed in oklch. The amber
// accent carries into both schemes; the full warm-dark ladder is the
// DarkColors override, which the page shows by default. Token-driven, so the
// whole shell, the bridged builder AND the live form's component CSS pick
// these up with no per-element styling.
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	t.Colors.Primary = style.Color{Name: "primary", Value: "oklch(0.82 0.155 78)"}
	t.Colors.PrimaryFg = style.Color{Name: "primary-fg", Value: "oklch(0.14 0.005 75)"}
	t.Colors.Accent = style.Color{Name: "accent", Value: "oklch(0.82 0.155 78)"}
	t.Fonts.Body = style.Font{Name: "body", Value: "-apple-system, BlinkMacSystemFont, Inter, 'Segoe UI', system-ui, sans-serif"}
	t.Fonts.Heading = style.Font{Name: "heading", Value: "-apple-system, BlinkMacSystemFont, Inter, 'Segoe UI', system-ui, sans-serif"}
	t.Fonts.Mono = style.Font{Name: "mono", Value: "ui-monospace, SFMono-Regular, 'JetBrains Mono', Menlo, Consolas, monospace"}
	t.Radii.SM = style.Radius{Name: "sm", Value: 4}
	t.Radii.MD = style.Radius{Name: "md", Value: 6}
	t.Radii.LG = style.Radius{Name: "lg", Value: 10}
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
		"selection":     "oklch(0.82 0.155 78 / 0.30)",
		"success":       "oklch(0.76 0.14 158)",
		"warning":       "oklch(0.82 0.12 80)",
		"danger":        "oklch(0.72 0.15 25)",
	}
	return t
}

// defaultDemoDoc is the schema the design page starts from before anything
// has been saved: a small investor-contact canvas that already exercises five
// of the seven field types and a few rules, so the page demonstrates the
// builder instead of opening on an empty list.
var defaultDemoDoc = Doc{
	Version: SchemaVersion,
	Fields: []Field{
		{Type: "text", Name: "full_name", Label: "Full name", Required: true,
			Rules: Rules{MinLength: new(2.0), MaxLength: new(80.0)}},
		{Type: "email", Name: "email", Label: "Email", Required: true,
			Help: "We reply within two working days."},
		{Type: "select", Name: "role", Label: "I am a…", Required: true,
			Options: []string{"Founder", "Operator", "Engineer", "Investor", "Other"}},
		{Type: "textarea", Name: "pitch", Label: "What are you building?", Required: true,
			Help:  "One paragraph is plenty.",
			Rules: Rules{MinLength: new(20.0), MaxLength: new(500.0)}},
		{Type: "date", Name: "launch", Label: "Target launch",
			Help: "Optional — skip it if you do not know yet."},
	},
}

// --- the design page ---------------------------------------------------------

// designPage is the self-contained HTML document served at DemoURL. It runs
// with NO UIHost: the theme tokens are inlined in <style>, the mount marker
// holds the builder iframe, and both host scripts are included directly — the
// generic platform broker (pluginhost.js) first, then this plugin's adapter.
// The inline <script> (toggle + live save-verdict readout) is acceptable ON
// THIS DEMO PAGE ONLY — the broker/adapter and the frame stay CSP-clean and
// same-origin-script only, the same allowance the richtext/datagrid demos
// make.
//
// The %s slots are: color scheme, theme tokens CSS, mount marker HTML,
// plugin version, platform broker script URL, adapter script URL. The shell
// CSS and the readout script are spliced in AFTER formatting
// ({{SHELL_CSS}} / {{READOUT_JS}}) because both contain literal "%" that
// Sprintf must never see as format verbs.
const designPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Form Builder — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / formbuilder</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/formbuilder" aria-current="page">Sandboxed</a>
    <a class="navlink navlink-proof" href="/formbuilder/live">Live form →</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>A form designed in a cage.<br>Enforced by the server.</h2>
    <p class="lead">The builder below runs in an opaque-origin iframe — no cookies, no storage, and <code>connect-src 'none'</code>, so it cannot save anything itself. The schema it edits is <em>data only</em>; Go validates every save and refuses what it does not understand. Then open the live form and try to get garbage past the server.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">schema formbuilder-v1</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">7 field types</span>
      <span class="badge">0 client trust</span>
    </p>
  </section>

  <section class="editor-card" aria-label="Form builder frame">
    <div class="editor-chrome" aria-hidden="true">
      <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
      <span class="editor-title">investor-contact.schema.json</span>
      <span class="editor-mode">sandboxed iframe</span>
    </div>
    %s
  </section>

  <section class="proof" aria-label="Live save verdict">
    <p class="proof-title"><span class="proof-dot" aria-hidden="true"></span>Schema — live from the server's last verdict</p>
    <div class="proof-grid">
      <div class="proof-stat proof-stat-lead">
        <p class="proof-number"><span id="fb-live-fields">{{FB_FIELDS}}</span></p>
        <p class="proof-label">fields the server accepted</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number"><span id="fb-live-rules">{{FB_RULES}}</span></p>
        <p class="proof-label">rules Go will enforce on submit</p>
      </div>
      <div class="proof-stat">
        <p class="proof-number proof-verdict" id="fb-live-verdict">—</p>
        <p class="proof-label">last save over the bridge</p>
      </div>
    </div>
  </section>
  <p class="proof-note">Every autosave POSTs to <code>/__gofastr/plugin/formbuilder/save</code>, where the schema is validated field by field. A bad schema never lands: the refusal code comes back and renders above.</p>

  <div class="under-editor">
    <p class="hints hints-fine">
      <span class="hint"><kbd>+ Text</kbd> add a field</span>
      <span class="hint"><kbd>⠿ drag</kbd> reorder</span>
      <span class="hint">edits autosave — watch the verdict move</span>
    </p>
    <p class="hints hints-touch">
      <span class="hint">Tap a type to add a field</span>
      <span class="hint">Use ↑ ↓ to reorder</span>
      <span class="hint">Edits autosave</span>
    </p>
    <p class="save-row">
      <span class="save-status">the server is the authority — the frame's hints are decoration</span>
      <a class="fui-btn fui-btn-primary" href="/formbuilder/live">Open the live form →</a>
    </p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🗒 Data only, by refusal</h3>
      <p>The saved doc is <code>{version, fields[]}</code> — descriptors, never markup. A label carrying <code>&lt;</code> is refused at save with <code>400 E_MARKUP</code>. The moment a schema could contain HTML, the proof would be gone.</p>
    </article>
    <article class="card">
      <h3>🛡 Rules run twice</h3>
      <p>The frame previews <code>required</code>, lengths, ranges and patterns for the person designing. On submit, Go re-derives every rule from the schema and re-checks the value — bypass the frame entirely (a plain <code>curl</code>) and the answer is the same.</p>
    </article>
    <article class="card">
      <h3>🔖 Versioned from day one</h3>
      <p>Saved schemas outlive the plugin that wrote them, so the doc carries <code>version</code>. Anything that is not <code>formbuilder-v1</code> is refused with <code>400 E_UNKNOWN_VERSION</code> — a future schema fails loudly, never silently.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · formbuilder %s · <a href="/formbuilder/live">the live form this schema produces →</a></p>
</footer>
<script>
{{READOUT_JS}}
</script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// designReadoutJS is the design page's inline script: the theme toggle
// (persisted to the same cookie richtext/datagrid use) and the live
// save-verdict readout. The readout polls the adapter's mirror on the IFRAME
// ELEMENT — the opaque frame is unreadable from the host page, so that mirror
// (the server's own {ok, code, fields, rules} verdict) is the only live
// channel. Spliced in post-Sprintf because it contains literal "%".
const designReadoutJS = `(function () {
  var html = document.documentElement;
  var btn = document.getElementById('fui-scheme-toggle');
  if (btn) {
    btn.addEventListener('click', function () {
      var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
      html.dataset.colorScheme = next;
      document.cookie = 'fui-color-scheme=' + next + '; path=/; max-age=31536000; samesite=lax';
    });
  }

  var elF = document.getElementById('fb-live-fields');
  var elR = document.getElementById('fb-live-rules');
  var elV = document.getElementById('fb-live-verdict');
  function tick() {
    var f = document.querySelector('iframe');
    if (!f) return;
    var s = f.__formbuilderSave;
    if (!s) return;
    if (elF) elF.textContent = s.ok ? String(s.fields) : '—';
    if (elR) elR.textContent = s.ok ? String(s.rules) : '—';
    if (elV) {
      elV.textContent = s.ok ? 'Saved ✓' : ('Refused: ' + s.code);
      elV.className = 'proof-number proof-verdict ' + (s.ok ? 'is-ok' : 'is-bad');
    }
  }
  tick();
  setInterval(tick, 250);
})();`

// renderDemo builds the self-contained design page. The mount's initial doc
// is the last-saved schema when one exists (LoadDoc), else the default demo
// canvas.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()
	docJSON, ok := p.LoadDoc(r.Context(), defaultDocID)
	if !ok && p.demoDoc != nil {
		if b, err := json.Marshal(cloneDoc(*p.demoDoc)); err == nil {
			docJSON = string(b)
		}
	}
	mount := Mount(MountConfig{DocID: defaultDocID, Doc: docJSON, MinHeight: demoBuilderHeight})
	// Script order is load-bearing: the platform broker first (defines
	// __gofastrPluginHost), then the adapter (registers with it).
	page := fmt.Sprintf(designPage,
		schemeFromCookie(r),
		tokens,
		string(mount),
		Version,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
	)
	page = strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1)
	page = strings.Replace(page, "{{FB_FIELDS}}", initialSchemaCounts(docJSON), 1)
	page = strings.Replace(page, "{{FB_RULES}}", initialSchemaRules(docJSON), 1)
	page = strings.Replace(page, "{{READOUT_JS}}", designReadoutJS, 1)
	return render.HTML(page)
}

// initialSchemaCounts / initialSchemaRules seed the proof strip with the
// SERVER's own view of the mounted schema, so the numbers are populated on
// first paint — before any autosave has crossed the bridge. They switch to
// the live mirror after the first save verdict.
func initialSchemaCounts(docJSON string) string {
	var d Doc
	if err := json.Unmarshal([]byte(docJSON), &d); err != nil {
		return "—"
	}
	return strconv.Itoa(len(d.Fields))
}

func initialSchemaRules(docJSON string) string {
	var d Doc
	if err := json.Unmarshal([]byte(docJSON), &d); err != nil {
		return "—"
	}
	return strconv.Itoa(d.RuleCount())
}

// --- the live-form proof route ------------------------------------------------

// liveVerdict is what the banner states: the server's answer to the last
// submit, with the HTTP status in plain sight — that number IS the proof.
type liveVerdict struct {
	Kind   string // "fresh" | "rejected" | "accepted"
	Status int    // 0 for fresh
}

// handleLive implements GET/POST LiveURL: the closing half of the loop. GET
// renders the SAVED schema as a real GoFastr form through ui.Form — no
// builder, no frame, just the framework's own components. POST validates the
// submission in Go (ValidateValues) and answers: a violation is re-rendered
// with field errors under a 422 banner; a clean submit shows the accepted
// values under a 200 banner. There is no client-side enforcement anywhere on
// this page — that is the point.
func (p *Plugin) handleLive(w http.ResponseWriter, r *http.Request) {
	doc, err := p.loadLiveDoc()
	if err != nil {
		http.Error(w, "formbuilder: live schema unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	values := url.Values{}
	errs := ui.FieldErrors{}
	verdict := liveVerdict{Kind: "fresh"}
	accepted := false

	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if perr := r.ParseForm(); perr != nil {
			http.Error(w, "formbuilder: bad form body: "+perr.Error(), http.StatusBadRequest)
			return
		}
		values = r.PostForm
		errs = ValidateValues(doc, values)
		if len(errs) > 0 {
			verdict = liveVerdict{Kind: "rejected", Status: http.StatusUnprocessableEntity}
		} else {
			verdict = liveVerdict{Kind: "accepted", Status: http.StatusOK}
			accepted = true
		}
	}

	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'self'; base-uri 'self'")
	if verdict.Status != 0 {
		w.WriteHeader(verdict.Status)
	}
	render.RespondHTML(w, p.renderLive(r, doc, values, errs, verdict, accepted))
}

// loadLiveDoc returns the schema the live form renders: the last-saved doc,
// else a CLONE of the demo canvas (cloned so normalisation can never mutate
// the plugin's default). A saved doc is re-validated defensively; it passed
// /save, so a failure here means the store was tampered with.
func (p *Plugin) loadLiveDoc() (Doc, error) {
	docJSON, ok := p.LoadDoc(nil, defaultDocID)
	if !ok {
		if p.demoDoc == nil {
			return Doc{Version: SchemaVersion}, nil
		}
		return cloneDoc(*p.demoDoc), nil
	}
	var doc Doc
	if err := json.Unmarshal([]byte(docJSON), &doc); err != nil {
		return Doc{}, err
	}
	if err := ValidateDoc(&doc); err != nil {
		return Doc{}, err
	}
	return doc, nil
}

func cloneDoc(d Doc) Doc {
	b, err := json.Marshal(d)
	if err != nil {
		return Doc{Version: SchemaVersion}
	}
	var out Doc
	if err := json.Unmarshal(b, &out); err != nil {
		return Doc{Version: SchemaVersion}
	}
	return out
}

// livePage: %s slots are color scheme, theme tokens CSS, verdict banner HTML,
// main body HTML (the form card or the accepted-values card), plugin version.
// {{FORM_CSS}} / {{SHELL_CSS}} / {{READOUT_JS}} splice post-Sprintf.
const livePage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Form Builder — Live form</title>
<style>
%s
{{SHELL_CSS}}
{{FORM_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / formbuilder / live</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink" href="/formbuilder">← Design</a>
    <a class="navlink is-active" href="/formbuilder/live" aria-current="page">Live form</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero hero-compact">
    <h2>Same schema.<br>Zero client trust.</h2>
    <p class="lead">This form is rendered by GoFastr's own <code>ui.Form</code> from the schema saved in the builder, and every rule is re-checked in Go when you submit. The inputs carry no <code>required</code>, no <code>pattern</code>, nothing — submit garbage and watch who answers.</p>
  </section>
  %s
</main>
<footer>
  <p>gofastr-plugins · formbuilder %s · <a href="/formbuilder">← back to the builder</a></p>
</footer>
<script>
{{READOUT_JS}}
</script>
</body>
</html>`

// liveToggleJS is the live page's only script: the shared theme toggle. The
// form itself needs nothing — it is server-rendered and server-validated.
const liveToggleJS = `(function () {
  var btn = document.getElementById('fui-scheme-toggle');
  if (!btn) return;
  btn.addEventListener('click', function () {
    var html = document.documentElement;
    var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
    html.dataset.colorScheme = next;
    document.cookie = 'fui-color-scheme=' + next + '; path=/; max-age=31536000; samesite=lax';
  });
})();`

// renderLive assembles the live page around either the re-rendered form
// (fresh/rejected) or the accepted-values view.
func (p *Plugin) renderLive(r *http.Request, doc Doc, values url.Values, errs ui.FieldErrors, verdict liveVerdict, accepted bool) render.HTML {
	var body strings.Builder
	body.WriteString(liveBanner(doc, verdict))

	var formHTML string
	if accepted {
		body.WriteString(liveAcceptedCard(doc, values))
	} else {
		formHTML = string(RenderForm(LiveURL, doc, values, errs))
		body.WriteString(`<section class="editor-card" aria-label="Live form (rendered by ui.Form, validated in Go)">
  <div class="editor-chrome" aria-hidden="true">
    <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
    <span class="editor-title">ui.Form — server-rendered</span>
    <span class="editor-mode">trusted host page</span>
  </div>
  <div class="live-form">` + formHTML + `</div>
</section>`)
	}

	page := fmt.Sprintf(livePage,
		schemeFromCookie(r),
		demoTheme().CSSCustomProperties(),
		body.String(),
		Version,
	)
	page = strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1)
	page = strings.Replace(page, "{{FORM_CSS}}", liveFormCSS(formHTML), 1)
	page = strings.Replace(page, "{{READOUT_JS}}", liveToggleJS, 1)
	return render.HTML(page)
}

// liveBanner is the verdict strip: the schema's shape plus, after a submit,
// the server's answer with its HTTP status in plain sight.
func liveBanner(doc Doc, verdict liveVerdict) string {
	shape := fmt.Sprintf("%d fields · %d rules — none enforced in this browser",
		len(doc.Fields), doc.RuleCount())
	switch verdict.Kind {
	case "rejected":
		return `<section class="verdict verdict-rejected" id="fb-verdict" data-verdict="rejected" role="alert">
  <p class="verdict-title">Server rejected — HTTP 422</p>
  <p class="verdict-text">` + shape + `. The frame was not consulted; Go re-derived every rule from the schema and checked the values itself.</p>
</section>`
	case "accepted":
		return `<section class="verdict verdict-accepted" id="fb-verdict" data-verdict="accepted">
  <p class="verdict-title">Accepted by the server — HTTP 200</p>
  <p class="verdict-text">` + shape + `. Every value below passed Go's own validation.</p>
</section>`
	default:
		return `<section class="verdict verdict-fresh" id="fb-verdict" data-verdict="fresh">
  <p class="verdict-title">` + shape + `</p>
  <p class="verdict-text">Submit anything — an empty required field, a broken pattern, a number out of range — and the refusal below the form is the server's, not the browser's.</p>
</section>`
	}
}

// liveAcceptedCard renders the accepted values as a definition list — the
// receipt the server hands back. All doc data crosses render.Escape.
func liveAcceptedCard(doc Doc, values url.Values) string {
	var b strings.Builder
	b.WriteString(`<section class="editor-card" aria-label="Accepted submission">
  <div class="editor-chrome" aria-hidden="true">
    <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
    <span class="editor-title">accepted by Go</span>
    <span class="editor-mode">trusted host page</span>
  </div>
  <dl class="live-accepted">`)
	for i := range doc.Fields {
		f := &doc.Fields[i]
		v := values.Get(f.Name)
		if f.Type == "checkbox" {
			if values.Has(f.Name) {
				v = "yes"
			} else {
				v = "no"
			}
		}
		if v == "" {
			v = "—"
		}
		b.WriteString("<dt>" + string(render.Escape(f.Label)) + "</dt><dd>" + string(render.Escape(v)) + "</dd>")
	}
	b.WriteString(`</dl>
  <p class="live-again"><a class="fui-btn" href="/formbuilder/live">Submit again</a></p>
</section>`)
	return b.String()
}

// liveFormCSS returns the component CSS for every ui.* component the rendered
// form actually used. These demo pages run with NO UIHost, so the registry's
// runtime auto-load (which needs the gofastr client runtime) never fires —
// the same reason richtext/ssr ships ReadCSS. Instead of a hand-maintained
// list, the form's own data-fui-comp markers are scanned and each registered
// stylesheet is inlined under the demo theme — the set can never drift from
// what the form renders.
func liveFormCSS(formHTML string) string {
	if formHTML == "" {
		return ""
	}
	var b strings.Builder
	for _, name := range registry.Scan(formHTML) {
		entry, ok := registry.Lookup(name)
		if !ok {
			continue
		}
		b.WriteString(entry.CSSFor(demoTheme()))
	}
	return b.String()
}

// demoShellCSS is the page chrome, kept class-for-class with the
// richtext/datagrid demo shells so every demo page reads as the same
// product, plus this plugin's verdict strip and live-form extras.
// Token-driven throughout — the theme toggle restyles everything, and the
// bridged tokens re-theme the builder inside the frame.
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
.navlink-proof { color: var(--color-primary, #e0a040); font-weight: 600; }
.navlink-proof:hover { color: color-mix(in oklab, var(--color-primary, #e0a040) 70%, var(--color-text, #18181B) 30%); background: color-mix(in srgb, var(--color-primary, #e0a040) 10%, transparent); }
main { width: 100%; max-width: var(--demo-measure); margin: 0 auto; padding: clamp(32px, 5vw, 56px) var(--demo-gutter) var(--spacing-md, 8px); }
.hero { margin: 0 0 clamp(28px, 4vw, 44px); }
.hero-compact { margin: 0 0 clamp(20px, 3vw, 32px); }
.hero h2 { font-size: clamp(2rem, 4vw, 2.75rem); line-height: 1.1; margin: 0 0 var(--spacing-lg, 16px); letter-spacing: -0.02em; font-weight: 700; max-width: 24ch; }
.hero-compact h2 { font-size: clamp(1.6rem, 3vw, 2.2rem); }
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
/* Proof strip: the server's last verdict, live. The numbers are the point
   of the plugin, so they get display type — the amber tint keys the panel
   to the accent without shouting. */
.proof { margin: var(--spacing-lg, 16px) 0 0; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); background: color-mix(in srgb, var(--color-primary, #e0a040) 4%, var(--color-surface, #fff)); padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); }
.proof-title { margin: 0 0 var(--spacing-md, 8px); font-size: var(--text-xs, .75rem); font-weight: 600; letter-spacing: .08em; text-transform: uppercase; color: var(--color-text-subtle, #71717A); display: flex; align-items: center; gap: var(--spacing-md, 8px); }
.proof-dot { width: 8px; height: 8px; flex: none; border-radius: 50%; background: var(--color-success, #166534); animation: proof-pulse 2.4s ease-in-out infinite; }
@keyframes proof-pulse { 0%, 100% { opacity: 1; } 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .proof-dot { animation: none; } }
.proof-grid { display: grid; grid-template-columns: 1.2fr 1fr 1.2fr; gap: var(--spacing-md, 8px) var(--spacing-xl, 24px); }
.proof-number { margin: 0; font-size: clamp(1.25rem, 2vw, 1.6rem); font-weight: 700; letter-spacing: -0.02em; line-height: 1.1; font-variant-numeric: tabular-nums; color: var(--color-text, #18181B); }
.proof-stat-lead .proof-number { font-size: clamp(1.75rem, 3.5vw, 2.5rem); }
.proof-label { margin: 4px 0 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); line-height: 1.5; }
.proof-verdict.is-ok { color: var(--color-success, #166534); }
.proof-verdict.is-bad { color: var(--color-danger, #b91c1c); }
.proof-note { margin: var(--spacing-md, 8px) 2px 0; font-size: var(--text-xs, .75rem); color: var(--color-text-subtle, #71717A); }
.under-editor { display: flex; align-items: center; justify-content: space-between; gap: var(--spacing-lg, 16px); flex-wrap: wrap; margin: var(--spacing-lg, 16px) 2px clamp(28px, 4vw, 40px); }
.under-editor p { margin: 0; }
.hints.hints-touch { display: none; }
@media (pointer: coarse) { .hints.hints-fine { display: none; } .hints.hints-touch { display: flex; } }
.hints { display: flex; flex-wrap: wrap; gap: var(--spacing-lg, 16px); color: var(--color-text-muted, #52525B); font-size: var(--text-xs, .75rem); }
.hint { display: inline-flex; align-items: center; gap: 5px; }
kbd { font-family: var(--font-mono, monospace); font-size: var(--text-xs, .75rem); border: 1px solid var(--color-border, #E4E4E7); border-bottom-width: 2px; border-radius: var(--radii-sm, 4px); padding: 1px 6px; background: var(--color-surface, #fff); color: var(--color-text, #18181B); }
.fui-btn { font: inherit; font-size: var(--text-sm, .875rem); font-weight: 500; padding: 8px 16px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); cursor: pointer; line-height: 1.2; text-decoration: none; display: inline-flex; align-items: center; transition: background 150ms ease, border-color 150ms ease; }
.fui-btn:hover { background: var(--color-surface-soft, #F4F4F5); border-color: var(--color-border-strong, #A1A1AA); }
.fui-btn-primary { background: var(--color-primary, #e0a040); color: var(--color-primary-fg, #fff); border-color: transparent; font-weight: 600; }
.fui-btn-primary:hover { background: color-mix(in srgb, var(--color-primary, #e0a040) 92%, var(--color-text, #18181B)); border-color: transparent; }
.live-form .ui-form__actions .ui-button { font: inherit; font-size: var(--text-sm, .875rem); font-weight: 600; padding: 10px 20px; border: 1px solid transparent; border-radius: var(--radii-md, 6px); background: var(--color-primary, #e0a040); color: var(--color-primary-fg, #fff); cursor: pointer; line-height: 1.4; }
.live-form .ui-form__actions .ui-button:hover { filter: brightness(1.05); }
.live-form .ui-form__actions .ui-button:focus-visible { outline: 2px solid var(--color-primary, #e0a040); outline-offset: 2px; }
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
/* Verdict strip (live page): the server's answer, with the status code in
   display type — that number is the proof. */
.verdict { border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-lg, 12px); padding: var(--spacing-lg, 16px) var(--spacing-xl, 24px); margin: 0 0 var(--spacing-lg, 16px); display: flex; flex-direction: column; gap: 4px; }
.verdict-title { margin: 0; font-size: clamp(1.1rem, 2vw, 1.35rem); font-weight: 700; letter-spacing: -0.01em; font-variant-numeric: tabular-nums; }
.verdict-text { margin: 0; font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); line-height: 1.6; max-width: 70ch; }
.verdict-fresh { background: color-mix(in srgb, var(--color-primary, #e0a040) 4%, var(--color-surface, #fff)); }
.verdict-rejected { background: color-mix(in srgb, var(--color-danger, #b91c1c) 7%, var(--color-surface, #fff)); border-color: color-mix(in srgb, var(--color-danger, #b91c1c) 35%, var(--color-border, #E4E4E7)); }
.verdict-rejected .verdict-title { color: var(--color-danger, #b91c1c); }
.verdict-accepted { background: color-mix(in srgb, var(--color-success, #166534) 7%, var(--color-surface, #fff)); border-color: color-mix(in srgb, var(--color-success, #166534) 35%, var(--color-border, #E4E4E7)); }
.verdict-accepted .verdict-title { color: var(--color-success, #166534); }
/* Live form + accepted receipt: the ui.* component CSS arrives in its own
   splice after this sheet; this is just the card padding around it. */
.live-form { padding: clamp(16px, 3vw, 32px); display: flex; flex-direction: column; gap: var(--spacing-lg, 16px); }
.live-form form { margin: 0; }
.live-accepted { margin: 0; padding: clamp(16px, 3vw, 32px); display: grid; grid-template-columns: minmax(8rem, 14rem) 1fr; gap: var(--spacing-sm, 4px) var(--spacing-lg, 16px); }
.live-accepted dt { font-size: var(--text-sm, .875rem); font-weight: 600; color: var(--color-text-muted, #52525B); }
.live-accepted dd { margin: 0; font-size: var(--text-sm, .875rem); color: var(--color-text, #18181B); overflow-wrap: anywhere; }
.live-again { margin: 0; padding: 0 clamp(16px, 3vw, 32px) clamp(16px, 3vw, 24px); }
@media (max-width: 720px) { .proof-grid { grid-template-columns: 1fr; gap: var(--spacing-md, 8px); } .live-accepted { grid-template-columns: 1fr; } }
@media (max-width: 560px) {
  header { flex-wrap: wrap; }
  .navlink { padding: 6px 10px; }
  .under-editor { justify-content: center; }
  /* Keep the builder usable on a phone: shorter frame, the page still
     resolves with the proof strip and cards stacked below. !important is
     required — the broker pins the iframe height with an inline style. */
  .editor-card iframe { height: 520px !important; }
}
`
