# Image crop / annotate / redact plugin

An image **cropper**, **annotator** and **redactor** with no upstream editor
library — a few hundred lines of canvas work in the frame and the same
pipeline again in Go. It is the second plugin built around the pdf design
where **the cage is the product, not the tax**: a frame holding a
confidential screenshot cannot exfiltrate it, and a client that lies about
what it did cannot change what gets stored.

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/imageedit`
- **Route prefix:** `/__gofastr/plugin/imageedit`
- **Isolation:** `sandbox-iframe-opaque` (`allow-scripts`, no `allow-same-origin`)
- **Canonical doc (schema `imageedit-v1`):** an **operation list**, never pixels
- **Capabilities:** `document:read`, `document:write`, `theme:read`;
  optional `upload:images`
- **Frame bundle:** dependency-free, ~20 KB raw / ~7 KB gzip

## The security property this plugin is built around

The framed CSP gives the frame **no network of any kind** — `connect-src
'none'`, no workers, no `blob:` fetches, and an opaque origin with no access
to cookies, storage, the CSRF token or the host DOM. The host fetches the
source image (`GET /img/{id}`, session + CSRF attached) and pushes the bytes
over the postMessage bridge as an `ArrayBuffer`; the frame decodes them with
`createImageBitmap`, which involves no CSP-covered fetch. Every byte
crossing the boundary does so through code the host controls.

On the way out, only the **operation list** crosses. The server resolves the
source itself, re-renders the doc, and hands the produced bytes to the host's
export handler. The stored result is a function of the doc rendered by Go —
not of anything the frame claims to have done.

## Mounting

```go
app.RegisterPlugin(imageedit.New(
    imageedit.WithSource(func(ctx context.Context, id string) ([]byte, error) {
        return media.Bytes(ctx, id) // (nil, nil) means 404
    }),
    imageedit.WithUploadHandler(func(ctx context.Context, req imageedit.UploadRequest) (string, error) {
        return media.Store(ctx, req.Bytes) // returns the new id
    }),
    imageedit.WithExportHandler(func(ctx context.Context, req imageedit.ExportRequest) (string, error) {
        return media.Store(ctx, req.Bytes) // returns the URL
    }),
    imageedit.WithDemoPage(), // themed demo at /imageedit
))
```

All handlers have demo-grade defaults ([SampleImage] behind the source, an
in-memory save store, a `data:` URL export), so `imageedit.New()` alone
works. Options also cover the ceilings: `WithMaxBytes` (16 MiB),
`WithMaxPixels` (24 MP), `WithMaxDim` (8192) and `WithJPEGQuality` (90) —
out-of-range values panic at construction rather than degrade quietly.

**Authorization is the host's job on every write route.**
`pluginhost.Allow` is a capability gate, not authentication — it passes for
anonymous callers. `POST /save`, `POST /export` and `POST /upload` all reach
the host's handler with the caller's context; a production host checks the
session there before persisting anything. The demo's `WithDevGrantAll`
bypasses the gate and must not survive into a production mount.

## The document model (`imageedit-v1`)

The canonical doc is a small JSON **operation list** that round-trips through
the hidden form field like every other plugin here:

```jsonc
{
  "schemaVersion": "imageedit-v1",
  "src":   { "kind": "id", "ref": "photo-9912", "sha256": "…" },
  "crop":  { "x": 0, "y": 0, "w": 1920, "h": 800 },   // omitted = uncropped
  "rotate": 90,                                        // 0 | 90 | 180 | 270, clockwise
  "annotations": [
    { "id": "a1", "type": "rect",  "color": "#D0342C", "width": 6, "x": 120, "y": 160, "w": 200, "h": 120 },
    { "id": "a2", "type": "arrow", "color": "#1C7ED6", "width": 5, "x": 700, "y": 500, "x2": 820, "y2": 420 },
    { "id": "a3", "type": "text",  "color": "#2F9E44", "size": 6,  "x": 200, "y": 560, "text": "APPROVED 042" }
  ],
  "redactions": [ { "id": "r1", "rect": { "x": 100, "y": 460, "w": 450, "h": 35 }, "fill": "#000000" } ],
  "rev": 3
}
```

**All geometry is in SOURCE-image pixels** — the decoded bitmap's own
coordinates, origin top-left, Y down (the convention both canvas `ImageData`
and Go's `image` package share). Annotations and redactions are pinned to
image content: crop and rotate define the transform, and the geometry is
mapped forward through it at render time, so a later rotate does not detach
an arrow from what it points at.

**The operation order is fixed: crop → rotate → annotate → redact.** Crop
then rotate is not the same picture as rotate then crop, and a preview that
disagrees with the server is the main way this plugin can be wrong. The
order is pinned in the doc format, enforced by both renderers, and asserted
by the agreement tests.

`src.sha256` (hex, lowercase) binds a doc to the exact bytes it was authored
against; on mismatch the export refuses with `E_SRC_MISMATCH` rather than
applying coordinates to a different picture. The frame computes it with
`crypto.subtle` when the context is secure; over plain `http://` to a
non-localhost host `crypto.subtle` is unavailable, the digest is omitted and
Go treats the empty string as "no binding".

### Colors, ids and bounds

Colors are `#RRGGBB` literals — content drawn into the bitmap, chosen for
contrast against arbitrary photos, not theme tokens (a dark-theme-only
annotation would vanish on a bright sky). The frame's palette is
`#D0342C #F5A623 #2F9E44 #1C7ED6 #111111 #FFFFFF`; the redaction fill
defaults to `#000000`. The structural bounds both `/save` and `/export`
enforce: ≤64 annotations, ≤64 redactions, ≤64-char text, stroke 1..64,
text scale 1..32, ids ≤64 chars.

## Two renderers, one pipeline

The frame previews and the server exports **the same integer pipeline**:

1. decode to straight-alpha 8-bit RGBA at origin;
2. crop (nil = whole image), rect intersection-clamped;
3. rotate 0/90/180/270 clockwise — an exact forward pixel bijection;
4. annotations: stroked rects (four `fillRect`s in a fixed order),
   Bresenham arrows with integer head math, and a 5×7 **bitmap font** for
   text;
5. redactions: filled rects, **last**, so nothing can draw over one.

There is no anti-aliasing, resampling or float rounding anywhere, so a PNG
input renders to **identical pixels** in the frame's canvas and in Go's
export. The font exists for the same reason: a text annotation must render
pixel-identically on both sides, and the standard library has no font
rasterizer (pulling `golang.org/x/image/font` would be the one new
dependency this plugin is forbidden to grow). The glyph table
(`render.go`'s `bitmapFont`, `js/src/font.ts`'s `ROWS`) is interchange:
never change one side without the other — the agreement tests are the
tripwire.

JPEG inputs decode with ±1 channel wobble between libjpeg variants
(browser vs Go), so JPEG agreement carries a per-channel tolerance of 2 in
the demo readout and the e2e; PNG agreement is exact.

## Export: the server owns the result

`POST /export` carries only `{docId, doc}`. The handler:

1. resolves `doc.src.ref` through [WithSource] (the frame cannot pick a
   URL, only an id the host knows);
2. checks the sha256 binding when present;
3. **caps at the header stage** — `image.DecodeConfig` reads the dimensions
   before `image.Decode` can allocate anything, so a 20000×20000 header is
   refused with `413 E_TOO_LARGE` and its pixels are never materialized;
4. composes the pipeline, re-encodes (PNG stays PNG; JPEG re-encodes at
   `WithJPEGQuality`), which **strips EXIF by construction** — no metadata
   survives a decode/re-encode round trip — and scans the output bytes for
   EXIF carriers anyway, so the claim is checked, not assumed;
5. verifies the redactions (below) and hands `[ExportRequest]` (bytes,
   format, dims, sha256, report) to the host's export handler.

The response carries `{url, format, width, height, bytes, sha256, report,
verify}` — everything the demo page and the e2e need to prove what happened.

## Redaction

**Redaction here removes content. It does not paint over it.**

1. Author rectangles over the regions to remove (source coordinates).
2. The compose pipeline fills them **last**, with the doc's fill color.
3. Before any bytes are released, Go walks every redaction rect in the
   **output** image and requires **every pixel to equal the fill exactly**.
   A wrong coordinate mapping, an off-by-one, or a "cover it with a shape"
   regression leaves original pixels behind and fails the export
   (`E_REDACT_VERIFY`, 500, no URL).
4. The verifier also flags a **vacuous** redaction — a rect whose region
   contained nothing but the fill color in the pre-redaction composite — as
   a warning in the report (the raster twin of pdf's invisible-text note).

The counter-example is kept as a regression test
(`TestRedactionVerifierRejectsLeak`): an image composed **without** the
redactions applied — exactly what cosmetic redaction produces — must be
rejected by the verifier. A verifier grading only its own successful
pipeline proves nothing.

## The upload path (optional `upload:images`)

The sandbox grants the frame no network, but a file picker needs none: the
frame reads a local image with `<input type="file">`, sends the bytes as an
`ArrayBuffer` over `requestUpload → POST /upload`, and the host stores them
via [WithUploadHandler] and returns the id. The frame then points
`doc.src.ref` at it and requests the bytes back like any other image.
`upload:images` is granted only when the handler is wired
(`[WithUploadHandler]` appends it, wildcards included — a grant that implies
a nil handler is a construction panic, the datagrid rule).

## Routes

| method + path | gate | purpose |
|---|---|---|
| GET `/img/{id}` | `document:read` | resolve + serve the source bytes (with `X-Image-Format/Width/Height`), header-capped |
| POST `/upload` | `upload:images` | store a frame-read image, return its id |
| POST `/save` | `document:write` | validate + persist the operation list |
| POST `/export` | `document:write` | server re-render + verify + store, return URL + report |

A denied capability answers **403** with `E_CAPABILITY_DENIED` on every
route, via the platform's `pluginhost.WriteCapabilityDenied`.

## Host CSP

None. This plugin needs **no host CSP changes at all** — it is fully
same-origin and fetches nothing from the frame. The demo's default export
handler answers `data:` URLs, so a demo page embedding the result needs the
usual `img-src 'self' data:` (the plugin's own demo page already carries it).

## Performance

The frame composes the full pipeline on every settled edit (a 960×640
compose is ~614 k pixel writes, single-digit milliseconds) and publishes a
1:1 PNG data URL of its render (`previewRender`) so the host page — which
cannot reach into the opaque frame — can display it beside the server's
export. Go's compose is one forward pass plus primitive fills; a 24 MP
worst case is bounded by the caps, not by trust.

## Bundle size

The frame bundle is **~20 KB raw / ~7 KB gzip** — no vendor editor, no
font file, no wasm. It is served at its own route and, like every framed
bundle here, is monolithic by construction: a dynamic `import()` is a
CORS-mode module fetch an opaque origin can never satisfy.
