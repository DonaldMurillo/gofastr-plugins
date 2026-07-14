# Decision record — how gofastr-plugins came to be

The connective tissue behind [`PLAN.md`](PLAN.md): the reframes, the locked
decisions, and the pressure-test that settled the hard part. Read this to
understand *why*, not just *what*.

## Origin

Started from GoFastr issue **#38** — "ui: Markdown editor component (render-only
today)". The issue as written asks for a textarea + live-preview pane. That
reading was wrong: the real target is a **full WYSIWYG rich-text editor**
(Notion/Confluence-class) where markdown is the storage/interchange format, not
the editing UX.

## The chain of reframes

1. **#38 as written** → "markdown source + live preview pane." Three competing
   plans were drafted (Claude, GLM-5.2, GPT-5.6-sol). GLM caught a real flaw:
   live-preview-via-input-trigger and native form-POST persistence **cannot
   coexist on one form** (runtime hijacks submit on any `data-fui-rpc` form —
   `runtime.js:429`; input-trigger requires such a form — `runtime.js:528`; HTML
   forbids nested forms). Verified. All three plans made obsolete by the reframe.
2. **Reframe → full WYSIWYG.** A real WYSIWYG is inherently a fat client, which
   collides head-on with GoFastr's SSR-thin / ≤12 KB core / no-client-logic model.
3. **Resolution → an official registry for heavy-JS plugins** that live OUTSIDE
   the core, keeping the core pure. The editor is plugin #1. ("Keep it Go-ing.")
4. **Third-party stance.** Treat the plugin as genuinely third-party — isolated,
   capability-scoped — even though first-party-maintained. This is the CLIENT
   mirror of GoFastr issue **#37** (server-side process-isolated modules).
5. **Topology → ONE new repo** (this one): plugin packages + a single example
   app that mounts them. Core gofastr untouched for v1.

## Locked decisions

- **D1 — Engine:** ProseMirror core (own the schema + plugins).
- **D2 — Build it full**, no lean v1. Block-JSON is canonical; markdown is an
  interchange/export + the SSR read view, not a storage constraint.
- **D3 — Genuinely third-party:** real isolation + capability grants, no trusted
  shortcut.
- **D4 — One new repo** (this), core untouched for v1 — uses existing hooks:
  `RegisterPlugin`/`Battery`, plugin-served routes for the `go:embed`'d bundle,
  `WithExtraScripts` for host-side broker JS, own RPC handlers + `auth.HasScope`
  for capability checks. Platform extraction into core is Phase 2.

## The isolation crux — result of a 2-model pressure-test

Claude (Opus 4.8) and GLM-5.2, briefed independently, **converged** on:

- **Opaque-origin sandboxed iframe** — `sandbox="allow-scripts"` WITHOUT
  `allow-same-origin`, served same-origin. Real isolation, zero extra infra.
- **Versioned `postMessage` RPC**; validate `event.source === iframe.contentWindow`
  (NOT `event.origin` — an opaque-origin frame's origin is the string `"null"`).
- **Capabilities reuse the existing `auth.md` `resource:verb` scope registry**
  (`document:read`, `document:write`, `upload:images`, `theme:read`,
  `navigation:intercept`) — same checker as the rest of the framework.
- **Block-JSON canonical** (opaque blob) **+ a markdown sibling column**.
- **Token-bridge theming**; a **host-side Go SSR renderer** for full-fidelity
  blocks markdown can't express.
- **Phase-0 go/no-go gate:** measured **p99 keystroke latency ≤16 ms** in the frame.

Full memos: [`design/isolation-crux-claude.md`](design/isolation-crux-claude.md),
[`design/isolation-crux-glm.md`](design/isolation-crux-glm.md).

Two independent models agreeing on the hardest call is a strong signal — with one
caveat: no *dissent* was heard.

## Phase 0 — DONE, gate CLEARED (2026-07-12)

The isolation spike is built and verified end to end (headless-Chrome e2e in
`example/smoke_test.go`). The opaque-origin sandboxed iframe + versioned
`postMessage` RPC works and is a usable editing surface:

- **Go/no-go latency gate: p50 = 3.5 ms, p99 = 8.6 ms** (target ≤ 16 ms) —
  **PASS.** All editing stays in-frame; the boundary carries only coarse events.
- Isolation proven from both sides: `sandbox="allow-scripts"` (no
  `allow-same-origin`), `iframe.contentDocument === null` from the parent, and
  in-frame probes confirm `document.cookie`/`localStorage`/`parent.document` are
  all unreachable.
- Round-trip (type → `docChanged` → host hidden fields), theme-token bridge
  (light/dark re-sync), and autosize all verified.

**Load-bearing gotchas discovered (feed these to every later phase):**
1. GoFastr's global security middleware sends `X-Frame-Options: DENY`, CSP
   `frame-ancestors 'none'`, and `Cross-Origin-Resource-Policy: same-origin` on
   EVERY response — which blocks the host from framing the editor AND blocks the
   opaque frame from fetching its own JS/CSS. The plugin's frame-asset handler
   overrides these (CSP `frame-ancestors 'self'` — which supersedes XFO — plus
   `Cross-Origin-Resource-Policy: cross-origin`). This is the client-side
   "missing hook"; it becomes a first-class `pluginhost` responsibility.
2. The frame CSP must allow `style-src 'self' 'unsafe-inline'` (ProseMirror sets
   inline style attributes; the theme bridge injects a `<style>:root{…}` block).
   `script-src` stays `'self'` — editor JS is external, so script isolation holds.
3. `EditorState.create` needs an explicit `schema` when there's no initial doc.
4. Driving input into an opaque OOPIF via chromedp requires disabling site
   isolation in the test browser (a harness concern only — the frame's opaque
   origin is unaffected); results are read via broker-stashed hooks on the
   iframe element (protocol §8a).

## Decision: secure by default, opt out (2026-07-13)

Re-examined the iframe question directly ("we control which plugins get
mounted — why the cage?"). Resolution, from the app owner:

- **Sandboxed opaque-origin iframe stays the DEFAULT** for every plugin. The
  threat it addresses is not "who installs the plugin" but what's *inside* it:
  heavy-JS plugins ship megabytes of unaudited npm transitive dependencies
  (mermaid alone is 2.6 MB), and deliberate installation is exactly how
  supply-chain compromises land. The browser-enforced boundary means a
  compromised dependency cannot reach host cookies/sessions/DOM.
- **Opt-out is allowed but must be explicit** — the "trusted mount" mode
  (same plugin API, in-page, no frame) for plugins the app owner compiles in
  and vouches for. BUILT the same day for the wysiwyg plugin:
  `wysiwyg.WithTrustedMount()` (host-side, never a default, never
  plugin-selectable) serves `editor-inline.js` (window.__gofastrWysiwyg.mountTrusted),
  `editor-scoped.css` (frame stylesheet rescoped under `.gofastr-wysiwyg-trusted`;
  the `:root` fallback-token block is dropped so page tokens inherit), and a
  frameless demo at `/__gofastr/plugin/wysiwyg/trusted`. Same protocol
  envelopes, transport swapped from postMessage to direct calls
  (`protocol.setTransport` + `routeEnvelope`); overlays attach inside the
  scoped wrapper (`ui.setOverlayParent`). The full e2e journey suite runs
  against BOTH mounts in WebKit + Chromium.

## Open threads

- **GPT-5.6-sol's crux take is PENDING** — its provider was rate-capped during
  the session (openai-codex 5h window). Add it when the window reopens; a genuine
  *alternative* isolation model would be more valuable than a third agreement.
- **GitHub remote** not yet created (repo is local-only).
- **Phases 1–3 COMPLETE (2026-07-13):**
  - Phase 1 — full editor + SSR read view + a11y gate (axe, zero
    serious/critical across framed/trusted/SSR + open menus) + mobile gate
    (iPhone/Pixel touch journeys) + the WebKit+Chromium user-journey e2e suite.
  - Phase 2 — platform extracted into gofastr core as
    `framework/pluginhost` (package + host broker + data-fui-plugin* attr
    registration + ARCHITECTURE/runtime-contract/CHANGELOG same-tree);
    this repo's `pluginhost` is now a thin alias.
  - Phase 3 — `plugins.json` registry manifest; core docs page
    `framework/docs/content/plugin-platform.md` (capability model reconciled
    with #37: grants reuse the battery/auth resource:verb scope grammar via
    auth.HasScope — no parallel registry); dogfood screenshots
    (light/dark × desktop/mobile × framed/trusted/SSR).
  - Phase 4 (collaboration/CRDT, presence) remains future work by design.
