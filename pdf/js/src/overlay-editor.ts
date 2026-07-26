// Overlay editor — the P2 annotation surface.
//
// Owns: the tool palette state, the per-page annotation rendering into each
// page's editLayer, single-selection with resize/move handles, pointer-driven
// creation gestures for every tool, keyboard shortcuts (undo/redo, delete,
// escape, tool cycling), and the image/signature picker modal.
//
// All geometry authored here is PDF user space (via coords.ts); CSS pixels
// appear only at the pointer edge and the paint edge. The editor never touches
// the bridge directly — mutations go through OverlayDoc commands, and the
// viewer owns the host-facing emit (debounced docChanged, requestExport).

import type { PageViewport, PDFPageProxy } from "pdfjs-dist";
import type { PdfModel } from "./pdfdoc";
import { el } from "./dom";
import {
  AddAnnotationCommand, RemoveAnnotationCommand, UpdateAnnotationCommand,
  AddPageOpCommand, AddRedactionCommand, RemoveRedactionCommand, UpdateRedactionCommand,
  BatchCommand, cryptoId,
} from "./doc";
import type { OverlayDoc, Annotation, AnnId, AnnotationType, Redaction } from "./doc";
import {
  pdfRectToCss, pdfQuadToCss, cssDragToPdfRect, cssPointToPdf, cssRectToPdfQuad,
  boundingQuads, nudgePdfRectByCssDelta, resizePdfRectByCssDelta,
  type ViewportLike, type PdfRect, type PdfQuad, type HandleEdge,
} from "./coords";
import { EditToolbar, type ToolId, type EditStyle } from "./edit-toolbar";
import type { StatusKind } from "./toolbar";
import { produceExportBytes } from "./export";
import { captureGroundTruth, capturedNeedles, findOtherOccurrences, type OtherOccurrence } from "./redact/capture";
import { rasterizeRedactions } from "./redact/rasterize";
import { verifyRedaction, summarizeReport, type VerifyReport, type VerifyRect } from "./redact/verify";
import { RedactPanel } from "./redact/panel";
import { state } from "./state";
// What the viewer exposes to the editor. `getEditSurface` returns null when a
// page isn't mounted/rendered, so the editor can call it defensively.
export interface EditorHost {
  getModel(): PdfModel | null;
  getViewport(page1: number): PageViewport | null;
  getEditSurface(page1: number): { layer: HTMLElement; viewport: PageViewport } | null;
  forEachPage(cb: (page1: number, layer: HTMLElement) => void): void;
  pagesEl: HTMLElement;
  getBytes(): Uint8Array;
  getCurrentPage(): number;
  /** Scroll the viewport to the given 1-based page (redact list jump-to). */
  jumpToPage(page: number): void;
  canExport(): boolean;
  mode: "view" | "annotate" | "redact";
  /** Rasterization DPI for redacted pages (init.config.redactDPI, default 200). */
  redactDPI: number;
  setStatus(msg: string, kind: StatusKind): void;
  onExportBytes(bytes: Uint8Array, kind: "export" | "download" | "print" | "redact", filename: string, report?: unknown): void;
  /** Announce a redaction state transition to the host IMMEDIATELY.
   *
   *  redactState used to reach the host only as a passenger on docChanged,
   *  which is debounced 250ms, gated on document:write, and — the part that
   *  actually broke — only emitted when the DOCUMENT changes. A terminal
   *  transition to "done" therefore reached the host only if some unrelated
   *  document mutation happened to be emitted after it. Whether one was is a
   *  timing race, so a redaction could rasterize, verify and export correctly
   *  while the host sat on a stale "working" forever, with nothing to show the
   *  user and no error to report. A status mirror must not depend on an
   *  unrelated event firing later. */
  onRedactState(redactState: string, lastVerifyReport?: unknown): void;
  invalidateOverlays(): void;
}

export interface Editor {
  readonly toolbar: { readonly root: HTMLElement };
  /** Present only in redact mode; the viewer mounts it under the toolbar. */
  readonly redactPanel?: { readonly root: HTMLElement };
  renderPage(pageIndex0: number): void;
  renderOverlays(): void;
  syncDocState(): void;
  dispose(): void;
}

// A pending creation/edit gesture (pointer-down → move → up).
interface Gesture {
  kind: "create" | "move" | "resize";
  page1: number;
  layer: HTMLElement;
  viewport: ViewportLike;
  startCssX: number; startCssY: number;   // gesture origin in layer CSS pixels
  edge?: HandleEdge;                        // resize edge
  annId?: AnnId;                            // move/resize target
  draftId?: AnnId;                          // create: the live preview annotation
  redactId?: string;                        // create: the live preview redaction rect
  inkPts?: Array<[number, number]>;         // ink: accumulated PDF-space points
  sticky?: boolean;                          // create: keep tool armed after place (Shift)
}

const STAMP_DATA_CAP_BYTES = 256 * 1024; // base64 length cap (~192 KiB raw)

function msg(e: unknown): string { return e instanceof Error ? e.message : String(e); }

export function createEditor(doc: OverlayDoc, host: EditorHost): Editor {
  const ed = new EditorImpl(doc, host);
  ed.bind();
  return ed;
}

class EditorImpl implements Editor {
  readonly toolbar: { readonly root: HTMLElement };
  readonly redactPanel?: RedactPanel;
  private readonly tool: EditToolbar;
  private readonly doc: OverlayDoc;
  private readonly host: EditorHost;
  private selectedId: AnnId | null = null;
  private gesture: Gesture | null = null;
  private stagedStamp: { mime: string; data: string; kind: "image" | "drawn" | "text"; label: string } | null = null;
  /** The open modal overlay + the element to restore focus to on close. */
  private activeModal: { overlay: HTMLElement; invoker: HTMLElement | null } | null = null;
  private boundDown: (e: PointerEvent) => void;
  private boundMove: (e: PointerEvent) => void;
  private boundUp: (e: PointerEvent) => void;
  private boundKey: (e: KeyboardEvent) => void;
  private style: EditStyle = { color: "#FFEB3B", width: 3, opacity: 0.35 };
  private currentReason: string = "";
  private redactBusy = false;
  /** Cached ground-truth previews for the pending list (id → preview). */
  private previews = new Map<string, string>();

  constructor(doc: OverlayDoc, host: EditorHost) {
    this.doc = doc;
    this.host = host;
    const isRedact = host.mode === "redact";
    this.tool = new EditToolbar({
      onTool: (t) => this.onToolChanged(t),
      onStyle: (s) => { this.style = s; },
      onUndo: () => this.doc.undo(),
      onRedo: () => this.doc.redo(),
      onExport: (flatten) => { void this.exportPdf(flatten); },
      onPageOp: (op) => this.applyPageOp(op),
      canExport: () => this.host.canExport(),
      onApplyRedaction: isRedact ? () => { void this.armRedaction(); } : undefined,
      onReason: isRedact ? (r) => { this.currentReason = r; } : undefined,
    }, host.mode);
    this.toolbar = this.tool;
    if (isRedact) {
      this.redactPanel = new RedactPanel({
        onJumpTo: (page) => this.host.jumpToPage(page),
        onDelete: (id) => { this.doc.apply(new RemoveRedactionCommand(id)); this.previews.delete(id); this.syncRedactPanel(); },
        onApply: () => { void this.armRedaction(); },
        onAddOccurrences: (needle, occ) => this.addOccurrences(needle, occ),
      });
    }
    // Static-bound handlers so add/removeEventListener keep their identity.
    this.boundDown = (e) => this.onPointerDown(e);
    this.boundMove = (e) => this.onPointerMove(e);
    this.boundUp = (e) => this.onPointerUp(e);
    this.boundKey = (e) => this.onKey(e);
  }

  bind(): void {
    // Pointer-down on the pages container; move/up on window so a drag keeps
    // tracking past the layer's edge. The pages container is the stable ancestor
    // of every page's editLayer (the viewer rebuilds page slots, so we attach
    // here, not on individual layers).
    this.host.pagesEl.addEventListener("pointerdown", this.boundDown);
    window.addEventListener("pointermove", this.boundMove, { passive: false });
    window.addEventListener("pointerup", this.boundUp);
    window.addEventListener("keydown", this.boundKey, { capture: true });
    this.syncDocState();
  }

  dispose(): void {
    this.host.pagesEl.removeEventListener("pointerdown", this.boundDown);
    window.removeEventListener("pointermove", this.boundMove);
    window.removeEventListener("pointerup", this.boundUp);
    window.removeEventListener("keydown", this.boundKey, { capture: true } as EventListenerOptions);
  }

  // --- doc + toolbar sync --------------------------------------------------

  syncDocState(): void {
    this.tool.syncState({
      canUndo: this.doc.canUndo(),
      canRedo: this.doc.canRedo(),
      canExport: this.host.canExport(),
    });
    this.syncRedactPanel();
    this.renderOverlays();
  }

  private onToolChanged(t: ToolId): void {
    // Switching away from a creation tool clears any staged stamp + selection.
    this.stagedStamp = null;
    if (t !== "select") this.select(null);
    if (t === "stamp") {
      // Open the picker; actual placement happens after a source is chosen.
      void this.openStampPicker();
    }
    this.host.setStatus(t === "select" ? "Select tool" : "Click on a page to place " + t, "ready");
  }

  // --- pointer routing -----------------------------------------------------

  private pageFromEvent(e: PointerEvent): { page1: number; layer: HTMLElement } | null {
    const tgt = e.target as Element | null;
    const slot = tgt && tgt.closest ? tgt.closest("[data-page]") as HTMLElement | null : null;
    if (!slot) return null;
    const p = Number(slot.getAttribute("data-page"));
    if (!Number.isFinite(p) || p < 1) return null;
    const surface = this.host.getEditSurface(p);
    if (!surface) return null;
    return { page1: p, layer: surface.layer };
  }

  private localCss(layer: HTMLElement, e: PointerEvent): [number, number] {
    const r = layer.getBoundingClientRect();
    return [e.clientX - r.left, e.clientY - r.top];
  }

  private onPointerDown(e: PointerEvent): void {
    if (e.button !== 0 && e.pointerType === "mouse") return; // primary button only for mouse
    const tool = this.tool.getCurrentTool();
    const hit = this.pageFromEvent(e);
    if (!hit) return;

    // Highlight is selection-driven, not click-driven: a pointerup with an
    // active text selection commits. So pointerdown just clears stale state.
    if (tool === "highlight") return;

    const surface = this.host.getEditSurface(hit.page1);
    if (!surface) return;
    const vp = surface.viewport as ViewportLike;
    const [lx, ly] = this.localCss(hit.layer, e);

    // Select tool: hit-test an annotation or a handle.
    if (tool === "select") {
      const handleEdge = this.handleAt(hit.page1, hit.layer, lx, ly);
      if (handleEdge && this.selectedId) {
        const ann = this.doc.getAnnotation(this.selectedId);
        if (ann) {
          e.preventDefault();
          this.doc.breakCoalesce(); // isolate this resize into its own undo unit
          this.gesture = { kind: "resize", page1: hit.page1, layer: hit.layer, viewport: vp, startCssX: lx, startCssY: ly, edge: handleEdge, annId: this.selectedId };
        }
        return;
      }
      const clicked = this.annotationAt(hit.page1, vp, lx, ly);
      this.select(clicked?.id ?? null);
      if (clicked) {
        e.preventDefault();
        this.doc.breakCoalesce(); // isolate this move into its own undo unit
        this.gesture = { kind: "move", page1: hit.page1, layer: hit.layer, viewport: vp, startCssX: lx, startCssY: ly, annId: clicked.id };
      }
      return;
    }

    // Stamp: place the staged stamp at the click; if none staged, open picker.
    if (tool === "stamp") {
      e.preventDefault();
      if (!this.stagedStamp) { void this.openStampPicker(); return; }
      this.placeStamp(hit.page1, vp, lx, ly);
      return;
    }

    // Redact tool: begin a drag gesture with a live preview redaction rect.
    if (tool === "redact") {
      e.preventDefault();
      const [px, py] = cssPointToPdf(vp, lx, ly);
      const red: Redaction = {
        id: cryptoId(),
        page: hit.page1,
        rect: [px, py, 1, 1],
        reason: this.currentReason,
      };
      this.doc.apply(new AddRedactionCommand(red));
      this.gesture = {
        kind: "create", page1: hit.page1, layer: hit.layer, viewport: vp,
        startCssX: lx, startCssY: ly, redactId: red.id,
      };
      return;
    }

    // Creation tools: begin a drag gesture with a live preview.
    e.preventDefault();
    const draft = this.newDraft(tool, hit.page1, vp, lx, ly);
    if (!draft) return;
    this.doc.apply(new AddAnnotationCommand(draft));
    this.select(draft.id);
    // ink seeds its PDF-space polyline from the first point; rect/ellipse/
    // arrow derive their rect from CSS corners in onPointerMove.
    const inkSeed = tool === "ink" ? [cssPointToPdf(vp, lx, ly)] as Array<[number, number]> : undefined;
    this.gesture = {
      kind: "create", page1: hit.page1, layer: hit.layer, viewport: vp,
      startCssX: lx, startCssY: ly, inkPts: inkSeed, sticky: e.shiftKey,
    };
    this.gesture.draftId = draft.id;
  }

  private onPointerMove(e: PointerEvent): void {
    const g = this.gesture;
    if (!g) return;
    e.preventDefault();
    const [lx, ly] = this.localCss(g.layer, e);
    if (g.kind === "create") {
      // Both corners are layer-CSS pixels; cssDragToPdfRect converts them
      // through the viewport. (The previous code stored the start corner in
      // PDF space and converted it a SECOND time inside cssDragToPdfRect —
      // the double conversion that displaced every dragged annotation.)
      if (g.redactId) {
        const r = cssDragToPdfRect(g.viewport, g.startCssX, g.startCssY, lx, ly);
        this.updateRedactRect(g.redactId, r);
        return;
      }
      if (!g.draftId) return;
      if (this.tool.getCurrentTool() === "ink") {
        // Ink is authored as a PDF-space polyline (it survives zoom/rotate
        // unprojected), so each sample is converted individually.
        const [px, py] = cssPointToPdf(g.viewport, lx, ly);
        g.inkPts = g.inkPts ?? [];
        const last = g.inkPts[g.inkPts.length - 1];
        if (!last || Math.hypot(px - last[0], py - last[1]) > 0.5) g.inkPts.push([px, py]);
        this.updateInk(g.draftId, g.inkPts);
      } else {
        const r = cssDragToPdfRect(g.viewport, g.startCssX, g.startCssY, lx, ly);
        this.updateRect(g.draftId, r);
      }
      return;
    }
    if (g.kind === "move" && g.annId) {
      // CSS delta → PDF delta via the dedicated helper, so the move tracks
      // the cursor 1:1 at any zoom/rotation. Ink points shift by the same
      // resolved PDF delta so the stroke moves with its bounding box.
      const dxCss = lx - g.startCssX, dyCss = ly - g.startCssY;
      g.startCssX = lx; g.startCssY = ly;
      const ann = this.doc.getAnnotation(g.annId);
      if (!ann) return;
      const moved = nudgePdfRectByCssDelta(g.viewport, ann.rect, dxCss, dyCss);
      const dpx = moved[0] - ann.rect[0], dpy = moved[1] - ann.rect[1];
      this.doc.applyCoalesced(new UpdateAnnotationCommand(g.annId, (a) => {
        a.rect = moved;
        if (a.type === "ink" && a.points) a.points = a.points.map((p) => [p[0] + dpx, p[1] + dpy] as [number, number]);
      }), "move:" + g.annId);
      return;
    }
    if (g.kind === "resize" && g.annId && g.edge) {
      const ann = this.doc.getAnnotation(g.annId);
      if (!ann) return;
      const newRect = resizePdfRectByCssDelta(g.viewport, ann.rect, g.edge, lx - g.startCssX, ly - g.startCssY);
      g.startCssX = lx; g.startCssY = ly;
      this.doc.applyCoalesced(new UpdateAnnotationCommand(g.annId, (a) => { a.rect = newRect; }), "resize:" + g.annId);
    }
  }
  private onPointerUp(_e: PointerEvent): void {
    const g = this.gesture;
    // Highlight is selection-driven and never opens a gesture (pointerdown
    // returns early so the native text selection can form), so its commit must
    // run BEFORE the null-gesture bail below — otherwise releasing a text
    // selection with the Highlight tool armed does nothing.
    if (this.tool.getCurrentTool() === "highlight") {
      this.commitHighlightIfSelection();
    }
    if (!g) return;
    let placedId: AnnId | null = null;
    if (g.kind === "create" && g.draftId) {
      if (this.tool.getCurrentTool() === "ink" && g.inkPts && g.inkPts.length > 1) {
        this.smoothInk(g.draftId, g.inkPts);
      }
      // Drop zero-area drafts (a click without a drag for rect/ellipse/arrow).
      const ann = this.doc.getAnnotation(g.draftId);
      if (ann && this.isDegenerateDraft(ann)) {
        this.doc.apply(new RemoveAnnotationCommand(g.draftId));
        this.select(null);
      } else if (ann) {
        placedId = g.draftId;
      }
    }
    if (g.kind === "create" && g.redactId) {
      const red = this.doc.getRedaction(g.redactId);
      if (red && (red.rect[2] < 3 || red.rect[3] < 3)) {
        // A click without a drag — drop the 1×1 draft so it cannot arm.
        this.doc.apply(new RemoveRedactionCommand(g.redactId));
      } else {
        this.syncRedactPanel();
      }
    }
    this.gesture = null;
    // Select-after-place (the default in every editor of this kind): once a
    // real annotation lands, drop the creation tool and keep the just-placed
    // annotation selected so it can be moved/resized immediately. Hold Shift
    // at pointerdown to keep the tool armed (sticky) for a burst of placements.
    // Redaction rects stay armed by default — authoring many is the norm.
    if (placedId && !g.sticky) {
      this.select(placedId);
      this.tool.chooseTool("select");
    }
  }

  // Highlight: turn the current text-layer selection into page-space quads.
  private commitHighlightIfSelection(): void {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return;
    const range = sel.getRangeAt(0);
    const rects = range.getClientRects();
    if (rects.length === 0) return;
    // All rects live over one page (the selection doesn't cross pages here).
    let layer: HTMLElement | null = null, page1 = 0;
    const container = range.commonAncestorContainer;
    const pageEl = (container instanceof Element ? container : container.parentElement)
      ?.closest("[data-page]") as HTMLElement | null;
    if (pageEl) {
      const p = Number(pageEl.getAttribute("data-page"));
      const surface = Number.isFinite(p) ? this.host.getEditSurface(p) : null;
      if (surface) { layer = surface.layer; page1 = p; }
    }
    if (!layer || !page1) return;
    const surface = this.host.getEditSurface(page1);
    if (!surface) return;
    const vp = surface.viewport as ViewportLike;
    const lr = layer.getBoundingClientRect();
    const quads: PdfQuad[] = [];
    for (let i = 0; i < rects.length; i++) {
      const r = rects[i];
      quads.push(cssRectToPdfQuad(vp, r.left - lr.left, r.top - lr.top, r.right - lr.left, r.bottom - lr.top));
    }
    if (quads.length === 0) return;
    const rect = boundingQuads(quads);
    const ann: Annotation = {
      id: cryptoId(), page: page1, type: "highlight", rect,
      color: this.style.color, opacity: 0.35, quads,
    };
    this.doc.apply(new AddAnnotationCommand(ann));
    sel.removeAllRanges();
    this.select(ann.id);
  }

  // --- draft factories -----------------------------------------------------

  private newDraft(tool: ToolId, page1: number, vp: ViewportLike, lx: number, ly: number): Annotation | null {
    const [px, py] = cssPointToPdf(vp, lx, ly);
    const color = this.style.color;
    const width = this.style.width;
    switch (tool) {
      case "highlight": return null; // handled on pointerup
      case "ink":
        return { id: cryptoId(), page: page1, type: "ink", rect: [px, py, 1, 1], color, width, points: [[px, py]] };
      case "rect":
      case "ellipse":
      case "arrow":
        return { id: cryptoId(), page: page1, type: tool, rect: [px, py, 1, 1], color, fill: null, width, opacity: this.style.opacity };
      case "text":
        // Place the painted top-left at the click: PDF y is the BOTTOM edge of
        // the rect, so shift up by the height so the box's top lands at the
        // click point (otherwise it appears one box-height above the click).
        return { id: cryptoId(), page: page1, type: "text", rect: [px, py - 24, 160, 24], color, text: "Text", fontSize: 14 };
      case "note":
        return { id: cryptoId(), page: page1, type: "note", rect: [px, py - 20, 20, 20], color, text: "" };
      default:
        return null;
    }
  }

  // Create-drag previews mutate the live annotation WITHOUT recording undo —
  // the AddAnnotation command pushed at pointerdown is the single undo unit for
  // the whole creation, so one Undo removes a just-placed annotation. (Move and
  // resize of an EXISTING annotation use applyCoalesced in onPointerMove so a
  // whole drag is one undo unit too.)
  private updateRect(id: AnnId, rect: PdfRect): void {
    this.doc.mutateAnnotation(id, (a) => { a.rect = rect; });
  }

  private updateInk(id: AnnId, pts: Array<[number, number]>): void {
    if (pts.length === 0) return;
    this.doc.mutateAnnotation(id, (a) => {
      if (a.type !== "ink") return;
      a.points = pts.slice();
      a.rect = inkBoundingRect(pts);
    });
  }

  // Catmull-Rom resample for a smoother freehand stroke. Replaces the raw
  // pointer samples with an interpolated polyline at a fixed PDF-space step.
  private smoothInk(id: AnnId, pts: Array<[number, number]>): void {
    if (pts.length < 3) { this.updateInk(id, pts); return; }
    const out: Array<[number, number]> = [pts[0]];
    const step = 1.0; // PDF points
    for (let i = 0; i < pts.length - 1; i++) {
      const p0 = pts[i - 1] ?? pts[i];
      const p1 = pts[i];
      const p2 = pts[i + 1];
      const p3 = pts[i + 2] ?? p2;
      const dist = Math.hypot(p2[0] - p1[0], p2[1] - p1[1]);
      const n = Math.max(2, Math.ceil(dist / step));
      for (let t = 1; t <= n; t++) {
        const s = t / n;
        const s2 = s * s, s3 = s2 * s;
        const x = 0.5 * ((2 * p1[0]) + (-p0[0] + p2[0]) * s + (2 * p0[0] - 5 * p1[0] + 4 * p2[0] - p3[0]) * s2 + (-p0[0] + 3 * p1[0] - 3 * p2[0] + p3[0]) * s3);
        const y = 0.5 * ((2 * p1[1]) + (-p0[1] + p2[1]) * s + (2 * p0[1] - 5 * p1[1] + 4 * p2[1] - p3[1]) * s2 + (-p0[1] + 3 * p1[1] - 3 * p2[1] + p3[1]) * s3);
        out.push([x, y]);
      }
    }
    this.updateInk(id, out);
  }

  private isDegenerateDraft(a: Annotation): boolean {
    if (a.type === "note") return false; // click-placed, fixed size
    if (a.type === "ink") return a.points.length < 2;
    return a.rect[2] < 3 || a.rect[3] < 3;
  }

  // --- modal focus management -------------------------------------------
  // The dialog opens from the Stamp tool button; Escape, backdrop click and
  // Cancel must all dismiss it, focus must be trapped inside while open, and
  // focus must return to the invoking button on close. Previously Escape only
  // cleared the staged stamp — the comment above the modal claimed Escape
  // closed it, but it never did, so a user could open the picker and be
  // unable to leave it.

  /** Open a managed modal: records the invoker, mounts the overlay, focuses the
   *  first focusable control. The editor's keydown handler closes it on Escape
   *  and traps Tab while it is the active modal. */
  private openModal(overlay: HTMLElement, invokerHint?: HTMLElement | null): void {
    // Prefer the explicit hint (the opening button) so focus is restored to the
    // right control even when a synthetic/programmatic click did not move
    // document.activeElement onto the button (a headless-engine quirk; a real
    // user click or Enter focuses it, matching activeElement).
    const invoker = invokerHint ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    this.activeModal = { overlay, invoker };
    document.body.appendChild(overlay);
    // Focus the first focusable control (the first tab) so keyboard users land
    // inside the dialog, not on the backdrop.
    const f = this.modalFocusables();
    if (f.length > 0) f[0].focus();
  }

  /** Tear down the active modal and restore focus to the invoking control. */
  private closeModal(): void {
    const m = this.activeModal;
    this.activeModal = null;
    if (m) {
      m.overlay.remove();
      // Restore focus synchronously. requestAnimationFrame was unreliable in
      // headless WebKit (the rAF never fired before the probe read activeElement),
      // leaving focus on the body; focusing here in the Escape/click handler is
      // correct and deterministic.
      try { m.invoker?.focus(); } catch { /* element gone */ }
    }
  }

  /** Visible, enabled focusable controls inside the active modal, in DOM order. */
  private modalFocusables(): HTMLElement[] {
    const m = this.activeModal;
    if (!m) return [];
    const sel = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';
    const nodes = Array.from(m.overlay.querySelectorAll<HTMLElement>(sel));
    return nodes.filter((n) => {
      if (n.hasAttribute("disabled")) return false;
      if (n.getAttribute("aria-hidden") === "true") return false;
      const rect = (n as HTMLElement).getBoundingClientRect();
      return rect.width > 0 && rect.height > 0;
    });
  }

  /** Wrap Tab/Shift+Tab within the active modal so focus cannot escape. */
  private trapTab(e: KeyboardEvent): void {
    const f = this.modalFocusables();
    if (f.length === 0) return;
    e.preventDefault();
    e.stopPropagation();
    const cur = document.activeElement;
    const i = f.indexOf(cur as HTMLElement);
    const shift = e.shiftKey;
    let next: HTMLElement;
    if (i === -1) next = shift ? f[f.length - 1] : f[0];
    else next = shift ? f[(i - 1 + f.length) % f.length] : f[(i + 1) % f.length];
    next.focus();
  }

  // --- stamp / signature picker -------------------------------------------

  private openStampPicker(): void {
    // Minimal modal: choose image (file), draw (canvas pad), or type (text).
    // On confirm, stage a stamp; the next page-click places it. Reusable for
    // the session until the tool changes or Escape is pressed.
    const overlay = el("div", { cls: "pdf-modal-overlay", role: "dialog", ariaModal: true, ariaLabel: "Add stamp or signature" });
    const panel = el("div", { cls: "pdf-modal-panel" });
    panel.appendChild(el("h2", { cls: "pdf-modal-title", text: "Add stamp or signature" }));

    const tabs = el("div", { cls: "pdf-modal-tabs", role: "tablist" });
    const tabImage = el("button", { cls: "pdf-modal-tab", type: "button", text: "Image", role: "tab", ariaSelected: "true" }) as HTMLButtonElement;
    const tabDraw = el("button", { cls: "pdf-modal-tab", type: "button", text: "Draw", role: "tab" }) as HTMLButtonElement;
    const tabType = el("button", { cls: "pdf-modal-tab", type: "button", text: "Type", role: "tab" }) as HTMLButtonElement;
    tabs.appendChild(tabImage); tabs.appendChild(tabDraw); tabs.appendChild(tabType);

    const body = el("div", { cls: "pdf-modal-body" });

    // --- image tab ---
    const imagePane = el("div", { cls: "pdf-modal-pane" });
    const fileInput = el("input", { type: "file", attrs: { accept: "image/png,image/jpeg" } }) as HTMLInputElement;
    const fileHint = el("p", { cls: "pdf-modal-hint", text: "PNG or JPEG. Large images are refused — cap " + Math.round(STAMP_DATA_CAP_BYTES / 1024) + " KiB." });
    imagePane.appendChild(fileInput); imagePane.appendChild(fileHint);

    // --- draw tab ---
    const drawPane = el("div", { cls: "pdf-modal-pane", attrs: { hidden: "hidden" } });
    const drawCanvas = el("canvas", { cls: "pdf-sig-canvas", attrs: { width: "320", height: "120" } }) as HTMLCanvasElement;
    const drawHint = el("p", { cls: "pdf-modal-hint", text: "Draw with mouse or touch." });
    const clearBtn = el("button", { cls: "pdf-edit-btn", type: "button", text: "Clear" }) as HTMLButtonElement;
    drawPane.appendChild(drawCanvas); drawPane.appendChild(drawHint); drawPane.appendChild(clearBtn);
    this.wireSigCanvas(drawCanvas, clearBtn);

    // --- type tab ---
    const typePane = el("div", { cls: "pdf-modal-pane", attrs: { hidden: "hidden" } });
    const typeInput = el("input", { type: "text", cls: "pdf-modal-input", attrs: { placeholder: "Type your name" } }) as HTMLInputElement;
    typePane.appendChild(typeInput);

    body.appendChild(imagePane); body.appendChild(drawPane); body.appendChild(typePane);

    const actions = el("div", { cls: "pdf-modal-actions" });
    const cancelBtn = el("button", { cls: "pdf-edit-btn", type: "button", text: "Cancel" }) as HTMLButtonElement;
    const okBtn = el("button", { cls: "pdf-edit-btn pdf-export-btn", type: "button", text: "Stage" }) as HTMLButtonElement;
    actions.appendChild(cancelBtn); actions.appendChild(okBtn);

    panel.appendChild(tabs); panel.appendChild(body); panel.appendChild(actions);
    overlay.appendChild(panel);
    // The Stamp tool button opened this dialog; restore focus to it on close.
    // A real user click focuses it (document.activeElement), but a synthetic
    // click in a headless engine may not, so resolve it explicitly.
    const stampBtn = document.querySelector<HTMLElement>('button.pdf-tool-btn[aria-label="Stamp"]');
    this.openModal(overlay, stampBtn);

    const showPane = (which: "image" | "draw" | "type") => {
      for (const t of [tabImage, tabDraw, tabType]) t.setAttribute("aria-selected", "false");
      (which === "image" ? tabImage : which === "draw" ? tabDraw : tabType).setAttribute("aria-selected", "true");
      imagePane.toggleAttribute("hidden", which !== "image");
      drawPane.toggleAttribute("hidden", which !== "draw");
      typePane.toggleAttribute("hidden", which !== "type");
    };
    tabImage.addEventListener("click", () => showPane("image"));
    tabDraw.addEventListener("click", () => showPane("draw"));
    tabType.addEventListener("click", () => showPane("type"));

    const close = () => this.closeModal();
    cancelBtn.addEventListener("click", close);
    overlay.addEventListener("click", (e) => { if (e.target === overlay) close(); });

    okBtn.addEventListener("click", () => {
      const active = [tabImage, tabDraw, tabType].find((t) => t.getAttribute("aria-selected") === "true");
      if (active === tabImage) {
        const f = fileInput.files && fileInput.files[0];
        if (!f) { this.host.setStatus("Pick an image first", "error"); return; }
        this.readImageFile(f).then((res) => {
          if (!res.ok) { this.host.setStatus(res.message ?? "image too large", "error"); return; }
          this.stagedStamp = { mime: res.mime, data: res.data, kind: "image", label: f.name };
          close();
          this.host.setStatus("Stamp staged — click a page to place it", "ready");
        });
      } else if (active === tabDraw) {
        const data = drawCanvas.toDataURL("image/png");
        if (data.length > STAMP_DATA_CAP_BYTES) {
          this.host.setStatus("Signature too large — clear and draw smaller", "error"); return;
        }
        this.stagedStamp = { mime: "image/png", data, kind: "drawn", label: "signature" };
        close();
        this.host.setStatus("Signature staged — click a page to place it", "ready");
      } else {
        const text = typeInput.value.trim();
        if (!text) { this.host.setStatus("Type a name first", "error"); return; }
        // Render the typed name to a small canvas so all stamp kinds share one
        // paint path (drawImage at export). Standard font only.
        const { mime, data } = renderTextStamp(text);
        this.stagedStamp = { mime, data, kind: "text", label: text };
        close();
        this.host.setStatus("Signature staged — click a page to place it", "ready");
      }
    });
  }

  private wireSigCanvas(canvas: HTMLCanvasElement, clearBtn: HTMLButtonElement): void {
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.strokeStyle = "#111"; ctx.lineWidth = 2.5; ctx.lineJoin = "round"; ctx.lineCap = "round";
    let drawing = false;
    let last: [number, number] | null = null;
    const pos = (e: PointerEvent): [number, number] => {
      const r = canvas.getBoundingClientRect();
      return [e.clientX - r.left, e.clientY - r.top];
    };
    const end = (e: PointerEvent) => { drawing = false; last = null; try { canvas.releasePointerCapture(e.pointerId); } catch { /* ignore */ } };
    canvas.addEventListener("pointerup", end);
    canvas.addEventListener("pointercancel", end);
    clearBtn.addEventListener("click", () => {
      ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, canvas.width, canvas.height);
    });
  }

  private readImageFile(f: File): Promise<{ ok: true; data: string; mime: string } | { ok: false; message?: string }> {
    const { promise, resolve } = Promise.withResolvers<{ ok: true; data: string; mime: string } | { ok: false; message?: string }>();
    if (f.size > STAMP_DATA_CAP_BYTES) {
      resolve({ ok: false, message: "image too large (cap " + Math.round(STAMP_DATA_CAP_BYTES / 1024) + " KiB)" });
      return promise;
    }
    const reader = new FileReader();
    reader.onerror = () => resolve({ ok: false, message: "could not read file" });
    reader.onload = () => {
      const r = reader.result;
      if (typeof r !== "string" || r.length > STAMP_DATA_CAP_BYTES) {
        resolve({ ok: false, message: "image too large after encoding" });
        return;
      }
      resolve({ ok: true, data: r, mime: f.type || "image/png" });
    };
    reader.readAsDataURL(f);
    return promise;
  }

  private placeStamp(page1: number, vp: ViewportLike, lx: number, ly: number): void {
    if (!this.stagedStamp) { void this.openStampPicker(); return; }
    const [px, py] = cssPointToPdf(vp, lx, ly);
    // Natural size from the data URL aspect (160pt wide default; aspect kept).
    const w = 160, h = 60;
    const ann: Annotation = {
      id: cryptoId(), page: page1, type: "stamp",
      rect: [px - w / 2, py - h / 2, w, h],
      mime: this.stagedStamp.mime, data: this.stagedStamp.data,
      kind: this.stagedStamp.kind, label: this.stagedStamp.label,
    };
    this.doc.apply(new AddAnnotationCommand(ann));
    this.select(ann.id);
  }

  // --- selection + hit testing --------------------------------------------

  private select(id: AnnId | null): void {
    this.selectedId = id;
    // Mirror onto __pdfState so tests can observe selection (the host parent
    // cannot read into the opaque frame; this in-frame field is the channel).
    state.selectedId = id;
    this.renderOverlays();
  }

  private annotationAt(page1: number, vp: ViewportLike, lx: number, ly: number): Annotation | null {
    // Hit-test in CSS space against each annotation's painted box. Topmost wins.
    const anns = this.doc.annotationsForPage(page1);
    const [px, py] = cssPointToPdf(vp, lx, ly);
    for (let i = anns.length - 1; i >= 0; i--) {
      const a = anns[i];
      const r = a.rect;
      // Slightly generous hit pad so thin strokes are grabbable.
      const pad = Math.max(4, (a.type === "ink" || a.type === "arrow" || a.type === "rect" || a.type === "ellipse") ? 6 : 0);
      if (px >= r[0] - pad && px <= r[0] + r[2] + pad && py >= r[1] - pad && py <= r[1] + r[3] + pad) {
        return a;
      }
    }
    return null;
  }

  private handleAt(page1: number, layer: HTMLElement, lx: number, ly: number): HandleEdge | null {
    if (!this.selectedId) return null;
    const ann = this.doc.getAnnotation(this.selectedId);
    if (!ann || ann.page !== page1) return null;
    const surface = this.host.getEditSurface(page1);
    if (!surface) return null;
    const handles = handleEdges(ann);
    if (!handles) return null;
    const box = pdfRectToCss(surface.viewport as ViewportLike, ann.rect);
    for (const edge of handles) {
      const p = handlePoint(box, edge);
      if (Math.hypot(p.x - lx, p.y - ly) <= 8) return edge;
    }
    return null;
  }

  // --- keyboard ------------------------------------------------------------

  private onKey(e: KeyboardEvent): void {
    // Undo/redo (Ctrl/Cmd+Z, Ctrl/Cmd+Shift+Z). Capture so the viewer's own
    // key handler (which listens on the scroll region) doesn't also act.
    const meta = e.ctrlKey || e.metaKey;
    // While a managed modal is open it owns the keyboard: Escape closes it,
    // Tab is trapped inside, and every other key (tool shortcuts, delete,
    // undo) is swallowed so the dialog is modal in behaviour, not just name.
    if (this.activeModal) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); this.closeModal(); return; }
      if (e.key === "Tab") { this.trapTab(e); return; }
      e.stopPropagation();
      return;
    }
    if (meta && (e.key === "z" || e.key === "Z")) {
      e.preventDefault();
      e.stopPropagation();
      if (e.shiftKey) this.doc.redo(); else this.doc.undo();
      return;
    }
    // Only handle the rest when an annotation is selected OR a tool is active
    // and focus isn't in a text field (so we don't steal typing).
    const ae = document.activeElement;
    const aeHtml = ae instanceof HTMLElement ? ae : null;
    const inField = ae instanceof HTMLInputElement || ae instanceof HTMLTextAreaElement || (aeHtml?.isContentEditable ?? false);
    if (inField) return;
    if (e.key === "Delete" || e.key === "Backspace") {
      if (this.selectedId) {
        e.preventDefault();
        this.doc.apply(new RemoveAnnotationCommand(this.selectedId));
        this.select(null);
      }
      return;
    }
    if (e.key === "Escape") {
      // Cancel any in-flight gesture, drop selection, and return to the
      // Select tool — the standard "Escape cancels what I was doing" escape
      // hatch. Also clears a staged stamp so it cannot surprise the next click.
      this.gesture = null;
      this.select(null);
      this.stagedStamp = null;
      if (this.tool.getCurrentTool() !== "select") this.tool.chooseTool("select");
      return;
    }
    // Tool shortcuts: v select, h highlight, d draw(ink), r rect, e ellipse,
    // a arrow, t text, n note, s stamp.
    const map: Record<string, ToolId> = {
      v: "select", h: "highlight", d: "ink", r: "rect", e: "ellipse",
      a: "arrow", t: "text", n: "note", s: "stamp",
    };
    const t = map[e.key.toLowerCase()];
    if (t && !meta && !e.altKey) {
      e.preventDefault();
      this.tool.chooseTool(t);
    }
  }

  // --- export --------------------------------------------------------------

  private async exportPdf(flatten: boolean): Promise<void> {
    if (!this.host.canExport()) {
      this.host.setStatus("Export is not granted for this mount", "error");
      return;
    }
    const model = this.host.getModel();
    if (!model) { this.host.setStatus("No document to export", "error"); return; }
    const src = this.host.getBytes();
    this.host.setStatus("Producing PDF…", "loading");
    try {
      const bytes = await produceExportBytes({
        sourceBytes: src,
        model,
        overlay: this.doc.state,
        flatten,
        redactDPI: 200,
      });
      const filename = "gofastr-export.pdf";
      this.host.onExportBytes(bytes, "export", filename);
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e);
      this.host.setStatus("Export failed: " + msg, "error");
    }
  }

  // Page operations accumulate in doc.pageOps and apply at export. Rotate is
  // reflected live (invalidateOverlays → re-render); delete/insert are applied
  // at export (the live slot list is the rendered source, not the exported
  // result). A documented v1 seam.
  private applyPageOp(op: "rotate" | "delete" | "insert"): void {
    const page = Math.max(1, this.host.getCurrentPage());
    if (op === "rotate") {
      this.doc.apply(new AddPageOpCommand({ op: "rotate", page, value: 90 }));
      this.host.invalidateOverlays();
      return;
    }
    if (op === "delete") {
      this.doc.apply(new AddPageOpCommand({ op: "delete", page, value: 0 }));
      this.host.setStatus("Page " + page + " will be removed in the exported PDF", "ready");
      return;
    }
    this.doc.apply(new AddPageOpCommand({ op: "insert", page, value: 0 }));
    this.host.setStatus("A blank page will be inserted after page " + page + " in the export", "ready");
  }
  // --- redaction (mode === "redact") --------------------------------------

  private updateRedactRect(id: string, rect: PdfRect): void {
    this.doc.mutateRedaction(id, (r) => { r.rect = rect; });
  }

  /** Refresh the pending list + Apply button from the live doc state. */
  private syncRedactPanel(): void {
    if (!this.redactPanel) return;
    const reds = this.doc.state.redactions;
    this.redactPanel.setPending(reds, this.previews);
    this.tool.setRedactionCount(reds.length);
  }

  /** Add rects for every other occurrence of `needle` (the assist). */
  /** Set the redaction state AND tell the host, in one step.
   *
   *  Every transition goes through here so none can be left unannounced. The
   *  assignment and the announcement being separate statements is what let
   *  "done" be set without ever reaching the host. */
  private setRedactState(s: typeof state.redactState, report?: unknown): void {
    state.redactState = s;
    this.host.onRedactState(s, report);
  }

  private addOccurrences(needle: string, occ: OtherOccurrence[]): void {
    const cmds = occ.map((o) => new AddRedactionCommand({
      id: cryptoId(), page: o.page, rect: o.rect.slice() as PdfRect, reason: needle ? `“${needle.slice(0, 40)}”` : "occurrence",
    }));
    // One undo unit for the whole batch.
    this.doc.apply(new BatchCommand(cmds, "Redact " + occ.length + " occurrence" + (occ.length === 1 ? "" : "s")));
    this.syncRedactPanel();
    this.host.invalidateOverlays();
    this.host.setStatus(`Added ${occ.length} redaction${occ.length === 1 ? "" : "s"} for “${needle.slice(0, 40)}”`, "ready");
  }

  /**
   * Arm → confirm → capture → rasterize → verify → emit. The capture happens
   * BEFORE the confirm so the modal can show the other-occurrences assist and
   * the consequence can name the real page count. If verification fails, NO
   * bytes are emitted (a file the user believes is redacted is worse than none).
   */
  private async armRedaction(): Promise<void> {
    if (this.redactBusy) return;
    const model = this.host.getModel();
    if (!model) { this.host.setStatus("No document to redact", "error"); return; }
    const redactions = this.doc.state.redactions.slice();
    if (redactions.length === 0) { this.host.setStatus("Draw a redaction rectangle first", "error"); return; }
    if (!this.host.canExport()) { this.host.setStatus("Export is not granted for this mount", "error"); return; }
    if (!this.redactPanel) return;

    this.redactBusy = true;
    this.setRedactState("armed");
    this.host.setStatus("Capturing redaction targets…", "loading");
    this.redactPanel.clearBody();

    // 1. Capture ground truth BEFORE modifying anything: the strings under each
    //    rect are the needles the verifier later proves absent.
    let captured;
    try {
      captured = await captureGroundTruth(model, redactions);
    } catch (e) {
      this.redactBusy = false;
      this.setRedactState("error");
      this.host.setStatus("Capture failed: " + msg(e), "error");
      return;
    }
    for (const c of captured) this.previews.set(c.redactionId, c.preview);
    this.syncRedactPanel();
    const needles = capturedNeedles(captured);

    // 2. Occurrences assist: for the longest captured needle (most specific),
    // find every other line that still contains it and is not already redacted.
    const primaryNeedle = needles.slice().sort((a, b) => b.length - a.length)[0] ?? "";
    let occurrences: OtherOccurrence[] = [];
    if (primaryNeedle) {
      try { occurrences = await findOtherOccurrences(model, primaryNeedle, redactions); } catch { /* non-fatal */ }
    }

    const affectedPages = new Set(redactions.map((r) => r.page)).size;
    const dpi = this.host.redactDPI || 200;

    // 3. Arm + confirm — the modal names the irreversible consequence.
    //
    // The promise resolves with WHICH way the modal closed. Inferring that from
    // a mutated `redactBusy` flag was the earlier shape, and it hid two bugs:
    // the cancel path never resolved at all (leaking a suspended async call per
    // dismissal), and the occurrences-assist path resolved nothing while
    // leaving redactBusy true — which made the `if (this.redactBusy) return`
    // guard at the top of this method swallow every subsequent Apply, with no
    // error anywhere. An explicit outcome makes each exit total.
    const outcome = await new Promise<"confirm" | "cancel" | "added-occurrences">((resolve) => {
      this.redactPanel!.showConfirm({
        redactionCount: redactions.length,
        pages: affectedPages,
        dpi,
        occurrences,
        needle: primaryNeedle,
        onConfirm: () => resolve("confirm"),
        onCancel: () => resolve("cancel"),
        onAddedOccurrences: () => resolve("added-occurrences"),
      });
    });
    if (outcome !== "confirm") {
      // Both non-confirm exits return the flow to rest so Apply works again.
      // addOccurrences() already set its own status naming what it added, so
      // only the true cancel writes one here.
      this.redactBusy = false;
      this.setRedactState("idle");
      if (outcome === "cancel") this.host.setStatus("Redaction cancelled", "ready");
      return;
    }

    // 4. Rasterize affected pages only (main-thread work — yields to keep UI live).
    this.setRedactState("working");
    this.host.setStatus(`Rasterizing ${affectedPages} page${affectedPages === 1 ? "" : "s"} at ${dpi} DPI…`, "loading");
    this.redactPanel.setProgress(0, redactions.length, 0);
    const tStart = performance.now();
    let lastPageAt = tStart;
    let maxBlockMs = 0;
    let output;
    try {
      output = await rasterizeRedactions({
        sourceBytes: this.host.getBytes(),
        model,
        redactions,
        dpi,
        onProgress: (_done, _total, _page) => {
          const now = performance.now();
          // Gap between consecutive progress callbacks ≈ one page's
          // render+paint+embed — the longest single main-thread block.
          maxBlockMs = Math.max(maxBlockMs, now - lastPageAt);
          lastPageAt = now;
          this.redactPanel!.setProgress(_done, _total, _page);
        },
      });
    } catch (e) {
      this.redactBusy = false;
      this.setRedactState("error");
      const m = msg(e);
      this.redactPanel.setError("Rasterization failed: " + m);
      this.host.setStatus("Redaction failed: " + m, "error");
      return;
    }
    state.lastRedactTotalMs = Math.round(performance.now() - tStart);
    state.lastRedactMaxBlockMs = Math.round(maxBlockMs);

    // 5. Verify BEFORE releasing the bytes.
    this.host.setStatus("Verifying redaction…", "loading");
    const verifyRects: VerifyRect[] = redactions.map((r) => ({ page: r.page, rect: r.rect }));
    let report: VerifyReport;
    try {
      report = await verifyRedaction(output.bytes, { needles, redactions: verifyRects });
    } catch (e) {
      this.redactBusy = false;
      this.setRedactState("error");
      const m = msg(e);
      this.redactPanel.setError("Verification failed to run: " + m);
      this.host.setStatus("Verification failed: " + m, "error");
      return;
    }
    state.lastVerifyReport = summarizeReport(report);

    // 6. Emit only on success; surface failure with the full report.
    if (report.ok) {
      state.lastExportError = null;
      this.setRedactState("done", summarizeReport(report));
      this.redactPanel.setResult(report);
      this.host.onExportBytes(output.bytes, "redact", "gofastr-redacted.pdf", summarizeReport(report));
      this.host.setStatus(`Redacted ${affectedPages} page${affectedPages === 1 ? "" : "s"} — verified, ${output.bytes.length} B`, "ready");
    } else {
      this.setRedactState("error");
      state.lastExportError = "verification failed";
      this.redactPanel.setResult(report);
      this.host.setStatus("Verification FAILED — no file emitted", "error");
    }
    this.redactBusy = false;
  }


  // --- rendering -----------------------------------------------------------

  renderOverlays(): void {
    this.host.forEachPage((_page1, _layer) => {
      // We re-render via the host's page index lookup; forEachPage gives the
      // layer but renderPage wants the 0-based index the viewer uses.
    });
    // Render every rendered page (cheap: each clears + repaints only its own).
    const n = this.host.getModel()?.pageCount ?? 0;
    for (let i = 0; i < n; i++) this.renderPage(i);
  }

  renderPage(pageIndex0: number): void {
    const page1 = pageIndex0 + 1;
    const surface = this.host.getEditSurface(page1);
    if (!surface) return;
    const { layer, viewport } = surface;
    const vp = viewport as ViewportLike;
    layer.replaceChildren();
    const anns = this.doc.annotationsForPage(page1);
    for (const a of anns) {
      const node = this.paintAnnotation(a, vp);
      if (node) layer.appendChild(node);
    }
    // Redaction rects (mode === "redact"): distinct hatched markers so they
    // read as removal regions, not annotations. Painted under the selection
    // ring so a selected annotation's handles stay on top.
    for (const r of this.doc.redactionsForPage(page1)) {
      layer.appendChild(this.paintRedaction(r, vp));
    }
    // Selection ring + handles on top.
    const sel = this.selectedId ? this.doc.getAnnotation(this.selectedId) : null;
    if (sel && sel.page === page1) {
      const box = pdfRectToCss(vp, sel.rect);
      const ring = el("div", {
        cls: "pdf-selection",
        attrs: { "data-ann": sel.id, "aria-hidden": "true" },
        style: {
          left: (box.left - 1) + "px", top: (box.top - 1) + "px",
          width: (box.width + 2) + "px", height: (box.height + 2) + "px",
        },
      });
      layer.appendChild(ring);
      for (const edge of handleEdges(sel) ?? []) {
        const p = handlePoint(box, edge);
        layer.appendChild(el("div", {
          cls: "pdf-handle pdf-handle-" + edge,
          attrs: { "data-edge": edge, "data-ann": sel.id, role: "button", tabindex: "0",
            "aria-label": "Resize " + edge },
          style: { left: (p.x - 5) + "px", top: (p.y - 5) + "px" },
        }));
      }
      // Note popup if the note has text or is selected.
      if (sel.type === "note") layer.appendChild(this.notePopup(sel));
      if (sel.type === "text") layer.appendChild(this.textEditor(sel, vp));
    }
  }

  /** Paint a redaction rect as a distinct hatched removal marker (not an
   *  annotation). The reason label reads inside; the heavy border + hatch
   *  pattern signals "content removed here", not "box drawn here". */
  private paintRedaction(r: Redaction, vp: ViewportLike): HTMLElement {
    const box = pdfRectToCss(vp, r.rect);
    const node = el("div", {
      cls: "pdf-redact-rect",
      attrs: { "data-rid": r.id, "data-page": String(r.page), role: "img", "aria-label": `Redaction on page ${r.page}${r.reason ? ": " + r.reason : ""}` },
      style: {
        left: box.left + "px", top: box.top + "px",
        width: box.width + "px", height: box.height + "px",
      },
    });
    if (r.reason) {
      const label = el("span", { cls: "pdf-redact-rect-label", text: r.reason });
      node.appendChild(label);
    }
    return node;
  }

  private paintAnnotation(a: Annotation, vp: ViewportLike): HTMLElement | null {
    const box = pdfRectToCss(vp, a.rect);
    switch (a.type) {
      case "highlight": {
        // One semi-transparent rectangle per quad (multi-line selections).
        const wrap = el("div", { cls: "pdf-ann pdf-highlight-wrap", attrs: { "data-ann": a.id }, style: layerBox(box) });
        for (const q of a.quads) {
          const qb = pdfQuadToCss(vp, q);
          wrap.appendChild(el("div", {
            cls: "pdf-highlight-quad",
            style: {
              position: "absolute",
              left: (qb.left - box.left) + "px", top: (qb.top - box.top) + "px",
              width: qb.width + "px", height: qb.height + "px",
              background: a.color, opacity: String(a.opacity),
              mixBlendMode: "multiply",
            },
          }));
        }
        return wrap;
      }
      case "ink": {
        // Single SVG sized to the bounding box; polyline in local coords.
        const wrap = el("div", { cls: "pdf-ann pdf-ink", attrs: { "data-ann": a.id }, style: layerBox(box) });
        const svgNs = "http://www.w3.org/2000/svg";
        const svg = document.createElementNS(svgNs, "svg");
        svg.setAttribute("width", String(Math.max(1, Math.round(box.width))));
        svg.setAttribute("height", String(Math.max(1, Math.round(box.height))));
        svg.setAttribute("viewBox", `0 0 ${Math.max(1, box.width)} ${Math.max(1, box.height)}`);
        const poly = document.createElementNS(svgNs, "polyline");
        const pts = a.points.map((p) => `${(p[0] - a.rect[0]).toFixed(1)},${(a.rect[3] + a.rect[1] - p[1]).toFixed(1)}`).join(" ");
        poly.setAttribute("points", pts);
        poly.setAttribute("fill", "none");
        poly.setAttribute("stroke", a.color);
        poly.setAttribute("stroke-width", String(a.width));
        poly.setAttribute("stroke-linecap", "round");
        poly.setAttribute("stroke-linejoin", "round");
        svg.appendChild(poly);
        wrap.appendChild(svg);
        return wrap;
      }
      case "rect":
        return el("div", {
          cls: "pdf-ann pdf-shape", attrs: { "data-ann": a.id },
          style: { ...layerBox(box), border: `${a.width}px solid ${a.color}`, background: a.fill ?? "transparent", opacity: String(a.opacity) },
        });
      case "ellipse":
        return el("div", {
          cls: "pdf-ann pdf-shape", attrs: { "data-ann": a.id },
          style: { ...layerBox(box), border: `${a.width}px solid ${a.color}`, background: a.fill ?? "transparent", borderRadius: "50%", opacity: String(a.opacity) },
        });
      case "arrow": {
        const wrap = el("div", { cls: "pdf-ann pdf-arrow", attrs: { "data-ann": a.id }, style: layerBox(box) });
        const svgNs = "http://www.w3.org/2000/svg";
        const svg = document.createElementNS(svgNs, "svg");
        svg.setAttribute("width", String(Math.max(1, Math.round(box.width))));
        svg.setAttribute("height", String(Math.max(1, Math.round(box.height))));
        const line = document.createElementNS(svgNs, "line");
        line.setAttribute("x1", "0"); line.setAttribute("y1", String(box.height));
        line.setAttribute("x2", String(box.width)); line.setAttribute("y2", "0");
        line.setAttribute("stroke", a.color); line.setAttribute("stroke-width", String(a.width));
        const head = document.createElementNS(svgNs, "polygon");
        const hs = Math.max(6, a.width * 3);
        head.setAttribute("points", `0,0 ${-hs},${-hs / 2} ${-hs},${hs / 2}`);
        head.setAttribute("fill", a.color);
        head.setAttribute("transform", `translate(${box.width},0)`);
        svg.appendChild(line); svg.appendChild(head);
        wrap.appendChild(svg);
        return wrap;
      }
      case "text":
        return el("div", {
          cls: "pdf-ann pdf-text-ann", attrs: { "data-ann": a.id }, text: a.text,
          style: { ...layerBox(box), color: a.color, fontSize: a.fontSize + "px", padding: "2px" },
        });
      case "note":
        // Anchor icon; the popup appears when selected.
        return el("div", {
          cls: "pdf-ann pdf-note", attrs: { "data-ann": a.id, title: a.text || "Note" },
          style: { ...layerBox(box), background: a.color, borderColor: "var(--color-border-strong, #999)" },
        });
      case "stamp": {
        const wrap = el("div", { cls: "pdf-ann pdf-stamp", attrs: { "data-ann": a.id }, style: layerBox(box) });
        const img = el("img", { attrs: { src: a.data, alt: a.label || "stamp" } }) as HTMLImageElement;
        img.style.width = "100%"; img.style.height = "100%";
        img.style.objectFit = "contain";
        wrap.appendChild(img);
        return wrap;
      }
      default:
        return null;
    }
  }

  private notePopup(a: Annotation & { type: "note" }): HTMLElement {
    const box = pdfRectToCss(this.curViewport(a.page), a.rect);
    const ta = el("textarea", { cls: "pdf-note-editor", attrs: { rows: "3" } }) as HTMLTextAreaElement;
    ta.value = a.text;
    const commit = () => this.doc.apply(new UpdateAnnotationCommand(a.id, (x) => {
      if (x.type === "note") x.text = ta.value;
    }));
    ta.addEventListener("input", commit);
    ta.addEventListener("change", commit);
    return el("div", {
      cls: "pdf-note-popup", role: "dialog", ariaLabel: "Note text",
      style: { left: (box.left + box.width + 6) + "px", top: box.top + "px" },
    }, [ta]);
  }

  private textEditor(a: Annotation & { type: "text" }, _vp: ViewportLike): HTMLElement {
    const box = pdfRectToCss(this.curViewport(a.page), a.rect);
    const input = el("input", { type: "text", cls: "pdf-text-editor", attrs: { value: a.text } }) as HTMLInputElement;
    input.value = a.text;
    input.style.width = box.width + "px";
    const commit = () => this.doc.apply(new UpdateAnnotationCommand(a.id, (x) => {
      if (x.type === "text") x.text = input.value;
    }));
    input.addEventListener("input", commit);
    input.addEventListener("change", commit);
    return el("div", {
      cls: "pdf-text-edit-wrap",
      style: { left: box.left + "px", top: box.top + "px" },
    }, [input]);
  }

  private curViewport(page1: number): ViewportLike {
    const vp = this.host.getViewport(page1);
    // Fall back to an identity viewport so paint never throws before the first
    // render; renderPage is only called on rendered pages anyway.
    if (!vp) return identityViewport();
    return vp;
  }
}

// --- helpers (module-private) ----------------------------------------------

function layerBox(b: { left: number; top: number; width: number; height: number }): Record<string, string> {
  return {
    position: "absolute",
    left: b.left + "px", top: b.top + "px", width: b.width + "px", height: b.height + "px",
  };
}

function inkBoundingRect(pts: Array<[number, number]>): PdfRect {
  if (pts.length === 0) return [0, 0, 1, 1];
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const [x, y] of pts) {
    if (x < minX) minX = x; if (y < minY) minY = y;
    if (x > maxX) maxX = x; if (y > maxY) maxY = y;
  }
  return [minX, minY, Math.max(1, maxX - minX), Math.max(1, maxY - minY)];
}

function handleEdges(a: Annotation): HandleEdge[] | null {
  // Only resizable types get the full 8-handle set; ink/note are move-only.
  if (a.type === "ink" || a.type === "note") return null;
  return ["nw", "n", "ne", "e", "se", "s", "sw", "w"];
}

function handlePoint(box: { left: number; top: number; width: number; height: number }, edge: HandleEdge): { x: number; y: number } {
  const cx = box.left + box.width / 2, cy = box.top + box.height / 2;
  const ex = edge.includes("e") ? box.left + box.width : edge.includes("w") ? box.left : cx;
  const ey = edge.includes("s") ? box.top + box.height : edge.includes("n") ? box.top : cy;
  return { x: ex, y: ey };
}

function identityViewport(): ViewportLike {
  return {
    convertToViewportPoint: (x, y) => [x, -y],
    convertToPdfPoint: (X, Y) => [X, -Y],
  };
}

// Render a typed name to a PNG data URL using a standard sans-serif font.
// Standard-14 only at export; here we just rasterise for the in-frame preview
// and the export path embeds the same text via pdf-lib's Helvetica.
function renderTextStamp(text: string): { mime: string; data: string } {
  const w = 320, h = 80;
  const c = document.createElement("canvas");
  c.width = w; c.height = h;
  const ctx = c.getContext("2d");
  if (!ctx) return { mime: "image/png", data: transparentPng() };
  ctx.clearRect(0, 0, w, h);
  ctx.fillStyle = "#111";
  ctx.font = "italic 36px Helvetica, Arial, sans-serif";
  ctx.textBaseline = "middle";
  ctx.fillText(text, 8, h / 2);
  return { mime: "image/png", data: c.toDataURL("image/png") };
}

function transparentPng(): string {
  // 1×1 transparent PNG fallback if canvas 2D is unavailable.
  return "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==";
}
