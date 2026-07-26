// EditToolbar — the P2 annotation toolbar.
//
// Separate from the viewer's Toolbar (nav/zoom/search): this owns the tool
// palette, the style controls (colour + stroke), undo/redo with correct
// disabled states, the flatten-on-export toggle, and the export button.
//
// Every control is built imperatively with explicit role + aria-label + visible
// focus ring (the repo gates on axe with zero serious/critical). Tool buttons
// are aria-pressed toggles; only one is pressed at a time. Tap targets are
// ≥44px on coarse pointers via CSS (.pdf-edit-btn).

import { el } from "./dom";
import type { AnnotationType } from "./doc";

export type ToolId = "select" | AnnotationType;

export interface EditStyle {
  color: string;
  width: number;     // PDF points
  opacity: number;   // 0..1 (fill+stroke for shapes; overlay for highlight)
}

export interface EditToolbarCallbacks {
  onTool: (tool: ToolId) => void;
  onStyle: (s: EditStyle) => void;
  onUndo: () => void;
  onRedo: () => void;
  onExport: (flatten: boolean) => void;
  onPageOp: (op: "rotate" | "delete" | "insert") => void;
  canExport: () => boolean;
}

const DEFAULT_TOOLS: Array<{ id: ToolId; label: string; glyph: string }> = [
  { id: "select", label: "Select", glyph: "▽" },
  { id: "highlight", label: "Highlight", glyph: "▯" },
  { id: "ink", label: "Draw", glyph: "✎" },
  { id: "rect", label: "Rectangle", glyph: "▭" },
  { id: "ellipse", label: "Ellipse", glyph: "◯" },
  { id: "arrow", label: "Arrow", glyph: "→" },
  { id: "text", label: "Text", glyph: "T" },
  { id: "note", label: "Note", glyph: "▣" },
  { id: "stamp", label: "Stamp", glyph: "★" },
];

const COLOR_PRESETS = ["#FFEB3B", "#FF5252", "#2196F3", "#4CAF50", "#FF9800", "#000000"];
const WIDTH_PRESETS = [1.5, 3, 6];

export class EditToolbar {
  readonly root: HTMLElement;
  private readonly toolBtns = new Map<ToolId, HTMLButtonElement>();
  private readonly undoBtn: HTMLButtonElement;
  private readonly redoBtn: HTMLButtonElement;
  private readonly exportBtn: HTMLButtonElement;
  private readonly flattenCheckbox: HTMLInputElement;
  private readonly cb: EditToolbarCallbacks;
  private current: ToolId = "select";
  private style: EditStyle = { color: "#FFEB3B", width: 3, opacity: 0.35 };

  constructor(cb: EditToolbarCallbacks) {
    this.cb = cb;

    // Tools group — toggles, one pressed at a time.
    const toolGroup = el("div", { cls: "pdf-tool-group", role: "group", ariaLabel: "Annotation tools" });
    for (const t of DEFAULT_TOOLS) {
      const btn = el("button", {
        cls: "pdf-edit-btn pdf-tool-btn",
        type: "button",
        text: t.glyph,
        title: t.label,
        ariaLabel: t.label,
        ariaPressed: t.id === "select",
        on: {
          click: () => this.chooseTool(t.id),
          keydown: (e) => {
            if ((e as KeyboardEvent).key === "Enter" || (e as KeyboardEvent).key === " ") {
              e.preventDefault();
              this.chooseTool(t.id);
            }
          },
        },
      }) as HTMLButtonElement;
      // Glyph + a visually-hidden text label for AT (the glyph is decorative).
      btn.appendChild(el("span", { cls: "pdf-visually-hidden", text: t.label }));
      this.toolBtns.set(t.id, btn);
      toolGroup.appendChild(btn);
    }

    // Style group — colour presets + stroke width presets.
    const styleGroup = el("div", { cls: "pdf-tool-group", role: "group", ariaLabel: "Annotation style" });
    for (const c of COLOR_PRESETS) {
      const sw = el("button", {
        cls: "pdf-edit-btn pdf-color-swatch",
        type: "button",
        title: "Colour " + c,
        ariaLabel: "Colour " + c,
        style: { background: c },
        attrs: { "data-color": c },
        on: { click: () => this.setStyle({ ...this.style, color: c, opacity: this.current === "highlight" ? 0.35 : 1 }) },
      });
      styleGroup.appendChild(sw);
    }
    for (const w of WIDTH_PRESETS) {
      const wb = el("button", {
        cls: "pdf-edit-btn pdf-width-btn",
        type: "button",
        text: String(w),
        title: "Stroke " + w + " pt",
        ariaLabel: "Stroke width " + w + " points",
        on: { click: () => this.setStyle({ ...this.style, width: w }) },
      });
      styleGroup.appendChild(wb);
    }

    // History group — undo/redo, disabled states driven by syncDocState.
    const histGroup = el("div", { cls: "pdf-tool-group", role: "group", ariaLabel: "History" });
    this.undoBtn = el("button", {
      cls: "pdf-edit-btn", type: "button", text: "↶", title: "Undo (Ctrl+Z)",
      ariaLabel: "Undo", disabled: true,
      on: { click: () => this.cb.onUndo() },
    }) as HTMLButtonElement;
    this.redoBtn = el("button", {
      cls: "pdf-edit-btn", type: "button", text: "↷", title: "Redo (Ctrl+Shift+Z)",
      ariaLabel: "Redo", disabled: true,
      on: { click: () => this.cb.onRedo() },
    }) as HTMLButtonElement;
    histGroup.appendChild(this.undoBtn);
    histGroup.appendChild(this.redoBtn);

    // Pages group — rotate / delete / insert-blank on the current page. These
    // accumulate in doc.pageOps and apply at export; the viewer reflects them
    // live (rotate re-renders, delete removes the slot).
    const pagesGroup = el("div", { cls: "pdf-tool-group", role: "group", ariaLabel: "Page operations" });
    const rotBtn = el("button", {
      cls: "pdf-edit-btn", type: "button", text: "⟳", title: "Rotate page 90°",
      ariaLabel: "Rotate page 90 degrees",
      on: { click: () => this.cb.onPageOp("rotate") },
    });
    const delBtn = el("button", {
      cls: "pdf-edit-btn", type: "button", text: "🗑", title: "Delete current page",
      ariaLabel: "Delete current page",
      on: { click: () => this.cb.onPageOp("delete") },
    });
    const insBtn = el("button", {
      cls: "pdf-edit-btn", type: "button", text: "＋", title: "Insert blank page after current",
      ariaLabel: "Insert blank page after current",
      on: { click: () => this.cb.onPageOp("insert") },
    });
    pagesGroup.appendChild(rotBtn);
    pagesGroup.appendChild(delBtn);
    pagesGroup.appendChild(insBtn);
    // Export group — flatten toggle + export button.
    const expGroup = el("div", { cls: "pdf-tool-group", role: "group", ariaLabel: "Export" });
    const flattenLabel = el("label", { cls: "pdf-flatten-label", text: "" });
    this.flattenCheckbox = el("input", { type: "checkbox", attrs: { checked: "checked" } }) as HTMLInputElement;
    this.flattenCheckbox.checked = true;
    flattenLabel.appendChild(this.flattenCheckbox);
    flattenLabel.appendChild(el("span", { text: " Flatten (bake into page)" }));
    expGroup.appendChild(flattenLabel);
    this.exportBtn = el("button", {
      cls: "pdf-edit-btn pdf-export-btn", type: "button", text: "Export PDF",
      title: "Produce a PDF with annotations and form values",
      ariaLabel: "Export PDF",
      on: { click: () => this.cb.onExport(this.flattenCheckbox.checked) },
    }) as HTMLButtonElement;
    expGroup.appendChild(this.exportBtn);

    this.root = el("div", { cls: "pdf-edit-toolbar", role: "toolbar", ariaLabel: "PDF annotation" }, [
      toolGroup, styleGroup, histGroup, pagesGroup, expGroup,
    ]);
  }

  chooseTool(t: ToolId): void {
    if (this.current === t) return;
    this.current = t;
    for (const [id, btn] of this.toolBtns) {
      btn.setAttribute("aria-pressed", String(id === t));
    }
    this.cb.onTool(t);
  }

  setStyle(s: EditStyle): void {
    this.style = s;
    this.cb.onStyle(s);
  }

  getStyle(): EditStyle { return this.style; }
  getCurrentTool(): ToolId { return this.current; }

  /** Drive disabled states from the live doc state. */
  syncState(snap: { canUndo: boolean; canRedo: boolean; canExport: boolean }): void {
    this.undoBtn.disabled = !snap.canUndo;
    this.redoBtn.disabled = !snap.canRedo;
    this.exportBtn.disabled = !snap.canExport;
    if (!snap.canExport) {
      this.exportBtn.title = "Export is not granted for this mount";
    } else {
      this.exportBtn.title = "Produce a PDF with annotations and form values";
    }
  }
}
