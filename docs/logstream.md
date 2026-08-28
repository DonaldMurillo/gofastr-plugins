# Log stream plugin (`logstream`)

A live log tail — [xterm.js](https://xtermjs.org/) in the same opaque-origin
sandboxed iframe as every other heavy-JS plugin — fed by a line source the
HOST pushes across the postMessage bridge without ever being asked. The
eighth sandboxed plugin, and the first whose traffic is not turn-based.

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/logstream`
- **Route prefix:** `/__gofastr/plugin/logstream`
- **Isolation:** `sandbox-iframe-opaque` (`allow-scripts`, no `allow-same-origin`)
- **Canonical doc:** none. A log tail has no document — the mount is just
  the frame, and there is no hidden form field.
- **Capabilities:** `stream:read`, `theme:read`. Nothing else: no writes, no
  uploads, no PTY, no shell, no command input. A terminal that can SEND
  input is a different plugin with a different security review.

## The problem this plugin is built around

Every other plugin here is turn-based: load a document, save a document.
Even the datagrid, which moves 100,000 rows, moves them one request at a
time in answer to a question the frame asked. A log tail is the opposite
shape:

- **open-ended** — there is no "done";
- **host-initiated** — the frame cannot ask for lines it does not know exist;
- **faster than it can be rendered** — the producer can outrun the consumer.

So this plugin exists to prove two things: the bridge carries a live push,
and overflow is handled EXPLICITLY — a gap the user cannot see is worse than
a gap labelled "1,432 lines dropped".

## The bridge protocol (no protocol change)

The protocol's `request` type is host→plugin only and the wrong tool for a
stream; no channel was added to core. Instead, two ordinary protocol-v1
EVENTS:

| direction | event | payload |
|---|---|---|
| host → frame | `streamBatch` (unsolicited) | `{first, last, lines:[{seq,text}], dropped}` |
| frame → host | `streamAck` | `{lastSeq, rendered, scrollback, cap}` |

The host side of the wire is `GET /stream?after=N` — chunked
**NDJSON**, one record per line (`{"seq":N,"text":"…"}`), flushed per line,
`Cache-Control: no-store`, `X-Accel-Buffering: no`. The host ADAPTER
(not the frame — the frame's CSP sets `connect-src 'none'`) opens that
stream, drains it greedily, and pushes batches. `?after=N` is the reconnect
contract: N is the last sequence number the frame acknowledged, and the
frame dedups by sequence on arrival, so a lost-ack race can never duplicate
a line.

ANSI escapes ride in `text` intact — the frame interprets colour, the host
never does. The handler enforces a line contract host-side: embedded
newlines/CRs collapse to spaces (a "line" that renders as three corrupts the
scrollback accounting), and lines truncate at 8 KiB.

## Backpressure: the point of the plugin

All of it lives in the host adapter, in four named constants (mirrored in
`e2e/tests/logstream-journeys.spec.ts`):

| constant | value | meaning |
|---|---|---|
| `MAX_IN_FLIGHT` | 4 | unacknowledged batches allowed in flight |
| `BATCH_MAX` | 24 | lines per `streamBatch` |
| `FLUSH_MS` | 100 | short batches still go out promptly |
| `BUFFER_MAX` | 2000 | bounded line buffer; overflow drops OLDEST |

The mechanism, end to end:

1. The frame renders **at most one batch per ~16 ms scheduler tick**
   (~60 batches/s) — the declared consumption rate, so a burst cannot
   monopolise the frame's main thread — and acks from xterm's write callback
   with the last sequence number it wrote.
2. One ack releases every in-flight batch at or below `lastSeq`.
3. When the window is full, incoming lines wait in the bounded buffer. When
   the BUFFER is full, the **oldest** lines are dropped and counted.
4. The count rides with the next batch, and the frame renders
   `⋯ N lines dropped — producer outran the render loop ⋯` — never a silent
   gap.
5. **Pause** is host-side: sending stops, draining continues into the
   bounded buffer, so a long pause overflows and shows the same marker on
   resume. The frame stays a pure sink whose every ack is truthful.

The demo generator makes the numbers concrete: **Calm** is 5 lines/s;
**Flood** is 6,000 lines/s — roughly 250 batches/s against a ~60-batch/s
consumer, so at Flood the overflow path is always live and the demo page's
dropped counter visibly climbs within a second. The rate switch belongs to
the example app (`POST /demo/logstream/rate`), not the plugin: the plugin is
read-only, and the producer's controls belong to the app that owns the
producer (`WithDemoControlURL` points the demo page at that route).

## The frame forgets

Scrollback is capped at **10,000 lines** (`SCROLLBACK_LINES` in
`logstream/js/src/term.ts`). A frame that never forgets would eventually
hold everything the host ever sent, and the streaming claim would die with
it — the datagrid learned this the hard way with its block cache. The cap
is not a promise on a page: every `streamAck` carries the live
`scrollback` depth next to the `cap`, so the demo page's counter and the
e2e suite both read the frame's own accounting.

## Go API

```go
app.RegisterPlugin(logstream.New(
    logstream.WithSource(mySource),          // required; New panics without it
    // logstream.WithDevGrantAll(),          // demo/tests only
    // logstream.WithDemoPage(),             // serves /logstream
    // logstream.WithDemoControlURL("/my/rate"),
))
```

`WithSource` installs the producer behind `GET /stream`:

```go
type SourceFunc func(ctx context.Context, after uint64, yield func(Line) error) error
```

Contract:

- Runs once per connected consumer; `ctx` cancels on disconnect and `yield`
  returns an error when the write fails — return promptly in both cases.
- Only lines with `Seq > after` (reconnect replay; the handler enforces it
  too).
- `yield` blocks until the line is on the wire, so a stalled consumer
  backpressures the source instead of being silently overrun. Dropping is
  the ADAPTER's job — visible, counted. The Go side stays lossless or loud.
- **AUTHENTICATION is the source's own job.** `pluginhost.Allow` is a
  capability gate, NOT authentication: it passes for anonymous callers, so
  a host exposing a sensitive log must check the session inside its
  `SourceFunc` before yielding anything.

Known limitation: the per-line write deadline (a dead client with an open
socket would otherwise pin the source) is best-effort — gofastr's router
wraps the `ResponseWriter` past what `http.NewResponseController` can reach,
so under this stack the deadline degrades to a no-op and disconnect
detection relies on the request context (which fires on clean closes). The
host adapter always drains greedily and aborts its fetch on teardown, so the
demo is unaffected.

## Security posture

- The frame is the platform's standard opaque origin: `connect-src 'none'`,
  no cookies, no storage, no parent DOM (self-probed at boot, mirrored as
  `iframe.__logstreamProbes`).
- `disableStdin: true` — the terminal cannot even accept keystrokes.
- The one route is read-only and capability-gated; there is no write
  surface to fail closed on (there is no write surface at all).

## Demo page (`/logstream`)

Built to `docs/demo-page-design.md`: brand bar, hero, fact chips, the mount
in window chrome (`api-gateway.log` / `sandboxed iframe`), an affordance
strip with Pause + Calam/Flood + live status, and the **live bridge
telemetry** strip — lines/s, lines delivered, lines dropped, scrollback
against its bound, unacked batches in flight — polled from the adapter's
mirrors on the iframe element (`__logstreamDelivered`,
`__logstreamDropped`, `__logstreamInFlight`, `__logstreamStats`). Switch to
Flood and watch "dropped" climb: that is the whole argument of the plugin
in one glance.

## Tests

- **Go** (`logstream/plugin_test.go`): content types + framed-CSP
  relaxation, demo page shape (with and without a control route), NDJSON
  emission with seq order + ANSI intact + streaming headers, `?after`
  replay, the line sanitiser, both sides of the capability gate with real
  requests, disconnect release, the missing-source panic, manifest
  invariants.
- **e2e** (`e2e/tests/logstream-journeys.spec.ts`, webkit + chromium):
  in-order ANSI-coloured rendering; the backpressure path actually running
  (dropped > 0, marker visible, scrollback ≤ bound); pause/resume catch-up
  without a reload; search reaching a line that scrolled out of the
  viewport but is still in scrollback; no console errors on a sandboxed
  mount without `allow-same-origin`.
