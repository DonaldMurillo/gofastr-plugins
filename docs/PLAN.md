# Master plan — Third-Party Heavy-JS Plugin Platform + Rich Text Editor (plugin #1)

Full Rich Text block editor (Notion/Confluence-class), built on **ProseMirror**,
running as a **genuinely third-party** plugin (isolated + capability-scoped,
even though we maintain it), on top of a heavy-JS plugin platform that lives
outside the SSR-thin, Go-pure core.

## Decisions locked (this session)

- **D1 — Engine: ProseMirror core** (not TipTap/Milkdown). We own the schema +
  plugins. Max control, max longevity, more assembly work — accepted.
- **D2 — Build it FULL, no lean v1.** Markdown is *a* serialization (export/
  import + SSR read view), **not** the storage constraint. The **block model
  (ProseMirror doc / JSON) is the source of truth**, so callouts, toggles,
  tables, colored text, columns, slash menu, drag handles are all in scope.
  "Use what we need to get the right experience."
- **D3 — Treat the plugin as genuinely third-party.** No trusted-first-party shortcut.
  The isolation boundary, capability grants, versioned host↔plugin protocol, and
  "no reaching into host internals" must ACTUALLY work. The editor is the
  forcing function that proves the third-party contract — this is the client
  mirror of #37 (which does the server/Go side).
- **D4 — Packaging: ONE new repo** (`gofastr-plugins`) holding the plugin
  packages + a single example gofastr app that mounts them. Heavy JS is a
  `go:embed`'d asset in the plugin package; gofastr core `go.mod` + ≤12 KB budget
  stay untouched (core is a normal dependency of the new repo). See "Repo
  topology" below.

## 0. Guiding principle (unchanged)

Build the **editor concrete first**; **extract the platform** from what it
actually needs. But now the platform surface includes the *isolation +
capability protocol*, so Phase 0 must spike THAT, not just the editor.

---

## 1. The three concerns (was two)

- **A — the editor** (`gofastr-plugins/richtext`): ProseMirror schema + plugins +
  UI, built to run as untrusted code using ONLY granted capabilities.
- **B — the plugin platform / host** (extracted into `framework/`): loads a
  third-party bundle into an isolation boundary, grants capabilities, brokers
  the host↔plugin protocol, serves assets same-origin, manages lifecycle +
  enable/disable + versioning.
- **C — the trust boundary itself** (the crux): how untrusted DOM-touching JS is
  contained on the client, and how #37's server-side capability:scope registry
  extends to it.

---

## 2. The crux — client-side third-party isolation (the real design work)

ProseMirror needs a live DOM (contenteditable), so a pure Web Worker can't host
the *view*. Untrusted DOM-touching code → the natural containment is an
**iframe sandbox** (`sandbox="allow-scripts"`, srcdoc or a plugin-origin
document), the editor inside it, talking to the host over a **postMessage RPC
protocol**. That buys real isolation but imposes hard UX costs that must be
solved, not hand-waved:

- **Sizing** — iframe auto-height to content (ResizeObserver → postMessage).
- **Focus & selection** — caret, focus ring, click-through, `/`-shortcuts across
  the boundary.
- **Theming** — design tokens must cross IN so the editor looks like the design
  system (Hard rule 7) even when isolated. Host posts the resolved `--*` token
  set; editor applies them inside. Light/dark sync on toggle.
- **Copy / paste / drag** — clipboard + DnD across an iframe boundary.
- **Mobile** — virtual keyboard + touch selection inside a sandboxed frame.
- **Image/file upload** — editor has NO direct network/storage capability; it
  must *request* an upload through a granted capability, host performs it.

The alternative — same-origin bundle with a CSP-scoped capability shim (no
iframe) — is far better UX but is NOT real isolation (shared origin, shared DOM,
shared globals); it only works for the *trusted* tier we explicitly rejected in
D3. **So the isolation model is the #1 thing to settle**, and it's the pressure-
test's job.

### The host↔plugin protocol (capabilities, versioned)
A small, explicit RPC surface:
- `host→plugin`: `init(docJSON|markdown, tokens, config, capabilities)`,
  `themeChanged(tokens)`, `requestSave()`, `uploadResult(url)`, `teardown()`.
- `plugin→host`: `ready()`, `docChanged(dirty)`, `save(docJSON, markdownExport)`,
  `requestUpload(file)`, `resize(height)`, `focusChanged(bool)`,
  `navigateIntercept(unsaved)`.
- **Source validation:** check `event.source === iframe.contentWindow`, NOT
  `event.origin` — an opaque-origin frame's origin is the string `"null"`, so
  origin-string checks are a trap (GLM caught this).
- **Capabilities REUSE the existing scope registry**, don't invent a parallel
  one: `auth.md`'s `resource:verb` token scopes (`posts:read`, `*:read`,
  `auth.HasScope`/`auth.RequireScope`, verified real) already express exactly
  this. Editor grants = `document:read`, `document:write`, `upload:images`,
  `theme:read`, `navigation:intercept` — same grammar, same checker. This is
  also the vocabulary #37 maps onto server-side.
- Everything else (host DB, cookies, CSRF token, `localStorage`, host DOM/
  globals, other plugins' data, filesystem, arbitrary network) is
  **unreachable** — that's the third-party guarantee.

---

## 3. B — the platform / host (extracted Phase 2, from Phase-0/1 reality)

Built on existing spine, plus the isolation host:
- **Registration** — `framework.Plugin`/`Battery` (`framework/plugin.go:64`,
  `framework/battery.go:33`).
- **Same-origin asset serving** — plugin `go:embed`s `dist/*`; host serves at
  `/__gofastr/plugin/<name>/<file>`. CSP-clean (first-party).
- **Isolation host** — creates the sandboxed iframe, injects the bundle, wires
  the postMessage protocol, enforces the granted capability set.
- **Manifest** (extend 0.17.0 module manifest): `clientBundle`, `mountSelector`,
  `capabilities[]`, `isolation: iframe|trusted`, `frameworkCompat` (semver),
  reuse existing enable/disable gating.
- **Registry/discovery** — curated index (`plugins.json` + docs page): module
  path, version, compat, capabilities requested. Convention, not a service.

---

## 4. A — the editor (full)

- **Document model** — ProseMirror schema covering the full block set (marks;
  headings; lists incl. task lists; quote; code block w/ language; divider;
  tables; callouts; toggles/collapsibles; images; and colored/inline styles).
  **Block-JSON is canonical.**
- **Markdown as interchange** — serialize doc→markdown (export/import, SSR read
  view) for the representable subset; features beyond markdown degrade
  gracefully on export (documented), full fidelity in JSON. Satisfies #39
  anti-lock-in for the portable core.
- **SSR read view** — first paint renders stored content read-only (markdown via
  `ui.Markdown`, or a server-side JSON→HTML renderer for full-fidelity blocks).
  No-JS = real content; hydrate swaps in the isolated editor.
- **Editing UI** — bubble toolbar, `/` slash menu, block drag handles, table
  controls — all inside the boundary.
- **Save** — debounced autosave + explicit; `save(docJSON, markdownExport)` over
  the protocol; host persists (JSON canonical, markdown alongside for export).
  Server enforces size limits; the render path stays the only HTML producer.
- **Form/entity integration** — host mirrors the markdown export (and/or a JSON
  ref) into a hidden field so it round-trips through standard form POST / entity
  CRUD.
- **Theming** — tokens crossed into the frame; editor CSS uses `var(--*)` only.
- **A11y + mobile** — dedicated gates (see risks).

---

## 5. Phasing

- **Phase 0 — the make-or-break spike.** In the NEW `gofastr-plugins` repo: its
  example app renders a page hosting a **sandboxed iframe** ProseMirror.
  Prove end-to-end: mounts; host tokens crossed in
  (looks on-theme, light+dark); edit; serialize (JSON + markdown export); save
  via postMessage→host RPC; auto-height; focus/selection acceptable; teardown on
  SPA nav. This validates D3's isolation is *usable*, not just secure. **Go/no-go
  gate = measured p99 keystroke latency ≤16 ms inside the frame** (GLM's crisp,
  measurable bar) — not a vibe check. If UX/latency is unacceptable here, that's
  the moment to renegotiate D3 — cheaply.
- **Phase 1 — full editor inside the boundary.** Whole block set + slash + tables
  + callouts + drag; block-JSON store + markdown export; SSR read view;
  autosave; form integration; theming; a11y + mobile passes; chromedp tests.
- **Phase 2 — extract platform B into gofastr core** (only what's proven
  needed). Isolation host, capability protocol, manifest extension, same-origin
  serving, lifecycle — distilled from Phase 0/1's plugin-side glue, moved into
  `framework/` so plugin #2 doesn't reimplement it. This is the FIRST time this
  effort touches the gofastr repo. Update `framework/ARCHITECTURE.md` +
  `core-ui/ARCHITECTURE.md` (+ any new `data-fui-*`) same commit (gofastr-docs
  rule).
- **Phase 3 — registry + #37 alignment + second plugin.** `plugins.json` +
  registry docs page (in gofastr core); reconcile the capability model with #37's
  server side; add a SECOND plugin to the `gofastr-plugins` example app to prove
  the extracted platform generalizes (the completeness canary in action).
- **Phase 4 — collaboration + beyond.** Yjs/CRDT over a granted capability;
  presence (app-side per #38); more block types.

---

## 6. Verification

- **Isolation (Phase 0):** assert the plugin frame canNOT reach host cookies/DOM/
  globals; capability calls are the only channel; `cmd/check-csp` clean; core
  `budget_test.go` UNCHANGED (heavy JS never enters core).
- **Round-trip:** golden JSON+markdown; `parse→serialize` identity for the
  representable set; documented lossy edges for the rest.
- **Editor interaction (chromedp, isolated run):** type/select/slash/table/drag →
  assert real DOM blocks + serialized output + autosave RPC body + clean SPA-nav
  teardown.
- **Dogfood (Phase 3):** pixels not probes — screenshot editor light/dark,
  desktop/mobile in meridian.

---

## 7. Docs (same commit as code)

`framework/docs/content/plugin-platform.md` (isolation + capability protocol +
trust tiers + #37 relation), `richtext-editor.md`, `framework/ARCHITECTURE.md`,
`core-ui/ARCHITECTURE.md`, the `gofastr-plugins` registry page, CHANGELOG.

---

## 8. Risks / open

- **R1 — iframe editor UX (NEW #1 risk).** Real isolation vs. a fluid editing
  experience is the central tension. Sizing/focus/paste/mobile inside a sandbox
  are all solvable but each is real work; the sum could feel worse than a native
  editor. Phase-0 spike exists to find this out before committing.
- **R2 — a11y of a custom editor inside an iframe.** Hard; must not regress
  axe-clean. Dedicated gate.
- **R3 — full-fidelity storage vs. markdown portability.** JSON canonical means
  the host stores an opaque blob; markdown export is the portability guarantee
  (#39). Keep export honest.
- **R4 — scope.** "Full Notion" is a large, multi-phase build (tables, slash,
  collab each substantial). Framed by phase; Phase 0 de-risks the foundation
  before the big spend.
- **R5 — protocol/version churn.** The host↔plugin contract is now a public,
  versioned API (third-party consumers depend on it); design for compat from
  the start, align with #37.
- **Open (pressure-test targets):** iframe-sandbox vs. same-origin-CSP-shim
  isolation; the exact capability/RPC surface; JSON↔markdown interchange design;
  how tokens/theme cross the boundary; server-side full-fidelity renderer vs.
  markdown-only SSR.

---

## Repo topology (settled)

**Two repos, and gofastr core is untouched for v1.**

1. **gofastr core** (this repo) — the framework. NO plugin code, NO heavy JS,
   go.mod stays pure. v1 likely needs **zero core changes**: a plugin uses the
   EXISTING extension points — `RegisterPlugin`/`Battery`, plugin-registered
   routes to serve its `go:embed`'d bundle same-origin, `WithExtraScripts`
   (`uihost.go:206`) to inject its host-side broker JS (same-origin → CSP-clean),
   and its own RPC handlers (which carry the host's auth context, so capability
   checks are just `auth.HasScope` in the plugin's Go half). To be CONFIRMED in
   the Phase-0 spike — if a hook is missing, that missing hook is the first real
   "Layer B" core change, added deliberately. Later: the shared broker/iframe/
   manifest glue is EXTRACTED into core once ≥2 plugins prove the pattern, plus a
   registry docs page.

2. **the plugins repo** (NEW, e.g. `gofastr-plugins`) — its own `go.mod`
   requiring gofastr. Contains:
   - the **plugin packages** (the ProseMirror editor first; future heavy-JS
     plugins alongside), each embedding its prebuilt JS via `go:embed`; and
   - **ONE example gofastr app** that imports and mounts the plugins — the
     integration host, visual/chromedp test surface, and **completeness canary**
     (a plugin that can't mount cleanly here is a platform gap). Exactly mirrors
     how gofastr keeps `examples/` beside its packages.

   The example app imports the sibling plugin packages directly (same module —
   no go.work, no cross-repo `replace` needed). The whole third-party contract is
   provable here: assert the mounted frame can't reach host cookies/DOM, CSP is
   clean, and — since gofastr core is a normal dependency — its own budget tests
   are unaffected by construction (the heavy JS never enters the core module).

**Consequence:** the earlier "single go.mod / go.work" worry evaporates — the
plugins + their example app live in their OWN module in the new repo, so core
purity (D4) is structural, not aspirational, with no changes to this repo.

---

## 10. Pressure-test

omp glm-5.2 + gpt-5.6-sol, scoped to the CRUX only (engine + full + third-party
are locked): **the client isolation model, the host↔plugin capability protocol,
the document/interchange model, and theming across the boundary.** Independent
takes matter most exactly here.
