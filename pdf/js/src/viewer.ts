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

// Hand the worker's exports to pdf.js so it takes the main-thread fake-worker
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
    if (!this.model) return;
    if (this.zoomMode !== "custom") {
      const before = this.scale;
      this.scale = this.computeScale();
      if (Math.abs(this.scale - before) > 1e-4) {
        for (const rt of this.pages) rt.gen++;
        this.inFlight = null;
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
  }

  onThemeChanged(params: unknown): void {
    if (hasTokens(params)) applyTokens(params.tokens);
  }

  async onLoadBytes(params: unknown): Promise<void> {
    const bytes = asBytes(params);
    if (!bytes) {
      const msg = "loadBytes: missing {bytes}";
      state.error = msg;
      sendEvent("renderError", { message: msg });
      this.toolbar.setStatus(msg, "error");
      return;
    }
    try {
      this.toolbar.setStatus("Opening document…", "loading");
      const model = await loadDocument(bytes);
      this.model = model;
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
      const msg = e instanceof Error ? e.message : String(e);
      state.error = msg;
      sendEvent("renderError", { message: msg });
      this.toolbar.setStatus("Failed to open document: " + msg, "error");
    }
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
      const placeholder = el("div", { cls: "pdf-page-placeholder", text: "Page " + (i + 1), attrs: { "aria-hidden": "true" } });
      const inner = el("div", { cls: "pdf-page-inner" }, [canvas, textLayer, annotLayer, placeholder]);
      const slot = el("div", {
        cls: "pdf-page",
        attrs: { "data-page": String(i + 1) },
        style: { width: "0px", height: "0px" },
      }, [inner]);
      this.pagesEl.appendChild(slot);
      this.pages[i] = { slot, canvas, textLayer, annotLayer, placeholder, spans: [], gen: 0, rendered: false, textContent: null, annotBuilt: false };
    }
  }

  // Recompute every page's CSS box at the current scale/rotation and the
  // cumulative tops. When `anchor` is true, scroll is re-anchored to keep the
  // current page at the same relative position (so zoom/rotate don't jump).
  private computeLayout(anchor: boolean): void {
    if (!this.model) return;
    const prevPage = this.currentPage - 1;
    const prevTop = this.cumTops[prevPage] ?? 0;
    const prevH = this.pages[prevPage]?.slot.offsetHeight ?? 1;
    const ratio = prevH > 0 ? (this.scrollEl.scrollTop - prevTop) / prevH : 0;

    let y = 0;
    for (let i = 0; i < this.model.pageCount; i++) {
      const rt = this.pages[i];
      const g = this.model.geom(i, this.scale, this.rotation);
      rt.slot.style.width = Math.floor(g.cssW) + "px";
      rt.slot.style.height = Math.floor(g.cssH) + "px";
      this.cumTops[i] = y;
      y += Math.floor(g.cssH) + PAGE_GAP;
    }
    this.cumTops[this.model.pageCount] = y;
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
    if (!this.model) return;
    const top = this.scrollEl.scrollTop;
    const vh = this.scrollEl.clientHeight;
    const lo = this.pageAtOffset(top - vh * RENDER_MARGIN);
    const hi = this.pageAtOffset(top + vh + vh * RENDER_MARGIN);
    const center = top + vh / 2;
    const want: number[] = [];
    for (let i = lo; i <= hi && i < this.model.pageCount; i++) {
      if (!this.pages[i].rendered) want.push(i);
    }
    // Prioritize by distance to viewport center.
    want.sort((a, b) => {
      const ca = this.cumTops[a] + (this.pages[a].slot.offsetHeight || 0) / 2 - center;
      const cb = this.cumTops[b] + (this.pages[b].slot.offsetHeight || 0) / 2 - center;
      return Math.abs(ca) - Math.abs(cb);
    });
    this.renderQueue = want;
    this.pumpRender();
  }

  private evictFar(): void {
    if (!this.model) return;
    const top = this.scrollEl.scrollTop;
    const vh = this.scrollEl.clientHeight;
    const lo = this.pageAtOffset(top - vh * EVICT_MARGIN);
    const hi = this.pageAtOffset(top + vh + vh * EVICT_MARGIN);
    for (let i = 0; i < this.model.pageCount; i++) {
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
    rt.placeholder.removeAttribute("hidden");
  }

  // --- render loop ---------------------------------------------------------

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
    this.inFlight = null;
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
    requestSave: () => ({ doc: null, schemaVersion: SCHEMA_VERSION }),
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
