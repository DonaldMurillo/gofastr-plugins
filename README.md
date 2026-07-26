# gofastr-plugins

Officially-maintained **heavy-JavaScript plugins** for the
[GoFastr](https://github.com/DonaldMurillo/gofastr) framework — the ones that
are too big (or too client-heavy) to live in the SSR-thin, Go-pure core.

Keeping them here means the core keeps its ≤12 KB runtime budget and its
minimal-dependency posture, while apps that want a fat client feature (a
Rich Text editor, etc.) opt in with a single `go get`.

## Why a separate repo

- The GoFastr core stays pure Go with a tiny runtime. Heavy JS never enters its
  `go.mod` or its JS budget.
- Each plugin embeds its **prebuilt** JS bundle via `go:embed` and serves it
  **same-origin** (CSP-clean). No CDN, no external `<script>`.
- Plugins are treated as **genuinely third-party** even though first-party-
  maintained: they run isolated (sandboxed iframe), talk to the host only over a
  versioned `postMessage` capability bridge, and cannot reach host cookies, the
  DB, or the host DOM. See [`docs/PLAN.md`](docs/PLAN.md).

## Layout

```
gofastr-plugins/
├── pluginhost/   the reusable platform: Manifest, AssetServer, Allow,
│                 MountMarker, broker — distilled from the editor
├── richtext/      the ProseMirror block editor plugin (#1) + ssr/ read renderer
├── mermaid/      the Mermaid diagram plugin (#2, the completeness canary)
├── pdf/          the PDF viewer/editor/redactor — the plugin whose cage IS
│                 the product: connect-src 'none' means a document opened for
│                 redaction cannot be exfiltrated by the frame that edits it
├── example/      ONE gofastr app that imports & mounts every plugin —
│                 the integration host, visual/e2e surface, completeness canary
├── plugins.json  the curated registry index (convention, not a service) —
│                 hosts fetch and vendor this file; see "The registry" below
├── docs/         PLAN.md, DECISIONS.md, design/ (frozen) + platform/plugin docs
└── CHANGELOG.md
```

## The registry

[`plugins.json`](plugins.json) at the repo root is the curated index — a
convention, not a service. There is no runtime discovery: apps import a plugin
package directly via its `modulePath` and mount it with
`framework.RegisterPlugin`.

It is **consumed by copy, not by import**. A host — GoFastr's docs site first —
fetches the file at deploy time, vendors it, and generates a page per plugin
from it. The published artifact is the JSON, so this repo deliberately exposes
no Go API for it: nothing to import means no module cycle (GoFastr would
otherwise be importing a repo that imports GoFastr) and no route by which
chromedp or an embedded JS bundle reaches the core's `go.mod`.

Tagging `vX.Y.Z` publishes a release with `plugins.json` attached
([`.github/workflows/release.yml`](.github/workflows/release.yml)), so hosts
fetch it from a stable URL:

```sh
# Pinned to a known release — what a reproducible deploy should use.
curl -fsSL -O https://github.com/DonaldMurillo/gofastr-plugins/releases/download/v0.1.0/plugins.json

# Or always the newest release.
curl -fsSL -O https://github.com/DonaldMurillo/gofastr-plugins/releases/latest/download/plugins.json
```

The **published** copy carries a `release` stamp (tag, commit, timestamp,
source) that the file in git does not — the release workflow adds it on the way
out. That is what keeps a vendored copy honest: without it, a year-old
`plugins.json` sitting in a host repo looks exactly like a fresh one. A test
asserts the committed file has no stamp, since a hand-written one would be a lie
the moment it landed.

The index is hand-maintained, so `internal/registry` guards the drift nothing
else would catch: rows must cover exactly the plugin packages in the repo, each
`routePrefix` must equal that package's own const, no row may request
`allow-same-origin`, and the parser rejects unknown fields — a new key must
reach the structs in the same change rather than being silently dropped. Those
guards run again inside the release workflow, so a broken index cannot reach a
host. Bump `registryVersion` on a breaking field change, since vendored copies
are then stale.


## Develop

This repo pins a **published** GoFastr release (`require github.com/DonaldMurillo/gofastr vX.Y.Z`),
so a fresh clone builds with nothing but `go build ./...` — no sibling checkout
required.

To edit both repos in one loop, use a Go workspace instead of a committed
`replace` directive. `go.work` is gitignored, so your local wiring never leaks
into what everyone else builds:

```sh
go work init . ../gofastr    # once
go run ./example
```

Verify a change the way a consumer sees it — resolving GoFastr from the module
proxy rather than your checkout:

```sh
GOWORK=off go build ./...
```

## Testing

Two layers, because this project is UI-heavy and engine differences are real
(several shipped bugs were Safari-only and invisible to Chrome-based tests):

- **Go suite** (`go test ./...`) — unit + integration + headless-Chrome checks
  of isolation, latency, round-tripping, and rendering.
- **User-journey e2e** (`cd e2e && npm test`) — Playwright drives the editor
  the way a person does (real clicks, typing, slash-menu selection, toolbar)
  in **WebKit (Safari's engine) and Chromium**, against **both mounts**: the
  default sandboxed iframe and the explicit trusted in-page opt-out
  (`richtext.WithTrustedMount()`, demo at `/__gofastr/plugin/richtext/trusted`).
  Any console/page error fails the test. `npm run test:headed` watches a run
  live.

## Status

Phase 0 — the isolation spike — is **built and the gate cleared**: an opaque-
origin sandboxed ProseMirror measured **p99 keystroke latency 8.6 ms** (target
≤ 16 ms), proven isolated from both sides. The platform glue is extracted into
`pluginhost`, the editor + a pure-Go SSR read view ship, and a second plugin
(`mermaid`) proves the platform generalizes. See [`CHANGELOG.md`](CHANGELOG.md)
and [`docs/`](docs/).
