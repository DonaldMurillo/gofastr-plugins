// The frame-side compositor — the TypeScript twin of imageedit/render.go.
// THE INTERCHANGE CONTRACT: every function here mirrors its Go counterpart
// with the identical integer arithmetic (crop → rotate → annotate → redact;
// fillRect / strokeRect / Bresenham / arrow head / bitmap font). No floats,
// no anti-aliasing, no resampling. A PNG source therefore composes to the
// same pixels in the frame's canvas and in Go's export — the property the
// preview-vs-server agreement tests assert. Never change one side alone.
//
// The input is straight-alpha 8-bit RGBA (canvas ImageData), the exact shape
// Go's toNRGBA normalizes to, so parity holds regardless of what either
// decoder produced upstream.

import { ADVANCE_CELLS, GLYPH_HEIGHT, GLYPH_WIDTH, glyphRows } from "./font";

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface Annotation {
  id: string;
  type: "rect" | "arrow" | "text";
  color: string; // #RRGGBB
  width: number;
  x: number;
  y: number;
  w: number;
  h: number;
  x2: number;
  y2: number;
  size: number;
  text: string;
}

export interface Redaction {
  id: string;
  rect: Rect;
  fill: string; // #RRGGBB
}

export interface Doc {
  schemaVersion: string;
  src: { kind: string; ref: string; sha256?: string };
  crop?: Rect | null;
  rotate: number; // 0 | 90 | 180 | 270, clockwise
  annotations: Annotation[];
  redactions: Redaction[];
  rev?: number;
}

/** A parsed #RRGGBB literal — the doc's only color syntax. */
export interface RGBA {
  r: number;
  g: number;
  b: number;
}

export function parseHexColor(s: string): RGBA {
  if (s.length !== 7 || s.charCodeAt(0) !== 0x23 /* '#' */) {
    throw new Error(`color ${JSON.stringify(s)} must be #RRGGBB`);
  }
  const byteAt = (i: number): number => {
    const c = s.charCodeAt(i);
    if (c >= 0x30 && c <= 0x39) return c - 0x30; // 0-9
    if (c >= 0x61 && c <= 0x66) return c - 0x61 + 10; // a-f
    if (c >= 0x41 && c <= 0x46) return c - 0x41 + 10; // A-F
    throw new Error(`bad hex digit in ${JSON.stringify(s)}`);
  };
  return {
    r: (byteAt(1) << 4) | byteAt(2),
    g: (byteAt(3) << 4) | byteAt(4),
    b: (byteAt(5) << 4) | byteAt(6),
  };
}

// --- Raster: the ImageData twin of Go's *image.NRGBA ------------------------

export class Raster {
  readonly width: number;
  readonly height: number;
  readonly data: Uint8ClampedArray;

  constructor(width: number, height: number, data?: Uint8ClampedArray) {
    this.width = width;
    this.height = height;
    this.data = data ?? new Uint8ClampedArray(width * height * 4);
  }

  static fromImageData(img: ImageData): Raster {
    return new Raster(img.width, img.height, new Uint8ClampedArray(img.data));
  }

  toImageData(): ImageData {
    return new ImageData(new Uint8ClampedArray(this.data), this.width, this.height);
  }
}

// --- The transform: crop → rotate (mirror of render.go) ---------------------

function clampInt(v: number, lo: number, hi: number): number {
  return v < lo ? lo : v > hi ? hi : v;
}

function minInt(a: number, b: number): number {
  return a < b ? a : b;
}

function maxInt(a: number, b: number): number {
  return a > b ? a : b;
}

function absInt(v: number): number {
  return v < 0 ? -v : v;
}

/** Truncate-toward-zero division — Go's int division, which JS's `/` is not. */
function truncDiv(a: number, b: number): number {
  return Math.trunc(a / b);
}

export function effectiveCrop(img: Raster, crop?: Rect | null): Rect {
  const full = { x: 0, y: 0, w: img.width, h: img.height };
  if (!crop) return full;
  const x0 = clampInt(Math.round(crop.x), 0, full.w);
  const y0 = clampInt(Math.round(crop.y), 0, full.h);
  const x1 = clampInt(Math.round(crop.x + crop.w), 0, full.w);
  const y1 = clampInt(Math.round(crop.y + crop.h), 0, full.h);
  return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
}

export function mapPoint(px: number, py: number, crop: Rect, rot: number): [number, number] {
  const x = px - crop.x;
  const y = py - crop.y;
  switch (rot) {
    case 90:
      return [crop.h - 1 - y, x];
    case 180:
      return [crop.w - 1 - x, crop.h - 1 - y];
    case 270:
      return [y, crop.w - 1 - x];
    default:
      return [x, y];
  }
}

export function mapRect(r: Rect, crop: Rect, rot: number): Rect {
  const corners: [number, number][] = [
    mapPoint(r.x, r.y, crop, rot),
    mapPoint(r.x + r.w - 1, r.y, crop, rot),
    mapPoint(r.x, r.y + r.h - 1, crop, rot),
    mapPoint(r.x + r.w - 1, r.y + r.h - 1, crop, rot),
  ];
  const xs = corners.map((c) => c[0]);
  const ys = corners.map((c) => c[1]);
  const minX = Math.min(...xs);
  const maxX = Math.max(...xs);
  const minY = Math.min(...ys);
  const maxY = Math.max(...ys);
  return { x: minX, y: minY, w: maxX - minX + 1, h: maxY - minY + 1 };
}

export function outDims(crop: Rect, rot: number): [number, number] {
  return rot === 90 || rot === 270 ? [crop.h, crop.w] : [crop.w, crop.h];
}

// --- Drawing primitives (mirror of render.go, line for line) ----------------

function fillRect(img: Raster, x: number, y: number, w: number, h: number, c: RGBA): void {
  if (w <= 0 || h <= 0) return;
  const x0 = clampInt(x, 0, img.width);
  const y0 = clampInt(y, 0, img.height);
  const x1 = clampInt(x + w, 0, img.width);
  const y1 = clampInt(y + h, 0, img.height);
  for (let yy = y0; yy < y1; yy++) {
    let i = (yy * img.width + x0) * 4;
    for (let xx = x0; xx < x1; xx++) {
      img.data[i] = c.r;
      img.data[i + 1] = c.g;
      img.data[i + 2] = c.b;
      img.data[i + 3] = 255;
      i += 4;
    }
  }
}

function strokeRect(img: Raster, r: Rect, t: number, c: RGBA): void {
  if (r.w <= 0 || r.h <= 0) return;
  const tTop = minInt(t, r.h);
  fillRect(img, r.x, r.y, r.w, tTop, c);
  const rem = r.h - tTop;
  if (rem <= 0) return;
  const tBot = minInt(t, rem);
  fillRect(img, r.x, r.y + r.h - tBot, r.w, tBot, c);
  const remH = r.h - tTop - tBot;
  if (remH <= 0) return;
  const tL = minInt(t, r.w);
  fillRect(img, r.x, r.y + tTop, tL, remH, c);
  const remW = r.w - tL;
  if (remW <= 0) return;
  const tR = minInt(t, remW);
  fillRect(img, r.x + r.w - tR, r.y + tTop, tR, remH, c);
}

function stamp(img: Raster, x: number, y: number, t: number, c: RGBA): void {
  const off = truncDiv(t - 1, 2);
  fillRect(img, x - off, y - off, t, t, c);
}

function drawLine(img: Raster, x1: number, y1: number, x2: number, y2: number, t: number, c: RGBA): void {
  const dx = absInt(x2 - x1);
  const sx = x1 > x2 ? -1 : 1;
  const dy = -absInt(y2 - y1);
  const sy = y1 > y2 ? -1 : 1;
  let err = dx + dy;
  let x = x1;
  let y = y1;
  for (;;) {
    stamp(img, x, y, t, c);
    if (x === x2 && y === y2) break;
    const e2 = 2 * err;
    if (e2 >= dy) {
      err += dy;
      x += sx;
    }
    if (e2 <= dx) {
      err += dx;
      y += sy;
    }
  }
}

/** floor(sqrt(n)) by the same integer bisection render.go uses. */
function isqrt(n: number): number {
  if (n <= 0) return 0;
  let lo = 0;
  let hi = n;
  while (lo < hi) {
    const mid = truncDiv(lo + hi + 1, 2);
    if (mid * mid <= n) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

function drawArrow(img: Raster, x1: number, y1: number, x2: number, y2: number, t: number, c: RGBA): void {
  drawLine(img, x1, y1, x2, y2, t, c);
  const dx = x2 - x1;
  const dy = y2 - y1;
  const length = isqrt(dx * dx + dy * dy);
  if (length === 0) return;
  let L = 3 * t + 5;
  if (L > length) L = length;
  const bx = truncDiv(dx * L, length); // truncate toward zero, as Go does
  const by = truncDiv(dy * L, length);
  drawLine(img, x2, y2, x2 - bx + truncDiv(by, 2), y2 - by - truncDiv(bx, 2), t, c);
  drawLine(img, x2, y2, x2 - bx - truncDiv(by, 2), y2 - by + truncDiv(bx, 2), t, c);
}

function drawText(img: Raster, x: number, y: number, s: string, scale: number, c: RGBA): void {
  let cursor = 0;
  for (const ch of s) {
    const rows = glyphRows(ch);
    if (rows) {
      for (let row = 0; row < GLYPH_HEIGHT; row++) {
        const bits = rows[row];
        for (let col = 0; col < GLYPH_WIDTH; col++) {
          if ((bits & (1 << (GLYPH_WIDTH - 1 - col))) !== 0) {
            fillRect(img, x + (cursor + col) * scale, y + row * scale, scale, scale, c);
          }
        }
      }
    }
    cursor += ADVANCE_CELLS;
  }
}

// --- Compose (mirror of render.go's compose) --------------------------------

export interface ComposeResult {
  out: Raster;
  /** Everything except the redactions — what a redaction removes. */
  pre: Raster;
  /** Output-space redaction rects, same order as doc.redactions. */
  redactionRects: Rect[];
  width: number;
  height: number;
}

export function compose(src: Raster, doc: Doc): ComposeResult {
  const crop = effectiveCrop(src, doc.crop);
  if (crop.w <= 0 || crop.h <= 0) throw new Error("crop rect outside the image");

  // Steps 1–2: crop, then rotate — one forward pass, each output pixel
  // written exactly once from its source pixel.
  const [ow, oh] = outDims(crop, doc.rotate);
  const base = new Raster(ow, oh);
  for (let py = crop.y; py < crop.y + crop.h; py++) {
    for (let px = crop.x; px < crop.x + crop.w; px++) {
      const [ox, oy] = mapPoint(px, py, crop, doc.rotate);
      const si = (py * src.width + px) * 4;
      const oi = (oy * ow + ox) * 4;
      base.data[oi] = src.data[si];
      base.data[oi + 1] = src.data[si + 1];
      base.data[oi + 2] = src.data[si + 2];
      base.data[oi + 3] = src.data[si + 3];
    }
  }

  // Step 3: annotations. Pre ends here.
  const pre = new Raster(ow, oh);
  pre.data.set(base.data);
  for (const a of doc.annotations) {
    const c = parseHexColor(a.color);
    if (a.type === "rect") {
      strokeRect(pre, mapRect({ x: a.x, y: a.y, w: a.w, h: a.h }, crop, doc.rotate), a.width, c);
    } else if (a.type === "arrow") {
      const [x1, y1] = mapPoint(a.x, a.y, crop, doc.rotate);
      const [x2, y2] = mapPoint(a.x2, a.y2, crop, doc.rotate);
      drawArrow(pre, x1, y1, x2, y2, a.width, c);
    } else if (a.type === "text") {
      const [tx, ty] = mapPoint(a.x, a.y, crop, doc.rotate);
      drawText(pre, tx, ty, a.text, a.size, c);
    }
  }

  // Step 4: redactions, last, on a fresh copy.
  const out = new Raster(ow, oh);
  out.data.set(pre.data);
  const rects: Rect[] = [];
  for (const r of doc.redactions) {
    const c = parseHexColor(r.fill);
    const m = mapRect(r.rect, crop, doc.rotate);
    fillRect(out, m.x, m.y, m.w, m.h, c);
    rects.push(m);
  }

  return { out, pre, redactionRects: rects, width: ow, height: oh };
}

// --- The inverse display→source mapping (frame-only; no Go twin needed) -----
//
// The user draws on the DISPLAYED (cropped+rotated, CSS-scaled) canvas. This
// maps a display point back to SOURCE-image pixels: un-scale, then invert
// crop∘rotate. Only the frame performs it — the stored doc values it
// produces are what both renderers consume.

export function displayToSource(outX: number, outY: number, crop: Rect, rot: number): [number, number] {
  // Invert the rotation on output-local coordinates. The caller has already
  // un-scaled any CSS display factor.
  let lx: number;
  let ly: number;
  switch (rot) {
    case 90:
      lx = outY;
      ly = crop.h - 1 - outX;
      break;
    case 180:
      lx = crop.w - 1 - outX;
      ly = crop.h - 1 - outY;
      break;
    case 270:
      lx = crop.w - 1 - outY;
      ly = outX;
      break;
    default:
      lx = outX;
      ly = outY;
  }
  return [lx + crop.x, ly + crop.y];
}
