// Rasterize-and-rebuild redaction pipeline (the browser/frame side).
//
// Pages WITH a redaction: render with pdf.js at the chosen DPI, paint the
// redaction rects opaque (optionally stamp the reason label), then embed the
// result as an image into a NEW page of a freshly created pdf-lib document at
// the original page box. Pages WITHOUT one: copyPages through losslessly so
// their text + vectors survive untouched.
//
// Then strip the two residuals a fresh rebuild does NOT drop on its own
// (measured, see docs/DECISIONS.md 2026-07-26):
//   - the Info dict (clearing keys leaves empty stubs → delete the dict)
//   - /Annots on every page (a secret planted in an annotation's /Contents
//     survived a rebuild — strip by default)
//
// This module produces bytes ONLY. It does not verify them and it does not
// touch the bridge — the editor orchestrates rasterize → verify → emit, and
// emits NOTHING if verification fails.

import { PDFDocument, PDFName, PDFRef } from "pdf-lib";
import type { PDFPageProxy, PageViewport } from "pdfjs-dist";
import type { PdfModel } from "../pdfdoc";
import type { PdfRect } from "../coords";
import type { Redaction } from "../doc";
import { MAX_CANVAS_PIXELS } from "../render";

export type RedactImageMime = "image/png" | "image/jpeg";

export interface RedactInput {
  sourceBytes: Uint8Array;
  model: PdfModel;
  redactions: Redaction[];
  dpi: number;
  mime?: RedactImageMime;
  quality?: number;        // jpeg 0..1 (ignored for png)
  /** Stamp each rect's reason label onto the rasterized page (default true). */
  stampReasons?: boolean;
  /** Progress callback (fires once per page, main-thread-friendly). */
  onProgress?: (done: number, total: number, page: number) => void;
  /** Yield to the event loop at least every `yieldMs` ms so the UI stays live. */
  yieldMs?: number;
}

export interface RedactStats {
  rasterized: number[];    // 1-based pages that became images
  copied: number[];        // 1-based pages copied losslessly
  imageBytes: number;      // total embedded image bytes
  /** DPI actually used per rasterized page (may be < requested after clamping). */
  dpiUsed: Record<number, number>;
}

export interface RedactOutput {
  bytes: Uint8Array;
  stats: RedactStats;
}

function dataUrlToBytes(dataUrl: string): Uint8Array {
  const comma = dataUrl.indexOf(",");
  if (comma < 0) return new Uint8Array(0);
  const b64 = dataUrl.slice(comma + 1);
  // atob predates the crypto APIs and is not gated by connect-src, so it works
  // in the sandboxed frame (same rationale as export.ts's stamp path).
  try {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return new Uint8Array(0);
  }
}

/** Resolve the effective (clamped) scale so the backing store stays under the
 *  WebKit silent-blank canvas cap. Returns {scale, dpi} actually used. */
function clampedScale(pageW: number, pageH: number, dpi: number): { scale: number; dpi: number } {
  const scale = dpi / 72;
  const px = pageW * scale * pageH * scale;
  if (px <= MAX_CANVAS_PIXELS) return { scale, dpi };
  // Scale the RENDER down; keep the page box (points) unchanged so the embedded
  // image still fills the page — only its backing DPI drops.
  const k = Math.sqrt(MAX_CANVAS_PIXELS / px);
  return { scale: scale * k, dpi: Math.round(dpi * k) };
}

/** Yield one macrotask so the UI thread stays reactive between heavy renders. */
function yieldToUI(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

export async function rasterizeRedactions(input: RedactInput): Promise<RedactOutput> {
  if (input.sourceBytes.length === 0) throw new Error("no source PDF bytes available for redaction");
  const mime: RedactImageMime = input.mime ?? "image/png";
  const quality = input.quality ?? 0.85;
  const stampReasons = input.stampReasons ?? true;
  const yieldMs = input.yieldMs ?? 16;

  const src = await PDFDocument.load(input.sourceBytes, { ignoreEncryption: true });
  const srcPages = src.getPages();
  const numPages = srcPages.length;

  const redactByPage = new Map<number, Redaction[]>();
  for (const r of input.redactions) {
    const arr = redactByPage.get(r.page);
    if (arr) arr.push(r);
    else redactByPage.set(r.page, [r]);
  }

  const out = await PDFDocument.create();
  const stats: RedactStats = { rasterized: [], copied: [], imageBytes: 0, dpiUsed: {} };
  let lastYield = performance.now();

  for (let i = 0; i < numPages; i++) {
    const page1 = i + 1;
    const srcPage = srcPages[i];
    const srcSize = srcPage.getSize();

    if (redactByPage.has(page1)) {
      const proxy = input.model.getPage(i);
      if (!proxy) {
        // No page proxy (unloaded): fall back to a lossless copy so we never
        // DROP a page. The redaction on it won't apply — the verifier will fail
        // this rect, which is the correct loud outcome.
        const [copied] = await out.copyPages(src, [i]);
        out.addPage(copied);
        stats.copied.push(page1);
      } else {
        const { scale, dpi } = clampedScale(srcSize.width, srcSize.height, input.dpi);
        const viewport = proxy.getViewport({ scale });
        const imgBytes = await renderRedactedPage(proxy, viewport, redactByPage.get(page1)!, mime, quality, stampReasons);
        stats.imageBytes += imgBytes.length;
        const img = mime === "image/png" ? await out.embedPng(imgBytes) : await out.embedJpg(imgBytes);
        // Page box = the viewport's point dimensions (rotation already folded
        // into the rendered image). /Rotate is dropped for redacted pages —
        // they are images now, and the visual is identical in every viewer.
        const pagePtW = viewport.width / scale;
        const pagePtH = viewport.height / scale;
        const newPage = out.addPage([pagePtW, pagePtH]);
        newPage.drawImage(img, { x: 0, y: 0, width: pagePtW, height: pagePtH });
        stats.rasterized.push(page1);
        stats.dpiUsed[page1] = dpi;
      }
    } else {
      const [copied] = await out.copyPages(src, [i]);
      out.addPage(copied);
      stats.copied.push(page1);
    }

    input.onProgress?.(page1, numPages, page1);
    if (performance.now() - lastYield >= yieldMs) {
      await yieldToUI();
      lastYield = performance.now();
    }
  }

  // --- strip the two residuals a fresh rebuild does NOT drop ----------------
  stripInfoDict(out);
  stripAnnotations(out);

  const bytes = await out.save({ useObjectStreams: true });
  return { bytes, stats };
}

async function renderRedactedPage(
  proxy: PDFPageProxy,
  viewport: PageViewport,
  rects: Redaction[],
  mime: RedactImageMime,
  quality: number,
  stampReasons: boolean,
): Promise<Uint8Array> {
  const canvas = document.createElement("canvas");
  canvas.width = Math.ceil(viewport.width);
  canvas.height = Math.ceil(viewport.height);
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("could not get 2d context for rasterize");
  // White background so JPEG (no alpha) and any transparency land on white,
  // matching how the page renders in a viewer.
  ctx.fillStyle = "#ffffff";
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  await proxy.render({ canvas, canvasContext: ctx, viewport }).promise;

  // Paint each redaction rect fully opaque. convertToViewportPoint maps PDF
  // user space (bottom-left) → device pixels (top-left, y-down), the same path
  // coords.ts uses at the CSS edge.
  for (const r of rects) {
    paintRect(ctx, viewport, r.rect, stampReasons ? r.reason : "");
  }

  const dataUrl = mime === "image/png"
    ? canvas.toDataURL("image/png")
    : canvas.toDataURL("image/jpeg", quality);
  return dataUrlToBytes(dataUrl);
}

function paintRect(ctx: CanvasRenderingContext2D, viewport: PageViewport, rect: PdfRect, reason: string): void {
  const [x, y, w, h] = rect;
  const [ax, ay] = viewport.convertToViewportPoint(x, y);
  const [bx, by] = viewport.convertToViewportPoint(x + w, y + h);
  const left = Math.min(ax, bx), top = Math.min(ay, by);
  const rw = Math.abs(bx - ax), rh = Math.abs(by - ay);
  ctx.fillStyle = "#000000";
  ctx.fillRect(left, top, rw, rh);
  if (reason) stampReason(ctx, left, top, rw, rh, reason);
}

/** Stamp the reason label centered in the rect, fitted to width. */
function stampReason(ctx: CanvasRenderingContext2D, left: number, top: number, rw: number, rh: number, reason: string): void {
  const maxFont = Math.min(14, rh / 2);
  if (maxFont < 7) return; // too small to read; skip rather than draw noise
  let size = maxFont;
  const label = reason.length > 60 ? reason.slice(0, 57) + "…" : reason;
  ctx.font = `${size}px sans-serif`;
  let m = ctx.measureText(label);
  // Shrink to fit width with a small margin.
  while (size > 7 && m.width > rw - 6) {
    size -= 1;
    ctx.font = `${size}px sans-serif`;
    m = ctx.measureText(label);
  }
  if (m.width > rw - 6) return; // still doesn't fit; skip
  ctx.fillStyle = "#ffffff";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText(label, left + rw / 2, top + rh / 2);
}

// --- metadata + annotation stripping --------------------------------------

/** Delete the trailer /Info reference AND the orphaned Info object so no stub
 *  survives. (Clearing keys via setTitle("") leaves empty entries behind —
 *  measured; the dict itself must go.) */
function stripInfoDict(doc: PDFDocument): void {
  const infoRef = doc.context.trailerInfo.Info;
  if (!infoRef) return;
  delete doc.context.trailerInfo.Info;
  // The trailer holds a PDFRef to the Info dict; remove the object too so the
  // writer does not emit an orphan carrying old fields.
  if (infoRef instanceof PDFRef) doc.context.delete(infoRef);
}

/** Strip /Annots from every page. An annotation's /Contents is exactly where a
 *  second copy of a sensitive string hides — a planted secret survived a fresh
 *  rebuild in the spike, so this is mandatory in redact mode. */
function stripAnnotations(doc: PDFDocument): void {
  const annotsName = PDFName.of("Annots");
  for (const page of doc.getPages()) {
    try {
      page.node.delete(annotsName);
    } catch {
      /* page may already lack /Annots — best-effort */
    }
  }
}

