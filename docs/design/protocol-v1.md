# Rich Text plugin — Phase-0 contract (protocol v1)

This is the **authoritative, minimal** contract for the Phase-0 isolation spike.
It converges the two crux memos ([`isolation-crux-claude.md`](isolation-crux-claude.md),
[`isolation-crux-glm.md`](isolation-crux-glm.md)) into ONE coherent surface. Every
worker (editor bundle, host broker, Go plugin, example/tests) builds to exactly
this. If something here is ambiguous for your task, halt and report — do not
invent a second convention.

Phase-0 goal: prove an **opaque-origin sandboxed iframe + postMessage RPC** is a
*usable* editing surface. Go/no-go gate = **measured p99 keystroke latency ≤ 16 ms
inside the frame.** Scope is deliberately tiny (paragraph + heading + bold); the
full block set is Phase 1.

---

## 1. Isolation model (fixed — do not weaken)

The editor runs in:

```html
<iframe
  src="/__gofastr/plugin/richtext/editor.html?v=<bundle-hash>"
  sandbox="allow-scripts"        <!-- NO allow-same-origin. Ever. -->
  referrerpolicy="no-referrer"
  title="Rich Text editor"
></iframe>
```

- `sandbox="allow-scripts"` **without** `allow-same-origin` → the frame document
  is an **opaque origin**. It cannot read `document.cookie`, `localStorage`,
  `window.parent.document`, the CSRF token, or any host DOM. That is the D3
  third-party guarantee, structurally.
- Served **same-origin** from the plugin's own asset route (below), so the host's
  strict CSP is satisfied by `default-src 'self'` with zero core CSP edits.
- The frame has **no network capability**. Its ONLY outbound channel is a granted
  capability RPC over `postMessage`.

---

## 2. Asset routes (fixed paths — both sides hard-code these)

The Go plugin `go:embed`s its prebuilt client assets and serves them same-origin:

| URL | Serves | Content-Type |
|---|---|---|
| `/__gofastr/plugin/richtext/editor.html` | the frame document | `text/html; charset=utf-8` |
| `/__gofastr/plugin/richtext/editor.js`   | the ProseMirror bundle (IIFE) | `text/javascript; charset=utf-8` |
| `/__gofastr/plugin/richtext/editor.css`  | the editor stylesheet (token-only) | `text/css; charset=utf-8` |

- `editor.html` loads `editor.js` and `editor.css` by **relative** path
  (`./editor.js?v=…`, `./editor.css?v=…`) so it works under the route prefix.
- `?v=<hash>` is a cache-buster the host appends; the editor must not depend on
  its value.
- The **host broker JS** is a separate same-origin script injected into the host
  page via `WithExtraScripts` (see §6). It is NOT inside the frame.

---

## 3. postMessage envelope (fixed)

Every message in both directions is a single structured-clone object:

```jsonc
{
  "v": 1,                                   // envelope version; bump = breaking
  "id": "h-42",                             // correlation id (host mints "h-N", plugin "p-N")
  "type": "request" | "response" | "event", // response echoes the request's id
  "src":  "host" | "plugin",
  "method": "init",                         // present on request & event; omitted on response
  "params": { },                            // request/event payload
  "result": null,                           // response only
  "error":  null                            // response only: { "code": "...", "message": "..." }
}
```

**Source validation (both sides, mandatory):**

- Host drops any message whose `event.source !== iframe.contentWindow`. It does
  **NOT** check `event.origin` — an opaque-origin frame's origin is the literal
  string `"null"`, so origin-string checks are a trap.
- Frame drops any message whose `event.source !== window.parent`.

**Correlation:** `request` → `response` matched by `id`. `event` is
fire-and-forget (no response, may omit `id`). Requests carry a 5 s timeout on the
sender side; a timeout rejects the pending promise with `code: "E_TIMEOUT"`.

**Capability errors:** a plugin→host request whose capability was not granted gets
a `response` with `error.code = "E_CAPABILITY_DENIED"`.

---

## 4. Methods (Phase-0 set — exactly these, no more)

### Handshake

The frame boots, then announces itself; the host replies with `init`. The host
CANNOT know when frame JS finished loading, so **the plugin speaks first.**

1. frame → host: `event ready { version, schemaVersion, minHeight }`
2. host → frame: `event init { doc, markdown, tokens, scheme, capabilities, schemaVersion }`

`doc` is ProseMirror doc JSON (or `null` → start empty). `tokens` is the resolved
`--*` map (see §7). `capabilities` is the granted subset (see §5).

### host → plugin

| method | type | params | notes |
|---|---|---|---|
| `init`         | event   | `{doc, markdown, tokens, scheme, capabilities, schemaVersion}` | reply to `ready` |
| `themeChanged` | event   | `{scheme, tokens}` | on light/dark flip |
| `requestSave`  | request | `{}` → result `{doc, markdown, schemaVersion}` | host pulls current doc |
| `uploadResult` | event   | `{reqId, url}` or `{reqId, error}` | answers a `requestUpload` |
| `teardown`     | request | `{}` → result `{}` | before iframe removal on SPA nav |
| `hostPointerdown` | event | `{}` | host page saw a pointerdown OUTSIDE this frame; the frame dismisses open overlays (iOS WebKit delivers no frame blur for a host tap) |

### plugin → host

| method | type | params | capability |
|---|---|---|---|
| `ready`            | event   | `{version, schemaVersion, minHeight}` | — |
| `docChanged`       | event   | `{doc, markdown, dirty, rev}` (debounced ~300 ms) | `document:write` |
| `save`             | event   | `{doc, markdown, schemaVersion}` | `document:write` |
| `requestUpload`    | event   | `{reqId, name, type, bytes}` (`bytes` = `ArrayBuffer`) | `upload:images` |
| `resize`           | event   | `{height}` | — |
| `focusChanged`     | event   | `{focused}` | — |
| `metric`           | event   | `{name:"keystroke", p50, p99, count, samplesMs:[…]}` | — (Phase-0 instrumentation) |

- `docChanged` carries `doc`+`markdown` so the host mirrors the hidden fields
  with no round trip. `save` is the explicit/autosave persist signal.
- `requestUpload.bytes` crosses as a transferable `ArrayBuffer` (structured
  clone). The frame never fetches; the host does the authenticated upload.
- `metric` is Phase-0-only: the editor's latency rig posts it so the host/tests
  can read the measured p99. Also mirrored to `window.__richtextMetrics` inside the
  frame (see §8).

---

## 5. Capabilities (reuse the auth `resource:verb` scope grammar)

Phase-0 grant set (host declares, grants a subset at `init`):

| capability | grants | absent → |
|---|---|---|
| `document:read`   | frame receives `init.doc` | starts empty |
| `document:write`  | host accepts `docChanged`/`save` | read-only |
| `upload:images`   | `requestUpload` honored (image mime only) | paste-image disabled |
| `theme:read`      | receives `tokens` + `themeChanged` | falls back to default tokens |

These map onto GoFastr's existing `auth.HasScope`/`RequireScope` checker — the Go
handler for each RPC gates on the matching scope. (`navigation:intercept` is
deferred to Phase 1.)

---

## 6. Host broker (same-origin, injected via WithExtraScripts)

`richtext/host/broker.js` runs in the **host page** (full privileges). It:

1. Finds each mount marker `<div data-fui-richtext data-fui-richtext-for="<jsonField>,<mdField>">`.
2. Creates the sandboxed iframe (§1) inside the marker, min-height from manifest.
3. Runs the handshake: waits for `ready`, resolves the host token map (§7), sends
   `init`.
4. On `docChanged`: writes `params.doc` (JSON.stringified) into the hidden input
   named `<jsonField>` and `params.markdown` into `<mdField>` in the enclosing
   form, so a normal form POST / `data-fui-rpc` submit round-trips it.
5. On `save`: POSTs `{doc, markdown}` to the plugin's save RPC route (§ Go API).
6. On `resize`: sets `iframe.style.height`.
7. On `requestUpload`: POSTs the bytes to the plugin's upload RPC route (with the
   host's credentials/CSRF), replies `uploadResult`.
8. On theme flip (observe `data-color-scheme` on `<html>`): re-resolves tokens,
   posts `themeChanged`.
9. On `gofastr:navigate` (SPA teardown): sends `teardown`, awaits ack (≤200 ms),
   removes the iframe + listeners. No leaks.

Per-iframe state is kept in a `WeakMap` keyed by the iframe element (per the
framework's per-instance-state rule).

---

## 7. Theming across the boundary

CSS custom properties do NOT inherit across an iframe boundary, so the host
**bridges resolved token values**:

1. Host enumerates every `--*` it emits and reads the **resolved** value via
   `getComputedStyle(document.documentElement).getPropertyValue(name)`.
2. Sends the full map in `init.tokens` and again in `themeChanged.tokens`.
3. The frame writes a single `<style>:root{ --x:…; --y:…; }</style>` block into its
   OWN document head. The editor CSS is **token-only** (`var(--color-text)`,
   `var(--spacing-md)`, …) — zero bespoke hex — so it matches the host palette by
   construction.
4. Light/dark: on `data-color-scheme` flip the host re-resolves and re-sends;
   the frame swaps its `:root` block synchronously (no FOUC, no
   `prefers-color-scheme` gating).

The exact token-name list the host emits is provided by the framework token API
(see the Go API section, filled from the framework map). The editor should
reference a documented subset and treat any missing token as a Phase-0 finding.

---

## 8. Keystroke-latency rig (the go/no-go gate)

Inside the frame, instrument input→paint latency:

- On each user text input (`beforeinput` or the ProseMirror transaction for a
  keystroke), record `t0 = performance.now()`.
- In the **next** `requestAnimationFrame` after the view updates, record
  `t1 = performance.now()`; sample = `t1 - t0` (input-to-next-paint).
- Keep a ring buffer of samples. Expose:
  - `window.__richtextMetrics = { samplesMs, p50(), p99(), count }` for chromedp to
    read directly inside the frame.
  - a `metric` event (§4) posted to the host every ~50 samples and on request.
- The **gate**: with ≥100 synthetic keystrokes, **p99 ≤ 16 ms**. Tests assert
  this; if it fails, the isolation model is renegotiated before Phase 1.

---

## 8a. Phase-0 test-observability hooks (parent-readable)

chromedp cannot read *inside* an opaque-origin frame. So the frame reports a few
facts back over `postMessage`, and the broker stashes them on the **iframe
element** where the parent (and chromedp) can read them:

- `iframe.__richtextReady` — `true` once the `init` handshake completed.
- `iframe.__richtextProbes` — `{ cookieEmpty, parentBlocked, storageBlocked }`, the
  frame's self-isolation checks (computed inside the frame at boot, sent on
  `ready`): `document.cookie === ""`, `window.parent.document` access throws,
  `localStorage` access throws. All three must be `true`.
- `iframe.__richtextTheme` — `{ scheme, sample:{ "--name": "value", … } }`, a few
  token values the frame resolved from its own `:root` **after** applying
  `init`/`themeChanged`. Lets a test assert the crossed token equals the host's.
- `iframe.__richtextLastMetric` — the latest `metric` payload `{ p50, p99, count }`.

These are Phase-0 instrumentation only. The frame sends them via `ready` (probes),
a `themeApplied` event (theme sample), and `metric` events; the broker copies them
onto the element. Nothing here weakens the isolation — the frame volunteers the
data; the parent still cannot reach in.

## 9. Document / interchange model

- **Canonical** = ProseMirror doc JSON, stored by the host as an **opaque blob**.
- **Markdown export** emitted client-side alongside every `docChanged`/`save`
  (lossy for non-representable blocks — none exist in Phase 0; paragraph/heading/
  bold are fully representable). Stored in a sibling field for portability + the
  no-JS SSR read view.
- `schemaVersion = "richtext-v1"`.
- Form integration: two hidden inputs (`…_json`, `…_md`) synced on `docChanged`.

---

## 10. Go plugin public API (verified against the framework)

Framework facts (core at `/Users/dom/programming/gofastr`, imported via the
`replace` in `go.mod`):

- `framework.Plugin` = `{ Name() string; Init(app *App) error }` (`framework/plugin.go:64`).
  Register with `app.RegisterPlugin(p)` before `app.InitPlugins()`.
- Routing is a custom Go-1.22-ServeMux router: `app.Router().Get/Post/Handle(...)`
  (`core/router`). Serve embedded assets with `http.StripPrefix` +
  `http.FileServer(http.FS(sub))` or a `{file}` param handler — precedent
  `battery/embed/plugin.go:48`.
- `uihost.WithExtraScripts(urls...)` (`framework/uihost/uihost.go:206`) is a
  **UIHost Option**, not an `*App` method; it emits `<script src=…></script>`
  before `</body>` on every page. To inject the broker you either mount a UIHost
  with this option, OR (Phase-0) the plugin's self-contained demo page includes
  the `<script>` itself.
- Auth scopes: `auth.HasScope(ctx, "document:write")` /
  `auth.RequireScope(scope)` from `battery/auth` (`apitoken.go:254`,
  `apitoken_middleware.go:221`). Sessions/JWT are unscoped and pass everything;
  a scoped token must carry the scope; **no auth in context ⇒ HasScope false.**
- Tokens: `style.CSSCustomPropertiesOf(theme)` (`core-ui/style/tokens.go:158`)
  → the `:root{ --…: … }` block incl. dark overrides. Light/dark via the
  `data-color-scheme` attribute (`tokens.go:118`).
- HTML: `render.HTML` is a string; `render.HTMLHandler(func(*http.Request) render.HTML)`
  and `render.RespondHTML` (`core/render`).

### Public surface (Worker B builds exactly this)

```go
package richtext

const (
    Name            = "richtext"
    Version         = "0.1.0-phase0"
    RoutePrefix     = "/__gofastr/plugin/richtext"
    EditorHTMLURL   = RoutePrefix + "/editor.html"
    BrokerScriptURL = RoutePrefix + "/broker.js"
    SaveURL         = RoutePrefix + "/save"
    UploadURL       = RoutePrefix + "/upload"
    DemoURL         = "/"                 // self-contained themed demo page
    SchemaVersion   = "richtext-v1"
)

// New constructs the plugin. Implements framework.Plugin (Name/Init).
func New(opts ...Option) *Plugin

type Option func(*Plugin)

// WithDevGrantAll bypasses the auth.HasScope gate on save/upload so the Phase-0
// demo runs without standing up auth. Default OFF (enforcing). Phase 1 removes it.
func WithDevGrantAll() Option
// WithCapabilities overrides the grant set sent to the editor in init.capabilities.
// Default: document:read, document:write, upload:images, theme:read.
func WithCapabilities(caps ...string) Option
// WithDemoPage registers the self-contained themed demo page at DemoURL.
func WithDemoPage() Option
// Persistence hooks; all default to an in-memory store so the demo/tests work OOTB.
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option
func WithUploadHandler(fn func(ctx context.Context, req UploadRequest) (UploadResult, error)) Option

func (p *Plugin) Name() string
func (p *Plugin) Init(app *framework.App) error   // registers ALL routes below
// LoadDoc returns the last-saved canonical JSON for docID (in-memory default).
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (docJSON string, ok bool)

// UIHostOption is uihost.WithExtraScripts(BrokerScriptURL), for apps using a UIHost.
func UIHostOption() uihost.Option

// Mount renders the mount marker + the two hidden inputs to drop into a form.
func Mount(cfg MountConfig) render.HTML
type MountConfig struct {
    DocID     string // persistence key (default "demo")
    JSONField string // hidden input name for canonical block-JSON (default "body_json")
    MDField   string // hidden input name for markdown export (default "body_md")
    MinHeight string // initial iframe height before first resize (default "240px")
    Doc       string // optional initial doc JSON, server-rendered for reload round-trip
}

type SaveRequest   struct { DocID, DocJSON, Markdown, SchemaVersion string }
type UploadRequest struct { Name, Type string; Bytes []byte }
type UploadResult  struct { URL string }
```

### Routes registered by `Init`

| method+path | handler |
|---|---|
| GET `EditorHTMLURL`, `…/editor.js`, `…/editor.css` | serve embedded assets (correct Content-Type; `Cache-Control: no-cache` in dev) |
| GET `BrokerScriptURL` | serve embedded `host/broker.js` (`text/javascript`) |
| POST `SaveURL` | parse `{docId, doc, markdown, schemaVersion}`; gate `document:write` (see below); call save handler |
| POST `UploadURL` | read image bytes (multipart or raw); gate `upload:images`; call upload handler; return `{url}` |
| GET `DemoURL` | (only with `WithDemoPage()`) self-contained themed page: token `:root` CSS via `style.CSSCustomPropertiesOf`, `data-color-scheme` toggle, the `Mount()` marker + a form, and `<script src=BrokerScriptURL>` |

Capability gate helper (proves the auth-scope reuse from D3):

```go
func (p *Plugin) allow(r *http.Request, cap string) bool {
    if p.devGrantAll { return true }
    return auth.HasScope(r.Context(), cap)   // battery/auth
}
```

The mount marker shape the broker looks for (§6):

```html
<div data-fui-richtext
     data-fui-richtext-docid="demo"
     data-fui-richtext-for="body_json,body_md"
     data-fui-richtext-minheight="240px"
     data-fui-richtext-doc='<initial doc JSON or empty>'></div>
<input type="hidden" name="body_json">
<input type="hidden" name="body_md">
```
