# Decision record — how gofastr-plugins came to be

The connective tissue behind [`PLAN.md`](PLAN.md): the reframes, the locked
decisions, and the pressure-test that settled the hard part. Read this to
understand *why*, not just *what*.

## Origin

Started from GoFastr issue **#38** — "ui: Markdown editor component (render-only
today)". The issue as written asks for a textarea + live-preview pane. That
reading was wrong: the real target is a **full Rich Text rich-text editor**
(Notion/Confluence-class) where markdown is the storage/interchange format, not
the editing UX.

## The chain of reframes

1. **#38 as written** → "markdown source + live preview pane." Three competing
   plans were drafted (Claude, GLM-5.2, GPT-5.6-sol). GLM caught a real flaw:
   live-preview-via-input-trigger and native form-POST persistence **cannot
   coexist on one form** (runtime hijacks submit on any `data-fui-rpc` form —
   `runtime.js:429`; input-trigger requires such a form — `runtime.js:528`; HTML
   forbids nested forms). Verified. All three plans made obsolete by the reframe.
2. **Reframe → full Rich Text.** A real Rich Text is inherently a fat client, which
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
  and vouches for. BUILT the same day for the richtext plugin:
  `richtext.WithTrustedMount()` (host-side, never a default, never
  plugin-selectable) serves `editor-inline.js` (window.__gofastrRichText.mountTrusted),
  `editor-scoped.css` (frame stylesheet rescoped under `.gofastr-richtext-trusted`;
  the `:root` fallback-token block is dropped so page tokens inherit), and a
  frameless demo at `/__gofastr/plugin/richtext/trusted`. Same protocol
  envelopes, transport swapped from postMessage to direct calls
  (`protocol.setTransport` + `routeEnvelope`); overlays attach inside the
  scoped wrapper (`ui.setOverlayParent`). The full e2e journey suite runs
  against BOTH mounts in WebKit + Chromium.

## Decision: the PDF plugin — rasterize to redact, and never trust the frame
## with the network (2026-07-26)

The fourth sandboxed plugin is a PDF **viewer / editor / redactor**. A redactor
is the first plugin here where getting it subtly wrong leaks user data rather
than rendering something ugly, so the decisions below were made against measured
evidence (two spikes) rather than judgement alone.

**Packaging — one plugin, three host-selected modes.** `pdf.WithMode(ModeView |
ModeAnnotate | ModeRedact)`, defaulting to `ModeView`. Mode is chosen by the
host, never by the plugin or the user, and is enforced on *both* sides: the
frame hides UI, and the Go handlers reject payloads the mode does not permit.
UI-only gating would be authorization in the wrong layer.

**No trusted mount for this plugin, ever.** richtext has one (see the 2026-07-13
decision above). A redactor must not. The sandbox is not a tax here — it is the
product's main security property, see below.

**`connect-src 'none'` is the feature.** The framed CSP gives the frame no
network of any kind. A frame holding a confidential PDF therefore *cannot
exfiltrate it*: no fetch, no XHR, no WebSocket, no worker, no cookies, no
storage, no host DOM. The host pushes document bytes in over the postMessage
bridge and receives produced bytes back the same way. That forces the rest of
the architecture, and it is worth more than the convenience it costs:

- The canonical document is a small JSON **overlay** (`pdf-v1`), never the file
  bytes; the PDF is an external resource the host resolves. All overlay geometry
  is in **PDF user space** (points, bottom-left origin) plus the page's own
  `/Rotate`, never CSS pixels, so it survives zoom, rotation and re-render and
  maps 1:1 into pdf-lib at export.
- `GET /doc/{id}` is called by the privileged host adapter, never by the frame.
- **Download, print and clipboard-write do not work in the frame** (the framed
  CSP's own `sandbox allow-scripts` token grants no `allow-downloads`,
  `allow-modals` or `allow-popups`; clipboard-write was observed rejected in
  both engines). All three become host capabilities over the bridge.
- **Code splitting is impossible** — a dynamic `import()` is a CORS-mode module
  fetch that an opaque origin can never satisfy. Everything ships in one bundle
  per entry. This is not a bundler preference; it is a hard constraint.

**Redaction engine — rasterize affected pages. MIT/Apache only.** Pages carrying
a redaction are rendered at a chosen DPI, masked, and embedded as images into a
**newly built** document; untouched pages are `copyPages`'d losslessly. The
alternatives were measured and rejected:

- **MuPDF-wasm** is the only turn-key true (text-preserving) redaction engine —
  and it is AGPL-3.0 or a quote-based Artifex commercial licence, 4.7 MB gzip.
- **PDFium-wasm** is licence-clean (BSD-3) but 2.0 MB gzip and exposes no
  redaction API; you would hand-roll the geometry MuPDF already does.
- **Rebuilding content streams from pdf.js `getOperatorList()`** is not
  tractable: pdf.js has no write-back, so it means re-implementing a PDF
  serializer, splitting `TJ` arrays mid-run and re-subsetting fonts.
- And decisively: **no WebAssembly can execute in the frame at all.** Tested
  directly — `WebAssembly.instantiate` throws `CompileError` under
  `script-src <origin>`; it needs `'wasm-unsafe-eval'`, which is a gofastr *core*
  policy change (and would still leave `eval()` blocked). So both wasm engines
  were unavailable regardless of licence.

The honest guarantee, which `docs/pdf.md` states plainly: content under a
redaction rect is absent from the output and the plugin proves it before
releasing the file; redacted pages become images and lose text searchability;
untouched pages keep full fidelity.

**Verification is client-side, gated in CI, and assumes nothing.** Six checks
before any bytes leave the frame: byte search over **decompressed** streams,
per-page text extraction, per-rect intersection ("covered but present"),
metadata/XMP, annotation contents, and incremental-update residue. Verification
failure emits **no file**. Two traps found by measurement:

1. **A naive `strings | grep` misses the leak entirely.** pdf-lib writes text as
   hex strings (`<546F6B656E3A…> Tj`) and packs the Info dict into a compressed
   object stream as UTF-16BE hex. You must inflate and decode before searching,
   or you ship a verifier that verifies nothing.
2. **`/Annots` survives `copyPages`** — proven by planting a secret in an
   annotation and watching it come through a fresh rebuild. An annotation's
   `/Contents` is exactly where a second copy of a sensitive string hides, so
   redact mode strips annotations by default. A fresh rebuild *does* drop XMP,
   `/Outlines`, `/AcroForm`, `/EmbeddedFiles` and JS `/AA` for free; the Info
   dict must be deleted explicitly because clearing its keys leaves stubs.

Absence is asserted **per rect**, not globally — the same string may legitimately
appear elsewhere un-redacted, and a global check would false-fail. Other
occurrences are reported as a warning, which is also why the UI offers "redact
the N other occurrences of this text": a user who redacts one instance and
misses three is the likeliest real-world failure.

The counter-example is a first-class regression test: a black rectangle drawn
over text, asserted to STILL leak that text three ways (pdf.js extraction,
decompressed-stream grep, real browser select-and-copy). It exists so we can
never quietly regress into shipping cosmetic redaction.

**The scanned-document trap (and why the plugin would otherwise have been
useless for its main job).** pdf.js decodes JPEG 2000 and JBIG2 — the codecs
real scans use, and scans are what people redact — through WebAssembly, which
cannot instantiate here. pdf.js *does* ship pure-JS fallbacks for both, but
reaches them with a **dynamic `import()`**, which an opaque origin can never
satisfy either. The observed result was the worst possible failure mode: a
scanned page rendered as a **blank white page with no error, no console message
and no CSP violation** — a user would "redact" a blank page and believe it
worked. (`verbosity: 0`, set to keep a console-noise gate green, had also
silenced the one warning that hinted at it.)

Fixed in `pdf/js/build.mjs`: an esbuild plugin rewrites that single dynamic
import into a static dispatcher over the two inlined fallbacks, and
`getDocument` passes `useWasm: false`. The rewrite is **asserted at build time**,
so a pdfjs-dist upgrade that reshapes the expression fails the build loudly
instead of silently restoring blank scans. Cost: ~156 KB gzip. Verified in
WebKit against real fixtures (`pdf/testdata/scan-jpx.pdf` went from 0 to 181,053
non-white pixels; `scan-jbig2.pdf` renders at 316,750), both kept as regression
fixtures. **This is why we did not need to ask core for `'wasm-unsafe-eval'`.**

## Decision: a capability the cage cannot hold is delivered TO it (2026-08-29)

Settled by the `scanner` plugin, and the shape generalises.

An opaque-origin frame cannot have the camera. Measured four ways: a plain
same-origin iframe gets it; `sandbox="allow-scripts"` is refused with
`SecurityError: Invalid security origin`; adding `allow="camera *"` changes
nothing; and only `allow-same-origin` works, which is the flag the platform
bans. Upstream reached the same conclusion independently (gofastr#273).

The rejected option was a `Permissions []string` manifest field. It would have
shipped as a no-op — not because of the default `Permissions-Policy`, but
because the frame cannot hold the feature at all. Adding a field that does
nothing is worse than having no field, because it reads like a capability.

**The decision: the host acquires the capability and streams its OUTPUT over the
bridge.** The camera lives on the host page, where the permission prompt is
against an origin a user can read; grayscale frames cross to the frame; the
decode happens inside; the result comes back as text. Capture outside,
processing inside, exfiltration still impossible.

This is the same shape as pdf's document bytes and genui's composition tree, and
it is now the answer for any capability in this class — geolocation, microphone,
a native picker. The cage does not get widened to reach a device; the device's
output is handed in.

One consequence a host must know: the framework's default `Permissions-Policy`
denies the camera to the **host document** too, so it fails there as a console
error rather than a prompt. That is opt-in per host, and the manifest has no way
to declare the requirement — filed as gofastr#294.

## Decision: no `'unsafe-eval'`, ever, and what that costs (2026-08-29)

The framed CSP grants no string eval, and the narrow wasm tier proposed in
gofastr#255 deliberately does not either.

The cost is concrete and worth stating rather than discovering: **a library that
compiles at runtime cannot run in the cage, however it ships.** AlaSQL is pure
JavaScript with no wasm anywhere and still cannot execute a query, because it
builds queries with `new Function`. "Pure JS" is not the property that matters;
"does not eval" is.

Measured, in the cage, under the real framed CSP: `eval` and `new Function` both
throw `EvalError`, with and without `'wasm-unsafe-eval'` — that tier grants
WebAssembly compilation and nothing else. SQLite via `sql.js` runs under it
(3.49.1, 9 ms init on chromium, 24 ms on webkit) and fails without it.

So the ordering for an engine plugin is: wasm under the narrow tier if the
engine has a wasm build, otherwise a library that does not compile at runtime,
and never `'unsafe-eval'`. A plugin that needs the last one is a plugin this
platform should not host.

## Threads, and how they closed

Nothing in this section is open. It is kept struck-through rather than deleted
because a decision log that silently drops what it once flagged is worth less
than one that shows what turned out to be wrong.

- ~~**Upstream: capability-denied status code disagrees with itself.**~~
  **RESOLVED.** This page's prose said "HTTP 412 on the route side"; the
  platform's own `pluginhost.WriteCapabilityDenied` — the helper every shipped
  plugin calls — wrote **403**. (The original entry also blamed
  `design/protocol-v1.md` §5. It never contained the string: `git log -S412`
  finds it only in `plugin-platform.md`. Recorded because a wrong citation in a
  decision log outlives the decision it cites.) The `pdf` plugin followed the implementation because uniformity
  matters more to a host writing one error branch than either code does alone.
  Core has since reconciled on 403 and says so in its own plugin-platform page
  ("closed with `403 E_CAPABILITY_DENIED`. This is the reconciliation").
  Found 2026-07-26, confirmed resolved against gofastr v0.79.0 on 2026-09-01.


- ~~**GPT-5.6-sol's crux take is PENDING**~~ — dropped 2026-09-01. It was parked
  on a rate-capped provider window in July and never picked back up. The
  isolation model it would have critiqued has since been validated the harder
  way: measured on both engines, and recorded in
  [`plugin-platform.md`](plugin-platform.md) § "What the cage can and cannot
  host".
- ~~**GitHub remote** not yet created (repo is local-only).~~ Created; the repo
  publishes releases, with `plugins.json` as the asset hosts fetch. v0.5.0 is
  current.
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
  - Phase 4 (collaboration/CRDT, presence) shipped as the `whiteboard` plugin:
    the host owns the SSE fan-out, replay-on-join and presence, and the cage
    collaborates with people it cannot reach. See
    [`whiteboard.md`](whiteboard.md).
