// Protocol v1 postMessage client (the frame side). See protocol-v1.md §3/§4.
//
// Envelope: {v:1, id, type:"request"|"response"|"event", src:"plugin",
// method, params, result, error}. Plugin mints ids "p-<n>". Responses echo the
// request id and OMIT method. Source check: drop any message whose
// event.source !== window.parent. The frame's only host channel is
// window.parent.postMessage — it never touches host DOM/cookies/storage.

import { PROTOCOL_VERSION } from "./schema.ts";

/** Protocol v1 envelope (protocol-v1.md §3). */
export interface Envelope {
  v: number;
  id: string;
  type: "request" | "response" | "event";
  src: "plugin" | "host";
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: ProtocolError | null;
}

export interface ProtocolError {
  code: string;
  message: string;
}

export type Handler = (params: Record<string, unknown>, msg: Envelope) => unknown;

interface PendingEntry {
  resolve: (result: unknown) => void;
  reject: (err: ProtocolError) => void;
  timer: ReturnType<typeof setTimeout>;
}

/**
 * Outbound transport. Default: window.parent.postMessage (the sandboxed-frame
 * channel). The trusted in-page mount swaps this for direct host callbacks —
 * same envelopes, no wire. Swapping the transport never weakens the framed
 * path: the frame bundle never calls setTransport.
 */
export interface Transport {
  post(msg: Envelope): void;
}

// The default (frame) transport: postMessage to the opaque-origin parent.
// targetOrigin "*" is required because the parent origin is opaque to us and
// we must not assume it. Exported so the trusted mount can restore it on
// destroy() (leaving a stale direct-call transport installed would break a
// later framed mount in the same realm).
export const defaultTransport: Transport = {
  post(msg) {
    window.parent.postMessage(msg, "*");
  },
};

let transport: Transport = defaultTransport;

export function setTransport(t: Transport) {
  transport = t;
}

let idCounter = 0;
const pending = new Map<string, PendingEntry>();
const REQUEST_TIMEOUT_MS = 5000;

function nextId() {
  idCounter += 1;
  return `p-${idCounter}`;
}

function post(msg: Envelope) {
  transport.post(msg);
}

/** Fire-and-forget event to the host (fresh correlation id, never answered). */
export function sendEvent(method: string, params: unknown = {}) {
  post({
    v: PROTOCOL_VERSION,
    id: nextId(),
    type: "event",
    src: "plugin",
    method,
    params,
  });
}

/** Request → Promise<result>. Rejects with {code:"E_TIMEOUT"} after 5 s. */
export function sendRequest(method: string, params: unknown = {}): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const id = nextId();
    const timer = setTimeout(() => {
      if (pending.delete(id)) {
        reject({ code: "E_TIMEOUT", message: `plugin request "${method}" timed out` });
      }
    }, REQUEST_TIMEOUT_MS);
    pending.set(id, { resolve, reject, timer });
    post({ v: PROTOCOL_VERSION, id, type: "request", src: "plugin", method, params });
  });
}

/** Respond to a host request, echoing its id; result OR {code,message} error. */
export function sendResponse(requestId: string, result: unknown, error: ProtocolError | null = null) {
  post({ v: PROTOCOL_VERSION, id: requestId, type: "response", src: "plugin", result, error });
}

/**
 * Build a message router from a {method → handler(params, msg)} map.
 * - host `response` resolves/rejects the matching pending request.
 * - host `event` is fire-and-forget: handler return value is ignored.
 * - host `request` MUST be answered: the router sends a response carrying the
 *   handler's resolved value (or an error object on throw).
 * The router enforces the source check and envelope validation.
 */
export function createRouter(handlers: Record<string, Handler>) {
  return function route(event: MessageEvent) {
    if (event.source !== window.parent) return;
    routeEnvelope(handlers, event.data as Envelope);
  };
}

/**
 * Envelope-level router shared by both mounts: the framed path wraps it with
 * the postMessage source check above; the trusted in-page mount delivers host
 * envelopes to it directly (no MessageEvent, no wire).
 */
export function routeEnvelope(handlers: Record<string, Handler>, msg: Envelope) {
  if (!msg || typeof msg !== "object") return;
  if (msg.v !== PROTOCOL_VERSION || msg.src !== "host") return;

  if (msg.type === "response") {
    const entry = pending.get(msg.id);
    if (!entry) return;
    clearTimeout(entry.timer);
    pending.delete(msg.id);
    if (msg.error) entry.reject(msg.error);
    else entry.resolve(msg.result);
    return;
  }

  // event or request. hasOwnProperty guard so a method named "constructor"/
  // "valueOf" can't resolve an inherited Object.prototype function (host→frame
  // only, but no reason to be sloppy).
  const handler =
    msg.method && Object.prototype.hasOwnProperty.call(handlers, msg.method)
      ? handlers[msg.method]
      : undefined;
  if (typeof handler !== "function") {
    // Unknown EVENT → ignore (additive, non-breaking). Unknown REQUEST → answer
    // with an error, or the caller's promise never settles (the trusted mount
    // has no host-side timeout to rescue it).
    if (msg.type === "request") {
      sendResponse(msg.id, null, { code: "E_UNSUPPORTED", message: `unknown method: ${msg.method}` });
    }
    return;
  }
  if (msg.type === "event") {
    try {
      handler((msg.params as Record<string, unknown>) || {}, msg);
    } catch (err) {
      // events are fire-and-forget; swallow handler errors (Phase-0: log only)
      console.error("[richtext] event handler error:", msg.method, err);
    }
    return;
  }
  if (msg.type === "request") {
    Promise.resolve()
      .then(() => handler((msg.params as Record<string, unknown>) || {}, msg))
      .then(
        (result) => sendResponse(msg.id, result === undefined ? null : result),
        (err: unknown) => {
          const e = err as { code?: string; message?: string };
          sendResponse(
            msg.id,
            null,
            e && e.code
              ? { code: e.code, message: e.message || "" }
              : { code: "E_INTERNAL", message: String(e && e.message ? e.message : err) }
          );
        }
      );
  }
}

