// GoFastr data grid — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage, or
// DOM, and never fetch (the framed CSP sets connect-src 'none'). On load it
// announces `ready`; the host replies `init` carrying the view-state doc
// (columns/sort/filter/pageSize — rows are NEVER in the doc), the theme
// tokens, and the granted capabilities; we then create the AG Grid infinite
// row model whose datasource turns every page request into a correlated
// requestRows → rowsResult bridge round trip. Sorting and filtering therefore
// happen in the HOST's Go rows source: the frame only ever holds pages.

import {
  AllCommunityModule,
  ModuleRegistry,
  createGrid,
} from "ag-grid-community";
import type {
  CellEditingStoppedEvent,
  ColDef,
  GridApi,
  GridOptions,
  ICellRendererParams,
  IGetRowsParams,
} from "ag-grid-community";

import { createRouter, sendEvent, rejectAllPending } from "./protocol";
import type { ProtocolError } from "./protocol";
import {
  handleBridgeResult,
  parseDoc,
  requestCellWrite,
  requestExport,
  requestRows,
  rejectAllPending as rejectBridge,
} from "./bridge";
import type { GridColumn, GridRow } from "./bridge";
import { applyScheme, applyTokens, gridTheme, sampleAppliedTokens } from "./theme";

ModuleRegistry.registerModules([AllCommunityModule]);

const SCHEMA_VERSION = "datagrid-v1";
const DOC_CHANGED_DEBOUNCE_MS = 500;
const AUTOSAVE_DEBOUNCE_MS = 1500;
const DEFAULT_PAGE_SIZE = 100;
// The block-cache bound — the load-bearing half of the volume claim. AG
// Grid's default maxBlocksInCache is UNLIMITED: without this the frame
// hoards every block ever fetched, and a scroll through 100,000 rows ends
// with 100,000 rows resident. The cap DERIVES from the page size so the
// resident-row ceiling is constant regardless of the doc: ceil(2,500 /
// blockSize) blocks ≤ 2,500 rows held, older blocks evicted as new ones
// load (AG Grid keeps the viewport's blocks plus the overflow prefetch).
const MAX_RESIDENT_ROWS = 2500;
const SAMPLE_TOKENS = ["--color-surface", "--color-text", "--color-border"];
// The closed status vocabulary rendered as tinted pills (.dg-pill-* in
// grid.css): a cell whose value is EXACTLY one of these words renders as a
// pill; every other value takes AG Grid's default text rendering untouched,
// so a host's own data is never re-styled by guesswork. Tone colors come from
// bridged tokens only (success/warning/danger + neutrals), both schemes.
const STATUS_TONES: Record<string, string> = {
  active: "ok",
  pending: "warn",
  blocked: "bad",
  expired: "off",
};

/** The live view-state doc. Rows are never part of it. */
interface LiveDoc {
  columns: GridColumn[];
  sort: { field: string; dir: "asc" | "desc" }[];
  filter: string;
  pageSize: number;
}

// --- runtime state (module-scoped; single instance per frame) ---------------
let root: HTMLElement | null = null;
let gridEl: HTMLElement | null = null;
let filterInput: HTMLInputElement | null = null;
let countEl: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let exportBtn: HTMLButtonElement | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

let api: GridApi | null = null;
let doc: LiveDoc = { columns: [], sort: [], filter: "", pageSize: 0 };
let initialized = false;
let canWrite = false;
let canExport = false;
let scheme = "light";
let lastTokens: unknown = null;
let docChangedTimer: number | undefined;
let autosaveTimer: number | undefined;
// The cache bound computed in buildGrid (ceil(MAX_RESIDENT_ROWS / blockSize)),
// read by cacheState() below.
let cacheCap = 0;

function hasCap(list: string[], name: string): boolean {
  return list.includes(name);
}

/** Narrow an untrusted postMessage params object to a string-keyed record. */
function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

function setStatus(text: string): void {
  if (statusEl) statusEl.textContent = text;
}

function setCount(text: string): void {
  if (countEl) countEl.textContent = text;
}

// ---------------------------------------------------------------------------
// The bridge datasource: AG Grid's infinite model asks for a page, we turn it
// into a correlated requestRows event and hand the result back.

function datasource(): GridOptions["datasource"] {
  return {
    getRows(params: IGetRowsParams): void {
      // sortModel is the grid's CURRENT sort — the source of truth, synced
      // back into the view-state doc below. AG Grid purges the infinite
      // cache itself when the sort model changes, so no manual reload here.
      const sort = (params.sortModel ?? []).map((sm) => ({
        field: sm.colId,
        dir: sm.sort === "desc" ? ("desc" as const) : ("asc" as const),
      }));
      syncSort(sort);
      requestRows({
        startRow: params.startRow,
        endRow: params.endRow,
        sort,
        filter: doc.filter,
        columns: doc.columns,
      }).then(
        (res) => {
          setCount(res.lastRow >= 0 ? `${res.lastRow.toLocaleString("en-US")} rows` : "rows");
          // AG Grid's successCallback expects the page rows plus the total
          // row count under the current sort/filter (-1 = unknown). The
          // lib types rows as any[]; our rows are already validated.
          const pageRows: unknown[] = res.rows;
          params.successCallback(pageRows, res.lastRow);
          // After the page settles, publish AG Grid's own view of what the
          // frame now retains. The adapter mirrors this onto the iframe
          // element (__datagridCacheBlocks) so a host page — which cannot
          // reach into the opaque frame — can display the retention claim
          // live (the demo page's "resident blocks" readout reads it).
          sendEvent("cacheState", cacheState());
        },
        (err: ProtocolError) => {
          console.error("[datagrid] requestRows failed:", err);
          setStatus("Row fetch failed");
          params.failCallback();
        }
      );
    },
  };
}

// --- view-state plumbing ----------------------------------------------------

function syncSort(sort: LiveDoc["sort"]): void {
  const changed = JSON.stringify(sort) !== JSON.stringify(doc.sort);
  doc.sort = sort;
  if (changed) scheduleDocSync();
}

function currentDoc(): Record<string, unknown> {
  return {
    schemaVersion: SCHEMA_VERSION,
    columns: doc.columns,
    sort: doc.sort,
    filter: doc.filter,
    pageSize: doc.pageSize || DEFAULT_PAGE_SIZE,
  };
}

function scheduleDocSync(): void {
  window.clearTimeout(docChangedTimer);
  docChangedTimer = window.setTimeout(() => {
    sendEvent("docChanged", { doc: currentDoc(), dirty: true });
    if (!canWrite) return;
    window.clearTimeout(autosaveTimer);
    autosaveTimer = window.setTimeout(() => {
      sendEvent("save", { doc: currentDoc(), schemaVersion: SCHEMA_VERSION });
    }, AUTOSAVE_DEBOUNCE_MS);
  }, DOC_CHANGED_DEBOUNCE_MS);
}

// --- grid construction ------------------------------------------------------

function colDef(c: GridColumn): ColDef {
  return {
    colId: c.field,
    headerName: c.header,
    width: c.width,
    sortable: c.sortable,
    // Filtering is the toolbar's server-side substring box, not an AG Grid
    // column filter — the frame must not filter rows it does not hold.
    filter: false,
    resizable: true,
    editable: canWrite && c.editable === true,
    // AG Grid contract (warning #17): an editable valueGetter column needs a
    // valueSetter. The real write crosses the bridge in
    // onCellEditingStopped (server-side persistence + local page mutation on
    // success); returning false keeps AG's own data-update path out of it.
    valueSetter: () => false,
    type: c.type === "number" ? "rightAligned" : undefined,
    valueGetter: (p) => {
      // AG Grid types row data as `any`; every page came through the bridge
      // guards, so the shape is established — narrow once, here.
      const row = p.data as GridRow | undefined;
      return row && row.cells ? row.cells[c.field] ?? "" : "";
    },
    // Status vocabulary → pill, everything else → plain text. The renderer
    // ALWAYS returns an element (AG Grid v36 only appends HTMLElement
    // results); content is set with textContent, never innerHTML, because
    // cell values are untrusted bridged data.
    cellRenderer: renderCell,
  };
}

/** One cell: a token-tinted pill for the status vocabulary, a text span for
 *  every other value. */
function renderCell(p: ICellRendererParams): HTMLElement {
  const v = typeof p.value === "string" ? p.value : "";
  const el = document.createElement("span");
  if (STATUS_TONES[v]) {
    el.className = "dg-pill dg-pill-" + STATUS_TONES[v];
  } else {
    el.className = "dg-cell-text";
  }
  el.textContent = v;
  return el;
}

function buildGrid(): void {
  if (!gridEl || doc.columns.length === 0) return;
  const blockSize = doc.pageSize || DEFAULT_PAGE_SIZE;
  // ceil keeps the ceiling AT MOST MAX_RESIDENT_ROWS for any page size the
  // host allows (1..500); max(2, …) keeps AG Grid's own requirement that
  // the cache cover more than the viewport + overflow.
  const maxBlocksInCache = Math.max(2, Math.ceil(MAX_RESIDENT_ROWS / blockSize));
  cacheCap = maxBlocksInCache;
  const options: GridOptions = {
    theme: gridTheme(lastTokens),
    rowModelType: "infinite",
    datasource: datasource(),
    // Click-to-select (v36 adds a checkbox column unless it is turned off):
    // exists so the token-bridged selection styling is exercised on the demo.
    rowSelection: { mode: "singleRow", checkboxes: false, enableClickSelection: true },
    // THE bound: without this the cache is unbounded (AG Grid default).
    maxBlocksInCache,
    getRowId: (p) => {
      // Same any-typed lib data; ids were validated by the bridge guards.
      const row = p.data as GridRow | undefined;
      return row && typeof row.id === "string" ? row.id : "";
    },
    columnDefs: doc.columns.map(colDef),
    defaultColDef: { minWidth: 80 },
    headerHeight: 36,
    onCellEditingStopped: handleEditStopped,
  };
  // Apply the doc's initial sort through the grid so the first fetch (and
  // the header arrows) agree with the persisted view state.
  if (doc.sort.length > 0) {
    options.initialState = { sort: { sortModel: doc.sort.map((s) => ({ colId: s.field, sort: s.dir })) } };
  }
  api = createGrid(gridEl, options);
  // Cache-state hook: AG Grid's own view of what the frame retains, exposed
  // read-only so a parent-side harness can prove the resident block count
  // stays at or below the cap — rows delivered over the bridge say nothing
  // about what the frame KEEPS.
  (window as unknown as Record<string, unknown>).__datagridCacheState = cacheState;
}

/** AG Grid's own view of what the frame retains (getCacheBlockState). */
function cacheState(): { count: number; cap: number } {
  const state = api?.getCacheBlockState?.();
  return { count: state ? Object.keys(state).length : 0, cap: cacheCap };
}

// A cell edit crosses the bridge, is gated + persisted host-side, and only
// then mutates the local page copy. On failure the displayed value reverts
// to the server truth via refreshCells.
function handleEditStopped(e: CellEditingStoppedEvent): void {
  // Same any-typed lib data (see colDef's valueGetter note).
  const row = e.node.data as GridRow | undefined;
  const field = e.colDef.colId;
  const value = e.newValue;
  if (!row || !field || typeof value !== "string") return;
  const before = row.cells ? row.cells[field] ?? "" : "";
  if (value === before) return;
  setStatus("Saving…");
  requestCellWrite({ rowId: row.id, field, value }).then(
    () => {
      if (!row.cells) row.cells = {};
      row.cells[field] = value;
      e.api.refreshCells({ rowNodes: [e.node] });
      setStatus("Saved");
    },
    (err: ProtocolError) => {
      console.error("[datagrid] cell write failed:", err);
      e.api.refreshCells({ rowNodes: [e.node] });
      setStatus("Save failed");
    }
  );
}

function applyFilter(): void {
  const value = filterInput ? filterInput.value.trim() : "";
  if (value === doc.filter) return;
  doc.filter = value;
  setStatus("");
  // The substring filter is not an AG Grid filter model, so the grid cannot
  // know it changed: swap in a fresh datasource to purge + reload.
  if (api) api.setGridOption("datasource", datasource());
  scheduleDocSync();
}

function doExport(): void {
  setStatus("Exporting…");
  requestExport({ format: "csv", sort: doc.sort, filter: doc.filter, columns: doc.columns }).then(
    (res) => {
      setStatus(`Export ready (${res.rowCount.toLocaleString("en-US")} rows) — downloading`);
    },
    (err: ProtocolError) => {
      console.error("[datagrid] export failed:", err);
      setStatus(`Export failed: ${err.code ?? "error"}`);
    }
  );
}

// ---------------------------------------------------------------------------
// host → plugin handlers

function handleInit(params: unknown): void {
  if (initialized) return;
  initialized = true;
  const p = asRecord(params);
  const caps = Array.isArray(p.capabilities) ? p.capabilities.filter((c): c is string => typeof c === "string") : [];
  canWrite = hasCap(caps, "data:write");
  canExport = hasCap(caps, "data:export");
  scheme = typeof p.scheme === "string" ? p.scheme : "light";
  lastTokens = p.tokens;
  applyTokens(p.tokens);
  applyScheme(scheme);
  doc = parseDoc(p.doc);
  if (doc.columns.length === 0) {
    setCount("No columns configured");
    buildGrid();
    return;
  }
  buildGrid();
  if (filterInput && doc.filter) filterInput.value = doc.filter;
  if (exportBtn) exportBtn.hidden = !canExport;
  sendEvent("themeApplied", { scheme, sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS) });
}

function handleThemeChanged(params: unknown): void {
  const p = asRecord(params);
  scheme = typeof p.scheme === "string" ? p.scheme : scheme;
  lastTokens = p.tokens;
  applyTokens(p.tokens);
  applyScheme(scheme);
  if (api) api.setGridOption("theme", gridTheme(lastTokens));
  sendEvent("themeApplied", { scheme, sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS) });
}

// requestSave is a REQUEST → must answer with the current view-state doc.
function handleRequestSave(): Record<string, unknown> {
  return { doc: currentDoc(), schemaVersion: SCHEMA_VERSION };
}

// teardown is a REQUEST → return {} after a clean teardown (no leaked
// listeners, nothing left pending).
function handleTeardown(): Record<string, never> {
  teardown();
  return {};
}

// ---------------------------------------------------------------------------
// Lifecycle

// §8a: self-isolation probes, computed INSIDE the opaque frame at boot. Under
// sandbox="allow-scripts" (no allow-same-origin) each of these is blocked by
// the browser, so accessing them throws — which is exactly the third-party
// guarantee.
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

function announceReady(): void {
  sendEvent("ready", {
    version: SCHEMA_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: 480,
    probes: isolationProbes(),
  });
}

function teardown(): void {
  window.clearTimeout(docChangedTimer);
  window.clearTimeout(autosaveTimer);
  if (api) {
    api.destroy();
    api = null;
  }
  rejectAllPending({ code: "E_TEARDOWN", message: "frame torn down" });
  rejectBridge({ code: "E_TEARDOWN", message: "frame torn down" });
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
}

function onFilterKey(ev: KeyboardEvent): void {
  if (ev.key === "Enter") applyFilter();
}

function boot(): void {
  root = document.getElementById("datagrid-root");
  gridEl = document.getElementById("datagrid-grid");
  filterInput = document.getElementById("dg-filter") as HTMLInputElement | null;
  countEl = document.getElementById("dg-count");
  statusEl = document.getElementById("dg-status");
  exportBtn = document.getElementById("dg-export") as HTMLButtonElement | null;

  if (filterInput) filterInput.addEventListener("keydown", onFilterKey);
  if (exportBtn) exportBtn.addEventListener("click", doExport);

  messageListener = createRouter({
    init: handleInit,
    themeChanged: handleThemeChanged,
    requestSave: handleRequestSave,
    teardown: handleTeardown,
    // Result events for the frame→host bridge round trips. Everything else
    // (resize / focusChanged / hostPointerdown / bootError) needs no action.
    rowsResult: (params: unknown) => handleBridgeResult("rowsResult", params),
    cellWriteResult: (params: unknown) => handleBridgeResult("cellWriteResult", params),
    exportResult: (params: unknown) => handleBridgeResult("exportResult", params),
  });
  window.addEventListener("message", messageListener);
  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
