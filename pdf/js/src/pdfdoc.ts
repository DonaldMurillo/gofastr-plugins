// PdfModel — wraps pdf.js's getDocument with the no-wasm options the framed CSP
// demands, and provides an incrementally-loaded page cache + geometry + outline
// destination resolution for the viewer.
//
// LOAD-BEARING OPTIONS (do not change): useWasm:false routes JPEG 2000 / JBIG2
// through pdf.js's PURE-JS fallbacks (statically inlined by build.mjs's
// pdfjsNoWasmFallbackPlugin). Removing it re-enables a wasm path that cannot
// instantiate under script-src without 'wasm-unsafe-eval', and whose fallback
// is a dynamic import() an opaque origin can never satisfy — leaving SCANNED
// pages blank with no error. useSystemFonts:true avoids standardFontDataUrl
// fetches (impossible under connect-src 'none'). verbosity:0 keeps the console
// clean (the gate asserts ZERO console messages).

import { getDocument } from "pdfjs-dist";
import type { PDFDocumentProxy, PDFPageProxy, PageViewport } from "pdfjs-dist";

// Minimal shape of a pdf.js indirect object reference ({ num, gen }). pdf.js
// does not re-export its full RefProxy type from the package root, so we model
// just the fields we read at this boundary.
interface PdfRef {
  num: number;
  gen: number;
}

export interface PageGeom {
  pageNumber: number;
  cssW: number; // CSS pixels (viewport.width)
  cssH: number; // CSS pixels (viewport.height)
  rotation: number; // effective rotation applied (page.rotate + userRotation)
}

export interface OutlineNode {
  title: string;
  bold: boolean;
  italic: boolean;
  dest: string | unknown[] | null;
  url: string | null;
  items: OutlineNode[];
}

export class PdfModel {
  readonly pdf: PDFDocumentProxy;
  readonly pageCount: number;
  private readonly pages: Array<PDFPageProxy | null>;

  constructor(pdf: PDFDocumentProxy) {
    this.pdf = pdf;
    this.pageCount = pdf.numPages;
    this.pages = new Array(pdf.numPages).fill(null);
  }

  // Load all page proxies incrementally, yielding to the event loop between
  // pages so a large document never freezes the main thread (no workers are
  // available under the framed CSP). onProgress(i, n) drives the loading UI.
  async loadAllPages(onProgress: (loaded: number, total: number) => void): Promise<void> {
    for (let i = 0; i < this.pageCount; i++) {
      this.pages[i] = await this.pdf.getPage(i + 1);
      onProgress(i + 1, this.pageCount);
      if (i + 1 < this.pageCount) await new Promise<void>((r) => setTimeout(r, 0));
    }
  }

  getPage(i: number): PDFPageProxy | null {
    return this.pages[i] ?? null;
  }

  // Effective rotation combining the page's inherent rotation and the user's
  // view-only rotation, normalized to [0,360).
  effectiveRotation(i: number, userRotation: number): number {
    const p = this.pages[i];
    const r = p ? p.rotate : 0;
    return (((r + userRotation) % 360) + 360) % 360;
  }

  // Display geometry at scale + userRotation. getViewport already folds
  // rotation into width/height, so callers get the rotated CSS box directly.
  geom(i: number, scale: number, userRotation: number): PageGeom {
    const p = this.pages[i];
    if (!p) return { pageNumber: i + 1, cssW: 0, cssH: 0, rotation: 0 };
    const rotation = this.effectiveRotation(i, userRotation);
    const vp = p.getViewport({ scale, rotation });
    return { pageNumber: i + 1, cssW: vp.width, cssH: vp.height, rotation };
  }

  viewportFor(i: number, scale: number, userRotation: number): PageViewport {
    const p = this.pages[i];
    if (!p) throw new Error("page " + (i + 1) + " not loaded");
    return p.getViewport({ scale, rotation: this.effectiveRotation(i, userRotation) });
  }

  async getOutline(): Promise<OutlineNode[]> {
    const raw = await this.pdf.getOutline();
    return raw ? (raw as OutlineNode[]) : [];
  }

  // Resolve a link/outline destination to a 1-based page number, or null.
  // `dest` may be a named-destination string, an explicit dest array, or null.
  async resolveDest(dest: string | unknown[] | null): Promise<number | null> {
    if (dest == null) return null;
    let explicit: unknown[] | null = null;
    if (typeof dest === "string") explicit = await this.pdf.getDestination(dest);
    else if (Array.isArray(dest)) explicit = dest;
    if (!explicit || explicit.length === 0) return null;
    const ref = explicit[0];
    if (ref && typeof ref === "object" && "num" in (ref as object)) {
      try {
        const idx = await this.pdf.getPageIndex(ref as PdfRef);
        return idx + 1;
      } catch {
        return null;
      }
    }
    return null;
  }

  cleanup(): Promise<unknown> {
    return this.pdf.cleanup();
  }
}

export async function loadDocument(bytes: Uint8Array): Promise<PdfModel> {
  const task = getDocument({
    data: bytes,
    useSystemFonts: true,
    useWasm: false,
    disableAutoFetch: true,
    disableStream: true,
    verbosity: 0,
  });
  const pdf = await task.promise;
  return new PdfModel(pdf);
}
