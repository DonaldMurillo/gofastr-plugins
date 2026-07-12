# Crux design memo — Claude (Opus)

## 1. Headline position (3 sentences)

Run the editor bundle in a **same-origin sandboxed iframe** (`srcdoc` served
from the plugin's own asset route, `sandbox="allow-scripts"` WITHOUT
`allow-same-origin`, so the frame is an opaque origin that cannot touch host
cookies, storage, or DOM) and let it talk to the host only through a
**versioned postMessage capability bridge**. The canonical document is
**ProseMirror block-JSON persisted as an opaque blob**, with a **client-side
markdown export** carried alongside for portability, form/entity integration,
and the no-JS SSR read view. The isolation is real (opaque origin + explicit
capability grants), and every editing-UX cost the frame creates
(sizing/focus/paste/drag/mobile/upload) is solved at the bridge, not by
weakening the sandbox.

## 2. Q1 — isolation model + each UX cost solved

**Model: opaque-origin sandboxed iframe.** `sandbox="allow-scripts"` and
crucially NOT `allow-same-origin` → the browser assigns the frame a null/opaque
origin, so even though the HTML is served same-origin, script inside cannot read
`document.cookie`, `localStorage`, the host DOM, or the CSRF meta tag. That is
the third-party guarantee (D3) with zero server infrastructure — no second
origin to host, deploy, or CORS. (A separate *cross-origin* frame is stronger
against Spectre-class attacks but needs a real second origin + COOP/COEP; I'd
keep that as an opt-in `isolation: "cross-origin"` manifest tier, default to
opaque-origin.) This satisfies the strict CSP because the frame document and its
bundle are first-party assets; add `frame-src 'self'` to the host CSP.

UX costs, each solved at the bridge:
- **Auto-sizing:** a `ResizeObserver` inside the frame posts `resize(height)`;
  the host sets the iframe height. No internal scrollbars; the page scrolls.
- **Focus & caret:** focus works natively inside a frame; the issue is *global*
  shortcuts (`/`, ⌘K) — the host's runtime can't see keydowns inside the frame.
  Solve by convention: editor owns keys while focused; it posts
  `focusChanged(bool)` so the host can suppress conflicting page shortcuts, and
  forwards any it doesn't own (e.g. ⌘S) via `hostShortcut(chord)`.
- **Keyboard across boundary:** only the explicitly-forwarded chords cross; no
  general key bridging (keeps the surface tiny and safe).
- **Copy/paste:** clipboard works inside the frame natively for user-initiated
  copy/paste; paste-sanitization is ProseMirror's job *inside* the frame. No
  host involvement, no capability needed.
- **Drag & drop (reorder):** internal to the frame — ProseMirror handles it.
  Cross-frame drag (drag a file in from desktop) lands on the frame directly if
  the drop zone is inside it; file bytes never touch the host except via the
  upload capability.
- **Mobile virtual keyboard:** the contenteditable is inside the frame, so the
  VK targets it natively; the `resize` bridge handles the viewport-shrink reflow.
  `visualViewport` is readable inside the frame.
- **Image/file upload:** editor has NO network capability. On paste/drop of an
  image it posts `requestUpload({name,type,bytes})`; the HOST performs the
  authenticated upload (it owns the cookie/CSRF + the storage battery) and
  replies `uploadResult({url})`; editor inserts the returned URL. This is the
  single most important capability — it's why the frame needs no network at all.

## 3. Q2 — host↔plugin protocol + capability table

**Transport:** `postMessage` with a strict envelope
`{v:1, id, dir, type, payload}`; the host validates `event.source === iframe.
contentWindow` and (for cross-origin tier) `event.origin`. Request/response
correlated by `id`; fire-and-forget events omit it. One in-flight `requestSave`
at a time.

**host → plugin:** `init({doc, markdown, tokens, config, capabilities})`,
`themeChanged({tokens})`, `requestSave()`, `uploadResult({reqId,url|error})`,
`teardown()`.
**plugin → host:** `ready()`, `docChanged({dirty})`,
`save({doc, markdown})`, `requestUpload({reqId,name,type,bytes})`,
`resize({height})`, `focusChanged({focused})`, `navigateIntercept({unsaved})`.

**Capability table (declared in manifest, granted by host, mapped to #37's
`capability:scope` registry):**

| Capability | Grants | Without it |
|---|---|---|
| `document:read` | receives `init.doc` | frame starts empty |
| `document:write` | host accepts `save` | read-only editor |
| `upload:images` | `requestUpload` honored (scoped to image mime) | paste-image disabled |
| `theme:read` | receives `tokens` + `themeChanged` | falls back to default tokens |
| `navigation:intercept` | `navigateIntercept` can block SPA nav on unsaved | nav proceeds, may lose edits |

**Explicitly unreachable (the third-party guarantee):** host cookies, CSRF
token, `localStorage`, host DOM/globals, the DB, other plugins' data, the
filesystem, arbitrary network (all fetch from inside the opaque-origin frame is
same-origin-blocked and CSP-limited; the ONLY outbound path is a granted
capability RPC).

## 4. Q3 — document / interchange model

- **Canonical = ProseMirror doc JSON**, persisted by the host as an **opaque
  blob** (a `text`/`jsonb` column via the entity system — the host doesn't parse
  it). This is what preserves full fidelity (tables, callouts, toggles, color).
- **Portability (#39):** on every `save`, the editor ALSO emits a **markdown
  export** (lossy for non-representable blocks, documented). The host stores
  markdown alongside the JSON. Export/import + diff/grep/anti-lock-in all work on
  the markdown; the JSON is the render/edit fidelity layer. If the JSON is ever
  lost or the engine swapped, the markdown still reconstructs the document's
  representable core.
- **Form/entity integration:** the host mirrors the markdown export into a hidden
  `<textarea name=cfg.Name>` (and optionally the JSON into a second hidden field)
  synced on `docChanged`, so a standard form POST / entity CRUD captures it with
  zero special handling.
- **SSR read view:** two-tier. The representable core renders server-side via
  `ui.Markdown` (no-JS-safe, SEO-safe, existing code). Full-fidelity blocks that
  markdown can't express (callouts/toggles/colored) need a **server-side
  JSON→HTML renderer** in the plugin's Go half (a pure function over the doc
  JSON, emitting design-token HTML) — this is real work but it's what makes the
  no-JS view lossless. v-early it can fall back to the markdown render; the JSON
  renderer is a fast-follow.

## 5. Q4 — theming across the boundary

- Host serializes the **resolved design tokens** (the `:root { --color-*, --font-
  *, --radii-*, ... }` set, already computed by `style.CSSCustomPropertiesOf`)
  and posts them in `init.tokens`. The frame writes them onto its own
  `:root`/`documentElement` style. The editor's CSS is authored **token-only**
  (`var(--color-text)` etc.) — identical discipline to `framework/ui/markdown.go`
  — so it inherits the exact palette. Reuse the `ui-markdown` prose CSS verbatim
  inside the frame so read-view and edit-view are pixel-identical.
- **Light/dark sync:** on toggle, the host's colorscheme module posts
  `themeChanged({tokens})` (or just the `data-color-scheme` value); the frame
  re-applies. No `prefers-color-scheme` gating inside the frame (matches the
  repo rule: never gate light/dark on the media query).
- **Containment:** because it's an opaque-origin frame, the editor CSS physically
  cannot reach host styles and vice-versa — theming isolation is free. The only
  thing that crosses is the token *values*, by explicit message.

## 6. Manifest additions (extend the 0.17.0 module manifest)

`clientBundle` (path), `frameDoc` (srcdoc template path), `mountSelector`,
`isolation: "opaque-origin" | "cross-origin"` (default opaque),
`capabilities: []` (from the table above), `frameworkCompat` (semver range),
plus the existing `enable/disable` gating. Host serves all assets same-origin at
`/__gofastr/plugin/<name>/<file>`; adds `frame-src 'self'` to CSP.

## 7. Phase-0 spike (make-or-break, before the big build)

One GoFastr page. A sandboxed opaque-origin iframe hosting a MINIMAL ProseMirror
(paragraph + heading + bold only). Prove, end to end: (a) frame gets tokens via
`init`, looks on-theme in light AND dark; (b) typing works, caret/focus feel
native; (c) `docChanged`→hidden-field sync; (d) `requestSave`→`save`→host
persists JSON+markdown; (e) `requestUpload` round-trips a pasted image through
the host; (f) `resize` auto-height with no inner scrollbar; (g) `teardown` on SPA
nav leaves no leaked frame/listeners. If focus/paste/mobile feel unacceptable
HERE, renegotiate D3 now — this spike is small and exists precisely to fail
cheap.

## 8. Top-3 risks

1. **Iframe editing feel** (sizing jank, focus edge cases, mobile VK, paste) —
   the whole reason for the Phase-0 spike. If it feels worse than a native
   editor, the third-party stance costs UX and we decide with eyes open.
2. **Server-side full-fidelity SSR renderer** — making the no-JS view lossless
   for non-markdown blocks is real, ongoing work (every new block type needs a
   Go renderer). Mitigate: markdown-render fallback first, JSON renderer as
   fast-follow, and treat "renders on the server" as part of a block's
   definition-of-done.
3. **Protocol as a public versioned API** — once third parties depend on the
   bridge, breaking it is expensive. Mitigate: `v:` in every envelope, an
   explicit capability negotiation in `init`, and freeze the surface small (the
   table above is deliberately ~5 capabilities).
