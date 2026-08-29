# scanner — the plugin whose input is a device

Barcode and QR scanning in an opaque-origin sandboxed iframe. It is the one
plugin here that wants something the cage cannot have, and the useful part is
how that turns out.

## The camera is unreachable from the frame

Not "awkward to reach". Refused. Measured on this repo's own harness, four
iframes on one page, same child document calling `getUserMedia({video:true})`,
Chromium with a fake camera and permission granted:

| frame | result |
|---|---|
| plain same-origin iframe (control) | ok, 1 video track |
| `sandbox="allow-scripts"` | `SecurityError: Invalid security origin` |
| `sandbox="allow-scripts"` + `allow="camera *"` | `SecurityError: Invalid security origin` |
| `sandbox="allow-scripts allow-same-origin"` + `allow="camera *"` | ok, 1 video track |

All four reported `isSecureContext: true`. The third row is the one that matters:
the `allow` attribute changes nothing on an opaque origin, so a manifest
permissions field would have been a no-op. The fourth works and needs exactly
the flag this platform bans.

Upstream reached the same conclusion independently and closed
DonaldMurillo/gofastr#273 on it.

## So the host captures and the cage decodes

- the **host page** owns the `MediaStream`, so the permission prompt is against
  an origin a user can read
- each frame is drawn at 480×360, converted to grayscale luminance (one byte per
  pixel), and sent over the bridge as `scanFrame`
- **one frame in flight**: the host sends the next only after the frame's
  `frameDone` ack, which arrives for every frame, decoded or not
- the frame returns `scanResult{text, format, via, decodeMs}`. Pixels never come
  back, and the frame still runs under `connect-src 'none'`

Three sources feed the same path: the camera, an image file the host reads, and
a sample the frame generates itself so the demo works with no camera and no
image assets in the repo.

## Host requirement: relax the Permissions-Policy

**A host that mounts this plugin must allow the camera on its own page.** The
framework's default is

```
Permissions-Policy: geolocation=(), microphone=(), camera=()
```

which denies the camera to the document itself, so `getUserMedia` fails on the
HOST as well as in the cage — and it surfaces as a console error rather than a
prompt, which reads like a plugin bug. The opt-in:

```go
framework.WithConfig(framework.AppConfig{
    SecurityHeaders: middleware.SecurityHeadersConfig{
        PermissionsPolicy: "geolocation=(), microphone=(), camera=(self)",
    },
})
```

### The plugin declares it, but only you can grant it

Since gofastr v0.74.0 the manifest carries the requirement:

```go
HostRequirements: []string{pluginhost.HostRequirementPrefix + "camera"}
```

Declaring it grants nothing, and cannot. A `Permissions-Policy` is your app's
response header, and a plugin must not be able to rewrite it. The line above is
still what opens the camera.

What the declaration buys is being told. Call the check once at startup with
the policy you configured:

```go
pluginhost.CheckHostRequirements(slog.Default(), yourPolicy, modules...)
```

It logs and never fails, so a plugin cannot take your app down by declaring
something. A host that forgets the opt-in gets a warning naming the plugin and
the fix, instead of meeting a `getUserMedia` console error with nothing
pointing back at the cause.

One sharp edge worth knowing: the check is deliberately narrow. It warns only
on the empty allowlist `camera=()`, the one shape that unambiguously denies the
feature everywhere. A policy that drops the directive entirely stays silent, so
**silence is not proof you granted it** — `example/main.go` asserts the literal
`camera=(self)` for that reason.

`example/main.go` does exactly this. Nothing else in the example needs the
camera, so the grant is `self` and no wider. A host that never calls
`startCamera()` can leave the default alone: the file and sample paths do not
touch the camera.

## Two decoders, and why the order is what it is

The platform's `BarcodeDetector` where the engine has one, and a bundled
**pure-JS** zxing where it does not — Safari, Firefox, and Chromium on Linux.
No wasm, so this plugin is not blocked on gofastr#255.

Native goes first for **correctness**, not speed. zxing's JS port fails to read
some valid QR codes its own encoder produces. Bisected, every payload rendered
identically at 300×300:

| payload | bytes | zxing |
|---|---|---|
| `GOFASTR_SCANNER_E2` | 18 | ok |
| `GOFASTR_SCANNER_E2E` | 19 | `NotFoundException` |
| `GOFASTRSCANNERE2E` | 17 | ok |

`BarcodeDetector` reads the failing one without complaint, so the symbol is
valid and the decoder is wrong. Two consequences live in the code:

- `scanResult.via` reports which decoder read it, because a plugin that reads a
  code on one machine and not another should say so
- the e2e **forces both paths** on every engine. CI's Linux chromium has no
  `BarcodeDetector` and a developer's mac does, so an unforced test would
  exercise one path per machine and neither run would notice the other rotting
- `scripts/gen-qr-fixture.mjs` refuses to write a fixture the decoder cannot
  read, so this trap cannot be committed as a test asset

zxing also `console.warn`s from inside `MultiFormatReader` on successful
decodes. At eight frames a second that is a flood, so the decode call captures
it into `__scanDebug.zxingWarnings()` and restores `console.warn` immediately.
Nothing is discarded; it moves.

## What it does not do

No document store, no upload route, no save path. A decoded string goes to the
host page and nowhere else. The grants are `scan:decode` and `theme:read`.
