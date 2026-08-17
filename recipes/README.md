# Recipes

Two complete blogs, built the two different ways GoFastr supports. Both are
runnable — `go run ./recipes/<name>` — and both are covered by the repo's Go
suite and its Playwright journeys, so they stay honest as the plugins move.

They are a matched pair. Same domain, same reading experience, opposite
answers to one question: where does the content live?

| | [`blogsite`](blogsite/) | [`blogapp`](blogapp/) |
|---|---|---|
| Content | markdown files in `content/` | rows in SQLite |
| Writing | a text editor, then a deploy | the rich text plugin, in a browser |
| Publishing | `git push` | a status toggle |
| Auth | none | a login gates the admin |
| Plugins used | none | `richtext` (+ `richtext/ssr` for reading) |
| Reading | server-rendered HTML, no JavaScript | server-rendered HTML, no JavaScript |

That last row is the point of the pair. Reading a post is the same job in both,
so both do it the same way: HTML rendered on the server, working without
scripts. What changes is who writes and how.

## `blogsite` — the markdown blog

A directory of markdown files with frontmatter. `content.go` parses them once at
boot and builds the ordering, tag facets, prev/next links, and search index in
memory; a request never touches the filesystem. The content is `go:embed`'d, so
a build is one binary with no assets directory beside it.

Covers what a blog actually needs: tag pages, a year-grouped archive,
pagination, substring search, RSS, JSON Feed, a sitemap derived from the route
table, drafts, and future-dated posts that publish themselves on the next boot.

Uses no plugin from this repository — deliberately. It is the baseline the other
recipe is measured against.

→ [`recipes/blogsite`](blogsite/)

## `blogapp` — the authored blog

Posts live in SQLite (GoFastr's own pure-Go engine, so no cgo and no new module
dependency) and are written in the browser with the sandboxed ProseMirror
editor. The canonical document is the editor's ProseMirror JSON;
`richtext/ssr` renders it to HTML on the server, so the ~600 KB editor bundle
loads on exactly one route and that route is behind a login.

Two things in it are worth reading even if you never want a blog:

- **The plugin's capability gate is not an authentication gate.**
  `pluginhost.Allow` resolves to `auth.ScopeMatch(...) && auth.HasScope(ctx, ...)`,
  and `HasScope` returns true when the context carries no token scopes. An
  anonymous POST to the plugin's save endpoint passes it. The gate answers
  "does this plugin hold this capability", not "may this caller use it" — so the
  app adds the second check itself, inside the save and upload handlers. A test
  proves an anonymous save leaves the stored post untouched.
- **A dynamic route matches an unknown slug.** Answering 200 with a "not found"
  body is a soft 404 — crawlers index it and uptime checks pass while every link
  is broken. A middleware resolves the slug before routing and rewrites misses
  to the 404 screen with the real status.

→ [`recipes/blogapp`](blogapp/)

## Running them

```sh
go run ./recipes/blogsite   # prints its URL
go run ./recipes/blogapp    # prints its URL; sign in at /admin/login, password "demo"
```

Both bind a random free port unless `PORT` is set.

## Tests

```sh
go test ./recipes/...              # both suites
cd e2e && npm test                 # includes the recipe journeys, WebKit + Chromium
```

The e2e config starts each recipe on its own port alongside the plugin gallery;
the ports live in [`e2e/tests/recipes.ts`](../e2e/tests/recipes.ts).
