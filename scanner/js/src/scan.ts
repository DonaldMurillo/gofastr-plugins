// GoFastr barcode scanner — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage, or
// DOM, and never fetch (the framed CSP sets connect-src 'none').
//
// The shape is the logstream shape with the camera turned inside out: an
// opaque origin cannot reach getUserMedia (measured: SecurityError under
// sandbox="allow-scripts", and allow="camera" does not help — the origin is
// still opaque), so the HOST page owns the MediaStream and PUSHES pixels
// across the bridge; this cage decodes them and hands back a string. Pixels
// never cross in the other direction and the cage still cannot open a socket.
//
// Flow control is the ack: the host keeps at most ONE scanFrame in flight and
// waits for its frameDone — decoded or not — before capturing the next. So
// EVERY scanFrame answers exactly one frameDone, including malformed ones
// (silence here would wedge the host's flight window forever).
//
// A frame with no code in it is the NORMAL case at scanRateHz frames a second
// (the rate is the host's business; this frame just handles whatever arrives):
// zxing reports "no code" by THROWING NotFoundException, which is caught and
// reported as frameDone{decoded:false} — never logged. A console error per
// blank frame would make the plugin unusable and fail the e2e suite, which
// asserts zero console errors.

// Deep imports into @zxing/library's ESM core, NOT the package root: the
// root index does `export * from './browser'` and the package carries no
// sideEffects flag, so esbuild cannot shake the Browser*Reader layer out —
// it was landing ~90 KB of getUserMedia/DOM code in a cage whose CSP and
// opaque origin make every line of it dead (and its subject, the camera,
// belongs to the host by design). The core readers/writers are pure JS with
// no DOM reach at all.
import BarcodeFormat from "@zxing/library/esm/core/BarcodeFormat";
import BinaryBitmap from "@zxing/library/esm/core/BinaryBitmap";
import DecodeHintType from "@zxing/library/esm/core/DecodeHintType";
import HybridBinarizer from "@zxing/library/esm/core/common/HybridBinarizer";
import MultiFormatReader from "@zxing/library/esm/core/MultiFormatReader";
import MultiFormatWriter from "@zxing/library/esm/core/MultiFormatWriter";
import RGBLuminanceSource from "@zxing/library/esm/core/RGBLuminanceSource";
import type BitMatrix from "@zxing/library/esm/core/common/BitMatrix";
import type Result from "@zxing/library/esm/core/Result";
import { createRouter, rejectAllPending, sendEvent } from "./protocol";
import { applyScheme, applyTokens, sampleAppliedTokens } from "./theme";

const SCHEMA_VERSION = "scanner-v1";
/** One decode result crossing the bridge per successful frame. */
const SAMPLE_TOKENS = ["--color-surface", "--color-text", "--color-border"];
/**
 * The scanSample QR is encoded at this canvas size — the measured case (a
 * 300x300 QR decodes in ~11 ms webkit / ~27 ms chromium in this cage).
 */
const SAMPLE_QR_SIZE = 300;
/** The fixed payload scanSample encodes — stable so e2e can assert it. */
const SAMPLE_TEXT = "https://gofastr.dev/plugins/scanner";
/**
 * Formats used when init.config carries none: the demo's own format. The
 * host's config normally overrides this with the full requested set.
 */
const DEFAULT_FORMATS: string[] = ["QR_CODE"];
/** scanStats is a mirror, not a stream: at most one every 500 ms. */
const STATS_MIN_INTERVAL_MS = 500;

/** What a successful decode hands to the UI and the bridge. */
interface DecodeHit {
  text: string;
  format: string;
  decodeMs: number;
  /** Which decoder read it. Reported because the answer differs by engine and
   *  a plugin that silently reads a code on one machine and not another is
   *  worse than one that reads it nowhere. */
  via: "native" | "zxing";
}

// --- runtime state (module-scoped; single instance per frame) ---------------
let root: HTMLElement | null = null;
let canvas: HTMLCanvasElement | null = null;
let ctx: CanvasRenderingContext2D | null = null;
let formatEl: HTMLElement | null = null;
let textEl: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

let reader: MultiFormatReader | null = null;
let initialized = false;

/** Decoder preference. "auto" means native-first, which is the shipping
 *  behaviour; the other two exist so the e2e can force EACH path on every
 *  engine. Without that, CI's Linux chromium (no BarcodeDetector) would only
 *  ever exercise zxing and a mac would only ever exercise native, and neither
 *  run would notice the other path rotting. */
let decoderPref: "auto" | "native" | "zxing" = "auto";

/** The platform decoder, where the engine has one. Preferred over zxing not
 *  for speed but for CORRECTNESS: zxing's JS port fails to read some valid QR
 *  codes its OWN encoder produces — measured, one 19-byte payload out of a
 *  bisected set, which BarcodeDetector reads without complaint (#19). Absent
 *  on Safari, Firefox, and Chromium on Linux, which is why the bundled
 *  fallback still ships. */
interface NativeDetector {
  detect(source: CanvasImageSource | ImageData): Promise<{ rawValue: string; format: string }[]>;
}
let nativeDetector: NativeDetector | null = null;
let nativeChecked = false;

function nativeAvailable(): boolean {
  return typeof (window as unknown as { BarcodeDetector?: unknown }).BarcodeDetector === "function";
}

/**
 * zxing's SCREAMING_SNAKE names are the plugin's vocabulary; the native API has
 * its own spelling. It is NOT just case: zxing says `PDF_417` and the platform
 * says `pdf417`. Lower-casing alone yields `pdf_417`, which is not a format any
 * engine knows — and the BarcodeDetector constructor REJECTS an unknown name
 * outright, so one wrong entry disables the native decoder entirely and
 * silently. It did, until this map existed.
 */
const NATIVE_BY_ZXING: Record<string, string> = {
  AZTEC: "aztec",
  CODABAR: "codabar",
  CODE_39: "code_39",
  CODE_93: "code_93",
  CODE_128: "code_128",
  DATA_MATRIX: "data_matrix",
  EAN_8: "ean_8",
  EAN_13: "ean_13",
  ITF: "itf",
  PDF_417: "pdf417",
  QR_CODE: "qr_code",
  UPC_A: "upc_a",
  UPC_E: "upc_e",
};
const ZXING_BY_NATIVE: Record<string, string> = Object.keys(NATIVE_BY_ZXING).reduce(
  (acc: Record<string, string>, k) => {
    acc[NATIVE_BY_ZXING[k]] = k;
    return acc;
  },
  {}
);

function toNativeFormat(f: string): string | null {
  return NATIVE_BY_ZXING[f] ?? null;
}
function fromNativeFormat(f: string): string {
  return ZXING_BY_NATIVE[f] ?? f.toUpperCase();
}

function getNativeDetector(): NativeDetector | null {
  if (nativeChecked) return nativeDetector;
  nativeChecked = true;
  if (!nativeAvailable()) return null;
  const Ctor = (window as unknown as { BarcodeDetector: new (o: { formats: string[] }) => NativeDetector })
    .BarcodeDetector;
  const wanted = activeFormats()
    .map(toNativeFormat)
    .filter((f): f is string => f !== null);
  try {
    nativeDetector = new Ctor({ formats: wanted });
  } catch {
    // Engines support different subsets and reject the WHOLE list if any entry
    // is unknown to them, so one exotic format would cost us the decoder. Fall
    // back to the format every implementation has; if even that is refused,
    // this engine has no usable native decoder and zxing carries the plugin.
    try {
      nativeDetector = new Ctor({ formats: ["qr_code"] });
    } catch {
      nativeDetector = null;
    }
  }
  return nativeDetector;
}

/** Minted seq for frame-GENERATED frames (scanSample): counts down from -1
 *  so it can never collide with a host-sequenced scanFrame (>= 0) — the host
 *  correlates by its own seqs and simply has no in-flight match for these. */
let sampleSeq = -1;

let framesSeen = 0;
let decodes = 0;
let lastDecodeMs = 0;
let lastText = "";
let lastResult: ({ seq: number } & DecodeHit) | null = null;
/** ABNORMAL errors only (malformed payloads, unexpected throws). A blank
 *  frame's NotFoundException is the normal no-code case and never lands here
 *  — otherwise lastError() would be set after every ordinary frame. */
let lastError: string | null = null;
let lastStatsAt = 0;

/** Narrow an untrusted postMessage params object to a string-keyed record. */
function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

/** The enum's own reverse map ("QR_CODE" -> 11), typed for name lookups. */
const formatByName = BarcodeFormat as unknown as Record<string, number>;

/** Map config format names to enum values, dropping unknowns. */
/** The configured format NAMES (zxing vocabulary), for the native detector's
 *  constructor — parseFormats gives zxing its numeric enum values instead. */
let formatNames: string[] = [];
function activeFormats(): string[] {
  return formatNames.length > 0 ? formatNames : DEFAULT_FORMATS.slice();
}

function parseFormats(raw: unknown): number[] {
  const names = Array.isArray(raw) ? raw.filter((f): f is string => typeof f === "string") : [];
  const known = names.filter((name) => typeof formatByName[name] === "number");
  const values = known.map((name) => formatByName[name]);
  if (values.length > 0) {
    formatNames = known;
    return values;
  }
  return DEFAULT_FORMATS.map((name) => formatByName[name]);
}

/**
 * The continuous-scan reader: hints set once (at init, or lazily with the
 * default format before init), decodeWithState per frame, reset after each
 * attempt — zxing's documented reuse pattern for stream scanning.
 */
function getReader(): MultiFormatReader {
  if (!reader) {
    reader = new MultiFormatReader();
    const hints = new Map<DecodeHintType, unknown>();
    hints.set(DecodeHintType.POSSIBLE_FORMATS, parseFormats(DEFAULT_FORMATS));
    reader.setHints(hints);
  }
  return reader;
}

// --- rendering ----------------------------------------------------------------

/**
 * Paint a grayscale buffer — the exact bytes that are about to be decoded —
 * onto the visible canvas, one byte per pixel. The visitor sees what the
 * decoder sees; grayscale in, so it is painted grayscale, never pretended
 * into colour. Setting the bitmap size only when it changes keeps putImageData
 * the sole per-frame write (a same-value width assignment clears the canvas).
 */
function paintGray(gray: Uint8ClampedArray, w: number, h: number): void {
  if (!canvas || !ctx) return;
  if (canvas.width !== w || canvas.height !== h) {
    canvas.width = w;
    canvas.height = h;
  }
  const image = ctx.createImageData(w, h);
  const rgba = image.data;
  for (let i = 0, o = 0; i < w * h; i += 1, o += 4) {
    const g = gray[i];
    rgba[o] = g;
    rgba[o + 1] = g;
    rgba[o + 2] = g;
    rgba[o + 3] = 255;
  }
  ctx.putImageData(image, 0, 0);
}

function showResult(hit: DecodeHit): void {
  if (formatEl) {
    formatEl.textContent = hit.format;
    formatEl.hidden = false;
  }
  if (textEl) textEl.textContent = hit.text;
  if (root) root.classList.add("decoded");
}

function updateStatus(): void {
  if (!statusEl) return;
  const parts = [`${framesSeen.toLocaleString("en-US")} frames`, `${decodes.toLocaleString("en-US")} decodes`];
  if (lastDecodeMs > 0) parts.push(`last ${Math.round(lastDecodeMs)} ms`);
  statusEl.textContent = parts.join(" · ");
}

// --- decoding -----------------------------------------------------------------

/**
 * Decode one grayscale frame. `gray` is LUMINANCE, exactly width*height bytes
 * — NOT RGBA: RGBLuminanceSource only converts when handed an Int32Array
 * (4 bytes/px); a Uint8ClampedArray passes through as per-pixel luma, so an
 * RGBA buffer instead silently becomes 4 garbage pixels per real one and the
 * reader throws NotFoundException — a lookup failure that reads like "bad
 * image", not "bad call". Measured; do not "fix" the call to take RGBA.
 *
 * Every zxing throw here (NotFound/Checksum/Format) means "no decodable code
 * in this frame" — the normal case at stream rate — so all of them return
 * null. Nothing logs: a per-blank-frame console error would fail the e2e
 * suite's zero-console-errors assertion.
 */
/** Count of zxing console.warn lines suppressed during decoding, and the last
 *  one. Nothing is hidden — it moves from the console to __scanDebug. */
let zxingWarnings = 0;
let lastZxingWarning = "";

/**
 * zxing's MultiFormatReader logs "non-ReaderException from reader: …" through
 * console.warn whenever one of its sub-readers throws something outside its own
 * exception hierarchy. Its own encoder's output triggers it, so it fires on
 * SUCCESSFUL decodes too — at the host's 8 frames a second that is a console
 * flood in a user's browser, for a condition zxing itself catches and recovers
 * from.
 *
 * The warning is captured rather than discarded: it lands in __scanDebug where
 * a developer can find it, and console.warn is restored immediately, so nothing
 * else logging during the decode is affected.
 */
function decodeZxing(gray: Uint8ClampedArray, w: number, h: number): DecodeHit | null {
  const r = getReader();
  const t0 = performance.now();
  const realWarn = console.warn;
  console.warn = (...args: unknown[]): void => {
    zxingWarnings += 1;
    lastZxingWarning = args.map((a) => String(a)).join(" ").slice(0, 200);
  };
  try {
    const source = new RGBLuminanceSource(gray, w, h);
    const bitmap = new BinaryBitmap(new HybridBinarizer(source));
    const result: Result = r.decodeWithState(bitmap);
    return {
      text: result.getText(),
      format: String(BarcodeFormat[result.getBarcodeFormat()]),
      decodeMs: performance.now() - t0,
      via: "zxing",
    };
  } catch {
    return null;
  } finally {
    console.warn = realWarn;
    r.reset();
  }
}

/** The platform decoder, reading the canvas we just painted — no second pixel
 *  conversion, since BarcodeDetector takes a canvas directly. Its "no code
 *  here" is an empty array rather than a throw; a REJECTION means the detector
 *  itself is unusable, so it is dropped and the fallback takes over for good. */
async function decodeNative(): Promise<DecodeHit | null> {
  const det = getNativeDetector();
  if (!det || !canvas) return null;
  const t0 = performance.now();
  try {
    const codes = await det.detect(canvas);
    if (codes.length === 0) return null;
    return {
      text: codes[0].rawValue,
      format: fromNativeFormat(codes[0].format),
      decodeMs: performance.now() - t0,
      via: "native",
    };
  } catch {
    nativeDetector = null;
    return null;
  }
}

/**
 * Decode one painted frame, native first.
 *
 * The order is a correctness decision, not a performance one: zxing's JS port
 * cannot read some valid QR codes its own encoder produces (#19). Native is
 * tried first where the engine has it and zxing catches everything else —
 * Safari, Firefox, and Chromium on Linux have no BarcodeDetector at all.
 *
 * `decoderPref` forces one path or the other for the e2e, which exercises BOTH
 * on every engine rather than whichever one the runner happens to provide.
 */
async function decodeFrame(gray: Uint8ClampedArray, w: number, h: number): Promise<DecodeHit | null> {
  if (decoderPref !== "zxing") {
    const hit = await decodeNative();
    if (hit) return hit;
    // Forced native must not silently fall through to the other decoder: the
    // test asking for native wants to know native failed.
    if (decoderPref === "native") return null;
  }
  return decodeZxing(gray, w, h);
}

/** Rec.601 luma from a canvas's RGBA pixels — the sample path's stand-in for
 *  the host's camera, which delivers gray directly. */
function rgbaToGray(rgba: Uint8ClampedArray, w: number, h: number): Uint8ClampedArray {
  const gray = new Uint8ClampedArray(w * h);
  for (let i = 0, o = 0; i < gray.length; i += 1, o += 4) {
    gray[i] = (rgba[o] * 299 + rgba[o + 1] * 587 + rgba[o + 2] * 114) / 1000;
  }
  return gray;
}

/**
 * The full life of one frame, host-pushed or frame-generated: count it, paint
 * it, decode it, ack it. scanResult rides only on success; frameDone ALWAYS
 * follows — it is the host's one-frame-in-flight release.
 */
function processFrame(seq: number, gray: Uint8ClampedArray, w: number, h: number): void {
  framesSeen += 1;
  paintGray(gray, w, h);
  // The native decoder is async, so the whole pipeline is. Frames are chained
  // rather than run concurrently: the host already sends one at a time, but a
  // scanSample can land mid-flight, and two decodes racing would interleave
  // their frameDone acks and free the host's slot early.
  decodeChain = decodeChain
    .then(() => decodeFrame(gray, w, h))
    .then((hit) => {
      if (hit) {
        decodes += 1;
        lastDecodeMs = hit.decodeMs;
        lastText = hit.text;
        lastResult = { seq, ...hit };
        showResult(hit);
        sendEvent("scanResult", { seq, text: hit.text, format: hit.format, decodeMs: hit.decodeMs, via: hit.via });
      }
      updateStatus();
      sendEvent("frameDone", { seq, decoded: hit !== null });
      maybeSendStats(hit !== null);
    })
    .catch((err: unknown) => {
      // The chain must never break: a rejection here would wedge the host's
      // one-frame window forever. Record it, ack the frame, carry on.
      lastError = `decode: ${String(err)}`;
      sendEvent("frameDone", { seq, decoded: false });
    });
}

/** Serializes decodes; see processFrame. */
let decodeChain: Promise<void> = Promise.resolve();

/** scanStats: host mirrors, throttled to at most one per 500 ms. lastText is
 *  sent whole; the HOST caps its mirror at 200 chars — the frame does not get
 *  to decide what is too long for someone else's page. */
function maybeSendStats(force: boolean): void {
  const now = performance.now();
  // The throttle is for the STREAM: at 8 frames a second a mirror update per
  // frame is noise. A decode is not stream traffic, and neither is a one-shot
  // scan — throttling those means a single file scan reports nothing at all,
  // because it finishes well inside the first interval. Force those through.
  if (!force && now - lastStatsAt < STATS_MIN_INTERVAL_MS) return;
  lastStatsAt = now;
  sendEvent("scanStats", { framesSeen, decodes, lastDecodeMs, lastText });
}

// --- host → plugin handlers -----------------------------------------------------

/**
 * One host-pushed camera frame. Malformed payloads still ack (see the file
 * header) but count as neither seen nor decoded; the reason lands in
 * __scanDebug.lastError, not the console.
 */
function handleScanFrame(params: unknown): void {
  const p = asRecord(params);
  const seq = p.seq;
  const w = p.width;
  const h = p.height;
  const gray = p.gray;
  const reject = (why: string): void => {
    lastError = why;
    sendEvent("frameDone", { seq: typeof seq === "number" ? seq : -1, decoded: false });
  };
  if (typeof seq !== "number" || !Number.isFinite(seq)) {
    reject(`scanFrame: bad seq (${String(seq)})`);
    return;
  }
  if (
    typeof w !== "number" || !Number.isInteger(w) || w <= 0 ||
    typeof h !== "number" || !Number.isInteger(h) || h <= 0
  ) {
    reject(`scanFrame: bad dimensions (${w}x${h})`);
    return;
  }
  // A byte-per-pixel view (Uint8ClampedArray or Uint8Array) holding at least
  // w*h luminance bytes; RGBLuminanceSource takes the first w*h as-is.
  const bytes = gray instanceof Uint8ClampedArray || gray instanceof Uint8Array ? gray : null;
  if (!bytes || bytes.length < w * h) {
    reject(`scanFrame: gray buffer must hold >= ${w * h} bytes, got ${bytes ? bytes.length : typeof gray}`);
    return;
  }
  processFrame(seq, new Uint8ClampedArray(bytes.buffer, bytes.byteOffset, w * h), w, h);
}

/**
 * The no-camera demo path: encode a QR inside the cage, DRAW it on the canvas
 * (the visitor watches the frame generate the very code it will read), read
 * the pixels back, and push them through the same gray pipeline a camera
 * frame arrives on. The writer's BitMatrix already carries its quiet zone and
 * scales modules to the requested size; the trailing `new Map()` of hints is
 * REQUIRED (the signature takes it unconditionally — omitting it throws
 * "Cannot read properties of undefined (reading 'get')" inside QRCodeWriter).
 */
function handleScanSample(): void {
  if (!canvas || !ctx) return;
  try {
    const matrix: BitMatrix = new MultiFormatWriter().encode(
      SAMPLE_TEXT,
      BarcodeFormat.QR_CODE,
      SAMPLE_QR_SIZE,
      SAMPLE_QR_SIZE,
      new Map()
    );
    const w = matrix.getWidth();
    const h = matrix.getHeight();
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w;
      canvas.height = h;
    }
    const image = ctx.createImageData(w, h);
    const px = image.data;
    for (let o = 0; o < px.length; o += 4) {
      px[o] = 0;
      px[o + 1] = 0;
      px[o + 2] = 0;
      px[o + 3] = 255;
    }
    for (let y = 0; y < h; y += 1) {
      for (let x = 0; x < w; x += 1) {
        if (matrix.get(x, y)) continue; // black module
        const o = (y * w + x) * 4; // else white
        px[o] = 255;
        px[o + 1] = 255;
        px[o + 2] = 255;
      }
    }
    ctx.putImageData(image, 0, 0);
    const shot = ctx.getImageData(0, 0, w, h);
    const seq = sampleSeq;
    sampleSeq -= 1;
    processFrame(seq, rgbaToGray(shot.data, w, h), w, h);
  } catch (err) {
    // Unreachable in practice (fixed payload); still ack-shaped so a host
    // waiting on this sample cannot wedge, and visible in __scanDebug.
    const why = err instanceof Error ? `${err.name}: ${err.message}` : String(err);
    lastError = `scanSample: ${why}`;
    updateStatus();
  }
}

function handleInit(params: unknown): void {
  if (initialized) return;
  initialized = true;
  const p = asRecord(params);
  applyTokens(p.tokens);
  applyScheme(typeof p.scheme === "string" ? p.scheme : "light");
  const config = asRecord(p.config);
  // scanRateHz is the HOST's capture cadence, published here for completeness;
  // the frame is rate-agnostic — it decodes whatever arrives and acks it.
  const hints = new Map<DecodeHintType, unknown>();
  hints.set(DecodeHintType.POSSIBLE_FORMATS, parseFormats(config.formats));
  reader = new MultiFormatReader();
  reader.setHints(hints);
  sendEvent("themeApplied", { scheme: p.scheme, sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS) });
  updateStatus();
}

function handleThemeChanged(params: unknown): void {
  const p = asRecord(params);
  applyTokens(p.tokens);
  applyScheme(p.scheme);
  sendEvent("themeApplied", { scheme: p.scheme, sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS) });
}

// teardown is a REQUEST → return {} after a clean teardown (no leaked
// listeners, nothing left pending).
function handleTeardown(): Record<string, never> {
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
  reader = null;
  rejectAllPending({ code: "E_TEARDOWN", message: "frame torn down" });
  return {};
}

// ---------------------------------------------------------------------------
// Lifecycle

// Self-isolation probes, computed INSIDE the opaque frame at boot. Under
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
    minHeight: 460,
    probes: isolationProbes(),
    // Which decoders this ENGINE has. The host mirrors it so a page (and the
    // e2e) can tell "this build has no native decoder" from "native failed".
    decoders: { native: nativeAvailable(), zxing: true },
  });
}

/**
 * In-frame debug hooks (the logstream/whiteboard pattern): the e2e suite
 * reads these through the frame's own window. lastError stays null through
 * ordinary no-code frames — see its declaration for why.
 */
/** Force a decoder path. Anything unrecognised resets to the shipping
 *  behaviour rather than wedging the frame in a state a caller cannot name. */
function handleSetDecoder(params: unknown): void {
  const which = asRecord(params).which;
  decoderPref = which === "native" || which === "zxing" ? which : "auto";
  updateStatus();
  sendEvent("decoderChanged", { decoder: decoderPref, decoders: { native: nativeAvailable(), zxing: true } });
}

function publishDebug(): void {
  (window as unknown as Record<string, unknown>).__scanDebug = {
    framesSeen: (): number => framesSeen,
    decodes: (): number => decodes,
    lastResult: (): ({ seq: number } & DecodeHit) | null => (lastResult ? { ...lastResult } : null),
    lastError: (): string | null => lastError,
    decoders: (): { native: boolean; zxing: boolean } => ({ native: nativeAvailable(), zxing: true }),
    decoder: (): string => decoderPref,
    zxingWarnings: (): { count: number; last: string } => ({ count: zxingWarnings, last: lastZxingWarning }),
  };
}

function boot(): void {
  root = document.getElementById("scanner-root");
  canvas = document.getElementById("scan-canvas") as HTMLCanvasElement | null;
  formatEl = document.getElementById("scan-format");
  textEl = document.getElementById("scan-text");
  statusEl = document.getElementById("scan-status");
  if (!root || !canvas) return;
  ctx = canvas.getContext("2d", { willReadFrequently: true });

  publishDebug();
  messageListener = createRouter({
    init: handleInit,
    themeChanged: handleThemeChanged,
    teardown: handleTeardown,
    // The push channel: the host captured these pixels and sends them
    // unasked. Every other platform event needs no action here.
    scanFrame: handleScanFrame,
    // The demo path: no camera, no image assets — the cage mints its own QR.
    scanSample: handleScanSample,
    // Test seam, not a feature: force one decoder so the e2e exercises BOTH
    // paths on every engine instead of whichever the runner happens to have.
    setDecoder: handleSetDecoder,
  });
  window.addEventListener("message", messageListener);
  updateStatus();
  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
