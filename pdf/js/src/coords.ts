// Coordinate conversion — PDF user space ↔ viewport CSS pixels.
//
// THE invariant of this whole plugin: every annotation's geometry is stored in
// PDF USER SPACE (points, origin BOTTOM-LEFT) plus the page's own /Rotate.
// CSS pixels live only at the edges — when a pointer event creates an
// annotation and when an annotation is painted back over the canvas. Storing
// viewport pixels would make annotations drift on zoom/rotation and land in
// the wrong place on export. So all conversion funnels through here.
//
// We delegate the actual transform to pdf.js's PageViewport, which already
// folds scale + (page-inherent ∪ user-view) rotation into convertToViewport*
// and convertToPDF*. This module is the narrow, typed, unit-tested surface the
// tools and the overlay layer use, so the maths appears in exactly one place.


/** A rect in PDF user space as the overlay stores it: [x, y, w, h]. */
export type PdfRect = [x: number, y: number, w: number, h: number];

/** A rectangle positioned in CSS pixels relative to a page's overlay layer. */
export interface CssBox {
  left: number;
  top: number;
  width: number;
  height: number;
}

/** A quad in PDF user space: four corners, clockwise from bottom-left. */
export interface PdfQuad {
  x1: number; y1: number;
  x2: number; y2: number;
  x3: number; y3: number;
  x4: number; y4: number;
}

/**
 * Minimal viewport surface this module needs. pdf.js's `PageViewport` satisfies
 * it; tests pass a fake. Keeping the surface narrow means the unit tests do not
 * have to import pdfjs-dist.
 */
export interface ViewportLike {
  convertToViewportPoint(x: number, y: number): number[];
  convertToPdfPoint(x: number, y: number): number[];
}

// --- PDF → CSS (paint) -----------------------------------------------------

/** Convert a PDF-space rect [x,y,w,h] to a CSS box over the overlay layer. */
export function pdfRectToCss(vp: ViewportLike, r: PdfRect): CssBox {
  const [x, y, w, h] = r;
  const a = vp.convertToViewportPoint(x, y);
  const b = vp.convertToViewportPoint(x + w, y + h);
  return {
    left: Math.min(a[0], b[0]),
    top: Math.min(a[1], b[1]),
    width: Math.abs(b[0] - a[0]),
    height: Math.abs(b[1] - a[1]),
  };
}

/** Convert a PDF-space quad (e.g. one line of a highlight) to a CSS box. */
export function pdfQuadToCss(vp: ViewportLike, q: PdfQuad): CssBox {
  const xs = [q.x1, q.x2, q.x3, q.x4];
  const ys = [q.y1, q.y2, q.y3, q.y4];
  const p = xs.map((xv, i) => vp.convertToViewportPoint(xv, ys[i]));
  const cxs = p.map((c) => c[0]);
  const cys = p.map((c) => c[1]);
  const left = Math.min(...cxs);
  const top = Math.min(...cys);
  return {
    left,
    top,
    width: Math.max(...cxs) - left,
    height: Math.max(...cys) - top,
  };
}

// --- CSS → PDF (author) ----------------------------------------------------

/**
 * Convert a CSS drag rectangle (two corners) into a PDF-space rect [x,y,w,h].
 * `w`/`h` are clamped to a tiny positive epsilon so a click (zero-area drag)
 * still yields a valid, minimally-sized annotation rather than a degenerate
 * rect the renderer/exporter would have to special-case everywhere.
 */
export function cssDragToPdfRect(
  vp: ViewportLike,
  ax: number, ay: number, bx: number, by: number,
  minSize = 2
): PdfRect {
  const a = vp.convertToPdfPoint(ax, ay);
  const b = vp.convertToPdfPoint(bx, by);
  const x = Math.min(a[0], b[0]);
  const y = Math.min(a[1], b[1]);
  let w = Math.abs(b[0] - a[0]);
  let h = Math.abs(b[1] - a[1]);
  if (w < minSize) w = minSize;
  if (h < minSize) h = minSize;
  return [x, y, w, h];
}

/** Convert a single CSS point to PDF user space (used for ink strokes). */
export function cssPointToPdf(vp: ViewportLike, x: number, y: number): [number, number] {
  const p = vp.convertToPdfPoint(x, y);
  return [p[0], p[1]];
}

/** Convert a CSS client rect (e.g. a text-selection range rect) to a PDF quad. */
export function cssRectToPdfQuad(
  vp: ViewportLike,
  left: number, top: number, right: number, bottom: number
): PdfQuad {
  // The four corners of the selection rect, returned as a clockwise quad in
  // PDF space. Selection rects are already axis-aligned in CSS, so the quad is
  // a rectangle, but keeping the quad shape lets a future text-extraction path
  // consume it directly.
  const [x1, y1] = vp.convertToPdfPoint(left, top);       // top-left
  const [x2, y2] = vp.convertToPdfPoint(right, top);      // top-right
  const [x3, y3] = vp.convertToPdfPoint(right, bottom);   // bottom-right
  const [x4, y4] = vp.convertToPdfPoint(left, bottom);    // bottom-left
  return { x1, y1, x2, y2, x3, y3, x4, y4 };
}

/** Bounding PDF rect that encloses a set of quads (a multi-line highlight). */
export function boundingQuads(quads: PdfQuad[]): PdfRect {
  if (quads.length === 0) return [0, 0, 0, 0];
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const q of quads) {
    const xs = [q.x1, q.x2, q.x3, q.x4];
    const ys = [q.y1, q.y2, q.y3, q.y4];
    for (const xv of xs) { if (xv < minX) minX = xv; if (xv > maxX) maxX = xv; }
    for (const yv of ys) { if (yv < minY) minY = yv; if (yv > maxY) maxY = yv; }
  }
  return [minX, minY, maxX - minX, maxY - minY];
}

/**
 * Move a PDF rect by a CSS-pixel delta (drag). Converts the delta to PDF space
 * at the current viewport so the move tracks the cursor 1:1 regardless of zoom
 * or rotation. Returns the new rect.
 */
export function nudgePdfRectByCssDelta(
  vp: ViewportLike,
  r: PdfRect,
  dxCss: number, dyCss: number
): PdfRect {
  // A delta in CSS maps to a delta in PDF by differencing two converted points.
  const o = vp.convertToPdfPoint(0, 0);
  const d = vp.convertToPdfPoint(dxCss, dyCss);
  const dx = d[0] - o[0];
  const dy = d[1] - o[1];
  return [r[0] + dx, r[1] + dy, r[2], r[3]];
}

/**
 * Resize a PDF rect from a CSS-pixel delta applied to the given handle edge.
 * `edge` names the side(s) being dragged; the opposite side stays fixed by
 * converting the fixed corner through the viewport so the resize is stable
 * under rotation. Keeps a positive minimum size.
 */
export type HandleEdge = "nw" | "ne" | "sw" | "se" | "n" | "s" | "e" | "w";

export function resizePdfRectByCssDelta(
  vp: ViewportLike,
  r: PdfRect,
  edge: HandleEdge,
  dxCss: number, dyCss: number,
  minSize = 4
): PdfRect {
  let [x, y, w, h] = r;
  const o = vp.convertToPdfPoint(0, 0);
  const d = vp.convertToPdfPoint(dxCss, dyCss);
  const dx = d[0] - o[0];
  const dy = d[1] - o[1];
  if (edge.includes("e")) w = Math.max(minSize, w + dx);
  if (edge.includes("w")) { const nx = Math.min(x + w - minSize, x + dx); w = x + w - nx; x = nx; }
  if (edge.includes("n")) { const ny = Math.min(y + h - minSize, y + dy); h = y + h - ny; y = ny; }
  if (edge.includes("s")) h = Math.max(minSize, h + dy);
  return [x, y, w, h];
}
