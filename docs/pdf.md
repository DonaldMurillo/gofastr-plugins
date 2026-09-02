# PDF viewer / editor / redactor plugin

A PDF **viewer**, **annotator** and **redactor** built on
[pdf.js](https://mozilla.github.io/pdf.js/) (render) and
[pdf-lib](https://pdf-lib.js.org/) (write). It is the fourth sandboxed heavy-JS
plugin, mounted the same way as richtext, mermaid and monaco: inside an
**opaque-origin sandboxed iframe**, talking to the host only over the versioned
postMessage bridge.

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/pdf`
- **Route prefix:** `/__gofastr/plugin/pdf`
- **Isolation:** `sandbox-iframe-opaque` (`allow-scripts`, no `allow-same-origin`)
- **Canonical doc (schema `pdf-v1`):** an annotation **overlay**, not the file bytes
- **Capabilities:** `document:read`, `document:write`, `theme:read`;
  optional `pdf:export`

## The security property this plugin is built around

The framed CSP gives the frame **no network of any kind** — `connect-src 'none'`,
no workers, no `blob:`, and an opaque origin with no access to cookies,
storage, the CSRF token or the host DOM. A frame holding a confidential PDF
therefore **cannot exfiltrate it**. The host pushes the document bytes in over
the bridge and receives produced bytes back the same way; every byte crossing
the boundary does so through code the host controls.

That is the whole reason this plugin exists in the cage rather than as a trusted
host-page script, and it is why **there is no trusted-mount opt-out for `pdf`**
(richtext has one; a redactor must not).

Three consequences worth knowing before you build against it:

- **Download, print and clipboard-write do not work inside the frame.** The
  framed CSP's own `sandbox allow-scripts` token grants no `allow-downloads`,
  `allow-modals` or `allow-popups`. All three are host-side operations reached
  over the bridge.
- **The document is fetched by the host, not the frame.** `GET /doc/{id}` runs
  in the host page with the session and CSRF token attached; the frame cannot
  call it. Authorization stays at the data layer.
- **There is no code splitting.** A dynamic `import()` is a CORS-mode module
  fetch an opaque origin can never satisfy, so the bundle is monolithic by
  construction.

## Mounting

```go
app.RegisterPlugin(pdf.New(
    pdf.WithMode(pdf.ModeAnnotate),
    pdf.WithSource(func(ctx context.Context, id string) ([]byte, error) {
        return documents.Bytes(ctx, id)
    }),
    pdf.WithSaveHandler(func(ctx context.Context, req pdf.SaveRequest) error {
        return documents.SaveOverlay(ctx, req.DocID, req.Doc)
    }),
    pdf.WithDemoPage(),   // themed demo at /pdf
))
```

`ErrConflict` from the save handler signals optimistic-concurrency failure and
maps to HTTP 409 (`E_CONFLICT`), relayed to the frame as a `saveResult` so the
viewer warns instead of silently dropping the edit — the same contract as
richtext and monaco.

`WithExportHandler` is what grants the optional **`pdf:export`** capability —
the permission for produced bytes to leave the frame at all. `ModeRedact`
requires it and **panics at construction** without it, rather than letting a
host discover at runtime that redaction can never deliver a file. The handler
receives the bytes, the export `Kind`, a sanitised `Filename` hint, and the
verification `Report`.

A denied capability answers **403** with `E_CAPABILITY_DENIED` on every route.
Early design drafts said 412. The framework reconciled on 403, matching
`pluginhost.WriteCapabilityDenied`, which is what every shipped plugin calls.

## Modes

| mode | viewer | annotations / forms / page ops | redaction |
|---|---|---|---|
| `ModeView` (default) | ✅ | — | — |
| `ModeAnnotate` | ✅ | ✅ | — |
| `ModeRedact` | ✅ | ✅ | ✅ |

Mode is **host-chosen** — never plugin- or user-selectable — and enforced on
both sides: the frame hides the UI *and* the Go handlers reject payloads the
mode does not permit. `ModeRedact` additionally requires the `pdf:export`
capability, and constructing it without one panics at startup rather than
degrading quietly.

## The document model (`pdf-v1`)

The canonical document is a small JSON **overlay** that round-trips through the
hidden form field like every other plugin here. The PDF itself is an external
resource the host resolves.

```jsonc
{
  "schemaVersion": "pdf-v1",
  "src":   { "kind": "url|id", "ref": "invoice-9912", "sha256": "…", "pages": 12 },
  "annotations": [
    { "id": "a1", "page": 3, "type": "highlight", "rect": [72, 640, 220, 14], … }
  ],
  "formFields": { "applicant_name": "…", "agree": true },
  "redactions": [ { "id": "r1", "page": 3, "rect": [72, 638, 224, 18], "reason": "PII" } ],
  "pageOps":    [ { "op": "rotate", "page": 4, "value": 90 } ],
  "rev": 7
}
```

**All geometry is in PDF user space** — points, origin bottom-left, alongside
the page's own `/Rotate` — never CSS pixels. The overlay therefore survives
zoom, view rotation and re-render, and maps 1:1 into pdf-lib at export.

`src.sha256` binds an overlay to the exact bytes it was authored against. On
load the frame recomputes it and, on mismatch, refuses to apply annotations and
says so, rather than silently painting boxes at stale coordinates. (`crypto.subtle`
requires a secure context, so over plain `http://` to a non-localhost host the
digest degrades to a non-cryptographic one and the warning softens — it never
hard-fails.)

## Redaction

**Redaction here removes content. It does not paint over it.**

1. Author rectangles over the regions to remove; each may carry a reason label.
2. Arm and confirm — the dialog names the consequence: *N pages will be
   permanently rasterized at D DPI and their text removed.*
3. Pages **with** a redaction are rendered at `WithRedactDPI` (default 200),
   masked, and embedded as images into a **newly created** document at the
   original page size and rotation. Pages **without** one are copied through
   losslessly, text intact.
4. Metadata, XMP, outlines, annotations, AcroForm values, embedded files and JS
   actions are stripped; the file is fully rewritten, never incrementally saved,
   so no prior object revision survives.
5. The frame **verifies its own output before releasing it** (below). If
   verification fails, no bytes are emitted.

### The guarantee, stated honestly

> Content under a redaction rect is absent from the exported file — it cannot be
> selected, copied, searched, or recovered from the bytes, and the plugin proves
> this before handing the file over. Redacted pages become images at the chosen
> DPI and are no longer text-searchable; pages without redactions keep full text
> and vector fidelity. Redaction is irreversible.

Lossless, text-preserving redaction (excising only the covered glyphs) needs a
different engine. The only turn-key one is MuPDF, which is AGPL-3.0 or a paid
Artifex licence — and no WebAssembly can execute in this frame at all, so it is
doubly unavailable. The engine sits behind a seam so it can be swapped later;
see [`DECISIONS.md`](DECISIONS.md).

### Verification

Six checks run against the produced bytes, in-frame, before release:

| check | catches |
|---|---|
| byte search | needles in **decompressed** streams |
| text extraction | anything pdf.js can still read per page |
| rect intersection | *"covered but present"* — no text bbox may hit a rect |
| metadata | Info-dict needles and XMP |
| annotations | needles hiding in annotation contents |
| incremental | a second `%%EOF` or a `/Prev` — a surviving prior revision |

The report travels with the export and is deliberately **bounded** — verdicts
plus a capped sample of evidence, never an unbounded list of occurrences. It
rides an HTTP header, and an oversized report is replaced by a compact record
that keeps the verdicts and sets `truncated: true`, because for an audit trail a
silently dropped report would read as a pass.

Two things this catches that a naive implementation would not:

- **`strings | grep` is not a byte search.** pdf-lib writes text as hex strings
  and packs the Info dict into a compressed object stream as UTF-16BE hex. The
  bytes must be inflated and decoded first, or the check silently passes a
  leaking file.
- **Absence is asserted per rect, not globally.** The same string may
  legitimately appear elsewhere un-redacted; a global check would false-fail.
  Other occurrences are surfaced as a warning — and the UI offers to redact them
  all, because redacting one instance and missing three is the likeliest
  real-world mistake. The rule **fails closed**: a needle present in the bytes
  but extractable nowhere (invisible text) is a leak, not an excuse.

The counter-example is kept as a regression test
(`pdf/js/spike-fake-redaction.mjs`): a black rectangle drawn over text, asserted
to *still* leak that text three ways, with the verifier required to reject it.
It exists so this plugin cannot quietly regress into cosmetic redaction. The
shipped pipeline's output has also been judged by an independent implementation
of the same six checks — a verifier grading its own homework proves nothing.

## Scanned documents

Scans are the documents people actually redact, and they usually arrive as
**JPEG 2000** or **JBIG2**. pdf.js decodes both through WebAssembly, which
cannot instantiate under the framed CSP, and its pure-JS fallbacks are reached
by a dynamic `import()` an opaque origin cannot satisfy either. Left alone, a
scanned page renders as a **blank white page with no error at all**.

The build fixes this: `pdf/js/build.mjs` rewrites that single dynamic import
into a static dispatcher over the two inlined fallbacks, and `getDocument`
passes `useWasm: false`. The rewrite is asserted at build time, so a
`pdfjs-dist` upgrade that reshapes the expression fails the build rather than
silently restoring blank scans. `pdf/testdata/scan-jpx.pdf` and
`scan-jbig2.pdf` are the regression fixtures.

## Host CSP

None. Unlike [`geomap`](geomap.md), this plugin needs **no host CSP changes at
all** — it is fully same-origin and fetches nothing. That is a direct
consequence of the bytes-over-the-bridge design.

## Performance

Everything runs on the main thread — there are no workers here — so the viewer
renders through a cancellable queue and the redactor yields every 16 ms.
Measured in WebKit: a 1-page redaction takes ~265 ms; a 50-page document with
every page rasterized takes ~8.5 s, with the longest single main-thread block at
~194 ms. Rasterization is per page: render, encode, release, so a long document
does not accumulate canvases.

Page images default to **PNG** (measured smaller than JPEG on text-heavy pages
at ≥200 DPI, and lossless); JPEG with a quality setting is available for
photo-heavy scans.

## Bundle size

The frame bundle is **2732 KB raw / 869 KB gzip** — pdf.js, pdf-lib, and the
inlined pure-JS JPEG 2000 / JBIG2 decoders. It is served at its own route and is
deliberately **not** subject to the core-ui runtime budget, the same posture as
monaco.

Nothing can be split out of it: a dynamic `import()` is a CORS-mode module fetch
that an opaque origin cannot satisfy, so the single bundle is a property of the
isolation model rather than a bundler setting.
