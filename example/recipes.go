package main

// The recipe landing pages.
//
// A plugin card in the gallery frames the plugin's live demo. A recipe card
// cannot do that: a recipe is a whole GoFastr app, and two UIHost apps cannot
// share a router — each claims the entire /__gofastr/* namespace at Mount time.
// Nor can this shell embed one cross-origin, because uihost ships
// `frame-ancestors 'none'` + `X-Frame-Options: DENY` by default and a recipe
// that relaxed that to be demo-able would be teaching the wrong thing.
//
// So a recipe gets a landing page instead: what it is, the handful of decisions
// worth knowing, the one command that runs it, and links straight to the
// implementation on GitHub. These pages are served by THIS app, same-origin, so
// they load in the gallery's content iframe exactly like a plugin demo.

import (
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr/core/render"
)

// repoTree is the GitHub base for source links.
const repoTree = "https://github.com/DonaldMurillo/gofastr-plugins/tree/main"

// recipePage is the content of one landing page.
type recipePage struct {
	Slug    string
	Title   string
	Lede    string
	Command string   // the one command that runs it
	Port    string   // where it lands by default in these docs
	Points  []point  // the decisions worth knowing
	Files   []srcRef // the files worth opening first
	Docs    []link   // README, docs page
}

type point struct {
	Heading string
	Body    string // may contain <code>…</code>, already trusted (authored here)
}

type srcRef struct {
	Path string // repo-relative, becomes both the label and the link
	What string
}

type link struct {
	Label string
	Href  string
}

var recipePages = []recipePage{
	{
		Slug:  "blogsite",
		Title: "Markdown blog",
		Lede: "Posts are markdown files kept in the repository. Writing one means adding a file and " +
			"deploying. There is no database and nothing to log into.",
		Command: "go run ./recipes/blogsite",
		Points: []point{
			{
				"A broken post stops the server",
				"A post with no date, a date it cannot read, or a slug that collides with another one will not " +
					"start the app. The alternatives are worse. Skip the file and you ship a blog quietly missing a " +
					"post; default the date and it sorts to the bottom of every list with a 1970 timestamp that feed " +
					"readers will cache.",
			},
			{
				"Every URL exists before the first request",
				"There is no <code>/posts/:slug</code> pattern here. Each post, tag and page gets its own route " +
					"when the process starts. Two things follow: a slug that does not exist matches nothing and gets " +
					"a real 404 without any lookup code, and the sitemap is built from the route table, so it cannot " +
					"drift from what the site actually serves.",
			},
			{
				"The markdown is read once, at startup",
				"Requests never open a file or parse anything — they read from memory. The posts are compiled " +
					"into the binary as well, so what you deploy is a single file with no content directory beside it.",
			},
			{
				"It uses none of the plugins",
				"Deliberately. This is the plain build: core markdown, screens and components, and no CSS of its " +
					"own. It is here so the other recipe's page shows you exactly what adding a browser editor costs.",
			},
		},
		Files: []srcRef{
			{"recipes/blogsite/content.go", "parses frontmatter and markdown into posts"},
			{"recipes/blogsite/screens.go", "registers routes and renders each page"},
			{"recipes/blogsite/feed.go", "builds the RSS and JSON feeds"},
			{"recipes/blogsite/content/posts", "contains the markdown posts and their frontmatter"},
		},
		Docs: []link{
			{"README", repoTree + "/recipes/blogsite/README.md"},
			{"docs/recipes.md", repoTree + "/docs/recipes.md"},
		},
	},
	{
		Slug:  "blogapp",
		Title: "Authored blog",
		Lede: "Posts live in SQLite, and you write them in the browser with the Rich Text plugin. " +
			"The public blog renders them on the server.",
		Command: "go run ./recipes/blogapp",
		Port:    "sign in at /admin/login with the password \"demo\"",
		Points: []point{
			{
				"The editor saves as you type",
				"Create a post and the editor opens it immediately. It saves each change as you type, so closing " +
					"the tab does not discard your work. Set the post to published when it is ready.",
			},
			{
				"Readers get server-rendered HTML",
				"Posts are stored in the editor's document format, then rendered to HTML on the server. The public " +
					"post page works without JavaScript and never loads the editor's roughly 600&nbsp;KB bundle.",
			},
			{
				"The plugin check is not user authentication",
				"The plugin's permission check only asks whether <em>the plugin</em> holds a capability. It does " +
					"not authenticate the caller or decide whether that person may use it; an anonymous request " +
					"passes it. The app therefore requires a logged-in admin in both the save and upload handlers. " +
					"A test submits an anonymous save and verifies that the post does not change.",
			},
			{
				"A missing post returns HTTP 404",
				"Because <code>/posts/:slug</code> matches any slug-shaped path, routing alone cannot tell whether " +
					"the post exists. The app looks up the slug before routing and returns HTTP 404 when it is " +
					"missing, instead of rendering a &ldquo;not found&rdquo; page with status 200.",
			},
		},
		Files: []srcRef{
			{"recipes/blogapp/main.go", "wires the app and checks admin access in the save and upload handlers"},
			{"recipes/blogapp/session.go", "handles login sessions and the admin check"},
			{"recipes/blogapp/public.go", "renders public pages without loading the editor"},
			{"recipes/blogapp/admin.go", "renders the post list and editor page"},
		},
		Docs: []link{
			{"README", repoTree + "/recipes/blogapp/README.md"},
			{"docs/recipes.md", repoTree + "/docs/recipes.md"},
		},
	},
	{
		Slug:  "relayboard",
		Title: "Measured funnel",
		Lede: "A three-screen product whose analytics run through this repository's PostHog integration. " +
			"The browser talks only to your own origin, and the funnel is still attributed end to end.",
		Command: "go run ./recipes/relayboard",
		Port:    "http://localhost:8099 (no PostHog key needed to start)",
		Points: []point{
			{
				"Attribution survives client-side navigation",
				"Land on <code>/?utm_source=twitter</code>, move to the pricing page, buy. The " +
					"<code>purchase</code> event still carries <code>utm_source=twitter</code> even though the " +
					"parameters left the address bar, because posthog-js registers them from the first URL it " +
					"sees and attaches them to every later capture.",
			},
			{
				"The app's own auth is the only identity",
				"The <code>whoami</code> endpoint answers from the session: an anonymous visitor gets " +
					"<code>{\"id\":null}</code>, a signed-in one gets their account id, and logging in merges " +
					"the anonymous person into the identified one. No identity exists on the analytics side to " +
					"disagree with.",
			},
			{
				"The feature gate is decided on the server, and fails closed",
				"<code>/beta</code> asks a forty-line <code>featureflag.Store</code> that posts to the flags " +
					"endpoint — <code>/decide</code> answers 403 on current self-hosted PostHog. An unknown key " +
					"returns no answer rather than false, so default semantics survive, and an error denies.",
			},
			{
				"It degrades to a working app",
				"With no <code>POSTHOG_KEY</code> every page still works: no plugin, no flag store, " +
					"<code>/beta</code> answers invite-only, and the A/B script no-ops because " +
					"<code>window.posthog</code> never appears. One log line says which mode it is in.",
			},
			{
				"No browser tests, on purpose",
				"posthog-js drops every capture it believes came from a bot, which is what a headless browser " +
					"looks like — so a browser suite here would assert against an empty funnel and pass. HTTP " +
					"smoke tests pin the routes, the gate against a fake PostHog, and the register-to-whoami " +
					"identity chain instead.",
			},
		},
		Files: []srcRef{
			{"recipes/relayboard/main.go", "the screens, the accounts, the flag store, the gate and the A/B script"},
			{"recipes/relayboard/relayboard_test.go", "smoke tests over HTTP, including against a fake PostHog"},
			{"posthog/plugin.go", "the integration this recipe is built on"},
		},
		Docs: []link{
			{"README", repoTree + "/recipes/relayboard/README.md"},
			{"docs/recipes.md", repoTree + "/docs/recipes.md"},
		},
	},
}

// registerRecipePages mounts a landing page per recipe. Registered alongside the
// gallery shell so they sit on the same router as the plugin routes.
func registerRecipePages(rt interface {
	Get(string, http.Handler)
}) {
	for _, p := range recipePages {
		body := recipePageHTML(p)
		rt.Get("/recipes/"+p.Slug, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Same relaxation the shell takes, and for the same reason: this page
			// carries an inline <style>. It frames nothing, so frame-src stays out.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, render.HTML(body))
		}))
	}
}

func recipePageHTML(p recipePage) string {
	var points strings.Builder
	for _, pt := range p.Points {
		points.WriteString(fmt.Sprintf(
			`<section class="point"><h3>%s</h3><p>%s</p></section>`,
			html.EscapeString(pt.Heading), pt.Body))
	}

	var files strings.Builder
	for _, f := range p.Files {
		files.WriteString(fmt.Sprintf(
			`<li><a href="%s/%s" target="_blank" rel="noopener noreferrer"><code>%s</code></a> — %s</li>`,
			repoTree, f.Path, html.EscapeString(f.Path), html.EscapeString(f.What)))
	}

	var docs strings.Builder
	for _, d := range p.Docs {
		docs.WriteString(fmt.Sprintf(
			`<a class="btn btn-ghost" href="%s" target="_blank" rel="noopener noreferrer">%s ↗</a>`,
			d.Href, html.EscapeString(d.Label)))
	}

	run := html.EscapeString(p.Command)
	if p.Port != "" {
		run += "\n# " + html.EscapeString(p.Port)
	}

	return strings.NewReplacer(
		"{{TITLE}}", html.EscapeString(p.Title),
		"{{LEDE}}", html.EscapeString(p.Lede),
		"{{RUN}}", run,
		"{{POINTS}}", points.String(),
		"{{FILES}}", files.String(),
		"{{DOCS}}", docs.String(),
		"{{SRC}}", repoTree+"/recipes/"+p.Slug,
	).Replace(recipePageTemplate)
}

// recipePageTemplate mirrors the shell's tokens so a landing page loaded into
// the content iframe looks like part of the same site.
const recipePageTemplate = `<!doctype html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{TITLE}} — GoFastr recipe</title>
<style>
  :root{
    --bg:#f6f7f9; --surface:#fff; --text:#1c2024; --muted:#5b6470; --border:#e3e6ea;
    --primary:#4f46e5; --primary-fg:#fff; --accent:#0d9488;
  }
  @media (prefers-color-scheme: dark){
    :root{--bg:#0b0b0e; --surface:#161619; --text:#f4f4f5; --muted:#a1a1aa; --border:#2a2a30; --primary:#7c7cf0; --accent:#2dd4bf}
  }
  *{box-sizing:border-box}
  html,body{margin:0}
  body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:var(--bg);color:var(--text);
       line-height:1.6;padding:48px 32px 72px}
  .wrap{max-width:760px;margin:0 auto}
  .eyebrow{display:inline-block;font-size:11px;letter-spacing:.06em;text-transform:uppercase;font-weight:700;
           color:var(--accent);background:color-mix(in srgb,var(--accent) 14%,transparent);
           padding:4px 9px;border-radius:999px;margin-bottom:14px}
  h1{font-size:30px;line-height:1.2;margin:0 0 10px}
  .lede{font-size:17px;color:var(--muted);margin:0 0 28px}
  h2{font-size:14px;letter-spacing:.06em;text-transform:uppercase;color:var(--muted);margin:40px 0 14px}
  .point{border-top:1px solid var(--border);padding-top:18px;margin-bottom:22px}
  .point h3{font-size:17px;margin:0 0 6px}
  .point p{margin:0;color:var(--muted)}
  code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.9em;
       background:color-mix(in srgb,var(--text) 8%,transparent);padding:1px 5px;border-radius:5px}
  pre{background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:16px 18px;
      overflow-x:auto;margin:0 0 8px}
  pre code{background:none;padding:0;font-size:13px;line-height:1.7}
  ul{padding-left:20px;margin:0;color:var(--muted)}
  li{margin-bottom:8px}
  a{color:var(--primary)}
  .actions{display:flex;flex-wrap:wrap;gap:10px;margin-top:10px}
  .btn{display:inline-block;text-decoration:none;font-size:14px;font-weight:600;padding:9px 16px;border-radius:9px;
       background:var(--primary);color:var(--primary-fg);border:1px solid var(--primary)}
  .btn-ghost{background:transparent;color:var(--text);border-color:var(--border)}
  .note{border-left:3px solid var(--accent);padding:2px 0 2px 14px;color:var(--muted);font-size:14px;margin:14px 0 0}
</style>
</head>
<body>
  <div class="wrap">
    <span class="eyebrow">Recipe</span>
    <h1>{{TITLE}}</h1>
    <p class="lede">{{LEDE}}</p>

    <div class="actions">
      <a class="btn" href="{{SRC}}" target="_blank" rel="noopener noreferrer">Source on GitHub ↗</a>
      {{DOCS}}
    </div>

    <h2>Run it</h2>
    <pre><code>{{RUN}}</code></pre>
    <p class="note">This recipe runs as its own app, so start it in a second terminal. It cannot run
    inside the gallery because both apps claim <code>/__gofastr/*</code>. Recipes also send
    <code>frame-ancestors 'none'</code>, which prevents the gallery from embedding them.</p>

    <h2>Implementation notes</h2>
    {{POINTS}}

    <h2>Source files</h2>
    <ul>{{FILES}}</ul>
  </div>
</body>
</html>`
