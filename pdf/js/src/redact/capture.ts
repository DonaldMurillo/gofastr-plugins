// Ground-truth capture for redaction verification.
//
// THE invariant the whole guarantee rests on: the verifier can only prove a
// string is GONE if we recorded that it was THERE. So before the source bytes
// are touched, for every redaction rect we collect the text items whose bbox
// intersects it. Those captured strings are the needles verify.ts later asserts
// absent from the redacted pages.
//
// Same module also powers the "redact the N other occurrences" assist: given a
// needle, find every line on every page that contains it and is NOT already
// covered by a redaction rect. A user who redacts one instance and misses three
// is the likeliest real-world failure (the spike reproduced exactly that), so
// the UI offers to add rects for all of them.
//
// All geometry is PDF user space (coords.ts); item bboxes come from pdf.js's
// transform + width/height, the same fields verify.ts's hitsRect uses.
import type { PdfModel } from "../pdfdoc";
import type { PdfRect } from "../coords";
import type { Redaction } from "../doc";
import { castTextContent, isTextItem, type PdfTextItem } from "../textlayer";

function itemBbox(it: PdfTextItem): PdfRect {
  const e = it.transform[4] ?? 0;
  const f = it.transform[5] ?? 0;
  return [e, f, it.width || 0, it.height || 0];
}

function rectsOverlap(a: PdfRect, b: PdfRect): boolean {
  return a[0] < b[0] + b[2] && a[0] + a[2] > b[0] && a[1] < b[1] + b[3] && a[1] + a[3] > b[1];
}

/** A meaningful needle: length >= minLen, not whitespace-only. */
function isNeedleWorthy(s: string, minLen = 3): boolean {
  return s.trim().length >= minLen;
}

export interface CapturedRedaction {
  redactionId: string;
  page: number;
  rect: PdfRect;
  /** Distinct captured strings (deduped, ordered by reading order). */
  needles: string[];
  /** A short preview of what was under the rect (for the confirm dialog). */
  preview: string;
}

/**
 * For every redaction rect, capture the text items intersecting it on the
 * SOURCE document (before any modification). Returns one entry per rect.
 * Pages that fail to load contribute an empty needle list (the rect still
 * rasterizes; it just yields no text needle).
 */
export async function captureGroundTruth(
  model: PdfModel,
  redactions: Redaction[],
): Promise<CapturedRedaction[]> {
  const byPage = new Map<number, Redaction[]>();
  for (const r of redactions) {
    const arr = byPage.get(r.page);
    if (arr) arr.push(r);
    else byPage.set(r.page, [r]);
  }
  const out: CapturedRedaction[] = [];
  for (const [page, rects] of byPage) {
    const proxy = model.getPage(page - 1);
    if (!proxy) {
      for (const r of rects) out.push({ redactionId: r.id, page, rect: r.rect, needles: [], preview: "" });
      continue;
    }
    let items: PdfTextItem[];
    try {
      items = castTextContent(await proxy.getTextContent()).items.filter(isTextItem);
    } catch {
      for (const r of rects) out.push({ redactionId: r.id, page, rect: r.rect, needles: [], preview: "" });
      continue;
    }
    // Sort by reading order (top-down by baseline desc, then x asc) so preview
    // reads naturally.
    const ordered = items.slice().sort((a, b) => {
      const ay = a.transform[5] ?? 0, by = b.transform[5] ?? 0;
      if (Math.abs(ay - by) > 2) return by - ay; // higher baseline first
      return (a.transform[4] ?? 0) - (b.transform[4] ?? 0);
    });
    for (const r of rects) {
      const hits = ordered.filter((it) => rectsOverlap(itemBbox(it), r.rect));
      const seen = new Set<string>();
      const needles: string[] = [];
      const previewParts: string[] = [];
      for (const it of hits) {
        const s = it.str;
        if (s && !seen.has(s) && isNeedleWorthy(s)) {
          seen.add(s);
          needles.push(s);
        }
        if (s) previewParts.push(s);
      }
      const preview = previewParts.join(" ").replace(/\s+/g, " ").trim().slice(0, 120);
      out.push({ redactionId: r.id, page, rect: r.rect, needles, preview });
    }
  }
  return out;
}

/** Flatten captured redactions into the distinct needle set for the verifier. */
export function capturedNeedles(captured: CapturedRedaction[]): string[] {
  const seen = new Set<string>();
  for (const c of captured) for (const n of c.needles) seen.add(n);
  return [...seen];
}

export interface OtherOccurrence {
  page: number;
  rect: PdfRect;     // a bbox covering the line(s) containing the needle
  preview: string;
}

/**
 * Find every line on every page containing `needle` that is NOT already under
 * one of `covered` redactions. Powers the "redact the N other occurrences"
 * assist. `pad` grows the derived rect so it fully covers the glyph run.
 */
export async function findOtherOccurrences(
  model: PdfModel,
  needle: string,
  covered: Redaction[],
  pad = 2,
): Promise<OtherOccurrence[]> {
  if (!needle) return [];
  const out: OtherOccurrence[] = [];
  const coveredByPage = new Map<number, PdfRect[]>();
  for (const r of covered) {
    const arr = coveredByPage.get(r.page);
    if (arr) arr.push(r.rect);
    else coveredByPage.set(r.page, [r.rect]);
  }
  for (let i = 0; i < model.pageCount; i++) {
    const page = i + 1;
    const proxy = model.getPage(i);
    if (!proxy) continue;
    let items: PdfTextItem[];
    try {
      items = castTextContent(await proxy.getTextContent()).items.filter(isTextItem);
    } catch {
      continue;
    }
    const withStr = items.filter((it) => it.str);
    // Group items into lines by rounded baseline (transform[5]).
    const byLine = new Map<number, PdfTextItem[]>();
    for (const it of withStr) {
      const f = Math.round(it.transform[5]);
      let arr = byLine.get(f);
      if (!arr) { arr = []; byLine.set(f, arr); }
      arr.push(it);
    }
    const coveredHere = coveredByPage.get(page) ?? [];
    for (const [, lineItems] of byLine) {
      const lineText = lineItems.map((x) => x.str).join("");
      if (!lineText.includes(needle)) continue;
      // Bounding box over the whole line.
      let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
      for (const it of lineItems) {
        const b = itemBbox(it);
        if (b[0] < minX) minX = b[0];
        if (b[1] < minY) minY = b[1];
        if (b[0] + b[2] > maxX) maxX = b[0] + b[2];
        if (b[1] + b[3] > maxY) maxY = b[1] + b[3];
      }
      const lineRect: PdfRect = [minX - pad, minY - pad, (maxX - minX) + 2 * pad, (maxY - minY) + 2 * pad];
      // Skip lines already fully covered by an existing redaction on this page.
      const already = coveredHere.some((r) => rectsOverlap(r, lineRect));
      if (already) continue;
      out.push({ page, rect: lineRect, preview: lineText.replace(/\s+/g, " ").trim().slice(0, 120) });
    }
  }
  return out;
}
