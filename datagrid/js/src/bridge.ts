// Frame→host correlated-event bridge (the datagrid addition to protocol v1).
//
// The protocol's `request` type is host→plugin only, so the frame cannot
// issue a first-class request. Instead it uses the richtext
// requestUpload → uploadResult pattern: fire-and-forget events carrying a
// `reqId`, answered by a correlated result event. This module owns the
// reqId ↔ pending-promise table and the payload guards for the three pairs:
//
//   requestRows      {reqId, startRow, endRow, sort, filter, columns}
//     → rowsResult   {reqId, rows, lastRow} | {reqId, error}
//   requestCellWrite {reqId, rowId, field, value}
//     → cellWriteResult {reqId, ok} | {reqId, error}
//   requestExport    {reqId, format, sort, filter, columns}
//     → exportResult {reqId, url, rowCount} | {reqId, error}
//
// Every result payload is an untrusted postMessage object: guards below
// narrow it before any consumer sees a typed value.

import { sendEvent, withResolvers } from "./protocol";

/** One grid column as it appears in the view-state doc and rows requests. */
export interface GridColumn {
  field: string;
  header: string;
  width?: number;
  type?: string;
  sortable?: boolean;
  filterable?: boolean;
  editable?: boolean;
}

/** One active server-side sort. */
export interface GridSort {
  field: string;
  dir: "asc" | "desc";
}

/** One row as it crosses the bridge: stable id + string cells by field. */
export interface GridRow {
  id: string;
  cells: Record<string, string>;
}

export interface RowsParams {
  startRow: number;
  endRow: number;
  sort: GridSort[];
  filter: string;
  columns: GridColumn[];
}

export interface RowsResult {
  rows: GridRow[];
  lastRow: number;
}

export interface CellWriteParams {
  rowId: string;
  field: string;
  value: string;
}

export interface ExportParams {
  format: "csv";
  sort: GridSort[];
  filter: string;
  columns: GridColumn[];
}

export interface ExportResult {
  url: string;
  rowCount: number;
}

/** A thrown bridge error: the {code} shape the protocol uses everywhere. */
export interface BridgeError {
  code: string;
  message?: string;
}

interface Settler {
  resolve: (value: unknown) => void;
  reject: (reason?: unknown) => void;
  timer: number;
}

// Dynamic reqId-keyed membership with per-entry timers → Map, not Record.
const pending = new Map<string, Settler>();
let reqCounter = 0;

const RESULT_METHODS: Record<string, true> = {
  rowsResult: true,
  cellWriteResult: true,
  exportResult: true,
};

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null;
}

/**
 * Core round trip: emit `method` with a fresh reqId, resolve on the matching
 * result event. Rows scans can legitimately take longer than the protocol's
 * 5 s request budget on a big table, so the timeout is per-call.
 */
function bridgeRequest(
  method: string,
  params: Record<string, unknown>,
  timeoutMs: number
): Promise<Record<string, unknown>> {
  reqCounter += 1;
  const reqId = `dg-${reqCounter}`;
  const { promise, resolve, reject } = withResolvers<unknown>();
  const timer = window.setTimeout(() => {
    if (pending.delete(reqId)) {
      reject({ code: "E_TIMEOUT", message: `${method} timed out` });
    }
  }, timeoutMs);
  pending.set(reqId, { resolve, reject, timer });
  sendEvent(method, { ...params, reqId });
  return promise.then((raw): Record<string, unknown> => {
    if (!isObject(raw)) {
      throw { code: "E_BAD_RESULT", message: `${method} result was not an object` };
    }
    return raw;
  });
}

/**
 * Route a result event to its pending request. Returns true when the method
 * is a bridge result AND a matching pending entry existed (consumed); the
 * frame router calls this before its own switch so unknown methods stay
 * unknown and ignored.
 */
export function handleBridgeResult(method: string, params: unknown): boolean {
  if (!RESULT_METHODS[method]) return false;
  if (!isObject(params) || typeof params.reqId !== "string") return false;
  const entry = pending.get(params.reqId);
  if (!entry) return false;
  clearTimeout(entry.timer);
  pending.delete(params.reqId);
  if (typeof params.error === "string") {
    entry.reject({ code: params.error });
  } else {
    entry.resolve(params);
  }
  return true;
}

/** Reject everything still outstanding (teardown — nothing may hang). */
export function rejectAllPending(reason: BridgeError): void {
  for (const entry of pending.values()) {
    clearTimeout(entry.timer);
    entry.reject(reason);
  }
  pending.clear();
}

// --- guards: narrow one result payload before handing it to the caller -----

function stringCells(raw: unknown): Record<string, string> {
  if (!isObject(raw)) return {};
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(raw)) {
    if (typeof v === "string") out[k] = v;
  }
  return out;
}

function parseRows(raw: unknown): GridRow[] {
  if (!Array.isArray(raw)) return [];
  const out: GridRow[] = [];
  for (const r of raw) {
    if (!isObject(r) || typeof r.id !== "string") continue;
    out.push({ id: r.id, cells: stringCells(r.cells) });
  }
  return out;
}

function requireSort(raw: unknown): GridSort[] {
  if (!Array.isArray(raw)) return [];
  const out: GridSort[] = [];
  for (const s of raw) {
    if (isObject(s) && typeof s.field === "string" && (s.dir === "asc" || s.dir === "desc")) {
      out.push({ field: s.field, dir: s.dir });
    }
  }
  return out;
}

/** Parse + validate the view-state doc (init.doc or a re-parsed mirror). */
export function parseDoc(raw: unknown): { columns: GridColumn[]; sort: GridSort[]; filter: string; pageSize: number } {
  if (!isObject(raw)) return { columns: [], sort: [], filter: "", pageSize: 0 };
  const columns: GridColumn[] = [];
  if (Array.isArray(raw.columns)) {
    for (const c of raw.columns) {
      if (!isObject(c) || typeof c.field !== "string" || c.field === "") continue;
      columns.push({
        field: c.field,
        header: typeof c.header === "string" && c.header !== "" ? c.header : c.field,
        width: typeof c.width === "number" && c.width > 0 ? c.width : undefined,
        type: c.type === "number" ? "number" : "text",
        sortable: c.sortable === true,
        filterable: c.filterable === true,
        editable: c.editable === true,
      });
    }
  }
  return {
    columns,
    sort: requireSort(raw.sort),
    filter: typeof raw.filter === "string" ? raw.filter : "",
    pageSize: typeof raw.pageSize === "number" && raw.pageSize > 0 ? Math.floor(raw.pageSize) : 0,
  };
}

// --- typed request wrappers -------------------------------------------------

const ROWS_TIMEOUT_MS = 15000;

export async function requestRows(params: RowsParams): Promise<RowsResult> {
  const raw = await bridgeRequest(
    "requestRows",
    params as unknown as Record<string, unknown>,
    ROWS_TIMEOUT_MS
  );
  return {
    rows: parseRows(raw.rows),
    lastRow: typeof raw.lastRow === "number" ? raw.lastRow : -1,
  };
}

export async function requestCellWrite(params: CellWriteParams): Promise<void> {
  await bridgeRequest(
    "requestCellWrite",
    params as unknown as Record<string, unknown>,
    ROWS_TIMEOUT_MS
  );
}

export async function requestExport(params: ExportParams): Promise<ExportResult> {
  const raw = await bridgeRequest(
    "requestExport",
    params as unknown as Record<string, unknown>,
    ROWS_TIMEOUT_MS
  );
  if (typeof raw.url !== "string" || raw.url === "") {
    throw { code: "E_BAD_RESULT", message: "exportResult carried no url" };
  }
  return {
    url: raw.url,
    rowCount: typeof raw.rowCount === "number" ? raw.rowCount : 0,
  };
}
