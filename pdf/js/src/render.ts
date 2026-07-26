// Renderer — rasterizes one page to a canvas.
//
// CANVAS AREA CLAMP (load-bearing, the WebKit silent-blank bug): WebKit —
// especially iOS — silently caps single-canvas area around 16 MP and, when
// exceeded, hands back a BLANK bitmap with NO error. A zoomed-in or high-DPR
// page can blow past that cap and render as white with zero console output.
// This class of failure has shipped bugs in this repo before. So we compute
// width*height of the requested backing store and, if it exceeds MAX_CANVAS
// PIXELS, scale the RENDER down (keeping the CSS display size unchanged so the
// page still looks right — only the backing-store DPI is reduced). ~12 MP is
// the safe target per the brief, comfortably under WebKit's hard cap.
//
// DPR: the backing store is devicePixelRatio× the CSS box (clamped as above),
// and the render transform scales the viewport render into the backing store.
// task.cancel() + RenderingCancelledException is how fast scroll recycles
// canvases; the caller swallows the resulting cancellation.

import { RenderingCancelledException } from "pdfjs-dist";
import type { PDFPageProxy, PageViewport, RenderTask } from "pdfjs-dist";

// Safe single-canvas backing-store cap (~12 megapixels). See file header.
export const MAX_CANVAS_PIXELS = 12_000_000;

export interface CanvasSample {
  nonBlank: boolean;
  nonWhitePixels: number;
}

export interface RenderJob {
  task: RenderTask;
}

// Configure the canvas backing store for a viewport at the current DPR, after
// the area clamp. Returns the sx/sy transform that maps the viewport (CSS
// pixels) into the (possibly clamped) backing store.
export function sizeCanvas(
  canvas: HTMLCanvasElement,
  viewport: PageViewport
): { cssW: number; cssH: number; backingW: number; backingH: number; sx: number; sy: number } {
  const dpr = window.devicePixelRatio || 1;
  const cssW = Math.max(1, Math.floor(viewport.width));
  const cssH = Math.max(1, Math.floor(viewport.height));
  let backingW = Math.max(1, Math.round(cssW * dpr));
  let backingH = Math.max(1, Math.round(cssH * dpr));
  const area = backingW * backingH;
  if (area > MAX_CANVAS_PIXELS) {
    // Clamp area, not dimension: keeps aspect ratio and display size, lowers DPI.
    const f = Math.sqrt(MAX_CANVAS_PIXELS / area);
    backingW = Math.max(1, Math.floor(backingW * f));
    backingH = Math.max(1, Math.floor(backingH * f));
  }
  canvas.width = backingW;
  canvas.height = backingH;
  canvas.style.width = cssW + "px";
  canvas.style.height = cssH + "px";
  const sx = backingW / cssW;
  const sy = backingH / cssH;
  return { cssW, cssH, backingW, backingH, sx, sy };
}

// Start a render. The caller owns the returned task: cancel() it on fast scroll
// or eviction and await task.promise (which rejects with
// RenderingCancelledException on cancel).
export function startRender(
  page: PDFPageProxy,
  canvas: HTMLCanvasElement,
  viewport: PageViewport
): RenderJob {
  const { sx, sy } = sizeCanvas(canvas, viewport);
  const task = page.render({
    canvas,
    viewport,
    transform: sx !== 1 || sy !== 1 ? [sx, 0, 0, sy, 0, 0] : undefined,
  });
  return { task };
}

// Did the render reject because we cancelled it (the only benign failure)?
export function isCancelled(e: unknown): boolean {
  if (e instanceof RenderingCancelledException) return true;
  return e instanceof Error && e.name === "RenderingCancelledException";
}

// Sample the whole canvas; "non-blank" = at least one non-transparent, non-white
// pixel. Tainted canvas (SecurityError) reports nonBlank=false — pdf.js draws
// from the in-memory PDF data (no cross-origin images), so the canvas is NOT
// tainted here. Used once on page 1 for the regression contract.
export function sampleCanvas(ctx: CanvasRenderingContext2D, w: number, h: number): CanvasSample {
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

// Release the backing store of a recycled/evicted canvas (set both dims to 0).
// Setting width=0 is the documented way to free the bitmap; cheaper than
// nulling the context and lets the slot reuse the same <canvas> node.
export function clearCanvas(canvas: HTMLCanvasElement): void {
  canvas.width = 0;
  canvas.height = 0;
}
