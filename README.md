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
├── wysiwyg/      the ProseMirror block editor plugin (plugin #1)
├── example/      ONE gofastr app that imports & mounts every plugin —
│                 the integration host, visual/e2e surface, completeness canary
└── docs/PLAN.md  the master plan
```

## Develop

The `example` app depends on a local checkout of GoFastr via a `replace`
directive (`../gofastr`). Run it:

```sh
go run ./example
```

## Status

Early scaffolding. Phase 0 (the isolation spike) is the current focus — prove a
sandboxed ProseMirror clears a p99 keystroke latency ≤16 ms bar before the full
build. See the plan.
