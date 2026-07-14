# gofastr-plugins

Officially-maintained **heavy-JavaScript plugins** for the
[GoFastr](https://github.com/DonaldMurillo/gofastr) framework — the ones that
are too big (or too client-heavy) to live in the SSR-thin, Go-pure core.

Keeping them here means the core keeps its ≤12 KB runtime budget and its
minimal-dependency posture, while apps that want a fat client feature (a
WYSIWYG editor, etc.) opt in with a single `go get`.

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
├── wysiwyg/      the ProseMirror block editor plugin (#1) + ssr/ read renderer
├── mermaid/      the Mermaid diagram plugin (#2, the completeness canary)
├── example/      ONE gofastr app that imports & mounts every plugin —
│                 the integration host, visual/e2e surface, completeness canary
├── plugins.json  the curated registry index (convention, not a service)
├── docs/         PLAN.md, DECISIONS.md, design/ (frozen) + platform/plugin docs
└── CHANGELOG.md
```

## Develop

The `example` app depends on a local checkout of GoFastr via a `replace`
directive (`../gofastr`). Run it:

```sh
go run ./example
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
  (`wysiwyg.WithTrustedMount()`, demo at `/__gofastr/plugin/wysiwyg/trusted`).
  Any console/page error fails the test. `npm run test:headed` watches a run
  live.

## Status

Phase 0 — the isolation spike — is **built and the gate cleared**: an opaque-
origin sandboxed ProseMirror measured **p99 keystroke latency 8.6 ms** (target
≤ 16 ms), proven isolated from both sides. The platform glue is extracted into
`pluginhost`, the editor + a pure-Go SSR read view ship, and a second plugin
(`mermaid`) proves the platform generalizes. See [`CHANGELOG.md`](CHANGELOG.md)
and [`docs/`](docs/).
