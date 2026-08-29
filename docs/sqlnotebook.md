# sqlnotebook

A real SQL engine inside the cage. SQLite compiled to WebAssembly runs in an
opaque-origin iframe that cannot open a socket, so a database can be queried in
the browser and still has no way to send anything anywhere.

This is the plugin that answered an open platform question. Until gofastr
v0.74.0 the framed Content-Security-Policy was a fixed string with no wasm
allowance, and every plugin that wanted one worked around it: `pdf` runs pdf.js
worker-free for exactly this reason. The workaround stood in for an answer for
months.

## What it proves

| | measured |
|---|---|
| SQLite version in the frame | 3.49.1 |
| init, chromium | 28 ms |
| init, webkit | 26 ms |
| the same frame without the tier | `CompileError`, both engines |

The last row is the point. The tier is what makes it work, and a frame that
does not get it fails loudly rather than degrading.

## The three constraints that shaped it

**The tier must be handed to the AssetServer, not just declared.** A manifest
carrying `CSP: ["'wasm-unsafe-eval'"]` changes nothing on its own. The header is
assembled by `AssetServer`, which does not read the manifest, so the plugin has
to thread it through explicitly:

```go
pluginhost.NewAssetServer(framedAssets(), RoutePrefix, specs).WithCSP(p.manifest.CSP)
```

Forget that call and the manifest still validates, the plugin still mounts, and
the frame refuses WebAssembly with nothing naming the cause. That is
DonaldMurillo/gofastr#300, and it is why this plugin's test asserts the **served
response header**, not the manifest. A test that checks `Validate()` passed
would go green on a frame that cannot work.

**The engine cannot fetch itself.** `connect-src 'none'` is not decorative.
sql.js's documented `locateFile` option fails in the cage:

```
Error: both async and sync fetching of the wasm failed
```

So the host adapter fetches `sql-wasm.wasm` (the host page is same-origin and
may) and posts the bytes into the frame, which calls `initSqlJs({ wasmBinary })`.
The frame never fetches anything. A database that cannot phone home is the
property that makes the tier worth granting at all.

**Every asset needs an explicit Content-Type.** An empty one plus the platform's
`nosniff` produces a 200 response, with correct bytes, that the browser refuses
to parse, and nothing appears in the server log or the console. Filed as
DonaldMurillo/gofastr#303.

## Why SQLite and not DuckDB

The ticket proposed DuckDB. Three independent findings ruled it out, any one of
them sufficient:

- **Size.** `duckdb-eh.wasm` is 35 MB. This repo caps its total embedded source
  at 20 MB, and 16.9 MB of that was already spent. sql.js's wasm is 658 KB.
- **It needs a Worker.** DuckDB's API is worker-based, and chromium refuses to
  construct a Worker from a same-origin script inside an opaque origin
  (`SecurityError`). WebKit allows it, which is an engine divergence worth
  knowing about on its own. Blob workers do work in both, so DuckDB is not
  strictly impossible, only expensive on top of the size problem.
- **sql.js needs no worker at all.** It is synchronous, which is why it worked
  first try.

Only single-threaded builds can work here regardless: multi-threaded wasm wants
`SharedArrayBuffer` with COOP and COEP, which fight the opaque origin head-on.

## The wire

Versioned, and small enough to read in full.

```
host  -> frame   { type: "sqlnb/init",  v: 1, wasm: ArrayBuffer, seed: string }
host  -> frame   { type: "sqlnb/query", v: 1, id, sql }
frame -> host    { type: "sqlnb/ready",  v: 1, sqliteVersion, ms }
frame -> host    { type: "sqlnb/result", v: 1, id, columns, rows, truncated, ms }
frame -> host    { type: "sqlnb/error",  v: 1, id, message }
```

Results are capped at 500 rows with `truncated` set when a query produced more.
The cap is on the bridge rather than the query so an accidental
`SELECT * FROM big` costs one bounded message instead of a hung frame.

## What stays true

The frame keeps every property the platform gives it. `allow-same-origin` is
still absent, so the origin is opaque and host cookies and the DOM are
unreachable. `connect-src 'none'` is untouched by the tier: WebAssembly
streaming instantiation still fails, on the network directive rather than the
wasm one. The tier grants exactly one thing, and `eval` and `new Function` are
still refused.
