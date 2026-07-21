// GoFastr tour runtime — trusted host-page guided-tour plugin.
//
// This is NOT a sandboxed plugin: it runs in the host page with full DOM
// access (it must, to spotlight real elements). It ships as a non-framed
// same-origin <script> injected via UIHostOption; the host page's normal CSP
// applies (external script + our injected <link rel="stylesheet">).
//
// API (attached to window.gofastrTour):
//   run(target)           - start a tour by id (fetched) or inline {steps}
//   restart(id)           - clear seen state, fetch + run
//   stop()                - close the active tour, if any
//   autoRun(id)           - run only if not already seen (client + server)
//   markSeen(id)          - record completion client-side AND POST /seen
//   isSeenLocally(id)     - read only the client side (no network)
//
// Bundle entry for esbuild; emitted as an IIFE by build.mjs.

const TOUR_JS_URL = "/__gofastr/plugin/tour/tour.js";
const TOUR_CSS_URL = "/__gofastr/plugin/tour/tour.css";
const TOURS_BASE = "/__gofastr/plugin/tour/tours";
const SEEN_URL = "/__gofastr/plugin/tour/seen";

const LS_PREFIX = "gofastrTour:seen:";

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

type Placement = "auto" | "top" | "bottom" | "left" | "right";

// Action is a UI action the runtime performs on the host page to reach BURIED
// targets (open a sidebar / toggle before spotlighting, or navigate).
interface Action {
  type: "click" | "wait" | "navigate";
  selector?: string;
  url?: string;
}

interface Step {
  // Target: a CSS selector (server- or JS-defined), OR a live element reference
  // (JS-defined tours only — a DOM node can't cross the JSON boundary). `target`
  // wins when both are present; frameworks pass their ref here (e.g. ref.current).
  selector?: string;
  target?: Element;
  title: string;
  body: string;
  placement?: Placement;
  before?: Action[]; // run on entering the step (reveal the target)
  after?: Action[]; // run when advancing past the step (e.g. close what opened)
  // Custom content for the bubble (any ONE overrides title/body):
  html?: string; // trusted, app-authored HTML (server- or JS-defined tours)
  content?: Node; // a prebuilt element/component (JS-defined tours only)
  render?: (el: HTMLElement) => void; // populate the bubble body yourself (JS only)
  className?: string; // extra class on the bubble for per-step styling
}

interface TourOptions {
  showProgress?: boolean;
  showDots?: boolean;
  allowKeyboard?: boolean;
  closeOnEscape?: boolean;
  backdrop?: boolean;
  accent?: string; // accent color (sets --gofastr-tour-accent)
  width?: string; // bubble max-width, e.g. "420px"
  className?: string; // extra class on every bubble in the tour
}

// Resolved options with every gate defaulted ON (undefined => default).
interface ResolvedOptions {
  showProgress: boolean;
  showDots: boolean;
  allowKeyboard: boolean;
  closeOnEscape: boolean;
  backdrop: boolean;
  accent?: string;
  width?: string;
  className?: string;
}

function resolveOptions(o?: TourOptions): ResolvedOptions {
  o = o || {};
  const on = (v: boolean | undefined) => v !== false; // default true
  return {
    showProgress: on(o.showProgress),
    showDots: on(o.showDots),
    allowKeyboard: on(o.allowKeyboard),
    closeOnEscape: on(o.closeOnEscape),
    backdrop: on(o.backdrop),
    accent: typeof o.accent === "string" ? o.accent : undefined,
    width: typeof o.width === "string" ? o.width : undefined,
    className: typeof o.className === "string" ? o.className : undefined,
  };
}

interface RunOptions {
  id?: string;
  steps: readonly Step[];
  options?: TourOptions;
}

type RunTarget = string | RunOptions;

interface TourDef {
  id: string;
  steps: Step[];
  options?: TourOptions;
}

interface GofastrTour {
  run(target: RunTarget): Promise<void>;
  restart(id: string): Promise<void>;
  stop(): void;
  autoRun(id: string): Promise<void>;
  markSeen(id: string): Promise<void>;
  isSeenLocally(id: string): boolean;
}

const PLACEMENTS: readonly Placement[] = ["auto", "top", "bottom", "left", "right"];

function isPlacement(value: unknown): value is Placement {
  return typeof value === "string" && (PLACEMENTS as readonly string[]).includes(value);
}

// isStepShape narrows an untrusted JSON value to Step. Selectors/titles/bodies
// are server-supplied; placement is optional. The trusted server owns the
// tour definitions, but a JSON response still crosses the network boundary —
// validate once here, then consume a typed value.
function isActionArray(v: unknown): boolean {
  if (v === undefined) return true;
  if (!Array.isArray(v)) return false;
  return v.every((a) => a && typeof a === "object" && typeof (a as { type?: unknown }).type === "string");
}

function isStepShape(value: unknown): value is Step {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  if (typeof v.selector !== "string") return false;
  if (typeof v.title !== "string") return false;
  if (typeof v.body !== "string") return false;
  if (v.placement !== undefined && !isPlacement(v.placement)) return false;
  if (!isActionArray(v.before) || !isActionArray(v.after)) return false;
  if (v.html !== undefined && typeof v.html !== "string") return false;
  if (v.className !== undefined && typeof v.className !== "string") return false;
  return true;
}

// parseTourOptions narrows the untrusted JSON `options` bag; render/content are
// functions/Nodes and never cross JSON, so JSON tours only carry booleans +
// style strings.
function parseTourOptions(raw: unknown): TourOptions | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const o = raw as Record<string, unknown>;
  const b = (k: string) => (typeof o[k] === "boolean" ? (o[k] as boolean) : undefined);
  const s = (k: string) => (typeof o[k] === "string" ? (o[k] as string) : undefined);
  return {
    showProgress: b("showProgress"),
    showDots: b("showDots"),
    allowKeyboard: b("allowKeyboard"),
    closeOnEscape: b("closeOnEscape"),
    backdrop: b("backdrop"),
    accent: s("accent"),
    width: s("width"),
    className: s("className"),
  };
}

function parseTourResponse(raw: unknown): TourDef {
  if (!raw || typeof raw !== "object") {
    throw new Error("tour: invalid tour response (not an object)");
  }
  if (!("id" in raw) || typeof raw.id !== "string") {
    throw new Error("tour: invalid tour response (missing id)");
  }
  if (!("steps" in raw) || !Array.isArray(raw.steps)) {
    throw new Error("tour: invalid tour response (missing steps)");
  }
  const steps: Step[] = [];
  for (const s of raw.steps) {
    if (!isStepShape(s)) {
      throw new Error("tour: step shape mismatch");
    }
    steps.push(s);
  }
  return { id: raw.id, steps, options: parseTourOptions((raw as { options?: unknown }).options) };
}

function parseSeenResponse(raw: unknown): boolean {
  if (!raw || typeof raw !== "object") return false;
  if (!("seen" in raw)) return false;
  return raw.seen === true;
}

// ---------------------------------------------------------------------------
// CSRF + fetch helpers
// ---------------------------------------------------------------------------

function csrfToken(): string | null {
  const meta = document.querySelector('meta[name="csrf-token"]');
  const content = meta?.getAttribute("content");
  return content && content.length > 0 ? content : null;
}

function jsonHeaders(): HeadersInit {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const tok = csrfToken();
  if (tok) headers["X-CSRF-Token"] = tok;
  return headers;
}

async function fetchTour(id: string): Promise<TourDef> {
  const resp = await fetch(`${TOURS_BASE}/${encodeURIComponent(id)}`, {
    method: "GET",
    credentials: "same-origin",
    headers: { Accept: "application/json" },
  });
  if (resp.status === 404) {
    throw new Error(`tour: unknown tour id "${id}"`);
  }
  if (!resp.ok) {
    throw new Error(`tour: fetch failed (${resp.status})`);
  }
  return parseTourResponse(await resp.json());
}

async function postSeen(id: string): Promise<void> {
  // Swallow network errors: client-side localStorage is the authoritative
  // auto-run gate; a failed POST must not break the tour UX. The server log
  // surfaces persistent failures.
  try {
    await fetch(SEEN_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(),
      body: JSON.stringify({ tourId: id }),
    });
  } catch {
    /* network failure — see comment above */
  }
}

async function querySeen(id: string): Promise<boolean> {
  try {
    const resp = await fetch(`${SEEN_URL}?tourId=${encodeURIComponent(id)}`, {
      method: "GET",
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    });
    if (!resp.ok) return false;
    return parseSeenResponse(await resp.json());
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// localStorage seen-state (3 call sites need the same silent-failure wrap)
// ---------------------------------------------------------------------------

function localSeenKey(id: string): string {
  return `${LS_PREFIX}${id}`;
}

function localMarkSeen(id: string): void {
  try {
    localStorage.setItem(localSeenKey(id), "1");
  } catch {
    /* localStorage disabled (private mode, quota) — server store still holds it */
  }
}

function localClearSeen(id: string): void {
  try {
    localStorage.removeItem(localSeenKey(id));
  } catch {
    /* see above */
  }
}

function localIsSeen(id: string): boolean {
  try {
    return localStorage.getItem(localSeenKey(id)) === "1";
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// Stylesheet injection
// ---------------------------------------------------------------------------

let cssInjected = false;

function injectStylesheet(): void {
  if (cssInjected) return;
  // The runtime is loaded as an external <script>; the matching stylesheet
  // lives at a fixed URL next to it. Inject a <link> so consumers only need
  // to wire a single script via UIHostOption.
  const existing = document.querySelector(`link[href="${TOUR_CSS_URL}"]`);
  if (existing) {
    cssInjected = true;
    return;
  }
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = TOUR_CSS_URL;
  link.dataset.gofastrTourCss = "1";
  document.head.appendChild(link);
  cssInjected = true;
}

// ---------------------------------------------------------------------------
// Overlay geometry
// ---------------------------------------------------------------------------

interface TargetRect {
  top: number;
  left: number;
  width: number;
  height: number;
}

type Side = "top" | "bottom" | "left" | "right";

const SCROLL_MARGIN = 12;       // px cushion when scrolling target into view / clamping
const BUBBLE_GAP = 12;          // px between target rect and tooltip bubble
const BUBBLE_MAX_W = 360;

function viewport(): { w: number; h: number } {
  return {
    w: document.documentElement.clientWidth,
    h: document.documentElement.clientHeight,
  };
}

function chooseSide(rect: TargetRect, hint: Placement): Side {
  if (hint !== "auto") return hint;
  // "auto": pick the side with the most room, preferring bottom > top > right > left.
  const vp = viewport();
  const room: Record<Side, number> = {
    bottom: vp.h - (rect.top + rect.height),
    top: rect.top,
    right: vp.w - (rect.left + rect.width),
    left: rect.left,
  };
  const order: Side[] = ["bottom", "top", "right", "left"];
  let best: Side = "bottom";
  let bestRoom = -1;
  for (const side of order) {
    if (room[side] > bestRoom) {
      best = side;
      bestRoom = room[side];
    }
  }
  return best;
}

function computeBubblePosition(
  rect: TargetRect,
  side: Side,
  bubbleSize: { w: number; h: number },
): { top: number; left: number } {
  const vp = viewport();
  const centeredLeft = rect.left + rect.width / 2 - bubbleSize.w / 2;
  const centeredTop = rect.top + rect.height / 2 - bubbleSize.h / 2;
  let top: number;
  let left: number;
  switch (side) {
    case "top":
      top = rect.top - bubbleSize.h - BUBBLE_GAP;
      left = centeredLeft;
      break;
    case "bottom":
      top = rect.top + rect.height + BUBBLE_GAP;
      left = centeredLeft;
      break;
    case "left":
      top = centeredTop;
      left = rect.left - bubbleSize.w - BUBBLE_GAP;
      break;
    case "right":
      top = centeredTop;
      left = rect.left + rect.width + BUBBLE_GAP;
      break;
  }
  // Clamp into the viewport so the bubble never leaves the screen.
  left = Math.max(SCROLL_MARGIN, Math.min(left, vp.w - bubbleSize.w - SCROLL_MARGIN));
  top = Math.max(SCROLL_MARGIN, Math.min(top, vp.h - bubbleSize.h - SCROLL_MARGIN));
  return { top, left };
}

// ---------------------------------------------------------------------------
// Tour session
// ---------------------------------------------------------------------------

class TourSession {
  readonly id: string | undefined;
  readonly steps: readonly Step[];
  private readonly opts: ResolvedOptions;
  private index = 0;
  private readonly root: HTMLElement;
  private readonly scrim: HTMLElement;        // 4-div dimmer covering page around target
  private readonly cutout: HTMLElement;       // transparent rect that repositions over target
  private readonly bubble: HTMLElement;       // tooltip dialog
  private readonly titleEl: HTMLElement;
  private readonly bodyEl: HTMLElement;
  private readonly progressEl: HTMLElement;
  private readonly dotsEl: HTMLElement;
  private readonly backBtn: HTMLButtonElement;
  private readonly nextBtn: HTMLButtonElement;
  private readonly skipBtn: HTMLButtonElement;
  private readonly resolvers: { promise: Promise<void>; resolve: () => void };
  private readonly onScrollResize: () => void;
  private readonly onKey: (e: KeyboardEvent) => void;

  constructor(id: string | undefined, steps: readonly Step[], opts: ResolvedOptions) {
    this.id = id;
    this.steps = steps;
    this.opts = opts;
    this.root = document.createElement("div");
    this.root.className = "gofastr-tour-root";
    if (opts.accent) this.root.style.setProperty("--gofastr-tour-accent", opts.accent);

    this.scrim = document.createElement("div");
    this.scrim.className = "gofastr-tour-scrim";
    this.scrim.setAttribute("aria-hidden", "true");

    this.cutout = document.createElement("div");
    this.cutout.className = "gofastr-tour-cutout";
    this.cutout.setAttribute("aria-hidden", "true");

    this.bubble = document.createElement("div");
    this.bubble.className = "gofastr-tour-bubble";
    this.bubble.setAttribute("role", "dialog");
    this.bubble.setAttribute("aria-modal", "false");
    this.bubble.tabIndex = -1;

    this.titleEl = document.createElement("div");
    this.titleEl.className = "gofastr-tour-title";
    this.titleEl.setAttribute("role", "heading");
    this.titleEl.setAttribute("aria-level", "2");
    this.bubble.appendChild(this.titleEl);

    this.skipBtn = document.createElement("button");
    this.skipBtn.type = "button";
    this.skipBtn.className = "gofastr-tour-skip";
    this.skipBtn.setAttribute("aria-label", "Skip tour");
    this.skipBtn.innerHTML = "&times;";
    this.bubble.appendChild(this.skipBtn);

    this.bodyEl = document.createElement("div");
    this.bodyEl.className = "gofastr-tour-body";
    this.bodyEl.setAttribute("aria-live", "polite");
    this.bubble.appendChild(this.bodyEl);

    this.dotsEl = document.createElement("div");
    this.dotsEl.className = "gofastr-tour-dots";
    this.dotsEl.setAttribute("role", "presentation");
    this.bubble.appendChild(this.dotsEl);

    this.progressEl = document.createElement("div");
    this.progressEl.className = "gofastr-tour-progress";
    this.bubble.appendChild(this.progressEl);

    const actions = document.createElement("div");
    actions.className = "gofastr-tour-actions";

    this.backBtn = document.createElement("button");
    this.backBtn.type = "button";
    this.backBtn.className = "gofastr-tour-btn gofastr-tour-back";
    this.backBtn.textContent = "Back";
    actions.appendChild(this.backBtn);

    this.nextBtn = document.createElement("button");
    this.nextBtn.type = "button";
    this.nextBtn.className = "gofastr-tour-btn gofastr-tour-btn-primary gofastr-tour-next";
    this.nextBtn.textContent = "Next";
    actions.appendChild(this.nextBtn);

    this.bubble.appendChild(actions);

    this.root.appendChild(this.scrim);
    this.root.appendChild(this.cutout);
    this.root.appendChild(this.bubble);
    document.body.appendChild(this.root);

    // Tour-level style/behaviour options.
    if (opts.width) this.bubble.style.maxWidth = opts.width;
    if (!opts.backdrop) { this.scrim.style.display = "none"; this.cutout.style.display = "none"; }
    if (!opts.showDots) this.dotsEl.style.display = "none";
    if (!opts.showProgress) this.progressEl.style.display = "none";

    this.resolvers = Promise.withResolvers<void>();

    this.onScrollResize = () => this.position();
    this.onKey = (e) => this.handleKey(e);

    this.skipBtn.addEventListener("click", () => this.finish("skip"));
    this.backBtn.addEventListener("click", () => { void this.go(-1); });
    this.nextBtn.addEventListener("click", () => { void this.go(1); });

    // Capture pointer events on the dimmer so the host page does not react to
    // clicks on the dimmed area while the tour is active.
    this.scrim.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
    });
  }

  async start(): Promise<void> {
    injectStylesheet();
    window.addEventListener("keydown", this.onKey, true);
    window.addEventListener("resize", this.onScrollResize, { passive: true });
    window.addEventListener("scroll", this.onScrollResize, { passive: true, capture: true });
    await this.showStep(0);
    return this.resolvers.promise;
  }

  /** Public teardown hook called by the global stop(). */
  stop(): void {
    this.finish("skip");
  }

  private async showStep(i: number): Promise<void> {
    if (i < 0) i = 0;
    if (i >= this.steps.length) {
      this.finish("done");
      return;
    }
    this.index = i;
    const step = this.steps[i];

    // Before-actions reveal a buried target (open a sidebar / expand a toggle),
    // then wait briefly for it to actually appear before spotlighting. A live
    // element ref (step.target) is already resolved, so there is nothing to wait
    // for — only a selector needs the DOM to catch up.
    await runActions(step.before);
    if (!step.target && step.selector) await waitForSelector(step.selector, 2000);

    // Per-step bubble class (tour-level className + this step's).
    this.bubble.className = "gofastr-tour-bubble" +
      (this.opts.className ? " " + this.opts.className : "") +
      (step.className ? " " + step.className : "");

    // Content: a custom renderer / node / HTML string takes over the body (and
    // hides the default title); otherwise fall back to plain title + body text.
    // `html`/`render`/`content` are app-authored (server- or JS-defined tours),
    // NOT user input — trusted by construction.
    const hasCustom = typeof step.render === "function" || step.content instanceof Node || typeof step.html === "string";
    this.bodyEl.replaceChildren();
    if (hasCustom) {
      this.titleEl.style.display = "none";
      if (typeof step.render === "function") step.render(this.bodyEl);
      else if (step.content instanceof Node) this.bodyEl.appendChild(step.content);
      else if (typeof step.html === "string") this.bodyEl.innerHTML = step.html;
    } else {
      this.titleEl.style.display = "";
      this.titleEl.textContent = step.title;
      this.bodyEl.textContent = step.body;
    }

    this.progressEl.textContent = `Step ${i + 1} of ${this.steps.length}`;
    this.renderDots();
    const isLast = i === this.steps.length - 1;
    this.nextBtn.textContent = isLast ? "Done" : "Next";
    this.backBtn.disabled = i === 0;
    this.backBtn.style.visibility = i === 0 ? "hidden" : "visible";

    // Resolve target (element ref or selector). A missing target means a stale
    // tour definition; we still show a centered bubble so it is not bricked.
    const target = resolveTarget(step);
    if (target instanceof HTMLElement) {
      target.scrollIntoView({ behavior: "smooth", block: "center", inline: "center" });
    }
    // Wait two animation frames so scrollIntoView's smooth scroll has a chance
    // to settle before we measure the target rect.
    await rafN(2);
    this.position();
    this.bubble.focus();
  }

  private renderDots(): void {
    this.dotsEl.replaceChildren();
    for (let i = 0; i < this.steps.length; i++) {
      const dot = document.createElement("span");
      dot.className = "gofastr-tour-dot" + (i === this.index ? " is-current" : "");
      this.dotsEl.appendChild(dot);
    }
  }

  private position(): void {
    const step = this.steps[this.index];
    if (!step) return;
    const vp = viewport();
    const matched = resolveTarget(step);
    // Cutout hugs the target; if no match, place a centered placeholder rect.
    const rect: TargetRect = matched instanceof HTMLElement
      ? { top: matched.getBoundingClientRect().top,
          left: matched.getBoundingClientRect().left,
          width: matched.getBoundingClientRect().width,
          height: matched.getBoundingClientRect().height }
      : { top: vp.h / 2 - 24, left: vp.w / 2 - 100, width: 200, height: 48 };

    this.cutout.style.top = `${rect.top}px`;
    this.cutout.style.left = `${rect.left}px`;
    this.cutout.style.width = `${rect.width}px`;
    this.cutout.style.height = `${rect.height}px`;

    positionScrim(this.scrim, rect);

    const bubbleRect = this.bubble.getBoundingClientRect();
    const side = chooseSide(rect, step.placement ?? "auto");
    const pos = computeBubblePosition(rect, side, {
      w: Math.min(BUBBLE_MAX_W, bubbleRect.width || BUBBLE_MAX_W),
      h: bubbleRect.height || 160,
    });
    this.bubble.style.top = `${pos.top}px`;
    this.bubble.style.left = `${pos.left}px`;
  }

  private handleKey(e: KeyboardEvent): void {
    if (e.key === "Escape") {
      if (!this.opts.closeOnEscape) return;
      e.preventDefault();
      this.finish("skip");
      return;
    }
    if (e.key === "Tab") {
      this.trapFocus(e); // focus trap stays on regardless of allowKeyboard (a11y)
      return;
    }
    if (!this.opts.allowKeyboard) return;
    if (e.key === "ArrowRight" || e.key === "Enter") {
      // Enter on a focused button would double-advance; let the button's click
      // handler take Enter only when the bubble itself has focus.
      if (e.key === "Enter" && document.activeElement !== this.bubble) return;
      e.preventDefault();
      void this.go(1);
      return;
    }
    if (e.key === "ArrowLeft") {
      e.preventDefault();
      void this.go(-1);
    }
  }

  private trapFocus(e: KeyboardEvent): void {
    const list = this.focusable().filter((el) => el.offsetParent !== null || el === document.activeElement);
    if (list.length === 0) return;
    const first = list[0];
    const last = list[list.length - 1];
    const active = document.activeElement;
    const insideBubble = active instanceof Node && this.bubble.contains(active);
    if (e.shiftKey) {
      if (active === first || !insideBubble) {
        e.preventDefault();
        last.focus();
      }
    } else {
      if (active === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }

  private focusable(): HTMLElement[] {
    return [this.skipBtn, this.backBtn, this.nextBtn];
  }

  private async go(delta: number): Promise<void> {
    // Advancing forward runs the current step's after-actions (e.g. close the
    // sidebar a before-action opened). Going back does not re-trigger them.
    if (delta > 0) await runActions(this.steps[this.index]?.after);
    const next = this.index + delta;
    if (next >= this.steps.length) {
      this.finish("done");
      return;
    }
    await this.showStep(next);
  }

  private finish(reason: "done" | "skip"): void {
    // Persist on BOTH completion AND skip — the brief: "remember per-tour
    // completion so a tour does not auto-run again". Restart clears it.
    if (this.id) {
      localMarkSeen(this.id);
      void postSeen(this.id);
    }
    this.teardown();
    this.resolvers.resolve();
    if (!this.id) return;
    const name = reason === "done" ? "gofastrtour:complete" : "gofastrtour:skip";
    document.dispatchEvent(new CustomEvent(name, { detail: { id: this.id } }));
  }

  private teardown(): void {
    window.removeEventListener("keydown", this.onKey, true);
    window.removeEventListener("resize", this.onScrollResize);
    window.removeEventListener("scroll", this.onScrollResize, { capture: true } as EventListenerOptions);
    if (this.root.parentNode) {
      this.root.parentNode.removeChild(this.root);
    }
  }
}

// positionScrim lays out 4 child divs inside `scrim` covering the viewport
// EXCEPT for the cutout rect. Each child has class `gofastr-tour-dim-{side}`
// and is sized here on every reposition.
function positionScrim(scrim: HTMLElement, rect: TargetRect): void {
  const vp = viewport();
  const parts = ["top", "bottom", "left", "right"] as const;
  type Part = typeof parts[number];
  const dims: Record<Part, { top: number; left: number; width: number; height: number }> = {
    top: { top: 0, left: 0, width: vp.w, height: Math.max(0, rect.top) },
    bottom: { top: rect.top + rect.height, left: 0, width: vp.w, height: Math.max(0, vp.h - (rect.top + rect.height)) },
    left: { top: rect.top, left: 0, width: Math.max(0, rect.left), height: rect.height },
    right: { top: rect.top, left: rect.left + rect.width, width: Math.max(0, vp.w - (rect.left + rect.width)), height: rect.height },
  };
  if (scrim.childElementCount !== parts.length) {
    scrim.replaceChildren();
    for (const p of parts) {
      const d = document.createElement("div");
      d.className = `gofastr-tour-dim gofastr-tour-dim-${p}`;
      scrim.appendChild(d);
    }
  }
  let i = 0;
  for (const p of parts) {
    const child = scrim.children[i] as HTMLElement;
    const d = dims[p];
    child.style.top = `${d.top}px`;
    child.style.left = `${d.left}px`;
    child.style.width = `${d.width}px`;
    child.style.height = `${d.height}px`;
    i++;
  }
}

// --- step actions: reveal buried targets -----------------------------------
// A step can `click` a control (open a sidebar / expand a toggle), `wait` for an
// element to appear, or `navigate`, before/after it is shown. Best-effort: a bad
// selector must never brick the walkthrough.

function waitForSelector(sel: string, timeoutMs: number): Promise<void> {
  return new Promise((resolve) => {
    if (document.querySelector(sel)) return resolve();
    let waited = 0;
    const stepMs = 50;
    const iv = setInterval(() => {
      waited += stepMs;
      if (document.querySelector(sel) || waited >= timeoutMs) {
        clearInterval(iv);
        resolve();
      }
    }, stepMs);
  });
}

async function runAction(a: Action): Promise<void> {
  try {
    if (a.type === "click" && a.selector) {
      const el = document.querySelector(a.selector);
      if (el instanceof HTMLElement) el.click();
    } else if (a.type === "navigate" && a.url) {
      location.assign(a.url);
    } else if (a.type === "wait" && a.selector) {
      await waitForSelector(a.selector, 4000);
    }
  } catch {
    /* best-effort — never let an action abort the tour */
  }
}

async function runActions(list?: Action[]): Promise<void> {
  if (!list) return;
  for (const a of list) await runAction(a);
}

// resolveTarget picks the step's element: a live `target` ref wins, else the
// `selector` is queried against the live DOM.
function resolveTarget(step: Step): Element | null {
  if (step.target instanceof Element) return step.target;
  if (typeof step.selector === "string" && step.selector) return document.querySelector(step.selector);
  return null;
}

// rafN resolves after N animation frames — lets layout settle after
// scrollIntoView before measuring the target rect.
function rafN(n: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  let remaining = n;
  const step = () => {
    if (--remaining <= 0) resolve();
    else requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
  return promise;
}

// ---------------------------------------------------------------------------
// Single active session
// ---------------------------------------------------------------------------

let active: TourSession | null = null;

async function startSession(id: string | undefined, steps: readonly Step[], options?: TourOptions): Promise<void> {
  if (active) active.stop();
  if (steps.length === 0) return;
  const session = new TourSession(id, steps, resolveOptions(options));
  active = session;
  try {
    await session.start();
  } finally {
    if (active === session) active = null;
  }
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

const api: GofastrTour = {
  async run(target: RunTarget): Promise<void> {
    if (typeof target === "string") {
      const tour = await fetchTour(target);
      await startSession(tour.id, tour.steps, tour.options);
      return;
    }
    if (target && typeof target === "object" && Array.isArray(target.steps)) {
      await startSession(target.id, target.steps, target.options);
      return;
    }
    throw new Error("tour: run() expects a tour id or { steps: Step[] }");
  },

  async restart(id: string): Promise<void> {
    localClearSeen(id);
    const tour = await fetchTour(id);
    await startSession(tour.id, tour.steps, tour.options);
  },

  stop(): void {
    active?.stop();
  },

  async autoRun(id: string): Promise<void> {
    // Client-side gate is authoritative for instant UX; server gate wins when
    // localStorage was cleared.
    if (localIsSeen(id)) return;
    const serverSeen = await querySeen(id);
    if (serverSeen) {
      localMarkSeen(id);
      return;
    }
    const tour = await fetchTour(id);
    await startSession(tour.id, tour.steps, tour.options);
  },

  async markSeen(id: string): Promise<void> {
    localMarkSeen(id);
    await postSeen(id);
  },

  isSeenLocally(id: string): boolean {
    return localIsSeen(id);
  },
};

// Attach to window. Skip if the script is loaded twice (defensive — the host
// page might inject it via both UIHostOption and a manual <script>). Augments
// the global Window interface (script file — merges into the global scope).
interface Window {
  gofastrTour?: GofastrTour;
}

if (typeof window !== "undefined" && !window.gofastrTour) {
  window.gofastrTour = api;
  // Signal readiness for host-page code that wants to defer autoRun() until
  // the runtime is installed.
  document.dispatchEvent(new CustomEvent("gofastrtour:ready"));
}

// TOUR_JS_URL documents the route contract alongside TOUR_CSS_URL/TOURS_BASE/
// SEEN_URL even though the runtime does not fetch its own script URL.
void TOUR_JS_URL;
