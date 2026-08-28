package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr/core/render"
)

// demoEntry is one item in the gallery sidebar + home grid.
type demoEntry struct {
	Slug  string // hash id, e.g. "monaco"
	Label string // sidebar/card title
	Path  string // URL loaded into the content iframe
	Blurb string // one-line description
	Badge string // isolation posture: "sandboxed" | "trusted" | "recipe"
	Icon  string // inline emoji glyph
}

// demoEntries drives both the sidebar and the home grid. Adding a plugin here is
// the only change needed to surface it in the gallery.
var demoEntries = []demoEntry{
	{"richtext", "Rich Text", "/richtext", "ProseMirror block editor — full Notion-class editing.", "sandboxed", "📝"},
	{"mermaid", "Mermaid", "/mermaid", "Author diagrams and see them render live.", "sandboxed", "📊"},
	{"monaco", "Monaco", "/monaco", "The VS Code editor — configurable, with a diff mode.", "sandboxed", "🖥️"},
	{"datagrid", "Data Grid", "/datagrid", "100,000 rows over the bridge — sort, filter and paging all run in Go; the frame only ever holds pages.", "sandboxed", "🗂️"},
	{"pdf", "PDF", "/pdf", "View, annotate and truly redact PDFs — the frame has no network at all.", "sandboxed", "📄"},
	{"tour", "Guided Tour", "/tour", "Appcues-style tour that spotlights real page elements.", "trusted", "🧭"},
	{"map", "Geomap", "/map", "MapLibre + OpenFreeMap vector map — editable pins, search, clustering.", "trusted", "🗺️"},
	{"chart", "Chart", "/chart", "One spec, two renderers — a pure-Go SSR SVG hydrated by a sandboxed Plot frame.", "sandboxed", "📈"},
}

// recipeEntries are the whole-app recipes. They get their own sidebar section
// and their own row on the home grid, because they answer a different question
// than a plugin demo does: not "what does this component do" but "what does an
// app that uses one look like".
//
// Each Path is a LANDING PAGE served by this app (see recipes.go), not the
// running recipe. A recipe is its own GoFastr app on its own port — two UIHosts
// cannot share a router, since each claims the whole /__gofastr/* namespace —
// and uihost ships `frame-ancestors 'none'` by default, so a recipe cannot be
// embedded in this shell even cross-origin. The landing page explains the
// recipe, links to the implementation on GitHub, and gives the one command that
// runs it.
var recipeEntries = []demoEntry{
	{"blogsite", "Markdown blog", "/recipes/blogsite", "Posts are markdown files that ship with the app.", "recipe", "📁"},
	{"blogapp", "Authored blog", "/recipes/blogapp", "Write posts in the browser. The app stores them in SQLite and sends readers server-rendered HTML.", "recipe", "✍️"},
}

// registerShell mounts the gallery shell at "/". The plugin demos keep their own
// routes (/richtext, /mermaid, /monaco, /tour) and load into a content iframe so
// the sidebar persists as you move between them.
func registerShell(rt interface {
	Get(string, http.Handler)
}) {
	rt.Get("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This page carries inline <style>/<script>; relax only this route's CSP
		// (the plugin demos it frames keep their own strict policies). frame-src
		// 'self' permits the same-origin content iframe.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
		render.RespondHTML(w, render.HTML(shellHTML()))
	}))
}

func shellHTML() string {
	navItem := func(b *strings.Builder, d demoEntry) {
		b.WriteString(fmt.Sprintf(
			`<a class="nav-item" href="#/%s" data-slug="%s"><span class="nav-ico">%s</span><span class="nav-label">%s</span></a>`,
			d.Slug, d.Slug, d.Icon, html.EscapeString(d.Label)))
	}
	card := func(b *strings.Builder, d demoEntry, cta string) {
		b.WriteString(fmt.Sprintf(
			`<a class="card" href="#/%s"><div class="card-top"><span class="card-ico">%s</span><span class="badge badge-%s">%s</span></div>`+
				`<h3>%s</h3><p>%s</p><span class="card-open">%s</span></a>`,
			d.Slug, d.Icon, d.Badge, d.Badge, html.EscapeString(d.Label),
			html.EscapeString(d.Blurb), cta))
	}

	var nav, cards, recipeNav, recipeCards strings.Builder
	for _, d := range demoEntries {
		navItem(&nav, d)
		card(&cards, d, "Open demo →")
	}
	for _, d := range recipeEntries {
		navItem(&recipeNav, d)
		card(&recipeCards, d, "Read about it →")
	}

	// The client script needs {slug,label,path} to switch the content iframe.
	// Plugins and recipes share one map — the shell treats both the same way.
	type jsDemo struct {
		Slug  string `json:"slug"`
		Label string `json:"label"`
		Path  string `json:"path"`
	}
	all := append(append([]demoEntry{}, demoEntries...), recipeEntries...)
	js := make([]jsDemo, len(all))
	for i, d := range all {
		js[i] = jsDemo{d.Slug, d.Label, d.Path}
	}
	demosJSON, _ := json.Marshal(js)

	// strings.Replace, not fmt.Sprintf: the template's CSS/JS is full of literal
	// '%' (100%, translateX(-100%), color-mix(… 10% …)) that Sprintf misreads.
	return strings.NewReplacer(
		"{{NAV}}", nav.String(),
		"{{CARDS}}", cards.String(),
		"{{RECIPE_NAV}}", recipeNav.String(),
		"{{RECIPE_CARDS}}", recipeCards.String(),
		"{{DEMOS}}", string(demosJSON),
	).Replace(shellTemplate)
}

// shellTemplate: {{NAV}}=sidebar items, {{CARDS}}=home grid, {{DEMOS}}=client JSON.
const shellTemplate = `<!doctype html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoFastr Plugins — Gallery</title>
<style>
  :root{
    --bg:#f6f7f9; --surface:#fff; --text:#1c2024; --muted:#5b6470; --border:#e3e6ea;
    --primary:#4f46e5; --primary-fg:#fff; --rail:248px; --topbar:56px;
  }
  html[data-theme="dark"]{
    --bg:#0b0b0e; --surface:#161619; --text:#f4f4f5; --muted:#a1a1aa; --border:#2a2a30; --primary:#7c7cf0;
  }
  *{box-sizing:border-box}
  html,body{margin:0;height:100%}
  body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:var(--bg);color:var(--text);display:flex;min-height:100%}

  /* Sidebar */
  aside{width:var(--rail);flex:0 0 var(--rail);background:var(--surface);border-right:1px solid var(--border);
        display:flex;flex-direction:column;position:sticky;top:0;height:100vh;z-index:40}
  .brand{display:flex;align-items:center;gap:10px;padding:18px 20px;font-weight:700;font-size:15px;border-bottom:1px solid var(--border)}
  .brand .dot{width:22px;height:22px;border-radius:6px;background:var(--primary);display:inline-flex;align-items:center;justify-content:center;color:var(--primary-fg);font-size:13px}
  nav{padding:10px;display:flex;flex-direction:column;gap:2px;overflow:auto}
  .nav-item,.nav-home{display:flex;align-items:center;gap:12px;padding:10px 12px;border-radius:9px;color:var(--text);text-decoration:none;font-size:14px;line-height:1}
  .nav-item:hover,.nav-home:hover{background:color-mix(in srgb,var(--primary) 10%,transparent)}
  .nav-item.active,.nav-home.active{background:var(--primary);color:var(--primary-fg)}
  .nav-ico{font-size:16px;width:20px;text-align:center}
  .nav-sec{padding:14px 14px 6px;font-size:11px;letter-spacing:.06em;text-transform:uppercase;color:var(--muted)}
  .rail-foot{margin-top:auto;padding:12px;border-top:1px solid var(--border)}
  .theme-btn{width:100%;font:inherit;font-size:13px;padding:8px;border:1px solid var(--border);border-radius:8px;background:transparent;color:var(--text);cursor:pointer}

  /* Main */
  main{flex:1;min-width:0;display:flex;flex-direction:column;height:100vh}
  .topbar{display:none;align-items:center;gap:12px;height:var(--topbar);padding:0 14px;border-bottom:1px solid var(--border);background:var(--surface);position:sticky;top:0;z-index:30}
  .burger{font-size:20px;line-height:1;background:transparent;border:1px solid var(--border);border-radius:8px;padding:6px 10px;cursor:pointer;color:var(--text)}
  .content{flex:1;min-height:0;position:relative}
  iframe{width:100%;height:100%;border:0;background:var(--surface);display:none}
  iframe.show{display:block}

  /* Home panel */
  .home{position:absolute;inset:0;overflow:auto;padding:48px 32px}
  .home.hide{display:none}
  .hero{max-width:820px;margin:0 auto 36px}
  .hero h1{font-size:30px;margin:0 0 8px}
  .hero p{color:var(--muted);font-size:16px;margin:0}
  .grid{max-width:900px;margin:0 auto;display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:16px}
  .card{display:flex;flex-direction:column;gap:8px;padding:18px;border:1px solid var(--border);border-radius:14px;background:var(--surface);text-decoration:none;color:var(--text);transition:transform .08s ease,border-color .08s ease}
  .card:hover{transform:translateY(-2px);border-color:var(--primary)}
  .card-top{display:flex;align-items:center;justify-content:space-between}
  .card-ico{font-size:24px}
  .card h3{margin:2px 0 0;font-size:16px}
  .card p{margin:0;color:var(--muted);font-size:13px;line-height:1.45;flex:1}
  .card-open{color:var(--primary);font-size:13px;font-weight:600}
  .badge{font-size:10px;letter-spacing:.04em;text-transform:uppercase;padding:3px 7px;border-radius:999px;font-weight:700}
  .badge-sandboxed{background:color-mix(in srgb,var(--primary) 16%,transparent);color:var(--primary)}
  .badge-trusted{background:color-mix(in srgb,#d97706 18%,transparent);color:#b45309}
  html[data-theme="dark"] .badge-trusted{color:#fbbf24}
  .badge-recipe{background:color-mix(in srgb,#0d9488 18%,transparent);color:#0f766e}
  html[data-theme="dark"] .badge-recipe{color:#2dd4bf}
  .hero-sec{margin-top:48px}
  .hero-sec h2{font-size:22px;margin:0 0 8px}

  /* Mobile: drawer */
  .scrim{display:none;position:fixed;inset:0;background:rgba(0,0,0,.4);z-index:35}
  @media (max-width:820px){
    aside{position:fixed;left:0;top:0;transform:translateX(-100%);transition:transform .2s ease}
    body.nav-open aside{transform:translateX(0)}
    body.nav-open .scrim{display:block}
    .topbar{display:flex}
  }
</style>
</head>
<body>
  <aside id="sidebar">
    <div class="brand"><span class="dot">⚡</span> GoFastr Plugins</div>
    <nav>
      <a class="nav-home" href="#/home"><span class="nav-ico">🏠</span><span class="nav-label">Home</span></a>
      <div class="nav-sec">Plugins</div>
      {{NAV}}
      <div class="nav-sec">Recipes</div>
      {{RECIPE_NAV}}
    </nav>
    <div class="rail-foot"><button class="theme-btn" id="theme">Toggle theme</button></div>
  </aside>
  <div class="scrim" id="scrim"></div>

  <main>
    <div class="topbar">
      <button class="burger" id="burger" aria-label="Open menu">☰</button>
      <strong id="topbar-title">GoFastr Plugins</strong>
    </div>
    <div class="content">
      <section class="home" id="home">
        <div class="hero">
          <h1>GoFastr plugin gallery</h1>
          <p>Heavy-JavaScript plugins for the GoFastr framework. Most run isolated in an opaque-origin sandboxed iframe; the guided tour runs as a trusted host-page script. Pick one to try it live.</p>
        </div>
        <div class="grid">{{CARDS}}</div>

        <div class="hero hero-sec">
          <h2>Recipes</h2>
          <p>These are complete apps you can run locally. They build the same blog two ways: one reads markdown files, while the other stores posts written in a browser.</p>
        </div>
        <div class="grid">{{RECIPE_CARDS}}</div>
      </section>
      <iframe id="frame" title="Plugin demo"></iframe>
    </div>
  </main>

<script>
(function(){
  var demos = {{DEMOS}};
  var frame = document.getElementById('frame');
  var home = document.getElementById('home');
  var title = document.getElementById('topbar-title');
  var byId = {}; demos.forEach(function(d){ byId[d.slug]=d; });

  function setActive(slug){
    document.querySelectorAll('.nav-item,.nav-home').forEach(function(a){
      a.classList.toggle('active', a.getAttribute('href') === '#/'+slug);
    });
  }
  function render(){
    var slug = (location.hash.replace(/^#\/?/,'') || 'home');
    if(slug === 'home' || !byId[slug]){
      frame.classList.remove('show'); frame.removeAttribute('src');
      home.classList.remove('hide'); setActive('home'); title.textContent='GoFastr Plugins';
    } else {
      var d = byId[slug];
      if(frame.getAttribute('data-slug') !== slug){ frame.src = d.path; frame.setAttribute('data-slug', slug); }
      frame.classList.add('show'); home.classList.add('hide'); setActive(slug); title.textContent = d.label;
    }
    document.body.classList.remove('nav-open'); // close drawer on navigate
  }
  window.addEventListener('hashchange', render);

  document.getElementById('burger').addEventListener('click', function(){ document.body.classList.toggle('nav-open'); });
  document.getElementById('scrim').addEventListener('click', function(){ document.body.classList.remove('nav-open'); });

  var theme = document.getElementById('theme');
  theme.addEventListener('click', function(){
    var next = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
  });

  render();
})();
</script>
</body>
</html>`
