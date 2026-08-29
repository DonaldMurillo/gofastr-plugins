package genui

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// schemeFromCookie reads the persisted color scheme (set by the demo toggle).
func schemeFromCookie(r *http.Request) string {
	if c, err := r.Cookie("fui-color-scheme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

// demoTheme is the shared showcase palette: a warm near-black surface ladder
// and one amber accent, identical to every other demo page here.
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
// The %s slots, in document order: color scheme, theme tokens CSS, component
// count, mount marker HTML, component count again, plugin version, broker URL,
// config.js URL, adapter URL. The shell CSS is spliced in AFTER formatting
// ({{SHELL_CSS}}) because it contains literal "%" that Sprintf must never see
// as format verbs.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Generative UI — Sandboxed Demo</title>
<style>
%s
{{SHELL_CSS}}
</style>
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / genui</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/genui" aria-current="page">Sandboxed</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>
<main>
  <section class="hero">
    <h2>A model arranged this view.<br>It never got to write code.</h2>
    <p class="lead">Ask for something below and a composer answers with a <strong>tree of component names and props</strong> — never markup, never CSS, never a script. Only %s components exist, each with a closed set of typed props, so there is no <code>style</code>, no <code>className</code> and no <code>dangerouslySetInnerHTML</code> to smuggle anything through. Anything outside that set is refused twice: once in Go before it is stored, once in the frame before it is rendered.</p>
    <p class="badges" aria-label="Facts">
      <span class="badge badge-primary">sandbox=&quot;allow-scripts&quot;</span>
      <span class="badge">connect-src 'none'</span>
      <span class="badge">the model runs in Go, not here</span>
      <span class="badge">2 grants: genui:compose · theme:read</span>
    </p>
  </section>

  <section class="editor-card" aria-label="Generated UI frame">
    <div class="editor-chrome" aria-hidden="true">
      <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
      <span class="editor-title">composition.tree</span>
      <span class="editor-mode">sandboxed iframe</span>
    </div>
    %s
  </section>
  <div class="under-editor">
    <p class="composer">
      <label class="visually-hidden" for="genui-prompt">What should it compose?</label>
      <input type="text" id="genui-prompt" value="show me q3 revenue" placeholder="ask for a view…">
      <button type="button" class="fui-btn fui-btn-primary" id="genui-compose">Compose</button>
      <button type="button" class="fui-btn" id="genui-attack">Try an unsafe composition</button>
    </p>
  </div>

  <div class="verdict" id="genui-verdict" aria-label="What the frame did with it">
    <p class="verdict-label">what the frame did with it</p>
    <p class="verdict-text" id="genui-verdict-text" role="status" aria-live="polite">nothing composed yet</p>
  </div>

  <section class="grid" aria-label="How it works">
    <article class="card">
      <h3>🧱 The registry is the boundary</h3>
      <p>%s components, each declaring exactly which props it takes and their types. A composition naming anything else — or passing a prop no component declares — is rejected whole, not sanitised. There is nothing to sanitise: generated output never becomes markup.</p>
    </article>
    <article class="card">
      <h3>🔁 Validated twice, on purpose</h3>
      <p>Go validates before storing and before serving; the frame validates again before rendering. Not belt and braces — "the host already checked it" is exactly the assumption that turns one bug into a rendered payload. Press <em>Try an unsafe composition</em> to watch the second check do its job.</p>
    </article>
    <article class="card">
      <h3>🔑 The model runs in Go</h3>
      <p>The composer and its credentials live server-side; the frame receives a finished tree over the bridge. A frame that could call a model could also send it your document — and under <code>connect-src 'none'</code> this one cannot call anything at all.</p>
    </article>
  </section>
</main>
<footer>
  <p>gofastr-plugins · genui %s · <a href="/">all plugins →</a></p>
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

  function demo() { return window.__gofastrGenuiDemo; }
  var row = document.getElementById('genui-verdict');
  var text = document.getElementById('genui-verdict-text');

  // The verdict line is the page's own reading of the frame's mirrors — the
  // same values the e2e asserts, so what a visitor sees and what the suite
  // checks cannot drift apart.
  function tick() {
    var f = document.querySelector('.editor-card iframe');
    if (!f) return;
    var state = f.__genuiState || 'idle';
    var r = f.__genuiRenderResult;
    row.className = 'verdict' + (state === 'rendered' ? ' is-rendered' : (state === 'refused' ? ' is-refused' : ''));
    if (state === 'pending') { text.textContent = 'composing…'; return; }
    if (state === 'rendered' && r) { text.textContent = 'rendered ' + r.nodeCount + ' nodes'; return; }
    if (state === 'refused' && r) { text.textContent = 'REFUSED — ' + (r.error || 'validation failed'); return; }
    if (state === 'failed') { text.textContent = 'generation failed'; return; }
  }
  setInterval(tick, 200);
  tick();

  document.getElementById('genui-compose').addEventListener('click', function () {
    var p = document.getElementById('genui-prompt').value;
    demo().compose(p).catch(function () {});
  });

  // The containment demo, run against the page itself: a composition naming a
  // component that does not exist, posted straight at the frame past the Go
  // validator. It is refused by the frame's own rules, in front of you. A
  // claim the page can demonstrate beats one it merely states.
  document.getElementById('genui-attack').addEventListener('click', function () {
    demo().pushRawComposition({
      schemaVersion: 'genui-v1',
      root: { component: 'ScriptTag', props: { src: 'https://example.com/evil.js' } }
    });
  });
})();
</script>
<script src="%s"></script>
<script src="%s"></script>
<script src="%s"></script>
</body>
</html>`

// demoShellCSS is the page chrome, kept class-for-class with the other demo
// shells. The rules after it are this page's own: the compose row and the
// verdict line.
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

.composer { display: flex; flex-wrap: wrap; gap: var(--spacing-md, 8px); align-items: center; }
.composer input[type=text] { flex: 1 1 22rem; font: inherit; font-size: var(--text-sm, .875rem); padding: 8px 12px; border: 1px solid var(--color-border, #E4E4E7); border-radius: var(--radii-md, 8px); background: var(--color-surface, #fff); color: var(--color-text, #18181B); }
.composer input[type=text]:focus-visible { outline: 2px solid var(--color-primary, #e0a040); outline-offset: 1px; }
.verdict { display: grid; gap: 6px; margin: var(--spacing-lg, 16px) 2px 0; }
.verdict-label { margin: 0; font-size: var(--text-xs, .75rem); text-transform: uppercase; letter-spacing: .08em; color: var(--color-text-subtle, #71717A); }
.verdict-text { margin: 0; font-family: var(--font-mono, monospace); font-size: var(--text-sm, .875rem); color: var(--color-text-muted, #52525B); word-break: break-word; min-height: 1.5em; }
.verdict.is-refused .verdict-text { color: var(--color-danger, #d1242f); }
.verdict.is-rendered .verdict-text { color: var(--color-text, #18181B); }
@media (max-width: 560px) { .editor-card iframe { height: 520px !important; } }
`

// renderDemo builds the self-contained demo page.
func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	// Script order is load-bearing: broker first (defines __gofastrPluginHost),
	// then this instance's config.js (publishes the registry + action
	// allow-list), then the adapter.
	n := fmt.Sprintf("%d", len(DefaultRegistry().Names()))
	page := fmt.Sprintf(demoPage,
		schemeFromCookie(r),
		demoTheme().CSSCustomProperties(),
		n,
		string(Mount(MountConfig{})),
		n,
		Version,
		pluginhost.BrokerScriptURL,
		ConfigScriptURL,
		AdapterScriptURL,
	)
	return render.HTML(strings.Replace(page, "{{SHELL_CSS}}", demoShellCSS, 1))
}
