# CRUX — client-side isolation for a third-party ProseMirror editor in GoFastr

**Worker: GLM · DESIGN MEMO ONLY (no production code, no repo edits).**
Repo root for all citations:
`/Users/dom/programming/gofastr/.claude/worktrees/purring-floating-sutherland`.
Every file:line below was read and verified in this worktree before writing.

---

## 1. Headline position (3 sentences)

Isolate the editor in a **sandboxed `<iframe sandbox="allow-scripts">`**
served from a **same-origin plugin document URL**
(`/__gofastr/plugin/<name>/editor.html`, `go:embed`-ed by the satellite Go
module), deliberately **omitting `allow-same-origin`** so the frame receives
an **opaque origin** — it then cannot read cookies/localStorage, cannot reach
`window.parent`/host DOM, cannot make credentialed same-origin requests, and
every host interaction flows over a **versioned `postMessage` RPC** gated by
**element-identity source checks** (`event.source === iframe.contentWindow`).
This is the only option that delivers genuine D3 isolation while preserving
ProseMirror's live-`contenteditable`-DOM editing model; Shadow-DOM and
Worker-proxy are rejected (§2), and the hard UX costs the boundary creates are
paid for explicitly (§2.1–2.7) rather than wished away. The host persists the
editor's block-JSON **opaquely** as an entity `JSON` column
(`framework/entity/declaration.go:177` → `schema.JSON`) alongside a
plugin-serialized **markdown** `TEXT` column for interchange/SSR-read/export,
while a **host-side Go renderer** in the satellite module produces the
token-styled SSR read view so the architecture stays SSR-first and CSP-clean.

---

## 2. Q1 — The client isolation model

### Decision: sandboxed iframe, opaque origin, same-origin document URL

The frame element is:

```html
<iframe
  src="/__gofastr/plugin/richeditor/editor.html?v=<content-hash>"
  sandbox="allow-scripts"
  <!-- NOTE: NO allow-same-origin, NO allow-forms, NO allow-top-navigation -->
  credentialless="true"   /* best-effort; ignored where unsupported */
  referrerpolicy="no-referrer"
></iframe>
```

Why each piece:

- **`sandbox="allow-scripts"` and nothing else.** The omitted
  `allow-same-origin` is the load-bearing decision: the frame is forced into
  an **opaque origin** ("null"), so per HTML spec it cannot access
  `document.cookie`, `localStorage`/`sessionStorage`, `window.parent`,
  `window.top`, sibling frames, or send credentialed same-origin fetches
  (opaque origin → CORS-tainted, cookies not attached). `allow-scripts`
  alone lets ProseMirror run; we deliberately refuse `allow-forms`,
  `allow-top-navigation`, `allow-popups`, `allow-same-origin`. This is real
  isolation, not a gentleman's agreement (locked decision #3).
- **Same-origin document URL, not `srcdoc`.** The host CSP default is
  `default-src 'self'; img-src 'self' data:; frame-ancestors 'none';
  base-uri 'self'` (`framework/docs/content/security.md:39`). There is **no
  explicit `frame-src`/`child-src` directive**, so iframe sources fall back
  to `default-src 'self'` — a same-origin `/__gofastr/plugin/...` URL
  satisfies it with **zero CSP edits to the core**. `srcdoc` also satisfies
  `'self'` but (a) can't be content-addressed/cached like
  `/__gofastr/runtime/<name>.js?v=<hash>` already is
  (`core-ui/runtime/runtime.js:1898` and ROADMAP §8), and (b) puts a large
  HTML string inline in the host document, fighting the ≤12 KB core budget.
  The document URL mirrors the existing split-module serving pattern. (If a
  page must SSR-inline the editor chrome for first-paint, `srcdoc` is the
  fallback — see Phase-0 spike §7.)
- **Opaque origin despite same-origin URL.** A reader may object "same-origin
  URL + sandbox = the frame is still same-origin." It is not: omitting
  `allow-same-origin` makes the browser treat the loaded document as a
  **fresh opaque origin regardless of its URL**. The same-origin URL only
  matters for the *parent's* CSP `frame-src` check; the loaded document's
  origin is opaque. This is the standard pattern for sandboxed
  ad/widget/comment frames.

### Rejected alternatives

- **Cross-origin iframe (separate host/port).** Stronger in principle but
  (a) forces a CSP `frame-src https://editor-cdn.example` addition — a core
  policy change the brief forbids ("Core `go.mod` and the ≤12 KB core budget
  must stay untouched" implies core CSP too); (b) demands real TLS/cert
  infra for a first-party-maintained plugin; (c) buys nothing over
  opaque-origin sandbox, which already blocks every cross-origin reach. Only
  worth it for an *actually* external vendor, which the brief says this is
  not (locked decision #3 is about *treating* it as third-party, not hosting
  it off-domain).
- **Shadow DOM + capability shim.** **Not isolation, ever.** Shadow DOM is a
  *style/DOM* encapsulation boundary, not a security one: the editor JS runs
  in the host origin with full access to `document.cookie`, `fetch` (CSRF
  token in `meta[name=csrf-token]` per `.claude/skills/component-build/SKILL.md`
  §"CSRF token forwarding"), `window.__gofastr`, other islands' DOM, and
  `window.top`. A "capability shim" is voluntary restraint that a bug or a
  malicious update silently bypasses — it fails locked decision #3 ("no
  reaching into host internals … no trusted same-origin shortcut"). Shadow
  DOM is still useful **inside** the frame for the editor's own DOM scoping,
  but it is not the isolation model.
- **Worker-for-logic + thin-DOM-proxy.** ProseMirror's view, state, and
  plugin layer (menus, tooltips, decorations, selection) are synchronously
  coupled to the DOM; proxying every transaction/selection/caret-move across
  a `postMessage` round trip adds latency to **every keystroke**. Editing UX
  is the one product requirement we cannot trade (locked decision #2:
  Notion/Confluence-class), so this is rejected on UX grounds, not security.

### 2.1 The UX costs the boundary creates — solved

Each cost below is real and must be carried over `postMessage`; none is
hand-waved.

**(a) iframe auto-sizing.** A `ResizeObserver` inside the frame watches
`document.documentElement.scrollHeight`; on change it posts `resize{height}`.
The host sets `iframe.style.height`. The host never polls. Before first
content (avoid a 150px-default stub), the manifest declares a `MinHeight`
token-resolved length and the host applies it as the initial height; the
first `resize` event lands within one frame. The reverse direction (host
tells frame to relayout, e.g. after a container width change) is
`relayout{}` host→plugin.

**(b) Focus & caret.** Native focus works inside the frame. Host→frame:
`focus{}` causes the frame to call `editor.focus()` and place the caret at
the last selection. Frame→host: `focusExit{edge:"top"|"bottom"}` fires when
the user tabs past the first/last focusable node inside the frame; the host
moves focus to the next/previous focusable element in the host document. No
focus trap is attempted — the editor is not a modal (contrast
`core-ui/widget` modal focus-trap behavior in `SKILL.md` §"A11y").

**(c) Keyboard shortcuts across the boundary.** Key events inside a
same-origin-or-opaque iframe **do not bubble to the parent document** (DOM
spec), so host global shortcuts (`data-fui-shortcut-click`/`-focus`,
`ARCHITECTURE.md` table) never fire while the editor is focused — this is
free and correct. Editor-internal shortcuts (Cmd/Ctrl+B/I/U, slash menu,
toggle, etc.) live entirely in the frame. Host toolbar buttons send
`execCommand{command,args}` (e.g. `"bold"`, `"insertTable"`, `"toggleHeading",
level:2`); the frame maps them to ProseMirror commands. The one host
shortcut that should still fire over the editor (e.g. a global Cmd+K command
palette) is opted in explicitly by the host forwarding a `hostShortcut{chord}`
event the frame is told about at `init`.

**(d) Copy / paste.** Native clipboard works inside the frame (user-gesture
writes are allowed for opaque origins). The frame intercepts `copy`/`paste`:
on paste it sanitizes to its own block-JSON schema and/or accepts
`text/html`/`text/plain` from `e.clipboardData`. For paste *from host
surfaces into the editor*, the host can `postMessage` an
`insertContent{json | markdown}` — but only when the editor is focused, to
avoid a confused-deputy injection. The frame's clipboard write never carries
host session data (it has none) — clipboard exfiltration is not a vector
here.

**(e) Drag-and-drop.** Internal DnD (block reordering, column resize) is
fully in-frame. Cross-frame DnD (drag an entity chip from a host sidebar
into the doc) does **not** try to use native cross-frame `dataTransfer`
(fragile across the opaque-origin boundary); instead the host listens for
`dragstart` on its draggable elements, stashes a pending payload, and on
`dropZoneEntered{}` from the frame sends `dropInsert{json, at:"end"}`. File
drops into the frame become an upload request (§2.1g).

**(f) Mobile virtual keyboard.** The contenteditable focus drives the soft
keyboard natively. Two frame-side handlers cover the rough edges:
`window.visualViewport` reports the effective viewport height → `resize`
keeps the editor visible above the keyboard; and `scrollSelectionIntoView`
on the ProseMirror view keeps the caret on screen. The host **must not**
call `doc.lockScroll` (`core-ui/ARCHITECTURE.md` §"Global document state")
while the editor has focus, or the page can't scroll to reveal the caret;
the handshake includes a `scrollLockReleased` host→plugin event when the
host would otherwise lock.

**(g) Image / file upload (the editor has no network).** The opaque-origin
frame cannot attach session cookies or the CSRF token, so it does not
upload. Flow: user drops/selects a file in the frame → frame posts
`requestUpload{uploadId, file, filename, mime}`. The `File`/`Blob` is
**structured-cloneable across `postMessage`** (HTML spec), so bytes cross
the boundary without base64. The host: (1) checks the grant
`upload:create` (§3), (2) performs the credentialed multipart POST to its
own upload endpoint **forwarding the CSRF token from
`meta[name=csrf-token]`** exactly as `SKILL.md` §"CSRF token forwarding"
prescribes — the token value **never enters the frame**, (3) returns
`requestUploadResponse{uploadId, url, id}`. The frame inserts an
image/file block referencing `{id, url}`. The editor remains genuinely
network-less; uploads are a *granted capability*, not a back channel.

---

## 3. Q2 — Host↔plugin protocol & capability model

### Transport: versioned `postMessage` JSON-RPC envelope

```jsonc
// host → plugin, or plugin → host (src flips)
{
  "v": 1,                         // envelope version; bump = breaking
  "id": "01J…",                   // ULID/UUID; correlation for req/resp
  "type": "request" | "response" | "event",
  "src": "host" | "plugin",
  "method": "init",               // absent on responses
  "params": { /* method-specific */ },
  "result": null,                 // responses only
  "error": null                   // {"code":"E_CAPABILITY_DENIED",...} on failure
}
```

- **Origin/source checks (both directions).** Because the frame is
  opaque-origin, `event.origin === "null"` **by design** — this is expected,
  not a weakness. The host validates **element identity**, not an origin
  string: `if (event.source !== editorIframe.contentWindow) return;`. Each
  editor instance binds to exactly one `<iframe>` element; a spoofed message
  from any other frame/window is dropped. The frame validates the inverse
  (`event.source === window.parent`) on receipt of `init` and then trusts
  only `id`-correlated responses.
- **Correlation.** `id` matches request↔response; `event` messages are
  fire-and-forget. The host keeps a `Map<id, {resolve,reject,timeout}>` per
  instance (WeakMap-keyed per iframe element, per the per-instance-state
  rule in `SKILL.md` §"Runtime-module contract").
- **Teardown mirrors the SPA contract.** On `gofastr:navigate`
  (`ARCHITECTURE.md` table + `SKILL.md` line 232) the host sends
  `teardown{}` and awaits the frame's `response` (or a 200 ms timeout)
  before the iframe is removed, so the plugin can flush pending timers /
  `IntersectionObserver`s / abort in-flight uploads — the same
  per-instance teardown rule every runtime module follows.

### Host → plugin methods

| Method | Params | Plugin responds |
|---|---|---|
| `init` | `{doc, tokens, config, capabilities, locale, schemaVersion}` | `ready` (async) |
| `setDoc` | `{doc}` | — |
| `themeChanged` | `{scheme, tokens}` | — |
| `requestSave` | `{}` | `{doc, markdown, schemaVersion}` |
| `execCommand` | `{command, args}` | `{ok, appliedAt}` |
| `setReadOnly` | `{readOnly}` | — |
| `relayout` | `{}` | `{height}` |
| `hostShortcut` | `{chord}` | — |
| `requestUploadResponse` | `{uploadId, url, id}` (or `error`) | — |
| `teardown` | `{}` | `{}` |

### Plugin → host methods (each is a capability; see table)

| Method | Params | Host responds |
|---|---|---|
| `ready` | `{version, schemaVersion, minHeight}` | — |
| `docChanged` | `{doc, rev}` (debounced) | — |
| `save` | `{doc, markdown}` | `{persistedAt}` |
| `requestUpload` | `{uploadId, file, filename, mime}` | via `requestUploadResponse` |
| `resize` | `{height}` | — |
| `focusExit` | `{edge}` | — |
| `navigateIntercept` | `{href}` | `{allow}` |
| `requestCapability` | `{capability}` | `{granted}` |

### Capability model — mapped onto #37's `capability:scope` registry

Issue #37 is a **design** issue (no code yet); the capability vocabulary that
**already exists** is the API-token scope model in
`framework/docs/content/auth.md:288–313` — `resource:verb` strings with
exact / verb-wildcard / resource-wildcard / global matching (`posts:read`,
`posts:*`, `*:read`, `*:*`), enforced by `RequireScope` and read via
`auth.TokenScopes(ctx)`. **This is the `capability:scope` registry the
client design mirrors.** The editor's capabilities are expressed in the same
shape and gated by the same registry:

| Capability grant | Meaning | Default for editor |
|---|---|---|
| `doc:read` | receive initial/current doc via `init`/`setDoc` | ✅ |
| `doc:write` | emit `docChanged`/`save` | ✅ |
| `clipboard:write` | write to clipboard on user copy | ✅ |
| `clipboard:read` | read clipboard on user paste | ✅ (paste only) |
| `upload:create` | `requestUpload` → host does credentialed upload | configurable |
| `navigate:intercept` | `navigateIntercept` for in-doc links | ✅ |
| `theme:read` | receive resolved token map + `themeChanged` | ✅ |
| `theme:write` | **never granted** (editor cannot flip host theme) | ❌ |
| `network:*` | direct host-origin fetch | **never granted** |
| `storage:*` | host `localStorage`/cookies | **impossible** (opaque origin) |

- Manifest declares the full set the plugin *wants*; host grants a subset at
  `init.capabilities`. A plugin calling a method whose grant is absent gets
  `error.code = "E_CAPABILITY_DENIED"`. `requestCapability` is the runtime
  elevation path (e.g. first paste → prompt for `clipboard:read`).
- The `RequireScope`-style check is the **single chokepoint**; it's the
  client mirror of the server-side token-scope gate, so a capability revocation
  (module disable, `framework/module.go:452` `Disable`) propagates
  immediately: a disabled module's editor iframe is removed and its RPC
  channel torn down — the "process isn't running" case from #37 maps to
  "the iframe isn't mounted" on the client.

### What the plugin CANNOT reach (explicit denylist)

`document.cookie`, `localStorage`/`sessionStorage`, `window.parent`,
`window.top`, `parent.document`, any host DOM node, `window.__gofastr*`
host globals, the CSRF token value, session cookies, other plugins' frames,
the host DB/filesystem, and any `fetch` that carries host credentials. Most
of these are **structurally impossible** (opaque origin + sandbox), not
merely policy — that's the point of choosing this model.

---

## 4. Q3 — Document / interchange model

### Canonical store: block-JSON, opaque to the host

The host persists the editor's block-JSON **without interpreting it**, as an
entity `JSON` column (`framework/entity/declaration.go:177` → `schema.JSON`;
maps to `map[string]any`, per `cmd/gofastr/generate.go:1299`). Opaqueness is
required — the host must not become coupled to the editor's internal schema
— but it is **not lock-in**, because three guarantees hold:

1. **Schema versioning.** Every `save`/`docChanged` carries
   `schemaVersion` (e.g. `"richeditor-v1"`); the manifest declares the
   schema (`Client.Schema`, §6). A breaking schema change is a major
   version bump with a migration path shipped in the satellite module.
2. **Markdown is always emitted alongside.** The plugin serializes
   block-JSON → markdown on every save (`save` returns both `{doc,
   markdown}`). The host stores markdown in a sibling `TEXT` column. This
   is the **portable interchange** and the export surface for issue #39
   (import/export).
3. **Dual-ownership of the schema, not the renderer.** The block-JSON
   schema (a versioned JSON Schema document) is the contract; the editor's
   ProseMirror view and the host's SSR renderer (below) are dual
   implementations of it.

### Markdown source = client serialize (the plugin owns it)

The host **cannot** faithfully render plugin-specific blocks (callouts,
toggles, colored text, complex tables) to markdown — it doesn't understand
them. So the plugin is the sole authority for serialization, and markdown is
emitted **client-side** on save. A server-side markdown renderer is a
non-goal: it would duplicate the plugin's block knowledge and drift. The
markdown column is authoritative for: search indexing, the SSR read-view
fallback, and export. For blocks markdown genuinely cannot express, the SSR
read view does not use the markdown — see below.

### Reaching a standard form POST / entity CRUD field

The editor is a `core-ui/widget` island. The host form carries two hidden
fields:

```html
<form data-fui-rpc="/island/article/save" data-fui-rpc-method="POST">
  <input type="hidden" name="body_json">   <!-- block-JSON -->
  <input type="hidden" name="body_md">     <!-- markdown -->
  <!-- ...title, etc. -->
  <div data-fui-widget="richeditor" data-fui-richeditor-for="body_json,body_md"></div>
  <button type="submit">Save</button>
</form>
```

The editor island writes into both hidden fields on `docChanged` (debounced)
and on `save`. Submitting the form fires the **existing** in-page RPC path
(`data-fui-rpc`, `ARCHITECTURE.md` "Forms and mutations" / the four-rule
contract in `SKILL.md`) — no new form mechanism, no new `data-fui-*`
attribute beyond the widget mount marker. The island handler validates
server-side (length, schema-version allowlist) and persists via the normal
entity CRUD field path. CSRF is handled by the form RPC itself (the host
attaches `X-CSRF-Token` per `SKILL.md`); the editor frame never sees the
token.

### SSR read view — host-side Go renderer, not the iframe

For full-fidelity blocks markdown can't express, the read view is rendered
**server-side by the satellite Go module**, not by loading a read-only
editor iframe. Concretely the satellite ships:

```go
// in the satellite module
package richeditor

// Render reads canonical block-JSON and emits design-system HTML.
// It uses core-ui/html primitives + registry.RegisterStyle, so the read
// view is a FIRST-CLASS design-system surface (Hard Rule 7) — NO bespoke
// CSS, token-only (var(--*)) — NOT plugin CSS.
func Render(doc map[string]any) render.HTML { /* ... */ }

var Style = registry.RegisterStyle("richeditor-readonly", styleFn)
```

Rationale, tied to the architecture:
- **SSR-first** (`CLAUDE.md` TL;DR bullet 1; `ARCHITECTURE.md` "The model in
  one paragraph"). A read-only iframe would defer first paint to JS — a
  regression the architecture was built to avoid.
- **CSP-clean.** The read view is host HTML + a registered component
  stylesheet (`registry.RegisterStyle` + `Style.WrapHTML`, `SKILL.md` §"CSS
  contract"), auto-loaded via `data-fui-comp="richeditor-readonly"` — no
  inline styles, no frame.
- **Token-styled** (`theming.md`: all CSS is `var(--*)`). The Go renderer
  composes `framework/ui` / `core-ui/html` primitives and emits
  `var(--color-*)` etc., so it recolors with `data-color-scheme` for free.
- **Single schema, dual renderer.** The Go SSR renderer and the in-frame
  ProseMirror view consume the **same** block-JSON schema. Block additions
  ship both renderers in the same `go get` update — the schema is the
  anti-drift contract. (Where a block is too dynamic for SSR — e.g. an
  embedded live chart — the SSR renderer emits a `<img>`/fallback and the
  runtime hydrates it, matching the existing `data-fui-carousel-defer`
  lazy-hydrate pattern.)

---

## 5. Q4 — Theming across the boundary

**Problem.** Hard Rule 7 (`CLAUDE.md`: "ONE styling surface … ALL styling
via design tokens `var(--*)` … ZERO bespoke CSS") must hold, but (a) CSS
custom properties **do not inherit across iframe boundaries** (CSS spec), and
(b) the opaque-origin frame **cannot fetch `/__gofastr/app.css`** credibly
(and we wouldn't want it reaching host CSS anyway).

**Solution — resolved-token bridge, frame-local `:root` block.**

1. **Host resolves its own tokens.** Once at init and on every theme change,
   the host reads the **complete** resolved token map from its own document:
   ```js
   const names = [...]; // every --* the host emits in /__gofastr/app.css
   const tokens = Object.fromEntries(
     names.map(n => [n, getComputedStyle(document.documentElement)
                         .getPropertyValue(n).trim()])
   );
   ```
   This captures **resolved values** (so `DarkColors` re-declarations under
   `:root[data-color-scheme="dark"]`, `core-ui/style/tokens.go:139-150`,
   are already baked in) for **all** token groups — colors, spacing, radii,
   fonts, text sizes — not just colors.
2. **Host sends tokens in `init` and `themeChanged`.** `{tokens:
   {"--color-primary":"#4F46E5","--spacing-md":"12px",...},
   scheme:"dark"}`.
3. **Frame injects its own `:root` block.** The frame writes
   `<style>:root{--color-primary:#4F46E5;…}</style>` into **its own**
   document. This is legal because the frame is a **separate document with
   its own CSP** — the host's strict CSP does **not** govern the framed
   document. The editor's ProseMirror CSS then references
   `var(--color-primary)` **exactly as host components do**, using the
   **same token names**, so it visually matches by construction.
4. **Light/dark stays in sync.** The host observes `data-color-scheme` on
   `<html>` (set by the synchronous `colorscheme.js` bootstrap,
   `core-ui/runtime/colorscheme.js:30`, or by
   `window.__gofastr_colorScheme.set` from the `themeswitch` module,
   `core-ui/runtime/src/themeswitch.js:47`). On flip, the host re-reads
   computed styles (the dark re-declaration has taken effect) and re-sends
   `themeChanged`; the frame swaps its `:root` block. No reload, no FOUC
   inside the frame (the swap is synchronous before next paint).
5. **Editor CSS stays token-only.** The frame's stylesheet uses only
   `var(--color-*)`, `var(--spacing-*)`, `var(--radii-*)`, `var(--font-*)`,
   `var(--text-*)`. **Zero** bespoke hex values. Where the editor needs a
   value the host tokens don't provide, that is a **missing token** — the
   right fix is to add it upstream (`CLAUDE.md` Hard Rule 7: "a surface
   that needs styling the design system doesn't provide is a MISSING
   component / layout / token — add it upstream"), surfaced as a Phase-0
   finding.
6. **The frame never links host CSS.** The frame receives **only** a
   `:root` variable block (no selectors, no rules) — so it cannot
   accidentally inherit host layout/component CSS, and the editor's
   selectors remain its own.

**On Hard Rule 7 under isolation.** The rule governs the **host document**.
The plugin frame is a separate, isolated, third-party document — it is **not**
part of the host design-system surface, and its internal CSS is its own
styling surface (using the same token *names* for visual consistency). The
SSR read view (§4) **is** a host surface and **must** use
`registry.RegisterStyle` + tokens, zero bespoke CSS. This split is the
principled reading of the rule once isolation is a hard requirement.

---

## 6. Manifest additions (extend, don't fork)

Extend the existing `ModuleManifest` (`framework/module.go:20-37`) with one
optional pointer — server-only modules leave it nil:

```go
type ModuleManifest struct {
    // existing fields — unchanged
    Version        string
    Description    string
    DependsOn      []string
    MigrationGroup string

    // NEW: present only for modules that ship an isolated client surface.
    Client *ClientModule `json:",omitempty"`
}

// ClientModule declares an opaque-origin sandboxed-iframe client plugin.
type ClientModule struct {
    // Entry is the same-origin document URL the host loads in the iframe.
    // Always under /__gofastr/plugin/<module>/...  (satisfies default-src
    // 'self' with zero CSP edits — security.md:39).
    Entry string // "/__gofastr/plugin/richeditor/editor.html"

    // ScriptHash content-addresses the embedded JS bundle; the host emits
    // ?v=<ScriptHash> on the Entry URL (mirrors /__gofastr/runtime/<n>.js?v=).
    ScriptHash string

    // Isolation is the sandbox policy. v1 allows exactly one value:
    // "sandbox-iframe-opaque"  →  <iframe sandbox="allow-scripts">, opaque origin.
    Isolation string

    // Sandbox lists the sandbox tokens. v1 fixpoint: ["allow-scripts"].
    // "allow-same-origin" is REJECTED at RegisterModule time.
    Sandbox []string

    // Capabilities is the full capability:scope set the plugin REQUESTS.
    // The host grants a subset at init. Vocabulary mirrors auth.md:288-313.
    Capabilities []string // ["doc:read","doc:write","upload:create",...]

    // Schema is the block-JSON schema version the plugin emits/consumes.
    Schema string // "richeditor-v1"

    // MinHeight is a token-resolved CSS length applied before the first
    // resize event, to avoid the 150px iframe default stub.
    MinHeight string // "var(--editor-min-height, 240px)"
}
```

`ModuleInfo` (`framework/module.go:50-60`) gains a `Client *ClientModule`
mirror for introspection (so `app.Modules().List()` and the MCP
introspection tools surface whether a module has a client surface, its
grants, and its schema version). `RegisterModule`
(`framework/module.go:616`) gains a validation step: if `Client != nil`,
`Isolation` must be the allowed value and `Sandbox` must not contain
`allow-same-origin` — fail-closed at registration, not at runtime.

---

## 7. Phase-0 spike — prove the model is usable

Before the full Notion-class build, ship a **minimal satellite module**
`richeditor-spike` that exercises every boundary mechanism with a throwaway
editor stub (≈5 KB JS, NOT ProseMirror):

- A `contenteditable` `<div>` that types, and serializes to a trivial
  block-JSON `{type:"doc",content:[{type:"paragraph",text:"…"}]}`.
- Implements: `init`, `ready`, `docChanged` (debounced), `requestSave` →
  `{doc, markdown}`, `resize`, `themeChanged`, and a fake `requestUpload`
  round-trip.
- Host side: a `core-ui/widget` island that mounts the iframe, does the
  `postMessage` handshake, syncs two hidden fields (`body_json`, `body_md`),
  and POSTs via the normal `data-fui-rpc` path to an entity with a `JSON`
  column + a `TEXT` column.

**Spike passes iff these are all true** (chromedp, per `SKILL.md` §"Verify"):

1. The rendered `<iframe>` has `sandbox="allow-scripts"` and **no**
   `allow-same-origin` attribute (assert on the DOM).
2. **Isolation proven:** from inside the frame,
   `window.parent.document` throws a SecurityError; `document.cookie` is
   `""`; `localStorage.getItem` throws (opaque origin). (Inject an assert
   script via the frame's own JS, surfaced through `ready`.)
3. **Round-trip:** type → `docChanged` fires → submit → `body_json` +
   `body_md` persist → reload → the saved doc re-`init`s and the text
   reappears.
4. **Theme sync:** flip `data-color-scheme` → host re-resolves tokens →
   frame's `:root` swaps → `getComputedStyle(frameDoc.documentElement)
   .getPropertyValue('--color-primary')` equals the host's value (assert
   equality, since opaque-origin frames are readable only via the frame's
   own instrumented JS reporting back).
5. **Size budget untouched:** `core.js` gzipped size is identical before
   and after (satellite module is `go get`, not core) — assert via the
   existing build/minify pipeline.
6. **Autosize:** change content → `resize` → iframe height tracks
   `scrollHeight` (no scrollbar inside the frame, no clipping).

The spike answers the one binary question that gates the big build: **is an
opaque-origin sandboxed iframe + postMessage RPC a usable editing surface**
(focus, typing, autosize, theme, save)? If p99 keystroke latency is
≤16 ms inside the frame, proceed; if not, the model is wrong and we
revisit before committing.

---

## 8. Top-3 risks + mitigations

1. **Editing latency across the boundary.** Every host-coordinated state
   change is a `postMessage` round trip; if typing or caret movement ever
   crosses the boundary, the editor feels laggy and the product fails
   locked decision #2. **Mitigation:** keep **all** editing logic —
   ProseMirror state, transactions, decorations, menus, slash menu, spell
   check — **inside the frame**. The boundary carries only coarse-grained
   events (`docChanged` debounced ~300 ms, `save`, `resize`, `upload`).
   **Typing never crosses the boundary.** The Phase-0 spike (§7) measures
   p99 keystroke latency as its go/no-go gate; >16 ms → stop.

2. **Theme-token drift / incomplete token set → editor looks "off."** If the
   host sends a curated subset of tokens, the editor's
   `var(--<something>)` references resolve to fallbacks and the visual
   match breaks, silently violating Hard Rule 7's *intent* (look like the
   design system). **Mitigation:** the host sends the **complete** resolved
   token map (every `--*` it emits in `/__gofastr/app.css`), enumerated
   from the theme struct, not hand-curated. The spike asserts (§7 step 4)
   that a sample of token values inside the frame equals the host's. Any
   token the editor needs that the host doesn't emit is surfaced as a
   missing-token finding and added upstream (Hard Rule 7's prescribed fix).

3. **Opaque-origin `"null"` weakens origin-string checks → spoofing.** A
   sibling frame or a injected script in the host page could
   `postMessage` toward the editor iframe with a forged payload; the
   `"null"` origin gives the host no string to distinguish friend from
   foe. **Mitigation:** the host validates **element identity**
   (`event.source === editorIframe.contentWindow`), not origin — each
   editor instance is bound to exactly one iframe element, and a message
   whose `source` is not that exact `contentWindow` is dropped before any
   capability check. Correlation `id`s are ULIDs generated host-side, so a
   spoofed *response* without a matching pending request is discarded.
   Capability grants are re-checked per-RPC, so even a successful spoof
   that forged a source reference (it can't, without already having DOM
   access, at which point the host is already compromised) is still capped
   by the granted set. The `"null"` origin is **expected** for opaque
   sandbox and documented as such, not treated as a bug.

---

## Final report

- **Memo file:** `/private/tmp/claude-501/-Users-dom-programming-gofastr/f3f0027b-9b0d-49a6-aa9f-80f3837e6f43/scratchpad/CRUX-glm.md`
- **Verified against:** `CLAUDE.md` (Hard Rules 7–9, SSR-first TL;DR);
  `core-ui/ARCHITECTURE.md` (SSR+hydration model, `data-fui-*` runtime
  attrs, `gofastr:navigate` teardown, doc-state manifest);
  `.claude/skills/component-build/SKILL.md` (WeakMap per-instance state,
  CSRF forwarding, CSS contract, verify-before-claim);
  `framework/plugin.go` / `framework/battery.go` (Plugin/Battery/Module
  spine); `framework/module.go:20-37` (`ModuleManifest` extended, not
  forked); `framework/docs/content/security.md:39` (CSP default — confirmed
  no `frame-src` directive, so same-origin iframe needs zero core edits);
  `framework/docs/content/auth.md:288-313` (the existing `capability:scope`
  token-scope registry the design mirrors); `framework/docs/content/
  theming.md` + `core-ui/style/tokens.go:139-150` (token + dark-mode
  mechanics); `core-ui/runtime/colorscheme.js` +
  `core-ui/runtime/src/themeswitch.js` (theme flip hooks);
  `core-ui/runtime/runtime.js:1898` (split-module serving/content-addressing
  pattern reused); `framework/entity/declaration.go:177` (`json`→
  `schema.JSON` for opaque blob storage); issue #37 (server-side process
  isolation protocol this client design mirrors).
- **Headline recommendation:** Ship the editor as a **same-origin,
  opaque-origin, `sandbox="allow-scripts"`-only iframe** inside a satellite
  Go module, talking to the host over a **versioned `postMessage` RPC**
  gated by **element-identity source checks** and a **`capability:scope`
  grant set** that mirrors the existing API-token scope registry. Persist
  block-JSON opaquely (`schema.JSON` column) + plugin-serialized markdown
  (`TEXT` column); render the SSR read view with a **host-side Go
  `registry.RegisterStyle` renderer** (token-only, CSP-clean). Gate the
  full build on a **Phase-0 spike** whose go/no-go criterion is measured
  p99 keystroke latency ≤16 ms inside the frame.
