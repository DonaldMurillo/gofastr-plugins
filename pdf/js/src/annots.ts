// Annotation layer — link annotations rendered as accessible overlays.
//
// The frame CANNOT navigate (sandbox="allow-scripts", no allow-popups,
// connect-src 'none'). So:
//   - INTERNAL links (same-document /GoTo dest): work as in-view jumps. Clicking
//     resolves the dest to a page number and scrolls to it.
//   - EXTERNAL links (URI action): rendered INERT. Navigation is prevented; the
//     target is conveyed via title + aria-label + a distinct (dashed) style so
//     the inert-ness and destination are visible and accessible. Never throws.
//
// Rects are converted from PDF space to viewport space via
// viewport.convertToViewportPoint (works at any zoom/rotation).

import type { PDFPageProxy, PageViewport } from "pdfjs-dist";
import { el } from "./dom";

export interface PdfAnnotation {
  subtype?: string;
  rect?: number[];                        // [x1, y1, x2, y2] in PDF user space
  dest?: string | unknown[] | null;       // internal destination
  url?: string | null;                    // external URI
  unsafeUrl?: string | null;
}

export interface AnnotLayerResult {
  internalCount: number;
  externalCount: number;
}

export async function buildAnnotLayer(
  page: PDFPageProxy,
  viewport: PageViewport,
  layer: HTMLElement,
  onInternal: (dest: string | unknown[]) => void
): Promise<AnnotLayerResult> {
  layer.replaceChildren();
  let annots: PdfAnnotation[] = [];
  try {
    annots = (await page.getAnnotations({ intent: "display" })) as unknown as PdfAnnotation[];
  } catch {
    return { internalCount: 0, externalCount: 0 };
  }
  let internalCount = 0;
  let externalCount = 0;
  for (const a of annots) {
    if (a.subtype !== "Link" || !a.rect || a.rect.length < 4) continue;
    const c1: number[] = viewport.convertToViewportPoint(a.rect[0], a.rect[1]);
    const c2: number[] = viewport.convertToViewportPoint(a.rect[2], a.rect[3]);
    const left = Math.min(c1[0], c2[0]);
    const top = Math.min(c1[1], c2[1]);
    const w = Math.abs(c2[0] - c1[0]);
    const h = Math.abs(c2[1] - c1[1]);
    const dest = a.dest;
    const externalUrl = a.url || a.unsafeUrl || null;
    const isExternal = !dest && !!externalUrl;
    if (isExternal) externalCount++;
    else internalCount++;
    const label = isExternal
      ? "External link (opens outside the viewer): " + externalUrl
      : "Internal link — go to destination";
    const link = el("a", {
      cls: "pdf-annot-link" + (isExternal ? " is-external" : " is-internal"),
      attrs: { href: "#", tabindex: "0" },
      title: label,
      ariaLabel: label,
      style: { left: left + "px", top: top + "px", width: w + "px", height: h + "px" },
      on: {
        click: (e) => {
          e.preventDefault();
          if (!isExternal && dest != null) onInternal(dest);
        },
        keydown: (e) => {
          if ((e as KeyboardEvent).key === "Enter" || (e as KeyboardEvent).key === " ") {
            e.preventDefault();
            if (!isExternal && dest != null) onInternal(dest);
          }
        },
      },
    });
    // data-url gives CSS/AT a stable hook to the (inert) external target.
    if (isExternal && externalUrl) link.setAttribute("data-url", externalUrl);
    layer.appendChild(link);
  }
  return { internalCount, externalCount };
}
