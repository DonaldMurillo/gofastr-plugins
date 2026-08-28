// Frame→host correlated-event bridge (the calendar addition to protocol v1).
//
// The protocol's `request` type is host→plugin only, so the frame cannot
// issue a first-class request. It uses the richtext requestUpload →
// uploadResult pattern: fire-and-forget events carrying a `reqId`, answered
// by a correlated result event. This module owns the reqId ↔ pending-promise
// table and the payload guards for the two pairs:
//
//   requestOccurrences {reqId, from, to}
//     → occurrencesResult {reqId, occurrences, conflicts, transitions, zone}
//   requestMove {reqId, eventId, date, dayDelta, minuteDelta}
//     → moveResult {reqId, occurrence, requestedWallMinutes,
//                   actualWallMinutes, elapsedMinutes, note, …}
//
// Every result payload is an untrusted postMessage object: guards below
// narrow it before any consumer sees a typed value. The frame renders the
// strings the server resolved and computes nothing it was not handed.

import { sendEvent, withResolvers } from "./protocol";

/** One resolved occurrence as it crosses the bridge (Go's Occurrence). */
export interface Occurrence {
  id: string;
  eventId: string;
  title: string;
  allDay: boolean;
  startUtc: string;
  endUtc: string;
  startWall: string;
  endWall: string;
  zone: string;
  zoneAbbr: string;
  startOffsetMinutes: number;
  recurring: boolean;
  exception: boolean;
  spansMidnight: boolean;
  days: number;
  conflictIds: string[] | null;
  dstNote: string;
}

/** One DST boundary the server flagged inside a window. */
export interface Transition {
  date: string;
  instantUtc: string;
  wallFrom: string;
  wallTo: string;
  deltaMinutes: number;
  kind: string;
}

export interface OccurrencesResult {
  occurrences: Occurrence[];
  conflicts: string[][];
  transitions: Transition[];
  zone: string;
  from: string;
  to: string;
}

/** The server's answer to one move intent — the demo's proof payload. */
export interface MoveResultData {
  occurrence: Occurrence;
  requestedWallMinutes: number;
  actualWallMinutes: number;
  elapsedMinutes: number;
  zone: string;
  zoneAbbr: string;
  offsetMinutes: number;
  note: string;
}

interface BridgeError {
  code: string;
  message?: string;
}

interface Settler {
  resolve: (v: Record<string, unknown>) => void;
  reject: (e: BridgeError) => void;
  timer: number;
}

// Dynamic reqId-keyed membership with per-entry timers → Map, not Record.
const pending = new Map<string, Settler>();
let reqCounter = 0;

const RESULT_METHODS: Record<string, true> = {
  occurrencesResult: true,
  moveResult: true,
};

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null;
}

/**
 * Core round trip: emit `method` with a fresh reqId, resolve on the matching
 * result event. Window fetches can legitimately take longer than the
 * protocol's 5 s request budget on a big month, so the timeout is per-call.
 */
function bridgeRequest(
  method: string,
  params: Record<string, unknown>,
  timeoutMs: number
): Promise<Record<string, unknown>> {
  const reqId = `c-${++reqCounter}`;
  const { promise, resolve, reject } = withResolvers<Record<string, unknown>>();
  const timer = window.setTimeout(() => {
    if (pending.delete(reqId)) {
      reject({ code: "E_TIMEOUT", message: `${method} timed out` });
    }
  }, timeoutMs);
  pending.set(reqId, { resolve, reject, timer });
  sendEvent(method, { ...params, reqId });
  return promise;
}

/**
 * Route a result event to its pending request. Returns true when the method
 * was one of ours (so the app router can stop), false otherwise.
 */
export function handleBridgeResult(method: string, params: unknown): boolean {
  if (!RESULT_METHODS[method]) return false;
  if (!isObject(params)) return true;
  const reqId = typeof params.reqId === "string" ? params.reqId : "";
  const entry = reqId ? pending.get(reqId) : undefined;
  if (!entry) return true;
  clearTimeout(entry.timer);
  pending.delete(reqId);
  if (typeof params.error === "string" && params.error) {
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

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function num(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

function parseOccurrence(raw: unknown): Occurrence | null {
  if (!isObject(raw)) return null;
  if (str(raw.id) === "" || str(raw.startWall) === "") return null;
  return {
    id: str(raw.id),
    eventId: str(raw.eventId),
    title: str(raw.title),
    allDay: raw.allDay === true,
    startUtc: str(raw.startUtc),
    endUtc: str(raw.endUtc),
    startWall: str(raw.startWall),
    endWall: str(raw.endWall),
    zone: str(raw.zone),
    zoneAbbr: str(raw.zoneAbbr),
    startOffsetMinutes: num(raw.startOffsetMinutes),
    recurring: raw.recurring === true,
    exception: raw.exception === true,
    spansMidnight: raw.spansMidnight === true,
    days: num(raw.days),
    conflictIds: Array.isArray(raw.conflictIds) ? raw.conflictIds.filter((c) => typeof c === "string") : [],
    dstNote: str(raw.dstNote),
  };
}

function parseOccurrences(raw: unknown): Occurrence[] {
  if (!Array.isArray(raw)) return [];
  const out: Occurrence[] = [];
  for (const item of raw) {
    const occ = parseOccurrence(item);
    if (occ) out.push(occ);
  }
  return out;
}

function parseTransitions(raw: unknown): Transition[] {
  if (!Array.isArray(raw)) return [];
  const out: Transition[] = [];
  for (const item of raw) {
    if (!isObject(item)) continue;
    const date = str(item.date);
    if (date === "") continue;
    out.push({
      date,
      instantUtc: str(item.instantUtc),
      wallFrom: str(item.wallFrom),
      wallTo: str(item.wallTo),
      deltaMinutes: num(item.deltaMinutes),
      kind: str(item.kind),
    });
  }
  return out;
}

// --- typed request wrappers -------------------------------------------------

const WINDOW_TIMEOUT_MS = 15000;

export async function requestOccurrences(from: string, to: string): Promise<OccurrencesResult> {
  const raw = await bridgeRequest("requestOccurrences", { from, to }, WINDOW_TIMEOUT_MS);
  return {
    occurrences: parseOccurrences(raw.occurrences),
    conflicts: Array.isArray(raw.conflicts)
      ? raw.conflicts.filter(
          (pair): pair is string[] =>
            Array.isArray(pair) && pair.length === 2 && pair.every((s) => typeof s === "string")
        )
      : [],
    transitions: parseTransitions(raw.transitions),
    zone: str(raw.zone),
    from: str(raw.from) || from,
    to: str(raw.to) || to,
  };
}

export async function requestMove(
  eventId: string,
  date: string,
  dayDelta: number,
  minuteDelta: number
): Promise<MoveResultData> {
  const raw = await bridgeRequest("requestMove", { eventId, date, dayDelta, minuteDelta }, WINDOW_TIMEOUT_MS);
  const occurrence = parseOccurrence(raw.occurrence);
  if (!occurrence) {
    throw { code: "E_BAD_RESULT", message: "moveResult carried no occurrence" };
  }
  return {
    occurrence,
    requestedWallMinutes: num(raw.requestedWallMinutes),
    actualWallMinutes: num(raw.actualWallMinutes),
    elapsedMinutes: num(raw.elapsedMinutes),
    zone: str(raw.zone),
    zoneAbbr: str(raw.zoneAbbr),
    offsetMinutes: num(raw.offsetMinutes),
    note: str(raw.note),
  };
}
