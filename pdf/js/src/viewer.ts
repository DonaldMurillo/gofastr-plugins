// GoFastr PDF viewer — in-frame entry point (protocol v1, schema pdf-v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY. Never touches host cookies,
// localStorage, or the host DOM. The PDF bytes arrive OVER THE BRIDGE (host
// adapter fetches same-origin, forwards the ArrayBuffer); the frame has
// connect-src 'none' and fetches nothing.
//
// Worker-free pdf.js: the worker module's WorkerMessageHandler is imported on
// the main thread and assigned to globalThis.pdfjsWorker, so pdf.js takes its
// FAKE-WORKER path with ZERO Worker spawn / fetch / blob. This is the
// load-bearing trick under the framed CSP. See pdfdoc.ts for the no-wasm image
// codec path (also load-bearing for scanned documents).
//
// This module orchestrates: virtualized continuous scroll, zoom, rotate, text
// layer, annotations, search, thumbnails, outline, keyboard, and a11y — built
// from the focused modules under src/. It preserves the spike's bridge/protocol
// and the __pdf* mirrors the Go test + webkit probe read.

import { WorkerMessageHandler } from "pdfjs-dist/build/pdf.worker.mjs";
import { version as pdfjsVersion } from "pdfjs-dist";
import type { PageViewport, PDFPageProxy } from "pdfjs-dist";

import { createRouter, sendEvent, type HandlerMap } from "./protocol";
import { el, applyTokens, hasTokens } from "./dom";
import { state, SCHEMA_VERSION, VIEWER_VERSION } from "./state";
import { isolationProbes, probeCaps } from "./probes";
import { loadDocument, type PdfModel } from "./pdfdoc";
import { startRender, isCancelled, sampleCanvas, clearCanvas } from "./render";
import { buildTextLayer, castTextContent, type PdfTextContent, type TextSpanRef } from "./textlayer";
import { buildAnnotLayer } from "./annots";
import { SearchController, type SearchStatus } from "./search";
import { Sidebar } from "./sidebar";
import { Toolbar, type ToolbarCallbacks } from "./toolbar";
import { DocumentBytesAssembler, isDocumentBytesChunk } from "./docbytes";
import {
  OverlayDoc, serializeOverlay, deserializeOverlay, type OverlayState,
  AddAnnotationCommand, SetFormFieldCommand, AddPageOpCommand,
} from "./doc";
import { createEditor, type Editor, type EditorHost } from "./overlay-editor";
import { pdfSha256 } from "./sha";
import { buildFormLayer } from "./forms";
// path. Assigned once at module load, before any getDocument() call.
window.pdfjsWorker = { WorkerMessageHandler };

// --- layout constants ------------------------------------------------------

const PAGE_PAD = 16;          // padding around the pages stack (horizontal)
const PAGE_GAP = 14;          // vertical gap between page slots
const RENDER_MARGIN = 1.0;    // render pages within this many viewports above/below
const EVICT_MARGIN = 2.5;     // evict pages farther than this many viewports
const ZOOM_STEPS = [50, 75, 100, 125, 150, 200, 300, 400];
const MIN_SCALE = 0.25;
const MAX_SCALE = 4.0;
const READY_MIN_HEIGHT = 480;
const DESIRED_FRAME_HEIGHT = 640;

type ZoomMode = "fit-width" | "fit-page" | "custom";

interface PageRuntime {
  slot: HTMLElement;
  canvas: HTMLCanvasElement;
  textLayer: HTMLElement;
  annotLayer: HTMLElement;
  editLayer: HTMLElement;      // editor overlay: tools paint + selection here
  formLayer: HTMLElement;      // AcroForm fill inputs (annotate mode)
  placeholder: HTMLElement;
  spans: TextSpanRef[];
  gen: number;           // render generation; stale completions are ignored
  rendered: boolean;
  textContent: PdfTextContent | null;
  annotBuilt: boolean;
}

// --- the viewer ------------------------------------------------------------

class PdfViewer {
  private readonly scrollEl: HTMLElement;
  private readonly pagesEl: HTMLElement;
  private readonly rootEl: HTMLElement;
  private readonly toolbar: Toolbar;
  private readonly sidebar: Sidebar;
  private readonly search: SearchController;

  private model: PdfModel | null = null;
  private pages: PageRuntime[] = [];
  private cumTops: number[] = [];      // cumTops[i] = pixel top of page i (0-based)
  private totalHeight = 0;

  private zoomMode: ZoomMode = "fit-width";
  private zoomPct = 100;
  private scale = 1;
  private rotation = 0;
  private currentPage = 1;

  private renderQueue: number[] = [];
  private inFlight: { pageIndex: number; task: { cancel: () => void }; gen: number } | null = null;
  private scrollRaf = 0;
  private pinchStartDist = 0;
  private pinchStartPct = 100;
  private page1Emitted = false;
  // P2 editing surface. The OverlayDoc is the single source of truth for
  // annotations/forms/pageOps; the assembler reassembles the chunked
  // documentBytes stream. `loading` + `state.rendered` is the guard that
  // makes the FIRST delivery win (chunked OR legacy loadBytes).
  readonly doc = new OverlayDoc();
  private readonly docBytes: DocumentBytesAssembler;
  private loading = false;
  private mode: "view" | "annotate" | "redact" = "view";
  private canWrite = false;
  private canExport = false;
  private docChangedTimer = 0;
  private editorMounted = false;
  private editor: Editor | null = null;
  private exportSeq = 0;
  private sourceBytes: Uint8Array = new Uint8Array(0);
  private readonly pendingExports = new Map<string, { filename: string; started: number; watchdog?: number }>();

  constructor() {
    this.rootEl = el("div", { id: "pdf-app", cls: "pdf-app" });
    this.scrollEl = el("div", { id: "pdf-scroll", cls: "pdf-scroll", role: "region", ariaLabel: "PDF document", tabIndex: 0 });
    this.pagesEl = el("div", { id: "pdf-pages", cls: "pdf-pages" });
    this.scrollEl.appendChild(this.pagesEl);

    const cb: ToolbarCallbacks = {
      onSidebarToggle: () => { this.sidebar.toggle(); this.toolbar.setSidebarOpen(this.sidebar.isOpen()); },
      onPrevPage: () => this.gotoPage(this.currentPage - 1),
      onNextPage: () => this.gotoPage(this.currentPage + 1),
      onGotoPage: (n) => this.gotoPage(n),
      onZoomIn: () => this.zoomIn(),
      onZoomOut: () => this.zoomOut(),
      onCycleZoomMode: () => this.cycleZoomMode(),
      onRotate: () => this.rotate(),
      onSearch: (q) => { void this.onSearch(q); },
      onSearchNext: () => { void this.search.next(); },
      onSearchPrev: () => { void this.search.prev(); },
      onSearchClear: () => { void this.search.clear(); },
    };
    this.toolbar = new Toolbar(cb);
    this.sidebar = new Sidebar(this.modelStub(), { onJump: (p) => this.gotoPage(p) });
    this.search = new SearchController(
      this.modelStub(),
      (page) => this.gotoPage(page),
      (s) => this.onSearchStatus(s)
    );
    this.rootEl.appendChild(this.toolbar.root);
    const body = el("div", { id: "pdf-body", cls: "pdf-body" }, [this.sidebar.root, this.scrollEl]);
    this.rootEl.appendChild(body);

    // Chunked documentBytes assembler. onReady loads; onError surfaces a
    // clear renderError rather than hanging on a spinner.
    this.docBytes = new DocumentBytesAssembler(
      (bytes) => { void this.loadFromBytes(bytes, "documentBytes"); },
      (message) => {
        state.error = message;
        sendEvent("renderError", { message });
        this.toolbar.setStatus(message, "error");
      },
    );
    // Doc mutations → debounced docChanged (gated on document:write) + the
    // state mirrors the e2e suite reads.
    this.doc.subscribe(() => this.onDocChanged());
  }

  // The model isn't known until the document loads; controllers hold a reference
  // they read lazily. This stub forwards to the live model once set. Every
  // property/method access on the proxy re-routes through the real instance.
  private modelStub(): PdfModel {
    const stub = { pageCount: 0 } as unknown as PdfModel;
    const self = this;
    return new Proxy(stub, {
      get(_t, prop) {
        if (self.model) return Reflect.get(self.model, prop);
        if (prop === "pageCount") return 0;
        return undefined;
      },
    }) as PdfModel;
  }

  attach(parent: HTMLElement): void {
    parent.appendChild(this.rootEl);
    this.scrollEl.addEventListener("scroll", () => this.onScrollThrottled(), { passive: true });
    // Recompute fit-width/fit-page when the scroll box resizes (window resize
    // OR sidebar toggle). ResizeObserver fires on border-box changes only, so
    // ordinary scrolling — which does not change the box — never relayouts.
    new ResizeObserver(() => this.relayout()).observe(this.scrollEl);
    this.scrollEl.addEventListener("keydown", (e) => this.onKey(e as KeyboardEvent));
    this.scrollEl.addEventListener("touchstart", (e) => this.onTouchStart(e as TouchEvent), { passive: true });
    this.scrollEl.addEventListener("touchmove", (e) => this.onTouchMove(e as TouchEvent), { passive: false });
    this.scrollEl.addEventListener("touchend", () => this.onTouchEnd(), { passive: true });
  }

  // Re-derive scale (fit modes depend on the live client width) and re-render
  // the visible pages. Custom-zoom scale is pct-based, so only fit modes react.
  private relayout(): void {
    if (!this.pagesReady()) return;
    if (this.zoomMode !== "custom") {
      const before = this.scale;
      this.scale = this.computeScale();
      if (Math.abs(this.scale - before) > 1e-4) {
        for (const rt of this.pages) rt.gen++;
        this.cancelInFlight();
        this.renderQueue = [];
        this.computeLayout(true);
        for (let i = 0; i < this.pages.length; i++) this.evictPage(i);
      }
    }
    this.onScrollThrottled();
  }

  // --- protocol ------------------------------------------------------------

  onInit(params: unknown): void {
    if (hasTokens(params)) applyTokens(params.tokens);
    this.applyModeFromInit(params);
    this.applyCapabilitiesFromInit(params);
    this.applyDocFromInit(params);
  }

  onThemeChanged(params: unknown): void {
    if (hasTokens(params)) applyTokens(params.tokens);
  }

  // documentBytes: the chunked delivery path (the target contract). The
  // assembler collects chunks by reqId, orders by seq, completes at total,
  // and hands the concatenated bytes to loadFromBytes. Out-of-order and
  // duplicate chunks are defended in the assembler; an incomplete stream
  // surfaces a clear error instead of hanging on a spinner.
  onDocumentBytes(params: unknown): void {
    if (!isDocumentBytesChunk(params)) return;
    this.docBytes.push(params);
  }

  // loadBytes: the legacy single-shot delivery. The adapter STILL emits this
  // after the chunks as a backward-compat fallback; the loading||rendered guard
  // ensures only the FIRST delivery renders, so a frame that handled the
  // chunked path ignores the late loadBytes (and vice-versa). Once the host
  // drops the fallback this stays as a tolerant no-op for older hosts.
  async onLoadBytes(params: unknown): Promise<void> {
    const bytes = asBytes(params);
    if (!bytes) {
      const msg = "loadBytes: missing {bytes}";
      state.error = msg;
      sendEvent("renderError", { message: msg });
      this.toolbar.setStatus(msg, "error");
      return;
    }
    await this.loadFromBytes(bytes, "loadBytes");
  }

  // Shared load path. `source` labels status/error messages so an assembler
  // timeout is distinguishable from a legacy-load failure in the host mirror.
  private async loadFromBytes(bytes: Uint8Array, source: string): Promise<void> {
    if (this.loading || state.rendered) return; // FIRST delivery wins
    this.loading = true;
    try {
      this.toolbar.setStatus("Opening document…", "loading");
      // pdf.js transfers (detaches) the underlying ArrayBuffer of the bytes
      // handed to getDocument — keep our own copy for the export path, and
      // give pdf.js a separate copy so sourceBytes stays intact.
      this.sourceBytes = bytes.slice();
      const model = await loadDocument(bytes.slice());
      this.model = model;
      this.bindOverlaySrc(this.sourceBytes, model.pageCount);
      this.toolbar.setStatus("Preparing pages…", "loading");
      await model.loadAllPages((loaded, total) => this.toolbar.setProgress(loaded, total));
      this.toolbar.setProgress(0, 0);
      this.mountPages();
      this.computeLayout(true);
      this.sidebar.setOutline(await model.getOutline());
      this.sidebar.buildThumbs();
      // Kick off the first render (page 1, in view) then the render loop.
      this.gotoPage(1, true);
      this.onScrollThrottled();
      sendEvent("resize", { height: DESIRED_FRAME_HEIGHT });
    } catch (e: unknown) {
      const msg = source + ": " + (e instanceof Error ? e.message : String(e));
      state.error = msg;
      sendEvent("renderError", { message: msg });
      this.toolbar.setStatus("Failed to open document: " + msg, "error");
    } finally {
      this.loading = false;
    }
  }

  // Bind the overlay's src to the loaded bytes (sha256 + page count). Best-
  // effort: a mismatch is a soft warning in the doc model, never a hard fail,
  // so a missing crypto.subtle (plain http to a non-localhost host) degrades
  // to a non-cryptographic fingerprint without opening any hole.
  private bindOverlaySrc(bytes: Uint8Array, pageCount: number): void {
    void pdfSha256(bytes).then((sha) => {
      this.doc.setStateSrc({ sha256: sha, pageCount });
    });
  }

  private applyModeFromInit(params: unknown): void {
    let mode: "view" | "annotate" | "redact" = "view";
    if (params && typeof params === "object") {
      const cfg = (params as { config?: unknown }).config;
      if (cfg && typeof cfg === "object") {
        const m = (cfg as { mode?: unknown }).mode;
        if (m === "view" || m === "annotate" || m === "redact") mode = m;
      }
    }
    this.mode = mode;
    state.mode = mode;
    // The editing surface (overlay layer + edit toolbar) is constructed once
    // the mode is known; annotate/redact get it, view does not. Redaction's
    // own tools are P3 — the seam is left obvious in EditToolbar.
    this.mountEditorIfGranted();
  }

  private applyCapabilitiesFromInit(params: unknown): void {
    // docChanged is gated on document:write. pdf:export is enforced by the Go
    // /export route (E_CAPABILITY_DENIED if not granted); the adapter's
    // DEFAULT_CAPS does not advertise it, so the frame enables the button in
    // annotate/redact and surfaces the Go-side denial as an error — matching
    // "UI gating is a convenience" (brief §6).
    let caps: unknown[] = [];
    if (params && typeof params === "object") {
      const c = (params as { capabilities?: unknown }).capabilities;
      if (Array.isArray(c)) caps = c;
    }
    this.canWrite = caps.includes("document:write");
    this.canExport = caps.includes("pdf:export") || this.mode !== "view";
    if (this.editor) this.editor.syncDocState();
  }


  private applyDocFromInit(params: unknown): void {
    // The host may round-trip a previously-saved overlay via init.doc. Parse
    // it into the live OverlayDoc so edits layer on top of saved state.
    if (!params || typeof params !== "object") return;
    const doc = (params as { doc?: unknown }).doc;
    if (!doc) return;
    const loaded = deserializeOverlay(doc);
    for (const a of loaded.annotations) this.doc.apply(new AddAnnotationCommand(a));
    for (const [k, v] of loaded.formFields) this.doc.apply(new SetFormFieldCommand(k, v.v));
    for (const op of loaded.pageOps) this.doc.apply(new AddPageOpCommand(op));
    this.doc.state.rev = loaded.rev;
    // Loaded edits are not "dirty" — they are the persisted baseline.
    this.doc.markClean();
  }

  private onDocChanged(): void {
    state.annotationCount = this.doc.state.annotations.length;
    state.dirty = this.doc.isDirty();
    state.undoDepth = this.doc.undoDepth();
    if (this.editor) this.editor.syncDocState();
    // Debounce: a burst of edits (typing, dragging a handle) emits ONE
    // docChanged when the user pauses. Gated on document:write so a host
    // that did not grant the cap never receives events it would drop.
    if (!this.canWrite) return;
    if (this.docChangedTimer) window.clearTimeout(this.docChangedTimer);
    this.docChangedTimer = window.setTimeout(() => {
      this.docChangedTimer = 0;
      const payload = serializeOverlay(this.doc.state);
      sendEvent("docChanged", {
        doc: payload,
        markdown: null,
        dirty: this.doc.isDirty(),
        rev: this.doc.state.rev,
      });
    }, 250);
  }

  private mountEditorIfGranted(): void {
    // P2 seam: annotate/redact get the overlay layer + edit toolbar; view
    // does not. Redaction-specific tools (P3) branch on this.mode === "redact"
    // inside EditToolbar — left as an obvious seam, not wired here.
    if (this.editorMounted) return;
    if (this.mode !== "annotate" && this.mode !== "redact") return;
    this.editorMounted = true;
    this.editor = createEditor(this.doc, {
      getModel: () => this.model,
      getViewport: (page1) => this.model ? this.model.viewportFor(page1 - 1, this.scale, this.rotation) : null,
      getEditSurface: (page1) => {
        const rt = this.pages[page1 - 1];
        if (!rt || !rt.rendered) return null;
        const vp = this.model ? this.model.viewportFor(page1 - 1, this.scale, this.rotation) : null;
        if (!vp) return null;
        return { layer: rt.editLayer, viewport: vp };
      },
      forEachPage: (cb) => {
        for (let i = 0; i < this.pages.length; i++) {
          const rt = this.pages[i];
          if (rt && rt.rendered) cb(i + 1, rt.editLayer);
        }
      },
      pagesEl: this.pagesEl,
      getBytes: () => this.sourceBytes,
      getCurrentPage: () => this.currentPage,
      canExport: () => this.canExport,
      mode: this.mode,
      setStatus: (msg, kind) => this.toolbar.setStatus(msg, kind),
      onExportBytes: (bytes, kind, filename) => this.requestExport(bytes, kind, filename),
      invalidateOverlays: () => this.invalidateOverlayLayer(),
    });
    this.rootEl.insertBefore(this.editor.toolbar.root, this.rootEl.firstChild);
  }

  // --- page slots ----------------------------------------------------------

  private mountPages(): void {
    if (!this.model) return;
    const n = this.model.pageCount;
    this.pagesEl.replaceChildren();
    this.pages = new Array(n);
    this.cumTops = new Array(n + 1).fill(0);
    for (let i = 0; i < n; i++) {
      const canvas = el("canvas", { cls: "pdf-canvas", attrs: { "aria-hidden": "true" } });
      const textLayer = el("div", { cls: "text-layer", role: "presentation" });
      const annotLayer = el("div", { cls: "annot-layer", role: "presentation" });
      const editLayer = el("div", { cls: "edit-layer", role: "presentation" });
      const formLayer = el("div", { cls: "form-layer", role: "presentation" });
      const placeholder = el("div", { cls: "pdf-page-placeholder", text: "Page " + (i + 1), attrs: { "aria-hidden": "true" } });
      const inner = el("div", { cls: "pdf-page-inner" }, [canvas, textLayer, annotLayer, editLayer, formLayer, placeholder]);
      const slot = el("div", {
        cls: "pdf-page",
        attrs: { "data-page": String(i + 1) },
        style: { width: "0px", height: "0px" },
      }, [inner]);
      this.pagesEl.appendChild(slot);
      this.pages[i] = { slot, canvas, textLayer, annotLayer, editLayer, formLayer, placeholder, spans: [], gen: 0, rendered: false, textContent: null, annotBuilt: false };
    }
  }

  // Recompute every page's CSS box at the current scale/rotation and the
  // cumulative tops. When `anchor` is true, scroll is re-anchored to keep the
  // current page at the same relative position (so zoom/rotate don't jump).
  private computeLayout(anchor: boolean): void {
    if (!this.pagesReady()) return;
    const prevPage = this.currentPage - 1;
    const prevTop = this.cumTops[prevPage] ?? 0;
    const prevH = this.pages[prevPage]?.slot.offsetHeight ?? 1;
    const ratio = prevH > 0 ? (this.scrollEl.scrollTop - prevTop) / prevH : 0;

    // Local binding: pagesReady() proved this.model is set, but a method call
    // cannot narrow a field for TypeScript, and a non-null assertion would
    // silence the checker rather than satisfy it.
    const model = this.model;
    if (!model) return;

    let y = 0;
    for (let i = 0; i < this.pages.length; i++) {
      const rt = this.pages[i];
      const g = model.geom(i, this.scale, this.rotation);
      rt.slot.style.width = Math.floor(g.cssW) + "px";
      rt.slot.style.height = Math.floor(g.cssH) + "px";
      this.cumTops[i] = y;
      y += Math.floor(g.cssH) + PAGE_GAP;
    }
    this.cumTops[this.pages.length] = y;
    this.totalHeight = y;
    this.pagesEl.style.height = y + "px";

    if (anchor) {
      const newTop = this.cumTops[prevPage] ?? 0;
      const newH = this.pages[prevPage]?.slot.offsetHeight ?? 1;
      this.scrollEl.scrollTop = newTop + ratio * newH;
    }
  }

  // --- scroll / visibility -------------------------------------------------

  private pageAtOffset(y: number): number {
    if (!this.model) return 0;
    const n = this.model.pageCount;
    // binary search for the largest i with cumTops[i] <= y
    let lo = 0, hi = n - 1, res = 0;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (this.cumTops[mid] <= y) { res = mid; lo = mid + 1; }
      else hi = mid - 1;
    }
    return res;
  }

  private onScrollThrottled(): void {
    if (this.scrollRaf) return;
    this.scrollRaf = window.requestAnimationFrame(() => {
      this.scrollRaf = 0;
      this.afterScroll();
    });
  }

  private afterScroll(): void {
    if (!this.model) return;
    const top = this.scrollEl.scrollTop;
    const vh = this.scrollEl.clientHeight;
    const center = top + vh * 0.4;
    const page = this.pageAtOffset(center) + 1;
    if (page !== this.currentPage) {
      this.currentPage = page;
      this.toolbar.setPage(page, this.model.pageCount);
      this.sidebar.setCurrentPage(page);
      state.currentPage = page;
      // fit-width/fit-page track the page in view: relayout when it changes.
      if (this.zoomMode !== "custom") {
        const before = this.scale;
        this.scale = this.computeScale();
        if (Math.abs(this.scale - before) > 1e-4) {
          this.computeLayout(true);
        }
      }
      this.emitViewChanged();
    }
    this.scheduleRenders();
    this.evictFar();
  }

  private scheduleRenders(): void {
    if (!this.pagesReady()) return;
    const top = this.scrollEl.scrollTop;
    const vh = this.scrollEl.clientHeight;
    const lo = this.pageAtOffset(top - vh * RENDER_MARGIN);
    const hi = this.pageAtOffset(top + vh + vh * RENDER_MARGIN);
    const center = top + vh / 2;
    const want: number[] = [];
    // Bound by the RUNTIME array, not model.pageCount. The two disagree for a
    // moment after the model lands and before the page slots are built, and a
    // ResizeObserver or scroll firing in that window would index past the end.
    // (The chunked documentBytes path shifted that timing enough to hit it —
    // the old single-shot load happened to close the gap by luck.)
    const last = Math.min(hi, this.pages.length - 1);
    for (let i = Math.max(0, lo); i <= last; i++) {
      if (!this.pages[i]?.rendered) want.push(i);
    }
    // Prioritize by distance to viewport center.
    want.sort((a, b) => {
      const ca = this.cumTops[a] + (this.pages[a]?.slot.offsetHeight || 0) / 2 - center;
      const cb = this.cumTops[b] + (this.pages[b]?.slot.offsetHeight || 0) / 2 - center;
      return Math.abs(ca) - Math.abs(cb);
    });
    this.renderQueue = want;
    this.pumpRender();
  }

  // True once the model AND its page runtimes both exist and agree. Several
  // loops walk model.pageCount while indexing this.pages, and the two disagree
  // for a window after the document lands and before the slots are built — a
  // ResizeObserver or a scroll firing in that window used to throw. One guard,
  // checked at every entry point, rather than optional-chaining each access.
  private pagesReady(): boolean {
    return !!this.model && this.pages.length === this.model.pageCount;
  }

  private evictFar(): void {
    if (!this.pagesReady()) return;
    const top = this.scrollEl.scrollTop;
    const vh = this.scrollEl.clientHeight;
    const lo = this.pageAtOffset(top - vh * EVICT_MARGIN);
    const hi = this.pageAtOffset(top + vh + vh * EVICT_MARGIN);
    for (let i = 0; i < this.pages.length; i++) {
      if (i >= lo && i <= hi) continue;
      const rt = this.pages[i];
      if (!rt.rendered && !this.renderQueue.includes(i) && (!this.inFlight || this.inFlight.pageIndex !== i)) continue;
      if (this.inFlight && this.inFlight.pageIndex === i) {
        try { this.inFlight.task.cancel(); } catch { /* ignore */ }
        this.inFlight = null;
      }
      this.evictPage(i);
    }
    // Cancel a queued/started render that is no longer wanted.
    if (this.inFlight) {
      const idx = this.inFlight.pageIndex;
      if (idx < lo || idx > hi) {
        try { this.inFlight.task.cancel(); } catch { /* ignore */ }
      }
    }
  }

  private evictPage(i: number): void {
    const rt = this.pages[i];
    if (!rt) return;
    rt.gen++;
    rt.rendered = false;
    rt.textContent = null;
    rt.annotBuilt = false;
    rt.spans = [];
    clearCanvas(rt.canvas);
    rt.textLayer.replaceChildren();
    rt.annotLayer.replaceChildren();
    rt.editLayer.replaceChildren();
    rt.formLayer.replaceChildren();
    rt.placeholder.removeAttribute("hidden");
  }

  // --- render loop ---------------------------------------------------------


  // Cancel any in-flight render task and clear the slot. Relayout/zoom must
  // call this BEFORE nulling inFlight, otherwise pumpRender starts a second
  // render on the same canvas and pdf.js throws "same canvas during multiple
  // render() operations".
  private cancelInFlight(): void {
    if (this.inFlight) {
      try { this.inFlight.task.cancel(); } catch { /* ignore */ }
      this.inFlight = null;
    }
  }
  private pumpRender(): void {
    if (this.inFlight) return;
    if (!this.model || this.renderQueue.length === 0) return;
    const pageIndex = this.renderQueue.shift()!;
    const rt = this.pages[pageIndex];
    if (!rt || rt.rendered) { this.pumpRender(); return; }
    const page = this.model.getPage(pageIndex);
    if (!page) { this.pumpRender(); return; }
    const viewport = this.model.viewportFor(pageIndex, this.scale, this.rotation);
    rt.gen++;
    const gen = rt.gen;
    rt.placeholder.removeAttribute("hidden");
    const { task } = startRender(page, rt.canvas, viewport);
    this.inFlight = { pageIndex, task: { cancel: () => task.cancel() }, gen };
    task.promise.then(
      () => this.onRenderDone(pageIndex, page, viewport, gen),
      (e: unknown) => this.onRenderError(pageIndex, e, gen)
    );
  }

  private async onRenderDone(pageIndex: number, page: PDFPageProxy, viewport: PageViewport, gen: number): Promise<void> {
    const rt = this.pages[pageIndex];
    this.inFlight = null;
    if (!rt || rt.gen !== gen) { this.pumpRender(); return; } // superseded/evicted
    rt.rendered = true;
    rt.placeholder.setAttribute("hidden", "hidden");

    // Text layer (selection + a11y) + annotation layer (links).
    let text = "";
    try {
      const tc = (await page.getTextContent()) as unknown as PdfTextContent;
      rt.textContent = tc;
      const built = buildTextLayer(castTextContent(tc), viewport, rt.textLayer);
      rt.spans = built.spans;
      text = built.text;
    } catch {
      rt.spans = [];
    }
    try {
      await buildAnnotLayer(page, viewport, rt.annotLayer, async (dest) => {
        if (this.model) {
          const p = await this.model.resolveDest(dest);
          if (p != null) this.gotoPage(p);
        }
      });
      rt.annotBuilt = true;
    } catch { /* best-effort */ }

    // Paint editor overlays + form inputs for this page now that its surface
    // is ready. Forms only render in annotate/redact (the fill UI); view skips.
    if (this.editor) this.editor.renderPage(pageIndex);
    if (this.mode !== "view") {
      void buildFormLayer(page, viewport, rt.formLayer, { doc: this.doc });
    }

    // Apply a live search highlight if the active match is on this page.
    this.search.applyHighlight(pageIndex, rt.spans);

    // Page-1 regression contract: emit `rendered` once with page-1 text + sample.
    if (pageIndex === 0 && !this.page1Emitted) {
      this.page1Emitted = true;
      this.emitPage1Rendered(page, rt, text);
    }

    this.pumpRender();
  }

  private onRenderError(pageIndex: number, e: unknown, gen: number): void {
    const rt = this.pages[pageIndex];
    this.inFlight = null;
    if (rt && rt.gen === gen) {
      if (isCancelled(e)) {
        // Benign: superseded by a newer render or evicted on fast scroll.
      } else {
        const msg = e instanceof Error ? e.message : String(e);
        rt.placeholder.textContent = "Page " + (pageIndex + 1) + " failed to render: " + msg;
        rt.placeholder.removeAttribute("hidden");
        rt.placeholder.classList.add("is-error");
        // Page-1 failure must surface (never a silently blank page).
        if (pageIndex === 0 && !this.page1Emitted) {
          this.page1Emitted = true;
          state.error = msg;
          sendEvent("renderError", { message: msg });
          this.toolbar.setStatus("Page 1 failed: " + msg, "error");
        }
      }
    }
    this.pumpRender();
  }

  private emitPage1Rendered(page: PDFPageProxy, rt: PageRuntime, text: string): void {
    let nonBlank = false;
    let nonWhitePixels = 0;
    const ctx = rt.canvas.getContext("2d");
    if (ctx) {
      const s = sampleCanvas(ctx, rt.canvas.width, rt.canvas.height);
      nonBlank = s.nonBlank;
      nonWhitePixels = s.nonWhitePixels;
    }
    const cssW = rt.slot.offsetWidth;
    const cssH = rt.slot.offsetHeight;
    state.rendered = true;
    state.text = text;
    state.pageCount = this.model?.pageCount ?? 0;
    state.nonBlank = nonBlank;
    state.nonWhitePixels = nonWhitePixels;
    state.widthPx = cssW;
    state.heightPx = cssH;
    sendEvent("rendered", {
      pageCount: state.pageCount,
      text,
      spanCount: rt.spans.length,
      nonBlank,
      nonWhitePixels,
      widthPx: cssW,
      heightPx: cssH,
      pdfjsVersion,
    });
    this.toolbar.setStatus(
      "Page 1 of " + state.pageCount + " rendered (" + nonWhitePixels + " inked px)",
      "ready"
    );
    // Empirically confirm what the sandbox blocks (print/download/clipboard);
    // the adapter mirrors it onto frame.__pdfCaps.
    void probeCaps().then(
      (caps) => {
        state.caps = caps;
        sendEvent("caps", {
          hasPrint: caps.hasPrint,
          clipboardWrite: caps.clipboardWrite,
          allowedFeatures: caps.allowedFeatures,
          origin: caps.origin,
        });
      },
      () => { /* never reject unhandled */ }
    );
    void page;
  }

  // --- zoom / rotate -------------------------------------------------------

  private computeScale(): number {
    if (!this.model) return 1;
    const i = this.currentPage - 1;
    const vp1 = this.model.viewportFor(i, 1, this.rotation);
    const availW = Math.max(64, this.scrollEl.clientWidth - 2 * PAGE_PAD);
    const availH = Math.max(64, this.scrollEl.clientHeight - 2 * PAGE_PAD);
    let s: number;
    if (this.zoomMode === "fit-width") s = availW / vp1.width;
    else if (this.zoomMode === "fit-page") s = Math.min(availW / vp1.width, availH / vp1.height);
    else s = this.zoomPct / 100;
    return Math.max(MIN_SCALE, Math.min(MAX_SCALE, s));
  }

  private applyZoomChange(anchor: boolean): void {
    this.scale = this.computeScale();
    // Invalidate all in-flight/queued renders (geometry changed).
    for (const rt of this.pages) rt.gen++;
    this.cancelInFlight();
    this.renderQueue = [];
    this.computeLayout(anchor);
    // Mark all pages unrendered (their canvas is now the wrong size); re-render.
    for (let i = 0; i < this.pages.length; i++) this.evictPage(i);
    this.onScrollThrottled();
    this.toolbar.setZoomLabel(this.zoomLabel());
    state.zoom = this.zoomMode === "custom" ? this.zoomPct : this.zoomMode;
    this.emitViewChanged();
  }

  private zoomLabel(): string {
    if (this.zoomMode === "fit-width") return "Fit width";
    if (this.zoomMode === "fit-page") return "Fit page";
    return Math.round(this.zoomPct) + "%";
  }

  private zoomIn(): void {
    if (this.zoomMode !== "custom") {
      this.zoomPct = nearestStepUp(this.scale * 100, ZOOM_STEPS);
      this.zoomMode = "custom";
    } else {
      this.zoomPct = nextStep(this.zoomPct, ZOOM_STEPS, +1);
    }
    this.applyZoomChange(true);
  }

  private zoomOut(): void {
    if (this.zoomMode !== "custom") {
      this.zoomPct = nearestStepDown(this.scale * 100, ZOOM_STEPS);
      this.zoomMode = "custom";
    } else {
      this.zoomPct = nextStep(this.zoomPct, ZOOM_STEPS, -1);
    }
    this.applyZoomChange(true);
  }

  private cycleZoomMode(): void {
    this.zoomMode = this.zoomMode === "fit-width" ? "fit-page" : this.zoomMode === "fit-page" ? "custom" : "fit-width";
    if (this.zoomMode === "custom") this.zoomPct = 100;
    this.applyZoomChange(true);
  }

  private rotate(): void {
    this.rotation = (this.rotation + 90) % 360;
    state.rotation = this.rotation;
    this.applyZoomChange(true);
  }

  // --- navigation ----------------------------------------------------------

  private gotoPage(n: number, instant = false): void {
    if (!this.model) return;
    const clamped = Math.max(1, Math.min(this.model.pageCount, Math.floor(n)));
    const top = this.cumTops[clamped - 1] ?? 0;
    if (instant) this.scrollEl.scrollTop = top;
    else this.scrollEl.scrollTo({ top, behavior: "auto" });
    this.currentPage = clamped;
    this.toolbar.setPage(clamped, this.model.pageCount);
    this.sidebar.setCurrentPage(clamped);
    state.currentPage = clamped;
    this.scheduleRenders();
    this.emitViewChanged();
  }

  // --- search --------------------------------------------------------------

  private async onSearch(q: string): Promise<void> {
    if (!this.model) return;
    if (!q.trim()) { await this.search.clear(); return; }
    this.toolbar.setStatus("Searching…", "loading");
    await this.search.ensureIndex((loaded, total) => this.toolbar.setStatus("Indexing " + loaded + " / " + total, "loading"));
    await this.search.search(q, {});
    const cnt = this.search.status().count;
    this.toolbar.setStatus(cnt > 0 ? "Found " + cnt + " matches" : "No matches", "ready");
  }

  private onSearchStatus(s: SearchStatus): void {
    this.toolbar.setMatch(s.index, s.count);
    state.matchCount = s.count;
    state.matchIndex = s.index;
    this.emitViewChanged();
  }

  // --- keyboard / touch ----------------------------------------------------

  private onKey(e: KeyboardEvent): void {
    const inField = e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement;
    if (e.key === "Escape") {
      this.toolbar.searchInput.blur();
      if (this.sidebar.isOpen()) this.sidebar.close();
      this.toolbar.setSidebarOpen(this.sidebar.isOpen());
      return;
    }
    if (inField) return; // let inputs handle their own keys
    const vh = this.scrollEl.clientHeight;
    switch (e.key) {
      case "PageDown":
      case "ArrowDown":
        e.preventDefault();
        this.scrollEl.scrollBy({ top: vh * 0.9 });
        break;
      case "PageUp":
      case "ArrowUp":
        e.preventDefault();
        this.scrollEl.scrollBy({ top: -vh * 0.9 });
        break;
      case "Home":
        e.preventDefault();
        this.gotoPage(1);
        break;
      case "End":
        e.preventDefault();
        if (this.model) this.gotoPage(this.model.pageCount);
        break;
      case "+":
      case "=":
        e.preventDefault();
        this.zoomIn();
        break;
      case "-":
      case "_":
        e.preventDefault();
        this.zoomOut();
        break;
      case "/":
        e.preventDefault();
        this.toolbar.focusSearch();
        break;
      case "n":
        e.preventDefault();
        void this.search.next();
        break;
      case "p":
        e.preventDefault();
        void this.search.prev();
        break;
      case "r":
        if (e.ctrlKey || e.metaKey) return; // don't hijack reload
        e.preventDefault();
        this.rotate();
        break;
    }
  }

  private onTouchStart(e: TouchEvent): void {
    if (e.touches.length === 2) {
      this.pinchStartDist = touchDistance(e);
      this.pinchStartPct = this.zoomMode === "custom" ? this.zoomPct : Math.round(this.scale * 100);
    }
  }

  private onTouchMove(e: TouchEvent): void {
    if (e.touches.length === 2 && this.pinchStartDist > 0) {
      e.preventDefault(); // stop native pinch-zoom so we can own it
      const dist = touchDistance(e);
      const ratio = dist / this.pinchStartDist;
      const pct = Math.max(ZOOM_STEPS[0], Math.min(ZOOM_STEPS[ZOOM_STEPS.length - 1], Math.round(this.pinchStartPct * ratio)));
      if (pct !== this.zoomPct) {
        this.zoomMode = "custom";
        this.zoomPct = pct;
        this.applyZoomChange(false);
      }
    }
  }

  private onTouchEnd(): void {
    this.pinchStartDist = 0;
  }

  // --- mirrors / events ----------------------------------------------------

  private emitViewChanged(): void {
    sendEvent("viewChanged", {
      currentPage: state.currentPage,
      pageCount: state.pageCount,
      zoom: state.zoom,
      rotation: state.rotation,
      matchCount: state.matchCount,
      matchIndex: state.matchIndex,
    });
  }

  // --- export (bridge egress) ---------------------------------------------
  //
  // The frame cannot download (sandbox forbids it). Produced bytes leave via a
  // `requestExport` event the host adapter relays to POST /export; the host
  // returns a URL (or an error) as `exportResult`. See pdf/host/adapter.js
  // relayExport + pdf/handlers.go handleExport for the wire shape. `kind` is
  // mode-checked on the Go side; "redact" requires ModeRedact.
  requestExport(bytes: Uint8Array, kind: "export" | "download" | "print" | "redact", filename: string): void {
    const reqId = "exp-" + (++this.exportSeq);
    state.lastExportBytes = bytes.length;
    state.lastExportError = null;
    this.toolbar.setStatus("Exporting " + filename + "…", "loading");
    sendEvent("requestExport", {
      reqId,
      kind,
      filename,
      bytes,
    });
    this.pendingExports.set(reqId, { filename, started: Date.now() });
    // The adapter is the one that resolves this with exportResult; if the host
    // never answers, a 30s watchdog surfaces a clear error instead of an
    // infinite spinner. (No host timeout exists in protocol v1 — requests are
    // host→plugin; export is plugin→host event, fire-and-forget by spec, so
    // the watchdog is the frame's own responsibility.)
    const watchdog = window.setTimeout(() => {
      if (this.pendingExports.has(reqId)) {
        this.pendingExports.delete(reqId);
        const msg = "export timed out (host did not answer exportResult)";
        state.lastExportError = msg;
        this.toolbar.setStatus(msg, "error");
      }
    }, 30_000);
    this.pendingExports.get(reqId)!.watchdog = watchdog;
  }

  onExportResult(params: unknown): void {
    if (!params || typeof params !== "object") return;
    const p = params as { reqId?: unknown; url?: unknown; error?: unknown };
    if (typeof p.reqId !== "string") return;
    const entry = this.pendingExports.get(p.reqId);
    if (!entry) return;
    this.pendingExports.delete(p.reqId);
    if (entry.watchdog) window.clearTimeout(entry.watchdog);
    if (typeof p.error === "string" && p.error) {
      state.lastExportError = p.error;
      this.toolbar.setStatus("Export failed: " + p.error, "error");
    } else if (typeof p.url === "string") {
      state.lastExportError = null;
      this.toolbar.setStatus("Exported " + entry.filename + " (" + state.lastExportBytes + " B)", "ready");
    }
  }

  // Invalidate the per-page overlay layer so a doc mutation (add/move/delete)
  // or a zoom/rotation change re-paints annotations on the affected pages.
  invalidateOverlayLayer(): void {
    if (this.editor) this.editor.renderOverlays();
  }
}

// --- small helpers (multi-site or non-obvious) ----------------------------

function asBytes(params: unknown): Uint8Array | null {
  if (typeof params !== "object" || params === null || !("bytes" in params)) return null;
  const b = params.bytes;
  if (b instanceof Uint8Array) return b;
  if (b instanceof ArrayBuffer) return new Uint8Array(b);
  return null;
}

function touchDistance(e: TouchEvent): number {
  const a = e.touches[0];
  const b = e.touches[1];
  return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY);
}

function nearestStepUp(v: number, steps: number[]): number {
  for (const s of steps) if (s > v) return s;
  return steps[steps.length - 1];
}
function nearestStepDown(v: number, steps: number[]): number {
  for (let i = steps.length - 1; i >= 0; i--) if (steps[i] < v) return steps[i];
  return steps[0];
}
function nextStep(v: number, steps: number[], dir: number): number {
  const last = steps.length - 1;
  let i = 0;
  while (i < last && steps[i] < v) i++;
  i = Math.max(0, Math.min(last, i + dir));
  return steps[i];
}

// --- boot ------------------------------------------------------------------

let activeViewer: PdfViewer | null = null;

function announceReady(): void {
  state.ready = true;
  state.probes = isolationProbes();
  sendEvent("ready", {
    version: VIEWER_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: READY_MIN_HEIGHT,
    probes: state.probes,
  });
}

function boot(): void {
  const viewer = new PdfViewer();
  activeViewer = viewer;
  const root = document.getElementById("pdf-root");
  if (root) viewer.attach(root);
  else viewer.attach(document.body);

  const handlers: HandlerMap = {
    init: (params) => {
      viewer.onInit(params);
    },
    themeChanged: (params) => {
      viewer.onThemeChanged(params);
    },
    loadBytes: (params) => {
      void viewer.onLoadBytes(params);
    },
    documentBytes: (params) => {
      viewer.onDocumentBytes(params);
    },
    requestSave: () => ({
      doc: serializeOverlay(viewer.doc.state),
      schemaVersion: SCHEMA_VERSION,
    }),
    exportResult: (params) => {
      viewer.onExportResult(params);
    },
    teardown: () => ({}),
  };
  window.addEventListener("message", createRouter(handlers));
  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}

// Keep the export referenced so esbuild's tree-shake does not drop the version
// binding read into state (no-op at runtime).
void pdfjsVersion;
void activeViewer;
