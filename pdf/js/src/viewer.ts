// GoFastr PDF viewer (SPIKE) — in-frame entry point (protocol v1, schema pdf-v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the host
// over window.parent.postMessage ONLY. Never touches host cookies, localStorage,
// or the host DOM. The PDF bytes arrive OVER THE BRIDGE (host adapter fetches the
// same-origin sample, forwards the ArrayBuffer); the frame has connect-src 'none'
// and fetches nothing.
//
// Worker-free pdf.js: the worker module's WorkerMessageHandler is imported on the
// main thread and assigned to globalThis.pdfjsWorker, so pdf.js's
// PDFWorker.#mainThreadWorkerMessageHandler returns it and the FAKE-WORKER path
// runs entirely on the main thread — no Worker() spawn, no blob, no fetch. This
// is the load-bearing trick under the framed CSP (connect-src 'none'; no blob:
// worker-src). See pdf.mjs lines 16015/16130.
//
// Canonical doc (schema pdf-v1): the frame is read-only for the spike; it emits
// a `rendered` event carrying page-1 stats (text, canvas non-blank, pixel sample)
// and a `resize` for the page height. No save path is exercised here.

import { getDocument, Util, version as pdfjsVersion } from "pdfjs-dist";
import { WorkerMessageHandler } from "pdfjs-dist/build/pdf.worker.mjs";
import type { PageViewport } from "pdfjs-dist";

// Hand the worker's exports to pdf.js so it takes the main-thread fake-worker
// path. Assigned once at module load, before any getDocument() call.
window.pdfjsWorker = { WorkerMessageHandler };

// pdf.js v6's fake-worker path (PDFWorker.#initialize, pdf.mjs:16015) returns
// BEFORE reading GlobalWorkerOptions.workerSrc when #mainThreadWorkerMessageHandler
// is set — so workerSrc is never fetched, never even read. Leaving it unset; any
// future code path that reads it would throw observably (caught by runRender).

// --- protocol v1 (the frame side) -----------------------------------------

const PROTOCOL_VERSION = 1;
const REQUEST_TIMEOUT_MS = 5000;

interface ProtocolError {
  code: string;
  message?: string;
}

interface ProtocolMessage {
  v: number;
  id?: string;
  type: "request" | "response" | "event";
  src: "host" | "plugin";
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: ProtocolError | null;
}

type Handler = (params: unknown, msg: ProtocolMessage) => unknown;
type HandlerMap = Record<string, Handler>;

interface PendingEntry {
  resolve: (result: unknown) => void;
  reject: (err: ProtocolError) => void;
  timer: number;
}

let idCounter = 0;
const pending = new Map<string, PendingEntry>();

function post(msg: ProtocolMessage): void {
  // The frame's ONLY host channel. parent.postMessage — never host DOM/cookies.
  window.parent.postMessage(msg, "*");
}

function sendEvent(method: string, params: Record<string, unknown> = {}): void {
  post({ v: PROTOCOL_VERSION, id: "p-" + ++idCounter, type: "event", src: "plugin", method, params });
}

function sendRequest(method: string, params: Record<string, unknown> = {}): Promise<unknown> {
  const id = "p-" + ++idCounter;
  const { promise, resolve, reject } = Promise.withResolvers<unknown>();
  const timer = window.setTimeout(() => {
    if (pending.delete(id)) reject({ code: "E_TIMEOUT", message: "request " + method + " timed out" });
  }, REQUEST_TIMEOUT_MS);
  pending.set(id, { resolve, reject, timer });
  post({ v: PROTOCOL_VERSION, id, type: "request", src: "plugin", method, params });
  return promise;
}

function sendResponse(requestId: string, result: unknown, error: ProtocolError | null = null): void {
  post({ v: PROTOCOL_VERSION, id: requestId, type: "response", src: "plugin", result, error });
}

// Inbound envelope guard. Validates the load-bearing fields; method/params are
// narrowed by the caller handlers (each handler re-validates its own params).
function isEnvelope(data: unknown): data is ProtocolMessage {
  if (typeof data !== "object" || data === null) return false;
  return "v" in data && data.v === PROTOCOL_VERSION && "src" in data && data.src === "host" && "type" in data;
}

function createRouter(handlers: HandlerMap): (event: MessageEvent) => void {
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

function toError(e: unknown): ProtocolError {
  if (e instanceof Error) return { code: "E_FRAME", message: e.message };
  return { code: "E_FRAME", message: String(e) };
}

// --- frame DOM + state -----------------------------------------------------

const SCHEMA_VERSION = "pdf-v1";
const RENDER_SCALE = 1.5;
const READY_MIN_HEIGHT = 320;

interface PdfFrameState {
  ready: boolean;
  rendered: boolean;
  error: string | null;
  probes: unknown;
  text: string;
  pageCount: number;
  nonBlank: boolean;
  nonWhitePixels: number;
  widthPx: number;
  heightPx: number;
  pdfjsVersion: string;
}

declare global {
  // One state object the host-side tests read inside the frame (mirrored onto the
  // iframe element by the adapter too, so the parent can read without crossing the
  // opaque boundary). See the README test notes.
  interface Window {
    __pdfState?: PdfFrameState;
    // Set at module load so pdf.js takes the main-thread fake-worker path.
    pdfjsWorker?: { WorkerMessageHandler: unknown } | undefined;
  }
}

const state: PdfFrameState = {
  ready: false,
  rendered: false,
  error: null,
  probes: null,
  text: "",
  pageCount: 0,
  nonBlank: false,
  nonWhitePixels: 0,
  widthPx: 0,
  heightPx: 0,
  pdfjsVersion,
};
window.__pdfState = state;

function $(id: string): HTMLElement | null {
  return document.getElementById(id);
}

function setStatus(text: string, kind: "ready" | "error" | "loading" = "loading"): void {
  const el = $("pdf-status");
  if (!el) return;
  el.textContent = text;
  el.dataset.state = kind;
}

function applyTokens(tokens: unknown): void {
  if (!tokens || typeof tokens !== "object") return;
  const blob: string[] = [":root {"];
  for (const [name, value] of Object.entries(tokens)) {
    if (typeof name === "string" && name.startsWith("--") && typeof value === "string") {
      blob.push("  " + name + ": " + value + ";");
    }
  }
  blob.push("}");
  if (blob.length <= 2) return; // no tokens — keep fallback :root in viewer.css
  const existing = document.getElementById("pdf-tokens");
  const style = existing instanceof HTMLStyleElement
    ? existing
    : (() => { const s = document.createElement("style"); s.id = "pdf-tokens"; document.head.appendChild(s); return s; })();
  style.textContent = blob.join("\n");
}

// --- isolation probes (§8a) ------------------------------------------------
// Under sandbox="allow-scripts" (no allow-same-origin) each access throws — the
// third-party guarantee. Reported in `ready` so the host can assert isolation.

interface IsolationProbes {
  cookieEmpty: boolean;
  parentBlocked: boolean;
  storageBlocked: boolean;
}

function isolationProbes(): IsolationProbes {
  let cookieEmpty = false;
  try {
    cookieEmpty = document.cookie === "";
  } catch { cookieEmpty = true; }
  let parentBlocked = false;
  try {
    // Reading parent.document throws on an opaque-origin frame.
    void window.parent.document;
    parentBlocked = false;
  } catch { parentBlocked = true; }
  let storageBlocked = false;
  try {
    void window.localStorage;
    storageBlocked = false;
  } catch { storageBlocked = true; }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

// --- PDF rendering ---------------------------------------------------------

interface PdfTextItem {
  str: string;
  transform: number[];
  width: number;
  height: number;
  hasEOL: boolean;
}

interface PdfTextContent {
  items: Array<PdfTextItem | { type: string; id: string }>;
}

interface RenderStats {
  pageCount: number;
  text: string;
  spanCount: number;
  nonBlank: boolean;
  nonWhitePixels: number;
  widthPx: number;
  heightPx: number;
}

interface CanvasSample {
  nonBlank: boolean;
  nonWhitePixels: number;
}

// Frame capability probe — empirically confirms what the sandbox allow-scripts
// (no allow-modals/downloads/popups) + opaque origin actually block. Decides
// whether download/print/clipboard must be HOST capabilities over the bridge
// (they must: none of them work in-frame).
interface FrameCaps {
  hasPrint: boolean;            // typeof window.print === "function" (calling it is blocked)
  clipboardWrite: string;       // "ok" or the rejection/error message
  allowedFeatures: string[];    // document.featurePolicy.allowedFeatures() if present
  origin: string;               // window.location.origin — "null" under opaque sandbox
}

// Build the selectable text overlay: one <span> per text item, positioned with
// Util.transform(viewport.transform, item.transform). The canvas shows the glyphs;
// this layer only provides hit-testing/selection (transparent text). The exact
// horizontal scale of each span is not critical for the spike — the assertion is
// that the DOM text layer CONTAINS the secret string, not pixel-exact layout.
function buildTextLayer(tc: PdfTextContent, viewport: PageViewport, layer: HTMLElement): { text: string; spanCount: number } {
  layer.replaceChildren();
  const parts: string[] = [];
  let spanCount = 0;
  for (const item of tc.items) {
    if (!("str" in item)) continue; // TextMarkedContent — structural, no glyphs
    parts.push(item.str);
    if (item.str.length === 0) continue;
    const span = document.createElement("span");
    span.textContent = item.str;
    const tx = Util.transform(viewport.transform, item.transform);
    const fontHeight = Math.hypot(tx[2], tx[3]);
    span.style.left = tx[4] + "px";
    span.style.top = tx[5] - fontHeight + "px";
    span.style.fontSize = Math.max(1, fontHeight) + "px";
    layer.appendChild(span);
    spanCount++;
  }
  return { text: parts.join(" "), spanCount };
}

// Sample every pixel; "non-blank" = at least one non-transparent, non-white pixel.
// Tainted canvas (SecurityError) reports nonBlank=false — pdf.js draws from the
// in-memory PDF data (no cross-origin images), so the canvas is NOT tainted here.
function sampleCanvas(ctx: CanvasRenderingContext2D, w: number, h: number): CanvasSample {
  let nonWhite = 0;
  try {
    const d = ctx.getImageData(0, 0, w, h).data;
    for (let i = 0; i < d.length; i += 4) {
      if (d[i + 3] === 0) continue; // transparent
      if (d[i] < 250 || d[i + 1] < 250 || d[i + 2] < 250) nonWhite++;
    }
  } catch {
    return { nonBlank: false, nonWhitePixels: 0 };
  }
  return { nonBlank: nonWhite > 0, nonWhitePixels: nonWhite };
}

async function renderPdf(bytes: Uint8Array): Promise<RenderStats> {
  // useSystemFonts:true => pdf.js uses the browser's installed Helvetica/Times/
  // Courier for the standard-14 fonts instead of fetching standardFontDataUrl
  // (impossible under connect-src 'none'). disableAutoFetch/disableStream make
  // the (worker-free) transport never attempt range/progressive fetches.
  // verbosity:0 (VerbosityLevel.ERRORS) keeps the console clean — the test gate
  // asserts ZERO console messages, and the fake-worker path otherwise warns once.
  // useWasm:false sends the JPEG 2000 / JBIG2 decoders straight down pdf.js's
  // pure-JS fallback path. WebAssembly cannot instantiate in this frame at all
  // (script-src has no 'wasm-unsafe-eval'), and without this the wasm attempt
  // fails and its fallback — reached by a dynamic import() an opaque origin can
  // never satisfy — fails too, leaving SCANNED pages blank with no error at all.
  // The fallbacks are statically inlined by pdfjsNoWasmFallbackPlugin in build.mjs.
  const task = getDocument({
    data: bytes,
    useSystemFonts: true,
    useWasm: false,
    disableAutoFetch: true,
    disableStream: true,
    verbosity: 0,
  });
  const pdf = await task.promise;
  const page = await pdf.getPage(1);
  const viewport = page.getViewport({ scale: RENDER_SCALE });

  const canvasEl = document.getElementById("pdf-canvas");
  const wrapper = $("pdf-page-wrapper");
  const layer = $("pdf-text-layer");
  if (!(canvasEl instanceof HTMLCanvasElement) || !wrapper || !layer) {
    throw new Error("viewer DOM is incomplete");
  }
  const ctx = canvasEl.getContext("2d");
  if (!ctx) throw new Error("2d canvas context unavailable");

  const outputScale = window.devicePixelRatio || 1;
  const cssW = Math.floor(viewport.width);
  const cssH = Math.floor(viewport.height);
  canvasEl.width = Math.floor(cssW * outputScale);
  canvasEl.height = Math.floor(cssH * outputScale);
  canvasEl.style.width = cssW + "px";
  canvasEl.style.height = cssH + "px";
  wrapper.style.width = cssW + "px";
  wrapper.style.height = cssH + "px";

  await page.render({
    canvas: canvasEl,
    viewport,
    transform: outputScale !== 1 ? [outputScale, 0, 0, outputScale, 0, 0] : undefined,
  }).promise;

  const textContent = await page.getTextContent();
  const { text, spanCount } = buildTextLayer(textContent, viewport, layer);
  const sample = sampleCanvas(ctx, canvasEl.width, canvasEl.height);

  const stats: RenderStats = {
    pageCount: pdf.numPages,
    text,
    spanCount,
    nonBlank: sample.nonBlank,
    nonWhitePixels: sample.nonWhitePixels,
    widthPx: cssW,
    heightPx: cssH,
  };
  // Release transport + worker-side resources (best-effort; fake worker is in-process).
  void pdf.cleanup();
  return stats;
}

// --- params narrowing (boundary validation) --------------------------------

function asBytes(params: unknown): Uint8Array | null {
  if (typeof params !== "object" || params === null || !("bytes" in params)) return null;
  const b = params.bytes;
  if (b instanceof Uint8Array) return b;
  if (b instanceof ArrayBuffer) return new Uint8Array(b);
  return null;
}

// --- boot ------------------------------------------------------------------

let activeBytes: Uint8Array | null = null;
let initReceived = false;
let rendering = false;

// Type guard for the tokens-bearing params shape shared by init/themeChanged.
function hasTokens(p: unknown): p is { tokens: unknown } {
  return !!p && typeof p === "object" && "tokens" in p;
}

function emitResize(heightPx: number): void {
  sendEvent("resize", { height: Math.ceil(heightPx) });
}

function announceReady(): void {
  state.ready = true;
  state.probes = isolationProbes();
  sendEvent("ready", { version: "0.1.0-spike", schemaVersion: SCHEMA_VERSION, minHeight: READY_MIN_HEIGHT, probes: state.probes });
}

// Empirically probe what the sandbox + opaque origin block: clipboard write,
// print availability, featurePolicy, and the frame's own origin string. The
// clipboard promise is awaited and its rejection captured so it never surfaces
// as an unhandled rejection (which would trip the zero-console-error gate).
async function probeCaps(): Promise<FrameCaps> {
  let clipboardWrite = "not-tested";
  try {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
      await navigator.clipboard.writeText("pdf-spike-probe");
      clipboardWrite = "ok";
    } else {
      clipboardWrite = "no navigator.clipboard.writeText";
    }
  } catch (e: unknown) {
    clipboardWrite = e instanceof Error ? e.message : String(e);
  }
  let allowedFeatures: string[] = [];
  const docFP = document as unknown as { featurePolicy?: { allowedFeatures: () => string[] } };
  if (docFP.featurePolicy && typeof docFP.featurePolicy.allowedFeatures === "function") {
    try { allowedFeatures = docFP.featurePolicy.allowedFeatures(); } catch { /* ignore */ }
  }
  return {
    hasPrint: typeof window.print === "function",
    clipboardWrite,
    allowedFeatures,
    origin: window.location.origin,
  };
}

async function runRender(): Promise<void> {
  // Exactly one render: never re-enter while rendering, never re-render after
  // success, and wait until BOTH init (tokens) and the bridged bytes are in.
  if (rendering || state.rendered) return;
  if (!activeBytes || !initReceived) return;
  rendering = true;
  try {
    setStatus("Rendering…");
    const stats = await renderPdf(activeBytes);
    state.rendered = true;
    state.text = stats.text;
    state.pageCount = stats.pageCount;
    state.nonBlank = stats.nonBlank;
    state.nonWhitePixels = stats.nonWhitePixels;
    state.widthPx = stats.widthPx;
    state.heightPx = stats.heightPx;
    emitResize(stats.heightPx);
    sendEvent("rendered", {
      pageCount: stats.pageCount,
      text: stats.text,
      spanCount: stats.spanCount,
      nonBlank: stats.nonBlank,
      nonWhitePixels: stats.nonWhitePixels,
      widthPx: stats.widthPx,
      heightPx: stats.heightPx,
      pdfjsVersion,
    });
    // Empirically confirm what the sandbox blocks (print/download/clipboard).
    // Fire-and-forget; the adapter mirrors it onto frame.__pdfCaps.
    void probeCaps().then(
      (caps) => sendEvent("caps", {
        hasPrint: caps.hasPrint,
        clipboardWrite: caps.clipboardWrite,
        allowedFeatures: caps.allowedFeatures,
        origin: caps.origin,
      }),
      (e: unknown) => { /* never reject unhandled */ void e; }
    );
    setStatus("Page 1 of " + stats.pageCount + " rendered (" + stats.nonWhitePixels + " inked px)", "ready");
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    state.error = msg;
    sendEvent("renderError", { message: msg });
    setStatus("Render failed: " + msg, "error");
  } finally {
    rendering = false;
  }
}

function boot(): void {
  const handlers: HandlerMap = {
    init: (params) => {
      if (hasTokens(params)) applyTokens(params.tokens);
      // The host also sends capabilities/scheme/schemaVersion; for the read-only
      // spike we only consume tokens. Mark init seen so runRender can proceed.
      initReceived = true;
      void runRender();
    },
    themeChanged: (params) => {
      if (hasTokens(params)) applyTokens(params.tokens);
    },
    loadBytes: (params) => {
      const bytes = asBytes(params);
      if (!bytes) {
        state.error = "loadBytes: missing {bytes}";
        sendEvent("renderError", { message: state.error });
        return;
      }
      activeBytes = bytes;
      void runRender();
    },
    requestSave: () => ({ doc: null, schemaVersion: SCHEMA_VERSION }),
    teardown: () => {
      activeBytes = null;
      return {};
    },
  };
  window.addEventListener("message", createRouter(handlers));
  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}

// Keep the unused export referenced for the type-strip pass — esbuild tree-
// shakes it; this is a no-op at runtime and documents the intent for readers.
void pdfjsVersion;
