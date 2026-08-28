// Frame→host correlated-event bridge (the imageedit addition to protocol v1).
//
// The protocol's `request` type is host→plugin only, so the frame cannot
// issue a first-class request. Instead it uses the richtext
// requestUpload → uploadResult pattern: fire-and-forget events carrying a
// `reqId`, answered by a correlated result event. This module owns the
// reqId ↔ pending-promise table and the payload guards for the three pairs:
//
//   requestImage   {reqId}                     → imageResult   {reqId, bytes: ArrayBuffer, mime}
//   requestUpload  {reqId, name, type, bytes}  → uploadResult  {reqId, id} | {reqId, error}
//   requestExport  {reqId, doc}                → exportResult  {reqId, url, width, height, …}
//
// Bytes cross as ArrayBuffer (structured clone, transferable) — the frame
// never fetches (connect-src 'none'); the host does the authenticated HTTP.

import { sendEvent, withResolvers } from "./protocol";
import type { ProtocolError } from "./protocol";
import type { Doc } from "./render";

export interface ImageResult {
  bytes: ArrayBuffer;
  mime: string;
}

export interface ExportResult {
  url: string;
  format: string;
  width: number;
  height: number;
  byteLength: number;
  sha256: string;
  verify: boolean;
}

/** One outstanding round trip: resolvers, the E_TIMEOUT timer, and the
 * payload narrowing this request's result must pass before resolving. */
interface Settler {
  resolve: (value: unknown) => void;
  reject: (reason: unknown) => void;
  timer: number;
  narrow: (raw: Record<string, unknown>) => unknown;
}

// Dynamic reqId-keyed membership with per-entry timers → Map, not Record.
const pending = new Map<string, Settler>();
let reqCounter = 0;

const RESULT_METHODS: Record<string, true> = {
  imageResult: true,
  uploadResult: true,
  exportResult: true,
};

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null;
}

/**
 * Core round trip: emit `method` with a fresh reqId, resolve on the matching
 * result event. Image relay and export can legitimately take longer than the
 * protocol's 5 s host-request budget on a big file, so the timeout is
 * per-call. Every result payload is an untrusted postMessage object; the
 * `narrow` guard runs BEFORE the caller sees a typed value.
 */
function bridgeRequest<T>(
  method: string,
  params: Record<string, unknown>,
  timeoutMs: number,
  narrow: (raw: Record<string, unknown>) => T
): Promise<T> {
  reqCounter += 1;
  const reqId = `b-${reqCounter}`;
  const { promise, resolve, reject } = withResolvers<T>();
  const timer = window.setTimeout(() => {
    if (pending.delete(reqId)) {
      reject({ code: "E_TIMEOUT", message: `${method} timed out` });
    }
  }, timeoutMs);
  pending.set(reqId, {
    resolve: resolve as (value: unknown) => void,
    reject: reject as (reason: unknown) => void,
    timer,
    narrow: narrow as (raw: Record<string, unknown>) => unknown,
  });
  sendEvent(method, { reqId, ...params });
  return promise;
}

/** Route a result event to its pending request. Returns true when handled. */
export function handleBridgeResult(method: string, params: unknown): boolean {
  if (!RESULT_METHODS[method]) return false;
  const p = isObject(params) ? params : {};
  const reqId = typeof p.reqId === "string" ? p.reqId : "";
  const entry = reqId ? pending.get(reqId) : undefined;
  if (!entry) return false;
  pending.delete(reqId);
  clearTimeout(entry.timer);
  if (typeof p.error === "string" && p.error !== "") {
    entry.reject({ code: p.error, message: String(p.message ?? p.error) });
    return true;
  }
  try {
    entry.resolve(entry.narrow(p));
  } catch (err) {
    entry.reject({ code: "E_BAD_PAYLOAD", message: String(err) });
  }
  return true;
}

/** Reject everything still outstanding (teardown — nothing may hang). */
export function rejectAllPending(reason: ProtocolError): void {
  for (const entry of pending.values()) {
    clearTimeout(entry.timer);
    entry.reject(reason);
  }
  pending.clear();
}

const RELAY_TIMEOUT_MS = 15000;

// --- typed request wrappers --------------------------------------------------

export function requestImage(ref: string): Promise<ImageResult> {
  return bridgeRequest(
    "requestImage",
    { ref },
    RELAY_TIMEOUT_MS,
    (raw): ImageResult => {
      const bytes = raw.bytes;
      if (!(bytes instanceof ArrayBuffer)) throw new Error("imageResult missing bytes");
      return { bytes, mime: typeof raw.mime === "string" ? raw.mime : "image/png" };
    }
  );
}

export function requestUpload(name: string, type: string, bytes: ArrayBuffer): Promise<{ id: string }> {
  return bridgeRequest(
    "requestUpload",
    { name, type, bytes },
    RELAY_TIMEOUT_MS,
    (raw): { id: string } => {
      if (typeof raw.id !== "string" || raw.id === "") throw new Error("uploadResult missing id");
      return { id: raw.id };
    }
  );
}

export function requestExport(doc: Doc): Promise<ExportResult> {
  return bridgeRequest(
    "requestExport",
    { doc: doc as unknown as Record<string, unknown> },
    RELAY_TIMEOUT_MS,
    (raw): ExportResult => {
      if (typeof raw.url !== "string" || raw.url === "") throw new Error("exportResult missing url");
      return {
        url: raw.url,
        format: typeof raw.format === "string" ? raw.format : "",
        width: typeof raw.width === "number" ? raw.width : 0,
        height: typeof raw.height === "number" ? raw.height : 0,
        byteLength: typeof raw.bytes === "number" ? raw.bytes : 0,
        sha256: typeof raw.sha256 === "string" ? raw.sha256 : "",
        verify: raw.verify === true,
      };
    }
  );
}
