# Monaco code-editor plugin

A configurable [Monaco](https://microsoft.github.io/monaco-editor/) (the VS Code
editor) plugin, mounted the same way as the richtext and mermaid plugins: inside
an **opaque-origin sandboxed iframe**, talking to the host only over the
versioned postMessage bridge. It is the third sandboxed heavy-JS plugin and the
proof that the platform generalizes to a large third-party editor.

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/monaco`
- **Route prefix:** `/__gofastr/plugin/monaco`
- **Isolation:** `sandbox-iframe-opaque` (`allow-scripts`, no `allow-same-origin`)
- **Canonical doc (schema `monaco-v1`):** `{code, language}`
- **Capabilities:** `document:read`, `document:write`, `theme:read`

## Mounting

```go
app.RegisterPlugin(monaco.New(
    monaco.WithDevGrantAll(), // demo/dev only — opens the capability gate
    monaco.WithDemoPage(),    // serves the themed demo at /monaco
))
```

Persist with `monaco.WithSaveHandler(func(ctx, req) error { ... })`. Return
`monaco.ErrConflict` to signal an optimistic-concurrency conflict — it maps to
HTTP 409 (`E_CONFLICT`), which the host adapter relays back to the frame as a
`saveResult` so the editor warns rather than silently dropping the edit (the same
contract as richtext).

## Configuration

Editor behaviour is configurable per-instance via `With…` options (language,
theme, read-only, minimap, word-wrap, line-numbers, font-size) and a **diff
editor** mode. Config is marshalled at `Init` and delivered to the frame in
`init.config`.

## Web workers under the opaque origin

Monaco normally spawns web workers for language services. Under
`sandbox="allow-scripts"` without `allow-same-origin` the frame is on an opaque
origin, where worker construction is restricted. The plugin therefore **boots
worker-free by default**: syntax highlighting runs on the main thread via the
monarch tokenizers, which is browser-verified to boot cleanly in both WebKit and
Chromium. Richer language services (completions/diagnostics) require workers and
are an explicit opt-in (`config.workers`); if the sandbox refuses the worker the
frame falls back to the worker-free path rather than throwing.

## Bundle size

`editor.js` is multi-megabyte (Monaco is large). It is served at its own route
and is deliberately **not** subject to the core-ui runtime budget.
