# Changelog

All notable changes to gofastr-plugins. Follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are
`0.x-phase` until the platform API stabilises.

## [Unreleased]

### Added — posthog: first-party PostHog in one call (2026-08-25)

[`posthog/`](posthog/) packages the PostHog recipe from gofastr's
analytics-recipes doc — the two-route relay table, the page bootstrap,
the whoami identity endpoint — behind `posthog.New(Config{Key, Region})`
+ `app.RegisterPlugin` + `host.RegisterExternalScript(p.ScriptURL())`
(or `p.Attach(host)`).

It is deliberately **not** one of this repo's sandboxed iframe plugins,
and the README's new "Integrations" section says so: posthog-js
instruments the whole host document, so it runs in the host page and
cannot be fenced. What stays first-party is the wire — the browser
talks only to your origin through `battery/relay`, the strict default
CSP needs no `script-src`/`connect-src` exceptions, and no vendor
cookie lands on your domain.

Three decisions worth writing down:

- **The config is baked into the served bytes, not script attributes.**
  `RegisterExternalScript` emits a bare `<script src>` tag, so the
  key, mount, region UI host and DNT flag are rendered into boot.js at
  `New` via `encoding/json`. That is load-bearing twice: the bootstrap
  is one file with no globals to race, and Go's JSON encoder
  HTML-escapes `<`/`>`/`&`, so a key containing `</script>` stays
  inert — pinned by a test that feeds exactly that key and asserts the
  escaped form (mutation-proven: appending the raw key to the served
  bytes fails the suite).
- **Secret key shapes panic at construction.** `phx_` (personal) and
  `sk_` (server) keys are secrets, and this package puts the key in
  bytes served to every visitor. `New` refuses them rather than ship
  one; `phc_` (public project) is the only shape that belongs
  client-side.
- **No `ExtraIngestPaths` knob.** PostHog has moved endpoints before
  (the `-assets` split), and when it happens again the escape hatch is
  a hand-declared `relay.New` alongside — a knob whose upstream is
  implied rather than named is how an open proxy starts. The package
  README documents the pattern instead.

Unit-tested with zero vendor account and zero egress: an unexported
`newWithUpstreams` seam lets the suite point both relay upstreams at
loopback httptest servers, so the region table, tail/query forwarding,
404/405 posture, the 8 MiB → 64 MiB session-replay body cap and the
rendered bootstrap are all asserted against real HTTP. The deep relay
matrix (hostile tails, credential stripping) stays in battery/relay's
own suite; five mutations (secret-key guard, region table, replay cap,
raw-key leak, Stringer arm) each fail their test.


### Changed — gofastr v0.46.0 → v0.65.0 (2026-08-17)

- Nineteen framework releases in one step. No plugin code changed: build, vet,
  the full Go suite, the eject canary and the 302 WebKit + Chromium journeys all
  pass untouched.

- **The `go` directive moves to 1.26.6**, because gofastr v0.65.0 requires it. A
  `go.mod` asking for less fails as a toolchain resolution error naming a
  transitive gofastr package, which is the trap `docs/eject.md` already
  describes. CI reads `go-version-file: go.mod`, so the job follows the bump
  without an edit.

- **New indirect dependencies, all from one upstream swap.** gofastr v0.56.0
  made `modernc.org/sqlite` the `sqlite3` driver, so `modernc.org/{sqlite,libc,
  mathutil,memory}` and four smaller transitives are now in the graph. It is a
  pure-Go driver, so nothing here needs cgo. The edge reaches this repo only
  through `recipes/blogapp`; no plugin imports `gofastr/sqlite`. The bump needed
  a `go mod tidy` for the new `go.sum` entries — `go get` alone left the build
  red.

- `frameworkCompat` stays at `>=0.28.0`, verified rather than carried forward:
  all six registry plugins still build against v0.28.0. `recipes/blogapp` does
  not (it uses `ui.TextField`, which landed later), but recipes are whole apps
  rather than registry entries and make no compat claim.

- `docs/eject.md` now shows v0.65.0 and `go 1.26.6` in its install examples. The
  CLI's own install block needed no change: it parses both numbers out of the
  embedded `go.mod` at init (`GoFastrVersion`, `GoVersion` in `source.go`), so
  `gofastr-plugin add` printed the new floor as soon as the require moved.

### Added — recipes: two complete blogs (2026-07-28)

`recipes/` holds whole apps rather than plugin demos. `example/` exists to mount
every plugin at once; a recipe exists to answer what an app that uses one
actually looks like, including the parts a demo skips — auth, persistence,
feeds, 404s.

The first two are a matched pair. Same domain, same reading experience, opposite
answers to "where does the content live?":

- **[`recipes/blogsite`](recipes/blogsite/)** — markdown files with frontmatter.
  `content.go` parses them once at boot and builds the ordering, tag facets,
  prev/next links, and search index in memory; a request never touches the
  filesystem. The content is `go:embed`'d, so a build is one binary with no
  assets directory beside it. Tag pages, a year-grouped archive, pagination,
  substring search, RSS 2.0, JSON Feed 1.1, sitemap, `robots.txt`, drafts, and
  future-dated posts that publish themselves on the next boot. It uses **no
  plugin from this repo** — that is the point: it is the baseline `blogapp` is
  measured against, and it exercises the GoFastr core UI path end to end with no
  CSS of its own.
- **[`recipes/blogapp`](recipes/blogapp/)** — posts in SQLite (GoFastr's pure-Go
  engine, so no cgo and **no new module dependency**), written in the browser
  with the `richtext` plugin. The canonical document is ProseMirror JSON;
  `richtext/ssr` renders it server-side, so readers get plain HTML and the
  ~600 KB editor bundle loads on exactly one route, behind a login.

**The capability gate is not an authentication gate.** This is the finding
`blogapp` was worth building for. `pluginhost.Allow(ctx, granted, cap)` is
`auth.ScopeMatch(granted, cap) && auth.HasScope(ctx, cap)`, and `HasScope`
returns **true** when the context carries no token scopes — sessions and JWTs
are unscoped by design. So an anonymous `POST /__gofastr/plugin/richtext/save`
passes it. The gate answers "does this plugin hold this capability", a question
about the plugin's authority, not the caller's identity. Every host that grants
a write capability has to add the second check itself. `blogapp` does, inside
its save and upload handlers, reading the admin session off the request context
that app-wide middleware annotated; `TestAnonymousPluginSaveCannotOverwriteAPost`
asserts an anonymous save leaves the stored body untouched. It also does **not**
set `richtext.WithDevGrantAll()`, which `example/` uses only because its demos
are unauthenticated.

**Soft 404s.** A database-backed app needs dynamic routes, and `/posts/:slug`
matches slugs that name nothing. Serving a not-found body at HTTP 200 is the
failure crawlers index and monitoring never notices. `blogapp` resolves the slug
in middleware before the host routes and rewrites a miss to its 404 screen with
the real status. `blogsite` avoids the problem entirely by registering one route
per post — its corpus is fixed at boot, which also makes the route table the
sitemap.

Two bugs the tests caught while building these, both now guarded:

- `ui.SiteHeader` renders its `Actions` slot **twice** (desktop bar + mobile
  drawer), so a form control with a fixed `id` there lands in the DOM twice — a
  duplicate-id a11y violation. Both recipes moved site search from `Actions` to
  a nav link, and both suites now walk every page asserting no duplicate ids.
- A slug rule that only checked draft status discarded a hand-typed slug on the
  save that published it, and a slug that `uniqueSlug` had suffixed
  (`untitled-post-2`, from a second new post) never followed its title. The rule
  is now: a hand-typed value wins; otherwise a derived slug follows the title
  while the post is a draft; publishing freezes it.

Both recipes are covered by `go test ./recipes/...` (37 tests: routing, feed and
sitemap shape, draft exclusion, the admin gate, the authoring round trip, the
anonymous-save refusal) and by Playwright journeys in **WebKit and Chromium**
(46 tests, including the editor mounting in its opaque-origin frame, typing
reaching the database over the bridge, and the reader getting no plugin).
`e2e/playwright.config.ts` now starts three servers; the recipe ports live in
[`e2e/tests/recipes.ts`](e2e/tests/recipes.ts).

**In the gallery.** `example/` now carries a **Recipes** section in its sidebar
and on its home grid, with a landing page per recipe
([`example/recipes.go`](example/recipes.go)) explaining the basics, giving the
one command that runs it, and linking to the implementation on GitHub. The
landing pages are served by the gallery itself, so they load in the same content
iframe and the sidebar persists — a recipe cannot be framed directly, because
two UIHost apps cannot share a router (each claims the whole `/__gofastr/*`
namespace) and uihost ships `frame-ancestors 'none'` by default.

See [`docs/recipes.md`](docs/recipes.md) and
[`recipes/README.md`](recipes/README.md).

### Added — gofastr-plugin: eject a plugin into your own repo (2026-07-26)

`cmd/gofastr-plugin` is the eject CLI. It copies a plugin's source into a
consumer repo the way shadcn copies a component, and the consumer owns the
result. It works for one reason: every plugin in this repo imports this repo's
`pluginhost`, and that package is a pure alias forwarding to
`gofastr/framework/pluginhost` in the core (see
[`pluginhost/pluginhost.go`](pluginhost/pluginhost.go)). The CLI rewrites that
import on the way out, so an ejected plugin depends on `gofastr` and nothing
else from this repo. The `gofastr-plugins` require can come straight out of the
consumer's `go.mod`.

- **What lands.** The plugin's Go sources (imports rewritten), the prebuilt
  bundle under `assets/` served same-origin via `go:embed` (unchanged), the host
  adapter, and the `js/` TypeScript sources plus `build.mjs` so the bundle can
  be rebuilt after an edit. Default target: `internal/plugins/<name>`.
- **It writes files and nothing else.** No `go get`, no `go mod tidy`, no `npm
  install`, and no edit to your `go.mod`. Dependency resolution belongs to
  whoever runs the project — they own the lockfiles, the proxy, and the choice
  of which `gofastr` patch to move to. What lands is code plus configuration
  (`package.json`, `package-lock.json`, `tsconfig.json`, `build.mjs`), which is
  enough to make the install reproducible when they run it. The CLI prints the
  commands; it does not execute them.
- **The lock file** (`gofastr-plugins.json` at the project root) records two
  hashes per vendored file: what the CLI wrote, and the upstream source it came
  from. `gofastr-plugin diff` reads that pair to tell whether *you* moved (your
  file no longer matches the written hash) or *upstream* moved, and exits
  non-zero on drift, so it works as a CI check. A plain `cp -r` fork cannot tell
  the two apart.
- **`--force` is the conflict rule.** `add` refuses to overwrite a file whose
  hash no longer matches what the CLI wrote (one you have edited) unless you
  pass `--force`. `--with-tests` also vendors the `*_test.go` files, which pulls
  `chromedp` into the consumer's `go.mod`; off by default for that reason.
  `--no-js` takes only the prebuilt bundle.
- **The tradeoff, stated plainly.** Eject when you need to change the plugin — a
  different toolbar, a different canonical document, a capability upstream will
  not grant. Do not eject just to use it: importing keeps you on `go get -u` for
  fixes, and ejecting takes you off it. Upstream fixes reach an ejected copy only
  when you run `diff` and merge them by hand. Owning the source moves no security
  boundary: a vendored sandboxed plugin is still opaque-origin sandboxed and
  still talks over the same versioned `postMessage` bridge; `geomap` and `pdf`
  still need the host CSP their docs specify.

See [`docs/eject.md`](docs/eject.md).

### Fixed — a finished redaction could look like it never finished (2026-07-26)

`redactState` reached the host only as a passenger on `docChanged` — an event
that is debounced 250 ms, gated on `document:write`, and emitted only when the
**document** changes. A status transition is none of those things. So the
terminal move to `done` arrived only if some unrelated document mutation
happened to be emitted after it, and whether one was is a race. When it lost,
the redaction had rasterized, verified and exported correctly while the host
sat on a stale `working` forever: no error, no console message, a progress UI
that never finished, and an export the user had no reason to believe existed.

This is why `e2e (webkit)` was red on `main` — the same shape as the tour
overlay bug below, engine timing deciding whether a later event rescued a state
nobody had announced. Every transition now goes through one `setRedactState`
that assigns **and** emits a dedicated `redactStateChanged`, undebounced and
independent of write capability. The assignment and the announcement being two
separate statements is precisely what let `done` be set without ever being sent.

- **The occurrences assist wedged redaction permanently.** Its button closed the
  confirm modal calling neither `onConfirm` nor `onCancel`, so the promise the
  arm/confirm flow awaits never settled and `redactBusy` stayed `true` — and the
  re-entrancy guard at the top of `armRedaction` then swallowed every later
  Apply in silence. A user who took the assist's advice lost the ability to
  redact for the rest of the session. `showConfirm` now resolves an explicit
  outcome (`confirm` / `cancel` / `added-occurrences`) so each exit is total;
  inferring it from a mutated flag is what hid this. The cancel path never
  resolved either, leaking a suspended call per dismissal.
- **The assist was unreachable from the demo, which is why nothing caught it.**
  A needle is a whole text item, never clipped to the rect, and no line in
  `sample.pdf` repeated — so the button never rendered. Page 2 now repeats page
  1's title verbatim, and a journey drives the button and then requires a
  subsequent redaction to actually complete. It fails against the previous
  bundle.


### Added — pdf 0.1.0: viewer, editor and redactor (2026-07-26)

The fourth sandboxed heavy-JS plugin, built on pdf.js (render) and pdf-lib
(write). Route `/__gofastr/plugin/pdf`, schema `pdf-v1`, demo at `/pdf`.
Bundle 2733 KB raw / 869 KB gzip.

**The sandbox is the product here, not the tax.** The framed CSP gives the
frame `connect-src 'none'`, no workers, no `blob:` and an opaque origin, so a
frame holding a confidential PDF *cannot exfiltrate it*. The host pushes the
document in over the bridge and takes produced bytes back the same way. This
plugin therefore has **no trusted-mount opt-out**, unlike richtext. Three
consequences fall out of it: download, print and clipboard-write are host
capabilities (the CSP sandbox token grants none of them in-frame); `/doc/{id}`
is fetched by the privileged host adapter, never the frame; and no code
splitting is possible, because a dynamic `import()` is a CORS-mode fetch an
opaque origin can never satisfy.

**Redaction removes content.** Pages carrying a redaction are rasterized and
embedded into a newly built document; untouched pages are `copyPages`'d
losslessly. Six checks run in-frame *before any bytes are released*, and
verification failure emits nothing. Two traps this catches that a naive
implementation would not:

- A raw substring scan is not a byte search. pdf-lib writes text as hex strings
  and packs the Info dict into a compressed object stream as UTF-16BE hex, so
  the bytes must be inflated and the tokens decoded first.
- Absence is asserted **per rect**. The same string may legitimately appear
  elsewhere, so a content-stream hit that text extraction places only on
  non-redacted pages is a warning — but it fails closed: present in the bytes
  yet extractable nowhere (invisible text) is a leak.

`/Annots` was measured surviving `copyPages` with a planted secret, so redact
mode strips annotations by default. A black-rectangle "redaction" is kept as a
regression test, proven to still leak three ways with the verifier rejecting
it, and the shipped pipeline's output was judged by an independent
implementation of the same six checks.

**Scanned documents would have rendered blank.** pdf.js decodes JPEG 2000 and
JBIG2 — the codecs real scans use, and scans are what people redact — through
WebAssembly, which cannot instantiate under the framed CSP at all; its pure-JS
fallbacks are reached by a dynamic `import()` an opaque origin cannot satisfy
either. Both paths dead meant a **blank white page with no error, no console
message and no CSP violation**. `pdf/js/build.mjs` rewrites that one dynamic
import into a static dispatcher over the inlined fallbacks, asserted at build
time so a pdfjs-dist upgrade fails loudly rather than silently restoring blank
scans. No gofastr core change was required.

Mode (`view` / `annotate` / `redact`) is a host decision enforced on both sides
of the bridge; `ModeRedact` requires the optional `pdf:export` capability and
panics at construction without it. Capability denial answers 403 on every route
— `protocol-v1.md` prose says 412, but `pluginhost.WriteCapabilityDenied`, which
every shipped plugin calls, writes 403; logged as an upstream thread rather than
split inside one plugin.

### Fixed — the pdf annotation editor behaved nothing like an editor (2026-07-26)

Found by driving the UI, not by reading tests — the suite was green throughout,
because every annotation assertion checked `annotationCount` or exported bytes,
never that an annotation lands where the user drew it. Position fidelity is now
the acceptance criterion, checked across zoom and rotation.

- Readiness lied: `__pdfRendered` fired ~500 ms before layout settled, with the
  page slot at the raw PDF height and a 0×0 canvas.
- Annotations painted at PDF coordinates — a drag from page-y 120 to 180 landed
  at y=719, 41 px tall for a 60 px gesture. Now dx=0, dy=0 against the gesture.
- Highlight never created anything; the stamp modal could not be closed with
  Escape; the tool stayed armed after placing, so the click meant to select what
  you just drew drew another one instead.

Selection itself was never broken — it was unreachable, because nothing was
where you left it.

### Changed — gofastr v0.38.1 → v0.46.0 (2026-07-26)

- Eight framework releases in one step. No plugin code changed: build, vet, the
  full Go suite and the WebKit + Chromium journeys all pass untouched.

  Two of the breaking changes land squarely on this repo's contract, so both were
  checked rather than assumed. The **iframe sandbox sanitizer is now an allow-list
  on both sides** (v0.45.0 flipped the Go half; v0.46.0 fixed the JS sink that
  actually sets the attribute), which breaks a manifest requesting
  `allow-popups-to-escape-sandbox`, `allow-top-navigation` or `allow-downloads` —
  ours request only `pluginhost.DefaultSandbox` (`allow-scripts`), the single
  token that keeps the frame opaque. And **`Manifest.Entry` must now be a
  same-origin absolute path**, dual-enforced in Go and the JS broker; mermaid's
  and monaco's entries already were, and `Validate()` runs at registration, which
  the suite exercises.

- `frameworkCompat` stays at `>=0.28.0`, verified rather than carried forward:
  the repo still builds against v0.28.0, v0.38.1 and v0.42.0. The field remains a
  best-effort build floor, not a tested runtime matrix.

### Fixed — the tour overlay showed before it was positioned (2026-07-25)

- `showStep` made the overlay visible and only then deferred the **first**
  `position()` until after `scrollIntoView` plus two animation frames. Until it
  ran, the scrim and cutout carried no geometry — the cutout spanned the whole
  viewport instead of hugging its target. On a fast machine that is a sub-frame
  flash; on a loaded CI runner it was a visibly misplaced spotlight on every
  step, and it is why `e2e (webkit)` had been red on `main` since the tour plugin
  landed while passing on every developer's machine.

  Positioning now happens synchronously before the frame wait, which still
  re-runs afterwards to pick up the smooth scroll. The regression guard asserts
  the invariant instead of racing it: drain microtasks without ever yielding an
  animation frame, and require the cutout to already hug its target.

### Changed — `optionalCapabilities` in the registry index (2026-07-25)

- `plugins.json` rows can now declare grants a plugin takes on **only when the
  host opts into the feature that needs them** — geomap's `geocode:search`
  appears solely under `WithSearch`, along with the route it gates. Someone
  deciding whether to adopt a plugin needs the difference between "this can reach
  the network" and "this can reach the network if you switch it on", and folding
  both into `capabilities` erased it. Modelled in `internal/registry` with a
  guard (mutation-tested) that an optional grant may not repeat an always-on one.

- Corrected two stale claims in the index's own `$comment`/`note`: it pointed
  maintainers at a sibling `./registry` Go module versioned via `registry/vX.Y.Z`
  tags. Neither exists — the parser is `internal/registry` (test-only) and the
  root module is versioned by this repo's own tags. The comment is instructions
  to whoever edits the file next, and it was sending them to the wrong place.

### Added — geomap 0.3.0: pin editing, search, clustering (2026-07-25)

- **The pin popup is a live editor.** A label input that writes through to the
  canonical doc plus a per-pin Delete button, replacing the static text popup;
  `removeMarker(id)` / `setMarkerLabel(id, label)` join the controller API, and
  read-only re-gates already-open popups in place rather than only blocking new
  pins.

  Building this surfaced a bug that had shipped silently: MapLibre toggles a
  marker's popup from the **map's** click event, so the marker click handler's
  `stopPropagation()` had been disabling every popup since the plugin landed.
  Nothing tested popups, so nothing caught it. The runtime now lets the event
  reach the map and ignores map clicks targeting a marker or popup instead.

- **Geolocate + scale controls**, on by default, off via
  `WithoutGeolocateControl` / `WithoutScaleControl`.

- **Opt-in place search** (`WithSearch`) — an in-map search box backed by a new
  **same-origin** proxy at `/__gofastr/plugin/map/geocode`, gated on a new
  `geocode:search` capability and registered only when search is enabled. The
  browser never calls a geocoder directly: proxying is what allows a
  policy-compliant identifying `User-Agent`, an application-wide 1 req/s limit
  (Nominatim's cap is per-application, so only the server can hold it), the
  caching that policy asks for, and a host page CSP that stays at
  `connect-src 'self'`. `WithGeocoder` swaps the lookup wholesale;
  `WithGeocodeEndpoint` points at a self-hosted instance. A failed lookup (502)
  is deliberately distinct from an empty result set. The example app wires a
  fixed offline dataset — the e2e journeys must not depend on a third party, and
  a demo has no business spending donated geocoding capacity.

- **Opt-in marker clustering** (`WithClustering`). Clusters are computed by a
  `cluster: true` GeoJSON source but rendered as **DOM markers**, so individual
  pins stay draggable and editable, no style glyphs are needed, and bubbles
  theme from the same tokens. Two MapLibre constraints are now pinned in code
  and docs: a source with no layers is never tiled (so `querySourceFeatures`
  returns nothing forever — a transparent probe layer keeps it alive), and
  `isStyleLoaded()` is a "no pending work" flag that flickers false while a
  vector style streams, so gating source creation on it leaves clustering
  permanently inactive.

- **Twelve new e2e journeys** across WebKit + Chromium covering rename/delete
  persistence, read-only popup gating, drag persistence, the style switcher and
  host-theme flip, the toolbar add/clear/reset/load paths, control presence,
  search (hit and no-match), and clustering fold/expand.

### Changed — the repo now stands on its own (2026-07-16)

- **Builds from a published GoFastr.** Dropped the `replace` directive pointing
  at a local `/Users/.../gofastr` checkout and pinned
  `github.com/DonaldMurillo/gofastr v0.28.0`. A fresh clone now builds with
  `go build ./...` and nothing else; previously it built only on a machine with
  a sibling checkout at exactly the right path. For the local two-repo edit
  loop, use a gitignored `go.work` (see README) — never a committed `replace`.
  Check a change the way a consumer sees it with `GOWORK=off go build ./...`.

- **`frameworkCompat`** raised to `>=0.28.0` on both plugins, and the
  `plugins.json` note corrected: it claimed development against "the v0.20.0
  local checkout via a replace directive", which is no longer how this builds.

### Added

- **`internal/registry`** — the schema of `plugins.json` plus the tests that
  keep it honest. The index is consumed by **copy, not import**: a host fetches
  the file from a release and vendors it, so the published artifact is the JSON
  and this repo exposes no Go API for it — nothing to import means no module
  cycle (GoFastr would otherwise import a repo that imports GoFastr) and no path
  by which chromedp or an embedded bundle reaches the core's `go.mod`.

  Because the index is hand-maintained, a stale row is invisible to every other
  test. The guards: rows must cover exactly the plugin packages present, each
  `routePrefix` must equal that package's own const, no row may request
  `allow-same-origin`, required fields must be non-empty, and the parser rejects
  unknown fields (a new JSON key must reach the structs in the same change
  rather than vanishing from every generated page). Each was mutation-tested to
  bite. The anchor is the `Name`/`RoutePrefix` consts, not a
  `pluginhost.Manifest` literal — richtext predates the extraction and declares
  none.

  Bump `registryVersion` on a breaking field change — vendored copies are then
  stale.

- **Releases** (`.github/workflows/release.yml`) — tagging `vX.Y.Z` now
  publishes a GitHub release with `plugins.json` attached, which is how hosts
  get the index:

  ```sh
  curl -fsSL -O https://github.com/DonaldMurillo/gofastr-plugins/releases/download/v0.1.0/plugins.json
  curl -fsSL -O https://github.com/DonaldMurillo/gofastr-plugins/releases/latest/download/plugins.json
  ```

  The workflow re-runs the index guards first, so a broken index fails the
  release instead of reaching a host, where it would stay invisible until a page
  rendered wrong.

  It also **stamps provenance into the published copy** — `release.tag`,
  `.commit`, `.published`, `.source` — closing the gap that makes copying risky:
  a vendored `plugins.json` otherwise cannot say how old it is, and a year-old
  one looks exactly like a fresh one. The file in git carries no stamp, and a
  test enforces that (a committed stamp would be false immediately).

- **LICENSE** — MIT, matching GoFastr. The repo is public and previously shipped
  without one, which left its terms undefined.
- **CI** (`.github/workflows/ci.yml`) — builds, vets, and tests the module, and
  runs the Playwright journeys in WebKit + Chromium. Also gates bundle
  freshness: `richtext/assets` is committed prebuilt and served by `go:embed`,
  so a source edit without a rebuild ships stale JS that no Go test would catch.

### Added — full editor feature set (2026-07-14)

- **Persistent formatting toolbar** — sticky bar with undo/redo, a block-type
  dropdown (Text/H1-3/Quote/Code/lists), B/I/U/S/inline-code, link, text color
  + highlight, alignment, and clear-formatting. Plugin-driven active states;
  keyboard-operable; configurable via `uiPlugins({ toolbar: false })`. Also the
  mobile undo path (no ⌘Z on touch keyboards).
- **Text alignment** — left/center/right/justify on paragraphs + headings
  (schema `align` attr, editor toDOM + SSR parity, toolbar buttons).
- **Word/character count** status bar below the editable (debounced, off the
  keystroke path).
- **Smart paste** — plain text that looks like markdown pastes as real blocks
  (via the markdown parser); a URL pasted over a selection wraps it in a link;
  image paste and HTML paste are preserved.
- **Find & replace** (Mod-F) — match highlighting via decorations, next/prev,
  replace one, replace all, case toggle, live match counter.
- **Table controls** — floating add/remove row+column, header-row toggle, and
  delete-table on cell focus; Tab at the last cell appends a row.
- **Code block language selector** — a dropdown on the focused code block.
- **Code syntax highlighting (2026-07-15)** — once a language is set, code
  blocks tokenize into `comment`/`string`/`number`/`keyword`/`function` spans in
  BOTH the live editor (ProseMirror inline decorations, `js/src/highlight.ts` +
  `js/src/codehighlight.ts`) and the no-JS read view (`ssr/highlight.go` +
  `renderCodeBlock`). No new JS/CSS dependency — a small config-driven lexer is
  hand-mirrored across the two languages and pinned by a shared parity fixture
  (`richtext/highlight-cases.json`) that both test suites assert against, so the
  editor and read view can't drift. Theme via overridable `--richtext-hl-*`
  tokens (GitHub-primer light defaults), defined identically in
  `frame/editor.css` and `ssr/style.go`. Supported: js/ts, go, python, rust,
  json, css, sql, bash, html (+ aliases); unknown languages render plain.

### Added — Phases 1–3 complete (2026-07-13)

- **Trusted in-page mount (the sandbox opt-out)** — `richtext.WithTrustedMount()`
  serves `editor-inline.js` (`window.__gofastrRichText.mountTrusted`), a
  page-scoped stylesheet (`editor-scoped.css`, rescoped under
  `.gofastr-richtext-trusted`; framework-token fallbacks dropped so host tokens
  inherit, plugin-local `--richtext-*` slot tokens kept), and a frameless demo
  at `/__gofastr/plugin/richtext/trusted`. Same protocol envelopes over a
  swapped transport (`protocol.setTransport` + `routeEnvelope`). Host-side
  opt-in only — never a default, never plugin-selectable (docs/DECISIONS.md
  "secure by default, opt out").
- **Platform extracted into gofastr core (Phase 2)** — the proven `pluginhost`
  package + host broker now live at `gofastr/framework/pluginhost`;
  `pluginhost/` here is a thin compatibility alias. `data-fui-plugin*` markers
  registered in core's ARCHITECTURE/runtime-contract docs.
- **User-journey e2e suite** (`e2e/`, Playwright, strict TypeScript) — 13
  journeys driven like a person (real clicks/taps/typing) against BOTH mounts
  in WebKit + Chromium, plus mobile gates (iPhone/Pixel touch) and an axe
  a11y gate (zero serious/critical across framed/trusted/SSR + open menus).
  Any console/page error fails a test. Dogfood shots: `npm run shots`
  (light/dark × desktop/mobile × framed/trusted/SSR).
- **TypeScript strict everywhere Node runs** — `richtext/js`, `mermaid/js`,
  `e2e/` all migrated; `tsc --noEmit` gates every build.

### Fixed

- Slash-menu selection (hover rebuild loop destroyed the element under the
  cursor; heading items double-invoked their command), Enter-in-list splitting
  the paragraph instead of the item, bubble-toolbar buttons throwing on every
  click, Safari CSP refusing frame subresources (`'self'` is `null` in an
  opaque frame — origin-keyed CSP + inlined styles), overlay clipping at the
  iframe edge (overlay-aware frame autosize), the frame-height ratchet
  (content-extent measurement), menu dismissal (outside click/tap + frame
  blur + `hostPointerdown` broker relay for iOS), and slash-menu keyboard
  scroll (active item kept in view; hover moved to `mousemove` so it can't
  fight arrow keys).

### Added — the Phase-0 build

The isolation spike is built and verified end to end. The opaque-origin sandboxed
iframe + versioned `postMessage` RPC is a usable, secure editing surface, and the
platform machinery that proved it is now extracted into a reusable package.

- **`pluginhost` — the platform** (`pluginhost/`). The reusable, plugin-agnostic
  host glue distilled out of the editor so a second heavy-JS plugin can reuse it:
  - `Manifest` / `ClientModule` — declarative client-module description;
    `Validate()` enforces the v1 invariants (no `allow-same-origin`, requires
    `allow-scripts`) so a mis-configured plugin fails loudly at construction.
  - `AssetServer` — serves embedded assets with correct Content-Types AND the
    framing/CORP/CSP header relaxation GoFastr's global security middleware
    otherwise blocks (the client-side isolation contract).
  - `Allow` — the capability gate reusing `battery/auth`'s `resource:verb`
    scopes (`auth.HasScope`).
  - `MountMarker` / `MountConfig` — the generic `data-fui-plugin*` mount marker
    + hidden-field HTML the generic broker scans for.
  - `RegisterBrokerRoute` / `UIHostOption` — serving + injecting the generic
    host broker (`host/pluginhost.js`), idempotent across plugins.
  - See [`docs/plugin-platform.md`](docs/plugin-platform.md).

- **`richtext` — the Rich Text editor** (`richtext/`). ProseMirror block editor,
  block-JSON canonical, markdown export + the pure-Go SSR read view (`richtext/ssr`).
  Plugin #1 and the forcing function that proved the third-party contract.
  See [`docs/richtext-editor.md`](docs/richtext-editor.md).

- **`richtext/ssr` — the SSR read renderer** (`richtext/ssr/`). Pure, deterministic
  Go `Render(doc map[string]any) → render.HTML` / `RenderJSON(docJSON)`, the
  server-side dual of the in-frame editor. Both implement the single schema in
  [`docs/design/schema-v1.md`](docs/design/schema-v1.md). Token-only HTML; unknown
  node/mark types degrade gracefully (forward-compatible).

- **`mermaid` — the second plugin** (`mermaid/`). An isolated Mermaid diagram
  editor/renderer — the completeness canary proving the extracted `pluginhost`
  platform generalizes beyond the editor. See [`docs/mermaid.md`](docs/mermaid.md).

- **`example` — the integration host** (`example/`). One GoFastr app that imports
  and mounts every plugin, serving both demos. The visual/e2e test surface and
  the completeness canary. Run with `go run ./example`.

- **Registry** — [`plugins.json`](plugins.json), the curated index (a
  convention, not a service): module path, version, isolation, capabilities,
  route prefix, schema, per plugin. Hosts fetch and vendor the file to generate
  a page per plugin.

- **Docs** — [`docs/plugin-platform.md`](docs/plugin-platform.md) (isolation +
  capability protocol + trust tiers + header/CSP contract + #37 relation +
  quickstart), [`docs/richtext-editor.md`](docs/richtext-editor.md),
  [`docs/mermaid.md`](docs/mermaid.md).

### Isolation / latency gate — CLEARED (2026-07-12)

The Phase-0 go/no-go gate (measured **p99 keystroke latency ≤ 16 ms** inside the
frame) is **PASS**:

- **p50 = 3.5 ms, p99 = 8.6 ms** (target ≤ 16 ms). All editing stays in-frame;
  the boundary carries only coarse events.
- Isolation proven from both sides: `sandbox="allow-scripts"` (no
  `allow-same-origin`), `iframe.contentDocument === null` from the parent, and
  in-frame probes confirm `document.cookie` / `localStorage` / `parent.document`
  are all unreachable.
- Round-trip (type → `docChanged` → host hidden fields), theme-token bridge
  (light/dark re-sync), and autosize all verified in `example/smoke_test.go`.

Load-bearing gotchas discovered (fed into `pluginhost`):
1. GoFastr's global security middleware sends `X-Frame-Options: DENY`, CSP
   `frame-ancestors 'none'`, and `Cross-Origin-Resource-Policy: same-origin` on
   every response — which blocks framing the editor AND blocks the opaque frame
   from fetching its own JS/CSS. `AssetServer` overrides these on framed assets
   (CSP `frame-ancestors 'self'` supersedes XFO; CORP `cross-origin`). See
   [`docs/DECISIONS.md`](docs/DECISIONS.md).
2. The frame CSP allows `style-src 'self' 'unsafe-inline'` (ProseMirror inline
   styles + the theme bridge `<style>:root{…}` block); `script-src` stays
   `'self'`.
3. `EditorState.create` needs an explicit `schema` when there's no initial doc.
4. Driving input into an opaque OOPIF via chromedp requires disabling site
   isolation in the test browser (a harness concern only).

### Notes

- The repo depends on a local GoFastr checkout via a `replace` directive
  (`../gofastr`), developed against gofastr `v0.20.0`. No published module
  version exists yet.
- Frozen design records: [`docs/PLAN.md`](docs/PLAN.md),
  [`docs/DECISIONS.md`](docs/DECISIONS.md), [`docs/design/`](docs/design/).
