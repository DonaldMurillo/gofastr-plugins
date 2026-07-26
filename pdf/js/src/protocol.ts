// Protocol v1 — the frame side of the versioned postMessage capability bridge.
//
// Behavior-identical extraction of the spike's protocol layer. The frame's ONLY
// host channel is window.parent.postMessage (never host DOM/cookies/storage).
// Source identity is the load-bearing check (event.source === window.parent);
// event.origin is never consulted because an opaque-origin frame's origin is
// the literal string "null" and is therefore untrustworthy as an identity check
// (see docs/design/protocol-v1.md §4 "Source validation").

export const PROTOCOL_VERSION = 1;
export const REQUEST_TIMEOUT_MS = 5000;

export interface ProtocolError {
  code: string;
  message?: string;
}

export interface ProtocolMessage {
  v: number;
  id?: string;
  type: "request" | "response" | "event";
  src: "host" | "plugin";
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: ProtocolError | null;
}

export type Handler = (params: unknown, msg: ProtocolMessage) => unknown;
export type HandlerMap = Record<string, Handler>;

interface PendingEntry {
  resolve: (v: unknown) => void;
  reject: (e: unknown) => void;
  timer: number;
}

let idCounter = 0;
const pending = new Map<string, PendingEntry>();

// The frame's ONLY host channel. parent.postMessage — never host DOM/cookies.
// Centralized so the "parent only" intent holds at every egress site.
function post(msg: ProtocolMessage): void {
  window.parent.postMessage(msg, "*");
}

export function sendEvent(method: string, params: Record<string, unknown> = {}): void {
  post({ v: PROTOCOL_VERSION, id: "p-" + ++idCounter, type: "event", src: "plugin", method, params });
}

export function sendRequest(method: string, params: Record<string, unknown> = {}): Promise<unknown> {
  const id = "p-" + ++idCounter;
  const { promise, resolve, reject } = Promise.withResolvers<unknown>();
  const timer = window.setTimeout(() => {
    if (pending.delete(id)) reject({ code: "E_TIMEOUT", message: "request " + method + " timed out" });
  }, REQUEST_TIMEOUT_MS);
  pending.set(id, { resolve, reject, timer });
  post({ v: PROTOCOL_VERSION, id, type: "request", src: "plugin", method, params });
  return promise;
}

export function sendResponse(requestId: string, result: unknown, error: ProtocolError | null = null): void {
  post({ v: PROTOCOL_VERSION, id: requestId, type: "response", src: "plugin", result, error });
}

// Inbound envelope guard. Validates the load-bearing fields; method/params are
// narrowed by the caller handlers (each handler re-validates its own params).
function isEnvelope(data: unknown): data is ProtocolMessage {
  if (typeof data !== "object" || data === null) return false;
  return "v" in data && data.v === PROTOCOL_VERSION && "src" in data && data.src === "host" && "type" in data;
}

export function toError(e: unknown): ProtocolError {
  if (e && typeof e === "object" && "code" in e) {
    const eo = e as { code: unknown; message?: unknown };
    if (typeof eo.code === "string") return { code: eo.code, message: typeof eo.message === "string" ? eo.message : undefined };
  }
  const msg = e instanceof Error ? e.message : String(e);
  return { code: "E_INTERNAL", message: msg };
}

export function createRouter(handlers: HandlerMap): (event: MessageEvent) => void {
  return (event: MessageEvent) => {
    // Source identity check — NEVER event.origin (opaque frame's origin is "null").
    if (event.source !== window.parent) return;
    if (!isEnvelope(event.data)) return;
    const msg = event.data;
    if (msg.type === "response") {
      if (msg.id === undefined) return;
      const entry = pending.get(msg.id);
      if (!entry) return;
      window.clearTimeout(entry.timer);
      pending.delete(msg.id);
      if (msg.error) entry.reject(msg.error);
      else entry.resolve(msg.result);
      return;
    }
    if (msg.type === "request" || msg.type === "event") {
      const method = typeof msg.method === "string" ? msg.method : "";
      const handler = handlers[method];
      if (!handler) return; // §4: unknown method → ignore
      try {
        const out = handler(msg.params, msg);
        if (msg.type === "request" && msg.id !== undefined) {
          if (out instanceof Promise) {
            out.then((v) => sendResponse(msg.id!, v), (e: unknown) => sendResponse(msg.id!, null, toError(e)));
          } else {
            sendResponse(msg.id, out);
          }
        }
      } catch (e: unknown) {
        if (msg.type === "request" && msg.id !== undefined) sendResponse(msg.id, null, toError(e));
      }
    }
  };
}
