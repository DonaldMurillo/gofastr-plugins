# Chart plugin (`chart`)

One chart spec, **two renderers that must agree**. The server renders a
static SVG in pure Go; the sandboxed Observable Plot frame hydrates it into
an interactive chart. With JavaScript off, the static SVG is the page — and
it is correct on its own. If the two renderers drift, the plugin is
worthless, so the agreement between them is the tested contract, not a
nice-to-have.

- **Identity:** `Name = "chart"`, `Version = "0.1.0"`,
  `RoutePrefix = "/__gofastr/plugin/chart"`, `SchemaVersion = "chart-v1"`.
- **Module path:** `github.com/DonaldMurillo/gofastr-plugins/chart`.

## What it is

This plugin generalizes what `richtext/ssr` does for documents: the same
canonical blob feeds a pure-Go server renderer (`chart/ssr`) and a
heavy-JS interactive renderer (an [Observable Plot] frame on the standard
[`pluginhost`](plugin-platform.md) platform — `sandbox="allow-scripts"`
without `allow-same-origin`, same as mermaid and monaco). `chart.Mount()`
renders the SSR SVG in a wrapper element followed by the normal mount
marker; the host adapter (`chart/host/adapter.js`) hides the wrapper when
the frame reports `ready` and un-hides it if the frame reports `bootError`,
so a frame failure degrades to the static chart instead of to nothing.
That handoff is why the plugin needed zero core changes —
`pluginhost.MountConfig` has no initial-children slot, and none was added.

[Observable Plot]: https://observablehq.com/plot/

## The canonical doc (chart-v1)

```jsonc
{
  "schemaVersion": "chart-v1",
  "type": "line",                    // line | bar | area | scatter — the whole set
  "title": "Weekly signups",         // optional
  "series": [
    { "name": "Product",  "points": [ { "x": 0, "y": 120 }, … ] },
    { "name": "Referral", "points": [ … ] }
  ],
  "axes": {
    "x": { "label": "week",    "tickCount": 10 },
    "y": { "label": "signups", "tickCount": 10 }
  },
  "options": { "legend": true, "width": 720, "height": 420 }
}
```

- **Both axes are quantitative.** chart-v1 has no band/categorical axis;
  a bar chart is columns at numeric x positions (series dodged side by
  side within each x group, x domain padded by half a bar group so edge
  bars fit). This is what keeps the tick-agreement story to exactly one
  algorithm.
- **The data lives in the doc** (unlike the datagrid plugin) because a
  chart's data is small by definition — with hard caps: **10,000 points
  total across at most 12 series** (`ssr.MaxPoints` / `ssr.MaxSeries`).
  Saving a spec past the caps is rejected (`E_BAD_SPEC`).
- Points must have finite numeric `x` and `y`; titles, labels and series
  names cap at 200 characters; `tickCount` clamps to 2–20 (default 10);
  width/height clamp to 200–1200 × 120–900 (defaults 720 × 420).

## Tick agreement (the crux)

Observable Plot samples axis ticks through d3-scale's `linear.ticks`,
which calls d3-array's `ticks(start, stop, count)` — a step of 1, 2, or 5
times a power of ten. `chart/ssr/ticks.go` is a **line-for-line port of
that algorithm** (cited to the d3-array 3.2.4 source, including the
reverse-domain branch and the count<2 doubling recursion), not a
hand-rolled nice-number pass — the two disagree on perfectly ordinary
data. Fidelity is enforced by `chart/ssr/ticks_test.go`, which replays a
committed ground-truth sweep (`chart/ssr/testdata/d3ticks.json`, ~3000
domains recorded from real d3 in Node) and fails on any float or label
mismatch.

Label strings agree by construction as well: both renderers format ticks
as `v.toFixed(p)` / `strconv.FormatFloat(v, 'f', p, 64)`, where `p` is the
smallest precision that round-trips the step (an engine-independent
derivation — `Math.log10` can differ between JS engines and Go at exact
power-of-ten boundaries). Both renderers pass the same explicit `domain`,
`ticks` count, and `tickFormat` to their scales, so no library default can
wedge anything open.

The e2e agreement journey (`e2e/tests/chart-journeys.spec.ts`) reads the
SSR `<svg>` from the host page before hydration and the frame's `<svg>`
after `ready`, and asserts equality of **tick labels, series names, and
data extents** across four awkward ranges (0–7, 0–1, −3.5–3.5, 0–1,000,000)
— exactly where a hand-rolled tick algorithm diverges from d3's.

## Capabilities used

`DefaultCapabilities`: `document:read`, `document:write`, `theme:read`. No
uploads, no network. The `POST /save` route gates on `document:write` via
`pluginhost.Allow` → `auth.HasScope`.

**`pluginhost.Allow` is a capability gate, NOT authentication.** It passes
for anonymous callers (a session in context is unscoped by design); it only
narrows what a scoped API token may do. Any host exposing `POST /save` in
production MUST check the session in its own middleware/handler first.
The demo skips that because it runs `WithDevGrantAll()`, which bypasses the
gate entirely — demo only.

## Theming

Both renderers read the same design tokens. The SSR SVG is styled
token-first (`var(--color-text, fallback)` classes, series colors as
`color-mix` derivations of `--color-primary`), so it tracks the host
palette — including a `data-color-scheme` flip — with no re-render. The
frame receives the same resolved tokens over `theme:read` and computes the
same mix ratios in JS for Plot's concrete color inputs
(`chart/js/src/palette.ts` mirrors the CSS rules in `chart/ssr/render.go`).
Light and dark both work.

## How to mount it

```go
import "github.com/DonaldMurillo/gofastr-plugins/chart"

app.RegisterPlugin(chart.New(
    chart.WithDemoPage(),   // serve the self-contained demo at "/chart"
    // chart.WithDevGrantAll(),            // demo only — bypasses auth.HasScope
    // chart.WithCapabilities(...),        // override the grant set
    // chart.WithSaveHandler(fn),          // default: in-memory map keyed by DocID
))
```

`New` builds and `Validate()`s a `pluginhost.Manifest`. `Init` registers
the broker route, the framed chart assets, the adapter, and `POST /save`.
Render a mount (SSR SVG + marker + hidden field) into a form:

```go
chart.Mount(chart.MountConfig{
    DocID:     "demo",        // persistence key
    SpecField: "chart_spec",  // hidden input for the spec JSON
    MinHeight: "360px",
    Spec:      specJSON,      // canonical chart-v1 JSON
})
```

Apps rendering through a `UIHost` inject the host scripts with
`chart.UIHostOption()` — platform broker first, then this plugin's adapter:

```go
uihost.New(..., chart.UIHostOption())
```

The demo page additionally exposes a spec-JSON editor (Apply → `POST /save`
→ reload), which is the write path the e2e journeys drive.

## Packages

| path | what |
|---|---|
| `chart/` | the plugin: identity consts, `Manifest`, `Init`, `Mount` (SSR + marker), save handler, demo |
| `chart/ssr/` | the pure-Go SVG renderer + the d3 tick port + golden tests |
| `chart/host/adapter.js` | broker adapter: SSR hide/show handoff, save wiring |
| `chart/js/` | the frame bundle source (Observable Plot); build with `npm run build` → committed `chart/assets/` |
