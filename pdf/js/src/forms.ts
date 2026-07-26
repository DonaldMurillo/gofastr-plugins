// AcroForm field enumeration + in-frame fill UI.
//
// Enumerates existing form-field widgets via pdf.js (the renderer already
// parses /AcroForm widget annotations), renders accessible inputs positioned
// in page space over the rendered page, and writes values into the overlay's
// formFields map (one SetFormFieldCommand per change). At export, pdf-lib
// writes those values into the real AcroForm — see export.ts fillFormFields.
//
// We enumerate from pdf.js (not pdf-lib) because the inputs must be positioned
// over the RENDERED page, and pdf.js already knows each widget's rect in the
// viewport's coordinate system. pdf-lib owns the write path.

import type { PDFPageProxy, PageViewport } from "pdfjs-dist";
import { el } from "./dom";
import { pdfRectToCss, type ViewportLike } from "./coords";
import type { OverlayDoc } from "./doc";
import { SetFormFieldCommand } from "./doc";

export interface PdfWidget {
  subtype?: string;        // "Widget"
  fieldType?: string;      // "Tx" (text) | "Btn" (checkbox/radio) | "Ch" (choice)
  fieldName?: string;      // fully-qualified AcroForm name
  fieldValue?: unknown;    // current value
  rect?: number[];         // PDF user space [x1, y1, x2, y2]
  checkBoxFlags?: number;
  buttonLabel?: string;
}

export interface FormLayerHost {
  doc: OverlayDoc;
}

// Build (or rebuild) the form-input layer for one page. Reads the live overlay
// values so a re-render after a fill shows the user's value, not the file's.
export async function buildFormLayer(
  page: PDFPageProxy,
  viewport: PageViewport,
  layer: HTMLElement,
  host: FormLayerHost,
): Promise<number> {
  layer.replaceChildren();
  let widgets: PdfWidget[] = [];
  try {
    widgets = (await page.getAnnotations({ intent: "display" })) as unknown as PdfWidget[];
  } catch {
    return 0;
  }
  const vp = viewport as ViewportLike;
  const doc = host.doc;
  let count = 0;
  for (const w of widgets) {
    if (w.subtype !== "Widget" || !w.fieldName || !w.rect || w.rect.length < 4) continue;
    const rect4: [number, number, number, number] = [w.rect[0], w.rect[1], w.rect[2], w.rect[3]];
    // pdf.js widget rects are [x1,y1,x2,y2]; coords.ts wants [x,y,w,h].
    const r: [number, number, number, number] = [
      Math.min(rect4[0], rect4[2]),
      Math.min(rect4[1], rect4[3]),
      Math.abs(rect4[2] - rect4[0]),
      Math.abs(rect4[3] - rect4[1]),
    ];
    const box = pdfRectToCss(vp, r);
    const live = doc.state.formFields.get(w.fieldName);
    const input = renderInput(w, live?.v ?? w.fieldValue, (val) => {
      doc.apply(new SetFormFieldCommand(w.fieldName!, val));
    });
    input.style.left = box.left + "px";
    input.style.top = box.top + "px";
    input.style.width = Math.max(40, box.width) + "px";
    input.style.height = Math.max(20, box.height) + "px";
    layer.appendChild(input);
    count++;
  }
  return count;
}

function renderInput(w: PdfWidget, value: unknown, onChange: (v: unknown) => void): HTMLElement {
  const name = w.fieldName ?? "field";
  const ariaLabel = "Form field " + name;
  if (w.fieldType === "Btn") {
    // Checkbox (radio needs an option name; treated as checkbox here).
    const cb = el("input", {
      type: "checkbox",
      cls: "pdf-form-input pdf-form-checkbox",
      ariaLabel,
      checked: value === true || value === "Yes" || value === "On" || value === true,
    }) as HTMLInputElement;
    if (value === true || value === "Yes" || value === "On") cb.checked = true;
    cb.addEventListener("change", () => onChange(cb.checked));
    return cb;
  }
  if (w.fieldType === "Ch") {
    // Choice / dropdown — render as a text input (pdf-lib selects by value).
    const inp = el("input", {
      type: "text",
      cls: "pdf-form-input pdf-form-text",
      ariaLabel,
    }) as HTMLInputElement;
    inp.value = typeof value === "string" ? value : value == null ? "" : String(value);
    inp.addEventListener("change", () => onChange(inp.value));
    return inp;
  }
  // Default: text field (Tx).
  const inp = el("input", {
    type: "text",
    cls: "pdf-form-input pdf-form-text",
    ariaLabel,
  }) as HTMLInputElement;
  inp.value = typeof value === "string" ? value : value == null ? "" : String(value);
  inp.addEventListener("change", () => onChange(inp.value));
  return inp;
}
