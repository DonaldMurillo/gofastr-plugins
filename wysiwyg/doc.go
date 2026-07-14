// Package wysiwyg is a full WYSIWYG block editor plugin for GoFastr, built on
// ProseMirror and delivered as a genuinely third-party, isolated heavy-JS
// plugin.
//
// Architecture (see ../docs/PLAN.md and ../docs/design/protocol-v1.md for the
// authoritative Phase-0 contract):
//
//   - The editor's JavaScript is a prebuilt bundle embedded via go:embed and
//     served same-origin (CSP-clean). It runs inside an opaque-origin sandboxed
//     iframe (sandbox="allow-scripts" WITHOUT allow-same-origin), so it cannot
//     reach host cookies, localStorage, the CSRF token, or the host DOM.
//   - Host and editor communicate ONLY over a versioned postMessage capability
//     bridge. Capabilities reuse GoFastr's existing resource:verb auth scopes
//     (document:read, document:write, upload:images, theme:read).
//   - The canonical document is ProseMirror block-JSON (stored opaquely); a
//     markdown export is emitted alongside for portability, form/entity
//     round-tripping, and the no-JS SSR read view.
//
// Identity/route constants (Name, Version, RoutePrefix, the *URL consts,
// SchemaVersion) live in plugin.go alongside the [Plugin] type.
//
// Status: Phase 0 (the isolation spike). The public surface is defined by
// protocol-v1.md §10.
package wysiwyg
