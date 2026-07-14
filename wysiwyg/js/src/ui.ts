// In-frame UI overlays for the WYSIWYG editor (schema wysiwyg-v1).
//
// Three ProseMirror view plugins, each rendering a floating DOM overlay inside
// the sandboxed frame (never the host DOM):
//   • slash menu   — `/` opens a filterable block inserter (all block types)
//   • bubble bar   — selection toolbar: marks, link editor, text color, highlight
//   • drag handle  — hover affordance to reorder top-level blocks (pointer DnD)
//
// Latency budget: none of these touch the keystroke path. The bubble hides on
// empty selection; the slash menu renders only on `/`; the handle only on hover.
// a11y: toolbar buttons carry role/aria-label/aria-pressed; the slash menu is a
// listbox with options; toggles expose aria-expanded; all are keyboard-navigable.

import { Plugin, PluginKey } from "prosemirror-state";
import type { EditorState, Command } from "prosemirror-state";
import type { EditorView } from "prosemirror-view";
import type { Node as PMNode, ResolvedPos } from "prosemirror-model";
import * as cmd from "./commands.ts";
import { COLOR_SLOTS } from "./schema.ts";

const slashKey = new PluginKey("wysiwyg-slash");
const bubbleKey = new PluginKey("wysiwyg-bubble");
const handleKey = new PluginKey("wysiwyg-draghandle");
const toolbarKey = new PluginKey("wysiwyg-toolbar");
const statusKey = new PluginKey("wysiwyg-status");
const blockCtlKey = new PluginKey("wysiwyg-blockctl");

// ---------------------------------------------------------------------------
// Tiny DOM helpers

// Attribute bag for el(): class/style/html plus aria-* / data-* / any attribute.
type ElAttrs = Record<string, unknown>;
type ElChild = HTMLElement | string | null | undefined;

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: ElAttrs | null,
  ...children: ElChild[]
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (attrs) {
    for (const k in attrs) {
      const v = attrs[k];
      if (v == null || v === false) continue;
      if (k === "class") node.className = v as string;
      else if (k === "style" && typeof v === "object") Object.assign(node.style, v);
      else if (k === "html") node.innerHTML = v as string;
      else node.setAttribute(k, v === true ? "" : String(v));
    }
  }
  for (const c of children) {
    if (c == null) continue;
    node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
  }
  return node;
}

function icon(svg: string) {
  return el("span", { class: "wysiwyg-ico", html: svg });
}

// Minimal line-style SVGs (no external resources; kept tiny).
const ICON = {
  bold: '<svg viewBox="0 0 16 16"><path d="M4 3h4.5a2.5 2.5 0 0 1 0 5H4zm0 5h5a2.5 2.5 0 0 1 0 5H4z"/></svg>',
  italic: '<svg viewBox="0 0 16 16"><path d="M7 3h6v1.4l-2 .3-2.4 7 1.8.3V13H4v-1.4l2-.3 2.4-7L6.6 4V3z"/></svg>',
  underline: '<svg viewBox="0 0 16 16"><path d="M4 3v5.5a4 4 0 0 0 8 0V3h-1.6v5.5a2.4 2.4 0 0 1-4.8 0V3zm0 10h8v1.4H4z"/></svg>',
  strike: '<svg viewBox="0 0 16 16"><path d="M2 7.3h12v1.4H2zM8 2c2 0 3.5.9 4 2.4l-1.5.5C10.1 4 9.3 3.4 8 3.4S5.9 4 5.9 5c0 .8.6 1.2 2 1.6H5.3C4.6 5.8 4.4 5 4.4 5c0-1.8 1.6-3 3.6-3zm0 9.6c1.5 0 2.6-.6 3-1.8l1.5.4c-.6 1.7-2.2 2.8-4.5 2.8-2 0-3.6-1-4-2.6h1.6c.3.8 1.2 1.2 2.4 1.2z"/></svg>',
  code: '<svg viewBox="0 0 16 16"><path d="M5.5 4 2 8l3.5 4 1-1L4 8l2.5-3zm5 0L9 5l2.5 3L9 11l1.5 1L14 8z"/></svg>',
  link: '<svg viewBox="0 0 16 16"><path d="M6.5 9.5l3-3M7 11l-1 1a2.8 2.8 0 0 1-4-4l1.5-1.5M9 5l1-1a2.8 2.8 0 0 1 4 4L13.5 9.5"/></svg>',
  text: '<svg viewBox="0 0 16 16"><path d="M3 4h10v1.5H9V13H7V5.5H3z"/></svg>',
  highlight: '<svg viewBox="0 0 16 16"><path d="M9.5 2 14 6.5 8 12.5l-3 .5.5-3zM3 13.5h6V15H3z"/></svg>',
  undo: '<svg viewBox="0 0 16 16"><path d="M6 4L3 7l3 3V8h4a3 3 0 0 1 0 6H6v-1.5h4a1.5 1.5 0 0 0 0-3H6V4z"/></svg>',
  redo: '<svg viewBox="0 0 16 16"><path d="M10 4l3 3-3 3V8H6a3 3 0 0 0 0 6h4v-1.5H6a1.5 1.5 0 0 1 0-3h4V4z"/></svg>',
  alignLeft: '<svg viewBox="0 0 16 16"><path d="M2 3h12v1.6H2zM2 6.7h8v1.6H2zM2 10.4h12v1.6H2z"/></svg>',
  alignCenter: '<svg viewBox="0 0 16 16"><path d="M2 3h12v1.6H2zM4 6.7h8v1.6H4zM2 10.4h12v1.6H2z"/></svg>',
  alignRight: '<svg viewBox="0 0 16 16"><path d="M2 3h12v1.6H2zM6 6.7h8v1.6H6zM2 10.4h12v1.6H2z"/></svg>',
  clear: '<svg viewBox="0 0 16 16"><path d="M4 3h9v1.6H9.3l-1.7 6.8H6L7.7 4.6H4zM3 12.4l9.5.0v1.3H3z"/><path d="M11.5 10.5l2.5 2.5-1 1-2.5-2.5z"/></svg>',
};

// Apply a ProseMirror COMMAND (state, dispatch, view) => bool to the view.
// Callers holding a factory must invoke it first: run(view, factory()).
// (Guessing factory-vs-command here is impossible — both are functions — and
// the old heuristic double-invoked commands, silently breaking every bubble
// toolbar button and the heading slash items.)
// Where floating overlays attach. Default: the frame's own document.body. The
// trusted in-page mount points this at the scoped wrapper so the overlays stay
// inside the `.gofastr-wysiwyg-trusted` CSS scope (they are position:fixed, so
// layout is unaffected — only style scoping and containment change).
let overlayParentRef: HTMLElement | null = null;
export function setOverlayParent(el: HTMLElement | null) {
  overlayParentRef = el;
}
function overlayParent(): HTMLElement {
  return overlayParentRef || document.body;
}

function run(view: EditorView, command: Command) {
  command(view.state, view.dispatch.bind(view), view);
  view.focus();
}

/** Anchor rectangle edges used to place a floating overlay. */
interface PlaceCoords {
  left: number;
  top: number;
  bottom: number;
}

// Position a fixed-position overlay at coords, clamping to the viewport.
function place(elNode: HTMLElement, { left, top, bottom }: PlaceCoords, prefer = "below", gap = 6) {
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  const r = elNode.getBoundingClientRect();
  let x = left - r.width / 2;
  x = Math.max(8, Math.min(x, vw - r.width - 8));
  let y = prefer === "below" ? bottom + gap : top - r.height - gap;
  if (y + r.height > vh - 8) y = top - r.height - gap;
  if (y < 8) y = bottom + gap;
  elNode.style.left = `${Math.round(x)}px`;
  elNode.style.top = `${Math.round(y)}px`;
}

// ---------------------------------------------------------------------------
// Slash menu

interface SlashItem {
  title: string;
  sub: string;
  kw: string;
  /** Command FACTORY — run() invokes it once to get the command (see NOTE below). */
  run: () => Command;
  needsUpload?: boolean;
}

const SLASH_ITEMS: SlashItem[] = [
  { title: "Text", sub: "Plain paragraph", kw: "paragraph text p", run: cmd.setParagraph },
  // NOTE: every `run` must be a FACTORY (called with no args, returns the
  // command) — run() invokes it once before applying. Storing a pre-invoked
  // command here (e.g. cmd.setHeading(1)) makes run() call the command with
  // no state and it throws instead of applying.
  { title: "Heading 1", sub: "Big section title", kw: "h1 heading title", run: () => cmd.setHeading(1) },
  { title: "Heading 2", sub: "Medium section title", kw: "h2 heading", run: () => cmd.setHeading(2) },
  { title: "Heading 3", sub: "Small section title", kw: "h3 heading", run: () => cmd.setHeading(3) },
  { title: "Heading 4", sub: "", kw: "h4 heading", run: () => cmd.setHeading(4) },
  { title: "Bulleted list", sub: "Simple bullet list", kw: "bullet ul list", run: cmd.toggleBulletList },
  { title: "Numbered list", sub: "Ordered list", kw: "number ol ordered list", run: cmd.toggleOrderedList },
  { title: "To-do list", sub: "Checkable tasks", kw: "task todo check list", run: cmd.toggleTaskList },
  { title: "Quote", sub: "Capture a quote", kw: "blockquote quote cite", run: cmd.toggleBlockquote },
  { title: "Code", sub: "Fenced code block", kw: "code fence pre", run: cmd.setCodeBlock },
  { title: "Divider", sub: "Horizontal rule", kw: "divider hr rule separator", run: cmd.insertDivider },
  { title: "Callout", sub: "Info / warn / note box", kw: "callout info warn note admonition", run: cmd.insertCallout },
  { title: "Toggle", sub: "Collapsible block", kw: "toggle collapsible details summary", run: cmd.insertToggle },
  { title: "Columns", sub: "Two-column layout", kw: "columns two layout", run: cmd.insertColumns },
  { title: "Table", sub: "Insert a 3×3 table", kw: "table grid cell", run: cmd.insertTable },
  { title: "Image", sub: "Upload an image", kw: "image picture upload photo", run: () => cmd.noop(), needsUpload: true },
];

// Predicate the editor entry sets so capability-gated items (Image needs
// upload:images) don't appear when the grant is absent — selecting one used to
// delete the "/…" text and silently do nothing.
let slashItemAllowed: (it: SlashItem) => boolean = () => true;
export function setSlashItemFilter(fn: (it: SlashItem) => boolean) {
  slashItemAllowed = fn;
}

function filteredItems(query: string): SlashItem[] {
  const q = query.trim().toLowerCase();
  const pool = SLASH_ITEMS.filter(slashItemAllowed);
  if (!q) return pool;
  return pool.filter((it) => {
    return (
      it.title.toLowerCase().includes(q) ||
      it.kw.toLowerCase().split(/\s+/).some((w) => w.startsWith(q))
    );
  });
}

export type { SlashItem };

export interface SlashMenuOptions {
  onPickImage?: (() => void) | null;
}

export function slashMenuPlugin({ onPickImage }: SlashMenuOptions = {}) {
  let ctrl: SlashMenu | null = null;
  return new Plugin({
    key: slashKey,
    view(editorView) {
      ctrl = new SlashMenu(editorView);
      return ctrl;
    },
    props: {
      handleTextInput(view, from, to, text) {
        return ctrl ? ctrl.onTextInput(from, to, text) : false;
      },
      handleKeyDown(view, event) {
        return ctrl ? ctrl.onKeyDown(view, event) : false;
      },
      handleClick() {
        if (ctrl) ctrl.close();
        return false;
      },
    },
  });
}

class SlashMenu {
  view: EditorView;
  open: boolean;
  slashPos: number;
  query: string;
  selected: number;
  list: HTMLElement | null;
  root: HTMLElement | null;
  live: HTMLElement | null = null;
  onDocPointerdown: (e: PointerEvent) => void;
  onWinBlur: () => void;
  onScroll: () => void;

  constructor(view: EditorView) {
    this.view = view;
    this.open = false;
    this.slashPos = -1;
    this.query = "";
    this.selected = 0;
    this.list = null;
    this.root = null;
    // Dismissal parity with every native menu: a mousedown anywhere OUTSIDE
    // the menu, or focus leaving the frame/page entirely (click on the host
    // page, tab away — cases that produce NO editor transaction, so update()
    // never runs), closes it. Without these the menu could only be closed by
    // Escape/commit — on touch layouts there is no Escape, so it stayed open
    // forever, keeping the frame grown and the page pushed down.
    // pointerdown, not mousedown: iOS WebKit does not synthesize mouse events
    // for taps on non-interactive elements, so a mousedown listener never
    // fired for an outside TAP — pointerdown covers mouse, touch, and pen.
    this.onDocPointerdown = (e: PointerEvent) => {
      if (!this.open) return;
      const t = e.target as Node | null;
      if (t && this.root && this.root.contains(t)) return; // item clicks commit
      this.close();
    };
    this.onWinBlur = () => this.close();
    document.addEventListener("pointerdown", this.onDocPointerdown, true);
    window.addEventListener("blur", this.onWinBlur);
    overlayDismissers.add(this.onWinBlur);
    this.onScroll = () => this.reposition();
    scrollFollowers.add(this.onScroll);
    this.build();
  }
  // Keep the open menu anchored to the "/" while the page scrolls under it.
  reposition() {
    if (!this.open || !this.root || this.root.style.display === "none") return;
    try {
      place(this.root, this.view.coordsAtPos(this.slashPos), "below", 8);
    } catch (e) {
      /* slashPos out of range mid-edit — the next update() closes us */
    }
  }
  build() {
    this.root = el("div", {
      id: SLASH_LISTBOX_ID,
      class: "wysiwyg-slash-menu",
      role: "listbox",
      "aria-label": "Insert block",
      // Keyboard-focusable (a11y: a scrollable region must be reachable by
      // keyboard — axe scrollable-region-focusable). Normally arrows reach us
      // through the editor's handleKeyDown while the editor keeps focus, but
      // if focus lands ON the menu (Tab), it must be operable there too.
      tabindex: "0",
      style: { display: "none" },
    });
    this.root.addEventListener("keydown", (e) => {
      this.onKeyDown(this.view, e); // same arrows/Enter/Escape handling
    });
    overlayParent().appendChild(this.root);
    // Polite live region for the empty-state ("No matches") — screen readers
    // otherwise get no signal that a filter came up empty.
    this.live = el("div", {
      class: "wysiwyg-sr-only",
      "aria-live": "polite",
      "aria-atomic": "true",
    });
    overlayParent().appendChild(this.live);
  }
  // Combobox wiring on the editor DOM: focus STAYS in the editable (the caret
  // keeps blinking there), so per the ARIA combobox pattern the editor is the
  // controlling element and carries aria-expanded / aria-controls /
  // aria-activedescendant pointing at the active option.
  private setEditorCombobox(open: boolean) {
    // The editable keeps role="textbox" (it IS a multiline text area — a role
    // swap to combobox would make aria-multiline invalid). textbox permits
    // aria-activedescendant, so the active option is exposed that way, and the
    // live region announces the open/empty state. aria-expanded is NOT valid on
    // textbox, so it's deliberately omitted rather than tripping aria-allowed-attr.
    const dom = this.view.dom as HTMLElement;
    if (!open) dom.removeAttribute("aria-activedescendant");
  }
  filtered(): SlashItem[] {
    return filteredItems(this.query);
  }
  onTextInput(from: number, to: number, text: string): boolean {
    if (!this.open) {
      if (text === "/") {
        const $from = this.view.state.doc.resolve(from);
        const before = $from.parent.textBetween(0, $from.parentOffset, "\n");
        if (/^\s*$/.test(before)) {
          this.slashPos = from;
          this.query = "";
          this.selected = 0;
          this.open = true;
        }
      }
      return false;
    }
    return false;
  }
  onKeyDown(view: EditorView, event: KeyboardEvent): boolean {
    if (!this.open) return false;
    const items = this.filtered();
    if (event.key === "ArrowDown") {
      event.preventDefault();
      this.selected = items.length ? (this.selected + 1) % items.length : 0;
      this.updateActive();
      return true;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      this.selected = items.length ? (this.selected - 1 + items.length) % items.length : 0;
      this.updateActive();
      return true;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      const it = items[this.selected];
      if (it) this.commit(it);
      else this.close();
      return true;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      this.close();
      return true;
    }
    return false;
  }
  update(view: EditorView, prevState: EditorState) {
    this.view = view;
    if (!this.open) return;
    const { selection } = view.state;
    if (selection.from <= this.slashPos || selection.empty === false) {
      this.close();
      return;
    }
    const text = view.state.doc.textBetween(this.slashPos + 1, selection.from, "\n");
    if (/\s/.test(text)) {
      this.close();
      return;
    }
    this.query = text;
    this.render();
  }
  render() {
    const items = this.filtered();
    if (this.selected >= items.length) this.selected = Math.max(0, items.length - 1);
    const root = this.root!;
    root.innerHTML = "";
    if (!items.length) {
      root.appendChild(el("div", { class: "wysiwyg-slash-empty" }, "No matches"));
    }
    items.forEach((it, i) => {
      const opt = el(
        "div",
        {
          id: `${SLASH_OPT_PREFIX}${i}`,
          class: "wysiwyg-slash-item" + (i === this.selected ? " is-active" : ""),
          role: "option",
          "aria-selected": i === this.selected ? "true" : "false",
        },
        el("div", { class: "wysiwyg-slash-title" }, it.title),
        it.sub ? el("div", { class: "wysiwyg-slash-sub" }, it.sub) : null
      );
      // Hover moves the highlight WITHOUT rebuilding the DOM. Re-rendering
      // here (innerHTML = "") replaces the element under the pointer, which
      // re-fires enter events on the replacement — a rebuild loop that makes a
      // subsequent mousedown land on a just-removed node, so clicking a menu
      // item silently did nothing (Safari showed this constantly).
      // mousemove, not mouseenter: when arrow keys scroll the list under a
      // stationary pointer, mouseenter fires on whatever slides under the
      // cursor and would yank the highlight away from the keyboard selection;
      // mousemove only fires when the user actually moves the mouse.
      opt.addEventListener("mousemove", () => {
        if (this.selected === i) return;
        this.selected = i;
        this.updateActive();
      });
      opt.addEventListener("mousedown", (e) => {
        e.preventDefault();
        this.commit(it);
      });
      root.appendChild(opt);
    });
    const coords = this.view.coordsAtPos(this.slashPos);
    root.style.display = "block";
    place(root, coords, "below", 8);
    this.setEditorCombobox(true);
    this.updateActiveDescendant(items.length);
    if (this.live) this.live.textContent = items.length ? "" : "No matching blocks";
    notifyOverlayChanged();
  }
  // Move the is-active highlight in place (hover/arrow) without a rebuild,
  // keeping the active item scrolled into the menu's visible area (the menu
  // is max-height + overflow-y: auto, so arrowing past the fold must scroll).
  updateActive() {
    const opts = this.root!.querySelectorAll(".wysiwyg-slash-item");
    opts.forEach((o, i) => {
      const on = i === this.selected;
      o.classList.toggle("is-active", on);
      o.setAttribute("aria-selected", on ? "true" : "false");
      if (on) o.scrollIntoView({ block: "nearest" });
    });
    this.updateActiveDescendant(opts.length);
  }
  private updateActiveDescendant(count: number) {
    const dom = this.view.dom as HTMLElement;
    if (count > 0 && this.selected >= 0 && this.selected < count) {
      dom.setAttribute("aria-activedescendant", `${SLASH_OPT_PREFIX}${this.selected}`);
    } else {
      dom.removeAttribute("aria-activedescendant");
    }
  }
  commit(it: SlashItem) {
    const view = this.view;
    // Delete the "/query" span using STORED positions, NOT the live selection.
    // A menu item lives outside the editable, so clicking it can blur/reset the
    // selection in some browsers (Safari collapses it; Chrome keeps it via
    // preventDefault) — reading view.state.selection here would then target the
    // wrong range and the block would silently not appear. slashPos + 1 (the "/")
    // + query length is the exact span, independent of focus/selection.
    const from = this.slashPos;
    const to = Math.min(from + 1 + this.query.length, view.state.doc.content.size);
    view.focus();
    view.dispatch(view.state.tr.delete(from, to));
    this.close();
    if (it.needsUpload) {
      if (typeof onPickImageRef === "function") onPickImageRef();
    } else {
      run(view, it.run()); // it.run is a factory; invoke it to get the command
    }
  }
  close() {
    this.open = false;
    this.query = "";
    if (this.root) this.root.style.display = "none";
    this.setEditorCombobox(false);
    if (this.live) this.live.textContent = "";
    notifyOverlayChanged();
  }
  destroy() {
    scrollFollowers.delete(this.onScroll);
    overlayDismissers.delete(this.onWinBlur);
    document.removeEventListener("pointerdown", this.onDocPointerdown, true);
    window.removeEventListener("blur", this.onWinBlur);
    if (this.root && this.root.parentNode) this.root.parentNode.removeChild(this.root);
    if (this.live && this.live.parentNode) this.live.parentNode.removeChild(this.live);
    this.root = null;
    this.live = null;
  }
}

// Stable ids for the ARIA combobox wiring (editor.aria-controls / -activedescendant).
const SLASH_LISTBOX_ID = "wysiwyg-slash-listbox";
const SLASH_OPT_PREFIX = "wysiwyg-slash-opt-";

// `commit()` references the image picker through this module-level hook set by
// the editor entry (the editor owns the upload capability).
let onPickImageRef: (() => void) | null = null;
export function setSlashImageHook(fn: (() => void) | null) {
  onPickImageRef = fn;
}

// Overlay-visibility hook: the editor entry registers its resize reporter here.
// Overlays (slash menu, bubble, link editor) are position:fixed INSIDE the
// sandboxed frame, and the frame is autosized to the document content — so an
// overlay opening near the bottom would be clipped at the iframe edge unless
// the frame grows to fit it. Every overlay open/close/reposition calls this so
// the host re-measures (content height ∪ overlay extent) and resizes the frame.
// Scroll re-anchoring: overlays are position:fixed (viewport coords). Inside
// the sandboxed frame the viewport never scrolls, so this never fired there —
// but the TRUSTED in-page mount lives on a scrolling page, where a fixed
// overlay visibly detaches from its anchor as the content moves. Overlays
// register a callback here; a capture-phase scroll listener relays to them,
// rAF-throttled.
const scrollFollowers = new Set<() => void>();
let scrollRaf: number | null = null;
function onAnyScroll() {
  if (scrollRaf != null) return;
  scrollRaf = requestAnimationFrame(() => {
    scrollRaf = null;
    scrollFollowers.forEach((fn) => fn());
  });
}
window.addEventListener("scroll", onAnyScroll, { capture: true, passive: true });

// Registry of open-overlay dismissers: the editor entry calls
// dismissAllOverlays() when the HOST page reports an interaction outside the
// frame (protocol event hostPointerdown) — on iOS WebKit the frame gets no
// blur for that, so this is the only dismissal signal that reaches us.
const overlayDismissers = new Set<() => void>();
export function dismissAllOverlays() {
  overlayDismissers.forEach((fn) => fn());
}

let onOverlayChangedRef: (() => void) | null = null;
export function setOverlayChangedHook(fn: (() => void) | null) {
  onOverlayChangedRef = fn;
}
function notifyOverlayChanged() {
  if (onOverlayChangedRef) onOverlayChangedRef();
}

// ---------------------------------------------------------------------------
// Bubble (format) toolbar

interface BubbleOptions {
  getColor?: unknown;
}

export function bubbleToolbarPlugin({ getColor = null }: BubbleOptions = {}) {
  let ctrl: Bubble | null = null;
  return new Plugin({
    key: bubbleKey,
    view(editorView) {
      ctrl = new Bubble(editorView, { getColor });
      return ctrl;
    },
  });
}

// Wire a control so it works for BOTH mouse and keyboard. mousedown only
// preventDefault()s (keeping the editor selection/focus — a toolbar button must
// not steal them), and the ACTION runs on `click`. A native <button> fires
// `click` on Enter/Space too, so this is the single path that makes the whole
// toolbar keyboard-operable (WCAG 2.1.1) without double-invoking on mouse.
function activate(btn: HTMLElement, onPress: () => void) {
  btn.addEventListener("mousedown", (e) => e.preventDefault());
  btn.addEventListener("click", (e) => {
    e.preventDefault();
    onPress();
  });
}

function markButton(
  view: EditorView,
  label: string,
  iconSvg: string,
  active: boolean,
  onPress: () => void
) {
  const btn = el(
    "button",
    {
      type: "button",
      class: "wysiwyg-tbtn" + (active ? " is-active" : ""),
      title: label,
      "aria-label": label,
      "aria-pressed": active ? "true" : "false",
    },
    icon(iconSvg)
  );
  activate(btn, onPress);
  return btn;
}

class Bubble {
  view: EditorView;
  opts: BubbleOptions;
  root: HTMLElement | null;
  colorOpen: boolean;
  colorKind?: "text" | "bg";
  linkEl: HTMLElement | null;

  onWinBlur: () => void;
  onScroll: () => void;

  constructor(view: EditorView, opts: BubbleOptions) {
    this.view = view;
    this.opts = opts;
    this.root = null;
    this.colorOpen = false;
    this.linkEl = null;
    // Focus leaving the frame/page produces no editor transaction, so update()
    // never runs — hide explicitly, like the slash menu (see its constructor).
    this.onWinBlur = () => this.hide();
    window.addEventListener("blur", this.onWinBlur);
    overlayDismissers.add(this.onWinBlur);
    this.onScroll = () => {
      if (this.root && this.root.style.display !== "none") this.position();
    };
    scrollFollowers.add(this.onScroll);
    this.build();
  }
  build() {
    this.root = el("div", {
      class: "wysiwyg-bubble",
      role: "toolbar",
      "aria-label": "Text formatting",
      style: { display: "none" },
    });
    // Escape closes the innermost open layer: color grid → link editor →
    // (nothing; selection dismissal is the caret's job). Consistent with the
    // slash menu, so Esc never strands the user in a sub-popover.
    this.root.addEventListener("keydown", (e) => {
      if (e.key !== "Escape") return;
      if (this.colorOpen) {
        e.preventDefault();
        this.colorOpen = false;
        this.render();
      } else if (this.linkEl) {
        e.preventDefault();
        this.closeLinkEditor();
      }
    });
    overlayParent().appendChild(this.root);
  }
  active(markName: string): boolean {
    const { state } = this.view;
    if (state.selection.empty) return false;
    let active = true;
    state.doc.nodesBetween(state.selection.from, state.selection.to, (node) => {
      if (node.isText && !node.marks.some((m) => m.type.name === markName)) active = false;
    });
    return active;
  }
  update(view: EditorView, prevState: EditorState) {
    this.view = view;
    const { selection } = view.state;
    if (
      selection.empty ||
      !view.editable ||
      !view.hasFocus() ||
      selection.from === selection.to
    ) {
      this.hide();
      return;
    }
    // Don't show over node selections handled elsewhere (images/tables keep their own chrome).
    this.show();
    this.sync();
    this.position();
  }
  show() {
    this.render();
    this.root!.style.display = "flex";
  }
  hide() {
    // Only notify when actually transitioning visible→hidden: hide() runs on
    // every empty-selection transaction (i.e. each keystroke), and the resize
    // report must stay OFF the keystroke path.
    const wasVisible = !!this.root && this.root.style.display !== "none";
    if (this.root) this.root.style.display = "none";
    this.colorOpen = false;
    this.closeLinkEditor();
    if (wasVisible) notifyOverlayChanged();
  }
  render() {
    const root = this.root!;
    root.innerHTML = "";
    const v = this.view;
    root.appendChild(
      markButton(v, "Bold", ICON.bold, this.active("strong"), () => run(v, cmd.toggleBold()))
    );
    root.appendChild(
      markButton(v, "Italic", ICON.italic, this.active("em"), () => run(v, cmd.toggleItalic()))
    );
    root.appendChild(
      markButton(v, "Underline", ICON.underline, this.active("underline"), () => run(v, cmd.toggleUnderline()))
    );
    root.appendChild(
      markButton(v, "Strikethrough", ICON.strike, this.active("strike"), () => run(v, cmd.toggleStrike()))
    );
    root.appendChild(
      markButton(v, "Inline code", ICON.code, this.active("code"), () => run(v, cmd.toggleInlineCode()))
    );
    root.appendChild(el("span", { class: "wysiwyg-tbtn-sep" }));
    root.appendChild(
      markButton(v, "Link", ICON.link, !!cmd.activeLinkMark(v.state), () => this.openLinkEditor())
    );
    // text color
    const textOpen = this.colorOpen && this.colorKind === "text";
    const colorBtn = el(
      "button",
      { type: "button", class: "wysiwyg-tbtn" + (textOpen ? " is-active" : ""), title: "Text color", "aria-label": "Text color", "aria-haspopup": "listbox", "aria-expanded": textOpen ? "true" : "false" },
      icon(ICON.text)
    );
    colorBtn.appendChild(el("span", { class: "wysiwyg-color-bar" }));
    activate(colorBtn, () => this.toggleColorPicker("text"));
    root.appendChild(colorBtn);
    // highlight
    const bgOpen = this.colorOpen && this.colorKind === "bg";
    const hlBtn = el(
      "button",
      { type: "button", class: "wysiwyg-tbtn" + (bgOpen ? " is-active" : ""), title: "Highlight", "aria-label": "Background highlight", "aria-haspopup": "listbox", "aria-expanded": bgOpen ? "true" : "false" },
      icon(ICON.highlight)
    );
    activate(hlBtn, () => this.toggleColorPicker("bg"));
    root.appendChild(hlBtn);

    if (this.colorOpen) this.renderColorPicker();
  }
  sync() {
    /* active states are recomputed in render() each update; nothing extra */
  }
  position() {
    const { selection } = this.view.state;
    const start = this.view.coordsAtPos(selection.from);
    const end = this.view.coordsAtPos(selection.to);
    place(this.root!, {
      left: (start.left + end.right) / 2,
      top: Math.min(start.top, end.top),
      bottom: Math.min(start.bottom, end.bottom),
    });
    notifyOverlayChanged();
  }
  toggleColorPicker(kind: "text" | "bg") {
    this.colorOpen = !this.colorOpen || this.colorKind !== kind;
    this.colorKind = kind;
    if (this.colorOpen) this.renderColorPicker();
    else this.render();
  }
  renderColorPicker() {
    const root = this.root!;
    root.querySelectorAll(".wysiwyg-colorgrid").forEach((n) => n.remove());
    // Shared grid (identical to the persistent toolbar's) — one source of truth.
    root.appendChild(
      buildColorGrid(this.view, this.colorKind!, () => {
        this.colorOpen = false;
        this.render();
      })
    );
  }
  openLinkEditor() {
    this.render();
    const root = this.root!;
    root.querySelectorAll(".wysiwyg-linkpop").forEach((n) => n.remove());
    const v = this.view;
    const existing = cmd.activeLinkMark(v.state);
    const wrap = el("div", { class: "wysiwyg-linkpop", role: "dialog", "aria-label": "Edit link" });
    const hrefInput = el("input", {
      type: "url",
      class: "wysiwyg-link-input",
      placeholder: "https://…",
      value: existing ? existing.attrs.href : "",
      "aria-label": "Link URL",
    });
    const applyBtn = el("button", { type: "button", class: "wysiwyg-link-apply" }, existing ? "Update" : "Apply");
    const removeBtn = el("button", { type: "button", class: "wysiwyg-link-remove" }, "Remove");
    // Inline error/feedback line (no more silent-close on a bad scheme).
    const err = el("div", { class: "wysiwyg-link-error", role: "alert", style: { display: "none" } });
    const doApply = () => {
      const { href, error } = cmd.normalizeLinkInput(hrefInput.value);
      if (error || !href) {
        err.textContent = error || "That link isn't valid";
        err.style.display = "block";
        hrefInput.focus();
        return;
      }
      run(v, cmd.setLink({ href }));
      this.closeLinkEditor();
    };
    activate(applyBtn, doApply);
    activate(removeBtn, () => {
      run(v, cmd.unsetLink());
      this.closeLinkEditor();
    });
    hrefInput.addEventListener("input", () => {
      err.style.display = "none";
    });
    hrefInput.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        doApply();
      } else if (e.key === "Escape") {
        e.preventDefault();
        this.closeLinkEditor();
      }
    });
    wrap.appendChild(hrefInput);
    wrap.appendChild(applyBtn);
    if (existing) {
      // Open the current target in a new tab — the missing "open link" affordance.
      const openBtn = el("a", {
        class: "wysiwyg-link-open",
        href: existing.attrs.href,
        target: "_blank",
        rel: "noopener noreferrer",
        title: "Open link",
        "aria-label": "Open link in a new tab",
      }, "↗");
      wrap.appendChild(openBtn);
      wrap.appendChild(removeBtn);
    }
    wrap.appendChild(err);
    root.appendChild(wrap);
    this.linkEl = wrap;
    setTimeout(() => hrefInput.focus(), 0);
  }
  closeLinkEditor() {
    if (this.linkEl && this.linkEl.parentNode) this.linkEl.parentNode.removeChild(this.linkEl);
    this.linkEl = null;
  }
  destroy() {
    scrollFollowers.delete(this.onScroll);
    overlayDismissers.delete(this.onWinBlur);
    window.removeEventListener("blur", this.onWinBlur);
    if (this.root && this.root.parentNode) this.root.parentNode.removeChild(this.root);
    this.root = null;
  }
}

// ---------------------------------------------------------------------------
// Block drag handle (pointer-based reorder of top-level blocks)

export function dragHandlePlugin() {
  let ctrl: DragHandle | null = null;
  return new Plugin({
    key: handleKey,
    view(editorView) {
      ctrl = new DragHandle(editorView);
      return ctrl;
    },
  });
}

/** A top-level block under the pointer: node + document span + child index. */
interface BlockInfo {
  node: PMNode;
  start: number;
  end: number;
  index: number;
}

class DragHandle {
  view: EditorView;
  handle: HTMLElement | null;
  active: boolean;
  fromIndex: number;
  indicator: HTMLElement | null;
  hideTimer: ReturnType<typeof setTimeout> | null;
  pointerId: number | null;
  onScroll!: () => void; // assigned in build()
  currentBlock?: BlockInfo;
  targetIndex?: number | null;
  fromNode?: PMNode | null;
  fromStart?: number;

  constructor(view: EditorView) {
    this.view = view;
    this.handle = null;
    this.active = false;
    this.fromIndex = -1;
    this.indicator = null;
    this.hideTimer = null;
    this.onMove = this.onMove.bind(this);
    this.onUp = this.onUp.bind(this);
    this.pointerId = null;
    this.build();
  }
  build() {
    this.handle = el(
      "div",
      {
        class: "wysiwyg-draghandle",
        role: "button",
        tabindex: "-1",
        "aria-label": "Drag to reorder block",
        title: "Drag to reorder",
        style: { display: "none" },
      },
      el("span", { class: "wysiwyg-draghandle-grip", "aria-hidden": "true" })
    );
    // Pointer events (not mouse) so touch + pen can drag too — the handle was
    // completely unusable on phones/tablets. touch-action:none (set in CSS)
    // stops the browser from scrolling instead of dragging.
    this.handle.addEventListener("pointerdown", (e) => this.start(e));
    overlayParent().appendChild(this.handle);

    this.indicator = el("div", { class: "wysiwyg-drop-indicator", style: { display: "none" } });
    overlayParent().appendChild(this.indicator);

    view_dom(this.view).addEventListener("mousemove", (e) => this.track(e));
    // Scrolling detaches a fixed-position handle from its block; hide it (the
    // next mouse move re-anchors it) — but never during an active drag.
    this.onScroll = () => {
      if (!this.active) {
        this.cancelHide();
        this.hideHandle();
      }
    };
    scrollFollowers.add(this.onScroll);
    // The handle sits OUTSIDE the editor's left edge, so moving the pointer
    // toward it leaves the editor — hiding instantly on mouseleave made the
    // handle impossible to reach. Keep it when leaving toward the handle, and
    // otherwise hide after a grace delay so crossing the gap is safe.
    view_dom(this.view).addEventListener("mouseleave", (e) => {
      if (e.relatedTarget && this.handle!.contains(e.relatedTarget as Node)) return;
      this.scheduleHide();
    });
    this.handle.addEventListener("mouseenter", () => this.cancelHide());
    this.handle.addEventListener("mouseleave", () => this.scheduleHide());
  }
  scheduleHide() {
    this.cancelHide();
    this.hideTimer = setTimeout(() => this.hideHandle(), 250);
  }
  cancelHide() {
    if (this.hideTimer) {
      clearTimeout(this.hideTimer);
      this.hideTimer = null;
    }
  }
  update(view: EditorView, prevState: EditorState) {
    this.view = view;
  }
  track(e: MouseEvent) {
    if (this.active) return;
    this.cancelHide();
    const block = this.blockAtPoint(e.clientX, e.clientY);
    if (!block) {
      this.hideHandle();
      return;
    }
    const coords = this.view.coordsAtPos(block.start);
    const handle = this.handle!;
    handle.style.display = "flex";
    handle.style.left = `${Math.round(coords.left - 26)}px`;
    handle.style.top = `${Math.round(coords.top - 2)}px`;
    this.currentBlock = block;
  }
  hideHandle() {
    if (!this.active && this.handle) this.handle.style.display = "none";
  }
  blockAtPoint(x: number, y: number): BlockInfo | null {
    const pos = this.view.posAtCoords({ left: x, top: y });
    if (!pos) return null;
    let $pos: ResolvedPos;
    try {
      $pos = this.view.state.doc.resolve(pos.pos);
    } catch (e) {
      return null;
    }
    const depth = Math.min(1, $pos.depth);
    if (depth < 1) return null;
    const node = $pos.node(1);
    const start = $pos.before(1);
    return { node, start, end: start + node.nodeSize, index: $pos.index(0) };
  }
  start(e: PointerEvent) {
    if (!this.currentBlock) return;
    e.preventDefault();
    this.active = true;
    this.pointerId = e.pointerId;
    try { this.handle!.setPointerCapture(e.pointerId); } catch (err) { /* best effort */ }
    this.fromIndex = this.currentBlock.index;
    this.fromNode = this.currentBlock.node;
    this.fromStart = this.currentBlock.start;
    document.body.style.userSelect = "none";
    window.addEventListener("pointermove", this.onMove);
    window.addEventListener("pointerup", this.onUp);
    window.addEventListener("pointercancel", this.onUp);
  }
  onMove(e: PointerEvent) {
    const block = this.blockAtPoint(e.clientX, e.clientY);
    if (!block) return;
    this.targetIndex = block.index;
    // A full-width horizontal line at the block's top edge (reads as "insert
    // here", not a stray caret). Span the editable's content box.
    const editRect = view_dom(this.view).getBoundingClientRect();
    const coords = this.view.coordsAtPos(block.start);
    const indicator = this.indicator!;
    indicator.style.display = "block";
    indicator.style.left = `${Math.round(editRect.left)}px`;
    indicator.style.width = `${Math.round(editRect.width)}px`;
    indicator.style.top = `${Math.round(coords.top - 2)}px`;
  }
  onUp(e: PointerEvent) {
    window.removeEventListener("pointermove", this.onMove);
    window.removeEventListener("pointerup", this.onUp);
    window.removeEventListener("pointercancel", this.onUp);
    if (this.pointerId != null) {
      try { this.handle!.releasePointerCapture(this.pointerId); } catch (err) { /* released already */ }
      this.pointerId = null;
    }
    document.body.style.userSelect = "";
    this.indicator!.style.display = "none";
    this.active = false;
    if (this.fromIndex < 0 || this.targetIndex == null) {
      this.cleanupDrag();
      return;
    }
    const tr = this.moveBlock(this.fromIndex, this.targetIndex);
    if (tr) this.view.dispatch(tr.scrollIntoView());
    this.cleanupDrag();
  }
  moveBlock(from: number, to: number) {
    const doc = this.view.state.doc;
    if (from === to || from < 0 || to < 0 || from >= doc.childCount || to >= doc.childCount)
      return null;
    const nodes: PMNode[] = [];
    doc.forEach((child) => nodes.push(child));
    const [moved] = nodes.splice(from, 1);
    nodes.splice(to, 0, moved);
    return this.view.state.tr.replaceWith(0, doc.content.size, nodes);
  }
  cleanupDrag() {
    this.fromIndex = -1;
    this.targetIndex = null;
    this.fromNode = null;
  }
  destroy() {
    // Unregister the scroll follower and clear any pending hide — without this
    // every view destroy (re-init, trusted destroy()) leaks a stale closure in
    // the module-level scrollFollowers set, retaining the whole view graph.
    scrollFollowers.delete(this.onScroll);
    this.cancelHide();
    if (this.handle && this.handle.parentNode) this.handle.parentNode.removeChild(this.handle);
    if (this.indicator && this.indicator.parentNode)
      this.indicator.parentNode.removeChild(this.indicator);
    this.handle = null;
    this.indicator = null;
  }
}

function view_dom(view: EditorView) {
  return view.dom;
}

// ---------------------------------------------------------------------------
// Public plugin bundle factory

// ---------------------------------------------------------------------------
// Persistent formatting toolbar (sticky bar above the editable)

export function toolbarPlugin() {
  let ctrl: Toolbar | null = null;
  return new Plugin({
    key: toolbarKey,
    view(editorView) {
      ctrl = new Toolbar(editorView);
      return ctrl;
    },
  });
}

// Block-type <select> options → the command that applies each.
const BLOCK_TYPES: Array<{ value: string; label: string; run: () => Command }> = [
  { value: "paragraph", label: "Text", run: cmd.setParagraph },
  { value: "h1", label: "Heading 1", run: () => cmd.setHeading(1) },
  { value: "h2", label: "Heading 2", run: () => cmd.setHeading(2) },
  { value: "h3", label: "Heading 3", run: () => cmd.setHeading(3) },
  { value: "blockquote", label: "Quote", run: cmd.toggleBlockquote },
  { value: "code_block", label: "Code", run: cmd.setCodeBlock },
  { value: "bullet_list", label: "Bulleted list", run: cmd.toggleBulletList },
  { value: "ordered_list", label: "Numbered list", run: cmd.toggleOrderedList },
  { value: "task_list", label: "To-do list", run: cmd.toggleTaskList },
];

class Toolbar {
  view: EditorView;
  root: HTMLElement | null;
  bar: HTMLElement | null = null;
  wrap: HTMLElement | null = null;
  ro: ResizeObserver | null = null;
  updateScrollCue: () => void = () => {};
  blockSelect: HTMLSelectElement | null = null;
  colorOpen: "text" | "bg" | null = null;
  colorPop: HTMLElement | null = null;
  buttons: Array<{ el: HTMLElement; isActive: () => boolean }> = [];

  constructor(view: EditorView) {
    this.view = view;
    this.root = null;
    this.build();
  }
  build() {
    const v = this.view;
    const bar = el("div", { class: "wysiwyg-toolbar", role: "toolbar", "aria-label": "Formatting" });
    this.root = bar;
    this.bar = bar;

    const group = (...kids: (HTMLElement | null)[]) => {
      const g = el("div", { class: "wysiwyg-tb-group" });
      kids.forEach((k) => k && g.appendChild(k));
      return g;
    };
    const sep = () => el("span", { class: "wysiwyg-tbtn-sep" });

    // Undo / redo (also the mobile undo path — no ⌘Z on touch keyboards).
    const undoBtn = this.tbtn("Undo", ICON.undo, () => run(v, cmd.undoCmd()));
    const redoBtn = this.tbtn("Redo", ICON.redo, () => run(v, cmd.redoCmd()));

    // Block-type selector.
    const select = el("select", { class: "wysiwyg-tb-select", "aria-label": "Block type" }) as HTMLSelectElement;
    BLOCK_TYPES.forEach((b) => select.appendChild(el("option", { value: b.value }, b.label)));
    select.addEventListener("mousedown", () => v.focus());
    select.addEventListener("change", () => {
      const b = BLOCK_TYPES.find((x) => x.value === select.value);
      if (b) run(v, b.run());
    });
    this.blockSelect = select;

    // Marks.
    const mark = (label: string, ic: string, name: string, c: () => Command) => {
      const b = this.tbtn(label, ic, () => run(v, c()));
      this.buttons.push({ el: b, isActive: () => this.markActive(name) });
      return b;
    };

    // Alignment.
    const align = (label: string, ic: string, a: "left" | "center" | "right") => {
      const b = this.tbtn(label, ic, () => run(v, cmd.setAlign(a)));
      this.buttons.push({ el: b, isActive: () => cmd.currentAlign(this.view.state) === a });
      return b;
    };

    bar.appendChild(group(undoBtn, redoBtn));
    bar.appendChild(sep());
    bar.appendChild(group(select));
    bar.appendChild(sep());
    bar.appendChild(
      group(
        mark("Bold", ICON.bold, "strong", cmd.toggleBold),
        mark("Italic", ICON.italic, "em", cmd.toggleItalic),
        mark("Underline", ICON.underline, "underline", cmd.toggleUnderline),
        mark("Strikethrough", ICON.strike, "strike", cmd.toggleStrike),
        mark("Inline code", ICON.code, "code", cmd.toggleInlineCode)
      )
    );
    bar.appendChild(sep());
    bar.appendChild(group(this.linkButton(), this.colorButton("text"), this.colorButton("bg")));
    bar.appendChild(sep());
    bar.appendChild(
      group(
        align("Align left", ICON.alignLeft, "left"),
        align("Align center", ICON.alignCenter, "center"),
        align("Align right", ICON.alignRight, "right")
      )
    );
    bar.appendChild(sep());
    bar.appendChild(group(this.tbtn("Clear formatting", ICON.clear, () => run(v, cmd.clearFormatting()))));

    // Wrap the scrolling bar so we can show edge fade-gradients (and a thin
    // scrollbar) as a "there's more, scroll sideways" cue on narrow frames.
    const wrap = el("div", { class: "wysiwyg-toolbar-wrap" });
    wrap.appendChild(bar);
    this.wrap = wrap;
    const editable = v.dom;
    if (editable.parentNode) editable.parentNode.insertBefore(wrap, editable);

    // Toggle the affordance classes on scroll and when the frame resizes.
    this.updateScrollCue = () => {
      const b = this.bar;
      if (!b) return;
      const atStart = b.scrollLeft <= 1;
      const atEnd = b.scrollLeft + b.clientWidth >= b.scrollWidth - 1;
      wrap.classList.toggle("can-scroll-left", !atStart);
      wrap.classList.toggle("can-scroll-right", !atEnd);
    };
    bar.addEventListener("scroll", this.updateScrollCue, { passive: true });
    if (typeof ResizeObserver !== "undefined") {
      this.ro = new ResizeObserver(() => this.updateScrollCue());
      this.ro.observe(bar);
    }
    // Initial state (after layout).
    setTimeout(() => this.updateScrollCue(), 0);
    this.sync();
  }
  tbtn(label: string, ic: string, onPress: () => void) {
    const b = el(
      "button",
      { type: "button", class: "wysiwyg-tbtn", title: label, "aria-label": label, "aria-pressed": "false" },
      icon(ic)
    );
    activate(b, onPress);
    return b;
  }
  linkButton() {
    const b = this.tbtn("Link", ICON.link, () => {
      const existing = cmd.activeLinkMark(this.view.state);
      openLinkPopover(this.view, b, !!existing);
    });
    this.buttons.push({ el: b, isActive: () => !!cmd.activeLinkMark(this.view.state) });
    return b;
  }
  colorButton(kind: "text" | "bg") {
    const label = kind === "bg" ? "Highlight" : "Text color";
    const b = this.tbtn(label, kind === "bg" ? ICON.highlight : ICON.text, () => {
      if (this.colorOpen === kind) {
        this.closeColor();
        return;
      }
      this.closeColor();
      this.colorOpen = kind;
      this.colorPop = buildColorGrid(this.view, kind, () => this.closeColor());
      b.setAttribute("aria-expanded", "true");
      b.parentElement!.appendChild(this.colorPop);
    });
    return b;
  }
  closeColor() {
    if (this.colorPop && this.colorPop.parentNode) this.colorPop.parentNode.removeChild(this.colorPop);
    this.colorPop = null;
    this.colorOpen = null;
    this.root!.querySelectorAll('[aria-expanded="true"]').forEach((e) => e.setAttribute("aria-expanded", "false"));
  }
  markActive(name: string): boolean {
    const { state } = this.view;
    const { from, $from, to, empty } = state.selection;
    const type = state.schema.marks[name];
    if (!type) return false;
    if (empty) return !!type.isInSet(state.storedMarks || $from.marks());
    return state.doc.rangeHasMark(from, to, type);
  }
  update() {
    this.sync();
  }
  sync() {
    // Reflect the current block type in the select + active states on buttons.
    if (this.blockSelect) this.blockSelect.value = this.currentBlockType();
    for (const b of this.buttons) {
      const on = b.isActive();
      b.el.classList.toggle("is-active", on);
      b.el.setAttribute("aria-pressed", on ? "true" : "false");
    }
  }
  currentBlockType(): string {
    const { $from } = this.view.state.selection;
    for (let d = $from.depth; d >= 0; d--) {
      const n = $from.node(d);
      const t = n.type.name;
      if (t === "heading") return "h" + n.attrs.level;
      if (["blockquote", "code_block", "bullet_list", "ordered_list", "task_list", "paragraph"].includes(t)) {
        // For list wrappers we want the LIST type, not the inner paragraph.
        if (t === "paragraph") continue;
        return t;
      }
    }
    return "paragraph";
  }
  destroy() {
    this.closeColor();
    if (this.ro) { this.ro.disconnect(); this.ro = null; }
    const outer = this.wrap || this.root;
    if (outer && outer.parentNode) outer.parentNode.removeChild(outer);
    this.root = null;
    this.wrap = null;
    this.bar = null;
  }
}

// Shared color grid (used by the bubble toolbar AND the persistent toolbar).
function buildColorGrid(view: EditorView, kind: "text" | "bg", onDone: () => void): HTMLElement {
  const grid = el("div", {
    class: "wysiwyg-colorgrid wysiwyg-colorgrid-" + kind,
    role: "listbox",
    "aria-label": kind === "bg" ? "Highlight color" : "Text color",
  });
  COLOR_SLOTS.forEach((slot) => {
    const sw = el("button", {
      type: "button",
      class: "wysiwyg-swatch wysiwyg-swatch-" + slot,
      role: "option",
      title: slot,
      "aria-label": slot,
      "data-slot": slot,
    });
    sw.style.color = `var(--wysiwyg-fg-${slot}, var(--color-text, currentColor))`;
    sw.style.background = `var(--wysiwyg-bg-${slot}, var(--color-surface, transparent))`;
    activate(sw, () => {
      run(view, kind === "bg" ? cmd.toggleBgColor(slot) : cmd.toggleTextColor(slot));
      onDone();
    });
    grid.appendChild(sw);
  });
  const clear = el("button", { type: "button", class: "wysiwyg-swatch wysiwyg-swatch-clear", title: "Clear color", "aria-label": "Clear color" }, "×");
  activate(clear, () => {
    run(view, cmd.clearColor());
    onDone();
  });
  grid.appendChild(clear);
  return grid;
}

// Shared link popover (bubble + toolbar). Anchored under `anchor`.
function openLinkPopover(view: EditorView, anchor: HTMLElement, existing: boolean) {
  document.querySelectorAll(".wysiwyg-linkpop-float").forEach((n) => n.remove());
  const wrap = el("div", { class: "wysiwyg-linkpop wysiwyg-linkpop-float", role: "dialog", "aria-label": "Edit link" });
  const cur = cmd.activeLinkMark(view.state);
  const input = el("input", {
    type: "url",
    class: "wysiwyg-link-input",
    placeholder: "https://…",
    value: cur ? cur.attrs.href : "",
    "aria-label": "Link URL",
  }) as HTMLInputElement;
  const applyBtn = el("button", { type: "button", class: "wysiwyg-link-apply" }, existing ? "Update" : "Apply");
  const err = el("div", { class: "wysiwyg-link-error", role: "alert", style: { display: "none" } });
  const close = () => { if (wrap.parentNode) wrap.parentNode.removeChild(wrap); };
  const apply = () => {
    const { href, error } = cmd.normalizeLinkInput(input.value);
    if (error || !href) { err.textContent = error || "That link isn't valid"; err.style.display = "block"; input.focus(); return; }
    run(view, cmd.setLink({ href }));
    close();
  };
  activate(applyBtn, apply);
  input.addEventListener("input", () => (err.style.display = "none"));
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); apply(); }
    else if (e.key === "Escape") { e.preventDefault(); close(); }
  });
  wrap.appendChild(input);
  wrap.appendChild(applyBtn);
  if (existing) {
    activate(el("button", { type: "button", class: "wysiwyg-link-remove" }, "Remove") as HTMLElement, () => {});
    const rm = el("button", { type: "button", class: "wysiwyg-link-remove" }, "Remove");
    activate(rm, () => { run(view, cmd.unsetLink()); close(); });
    const open = el("a", { class: "wysiwyg-link-open", href: cur ? cur.attrs.href : "#", target: "_blank", rel: "noopener noreferrer", title: "Open link", "aria-label": "Open link in a new tab" }, "↗");
    wrap.appendChild(open);
    wrap.appendChild(rm);
  }
  wrap.appendChild(err);
  overlayParent().appendChild(wrap);
  const r = anchor.getBoundingClientRect();
  place(wrap, { left: r.left + r.width / 2, top: r.top, bottom: r.bottom }, "below", 6);
  setTimeout(() => input.focus(), 0);
  // Dismiss on outside pointerdown.
  const onDown = (e: PointerEvent) => {
    if (!(e.target instanceof Node)) return;
    if (wrap.contains(e.target) || anchor.contains(e.target)) return;
    close();
    document.removeEventListener("pointerdown", onDown, true);
  };
  setTimeout(() => document.addEventListener("pointerdown", onDown, true), 0);
}

// ---------------------------------------------------------------------------
// Contextual block controls: table structure + code-block language

const CODE_LANGS = ["", "javascript", "typescript", "go", "python", "rust", "json", "html", "css", "bash", "sql", "markdown"];

export function blockControlsPlugin() {
  let ctrl: BlockControls | null = null;
  return new Plugin({
    key: blockCtlKey,
    view(editorView) {
      ctrl = new BlockControls(editorView);
      return ctrl;
    },
  });
}

class BlockControls {
  view: EditorView;
  bar: HTMLElement | null;
  onScroll: () => void;

  constructor(view: EditorView) {
    this.view = view;
    this.bar = el("div", { class: "wysiwyg-blockctl", style: { display: "none" } });
    overlayParent().appendChild(this.bar);
    this.onScroll = () => this.reposition();
    scrollFollowers.add(this.onScroll);
  }
  update() {
    this.render();
  }
  reposition() {
    if (this.bar && this.bar.style.display !== "none") this.render();
  }
  render() {
    const v = this.view;
    const bar = this.bar!;
    const { $from } = v.state.selection;
    // Find an enclosing table or code_block.
    let table: { pos: number } | null = null;
    let codePos = -1;
    for (let d = $from.depth; d > 0; d--) {
      const n = $from.node(d);
      if (n.type.name === "table" && !table) table = { pos: $from.before(d) };
      if (n.type.name === "code_block" && codePos < 0) codePos = $from.before(d);
    }
    if (!table && codePos < 0) {
      bar.style.display = "none";
      return;
    }
    bar.innerHTML = "";
    let anchorPos: number;
    if (codePos >= 0) {
      anchorPos = codePos;
      bar.appendChild(this.codeControls(codePos));
    } else {
      anchorPos = table!.pos;
      bar.appendChild(this.tableControls());
    }
    bar.style.display = "flex";
    // Anchor above the block's top-left.
    try {
      const coords = v.coordsAtPos(anchorPos + 1);
      place(bar, { left: coords.left + 40, top: coords.top, bottom: coords.top }, "above", 6);
    } catch (e) {
      bar.style.display = "none";
    }
  }
  tableControls(): HTMLElement {
    const v = this.view;
    const g = el("div", { class: "wysiwyg-blockctl-group", role: "toolbar", "aria-label": "Table" });
    const btn = (label: string, c: () => Command) => {
      const b = el("button", { type: "button", class: "wysiwyg-blockctl-btn", title: label, "aria-label": label }, label);
      activate(b, () => run(v, c()));
      return b;
    };
    g.appendChild(btn("+ Row", () => cmd.addRowAfter as unknown as Command));
    g.appendChild(btn("+ Col", () => cmd.addColumnAfter as unknown as Command));
    g.appendChild(btn("− Row", () => cmd.deleteRow as unknown as Command));
    g.appendChild(btn("− Col", () => cmd.deleteColumn as unknown as Command));
    g.appendChild(btn("Header", () => cmd.toggleHeaderRow as unknown as Command));
    g.appendChild(btn("Delete", () => cmd.deleteTable as unknown as Command));
    return g;
  }
  codeControls(pos: number): HTMLElement {
    const v = this.view;
    const node = v.state.doc.nodeAt(pos);
    const cur = (node && node.attrs.language) || "";
    const g = el("div", { class: "wysiwyg-blockctl-group" });
    const sel = el("select", { class: "wysiwyg-blockctl-select", "aria-label": "Code language" }) as HTMLSelectElement;
    CODE_LANGS.forEach((l) => {
      const o = el("option", { value: l }, l || "plain text") as HTMLOptionElement;
      if (l === cur) o.selected = true;
      sel.appendChild(o);
    });
    sel.addEventListener("mousedown", () => v.focus());
    sel.addEventListener("change", () => {
      const lang = sel.value || null;
      const n = v.state.doc.nodeAt(pos);
      if (n) v.dispatch(v.state.tr.setNodeMarkup(pos, undefined, { ...n.attrs, language: lang }));
      v.focus();
    });
    g.appendChild(sel);
    return g;
  }
  destroy() {
    scrollFollowers.delete(this.onScroll);
    if (this.bar && this.bar.parentNode) this.bar.parentNode.removeChild(this.bar);
    this.bar = null;
  }
}

// ---------------------------------------------------------------------------
// Word/character count status bar (below the editable)

export function statusBarPlugin() {
  let ctrl: StatusBar | null = null;
  return new Plugin({
    key: statusKey,
    view(editorView) {
      ctrl = new StatusBar(editorView);
      return ctrl;
    },
  });
}

class StatusBar {
  view: EditorView;
  root: HTMLElement | null;
  timer: ReturnType<typeof setTimeout> | null = null;

  constructor(view: EditorView) {
    this.view = view;
    this.root = el("div", { class: "wysiwyg-statusbar", role: "status", "aria-live": "off" });
    // Below the editable, at the bottom of the editor.
    const editable = view.dom;
    if (editable.parentNode) editable.parentNode.appendChild(this.root);
    this.refresh();
  }
  update() {
    // Off the keystroke path: debounce the (cheap) recount.
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => this.refresh(), 250);
  }
  refresh() {
    if (!this.root) return;
    const text = this.view.state.doc.textBetween(0, this.view.state.doc.content.size, " ", " ");
    const chars = text.length;
    const words = text.trim() ? text.trim().split(/\s+/).length : 0;
    this.root.textContent = `${words} word${words === 1 ? "" : "s"} · ${chars} character${chars === 1 ? "" : "s"}`;
  }
  destroy() {
    if (this.timer) clearTimeout(this.timer);
    if (this.root && this.root.parentNode) this.root.parentNode.removeChild(this.root);
    this.root = null;
  }
}

export interface UiPluginsOptions {
  onPickImage?: (() => void) | null;
  /** Persistent top toolbar (default true). */
  toolbar?: boolean;
  /** Word/character count status bar (default true). */
  statusBar?: boolean;
}

export function uiPlugins(opts: UiPluginsOptions = {}) {
  const plugins = [
    slashMenuPlugin({ onPickImage: opts.onPickImage }),
    bubbleToolbarPlugin(),
    dragHandlePlugin(),
  ];
  if (opts.toolbar !== false) plugins.unshift(toolbarPlugin());
  if (opts.statusBar !== false) plugins.push(statusBarPlugin());
  plugins.push(blockControlsPlugin());
  return plugins;
}
