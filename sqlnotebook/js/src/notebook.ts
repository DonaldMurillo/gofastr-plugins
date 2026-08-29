// GoFastr SQL notebook — in-frame controller (sqlnotebook protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage, or
// DOM, and never fetch (the framed CSP sets connect-src 'none').
//
// The engine story (measured, not inferred — see the repo's spike notes):
//   - SQLite runs as sql.js (wasm). The emscripten glue (sql-wasm.js) loads
//     as a plain <script src> from the frame's own origin; the .wasm CANNOT
//     be fetched by the frame, so the HOST adapter fetches it same-origin
//     and posts `sqlnb/init` {v, wasm: ArrayBuffer, seed}. We hand those
//     bytes to initSqlJs({ wasmBinary }) — NEVER `locateFile`, which would
//     make sql.js fetch and die under connect-src 'none'.
//   - Everything below runs against a single in-memory Database created at
//     init and living as long as the frame does.
//
// sqlnotebook wire protocol (rides the broker envelope as method + params;
// the `type` strings in the brief ARE the envelope methods, and each params
// carries its own protocol `v`):
//   host → frame: sqlnb/init  {v:1, wasm: ArrayBuffer, seed: string}
//                   sqlnb/query {v:1, id: number, sql: string}
//   frame → host: sqlnb/ready  {v:1, sqliteVersion: string, ms: number}
//                   sqlnb/result {v:1, id, columns, rows, truncated, ms}
//                   sqlnb/error  {v:1, id, message}
// Unknown method or mismatched v: ignored silently, never a throw (§4
// forward-compat). Query ids issued by the host are positive; ids minted
// here for UI-initiated runs are NEGATIVE, so the two ranges can never
// collide and the adapter only resolves ids it actually has outstanding.
//
// Results are capped at MAX_ROWS (500) rows with truncated:true when the
// query produced more. A query that throws posts sqlnb/error and leaves the
// last good result table visible — an error must not erase evidence.

// --- identity (must agree with the Go plugin and the host adapter) --------

const PLUGIN_VERSION = "0.1.0";
const SCHEMA_VERSION = "sqlnotebook.v1";
/** sqlnotebook wire-protocol version carried inside every sqlnb/* params. */
const SQLNB_VERSION = 1;
/** Row cap on rendered AND posted results; truncated flags the cut. */
const MAX_ROWS = 500;
const SAMPLE_TOKENS = ["--color-surface", "--color-text", "--color-border"];

// --- sql.js ambient declarations --------------------------------------------
// sql-wasm.js is a classic <script> loaded BEFORE this bundle; it defines a
// global initSqlJs. sql.js ships no .d.ts, so the surface used here is
// declared locally (the pdf worker-shim.d.ts pattern, inlined).

/** Minimal surface of sql.js's Database used by this frame. */
interface SqlDatabase {
  exec(sql: string): { columns: string[]; values: unknown[][] }[];
  run(sql: string): void;
  close(): void;
}

interface SqlJsStatic {
  Database: new (data?: ArrayLike<number> | null) => SqlDatabase;
}

interface SqlJsConfig {
  /** The wasm bytes handed over the bridge. NEVER locateFile — see header. */
  wasmBinary?: ArrayBuffer | Uint8Array;
}

declare const initSqlJs: (config?: SqlJsConfig) => Promise<SqlJsStatic>;

// --- protocol v1 client (frozen envelope — same shape every frame here) ---
// See docs/design/protocol-v1.md §3/§4. The frame's only host channel is
// window.parent.postMessage; source identity (event.source ===
// window.parent) is the gate, event.origin is never consulted because an
// opaque-origin frame's origin is the literal string "null".

const PROTOCOL_VERSION = 1;

interface ProtocolError {
  code: string;
  message: string;
}

interface ProtocolMessage {
  v: number;
  id: string;
  type: "request" | "response" | "event";
  src: "host" | "plugin";
  method?: string;
  params?: unknown;
}

type Handler = (params: unknown, msg: ProtocolMessage) => unknown;
type HandlerMap = Record<string, Handler>;

let idCounter = 0;

function post(msg: object): void {
  // targetOrigin "*": the parent origin is opaque to this frame; the source
  // check on both sides is the actual gate.
  window.parent.postMessage(msg, "*");
}

/** Fire-and-forget event to the host (fresh correlation id, never answered). */
function sendEvent(method: string, params: Record<string, unknown> = {}): void {
  post({
    v: PROTOCOL_VERSION,
    id: "p-" + ++idCounter,
    type: "event",
    src: "plugin",
    method,
    params,
  });
}

/** Respond to a host request, echoing its id; result OR {code,message}. */
function sendResponse(requestId: string, result: unknown, error: ProtocolError | null = null): void {
  post({ v: PROTOCOL_VERSION, id: requestId, type: "response", src: "plugin", result, error });
}

/** Build a message router from a {method → handler} map. Unknown method →
 *  ignore silently (§4); handler throws are contained, never propagated
 *  into the message dispatch. */
function createRouter(handlers: HandlerMap): (event: MessageEvent) => void {
  return function route(event: MessageEvent): void {
    if (event.source !== window.parent) return;
    const msg = event.data as ProtocolMessage | null;
    if (!msg || typeof msg !== "object") return;
    if (msg.v !== PROTOCOL_VERSION || msg.src !== "host") return;
    if (msg.type === "response") return; // we send no requests; nothing pending

    const handler = handlers[msg.method ?? ""];
    if (typeof handler !== "function") return; // unknown method → ignore
    if (msg.type === "event") {
      try {
        handler(msg.params ?? {}, msg);
      } catch (err) {
        console.error("[sqlnotebook] event handler error:", msg.method, err);
      }
      return;
    }
    if (msg.type === "request") {
      Promise.resolve()
        .then(() => handler(msg.params ?? {}, msg))
        .then(
          (result) => sendResponse(msg.id, result === undefined ? null : result),
          (err: unknown) => {
            const e = err as ProtocolError | null | undefined;
            sendResponse(
              msg.id,
              null,
              e && typeof e.code === "string"
                ? e
                : { code: "E_INTERNAL", message: String((e as { message?: unknown })?.message ?? e) }
            );
          }
        );
    }
  };
}

// --- theming across the iframe boundary (protocol-v1.md §7) ----------------
// CSS custom properties do not inherit across an iframe boundary, so the
// host bridges resolved token VALUES. On init/themeChanged we write a single
// <style id="sqlnb-tokens">:root{ … }</style> into our own <head>. The frame
// CSS is token-only with inline fallbacks, so it is legible before init and
// matches the host palette by construction after.

const TOKEN_STYLE_ID = "sqlnb-tokens";

function applyTokens(tokens: unknown): void {
  if (!tokens || typeof tokens !== "object") return;
  let rules = ":root{";
  let count = 0;
  for (const [name, value] of Object.entries(tokens as Record<string, unknown>)) {
    if (!name.startsWith("--") || typeof value !== "string" || value === "") continue;
    rules += `${name}:${value};`;
    count += 1;
  }
  rules += "}";
  if (count === 0) return;
  let style = document.getElementById(TOKEN_STYLE_ID);
  if (!style) {
    style = document.createElement("style");
    style.id = TOKEN_STYLE_ID;
    document.head.appendChild(style);
  }
  style.textContent = rules;
}

function applyScheme(scheme: unknown): void {
  if (typeof scheme === "string" && scheme) {
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }
}

/** Resolve token NAMES from :root after applying — the §8a round trip. */
function sampleAppliedTokens(names: string[]): Record<string, string> {
  const out: Record<string, string> = {};
  const cs = getComputedStyle(document.documentElement);
  for (const name of names) {
    const v = cs.getPropertyValue(name);
    if (v) out[name] = v.trim();
  }
  return out;
}

// --- runtime state -----------------------------------------------------------

let db: SqlDatabase | null = null;
let engineReady = false;
let messageListener: ((event: MessageEvent) => void) | null = null;
/** UI query ids are negative so they can never collide with host ids. */
let uiQuerySeq = 0;

let runButton: HTMLButtonElement | null = null;
let sqlInput: HTMLTextAreaElement | null = null;
let statusEl: HTMLElement | null = null;
let errorEl: HTMLElement | null = null;
let resultEl: HTMLElement | null = null;
let tablesEl: HTMLElement | null = null;

/** Narrow an untrusted postMessage params object to a string-keyed record. */
function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

function now(): number {
  return typeof performance !== "undefined" && performance.now ? performance.now() : Date.now();
}

// --- engine -------------------------------------------------------------------

async function handleSqlnbInit(params: unknown): Promise<void> {
  const p = asRecord(params);
  if (p.v !== SQLNB_VERSION) return; // mismatched v → ignore silently
  if (engineReady || db) return; // second init → ignore (idempotent)
  if (typeof initSqlJs !== "function") {
    sendEvent("bootError", { message: "sql-wasm.js did not load (initSqlJs missing)" });
    return;
  }
  const wasm = p.wasm;
  if (!(wasm instanceof ArrayBuffer)) {
    showError("sqlnb/init arrived without wasm bytes — cannot start the engine");
    return;
  }

  const t0 = now();
  try {
    // NEVER locateFile: it makes sql.js fetch the .wasm over HTTP, which
    // connect-src 'none' refuses. The bytes were handed to us.
    const SQL = await initSqlJs({ wasmBinary: wasm });
    // Local const keeps the narrowing: the module-level `db` is captured by
    // teardown closures, so TS will not keep it non-null across the try.
    const database = new SQL.Database();
    db = database;
    const seed = typeof p.seed === "string" ? p.seed : "";
    if (seed !== "") {
      try {
        database.run(seed);
      } catch (err) {
        // The engine is up even if the seed is bad — say so visibly and keep
        // going, so a faulty seed does not masquerade as a dead plugin.
        showError("seed SQL failed: " + String((err as { message?: unknown })?.message ?? err));
      }
    }
  } catch (err) {
    const message = String((err as { message?: unknown })?.message ?? err);
    showError("engine failed to start: " + message);
    // A CSP without 'wasm-unsafe-eval' fails exactly here (CompileError);
    // surface it on the platform's dead-frame channel, not just the DOM.
    sendEvent("bootError", { message: "initSqlJs failed: " + message });
    return;
  }

  engineReady = true;
  refreshSchema();
  let sqliteVersion = "";
  if (db) {
    try {
      const v = db.exec("select sqlite_version() as v");
      sqliteVersion = String(v[0]?.values?.[0]?.[0] ?? "");
    } catch {
      sqliteVersion = "";
    }
  }

  if (statusEl) statusEl.textContent = "engine ready";
  sendEvent("sqlnb/ready", {
    v: SQLNB_VERSION,
    sqliteVersion,
    ms: Math.round(now() - t0),
  });
}

/** Run one SQL text, post result/error with the caller's id, render. */
function runQuery(id: number, sql: string): void {
  if (!db) {
    sendEvent("sqlnb/error", { v: SQLNB_VERSION, id, message: "engine not initialized" });
    showError("engine not initialized — waiting for sqlnb/init");
    return;
  }
  const t0 = now();
  let columns: string[] = [];
  let rows: unknown[][] = [];
  try {
    const sets = db.exec(sql);
    // Show the LAST statement's result set: notebook convention (the final
    // output of a multi-statement paste is the interesting one), and a
    // no-row statement (CREATE/INSERT) naturally leaves columns empty.
    // sql.js caveat: exec() emits NO entry for a SELECT that returns zero
    // rows, so "0-row SELECT" and "no result set" are indistinguishable
    // here — both post columns: [] (an e2e wanting headers must query
    // something with rows).
    for (const set of sets) {
      if (set && Array.isArray(set.columns)) {
        columns = set.columns.map(String);
        rows = set.values;
      }
    }
  } catch (err) {
    // Thrown SQL error: report, and LEAVE the last good result standing.
    const message = String((err as { message?: unknown })?.message ?? err);
    sendEvent("sqlnb/error", { v: SQLNB_VERSION, id, message });
    showError(message);
    return;
  }
  const truncated = rows.length > MAX_ROWS;
  const shown = rows.slice(0, MAX_ROWS);
  const ms = Math.round(now() - t0);

  renderResult(columns, shown, truncated, ms);
  if (errorEl) errorEl.hidden = true;
  sendEvent("sqlnb/result", {
    v: SQLNB_VERSION,
    id,
    columns,
    rows: shown,
    truncated,
    ms,
  });
}

function handleSqlnbQuery(params: unknown): void {
  const p = asRecord(params);
  if (p.v !== SQLNB_VERSION) return; // mismatched v → ignore silently
  if (typeof p.id !== "number" || !isFinite(p.id)) return;
  if (typeof p.sql !== "string") return;
  runQuery(p.id, p.sql);
}

// --- rendering ----------------------------------------------------------------

function showError(message: string): void {
  if (!errorEl) return;
  errorEl.textContent = message;
  errorEl.hidden = false;
}



/** One display cell: NULL italic, numbers right-aligned, blobs sized. */
function appendCell(tr: HTMLTableRowElement, value: unknown): void {
  const td = document.createElement("td");
  if (value === null) {
    td.textContent = "NULL";
    td.className = "sqlnb-null";
  } else if (typeof value === "number") {
    td.textContent = String(value);
    td.className = "sqlnb-num";
  } else if (value instanceof Uint8Array) {
    td.textContent = `blob(${value.byteLength} B)`;
  } else {
    td.textContent = String(value);
  }
  tr.appendChild(td);
}

function renderResult(columns: string[], rows: unknown[][], truncated: boolean, ms: number): void {
  if (statusEl) {
    statusEl.textContent =
      columns.length === 0
        ? `ok · no result set · ${ms} ms`
        : `${rows.length} row${rows.length === 1 ? "" : "s"}${truncated ? ` (truncated at ${MAX_ROWS})` : ""} · ${ms} ms`;
  }
  if (!resultEl) return;
  resultEl.textContent = "";
  if (columns.length === 0) {
    const hint = document.createElement("div");
    hint.className = "sqlnb-hint";
    hint.textContent = "statement executed — no result set";
    resultEl.appendChild(hint);
    return;
  }
  const table = document.createElement("table");
  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  for (const col of columns) {
    const th = document.createElement("th");
    th.scope = "col";
    th.textContent = col;
    headRow.appendChild(th);
  }
  thead.appendChild(headRow);
  table.appendChild(thead);
  const tbody = document.createElement("tbody");
  for (const row of rows) {
    const tr = document.createElement("tr");
    for (const cell of row) appendCell(tr, cell);
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  resultEl.appendChild(table);
}

/** Rebuild the schema sidebar from sqlite_master + PRAGMA table_info. */
function refreshSchema(): void {
  if (!db || !tablesEl) return;
  interface TableSchema {
    name: string;
    cols: { name: string; type: string; pk: boolean }[];
  }
  const tables: TableSchema[] = [];
  try {
    const sets = db.exec(
      "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
    );
    const names = sets[0]?.values?.map((r) => String(r[0])) ?? [];
    for (const name of names) {
      const escaped = name.replace(/"/g, '""');
      const cols = db.exec(`PRAGMA table_info("${escaped}")`);
      tables.push({
        name,
        cols: (cols[0]?.values ?? []).map((r) => ({
          name: String(r[1]),
          type: r[2] === null || r[2] === undefined ? "" : String(r[2]),
          pk: Number(r[5]) > 0,
        })),
      });
    }
  } catch {
    return; // schema read failed; keep whatever is rendered
  }
  tablesEl.textContent = "";
  if (tables.length === 0) {
    const hint = document.createElement("div");
    hint.className = "sqlnb-hint";
    hint.textContent = "no tables yet";
    tablesEl.appendChild(hint);
    return;
  }
  for (const table of tables) {
    const details = document.createElement("details");
    details.open = true;
    const summary = document.createElement("summary");
    summary.textContent = table.name;
    details.appendChild(summary);
    const ul = document.createElement("ul");
    for (const col of table.cols) {
      const li = document.createElement("li");
      li.appendChild(document.createTextNode(col.name));
      if (col.pk) {
        // "PK", not a glyph. A pilcrow means "paragraph" to every reader who
        // has met one before, and a marker nobody can name is decoration.
        // Every SQL tool spells this one out, and the title gives a screen
        // reader something to say.
        const pk = document.createElement("span");
        pk.className = "sqlnb-col-pk";
        pk.textContent = "PK";
        pk.title = "primary key";
        li.appendChild(pk);
      }
      if (col.type !== "") {
        const type = document.createElement("span");
        type.className = "sqlnb-col-type";
        type.textContent = " " + col.type;
        li.appendChild(type);
      }
      ul.appendChild(li);
    }
    details.appendChild(ul);
    tablesEl.appendChild(details);
  }
}

// --- host → plugin handlers ----------------------------------------------------

function handleInit(params: unknown): void {
  const p = asRecord(params);
  applyTokens(p.tokens);
  applyScheme(p.scheme);
  sendEvent("themeApplied", { scheme: p.scheme, sample: sampleAppliedTokens(SAMPLE_TOKENS) });
}

function handleThemeChanged(params: unknown): void {
  const p = asRecord(params);
  applyTokens(p.tokens);
  applyScheme(p.scheme);
  sendEvent("themeApplied", { scheme: p.scheme, sample: sampleAppliedTokens(SAMPLE_TOKENS) });
}

// requestSave is a REQUEST → must answer. The notebook's document is the
// editor's SQL text; that is the honest round-trip for a reload.
function handleRequestSave(): Record<string, unknown> {
  return { doc: sqlInput ? sqlInput.value : null, schemaVersion: SCHEMA_VERSION };
}

// teardown is a REQUEST → return {} after a clean teardown.
function handleTeardown(): Record<string, never> {
  if (db) {
    try {
      db.close();
    } catch {
      /* already closed */
    }
    db = null;
  }
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
  return {};
}

// --- lifecycle -----------------------------------------------------------------

// §8a: self-isolation probes, computed INSIDE the opaque frame at boot.
// Under sandbox="allow-scripts" (no allow-same-origin) each access throws —
// which is exactly the third-party guarantee.
function isolationProbes(): { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } {
  let cookieEmpty = false;
  let parentBlocked = false;
  let storageBlocked = false;
  try {
    cookieEmpty = document.cookie === "";
  } catch {
    cookieEmpty = true;
  }
  try {
    void (window.parent as unknown as { document?: unknown }).document;
  } catch {
    parentBlocked = true;
  }
  try {
    void window.localStorage.length;
  } catch {
    storageBlocked = true;
  }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

function runFromUi(): void {
  if (!sqlInput) return;
  const sql = sqlInput.value;
  if (sql.trim() === "") {
    showError("nothing to run — the SQL cell is empty");
    return;
  }
  // Negative id: UI-minted, disjoint from the host's positive query ids.
  runQuery(-(++uiQuerySeq), sql);
}

function boot(): void {
  runButton = document.getElementById("sqlnb-run") as HTMLButtonElement | null;
  sqlInput = document.getElementById("sqlnb-sql") as HTMLTextAreaElement | null;
  statusEl = document.getElementById("sqlnb-status");
  errorEl = document.getElementById("sqlnb-error");
  resultEl = document.getElementById("sqlnb-result");
  tablesEl = document.getElementById("sqlnb-tables");
  if (!runButton || !sqlInput || !resultEl) return;

  runButton.addEventListener("click", runFromUi);
  sqlInput.addEventListener("keydown", (ev: KeyboardEvent) => {
    if (ev.key === "Enter" && (ev.ctrlKey || ev.metaKey)) {
      ev.preventDefault();
      runFromUi();
    }
  });

  if (typeof initSqlJs !== "function") {
    // The glue script tag failed (CSP/404) — the frame cannot ever start.
    sendEvent("bootError", { message: "sql-wasm.js did not load (initSqlJs missing)" });
    showError("sql-wasm.js did not load; the engine cannot start");
    return;
  }

  messageListener = createRouter({
    init: handleInit,
    themeChanged: handleThemeChanged,
    requestSave: handleRequestSave,
    teardown: handleTeardown,
    // The sqlnotebook channel: engine bytes + seed, then queries.
    "sqlnb/init": handleSqlnbInit,
    "sqlnb/query": handleSqlnbQuery,
  });
  window.addEventListener("message", messageListener);

  sendEvent("ready", {
    version: PLUGIN_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: 360,
    probes: isolationProbes(),
  });
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
