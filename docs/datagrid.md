# Data grid plugin (`datagrid`)

A data grid over [AG Grid Community](https://www.ag-grid.com/javascript-data-grid/)’s
**infinite row model** — the fifth sandboxed heavy-JS plugin, and the first
whose traffic profile is not one small document. The demo serves **100,000
deterministic rows**; the frame renders them one page at a time.

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/datagrid`
- **Route prefix:** `/__gofastr/plugin/datagrid`
- **Isolation:** `sandbox-iframe-opaque` (`allow-scripts`, no `allow-same-origin`)
- **Canonical doc (schema `datagrid-v1`):** view state only —
  `{columns[], sort, filter, pageSize}`. **Rows are never part of the doc.**
- **Capabilities:** `data:read`, `theme:read`;
  optional `data:write` (cell edits + view-state saves) and `data:export` (CSV)

## The problem this plugin is built around

Every other plugin in this repo moves ONE small document across the bridge. A
data grid moves rows by the thousand, and two structural facts make that
impossible to fake:

- `postMessage` cannot carry 100k rows as one JSON payload without stalling
  the frame.
- The framed CSP sets `connect-src 'none'` (see `framedCSP` in gofastr’s
  `framework/pluginhost/assets.go`), so **the frame can never fetch its own
  rows** — not from your API, not from anywhere.

So every page arrives from the host over the broker, and **server-side sort,
filter and paging are the point**. A grid that loads all rows up front would
prove nothing about the platform.

## The bridge protocol (no protocol change)

Frame-to-host requests are fire-and-forget events answered by a correlated
event — exactly richtext’s `requestUpload` → `uploadResult` pair, since the
protocol’s `request` type is host→plugin only:

| frame emits | host adapter POSTs | frame receives |
|---|---|---|
| `requestRows` `{reqId, startRow, endRow, sort, filter, columns}` | `/rows` | `rowsResult` `{reqId, rows, lastRow}` (`lastRow` = −1 when unknown) |
| `requestCellWrite` `{reqId, rowId, field, value}` | `/cell` | `cellWriteResult` `{reqId, ok}` \| `{reqId, error}` |
| `requestExport` `{reqId, format:"csv", sort, filter, columns}` | `/export` | `exportResult` `{reqId, url, rowCount}` |

The host adapter mirrors bridge traffic onto the iframe element so tests (and
you, in devtools) can watch the volume claim live:
`iframe.__datagridRowsDelivered` is the running count of rows that actually
crossed into the frame this session, `iframe.__datagridMaxRowsDelivered`
the largest single response, and `iframe.__datagridCacheBlocks` (published by
the frame after every page settles as a `cacheState` event — a host page
cannot reach into the opaque frame to read AG Grid's own cache state) the
resident block count against its cap. The demo page's live "bridge
telemetry" strip reads exactly these mirrors. A jump-scroll to row 50,000
in the demo delivers a few hundred rows, never the table.

AG Grid’s infinite row model maps directly onto this: the datasource’s
`getRows` turns into a `requestRows` event; sort clicks purge the cache and
re-fetch through the server; the toolbar’s filter box (a server-side
substring filter, deliberately NOT an AG Grid column filter) swaps in a fresh
datasource. The frame never sorts or filters rows it does not hold — it
cannot, because it never holds them.

## Document model (`datagrid-v1`)

The canonical doc is **view state only**:

```jsonc
{
  "schemaVersion": "datagrid-v1",
  "columns": [
    { "field": "amount", "header": "Amount", "width": 110, "type": "number", "sortable": true }
  ],
  "sort":   [{ "field": "amount", "dir": "desc" }],
  "filter": "",
  "pageSize": 100
}
```

Columns declare the schema (including which are numeric, so the host’s rows
source can sort them numerically); `sort`/`filter`/`pageSize` are the live
view. The doc round-trips through the hidden form field like every other
plugin’s, and autosaves via `POST /save` when `data:write` is granted.
**Rows are never saved into it and never echoed back out of it.**

## Capabilities, and the authentication warning

| capability | always on? | gates |
|---|---|---|
| `data:read` | yes | `POST /rows` (page reads) |
| `theme:read` | yes | token bridging |
| `data:write` | optional — granted by `WithCellWriteHandler` | `POST /cell`, `POST /save` |
| `data:export` | optional — granted by `WithExportHandler` | `POST /export` |

The optional grants follow the pdf `pdf:export` pattern: the capability is
appended exactly when the host wires the matching handler option, and
`New` **panics** if a capability is granted without its handler (or, for
`data:write`, if cell editing is impossible because no handler exists).

> **`pluginhost.Allow` is a capability gate, NOT authentication.** It passes
> for anonymous callers (and for unscoped sessions it is bounded only by the
> plugin’s grant set). Any route that writes — `POST /cell` above all — must
> be treated as unauthenticated until the HOST’s own handler checks the
> session. `WithCellWriteHandler` is where that check belongs; the demo’s
> `WithDevGrantAll()` skips the gate entirely and MUST NOT survive into a
> production mount.

## CSV export runs on the host

A sandboxed frame cannot start a download (no `allow-downloads`, no
`allow-popups`) — the same reason the pdf plugin makes export a host
capability. The flow: the frame emits `requestExport`; the plugin **pages
through the rows source in Go** under the request’s sort/filter, spilling
CSV to a temp file **chunk by chunk (5,000 rows per chunk, so peak memory
is one chunk whatever the table size)**; your `WithExportHandler` receives
the finished CSV as a stream (`ExportRequest.CSV`, an `io.Reader` it must
fully consume before returning — the temp file is deleted when the handler
returns) and returns a URL; the adapter replies `exportResult` and clicks a
transient `<a download>` **in the host page** — that click is the only way
the produced file reaches the user’s disk.

Only the URL crosses the postMessage bridge. Two failure rules worth
knowing: a rows-source error **mid-scan** is an error response (the handler
is never called — a failed export never looks like a successful short
file), and every exported field — headers included, since headers ride in
the doc — is **formula-sanitised**: a value starting with `=`, `+`, `-`,
`@`, tab or CR is prefixed with a single quote so spreadsheet clients
render it as text instead of evaluating it.

## Mounting

```go
import "github.com/DonaldMurillo/gofastr-plugins/datagrid"

app.RegisterPlugin(datagrid.New(
    // REQUIRED — a grid has no meaningful default dataset (unlike pdf's
    // embedded sample), so construction panics without a source.
    datagrid.WithRowsSource(func(ctx context.Context, q datagrid.RowsQuery) (datagrid.RowsPage, error) {
        return queries.PageRows(ctx, q.StartRow, q.EndRow, q.Sort, q.Filter)
    }),
    // Optional capabilities — each both grants the capability and wires the
    // handler behind it:
    datagrid.WithCellWriteHandler(func(ctx context.Context, req datagrid.CellWriteRequest) error {
        return queries.SaveCell(ctx, req.RowID, req.Field, req.Value) // ← check the session HERE
    }),
    datagrid.WithExportHandler(func(ctx context.Context, req datagrid.ExportRequest) (string, error) {
        return files.Store(ctx, req.CSV) // streams the CSV; returns a URL
    }),
    datagrid.WithDemoPage(), // themed demo at /datagrid
))
```

Drop the mount marker into a form:

```go
datagrid.Mount(datagrid.MountConfig{
    DocID: "orders",   // persistence key for the view-state doc
    Doc:   initialDoc, // optional view-state JSON (columns, sort, filter, pageSize)
    Field: "datagrid_doc", // hidden input the adapter mirrors the doc into
    MinHeight: "560px",
})
```

Apps rendering through a `UIHost` inject the host scripts with
`datagrid.UIHostOption()` — platform broker, then this instance’s
`config.js` (which publishes whether `data:write`/`data:export` were wired,
so the adapter merges them into the capabilities it registers), then the
adapter.

## Guards worth knowing

- **Page-size ceiling.** `POST /rows` rejects any request window over 500
  rows (`400 E_PAGE_TOO_LARGE`). This is the integrity behind the volume
  claim: a single `requestRows` can never ask for the whole table, so the
  frame can only ever hold pages.
- **Bounded block cache.** AG Grid’s default `maxBlocksInCache` is
  unlimited, which would let the frame hoard every block ever fetched — a
  scroll through 100,000 rows would end with 100,000 rows resident, bridge
  paging or not. The frame therefore caps its cache at
  `⌈2,500 / pageSize⌉` blocks (derived from the page size, so the ceiling
  is constant): with the default 100-row page that is **25 blocks = 2,500
  resident rows** (plus the overflow block AG Grid prefetches around the
  viewport), with older blocks evicted as new ones load. The e2e suite
  proves this from AG Grid’s own `getCacheBlockState()`, not from bridge
  traffic.
- **Envelope strictness.** Request bodies are capped at 64 KiB and must be
  exactly ONE JSON value — anything after the first value (a second object,
  stray bytes, padding whitespace) is a `400 E_BAD_JSON`.
- **Save-path normalisation.** `POST /save` persists the doc it validated:
  columns normalised, `pageSize` clamped to the 500-row bridge limit, sort
  models capped at 4 keys and filters at 256 chars (the same bounds
  `/rows` enforces). A save with `pageSize: 100000` cannot come back on
  the next load as a frame asking for a block the bridge must refuse.
- **Column projection.** `/rows` ships only the requested columns’ cells —
  a one-column request sends one column across the bridge, not every field
  the source returns.
- **Wildcard grants validated at construction.** Grant matching uses the
  framework’s scope grammar (`data:*`, `*:*`, `*:write` all imply
  `data:write`), so `New` requires the matching handler for anything the
  grant set implies — and every route fails closed (a clear error, never a
  panic) if its handler is somehow unwired.
- **Deterministic demo dataset.** 100,000 rows generated in Go in
  `example/datagrid.go` from fixed formulas (no database, no network), so
  the e2e suite recomputes the same values in TypeScript and asserts exact
  cells at row 50,000.

## Theming approach

**AG Grid’s Theming API** (`themeQuartz.withParams`), not a bundled
stylesheet. AG Grid v33+ injects its theme CSS at runtime; the platform’s
framed CSP carries `style-src <origin> 'unsafe-inline'`, so the injection is
permitted — and this is **verified in WebKit** by the e2e suite
(`e2e/tests/datagrid-journeys.spec.ts` runs in both the `webkit` and
`chromium` Playwright projects; the boot + fully-styled render were also
checked visually in WebKit during development). The theme parameters are
mapped from the bridged host tokens (`--color-surface` → `backgroundColor`,
`--color-primary` → `accentColor`, `--font-body` → `fontFamily`, …), so a
palette flip on the host re-resolves inside the frame with zero bespoke hex.
The param set is deliberately small — every added param is another
quartz-version coupling to re-verify in WebKit.

The frame also renders a small **status vocabulary** as tinted pills: a cell
whose value is exactly `active`, `pending`, `blocked` or `expired` draws a
`--color-success` / `-warning` / `-danger` / neutral chip (12% tint, token
colors only, both schemes); every other value takes AG Grid's default text
rendering untouched, so host data is never re-styled by guesswork. The demo
page's dark theme re-states the three tone tokens lighter than the
framework's light values, which are tuned for tinted chips on light surfaces.

## Bundle size

The frame bundle is **~1.05 MB raw / ~307 KB gzip** — AG Grid Community
(all community modules) plus the controller, built as a single minified
IIFE. Like the pdf viewer it is deliberately monolithic: a dynamic
`import()` is a CORS-mode module fetch an opaque origin can never satisfy,
so code splitting is a property of the isolation model, not a bundler
setting.

## Performance

The frame does no data work beyond holding pages; sorting 100,000 rows,
filtering, and CSV serialisation all happen in the host process. The
measured bridge traffic for a jump-scroll to row 50,000 plus follow-up
scrolling in the demo is a few hundred rows (see the `rows-delivered`
annotation the e2e suite attaches to the volume test). Retention is bounded
to match: whatever the user scrolls through, the frame’s block cache holds
at most ⌈2,500 / pageSize⌉ blocks (25 blocks / 2,500 rows at the default
page size), verified in the e2e suite against AG Grid’s own cache state.
