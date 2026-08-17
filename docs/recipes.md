# Recipes

The plugin docs answer "how do I mount this". The recipes answer "what does a
whole app that uses it look like".

Each one is a runnable GoFastr app in this repository, covered by the Go suite
and the Playwright journeys. Read them on GitHub or run them locally; there is
no walkthrough to keep in sync with the code, because the code is the
walkthrough.

The plugin gallery (`go run ./example`) lists them under **Recipes** with a
landing page each — the basics, the command that runs it, and links to the
source. It cannot frame a running recipe: two UIHost apps cannot share a router,
since each claims the whole `/__gofastr/*` namespace, and uihost ships
`frame-ancestors 'none'` by default.

## The blog pair

Two complete blogs, same domain and same reading experience, differing in where
the content lives.

### [`recipes/blogsite`](https://github.com/DonaldMurillo/gofastr-plugins/tree/main/recipes/blogsite) — the markdown blog

Content is a directory of markdown files with frontmatter, parsed once at boot
and `go:embed`'d into the binary. Tag pages, a year-grouped archive,
pagination, search, RSS, JSON Feed, a sitemap derived from the route table,
drafts, and future-dated posts that publish themselves on the next boot.

It uses **no plugin from this repository**, deliberately — it is the baseline
the other recipe is measured against. What it does show is the GoFastr core UI
path end to end: `core/markdown`, `core-ui/app` screens, `framework/ui`
components, and `framework/uihost` for the shell and the SEO endpoints, with no
CSS of its own.

```sh
go run ./recipes/blogsite
```

### [`recipes/blogapp`](https://github.com/DonaldMurillo/gofastr-plugins/tree/main/recipes/blogapp) — the authored blog

Posts live in SQLite and are written in the browser with the `richtext` plugin.
The canonical document is ProseMirror JSON; `richtext/ssr` renders it on the
server, so the editor bundle loads on one route and that route is behind a
login.

```sh
go run ./recipes/blogapp   # sign in at /admin/login, password "demo"
```

Two things in it generalize past blogs:

**The capability gate is not an authentication gate.** `pluginhost.Allow`
resolves to `auth.ScopeMatch(granted, cap) && auth.HasScope(ctx, cap)`, and
`HasScope` returns true for a context with no token scopes. An anonymous POST to
a plugin's save endpoint passes it. The gate is about the plugin's authority,
not the caller's identity — every host that grants a write capability has to add
its own check, and `blogapp` does it inside the save and upload handlers with a
test that proves an anonymous save changes nothing.

**A dynamic route matches an unknown slug.** Any app whose content changes at
runtime has this problem: `/posts/:slug` matches a slug that names nothing, and
a screen that renders its own "not found" body answers HTTP 200. `blogapp`
resolves the slug in middleware before the host routes and rewrites a miss to
its 404 screen with the real status.

## Running the tests

```sh
go test ./recipes/...        # both recipes
cd e2e && npm test           # journeys for both, WebKit + Chromium
```

The e2e config boots each recipe on its own port alongside the plugin gallery.

## See also

- [`plugin-platform.md`](plugin-platform.md) — the isolation model and the
  capability protocol the recipes sit on top of
- [`richtext-editor.md`](richtext-editor.md) — the plugin `blogapp` mounts
