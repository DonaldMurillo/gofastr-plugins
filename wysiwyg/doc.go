// Package wysiwyg is a full WYSIWYG block editor plugin for GoFastr, built on
// ProseMirror and delivered as a genuinely third-party, isolated heavy-JS
// plugin.
//
// Architecture (see ../docs/PLAN.md for the full plan):
//
//   - The editor's JavaScript is a prebuilt bundle embedded via go:embed and
//     served same-origin (CSP-clean). It runs inside an opaque-origin sandboxed
//     iframe (sandbox="allow-scripts" WITHOUT allow-same-origin), so it cannot
//     reach host cookies, localStorage, the CSRF token, or the host DOM.
//   - Host and editor communicate ONLY over a versioned postMessage capability
//     bridge. Capabilities reuse GoFastr's existing resource:verb auth scopes
//     (document:read, document:write, upload:images, theme:read,
//     navigation:intercept).
//   - The canonical document is ProseMirror block-JSON (stored opaquely); a
//     markdown export is emitted alongside for portability, form/entity
//     round-tripping, and the no-JS SSR read view.
//
// Status: scaffolding. Phase 0 (the isolation spike) comes first.
package wysiwyg

// Name is the plugin's registry name.
const Name = "wysiwyg"

// Version is the plugin's semantic version.
const Version = "0.0.0-dev"
