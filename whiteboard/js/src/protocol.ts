// Protocol v1 postMessage client (the frame side). See protocol-v1.md §3/§4.
//
// Envelope: {v:1, id, type:"request"|"response"|"event", src:"plugin",
// method, params, result, error}. Plugin mints ids "p-<n>". Responses echo
// the request id and OMIT method. Source check: drop any message whose
// event.source !== window.parent. The frame's only host channel is
// window.parent.postMessage — it never touches host DOM/cookies/storage,
// and never the network (the framed CSP sets connect-src 'none').
//
// Structurally the datagrid/mermaid frame client: the protocol is frozen and
// lives in the platform, so every plugin frames the same client. Divergence
// (carried over deliberately): handler params are `unknown` (never `any`) —
// each handler narrows its own payload defensively.

export const PROTOCOL_VERSION = 1;

/**
 * Promise.withResolvers, ponyfilled for engines that predate it (Safari < 17.4,
 * older embedded Chromia). The constructor form runs only when the native
 * method is missing; everything else uses the platform API.
 */
export function withResolvers<T>(): {
  promise: Promise<T>;
  resolve: (value: T | PromiseLike<T>) => void;
  reject: (reason?: unknown) => void;
} {
  if (typeof Promise.withResolvers === "function") {
    return Promise.withResolvers<T>();
  }
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/** Structured protocol error carried in the envelope's `error` slot. */
export interface ProtocolError {
  code: string;
  message?: string;
}

/** The protocol v1 envelope as it crosses the postMessage boundary. */
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

/** A pending sendRequest entry: resolvers plus the E_TIMEOUT timer. */
export interface PendingEntry {
  resolve: (value: unknown) => void;
  reject: (reason: unknown) => void;
  timer: number;
}

// postMessage payloads are untyped at the boundary; every handler validates
// and narrows its own params.
type Handler = (params: unknown, msg: ProtocolMessage) => unknown;
export type HandlerMap = Record<string, Handler>;

let idCounter = 0;
const pending = new Map<string, PendingEntry>(); // dynamic reqId-keyed membership
const REQUEST_TIMEOUT_MS = 5000;

function nextId(): string {
  idCounter += 1;
  return `p-${idCounter}`;
}

function post(msg: object): void {
  window.parent.postMessage(msg, "*");
}

/** Fire-and-forget event to the host (fresh correlation id, never answered). */
export function sendEvent(method: string, params: Record<string, unknown> = {}): void {
  post({ v: PROTOCOL_VERSION, id: nextId(), type: "event", src: "plugin", method, params });
}

/**
 * Request → Promise<result>. The frame uses this sparingly (the protocol's
 * first-class request direction is host→plugin); rejects with
 * {code:"E_TIMEOUT"} after 5 s.
 */
export function sendRequest(method: string, params: Record<string, unknown> = {}): Promise<unknown> {
  const { promise, resolve, reject } = withResolvers<unknown>();
  const id = nextId();
  const timer = window.setTimeout(() => {
    if (pending.has(id)) {
      pending.delete(id);
      reject({ code: "E_TIMEOUT", message: `request ${method} timed out` });
    }
  }, REQUEST_TIMEOUT_MS);
  pending.set(id, { resolve, reject, timer });
  post({ v: PROTOCOL_VERSION, id, type: "request", src: "plugin", method, params });
  return promise;
}

/** Respond to a host request, echoing its id; result OR {code,message} error. */
export function sendResponse(requestId: string, result: unknown, error: ProtocolError | null = null): void {
  post({ v: PROTOCOL_VERSION, id: requestId, type: "response", src: "plugin", result, error });
}

/** Reject every still-pending request (used on teardown so nothing hangs). */
export function rejectAllPending(reason: ProtocolError): void {
  for (const entry of pending.values()) {
    window.clearTimeout(entry.timer);
    entry.reject(reason);
  }
  pending.clear();
}

/**
 * Build a message router from a {method → handler(params, msg)} map.
 * Host-originated requests are answered with the handler's return value (or
 * its thrown error as an E_INTERNAL response); events are fire-and-forget and
 * handler errors are logged, never propagated into the host page.
 */
export function createRouter(handlers: HandlerMap): (event: MessageEvent) => void {
  return function route(event: MessageEvent): void {
    if (event.source !== window.parent) return;
    const msg = event.data as ProtocolMessage | null;
    if (!msg || typeof msg !== "object") return;
    if (msg.v !== PROTOCOL_VERSION || msg.src !== "host") return;

    if (msg.type === "response") {
      const entry = pending.get(msg.id ?? "");
      if (!entry) return;
      window.clearTimeout(entry.timer);
      pending.delete(msg.id ?? "");
      if (msg.error) entry.reject(msg.error);
      else entry.resolve(msg.result);
      return;
    }

    // event or request
    const handler = handlers[msg.method ?? ""];
    if (typeof handler !== "function") return; // unknown method → ignore
    if (msg.type === "event") {
      try {
        handler(msg.params ?? {}, msg);
      } catch (err) {
        // events are fire-and-forget; swallow handler errors (log only)
        console.error("[whiteboard] event handler error:", msg.method, err);
      }
      return;
    }
    if (msg.type === "request") {
      Promise.resolve()
        .then(() => handler(msg.params ?? {}, msg))
        .then(
          (result) => sendResponse(msg.id ?? "", result === undefined ? null : result),
          (err: unknown) => {
            const e = err as { code?: string; message?: unknown } | null | undefined;
            sendResponse(
              msg.id ?? "",
              null,
              e && e.code
                ? (e as ProtocolError)
                : { code: "E_INTERNAL", message: String(e && e.message ? e.message : e) }
            );
          }
        );
    }
  };
}
