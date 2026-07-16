# Mermaid diagram plugin (`mermaid`)

An isolated Mermaid diagram editor/renderer — the **second** heavy-JS plugin and
the **completeness canary**: it exists to prove the extracted
[`pluginhost`](plugin-platform.md) platform generalizes beyond the editor, rather
than being shaped around it.

- **Identity:** `Name = "mermaid"`, `Version = "0.1.0-phase0"`,
  `RoutePrefix = "/__gofastr/plugin/mermaid"`, `SchemaVersion = "mermaid-v1"`.
- **Module path:** `github.com/DonaldMurillo/gofastr-plugins/mermaid`.

## What it is

A Mermaid source editor that renders diagrams inside the same opaque-origin
sandboxed iframe as the Rich Text editor (`sandbox="allow-scripts"` without
`allow-same-origin`), reusing every platform piece: `pluginhost.AssetServer`,
`pluginhost.Allow`, `pluginhost.MountMarker`, `pluginhost.RegisterBrokerRoute`,
and the generic broker. The only plugin-specific code is the Go wiring
(`New` / `Init` / `Mount` / `handleSave`), the thin JS adapter
(`host/adapter.js`) that registers with the generic broker, and the framed
client assets (`assets/diagram.{html,js,css}`).

## Document model

- **Canonical** = `{ "source": "<mermaid text>" }`, `schemaVersion = "mermaid-v1"`.
- A single hidden input (`diagram_source`) is synced on `docChanged`, so a form
  POST round-trips the source text.
- There is **no upload path** — mermaid has no `upload:images` capability.

## Capabilities used

`DefaultCapabilities`: `document:read`, `document:write`, `theme:read`. The
`POST /save` route gates on `document:write` via `pluginhost.Allow` →
`auth.HasScope`. See [`plugin-platform.md`](plugin-platform.md#the-capability-model).

## How to mount it

```go
import "github.com/DonaldMurillo/gofastr-plugins/mermaid"

app.RegisterPlugin(mermaid.New(
    mermaid.WithDemoPage(),   // serve the self-contained demo at "/mermaid"
    // mermaid.WithDevGrantAll(),            // Phase-0 demo only — bypasses auth.HasScope
    // mermaid.WithCapabilities(...),        // override the grant set
    // mermaid.WithSaveHandler(fn),          // default: in-memory map keyed by DocID
))
```

`New` builds and `Validate()`s a `pluginhost.Manifest` so a bad isolation/sandbox
config aborts construction rather than silently de-opaquing the frame at runtime.
`Init` registers the broker route, the framed diagram assets, the adapter, and
the `POST /save` route. Drop the mount marker into a form:

```go
mermaid.Mount(mermaid.MountConfig{
    DocID:       "demo",            // persistence key
    SourceField: "diagram_source",  // hidden input for the mermaid text
    MinHeight:   "320px",           // initial iframe height before first resize
    Doc:         initialDoc,        // optional initial {source:"..."} JSON
})
```

Apps rendering through a `UIHost` inject the host scripts with
`mermaid.UIHostOption()` — platform broker first, then this plugin's adapter:

```go
uihost.New(..., mermaid.UIHostOption())
```

## Co-mounting with the editor

The mermaid demo lives at `/mermaid` (not `/`, which the richtext demo owns), so
both plugins co-mount in the same app — which is exactly what the
[`example`](../example/main.go) app does. That an unrelated second plugin mounts
cleanly on the shared platform is the canary: a plugin that cannot mount here is
a platform gap, not a plugin bug.
