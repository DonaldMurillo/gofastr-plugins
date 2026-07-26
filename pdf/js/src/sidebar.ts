// Sidebar — collapsible panel with two tabs: document outline (bookmarks) and
// page thumbnails. Outline is fetched from pdf.getOutline(); the outline tab is
// hidden when the document has none. Thumbnails are LAZY: a single
// IntersectionObserver renders only the thumb slots near the sidebar viewport at
// low DPI, and clears far ones, so a 500-page document never allocates 500
// thumb canvases. Thumb rendering reuses the main render path (startRender) at a
// small scale; it is serial (one at a time) so it never contends badly with the
// main page render on the single-threaded fake-worker.

import type { PdfModel, OutlineNode } from "./pdfdoc";
import { startRender, clearCanvas, isCancelled } from "./render";
import { el } from "./dom";

export interface SidebarCallbacks {
  onJump: (page: number) => void;
}

const THUMB_MAX_W = 148;
const THUMB_MAX_H = 210;

interface ThumbSlot {
  pageIndex: number;
  button: HTMLButtonElement;
  canvas: HTMLCanvasElement;
  rendered: boolean;
  pending: boolean;
}

export class Sidebar {
  readonly root: HTMLElement;
  private readonly panelOutline: HTMLElement;
  private readonly panelThumbs: HTMLElement;
  private readonly tabOutline: HTMLButtonElement;
  private readonly tabThumbs: HTMLButtonElement;
  private readonly model: PdfModel;
  private readonly cb: SidebarCallbacks;
  private readonly slots: ThumbSlot[] = [];
  private observer: IntersectionObserver | null = null;
  private current = 1;
  private activeTab: "outline" | "thumbs" = "outline";
  private hasOutline = false;
  private thumbTask: { cancel: () => void } | null = null;
  private queue: ThumbSlot[] = [];

  constructor(model: PdfModel, cb: SidebarCallbacks) {
    this.model = model;
    this.cb = cb;

    this.tabOutline = el("button", {
      cls: "pdf-tab",
      role: "tab",
      text: "Outline",
      ariaSelected: "true",
      ariaControls: "pdf-panel-outline",
      tabIndex: 0,
      on: { click: () => this.switchTab("outline") },
    });
    this.tabThumbs = el("button", {
      cls: "pdf-tab",
      role: "tab",
      text: "Pages",
      ariaSelected: "false",
      ariaControls: "pdf-panel-thumbs",
      tabIndex: -1,
      on: { click: () => this.switchTab("thumbs") },
    });

    this.panelOutline = el("div", { id: "pdf-panel-outline", cls: "pdf-panel", role: "tabpanel", ariaLabel: "Document outline", tabIndex: 0 });
    this.panelThumbs = el("div", { id: "pdf-panel-thumbs", cls: "pdf-panel", role: "tabpanel", ariaLabel: "Page thumbnails", tabIndex: 0, attrs: { hidden: "hidden" } });

    const tablist = el("div", { cls: "pdf-tabs", role: "tablist", ariaLabel: "Sidebar views" }, [this.tabOutline, this.tabThumbs]);
    this.root = el("aside", {
      id: "pdf-sidebar",
      cls: "pdf-sidebar",
      role: "complementary",
      ariaLabel: "Document outline and page thumbnails",
      attrs: { hidden: "hidden" },
    }, [tablist, this.panelOutline, this.panelThumbs]);
  }

  // Render the outline tree. Hides the outline tab (and defaults to thumbs) when
  // the document has no outline.
  setOutline(nodes: OutlineNode[]): void {
    this.hasOutline = nodes.length > 0;
    this.panelOutline.replaceChildren();
    if (!this.hasOutline) {
      this.tabOutline.setAttribute("hidden", "hidden");
      this.switchTab("thumbs");
      return;
    }
    this.tabOutline.removeAttribute("hidden");
    const list = el("ul", { cls: "pdf-outline", role: "tree" });
    for (const node of nodes) list.appendChild(this.buildOutlineNode(node, 0));
    this.panelOutline.appendChild(list);
    this.switchTab("outline");
  }

  private buildOutlineNode(node: OutlineNode, depth: number): HTMLElement {
    const li = el("li", { cls: "pdf-outline-item", role: "treeitem", attrs: { "aria-level": String(depth + 1) } });
    const external = !node.dest && !!node.url;
    const label = node.title || (external ? node.url || "link" : "destination");
    const btn = el("button", {
      cls: "pdf-outline-btn" + (external ? " is-external" : ""),
      text: label,
      title: external ? "External link (inert): " + (node.url || "") : label,
      tabIndex: 0,
      on: {
        click: async () => {
          if (external) return;
          if (node.dest != null) {
            const page = await this.model.resolveDest(node.dest);
            if (page != null) this.cb.onJump(page);
          }
        },
      },
    });
    li.appendChild(btn);
    if (node.items && node.items.length > 0) {
      const sub = el("ul", { cls: "pdf-outline", role: "group" });
      for (const child of node.items) sub.appendChild(this.buildOutlineNode(child, depth + 1));
      li.appendChild(sub);
    }
    return li;
  }

  // Build thumbnail slots (light DOM; canvases fill lazily via the observer).
  buildThumbs(): void {
    this.panelThumbs.replaceChildren();
    this.slots.length = 0;
    const list = el("div", { cls: "pdf-thumb-list", role: "list", ariaLabel: "Pages" });
    for (let i = 0; i < this.model.pageCount; i++) {
      const canvas = el("canvas", { cls: "pdf-thumb-canvas", attrs: { "aria-hidden": "true" } });
      const num = el("span", { cls: "pdf-thumb-num", text: String(i + 1) });
      const ph = el("div", { cls: "pdf-thumb-placeholder", attrs: { "data-page": String(i + 1) } }, [canvas]);
      const button = el("button", {
        cls: "pdf-thumb",
        role: "listitem",
        type: "button",
        ariaLabel: "Page " + (i + 1),
        tabIndex: 0,
        on: { click: () => this.cb.onJump(i + 1) },
      }, [ph, num]);
      const slot: ThumbSlot = { pageIndex: i, button, canvas, rendered: false, pending: false };
      this.slots.push(slot);
      list.appendChild(button);
    }
    this.panelThumbs.appendChild(list);
    this.markCurrent();
  }

  // Lazily render the visible thumbs. Call when the sidebar opens.
  startObserving(): void {
    if (this.observer) return;
    this.observer = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          const slot = this.slotFor(e.target);
          if (!slot) continue;
          if (e.isIntersecting) {
            if (!slot.rendered && !slot.pending) {
              slot.pending = true;
              this.queue.push(slot);
              this.pumpQueue();
            }
          } else if (slot.rendered) {
            // Evict a far thumb to bound memory.
            clearCanvas(slot.canvas);
            slot.rendered = false;
          }
        }
      },
      { root: this.panelThumbs, rootMargin: "200px 0px 200px 0px", threshold: 0.01 }
    );
    for (const s of this.slots) this.observer.observe(s.button);
    this.pumpQueue();
  }

  stopObserving(): void {
    if (this.observer) {
      this.observer.disconnect();
      this.observer = null;
    }
    if (this.thumbTask) {
      try { this.thumbTask.cancel(); } catch { /* ignore */ }
      this.thumbTask = null;
    }
    this.queue.length = 0;
  }

  private slotFor(target: Element): ThumbSlot | null {
    for (const s of this.slots) if (s.button === target) return s;
    return null;
  }

  // Render one queued thumb at a time. Cancels mid-flight if superseded.
  private pumpQueue(): void {
    if (this.thumbTask) return;
    const slot = this.queue.shift();
    if (!slot) return;
    const page = this.model.getPage(slot.pageIndex);
    if (!page) {
      slot.pending = false;
      this.pumpQueue();
      return;
    }
    const base = page.getViewport({ scale: 1 });
    const scale = Math.min(THUMB_MAX_W / base.width, THUMB_MAX_H / base.height);
    const vp = page.getViewport({ scale });
    const { task } = startRender(page, slot.canvas, vp);
    this.thumbTask = { cancel: () => task.cancel() };
    task.promise.then(
      () => {
        slot.rendered = true;
        slot.pending = false;
        this.thumbTask = null;
        this.pumpQueue();
      },
      (e: unknown) => {
        slot.pending = false;
        this.thumbTask = null;
        if (!isCancelled(e)) {
          // A real failure: leave the placeholder; do not throw (sidebar is best-effort).
        }
        this.pumpQueue();
      }
    );
  }

  setCurrentPage(n: number): void {
    this.current = n;
    this.markCurrent();
    // Keep the current thumb visible in the sidebar.
    const slot = this.slots[n - 1];
    if (slot && this.activeTab === "thumbs" && this.root.hasAttribute("hidden") === false) {
      slot.button.scrollIntoView({ block: "nearest" });
    }
  }

  private markCurrent(): void {
    for (const s of this.slots) {
      if (s.pageIndex + 1 === this.current) s.button.classList.add("is-current");
      else s.button.classList.remove("is-current");
    }
  }

  switchTab(tab: "outline" | "thumbs"): void {
    this.activeTab = tab;
    const outlineOn = tab === "outline" && this.hasOutline;
    this.tabOutline.setAttribute("aria-selected", String(outlineOn));
    this.tabThumbs.setAttribute("aria-selected", String(tab === "thumbs"));
    this.tabOutline.tabIndex = outlineOn ? 0 : -1;
    this.tabThumbs.tabIndex = tab === "thumbs" ? 0 : -1;
    if (outlineOn) {
      this.panelThumbs.setAttribute("hidden", "hidden");
      this.panelOutline.removeAttribute("hidden");
    } else {
      this.panelOutline.setAttribute("hidden", "hidden");
      this.panelThumbs.removeAttribute("hidden");
      this.startObserving();
    }
  }

  open(): void {
    this.root.removeAttribute("hidden");
    if (this.activeTab === "thumbs" || !this.hasOutline) this.startObserving();
  }

  close(): void {
    this.root.setAttribute("hidden", "hidden");
    this.stopObserving();
  }

  isOpen(): boolean {
    return this.root.hasAttribute("hidden") === false;
  }

  toggle(): void {
    if (this.isOpen()) this.close();
    else this.open();
  }
}
