// GoFastr whiteboard — the in-frame board controller.
//
// The document is a Yjs CRDT: strokes live in a Y.Map keyed by stroke id, so
// every edit is an order-insensitive binary update blob. That property is the
// whole reason this plugin can exist: the blobs are OPAQUE to the host, cross
// the postMessage bridge as ArrayBuffers, and the host relays them to the
// other participants over its own connection. The frame never opens a socket
// — its CSP forbids one (connect-src 'none') — and loses nothing by it.
//
// Presence (remote cursors) is NOT part of the CRDT: it is ephemeral, so it
// rides its own bridge events and expires. Identity comes FROM THE HOST as an
// opaque participant id plus a colour — never a name. The frame does not know
// who it is collaborating with, only how many and in what colour, which is an
// isolation property, not a UX accident (docs/whiteboard.md).

import * as Y from "yjs";
import { sendEvent } from "./protocol";

export const SCHEMA_VERSION = "whiteboard-v1";

/** Coordinate precision: normalized [0..1] coords quantized to 1/1000 of the
 *  canvas, so update blobs stay small and renders are size-independent. */
const COORD_SCALE = 1000;
const ERASE_HIT_RADIUS = 0.012; // normalized distance; ~12px on a 1000px board
const PRESENCE_THROTTLE_MS = 110;
const PRESENCE_TTL_MS = 5000;
const STROKE_SIZES: Record<string, number> = { s: 2.5, m: 5, l: 10 };

/** One committed stroke as it lives in the CRDT. Points are [x,y] pairs in
 *  normalized [0..1] board coordinates (x right, y down). */
export interface Stroke {
  id: string;
  color: string;
  size: number;
  points: number[];
}

interface RemoteCursor {
  pid: string;
  color: string;
  x: number;
  y: number;
  down: boolean;
  el: HTMLElement;
  expiresAt: number;
}

const REMOTE_ORIGIN = "remote"; // Yjs transaction origin for host-delivered updates

// --- module state (single instance per frame) --------------------------------
let ydoc: Y.Doc | null = null;
let strokes: Y.Map<Stroke> | null = null;
let canvas: HTMLCanvasElement | null = null;
let ctx: CanvasRenderingContext2D | null = null;
let cursorsEl: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let swatchEl: HTMLElement | null = null;
let participantLabelEl: HTMLElement | null = null;

let canSync = false;
let connected = false;
let pid = "";
let color = "";
let participants = 0;
let tool: "pen" | "eraser" = "pen";
let strokeSize = STROKE_SIZES.m;
let drawing = false;
let erasing = false;
let currentPoints: number[] = [];
let redrawQueued = false;
let resizeObserver: ResizeObserver | null = null;
let lastPresenceSent = 0;
let presenceTimer = 0;
const remoteCursors = new Map<string, RemoteCursor>();

// Bytes/counters mirrored for the demo readout and the e2e suite.
const traffic = { updatesSent: 0, bytesSent: 0, updatesReceived: 0, bytesReceived: 0 };

/** Self-isolation probes (protocol §8a): computed at boot, sent on ready.
 *  In a sandboxed opaque-origin frame, reading cookie/storage THROWS rather
 *  than returning empty — a throw IS the pass condition, so each probe is
 *  try/caught and never allowed to break the ready handshake. */
export function computeProbes(): { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } {
  let cookieEmpty = true;
  try {
    cookieEmpty = document.cookie === "";
  } catch {
    cookieEmpty = true; // unreadable = isolated, which is what we assert
  }
  let parentBlocked = false;
  try {
    void (window.parent as Window & { document?: unknown }).document;
  } catch {
    parentBlocked = true;
  }
  let storageBlocked = false;
  try {
    void window.localStorage.length;
  } catch {
    storageBlocked = true;
  }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

// --- canvas ------------------------------------------------------------------

function quantize(v: number): number {
  const n = Math.min(1, Math.max(0, v));
  return Math.round(n * COORD_SCALE) / COORD_SCALE;
}

function toCanvasSpace(x: number, y: number): { x: number; y: number } {
  const rect = canvas ? canvas.getBoundingClientRect() : { width: 1, height: 1, x: 0, y: 0 } as DOMRect;
  return {
    x: quantize((x - rect.left) / Math.max(1, rect.width)),
    y: quantize((y - rect.top) / Math.max(1, rect.height)),
  };
}

function drawStrokePath(c: CanvasRenderingContext2D, w: number, h: number, s: Stroke): void {
  const pts = s.points;
  if (pts.length < 2) return;
  c.strokeStyle = s.color;
  c.lineWidth = Math.max(1, (s.size / COORD_SCALE) * Math.min(w, h));
  c.lineCap = "round";
  c.lineJoin = "round";
  c.beginPath();
  c.moveTo(pts[0] * w, pts[1] * h);
  if (pts.length === 2) {
    // A tap is a dot: give the single point a visible round cap by drawing
    // a zero-length segment with round linecap.
    c.lineTo(pts[0] * w + 0.01, pts[1] * h + 0.01);
  } else {
    for (let i = 2; i < pts.length; i += 2) c.lineTo(pts[i] * w, pts[i + 1] * h);
  }
  c.stroke();
}

/** The colour this frame draws with. It is the host-assigned participant
 *  colour, full stop: identity (what you draw as) is the host's decision, so
 *  there is no colour picker in the cage to override it with. Before the host
 *  has assigned one, fall back to the RESOLVED primary token — canvas
 *  strokeStyle cannot parse var() references, so the computed value crosses. */
function strokeColor(): string {
  if (color) return color;
  const t = getComputedStyle(document.documentElement).getPropertyValue("--color-primary").trim();
  return t || "#e0a040";
}

/** Full redraw, rAF-batched: the map is the truth, the canvas is a cache. */
function scheduleRedraw(): void {
  if (redrawQueued || !canvas) return;
  redrawQueued = true;
  // Whichever of the two fires first wins; the other becomes a no-op.
  const run = () => {
    if (!redrawQueued) return;
    redrawQueued = false;
    redraw();
  };
  requestAnimationFrame(run);
  // requestAnimationFrame does NOT fire in a page that is not painting — a
  // backgrounded tab, or one that has not produced its first frame yet. With
  // rAF alone the queued flag latches true and never clears, so the board stays
  // blank AND every later stroke is swallowed by the `redrawQueued` guard. A
  // joiner whose replay landed before its first frame would sit on an empty
  // board forever. The timer is the floor that guarantees a paint.
  setTimeout(run, 100);
}

/** Strokes in a replica-independent paint order: creation time, then id. */
function orderedStrokes(): Stroke[] {
  if (!strokes) return [];
  const stamp = (id: string): string => id.split("-")[3] ?? "";
  return [...strokes.entries()]
    .sort((a, b) => {
      const ta = stamp(a[0]);
      const tb = stamp(b[0]);
      if (ta !== tb) return ta < tb ? -1 : 1;
      return a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0;
    })
    .map(([, s]) => s);
}

function redraw(): void {
  if (!ctx || !canvas) return;
  const w = canvas.width;
  const h = canvas.height;
  ctx.clearRect(0, 0, w, h);
  if (!strokes) return;
  // Paint in a DETERMINISTIC order, not the CRDT's iteration order.
  //
  // Yjs guarantees every replica converges on the same SET of strokes; it says
  // nothing about the order a Y.Map yields them in, and two replicas that
  // received the same strokes by different routes iterate them differently.
  // Overlapping strokes then composite in a different z-order and the two
  // boards render different pixels while holding identical documents — which
  // is exactly what the offline-convergence journey caught.
  //
  // Stroke ids are `s-<pid>-<clientID>-<base36 ms>-<rand>`, so the timestamp
  // field gives chronological z-order (later strokes on top, which is what a
  // drawing surface should do) and the whole id breaks ties. Both are stable
  // across replicas.
  for (const s of orderedStrokes()) drawStrokePath(ctx, w, h, s);
  if (drawing && currentPoints.length >= 2) {
    // The in-progress local stroke renders immediately (not yet in the CRDT).
    drawStrokePath(ctx, w, h, { id: "", color: strokeColor(), size: strokeSize, points: currentPoints });
  }
}

function resizeCanvas(): void {
  if (!canvas) return;
  const dpr = Math.max(1, window.devicePixelRatio || 1);
  const rect = canvas.getBoundingClientRect();
  const w = Math.max(1, Math.round(rect.width * dpr));
  const h = Math.max(1, Math.round(rect.height * dpr));
  if (canvas.width !== w || canvas.height !== h) {
    canvas.width = w;
    canvas.height = h;
  }
  redraw();
}

/** Copy a Uint8Array's bytes into a standalone ArrayBuffer for postMessage.
 *  Yjs hands back views whose .buffer is typed ArrayBufferLike (and possibly
 *  larger than the view); the bridge wants a clean clone of exactly the view. */
function toArrayBuffer(u: Uint8Array): ArrayBuffer {
  const out = new ArrayBuffer(u.byteLength);
  new Uint8Array(out).set(u);
  return out;
}


// --- CRDT --------------------------------------------------------------------

function initDoc(): void {
  ydoc = new Y.Doc();
  strokes = ydoc.getMap<Stroke>("strokes");
  // Only locally-originated transactions produce outbound updates; host
  // deliveries are tagged REMOTE_ORIGIN and never echo back (the host already
  // excludes the originator, but the tag makes re-echo a structural no-op).
  ydoc.on("update", (update: Uint8Array, origin: unknown) => {
    if (origin === REMOTE_ORIGIN) return;
    traffic.updatesSent += 1;
    traffic.bytesSent += update.byteLength;
    if (canSync && connected) {
      sendEvent("syncUpdate", { update: toArrayBuffer(update) });
    }
    emitBoardState();
  });
  strokes.observe(() => scheduleRedraw());
}

function applyRemoteUpdate(data: ArrayBuffer): void {
  if (!ydoc) return;
  const bytes = new Uint8Array(data);
  traffic.updatesReceived += 1;
  traffic.bytesReceived += bytes.byteLength;
  Y.applyUpdate(ydoc, bytes, REMOTE_ORIGIN);
  emitBoardState();
}

/** Full state for the reconnect handshake: the adapter publishes this to the
 *  room hub so offline edits reach everyone else. */
export function snapshotState(): ArrayBuffer {
  if (!ydoc) return new ArrayBuffer(0);
  const u = Y.encodeStateAsUpdate(ydoc);
  return toArrayBuffer(u);
}

// --- presence ----------------------------------------------------------------

function touchCursor(p: { pid: string; color: string; x: number; y: number; down: boolean }): void {
  if (!cursorsEl) return;
  const visible = p.x >= 0 && p.y >= 0;
  let cur = remoteCursors.get(p.pid);
  if (!visible) {
    if (cur) {
      cur.el.remove();
      remoteCursors.delete(p.pid);
    }
    return;
  }
  if (!cur) {
    const el = document.createElement("div");
    el.className = "wb-cursor";
    el.setAttribute("data-wb-cursor", p.pid);
    el.style.setProperty("--cursor-color", p.color);
    cursorsEl.appendChild(el);
    cur = { pid: p.pid, color: p.color, x: p.x, y: p.y, down: false, el, expiresAt: 0 };
    remoteCursors.set(p.pid, cur);
  }
  cur.x = p.x;
  cur.y = p.y;
  cur.color = p.color;
  cur.expiresAt = performance.now() + PRESENCE_TTL_MS;
  cur.el.style.setProperty("--cursor-color", p.color);
  cur.el.style.left = `${p.x * 100}%`;
  cur.el.style.top = `${p.y * 100}%`;
  cur.el.classList.toggle("wb-cursor-down", p.down);
}

function reapStaleCursors(): void {
  const now = performance.now();
  for (const [id, cur] of remoteCursors) {
    if (cur.expiresAt < now) {
      cur.el.remove();
      remoteCursors.delete(id);
    }
  }
}

function sendPresence(x: number, y: number, down: boolean, force = false): void {
  if (!canSync) return;
  const now = performance.now();
  if (!force && now - lastPresenceSent < PRESENCE_THROTTLE_MS) return;
  lastPresenceSent = now;
  sendEvent("presenceUpdate", { x, y, down });
}

// --- status / telemetry --------------------------------------------------------

function renderStatus(): void {
  if (!statusEl) return;
  let text: string;
  let cls: string;
  if (!canSync) {
    text = "no room hub — local only";
    cls = "wb-offline";
  } else if (connected) {
    const noun = participants === 1 ? "participant" : "participants";
    text = `synced · ${participants || 1} ${noun}`;
    cls = "wb-online";
  } else {
    text = "offline — drawing locally";
    cls = "wb-offline";
  }
  statusEl.textContent = text;
  statusEl.className = `wb-status ${cls}`;
}

function renderIdentity(): void {
  if (swatchEl) swatchEl.style.background = color || "var(--color-border-strong)";
  if (participantLabelEl) participantLabelEl.textContent = pid ? `you are ${pid}` : "host assigns identity";
}

/** Periodic bridge telemetry so a parent-side test/demo can see the document
 *  without reading into the opaque frame. */
function emitBoardState(): void {
  sendEvent("boardState", {
    strokes: strokes ? strokes.size : 0,
    pid,
    color,
    connected,
    participants,
    ...traffic,
  });
}

export function applySyncStatus(p: {
  connected?: unknown;
  pid?: unknown;
  color?: unknown;
  participants?: unknown;
}): void {
  if (typeof p.connected === "boolean") connected = p.connected;
  if (typeof p.pid === "string") pid = p.pid;
  if (typeof p.color === "string" && p.color) color = p.color;
  if (typeof p.participants === "number") participants = p.participants;
  renderIdentity();
  renderStatus();
  emitBoardState();
}

export function setCanSync(v: boolean): void {
  canSync = v;
  renderStatus();
}

// --- pointer input -------------------------------------------------------------

function strokeHit(x: number, y: number): string | null {
  if (!strokes) return null;
  // Iterate newest-last; a tie goes to the most recently added stroke.
  let hit: string | null = null;
  for (const [id, s] of strokes.entries()) {
    const pts = s.points;
    const r = Math.max(ERASE_HIT_RADIUS, (s.size / COORD_SCALE) * 0.8);
    for (let i = 0; i < pts.length; i += 2) {
      const dx = pts[i] - x;
      const dy = pts[i + 1] - y;
      if (dx * dx + dy * dy <= r * r) {
        hit = id;
        break;
      }
    }
  }
  return hit;
}

function eraseAt(x: number, y: number): void {
  if (!strokes) return;
  const id = strokeHit(x, y);
  if (id !== null && strokes.has(id)) {
    strokes.delete(id); // CRDT delete: converges with every other replica
  }
}

function onPointerDown(e: PointerEvent): void {
  if (!canvas) return;
  canvas.setPointerCapture(e.pointerId);
  const { x, y } = toCanvasSpace(e.clientX, e.clientY);
  sendPresence(x, y, true, true);
  if (tool === "eraser") {
    erasing = true;
    eraseAt(x, y);
    return;
  }
  drawing = true;
  currentPoints = [x, y];
  redraw();
}

function onPointerMove(e: PointerEvent): void {
  const { x, y } = toCanvasSpace(e.clientX, e.clientY);
  sendPresence(x, y, drawing || erasing);
  if (erasing) {
    eraseAt(x, y);
    return;
  }
  if (!drawing) return;
  const n = currentPoints.length;
  // Skip sub-quantum moves so a drag produces sane point counts.
  if (n >= 2) {
    const dx = currentPoints[n - 2] - x;
    const dy = currentPoints[n - 1] - y;
    if (dx * dx + dy * dy < (0.6 / COORD_SCALE) ** 2) return;
  }
  currentPoints.push(x, y);
  redraw();
}

function onPointerUp(e: PointerEvent): void {
  if (drawing && strokes && currentPoints.length >= 2 && ydoc) {
    const id = `s-${pid || "local"}-${ydoc.clientID.toString(36)}-${Date.now().toString(36)}-${Math.floor(Math.random() * 1e6).toString(36)}`;
    const committed: Stroke = {
      id,
      color: strokeColor(), // host-assigned participant colour once identity lands
      size: strokeSize,
      points: currentPoints.slice(),
    };
    ydoc.transact(() => {
      strokes!.set(id, committed);
    });
  }
  drawing = false;
  erasing = false;
  currentPoints = [];
  if (canvas && canvas.hasPointerCapture(e.pointerId)) canvas.releasePointerCapture(e.pointerId);
  const { x, y } = toCanvasSpace(e.clientX, e.clientY);
  sendPresence(x, y, false, true);
  scheduleRedraw();
}

function onPointerLeave(_e: PointerEvent): void {
  sendPresence(-1, -1, false, true); // x/y < 0 hides the remote cursor immediately
}

// --- toolbar -------------------------------------------------------------------

/** One shared "which buttons are on" computation so every click refreshes
 *  the whole tool group consistently (aria-pressed + .wb-btn-on stay in
 *  lockstep with the tool/size state). */
const TOOL_BUTTON_IDS = ["wb-tool-pen", "wb-tool-eraser", "wb-size-s", "wb-size-m", "wb-size-l"];

function buttonIsOn(btnId: string): boolean {
  switch (btnId) {
    case "wb-tool-pen": return tool === "pen";
    case "wb-tool-eraser": return tool === "eraser";
    case "wb-size-s": return strokeSize === STROKE_SIZES.s;
    case "wb-size-m": return strokeSize === STROKE_SIZES.m;
    default: return strokeSize === STROKE_SIZES.l;
  }
}

function refreshToolbar(): void {
  for (const btnId of TOOL_BUTTON_IDS) {
    const el = document.getElementById(btnId) as HTMLButtonElement | null;
    if (!el) continue;
    const on = buttonIsOn(btnId);
    el.classList.toggle("wb-btn-on", on);
    el.setAttribute("aria-pressed", on ? "true" : "false");
  }
}

function bindToolbar(): void {
  const activations: Record<string, () => void> = {
    "wb-tool-pen": () => { tool = "pen"; },
    "wb-tool-eraser": () => { tool = "eraser"; },
    "wb-size-s": () => { strokeSize = STROKE_SIZES.s; },
    "wb-size-m": () => { strokeSize = STROKE_SIZES.m; },
    "wb-size-l": () => { strokeSize = STROKE_SIZES.l; },
  };
  for (const [btnId, activate] of Object.entries(activations)) {
    const btn = document.getElementById(btnId) as HTMLButtonElement | null;
    if (!btn) continue;
    btn.addEventListener("click", () => {
      activate();
      refreshToolbar();
    });
  }
}

// --- boot ----------------------------------------------------------------------

/** Wire the DOM, the CRDT, and pointer input. Called once from main.ts after
 *  init (the canvas needs tokens applied for a sane first paint, though it
 *  re-renders token-agnostically — strokes carry their own colors). */
export function mountBoard(): void {
  canvas = document.getElementById("wb-canvas") as HTMLCanvasElement | null;
  cursorsEl = document.getElementById("wb-cursors");
  statusEl = document.getElementById("wb-status");
  swatchEl = document.getElementById("wb-participant-swatch");
  participantLabelEl = document.getElementById("wb-participant-label");
  if (!canvas) return;
  ctx = canvas.getContext("2d");
  if (!ctx) return;

  initDoc();

  canvas.addEventListener("pointerdown", onPointerDown);
  canvas.addEventListener("pointermove", onPointerMove);
  canvas.addEventListener("pointerup", onPointerUp);
  canvas.addEventListener("pointercancel", onPointerUp);
  canvas.addEventListener("pointerleave", onPointerLeave);

  bindToolbar();

  resizeObserver = new ResizeObserver(() => resizeCanvas());
  resizeObserver.observe(canvas.parentElement ?? canvas);
  resizeCanvas();

  presenceTimer = window.setInterval(reapStaleCursors, 1000);
  renderStatus();
  emitBoardState();
}

/** Host-delivered CRDT update (SSE → adapter → bridge). */
export function handleSyncApply(params: unknown): void {
  const p = params as { update?: unknown } | null;
  const u = p && p.update;
  if (u instanceof ArrayBuffer) applyRemoteUpdate(u);
  else if (u instanceof Uint8Array) applyRemoteUpdate(toArrayBuffer(u));
}

/** Host-delivered presence: {pid, color, x, y, down} — never a name. */
export function handlePresenceApply(params: unknown): void {
  const p = params as { pid?: unknown; color?: unknown; x?: unknown; y?: unknown; down?: unknown } | null;
  if (!p || typeof p.pid !== "string") return;
  touchCursor({
    pid: p.pid,
    color: typeof p.color === "string" ? p.color : "var(--color-primary)",
    x: typeof p.x === "number" ? p.x : -1,
    y: typeof p.y === "number" ? p.y : -1,
    down: p.down === true,
  });
}

/** Teardown (protocol v1): drop listeners and timers, ack. */
export function teardownBoard(): void {
  window.clearInterval(presenceTimer);
  resizeObserver?.disconnect();
  remoteCursors.forEach((c) => c.el.remove());
  remoteCursors.clear();
}

// --- test/debug hooks (published by main.ts onto window.__wbDebug) -------------

export interface DebugApi {
  strokeIds(): string[];
  /** Ids in the order redraw() paints them — the property that must agree
   *  across replicas, asserted directly rather than inferred from pixels. */
  paintOrder(): string[];
  strokeDump(): string;
  presencePids(): string[];
  connected(): boolean;
  identity(): { pid: string; color: string; participants: number };
  traffic(): typeof traffic;
  cursorCount(): number;
}

export function debugApi(): DebugApi {
  return {
    strokeIds: () => (strokes ? [...strokes.keys()].sort() : []),
    paintOrder: () => orderedStrokes().map((s) => s.id),
    strokeDump: () =>
      strokes
        ? JSON.stringify(
            [...strokes.entries()]
              .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0))
              .map(([id, s]) => [id, s.color, s.size, s.points.length, s.points.reduce((a, v) => a + v, 0)])
          )
        : "[]",
    presencePids: () => [...remoteCursors.keys()].sort(),
    connected: () => connected && canSync,
    identity: () => ({ pid, color, participants }),
    traffic: () => ({ ...traffic }),
    cursorCount: () => remoteCursors.size,
  };
}
