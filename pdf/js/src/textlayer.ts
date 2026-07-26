// Text layer — the selectable, a11y text overlay over each page canvas.
//
// One <span> per pdf.js text item, positioned with
// Util.transform(viewport.transform, item.transform) and oriented to the text's
// device-space angle so it tracks BOTH zoom and view rotation. The canvas shows
// the glyphs; this layer is transparent text providing hit-testing/selection
// (and the screen-reader surface). Returns the readable page text (space-joined)
// for the page-1 mirror contract, plus span refs the search controller uses to
// highlight the active match.

import { Util } from "pdfjs-dist";
import type { PageViewport } from "pdfjs-dist";
import { el } from "./dom";

export interface PdfTextItem {
  str: string;
  transform: number[];
  width: number;
  height: number;
  hasEOL: boolean;
}
export interface PdfTextContent {
  items: Array<PdfTextItem | { type: string; id: string }>;
}

export interface TextSpanRef {
  span: HTMLSpanElement;
  str: string;
}

export function isTextItem(item: PdfTextItem | { type: string; id: string }): item is PdfTextItem {
  return "str" in item;
}

export function castTextContent(tc: { items: Array<{ str?: unknown; transform?: unknown; width?: unknown; type?: unknown }> }): PdfTextContent {
  return tc as unknown as PdfTextContent;
}

// Build (or rebuild) the overlay. Re-running this on zoom/rotation change is
// how the layer stays aligned to the canvas at every scale/orientation.
export function buildTextLayer(
  tc: PdfTextContent,
  viewport: PageViewport,
  layer: HTMLElement
): { text: string; spans: TextSpanRef[] } {
  layer.replaceChildren();
  const parts: string[] = [];
  const spans: TextSpanRef[] = [];
  for (const item of tc.items) {
    if (!isTextItem(item)) continue;
    if (item.str.length === 0) continue;
    parts.push(item.str);
    const tx = Util.transform(viewport.transform, item.transform);
    const fontHeight = Math.hypot(tx[2], tx[3]) || 1;
    const deg = (Math.atan2(tx[1], tx[0]) * 180) / Math.PI;
    // Dynamic positioning only (CSS owns position:absolute, white-space, etc.).
    const span = el("span", {
      text: item.str,
      style: {
        left: tx[4] + "px",
        top: tx[5] - fontHeight + "px",
        "font-size": fontHeight + "px",
        "transform-origin": "0 0",
      },
    });
    layer.appendChild(span);
    // offsetWidth is the untransformed natural width; scale horizontally only
    // for roughly-horizontal text so selection boxes match glyph runs. Rotated
    // text keeps correct position/orientation; its selection box is approximate.
    const measured = span.offsetWidth;
    let transform = "rotate(" + deg + "deg)";
    if (measured > 0) {
      const norm = ((deg % 180) + 180) % 180;
      const horizontal = norm < 12 || norm > 168;
      if (horizontal) {
        const targetW = item.width * viewport.scale;
        transform += " scaleX(" + targetW / measured + ")";
      }
    }
    span.style.transform = transform;
    spans.push({ span, str: item.str });
  }
  return { text: parts.join(" "), spans };
}

// Reset a span's text to its original string, clearing any search <mark> wrap.
// Style attributes are untouched (only child nodes are replaced).
export function resetSpanText(ref: TextSpanRef): void {
  ref.span.textContent = ref.str;
}
