# Eject a plugin into your repo

`cmd/gofastr-plugin` vendors a plugin's source into your own repo — the shadcn
model, applied to Go. You copy the plugin in and own it, rather than `go get`-ing
it and importing it.

The reason this is ownership rather than a fork you keep re-syncing by hand:
every plugin in this repo imports this repo's `pluginhost` package, and that
package is a pure alias that forwards to `gofastr/framework/pluginhost` in the
core — [`pluginhost/pluginhost.go`](../pluginhost/pluginhost.go) says so itself.
The CLI rewrites that import on the way out. So an ejected plugin depends on
`gofastr` and nothing else from this repo, and the `gofastr-plugins` `require`
can come straight out of your `go.mod`. That is what makes this ownership rather
than a copy you have to nurse.

## When to eject

Eject when you need to **change** the plugin. Import when you only need to
**use** it.

- **Import (the default).** One `go get`, one import, done; `go get -u` keeps you
  on upstream fixes. This is the right call for almost everyone.
- **Eject.** You want a different toolbar, a different canonical document shape,
  or a capability the upstream plugin will not grant. The cost: fixes upstream
  now reach you only when you run `diff` and merge them in by hand. You are off
  `go get -u` for that plugin.

Owning the source is a maintenance decision, not a quality one. The vendored
plugin is byte-identical to the imported one until you edit it.

## Surface

```
gofastr-plugin — vendor a GoFastr plugin into your own repo

USAGE
  gofastr-plugin list
  gofastr-plugin add <plugin>... [flags]
  gofastr-plugin diff [<plugin>]

FLAGS (add)
  --dir string     parent dir for vendored plugins (default "internal/plugins")
  --module string  consumer module path (default: read from the nearest go.mod)
  --with-tests     also vendor the plugin's *_test.go files (pulls chromedp into your go.mod)
  --no-js          skip the TypeScript sources; take only the prebuilt bundle
  --force          overwrite files you have edited since ejecting
  --dry-run        print the plan, write nothing
```

Run it the way Go tools are run, with no install step:

```sh
go run github.com/DonaldMurillo/gofastr-plugins/cmd/gofastr-plugin@latest add mermaid
```

`list` prints the plugins you can eject. `add <plugin>` copies one (or several)
into `--dir`. `diff` tells you what has moved since — yours or upstream's.

The version you run the CLI at *is* the version you eject: the plugin sources
are embedded in the binary, so `@latest` and `@v0.4.0` hand you different code.
Pin the tag when you want the eject reproducible, the same way a deploy pins the
[`plugins.json`](../plugins.json) it vendors. The tag you used is recorded as
`ejectedFrom` in the lock file, so a copy always says where it came from.

## Walkthrough: eject mermaid

From your consumer repo (the one whose `go.mod` will own the copy):

```sh
# 1. See what is available.
go run github.com/DonaldMurillo/gofastr-plugins/cmd/gofastr-plugin@latest list

# 2. Vendor mermaid into internal/plugins/mermaid and rewrite its imports.
go run github.com/DonaldMurillo/gofastr-plugins/cmd/gofastr-plugin@latest add mermaid

# 3. Resolve the dependencies yourself. The CLI does not do this for you.
#    Your go.mod needs `go 1.26.5` or newer; see the note below.
go get github.com/DonaldMurillo/gofastr@v0.46.0
go mod tidy

# 4. Build your app; the vendored package is now yours.
go build ./...
```

`add` reads your module path from the nearest `go.mod` and rewrites the
`pluginhost` import in the copied Go files to point at `gofastr/framework/pluginhost`.

## What the CLI does not do

`add` writes files. That is all it does. It does not run `go get`, `go mod
tidy`, or `npm install`, and it does not edit your `go.mod`.

This is deliberate. Dependency resolution belongs to whoever runs the project:
they own the lockfiles, the CI cache, the private proxy, the vendor directory,
the decision about which `gofastr` patch release to move to. A vendoring tool
that quietly mutates `go.mod` or drops a `node_modules/` into your tree is doing
work you did not ask for, in a way that is invisible in review. So `add` prints
the commands and leaves them to you.

What lands is code plus **configuration** — `package.json`, `package-lock.json`,
`tsconfig.json`, `build.mjs` — which is enough for the install to be
reproducible when you do run it. The exact versions the upstream bundle was
built from are in that lockfile.

After ejecting a plugin with its `js/` sources, the full set is:

```sh
go get github.com/DonaldMurillo/gofastr@v0.46.0   # if not already required
go mod tidy
cd internal/plugins/mermaid/js && npm ci          # only if you intend to rebuild the bundle
```

The `npm ci` is optional and only for rebuilding: `assets/` ships prebuilt and
`go:embed` serves it, so a plugin you do not intend to modify needs no Node
toolchain at all.

### Raise your `go` directive first

Ejected plugins need `go 1.26.5` or newer, and a `go.mod` asking for less fails
in a way that does not name itself. You get a toolchain resolution error about a
transitive gofastr package:

```
go: toolchain upgrade needed to resolve github.com/DonaldMurillo/gofastr/core-ui/style
go: github.com/DonaldMurillo/gofastr@v0.46.0 requires go >= 1.26.5 (running go 1.24.2)
```

That reads like a broken dependency; it is a `go` directive that is too old. Go
will not raise it for you, so nothing self-corrects. `add` prints the floor in
its install block for this reason.

## What lands where

```
internal/plugins/mermaid/
├── plugin.go handlers.go demo.go assets.go   # imports rewritten
├── assets/                                   # the prebuilt bundle, served same-origin via go:embed
├── host/adapter.js
└── js/                                       # the TypeScript the bundle is built from, + build.mjs
```

Plus `gofastr-plugins.json` at your project root: the lock file. The Go sources
are the same files as `mermaid/*.go` with one import path changed. `assets/` is
the committed prebuilt bundle, untouched — `go:embed` serves it same-origin
exactly as the imported plugin does. `host/adapter.js` is the thin host-side
adapter that registers with the generic broker. `js/` is the TypeScript source
plus that plugin's `build.mjs`, so you can rebuild the bundle after editing it
(see [Rebuilding the JS bundle](#rebuilding-the-js-bundle)).

## The import rewrite, and the dependency that drops

The plugin's Go files import this repo's alias:

```go
import "github.com/DonaldMurillo/gofastr-plugins/pluginhost"
```

The vendored copy imports the core directly:

```go
import "github.com/DonaldMurillo/gofastr/framework/pluginhost"
```

`pluginhost` is the package that ties a plugin to this repo. Once the import is
rewritten, the copy has no tie left to `gofastr-plugins`, so the `require` comes
out of your `go.mod`:

Before (importing mermaid):

```
require (
	github.com/DonaldMurillo/gofastr v0.46.0
	github.com/DonaldMurillo/gofastr-plugins v0.3.0
)
```

After ejecting mermaid:

```
require github.com/DonaldMurillo/gofastr v0.46.0
```

The versions above are illustrative — pin the gofastr release you actually run
and the gofastr-plugins release you eject from. `frameworkCompat` in
[`plugins.json`](../plugins.json) is the build floor.

`richtext` is the one plugin with a second intra-repo import: it uses its own
`richtext/ssr` sub-package for the pure-Go read view. That is rewritten too, to
the vendored copy — `plugin.go` in an ejected `richtext` imports
`<your-module>/internal/plugins/richtext/ssr`, and `ssr/` lands alongside it. So
`richtext` drops the `require` like the rest.

The rewrite matches a plugin's own package and its subpackages, and nothing
else. If a future plugin imports something from this repo that those rules do
not cover, `add` fails with the file and the offending path rather than writing
a copy that still reaches back here. That check is what keeps the promise in
this section true as the repo changes, instead of true only on the day it was
written.

## The lock file, and why two hashes

`gofastr-plugins.json` sits at your project root and records, per vendored file,
two hashes:

- a hash of **what the CLI wrote**, and
- a hash of the **upstream source** the file came from.

That pair is what `diff` reads. If a file no longer matches the hash of what was
written, **you** changed it. If the upstream source no longer matches the hash
it had when you ejected, **upstream** changed. So `diff` can tell you which side
moved, per file, and both at once — the thing a plain `cp -r` fork can never tell
you.

```sh
gofastr-plugin diff            # every vendored plugin
gofastr-plugin diff mermaid    # just one
```

`diff` exits non-zero when there is drift, so it works as a CI check that notices
when a plugin you own has fallen behind upstream, or when a teammate has edited a
vendored file without recording why.

The shape, trimmed to one file:

```json
{
  "version": "1",
  "source": "github.com/DonaldMurillo/gofastr-plugins",
  "dir": "internal/plugins",
  "plugins": {
    "mermaid": {
      "version": "0.1.0-phase0",
      "ejectedFrom": "v0.4.0",
      "ejectedAt": "2026-07-26T20:59:31Z",
      "dir": "internal/plugins/mermaid",
      "withTests": false,
      "withJS": true,
      "files": {
        "internal/plugins/mermaid/assets.go": {
          "upstream": "sha256:d13a3189…",
          "vendored": "sha256:d13a3189…"
        }
      }
    }
  }
}
```

`ejectedFrom` is the CLI version the copy came from, so a vendored tree always
says where it originated — the same reasoning behind the `release` stamp on a
published [`plugins.json`](../plugins.json). Built from a checkout rather than a
release tag it reads `v0.3.0+dirty`, which is Go's own build-info wording for an
uncommitted tree.

Commit this file. It is what makes `diff` meaningful for anyone but the person
who ran `add`.

## Conflicts and `--force`

`add` refuses to overwrite a file whose hash no longer matches what the CLI
wrote — that is, a file you have edited since ejecting. The guarantee: a re-run
cannot silently clobber your changes. To overwrite anyway, pass `--force`:

```sh
go run github.com/DonaldMurillo/gofastr-plugins/cmd/gofastr-plugin@latest add mermaid --force
```

`--dry-run` prints the plan (what would be added, overwritten, or skipped) and
writes nothing. Run it before a `--force`, or the first time you eject into a
directory that already has content.

## Rebuilding the JS bundle

Each plugin's `js/` ships a `build.mjs` that emits the prebuilt bundle into
`../assets/`, which `go:embed` then serves. After you edit the TypeScript,
rebuild the bundle the same way this repo does:

```sh
cd internal/plugins/mermaid/js
npm ci
npm run build      # regenerates ../assets/diagram.{js,css,html}
```

In this repo, CI asserts the committed bundle is up to date with its sources:
the *bundle is up to date with its sources* job in
[`.github/workflows/ci.yml`](../.github/workflows/ci.yml) rebuilds and fails if
`assets/` would change. A vendored copy has no such guard unless you add one, so
treat a rebuild as part of any edit to `js/`.

## The tradeoff

Ejecting is the right call when you need to change the plugin. It is the wrong
call if you just want the plugin. Importing keeps you on `go get -u` for fixes;
ejecting takes you off it. Upstream fixes reach an ejected copy only when you run
`diff` and merge them in by hand.

`--with-tests` drags `chromedp` into your `go.mod` — the test harness drives a
real headless Chrome — which is why it is off by default. Take it only if you
intend to keep the plugin's tests running in your repo.

## Isolation is unchanged

Owning the source moves no security boundary. A vendored sandboxed plugin is
still an opaque-origin sandboxed iframe; it still talks to the host only over the
same versioned `postMessage` bridge, and the same capability grants apply.
Plugins that need a host-page CSP still need it: `pdf`'s frame CSP is
`connect-src 'none'` (see [`pdf.md`](pdf.md)), and `geomap`'s search keeps the
host page at `connect-src 'self'` via its same-origin geocode proxy (see
[`geomap.md`](geomap.md)). Copying the source does not relax any of that — the
sandbox is in the frame attributes and the headers, not in who owns the files.
