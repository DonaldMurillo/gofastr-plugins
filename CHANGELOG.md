# Changelog

All notable changes to gofastr-plugins. Follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are
`0.x-phase` until the platform API stabilises.

## [Unreleased]

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

### Added — Phases 1–3 complete (2026-07-13)

- **Trusted in-page mount (the sandbox opt-out)** — `wysiwyg.WithTrustedMount()`
  serves `editor-inline.js` (`window.__gofastrWysiwyg.mountTrusted`), a
  page-scoped stylesheet (`editor-scoped.css`, rescoped under
  `.gofastr-wysiwyg-trusted`; framework-token fallbacks dropped so host tokens
  inherit, plugin-local `--wysiwyg-*` slot tokens kept), and a frameless demo
  at `/__gofastr/plugin/wysiwyg/trusted`. Same protocol envelopes over a
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
- **TypeScript strict everywhere Node runs** — `wysiwyg/js`, `mermaid/js`,
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

- **`wysiwyg` — the WYSIWYG editor** (`wysiwyg/`). ProseMirror block editor,
  block-JSON canonical, markdown export + the pure-Go SSR read view (`wysiwyg/ssr`).
  Plugin #1 and the forcing function that proved the third-party contract.
  See [`docs/wysiwyg-editor.md`](docs/wysiwyg-editor.md).

- **`wysiwyg/ssr` — the SSR read renderer** (`wysiwyg/ssr/`). Pure, deterministic
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

- **Registry** — [`plugins.json`](plugins.json), the curated index (a convention,
  not a service): module path, version, isolation, capabilities, route prefix,
  schema, per plugin.

- **Docs** — [`docs/plugin-platform.md`](docs/plugin-platform.md) (isolation +
  capability protocol + trust tiers + header/CSP contract + #37 relation +
  quickstart), [`docs/wysiwyg-editor.md`](docs/wysiwyg-editor.md),
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
