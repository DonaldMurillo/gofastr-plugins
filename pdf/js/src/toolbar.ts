// Toolbar — the viewer's control bar: sidebar toggle, page navigation, zoom,
// rotate, and search. Built imperatively with explicit roles/aria so every
// control is reachable by Tab with a visible focus ring (the repo gates on axe
// with zero serious/critical violations). The toolbar owns DOM + click/input
// wiring; orchestration (scroll, zoom math, keyboard shortcuts) lives in the
// viewer, which supplies the callbacks below.

import { el } from "./dom";

export interface ToolbarCallbacks {
  onSidebarToggle: () => void;
  onPrevPage: () => void;
  onNextPage: () => void;
  onGotoPage: (n: number) => void;
  onZoomIn: () => void;
  onZoomOut: () => void;
  onCycleZoomMode: () => void;
  onRotate: () => void;
  onSearch: (q: string) => void;
  onSearchNext: () => void;
  onSearchPrev: () => void;
  onSearchClear: () => void;
}

export type StatusKind = "loading" | "ready" | "error";

export class Toolbar {
  readonly root: HTMLElement;
  private readonly statusEl: HTMLElement;
  private readonly progressEl: HTMLElement;
  private readonly progressLabel: HTMLElement;
  private readonly progressFill: HTMLElement;
  private readonly pageInput: HTMLInputElement;
  private readonly pageCountEl: HTMLElement;
  private readonly zoomBtn: HTMLButtonElement;
  private readonly matchEl: HTMLElement;
  private readonly sidebarBtn: HTMLButtonElement;
  readonly searchInput: HTMLInputElement;
  private searchTimer = 0;

  constructor(cb: ToolbarCallbacks) {
    this.sidebarBtn = el("button", {
      cls: "pdf-btn pdf-icon-btn",
      text: "☰",
      title: "Toggle sidebar (outline / pages)",
      ariaLabel: "Toggle sidebar (outline / pages)",
      ariaExpanded: false,
      ariaControls: "pdf-sidebar",
      on: { click: () => cb.onSidebarToggle() },
    });

    this.pageInput = el("input", {
      cls: "pdf-page-input",
      attrs: { type: "number", min: "1", value: "1", "aria-label": "Current page" },
    });
    this.pageInput.addEventListener("change", () => {
      const n = this.parseInt(this.pageInput.value);
      if (n != null) cb.onGotoPage(n);
    });
    this.pageInput.addEventListener("keydown", (e) => {
      if ((e as KeyboardEvent).key === "Enter") {
        const n = this.parseInt(this.pageInput.value);
        if (n != null) cb.onGotoPage(n);
      }
    });
    this.pageCountEl = el("span", { cls: "pdf-page-count", text: " / —", attrs: { "aria-hidden": "true" } });
    const prevBtn = el("button", {
      cls: "pdf-btn pdf-icon-btn", text: "▲", title: "Previous page", ariaLabel: "Previous page",
      on: { click: () => cb.onPrevPage() },
    });
    const nextBtn = el("button", {
      cls: "pdf-btn pdf-icon-btn", text: "▼", title: "Next page", ariaLabel: "Next page",
      on: { click: () => cb.onNextPage() },
    });
    const pageGroup = el("div", { cls: "pdf-tool-group", role: "group", ariaLabel: "Page navigation" }, [prevBtn, this.pageInput, this.pageCountEl, nextBtn]);

    const zoomOut = el("button", {
      cls: "pdf-btn pdf-icon-btn", text: "−", title: "Zoom out", ariaLabel: "Zoom out",
      on: { click: () => cb.onZoomOut() },
    });
    this.zoomBtn = el("button", {
      cls: "pdf-btn pdf-zoom-label", text: "Fit width", title: "Zoom mode (click to cycle)", ariaLabel: "Zoom mode", ariaPressed: "false",
      on: { click: () => cb.onCycleZoomMode() },
    });
    const zoomIn = el("button", {
      cls: "pdf-btn pdf-icon-btn", text: "+", title: "Zoom in", ariaLabel: "Zoom in",
      on: { click: () => cb.onZoomIn() },
    });
    const rotateBtn = el("button", {
      cls: "pdf-btn pdf-icon-btn", text: "⟳", title: "Rotate 90°", ariaLabel: "Rotate clockwise 90 degrees",
      on: { click: () => cb.onRotate() },
    });
    const zoomGroup = el("div", { cls: "pdf-tool-group", role: "group", ariaLabel: "Zoom and rotation" }, [zoomOut, this.zoomBtn, zoomIn, rotateBtn]);

    this.searchInput = el("input", {
      cls: "pdf-search-input",
      attrs: { type: "search", placeholder: "Find…", "aria-label": "Search document text", autocomplete: "off" },
    });
    this.searchInput.setAttribute("aria-controls", "pdf-match-count");
    this.searchInput.addEventListener("input", () => {
      window.clearTimeout(this.searchTimer);
      this.searchTimer = window.setTimeout(() => cb.onSearch(this.searchInput.value), 220);
    });
    this.searchInput.addEventListener("keydown", (e) => {
      const ke = e as KeyboardEvent;
      if (ke.key === "Enter") {
        ke.preventDefault();
        if (ke.shiftKey) cb.onSearchPrev();
        else cb.onSearchNext();
      } else if (ke.key === "Escape") {
        this.searchInput.value = "";
        cb.onSearchClear();
        this.searchInput.blur();
      }
    });
    const searchPrev = el("button", {
      cls: "pdf-btn pdf-icon-btn", text: "▲", title: "Previous match", ariaLabel: "Previous search match",
      on: { click: () => cb.onSearchPrev() },
    });
    const searchNext = el("button", {
      cls: "pdf-btn pdf-icon-btn", text: "▼", title: "Next match", ariaLabel: "Next search match",
      on: { click: () => cb.onSearchNext() },
    });
    this.matchEl = el("span", { id: "pdf-match-count", cls: "pdf-match-count", role: "status", ariaLive: "polite", attrs: { hidden: "hidden" } });
    const searchGroup = el("div", { cls: "pdf-tool-group pdf-search-group", role: "search", ariaLabel: "Search document" }, [this.searchInput, searchPrev, searchNext, this.matchEl]);

    this.progressLabel = el("span", { cls: "pdf-progress-label", text: "" });
    const progressFill = el("div", { cls: "pdf-progress-fill" });
    this.progressFill = progressFill;
    this.progressEl = el("div", { cls: "pdf-progress-bar", role: "progressbar", ariaLabel: "Document preparation progress", attrs: { "aria-valuemin": "0", "aria-valuemax": "100" } }, [progressFill]);
    const progressWrap = el("div", { cls: "pdf-progress", attrs: { hidden: "hidden" } }, [this.progressEl, this.progressLabel]);

    this.statusEl = el("p", { id: "pdf-status", cls: "pdf-status", role: "status", ariaLive: "polite", text: "Loading…" });

    this.root = el("div", { id: "pdf-toolbar", cls: "pdf-toolbar", role: "toolbar", ariaLabel: "PDF viewer controls" }, [
      el("div", { cls: "pdf-toolbar-row" }, [this.sidebarBtn, pageGroup, zoomGroup, searchGroup]),
      progressWrap,
      this.statusEl,
    ]);
  }

  private parseInt(s: string): number | null {
    const n = /^-?\d+$/.test(s.trim()) ? Number(s) : NaN;
    return Number.isFinite(n) ? n : null;
  }

  setStatus(text: string, kind: StatusKind = "loading"): void {
    this.statusEl.textContent = text;
    this.statusEl.dataset.state = kind;
  }

  setProgress(loaded: number, total: number): void {
    const wrap = this.progressEl.parentElement;
    if (!wrap) return;
    if (total <= 0 || loaded >= total) {
      wrap.setAttribute("hidden", "hidden");
      return;
    }
    wrap.removeAttribute("hidden");
    const pct = Math.round((loaded / total) * 100);
    this.progressEl.setAttribute("aria-valuenow", String(pct));
    this.progressFill.style.width = pct + "%";
    this.progressLabel.textContent = loaded + " / " + total;
  }

  setPage(current: number, total: number): void {
    this.pageInput.max = String(total);
    if (document.activeElement !== this.pageInput) this.pageInput.value = String(current);
    this.pageCountEl.textContent = " / " + total;
  }

  setZoomLabel(label: string): void {
    this.zoomBtn.textContent = label;
  }

  setMatch(index: number, count: number): void {
    if (count <= 0) {
      this.matchEl.setAttribute("hidden", "hidden");
      this.matchEl.textContent = this.searchInput.value ? "0 results" : "";
      return;
    }
    this.matchEl.removeAttribute("hidden");
    this.matchEl.textContent = index + " of " + count;
  }

  focusSearch(): void {
    this.searchInput.focus();
    this.searchInput.select();
  }

  setSidebarOpen(open: boolean): void {
    this.sidebarBtn.setAttribute("aria-expanded", String(open));
  }
}
