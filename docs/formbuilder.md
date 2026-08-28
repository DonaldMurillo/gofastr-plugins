# Form builder plugin (`formbuilder`)

A form-schema authoring tool — and the first plugin in this repo whose output
the **framework itself consumes and enforces**, rather than content it hands
back for display. The canonical doc is a form schema (`formbuilder-v1`);
GoFastr's own `ui.Form` renders it, and the host re-derives every rule from
it in Go on every submit. A form designed in a sandboxed browser frame is
enforced on the server, with **no client trust anywhere in the path**.

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/formbuilder`
- **Route prefix:** `/__gofastr/plugin/formbuilder`
- **Isolation:** `sandbox-iframe-opaque` (`allow-scripts`, no `allow-same-origin`)
- **Canonical doc (schema `formbuilder-v1`):** data only —
  `{version, fields: [{type, name, label, required, help, rules}]}`.
  **Never markup.**
- **Capabilities:** `document:read`, `document:write`, `theme:read` —
  all always-on; there are no optional grants.
- **Demo:** `/formbuilder` (design, in the frame) and `/formbuilder/live`
  (the proof route: the saved schema rendered by `ui.Form`, validated in Go).

## The loop this plugin exists to close

1. **Design in the cage.** The builder — a drag-to-reorder field list plus a
   property panel, deliberately no vendor form-builder library — runs in the
   opaque-origin sandboxed iframe like every other plugin here. The framed
   CSP sets `connect-src 'none'`, so the frame cannot save anything itself:
   every autosave crosses the postMessage bridge as a `save` event, which
   the host adapter POSTs to `POST /save`.
2. **Go validates the save.** Unknown field type, duplicate name, empty or
   invalid name, malformed rule, unknown version, markup in a label — each
   is a `400` with a specific error code, and nothing is persisted. A
   schema that gets past the frame still gets refused here; the refusal
   code crosses back into the frame and renders in the builder's status
   line (`Refused: E_DUPLICATE_NAME`).
3. **The live form answers.** `/formbuilder/live` renders the **saved**
   schema through the framework's own `ui.Form` — `ui.FormField` +
   `html.Input`/`TextArea`/`Select`, `ui.Checkbox` — and on POST re-derives
   every rule from the schema in Go. Submit garbage from the browser, or
   from `curl` with the frame bypassed entirely: the answer is the same
   `422` with field errors.

## Document model (`formbuilder-v1`)

```jsonc
{
  "version": "formbuilder-v1",
  "fields": [
    {
      "type": "text",             // text | email | number | textarea | select | checkbox | date
      "name": "full_name",        // ^[a-z][a-z0-9_]{0,39}$ — the form-field identifier
      "label": "Full name",       // defaults to the name when empty; plain text only
      "required": true,
      "help": "",                 // optional helper line; plain text only
      "options": ["a", "b"],      // select only: 1–32 non-empty, unique
      "rules": {
        "minLength": 2,           // text-like fields only
        "maxLength": 80,
        "min": 1,                 // number fields only
        "max": 20,
        "pattern": "^[A-Z].*$"    // compiled with Go's RE2 at SAVE time
      }
    }
  ]
}
```

**Data only, by refusal.** `version` must be `formbuilder-v1` (empty is
stamped on save; anything else is `400 E_UNKNOWN_VERSION` — a saved schema
has to outlive the plugin that wrote it, so an unknown future version fails
loudly). Labels, help and options may not contain `<`
(`400 E_MARKUP`): the moment a schema could contain HTML, the proof would be
gone. The frame contributes structurally too — the builder builds its entire
DOM with `createElement`/`textContent` and never emits HTML from doc data.

### The refusal vocabulary

| code | meaning |
|---|---|
| `E_BAD_JSON` | envelope is not exactly one JSON value (trailing garbage refused; trailing whitespace allowed) |
| `E_UNKNOWN_VERSION` | doc `version` is not `formbuilder-v1` |
| `E_TOO_MANY_FIELDS` | more than 48 fields |
| `E_UNKNOWN_FIELD_TYPE` | type outside the seven |
| `E_EMPTY_NAME` / `E_INVALID_NAME` | name missing, or outside `^[a-z][a-z0-9_]{0,39}$` |
| `E_DUPLICATE_NAME` | the same name twice |
| `E_LABEL_TOO_LONG` / `E_HELP_TOO_LONG` | label > 120 / help > 240 runes |
| `E_MARKUP` | `<` in a label, help or option — schemas are data |
| `E_BAD_SELECT` | select without options, empty/duplicate option, or options on a non-select field |
| `E_BAD_RULE` | uncompilable pattern, inverted or fractional bounds, or a rule that does not apply to the field's type |

The persisted record is the **validated, normalised** doc — version stamped,
empty labels defaulted to the name — never the raw request body. The save
response reports the server's own count (`{status, docId, fields, rules}`),
which is what the demo page's proof strip displays.

## The live form (`/formbuilder/live`)

`RenderForm` (formbuilder/render.go) maps each field onto the framework's own
components; `ValidateValues` re-derives every rule from the schema and applies
it to the POSTed values:

- required (empty check; checkbox must be present), lengths in runes,
  numeric ranges, `regexp` patterns, `net/mail` email parse (round-tripped,
  so `Name <a@b>` does not pass), `YYYY-MM-DD` date parse;
- **select membership is enforced even on optional fields** — a crafted POST
  carrying a foreign value is refused, because membership is a schema
  constraint, not a UX hint.

A violation re-renders the form with `ui.FieldErrors` under a **422** verdict
banner; a clean submit renders the accepted values under a **200** banner.
The rendered inputs deliberately carry **no native constraint attributes** —
no `required`, no `pattern`, no `min` — and the `<form>` itself carries
**`novalidate`**, so even the type-builtin format checks an
`email`/`number`/`date` input would otherwise run on submit are off. Any of
those would make the browser the enforcer and mask exactly what this page
exists to demonstrate. Input *types* stay (`email`/`number`/`date` are
typing aids; the server re-parses anyway).

The page runs with no UIHost, so the ui.* components' registered stylesheets
are inlined by scanning the rendered form's own `data-fui-comp` markers
(`registry.Scan` → `Entry.CSSFor` under the demo theme) — the set can never
drift from what the form renders.

## Capabilities, and the authentication warning

| capability | always on? | gates |
|---|---|---|
| `document:read` | yes | frame receives `init.doc` |
| `document:write` | yes | `POST /save` |
| `theme:read` | yes | token bridging |

> **`pluginhost.Allow` is a capability gate, NOT authentication.** It passes
> for anonymous callers (and for unscoped sessions it is bounded only by the
> plugin's grant set). `POST /save` persists schemas and must be treated as
> unauthenticated until the HOST's own handler checks the session —
> `WithSaveHandler` is where that check belongs. The demo's
> `WithDevGrantAll()` skips the gate entirely and MUST NOT survive into a
> production mount.

## Mounting

```go
import "github.com/DonaldMurillo/gofastr-plugins/formbuilder"

app.RegisterPlugin(formbuilder.New(
    // Optional: persist schemas for real. The default is an in-memory map
    // keyed by DocID (the demo store). Check the session HERE — Allow is
    // not authentication.
    formbuilder.WithSaveHandler(func(ctx context.Context, req formbuilder.SaveRequest) error {
        return store.SaveFormSchema(ctx, req.DocID, req.DocJSON)
    }),
    // Optional: the starting canvas for fresh mounts (default: the demo's
    // investor-contact schema).
    formbuilder.WithDemoDoc(myDoc),
    formbuilder.WithDemoPage(), // design demo at /formbuilder + live form at /formbuilder/live
))
```

Drop the mount marker into a form:

```go
formbuilder.Mount(formbuilder.MountConfig{
    DocID:  "signup",           // persistence key
    Doc:    initialSchemaJSON,  // optional; reload round-trip
    Field:  "form_schema",      // hidden input the adapter mirrors into
    MinHeight: "560px",
})
```

Apps rendering through a `UIHost` inject the host scripts with
`formbuilder.UIHostOption()` — platform broker first, then this plugin's
adapter. Reading the schema server-side is `LoadDoc(ctx, docID)` (or your own
save handler's store); rendering it anywhere is `RenderForm(action, doc,
values, errs)`.

## Bundle

The frame bundle is **~13 KB raw / ~5 KB gzip** — no third-party code. A
drag-to-reorder list and a property panel are ordinary DOM work; a vendor
form-builder would bring a palette that fights the design tokens. Built as a
single minified IIFE like every other plugin here (an opaque origin cannot
satisfy a CORS-mode module fetch, so the bundle is monolithic by
construction).
