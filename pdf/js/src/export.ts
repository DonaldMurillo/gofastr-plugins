// Export — the pdf-lib write path.
//
// Produces a new PDF from the source bytes + the overlay: applies page ops,
// draws annotations into page content, fills AcroForm fields, and returns the
// bytes. Redaction is P3 and not built here; the seam (redactDPI) is kept for
// that future path.
//
// The frame CANNOT download (sandbox forbids it). The returned bytes leave via
// a requestExport bridge event the host relays to POST /export; this module
// only produces them.

import { PDFDocument, StandardFonts, rgb, degrees } from "pdf-lib";
import type { PDFFont, PDFPage } from "pdf-lib";
import type { PdfModel } from "./pdfdoc";
import type { OverlayState, Annotation, PageOp, FormValue } from "./doc";

export interface ExportInput {
  sourceBytes: Uint8Array;
  model: PdfModel;
  overlay: OverlayState;
  flatten: boolean;
  redactDPI: number;   // seam for P3 redaction; unused here
}

export interface ExportReport {
  pages: number;
  annotationsDrawn: number;
  fieldsFilled: number;
  flattened: boolean;
}

export async function produceExportBytes(input: ExportInput): Promise<Uint8Array> {
  return (await produceExport(input)).bytes;
}

export async function produceExport(input: ExportInput): Promise<{ bytes: Uint8Array; report: ExportReport }> {
  if (input.sourceBytes.length === 0) {
    throw new Error("no source PDF bytes available for export");
  }
  const doc = await PDFDocument.load(input.sourceBytes, { ignoreEncryption: true });
  const helv = await doc.embedFont(StandardFonts.Helvetica);

  // 1. Page ops (rotate / delete / move / insert-blank / append-blank).
  await applyPageOps(doc, input.overlay.pageOps);

  // 2. Draw annotations. pdf-lib pages are 0-indexed; overlay pages are 1-based.
  //    A page that no longer exists (post-delete) is skipped — its annotations
  //    went with it. Per-annotation errors are swallowed so one bad apple cannot
  //    sink the export.
  let drawn = 0;
  for (const ann of input.overlay.annotations) {
    const page = doc.getPage(ann.page - 1);
    if (!page) continue;
    try {
      await drawAnnotation(doc, page, ann, helv);
      drawn++;
    } catch {
      // best-effort
    }
  }

  // 3. Fill form fields (text/checkbox/dropdown; radio needs an option name).
  const filled = fillFormFields(doc, input.overlay.formFields);

  // 4. Flatten: bake form appearances into page content. Annotations are
  //    already drawn as page content (v1 does not emit real PDF annotation
  //    dicts; that keeps the output predictable across viewers). Flatten is
  //    delegated to pdf-lib's form.flatten().
  if (input.flatten && filled > 0) {
    try {
      const form = doc.getForm();
      form.updateFieldAppearances();
      try { form.flatten(); } catch { /* some fields resist flatten */ }
    } catch { /* no form */ }
  }

  const out = await doc.save({ useObjectStreams: true });
  return {
    bytes: out,
    report: { pages: doc.getPageCount(), annotationsDrawn: drawn, fieldsFilled: filled, flattened: input.flatten },
  };
}

// --- page operations -------------------------------------------------------

async function applyPageOps(doc: PDFDocument, ops: PageOp[]): Promise<void> {
  // Operate on the CURRENT index space at each step — the natural semantics
  // for an append-only op list the user authored against the live view.
  for (const op of ops) {
    const idx = op.page - 1;
    switch (op.op) {
      case "rotate": {
        const p = doc.getPage(idx);
        if (p) {
          const cur = p.getRotation().angle;
          p.setRotation(degrees((cur + op.value) % 360));
        }
        break;
      }
      case "delete": {
        if (idx >= 0 && idx < doc.getPageCount()) doc.removePage(idx);
        break;
      }
      case "move": {
        // removePage returns void; copy the page, remove the original, then
        // insert the copy at the adjusted destination.
        const from = idx;
        const to = op.value - 1;
        const count = doc.getPageCount();
        if (from < 0 || from >= count || to < 0 || to >= count || from === to) break;
        const [copy] = await doc.copyPages(doc, [from]);
        doc.removePage(from);
        const insertAt = from < to ? to - 1 : to;
        doc.insertPage(insertAt, copy);
        break;
      }
      case "insert": {
        // Blank page (A4 default) at idx; value reserved for a future
        // "insert N" / source-doc id.
        doc.insertPage(idx, [595, 842]);
        break;
      }
      case "append": {
        // Append a blank page. External-PDF append needs staged bytes that do
        // not fit the small overlay JSON — a documented v1 limitation; the op
        // is the seam.
        doc.addPage([595, 842]);
        break;
      }
    }
  }
}

// --- annotation drawing ----------------------------------------------------

function hexToRgb(hex: string): ReturnType<typeof rgb> {
  let h = hex.replace(/^#/, "");
  if (h.length === 3) h = h.split("").map((c) => c + c).join("");
  if (h.length !== 6 || /[^0-9a-fA-F]/.test(h)) return rgb(0, 0, 0);
  const r = parseInt(h.slice(0, 2), 16) / 255;
  const g = parseInt(h.slice(2, 4), 16) / 255;
  const b = parseInt(h.slice(4, 6), 16) / 255;
  return rgb(r, g, b);
}

async function drawAnnotation(doc: PDFDocument, page: PDFPage, a: Annotation, font: PDFFont): Promise<void> {
  const [x, y, w, h] = a.rect;
  switch (a.type) {
    case "highlight": {
      const color = hexToRgb(a.color);
      for (const q of a.quads) {
        const qx = Math.min(q.x1, q.x2, q.x3, q.x4);
        const qy = Math.min(q.y1, q.y2, q.y3, q.y4);
        const qw = Math.max(q.x1, q.x2, q.x3, q.x4) - qx;
        const qh = Math.max(q.y1, q.y2, q.y3, q.y4) - qy;
        page.drawRectangle({ x: qx, y: qy, width: qw, height: qh, color, opacity: a.opacity });
      }
      break;
    }
    case "ink": {
      const color = hexToRgb(a.color);
      for (let i = 1; i < a.points.length; i++) {
        const p0 = a.points[i - 1], p1 = a.points[i];
        page.drawLine({
          start: { x: p0[0], y: p0[1] }, end: { x: p1[0], y: p1[1] },
          thickness: a.width, color,
        });
      }
      break;
    }
    case "rect": {
      page.drawRectangle({
        x, y, width: w, height: h,
        borderColor: hexToRgb(a.color), borderWidth: a.width,
        color: a.fill ? hexToRgb(a.fill) : undefined,
        opacity: a.opacity, borderOpacity: a.opacity,
      });
      break;
    }
    case "ellipse": {
      page.drawEllipse({
        x: x + w / 2, y: y + h / 2, xScale: w / 2, yScale: h / 2,
        borderColor: hexToRgb(a.color), borderWidth: a.width,
        color: a.fill ? hexToRgb(a.fill) : undefined,
        opacity: a.opacity, borderOpacity: a.opacity,
      });
      break;
    }
    case "arrow": {
      const color = hexToRgb(a.color);
      page.drawLine({ start: { x, y }, end: { x: x + w, y: y + h }, thickness: a.width, color });
      // Triangular head as two short lines (no SVG dependency needed).
      const hs = Math.max(6, a.width * 3);
      const ang = Math.atan2(h, w);
      const tip = { x: x + w, y: y + h };
      const l = { x: tip.x - hs * Math.cos(ang - 0.4), y: tip.y - hs * Math.sin(ang - 0.4) };
      const r2 = { x: tip.x - hs * Math.cos(ang + 0.4), y: tip.y - hs * Math.sin(ang + 0.4) };
      page.drawLine({ start: tip, end: l, thickness: a.width, color });
      page.drawLine({ start: tip, end: r2, thickness: a.width, color });
      break;
    }
    case "text": {
      const color = hexToRgb(a.color);
      const size = a.fontSize;
      const lines = a.text.split("\n");
      for (let i = 0; i < lines.length; i++) {
        page.drawText(lines[i], {
          x, y: y + h - size - i * size * 1.2, size, font, color, maxWidth: w,
        });
      }
      break;
    }
    case "note": {
      // Anchor icon rect so the note's position is visible. The popup text is
      // a UI affordance; a real PDF popup annotation is deferred.
      page.drawRectangle({ x, y, width: Math.max(12, w), height: Math.max(12, h), color: hexToRgb(a.color) });
      break;
    }
    case "stamp": {
      const bytes = dataUrlToBytes(a.data);
      if (bytes.length === 0) break;
      let img;
      try {
        img = a.mime === "image/jpeg" ? await doc.embedJpg(bytes) : await doc.embedPng(bytes);
      } catch {
        break;
      }
      page.drawImage(img, { x, y, width: w, height: h });
      break;
    }
  }
}

function dataUrlToBytes(dataUrl: string): Uint8Array {
  const comma = dataUrl.indexOf(",");
  if (comma < 0) return new Uint8Array(0);
  const b64 = dataUrl.slice(comma + 1);
  // atob predates the crypto APIs and is not gated by connect-src, so it works
  // in the sandboxed frame.
  try {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return new Uint8Array(0);
  }
}

// --- form filling ----------------------------------------------------------
//
// pdf-lib's form API is typed by field subclass; the wrong setter throws. We
// probe each field's concrete class via constructor name and call the matching
// setter, tolerating any error per-field.

function fillFormFields(doc: PDFDocument, values: Map<string, FormValue>): number {
  let form;
  try { form = doc.getForm(); } catch { return 0; }
  let filled = 0;
  for (const [name, val] of values) {
    let field;
    try { field = form.getField(name); } catch { continue; }
    if (!field) continue;
    const raw = val.v;
    const typeName: string = (field.constructor as { name?: string }).name ?? "";
    try {
      if (typeof raw === "boolean") {
        if (typeName === "PDFCheckBox") {
          const cb = field as unknown as { check: () => void; uncheck: () => void };
          raw ? cb.check() : cb.uncheck();
          filled++;
        }
        // PDFRadioGroup needs an option name, not a bare bool — skip.
      } else {
        const s = typeof raw === "string" ? raw : String(raw);
        if (typeName === "PDFTextField" || typeName === "PDFTextArea") {
          (field as unknown as { setText: (s: string) => void }).setText(s);
          filled++;
        } else if (typeName === "PDFDropdown") {
          (field as unknown as { select: (s: string) => void }).select(s);
          filled++;
        } else if (typeName === "PDFRadioGroup") {
          (field as unknown as { select: (s: string) => void }).select(s);
          filled++;
        }
      }
    } catch {
      // field type mismatch or read-only — skip.
    }
  }
  return filled;
}
