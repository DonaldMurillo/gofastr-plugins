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

## Open threads

- **GPT-5.6-sol's crux take is PENDING** — its provider was rate-capped during
  the session (openai-codex 5h window). Add it when the window reopens; a genuine
  *alternative* isolation model would be more valuable than a third agreement.
- **GitHub remote** not yet created (repo is local-only).
- **Phase 0 — the isolation spike** is the next build step (sandboxed
  ProseMirror + the ≤16 ms measurement rig, in `example/`).
