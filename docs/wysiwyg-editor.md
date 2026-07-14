# WYSIWYG editor plugin (`wysiwyg`)

A full WYSIWYG block editor for GoFastr, built on **ProseMirror** and delivered
as a genuinely third-party, isolated heavy-JS plugin. It is **plugin #1** — the
forcing function that proved the third-party isolation contract documented in
[`plugin-platform.md`](plugin-platform.md) and [`design/protocol-v1.md`](design/protocol-v1.md).

- **Identity:** `Name = "wysiwyg"`, `Version = "0.1.0-phase0"`,
  `RoutePrefix = "/__gofastr/plugin/wysiwyg"`, `SchemaVersion = "wysiwyg-v1"`.
- **Module path:** `github.com/DonaldMurillo/gofastr-plugins/wysiwyg`.

## What it is

A Notion/Confluence-class block editor whose editing surface runs inside an
opaque-origin sandboxed iframe (`sandbox="allow-scripts"` without
`allow-same-origin`). The host page and the editor frame communicate **only** over
the versioned `postMessage` capability bridge — the editor cannot reach host
cookies, `localStorage`, the CSRF token, or the host DOM. See
[`plugin-platform.md`](plugin-platform.md) for the isolation model.

## The block set

Defined by the single anti-drift schema in [`design/schema-v1.md`](design/schema-v1.md),
implemented by **both** renderers (the in-frame ProseMirror `Schema` and the
pure-Go SSR renderer). A block exists in v1 only if both handle it.

**Nodes:** `doc`, `paragraph`, `heading` (1–6), `blockquote`, `code_block`
(with `language`), `divider`, `bullet_list`, `ordered_list`, `list_item`,
`task_list` / `task_item` (checkbox), `callout` (info/warn/success/danger/note),
`toggle` / `toggle_summary` / `content` (collapsible), `columns` / `column`
(2 or 3), `image` (via `upload:images`), and the `prosemirror-tables` model
(`table` / `table_row` / `table_header` / `table_cell`).

**Marks:** `strong`, `em`, `code`, `strike`, `underline`, `link`,
`textColor`, `bgColor`. Color values are **named palette slots** mapping to
design tokens (`var(--wysiwyg-fg-<name>)`), never raw hex — so colored text
recolors with the theme (Hard Rule 7, see schema §3).

## The document model

- **Canonical** = ProseMirror doc JSON, `{ "type": "doc", "content": [ … ] }`,
  stored by the host as an **opaque blob**. `schemaVersion = "wysiwyg-v1"`.
- **Markdown export** is emitted alongside every `docChanged` / `save`. It is a
  **lossy projection** (schema §4): the representable core (paragraphs,
  headings, lists, code fences, tables-as-GFM, links) round-trips losslessly;
  `callout`/`toggle` degrade to HTML-passthrough; `columns` flatten; color marks
  drop to plain text. Full fidelity always remains in the JSON.
- **Form integration:** two hidden inputs (`body_json`, `body_md`) are synced on
  `docChanged`, so a normal form POST / `data-fui-rpc` submit round-trips both
  the canonical JSON and the portable markdown.

## SSR read view (`wysiwyg/ssr`)

The server-side dual of the in-frame editor: pure, deterministic Go that walks
the stored JSON and emits semantic, design-token HTML for the **no-JS first
paint** — real content, SEO-safe, hydrate-swappable by the editor iframe.

```go
docJSON, _ := store.Load(ctx, id)
html, err := ssr.RenderJSON(docJSON)   // or ssr.Render(doc map[string]any)
render.RespondHTML(w, r, html)
```

- The stylesheet auto-loads via `registry.RegisterStyle("wysiwyg-read", …)` —
  the runtime loads its CSS the first time a `data-fui-comp="wysiwyg-read"`
  marker appears, so no manual `<link>` is needed.
- The only inline styles are per-node color-slot assignments of the form
  `style="color:var(--wysiwyg-fg-<slot>,var(--color-<token>))"` — token refs
  only, never a literal color.
- Unknown node/mark types render their `content` (or nothing) rather than
  throwing — forward-compatible with a newer editor schema.
- `Render` never panics on malformed input; `RenderJSON` returns an error only
  when the JSON itself is unparseable.

## How to mount it

```go
import "github.com/DonaldMurillo/gofastr-plugins/wysiwyg"

app.RegisterPlugin(wysiwyg.New(
    wysiwyg.WithDemoPage(),    // serve the self-contained themed demo at "/"
    // wysiwyg.WithDevGrantAll(),            // Phase-0 demo only — bypasses auth.HasScope
    // wysiwyg.WithCapabilities(...),        // override the grant set
    // wysiwyg.WithSaveHandler(fn),          // default: in-memory map keyed by DocID
    // wysiwyg.WithUploadHandler(fn),        // default: data: URL echoing the bytes
))
```

`New` returns a `framework.Plugin` (`Name` / `Init`). `Init` registers the
generic platform broker route (idempotent), the framed editor assets via
`pluginhost.AssetServer`, the broker adapter, and the `POST /save` +
`POST /upload` RPC routes. Drop the mount marker into any form:

```go
wysiwyg.Mount(wysiwyg.MountConfig{
    DocID:     "demo",      // persistence key (default "demo")
    JSONField: "body_json", // hidden input for canonical block-JSON
    MDField:   "body_md",   // hidden input for markdown export
    MinHeight: "240px",     // initial iframe height before first resize
    Doc:       initialDoc,  // optional initial doc JSON (reload round-trip)
})
```

Apps rendering through a `UIHost` inject the host scripts with
`wysiwyg.UIHostOption()` — the platform broker first, then this plugin's adapter
(order matters; the adapter registers with the broker the former defines):

```go
uihost.New(..., wysiwyg.UIHostOption())
```

## Capabilities used

Default grant set (`DefaultCapabilities`): `document:read`, `document:write`,
`upload:images`, `theme:read`. Each RPC route gates on the matching scope via
`pluginhost.Allow` → `auth.HasScope`. `navigation:intercept` is deferred to
Phase 1. See [`plugin-platform.md`](plugin-platform.md#the-capability-model).

## Phase-0 latency result — gate CLEARED

The go/no-go gate (measured **p99 keystroke latency ≤ 16 ms** inside the frame)
was cleared on 2026-07-12 (see [`DECISIONS.md`](DECISIONS.md) "Phase 0 — DONE"):

| metric | value | target |
|---|---|---|
| p50 keystroke latency | **3.5 ms** | — |
| p99 keystroke latency | **8.6 ms** | ≤ 16 ms |

All editing stays in-frame; the postMessage boundary carries only coarse events
(`docChanged`, `resize`, …), never per-keystroke traffic. The measurement rig
(`beforeinput` → next `requestAnimationFrame` sample, ≥100 keystrokes) and the
hard-fail ceiling live in `example/smoke_test.go`. Isolation was proven from
both sides: `sandbox="allow-scripts"` (no `allow-same-origin`),
`iframe.contentDocument === null` from the parent, and in-frame probes confirm
`document.cookie` / `localStorage` / `parent.document` are all unreachable.
