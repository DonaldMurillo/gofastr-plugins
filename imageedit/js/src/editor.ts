// GoFastr image editor — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage or
// DOM, and never fetch (the framed CSP sets connect-src 'none'). On load it
// announces `ready`; the host replies `init` carrying the operation-list doc,
// the theme tokens and the granted capabilities. The frame then asks for the
// image BYTES over the bridge (requestImage), decodes them with
// createImageBitmap — no CSP-covered fetch involved — and previews the doc
// by composing it with the same integer pipeline the Go server will run at
// export (render.ts is render.go's twin).
//
// The doc — never pixels — is the editor's whole output: every edit mutates
// the operation list, mirrors it to the host (docChanged) and the hidden
// form field, and autosaves it. The Export button sends ONLY the doc; the
// server re-renders, verifies the redactions and returns a URL.

import { createRouter, rejectAllPending, sendEvent } from "./protocol";
import {
  handleBridgeResult,
  rejectAllPending as rejectBridge,
  requestExport,
  requestImage,
  requestUpload,
} from "./bridge";
import type { ProtocolError } from "./protocol";
import { Raster, compose, displayToSource, effectiveCrop } from "./render";
import type { Annotation, Doc, Redaction, Rect } from "./render";
import { applyScheme, applyTokens, sampleAppliedTokens } from "./theme";

const SCHEMA_VERSION = "imageedit-v1";
const DOC_CHANGED_DEBOUNCE_MS = 400;
const AUTOSAVE_DEBOUNCE_MS = 1500;
const PREVIEW_DEBOUNCE_MS = 500;
const UNDO_LIMIT = 50;
const SAMPLE_TOKENS = ["--color-surface", "--color-text", "--color-border"];

/** Content palette for annotations/redactions — drawn INTO the image, so
 *  these are content colors chosen for contrast against arbitrary photos,
 *  not theme tokens (a dark-theme-only annotation would vanish on a photo). */
const PALETTE = ["#D0342C", "#F5A623", "#2F9E44", "#1C7ED6", "#111111", "#FFFFFF"];
const REDACTION_FILL = "#000000";

type Tool = "select" | "crop" | "rect" | "arrow" | "text" | "redact";

// --- runtime state (module-scoped; single instance per frame) ---------------

let canvas: HTMLCanvasElement | null = null;
let ctx: CanvasRenderingContext2D | null = null;
let statusEl: HTMLElement | null = null;
let textInput: HTMLInputElement | null = null;
let fileInput: HTMLInputElement | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

let srcRaster: Raster | null = null;
let srcDigest = ""; // sha256 hex of the source bytes, when computable
let doc: Doc = emptyDoc("demo");
let initialized = false;
let canWrite = false;
let canUpload = false;
let tool: Tool = "select";
let color = PALETTE[0];
let strokeWidth = 4;
let glyphScale = 4;
let dragFrom: [number, number] | null = null; // output-space drag origin
let dragCurrent: [number, number] | null = null;
let baseImageData: ImageData | null = null; // compose(doc) at drag start
let idCounter = 0;
const undoStack: string[] = [];

let docChangedTimer: number | undefined;
let autosaveTimer: number | undefined;
let previewTimer: number | undefined;

function emptyDoc(ref: string): Doc {
  return {
    schemaVersion: SCHEMA_VERSION,
    src: { kind: "id", ref },
    rotate: 0,
    annotations: [],
    redactions: [],
    rev: 0,
  };
}

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

function setStatus(text: string): void {
  if (statusEl) statusEl.textContent = text;
}

function nextId(prefix: string): string {
  idCounter += 1;
  return `${prefix}${idCounter}`;
}

function hasCap(list: string[], name: string): boolean {
  return list.includes(name);
}

// --- doc plumbing -------------------------------------------------------------

/** The doc as it crosses the bridge: every field Go's Doc expects, with the
 *  optional crop omitted when absent (Go's *Rect + omitempty contract). */
function currentDoc(): Record<string, unknown> {
  const out: Record<string, unknown> = {
    schemaVersion: SCHEMA_VERSION,
    src: doc.src,
    rotate: doc.rotate,
    annotations: doc.annotations,
    redactions: doc.redactions,
    rev: doc.rev,
  };
  if (doc.crop) out.crop = doc.crop;
  return out;
}

function scheduleDocSync(): void {
  window.clearTimeout(docChangedTimer);
  docChangedTimer = window.setTimeout(() => {
    sendEvent("docChanged", { doc: currentDoc(), dirty: true, rev: doc.rev });
    if (!canWrite) return;
    window.clearTimeout(autosaveTimer);
    autosaveTimer = window.setTimeout(() => {
      sendEvent("save", { doc: currentDoc(), schemaVersion: SCHEMA_VERSION });
    }, AUTOSAVE_DEBOUNCE_MS);
  }, DOC_CHANGED_DEBOUNCE_MS);
}

function pushUndo(): void {
  undoStack.push(JSON.stringify(currentDoc()));
  if (undoStack.length > UNDO_LIMIT) undoStack.shift();
}

function mutate(fn: () => void): void {
  pushUndo();
  fn();
  doc.rev = (doc.rev ?? 0) + 1;
  scheduleDocSync();
  redraw();
}

function undo(): void {
  const snapshot = undoStack.pop();
  if (!snapshot) {
    setStatus("Nothing to undo");
    return;
  }
  doc = parseDoc(JSON.parse(snapshot));
  scheduleDocSync();
  redraw();
}

function resetDoc(): void {
  mutate(() => {
    doc.crop = null;
    doc.rotate = 0;
    doc.annotations = [];
    doc.redactions = [];
  });
  setStatus("Cleared all operations");
}

/** Narrow an untrusted init.doc payload to the live doc shape. Unknown
 *  fields are dropped on the floor; bad values degrade to defaults rather
 *  than throwing — the SERVER re-validates everything at render anyway. */
function parseDoc(raw: unknown): Doc {
  const p = asRecord(raw);
  const src = asRecord(p.src);
  const d = emptyDoc(typeof src.ref === "string" && src.ref !== "" ? src.ref : "demo");
  if (typeof src.sha256 === "string") d.src.sha256 = src.sha256;
  if (p.crop && typeof p.crop === "object") {
    const c = asRecord(p.crop);
    d.crop = {
      x: num(c.x),
      y: num(c.y),
      w: num(c.w),
      h: num(c.h),
    };
  }
  const rot = num(p.rotate);
  if (rot === 90 || rot === 180 || rot === 270) d.rotate = rot;
  if (Array.isArray(p.annotations)) {
    d.annotations = p.annotations.slice(0, 64).map((a): Annotation => {
      const r = asRecord(a);
      return {
        id: typeof r.id === "string" ? r.id : nextId("a"),
        type: (r.type === "rect" || r.type === "arrow" || r.type === "text" ? r.type : "rect"),
        color: isColor(r.color) ? r.color : "#D0342C",
        width: clampInt(num(r.width) || 4, 1, 64),
        x: num(r.x),
        y: num(r.y),
        w: num(r.w),
        h: num(r.h),
        x2: num(r.x2),
        y2: num(r.y2),
        size: clampInt(num(r.size) || 4, 1, 32),
        text: typeof r.text === "string" ? r.text.slice(0, 64) : "",
      };
    });
  }
  if (Array.isArray(p.redactions)) {
    d.redactions = p.redactions.slice(0, 64).map((r): Redaction => {
      const q = asRecord(r);
      const rect = asRecord(q.rect);
      return {
        id: typeof q.id === "string" ? q.id : nextId("r"),
        rect: { x: num(rect.x), y: num(rect.y), w: num(rect.w), h: num(rect.h) },
        fill: isColor(q.fill) ? q.fill : REDACTION_FILL,
      };
    });
  }
  d.rev = num(p.rev);
  return d;
}

function num(v: unknown): number {
  const n = typeof v === "number" ? Math.round(v) : Number.NaN;
  return Number.isFinite(n) ? n : 0;
}

function clampInt(v: number, lo: number, hi: number): number {
  return v < lo ? lo : v > hi ? hi : v;
}

function isColor(v: unknown): v is string {
  return typeof v === "string" && /^#[0-9a-fA-F]{6}$/.test(v);
}

// --- image loading -----------------------------------------------------------

async function loadImage(): Promise<void> {
  setStatus("Requesting image over the bridge…");
  // The ref the DOC names — not the mount's docId: after an upload + reload
  // the doc points at the uploaded image, and that is what must load.
  const res = await requestImage(doc.src.ref);
  srcDigest = await digestHex(res.bytes);
  const bitmap = await createImageBitmap(new Blob([res.bytes], { type: res.mime }));
  const c = document.createElement("canvas");
  c.width = bitmap.width;
  c.height = bitmap.height;
  const bctx = c.getContext("2d", { willReadFrequently: true });
  if (!bctx) throw new Error("no 2d context for decode");
  bctx.drawImage(bitmap, 0, 0);
  bitmap.close();
  srcRaster = Raster.fromImageData(bctx.getImageData(0, 0, c.width, c.height));
  undoStack.length = 0; // undo must not resurrect ops for the previous image
  setStatus(`Image ${c.width}×${c.height} resident — preview is local, export is server-side`);
  redraw();
}

/** sha256 of the source bytes, when crypto.subtle exists (secure context).
 *  Over plain http to a non-localhost host it is unavailable: the digest is
 *  omitted and Go treats the empty string as "no binding" (documented in
 *  docs/imageedit.md). */
async function digestHex(bytes: ArrayBuffer): Promise<string> {
  if (!crypto || !crypto.subtle) return "";
  try {
    const digest = await crypto.subtle.digest("SHA-256", bytes);
    return Array.from(new Uint8Array(digest))
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
  } catch {
    return "";
  }
}

// --- rendering -----------------------------------------------------------------

/** Full compose + display + (debounced) preview event. THE preview is the
 *  1:1 composed raster — the thing the server must agree with. */
function redraw(): void {
  if (!srcRaster || !canvas || !ctx) return;
  let result;
  try {
    result = compose(srcRaster, doc);
  } catch (err) {
    setStatus(`Preview error: ${String(err)}`);
    return;
  }
  if (canvas.width !== result.width || canvas.height !== result.height) {
    canvas.width = result.width;
    canvas.height = result.height;
    sendEvent("resize", { height: canvas.height + 96 });
  }
  const imageData = result.out.toImageData();
  ctx.putImageData(imageData, 0, 0);
  baseImageData = imageData;
  schedulePreview();
}

function schedulePreview(): void {
  window.clearTimeout(previewTimer);
  previewTimer = window.setTimeout(sendPreview, PREVIEW_DEBOUNCE_MS);
}

/** Publish the frame's own 1:1 render as a PNG data URL so the host page
 *  (which cannot reach into the opaque frame) can set it beside the
 *  server-rendered export — the live, visible preview-vs-server agreement. */
function sendPreview(): void {
  if (!canvas) return;
  try {
    sendEvent("previewRender", {
      dataUrl: canvas.toDataURL("image/png"),
      width: canvas.width,
      height: canvas.height,
    });
  } catch {
    // toDataURL failing costs the demo its readout, not the editor its doc.
  }
}

// --- pointer interactions ---------------------------------------------------

function canvasPoint(ev: PointerEvent): [number, number] | null {
  if (!canvas) return null;
  const rect = canvas.getBoundingClientRect();
  if (rect.width === 0 || rect.height === 0) return null;
  // CSS-scaled display px → output px.
  const outX = Math.round(((ev.clientX - rect.left) * canvas.width) / rect.width);
  const outY = Math.round(((ev.clientY - rect.top) * canvas.height) / rect.height);
  if (outX < 0 || outY < 0 || outX >= canvas.width || outY >= canvas.height) return null;
  return [outX, outY];
}

function sourcePoint(ev: PointerEvent): [number, number] | null {
  const out = canvasPoint(ev);
  if (!out || !srcRaster) return null;
  const crop = effectiveCrop(srcRaster, doc.crop);
  return displayToSource(out[0], out[1], crop, doc.rotate);
}

function onPointerDown(ev: PointerEvent): void {
  if (!srcRaster || tool === "select") return;
  if (tool === "text") {
    placeText(ev);
    return;
  }
  const out = canvasPoint(ev);
  if (!out) return;
  dragFrom = out;
  dragCurrent = out;
  if (canvas) canvas.setPointerCapture(ev.pointerId);
}

function onPointerMove(ev: PointerEvent): void {
  if (!dragFrom || !canvas || !ctx || !baseImageData) return;
  const out = canvasPoint(ev);
  if (!out) return;
  dragCurrent = out;
  // Ghost preview: base frame + the in-progress shape. The ghost may use
  // canvas AA freely — it is transient; the committed op re-renders through
  // the integer pipeline on pointerup.
  ctx.putImageData(baseImageData, 0, 0);
  drawGhost(dragFrom, out);
}

function onPointerUp(ev: PointerEvent): void {
  if (!dragFrom || !dragCurrent || !srcRaster) {
    dragFrom = null;
    dragCurrent = null;
    return;
  }
  const from = dragFrom;
  const to = dragCurrent;
  dragFrom = null;
  dragCurrent = null;
  const dist = Math.abs(to[0] - from[0]) + Math.abs(to[1] - from[1]);
  if (dist < 3) {
    redraw(); // a tap is not a shape
    return;
  }
  const crop = effectiveCrop(srcRaster, doc.crop);
  const s1 = displayToSource(from[0], from[1], crop, doc.rotate);
  const s2 = displayToSource(to[0], to[1], crop, doc.rotate);
  const rect = normalizeRect(s1, s2);
  mutate(() => {
    if (tool === "crop") {
      applyCrop(rect);
    } else if (tool === "redact") {
      doc.redactions.push({ id: nextId("r"), rect, fill: REDACTION_FILL });
    } else if (tool === "arrow") {
      doc.annotations.push({
        id: nextId("a"),
        type: "arrow",
        color,
        width: strokeWidth,
        x: s1[0],
        y: s1[1],
        w: 0,
        h: 0,
        x2: s2[0],
        y2: s2[1],
        size: 0,
        text: "",
      });
    } else {
      doc.annotations.push({
        id: nextId("a"),
        type: "rect",
        color,
        width: strokeWidth,
        x: rect.x,
        y: rect.y,
        w: rect.w,
        h: rect.h,
        x2: 0,
        y2: 0,
        size: 0,
        text: "",
      });
    }
  });
}

function normalizeRect(a: [number, number], b: [number, number]): Rect {
  return {
    x: Math.min(a[0], b[0]),
    y: Math.min(a[1], b[1]),
    w: Math.abs(b[0] - a[0]) + 1,
    h: Math.abs(b[1] - a[1]) + 1,
  };
}

/** Crop is stored in SOURCE coordinates; the drag happened on the already
 *  cropped view, so the new rect maps back inside the old one — intersect
 *  rather than replace, and drop it if it degenerates. */
function applyCrop(rect: Rect): void {
  if (!doc.crop) {
    if (rect.w >= 8 && rect.h >= 8) doc.crop = rect;
    return;
  }
  const x0 = Math.max(doc.crop.x, rect.x);
  const y0 = Math.max(doc.crop.y, rect.y);
  const x1 = Math.min(doc.crop.x + doc.crop.w, rect.x + rect.w);
  const y1 = Math.min(doc.crop.y + doc.crop.h, rect.y + rect.h);
  if (x1 - x0 >= 8 && y1 - y0 >= 8) {
    doc.crop = { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
  }
}

function drawGhost(from: [number, number], to: [number, number]): void {
  if (!ctx) return;
  const x = Math.min(from[0], to[0]);
  const y = Math.min(from[1], to[1]);
  const w = Math.abs(to[0] - from[0]);
  const h = Math.abs(to[1] - from[1]);
  ctx.save();
  if (tool === "redact") {
    ctx.fillStyle = "rgba(0,0,0,0.55)";
    ctx.fillRect(x, y, w, h);
    ctx.strokeStyle = "#ffffff";
    ctx.lineWidth = 1;
    ctx.strokeRect(x, y, w, h);
  } else if (tool === "crop") {
    // Dim everything outside the candidate crop: four band fills, no
    // self-drawImage (a canvas drawn onto itself after the dim would copy
    // the dim).
    ctx.fillStyle = "rgba(0,0,0,0.45)";
    ctx.fillRect(0, 0, ctx.canvas.width, y);
    ctx.fillRect(0, y + h, ctx.canvas.width, ctx.canvas.height - y - h);
    ctx.fillRect(0, y, x, h);
    ctx.fillRect(x + w, y, ctx.canvas.width - x - w, h);
    ctx.strokeStyle = "#ffffff";
    ctx.setLineDash([6, 4]);
    ctx.lineWidth = 1;
    ctx.strokeRect(x, y, w, h);
  } else if (tool === "arrow") {
    ctx.strokeStyle = color;
    ctx.lineWidth = strokeWidth;
    ctx.lineCap = "round";
    ctx.beginPath();
    ctx.moveTo(from[0], from[1]);
    ctx.lineTo(to[0], to[1]);
    ctx.stroke();
  } else {
    ctx.strokeStyle = color;
    ctx.lineWidth = strokeWidth;
    ctx.strokeRect(x, y, w, h);
  }
  ctx.restore();
}

function placeText(ev: PointerEvent): void {
  const s = sourcePoint(ev);
  const text = textInput ? textInput.value.trim() : "";
  if (!s || text === "") {
    setStatus(text === "" ? "Type text in the box first" : "");
    return;
  }
  mutate(() => {
    doc.annotations.push({
      id: nextId("a"),
      type: "text",
      color,
      width: strokeWidth,
      x: s[0],
      y: s[1],
      w: 0,
      h: 0,
      x2: 0,
      y2: 0,
      size: glyphScale,
      text,
    });
  });
}

// --- toolbar actions -----------------------------------------------------------

function rotateBy(deg: number): void {
  mutate(() => {
    doc.rotate = (((doc.rotate + deg) % 360) + 360) % 360;
  });
}

function doExport(): void {
  setStatus("Exporting — the server re-renders the operation list…");
  requestExport(doc).then(
    (res) => {
      sendPreview(); // pair the freshest 1:1 render with this export
      setStatus(
        res.verify
          ? `Exported ${res.width}×${res.height} ${res.format} (${res.byteLength.toLocaleString("en-US")} B) — redactions verified`
          : `Export failed verification`
      );
    },
    (err: ProtocolError) => {
      console.error("[imageedit] export failed:", err);
      setStatus(`Export failed: ${err.code ?? "error"}`);
    }
  );
}

async function onFileChosen(ev: Event): Promise<void> {
  const input = ev.target as HTMLInputElement;
  const file = input.files && input.files[0];
  if (!file) return;
  try {
    const bytes = await file.arrayBuffer();
    setStatus("Uploading over the bridge…");
    const res = await requestUpload(file.name, file.type || "image/png", bytes);
    mutate(() => {
      doc = emptyDoc(res.id);
      undoStack.length = 0;
    });
    await loadImage();
  } catch (err) {
    const e = err as ProtocolError;
    console.error("[imageedit] upload failed:", err);
    setStatus(`Upload failed: ${e.code ?? String(err)}`);
  } finally {
    input.value = "";
  }
}

function selectTool(next: Tool): void {
  tool = next;
  document.querySelectorAll<HTMLElement>(".ie-tool[data-tool]").forEach((b) => {
    const active = b.dataset.tool === tool;
    b.classList.toggle("is-active", active);
    b.setAttribute("aria-pressed", active ? "true" : "false");
  });
  if (canvas) canvas.style.cursor = tool === "select" ? "default" : "crosshair";
}

// --- host → plugin handlers ------------------------------------------------------

function handleInit(params: unknown): void {
  if (initialized) return;
  initialized = true;
  const p = asRecord(params);
  const caps = Array.isArray(p.capabilities) ? p.capabilities.filter((c): c is string => typeof c === "string") : [];
  canWrite = hasCap(caps, "document:write");
  canUpload = hasCap(caps, "upload:images");
  applyTokens(p.tokens);
  applyScheme(typeof p.scheme === "string" ? p.scheme : "light");
  doc = parseDoc(p.doc);
  if (fileInput) fileInput.disabled = !canUpload;
  const loadBtn = document.getElementById("ie-load");
  if (loadBtn) loadBtn.classList.toggle("ie-hidden", !canUpload);
  sendEvent("themeApplied", {
    scheme: typeof p.scheme === "string" ? p.scheme : "light",
    sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS),
  });
  loadImage().then(
    () => sendEvent("imageLoaded", { ref: doc.src.ref, digest: srcDigest !== "" }),
    (err: unknown) => {
      console.error("[imageedit] image load failed:", err);
      setStatus(`Image load failed: ${String(err)}`);
    }
  );
}

function handleThemeChanged(params: unknown): void {
  const p = asRecord(params);
  applyTokens(p.tokens);
  applyScheme(typeof p.scheme === "string" ? p.scheme : "light");
}

function handleRequestSave(): Record<string, unknown> {
  return { doc: currentDoc(), schemaVersion: SCHEMA_VERSION };
}

function handleTeardown(): Record<string, never> {
  teardown();
  return {};
}

// --- lifecycle -----------------------------------------------------------------

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
    minHeight: 520,
    probes: isolationProbes(),
  });
}

function teardown(): void {
  window.clearTimeout(docChangedTimer);
  window.clearTimeout(autosaveTimer);
  window.clearTimeout(previewTimer);
  rejectAllPending({ code: "E_TEARDOWN", message: "frame torn down" });
  rejectBridge({ code: "E_TEARDOWN", message: "frame torn down" });
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
}

function bindToolbar(): void {
  document.querySelectorAll<HTMLElement>(".ie-tool[data-tool]").forEach((btn) => {
    btn.addEventListener("click", () => selectTool((btn.dataset.tool as Tool) ?? "select"));
  });
  document.getElementById("ie-rotate-l")?.addEventListener("click", () => rotateBy(-90));
  document.getElementById("ie-rotate-r")?.addEventListener("click", () => rotateBy(90));
  document.getElementById("ie-undo")?.addEventListener("click", undo);
  document.getElementById("ie-reset")?.addEventListener("click", resetDoc);
  document.getElementById("ie-export")?.addEventListener("click", doExport);
  document.getElementById("ie-load")?.addEventListener("click", () => fileInput?.click());
  document.querySelectorAll<HTMLElement>(".ie-swatch").forEach((sw) => {
    sw.addEventListener("click", () => {
      const c = sw.dataset.color;
      if (!c) return;
      color = c;
      document.querySelectorAll<HTMLElement>(".ie-swatch").forEach((s) => {
        s.classList.toggle("is-active", s.dataset.color === color);
      });
    });
  });
  const widthSel = document.getElementById("ie-width") as HTMLSelectElement | null;
  widthSel?.addEventListener("change", () => {
    strokeWidth = clampInt(Number(widthSel.value) || 4, 1, 64);
  });
  const sizeSel = document.getElementById("ie-size") as HTMLSelectElement | null;
  sizeSel?.addEventListener("change", () => {
    glyphScale = clampInt(Number(sizeSel.value) || 4, 1, 32);
  });
  fileInput?.addEventListener("change", (ev) => void onFileChosen(ev));
}

function boot(): void {
  canvas = document.getElementById("ie-canvas") as HTMLCanvasElement | null;
  ctx = canvas ? canvas.getContext("2d", { willReadFrequently: true }) : null;
  statusEl = document.getElementById("ie-status");
  textInput = document.getElementById("ie-text") as HTMLInputElement | null;
  fileInput = document.getElementById("ie-file") as HTMLInputElement | null;

  bindToolbar();
  selectTool("select");

  if (canvas) {
    canvas.addEventListener("pointerdown", onPointerDown);
    canvas.addEventListener("pointermove", onPointerMove);
    canvas.addEventListener("pointerup", onPointerUp);
    canvas.addEventListener("pointercancel", () => {
      dragFrom = null;
      dragCurrent = null;
      redraw();
    });
  }

  messageListener = createRouter({
    init: handleInit,
    themeChanged: handleThemeChanged,
    requestSave: handleRequestSave,
    teardown: handleTeardown,
    // Result events for the frame→host bridge round trips. Everything else
    // (resize / focusChanged / hostPointerdown / bootError) needs no action.
    imageResult: (params: unknown) => handleBridgeResult("imageResult", params),
    uploadResult: (params: unknown) => handleBridgeResult("uploadResult", params),
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
