# blogapp — the authored blog

A blog whose posts live in SQLite and are written in the browser with this
repository's sandboxed rich text editor.

```sh
go run ./recipes/blogapp
```

It prints its URL. Sign in at `/admin/login` with the password `demo`.

| Variable | Effect |
|---|---|
| `PORT` | pin the port (default: a random free one) |
| `BLOG_DB` | path to a database file (default: in-memory, seeded on every boot) |
| `BLOG_ADMIN_PASSWORD` | replace the demo password |
| `SITE_URL` | absolute origin for the feed and sitemap |

## What is here

```
blogapp/
├── store.go      the posts + images tables, and every query
├── session.go    the admin gate (a demo stand-in — see below)
├── chrome.go     shared shell, plus the resolve-or-404 middleware
├── public.go     the reading side; renders stored documents via richtext/ssr
├── admin.go      login, the post list, and the edit screen that mounts the editor
├── feed.go       RSS + sitemap + robots.txt
├── seed.go       starter posts, written as real ProseMirror documents
└── main.go       wiring, and the two gated plugin handlers
```

## The capability gate is not an authentication gate

This is the thing to take away from the recipe.

The rich text plugin gates its save endpoint through
`pluginhost.Allow(ctx, granted, cap)`, which is:

```go
auth.ScopeMatch(granted, cap) && auth.HasScope(ctx, cap)
```

and `auth.HasScope` **returns true when the context carries no token scopes** —
sessions and JWTs are unscoped by design. So an anonymous
`POST /__gofastr/plugin/richtext/save` passes that check. The gate answers
"does this plugin hold this capability", which is a question about the plugin's
authority, not about who is calling.

The app has to answer the second question. Here that happens inside the save and
upload handlers in `main.go`: session middleware is installed app-wide with
`fw.Use`, so it also runs for the plugin's own endpoints, and each handler reads
the admin session off the request context and refuses anonymous callers.
`TestAnonymousPluginSaveCannotOverwriteAPost` proves it — an anonymous save for
a real post id leaves the stored body untouched.

Note also what is *not* used: `richtext.WithDevGrantAll()`. The `example/` app
sets it because its demos are unauthenticated. An app with a real admin must
not — though leaving it off is not what secures these endpoints either. The
checks inside the handlers are.

## Readers never load the editor

The canonical document is the editor's ProseMirror JSON. `richtext/ssr` renders
it to design-token HTML in pure Go:

```go
html, err := ssr.RenderJSON(post.BodyJSON)
```

so a post page is plain server-rendered HTML with working links, tables, and
code blocks, script or no script. The editor bundle appears on one route,
`/admin/posts/{id}`, behind the login.

Each post also stores a markdown export (`body_md`). It is lossy — search greps
it, and nothing reconstructs a post from it.

## Two persistence paths, on purpose

The editor autosaves the body over the plugin bridge, keyed by the post id, so a
long writing session survives a closed tab. The admin form saves title, slug,
summary, tags, and status. They write disjoint columns (`Store.UpdateBody` vs
`Store.Update`), so the two paths cannot clobber each other.

## The slug rule

A draft's slug follows its title; publishing freezes it; a hand-typed value
always wins. Without the first rule a post created as "Untitled post" keeps
`/posts/untitled-post` forever, because the form round-trips the slug it was
rendered with and every save looks like an explicit choice. Without the second,
a live URL moves whenever someone tweaks a title, breaking every link and feed
entry pointing at it.

## Soft 404s

The public post and tag routes are patterns, because the corpus changes at
runtime — and a pattern matches slugs that name nothing. Serving a "not found"
body at HTTP 200 is a soft 404: crawlers index it, and uptime checks pass while
every link is broken.

Each dynamic screen answers its own status. It embeds `missed`, calls
`s.notFound(...)` on the branch where its entity is gone, and satisfies
`uihost.ScreenStatusCode`, which the host reads after the body has rendered
through the layout. The reader gets the full themed page and the crawler gets
404, from one store lookup.

An earlier version of this recipe did it with middleware that resolved every
public path before the host routed, rewrote misses to `/404`, and wrapped the
`ResponseWriter` to force the code. It worked, and it is worth knowing why it
went: it re-parsed path prefixes the router had already matched, and it
repeated the lookup the screen was about to do anyway.

## The auth here is a demo stand-in

`session.go` is one shared password, sessions in a map, no accounts, no
rotation, no lockout. It exists so the recipe can show a real authorization
boundary around the editor without turning into an auth tutorial.

A real app deletes that file and wires
[`battery/auth`](https://github.com/DonaldMurillo/gofastr/tree/main/battery/auth),
which has accounts, password reset, OAuth, 2FA, and API tokens. What is *not* a
stand-in is where the boundary sits — that part is the lesson.

## The other recipe

If your posts would be happier as files in git, see
[`recipes/blogsite`](../blogsite/) — same reading experience, markdown on disk,
publishing by deploy.
