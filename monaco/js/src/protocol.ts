// Protocol v1 postMessage client (the frame side). See protocol-v1.md §3/§4.
//
// Envelope: {v:1, id, type:"request"|"response"|"event", src:"plugin",
// method, params, result, error}. Plugin mints ids "p-<n>". Responses echo the
// request id and OMIT method. Source check: drop any message whose
// event.source !== window.parent. The frame's only host channel is
// window.parent.postMessage — it never touches host DOM/cookies/storage.

export const PROTOCOL_VERSION = 1;

/** Structured protocol error carried in the envelope's `error` slot. */
export interface ProtocolError {
  code: string;
  message: string;
}

/** The protocol v1 envelope as it crosses the postMessage boundary. */
export interface ProtocolMessage {
  v: number;
  id: string;
  type: "request" | "response" | "event";
  src: "plugin" | "host";
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: ProtocolError | null;
}

// Handler params are `unknown`: postMessage payloads are untyped at the
// boundary; each handler validates/narrows its own params defensively (no `any`
// — precision at the boundary is the whole point).
export type Handler = (params: unknown, msg: ProtocolMessage) => unknown;
export type HandlerMap = Record<string, Handler>;

interface PendingEntry {
  resolve: (value: unknown) => void;
  reject: (reason?: unknown) => void;
  timer: number;
}

let idCounter = 0;
const pending = new Map<string, PendingEntry>(); // id -> {resolve, reject, timer}
const REQUEST_TIMEOUT_MS = 5000;

function nextId(): string {
  idCounter += 1;
  return `p-${idCounter}`;
}

function post(msg: object): void {
  // The frame runs under sandbox="allow-scripts" (opaque origin). postMessage is
  // the sanctioned outbound channel; targetOrigin "*" is required because the
  // parent origin is opaque to us and we must not assume it.
  window.parent.postMessage(msg, "*");
}

/** Fire-and-forget event to the host (fresh correlation id, never answered). */
export function sendEvent(method: string, params: Record<string, unknown> = {}): void {
  post({
    v: PROTOCOL_VERSION,
    id: nextId(),
    type: "event",
    src: "plugin",
    method,
    params,
  });
}

/** Request -> Promise<result>. Rejects with {code:"E_TIMEOUT"} after 5 s. */
export function sendRequest(method: string, params: Record<string, unknown> = {}): Promise<unknown> {
  const id = nextId();
  const { promise, resolve, reject } = Promise.withResolvers<unknown>();
  const timer = setTimeout(() => {
    if (pending.delete(id)) {
      reject({ code: "E_TIMEOUT", message: `plugin request "${method}" timed out` });
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

/**
 * Build a message router from a {method -> handler(params, msg)} map.
 * - host `response` resolves/rejects the matching pending request.
 * - host `event` is fire-and-forget: handler return value is ignored.
 * - host `request` MUST be answered: the router sends a response carrying the
 *   handler's resolved value (or an error object on throw).
 * The router enforces the source check and envelope validation.
 */
export function createRouter(handlers: HandlerMap): (event: MessageEvent) => void {
  return function route(event: MessageEvent): void {
    if (event.source !== window.parent) return;
    const msg = event.data as ProtocolMessage | null;
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

    // event or request
    const handler = handlers[msg.method ?? ""];
    if (typeof handler !== "function") return; // unknown method -> ignore
    if (msg.type === "event") {
      try {
        handler(msg.params ?? {}, msg);
      } catch (err) {
        // events are fire-and-forget; swallow handler errors (log only)
        console.error("[monaco] event handler error:", msg.method, err);
      }
      return;
    }
    if (msg.type === "request") {
      Promise.resolve()
        .then(() => handler(msg.params ?? {}, msg))
        .then(
          (result) => sendResponse(msg.id, result === undefined ? null : result),
          (err: unknown) => {
            const e = err as { code?: string; message?: unknown } | null | undefined;
            sendResponse(
              msg.id,
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
