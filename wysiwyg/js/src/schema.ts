// ProseMirror schema — the SINGLE source of truth for block schema v1
// (`schemaVersion = "wysiwyg-v1"`). This file is the anti-drift contract shared
// with the Go SSR renderer (Worker E): every node/mark here MUST be handled by
// both renderers. Adding a block = update docs/design/schema-v1.md + both sides.
//
// See docs/design/schema-v1.md §1 (nodes), §2 (marks), §3 (colors).
//
// Lists are defined locally (not via addListNodes) so `ordered_list` uses the
// contract attr name `start` (the library default is `order`). Tables use the
// standard prosemirror-tables node model (colspan/rowspan/colwidth), which the
// SSR renderer reads identically. Colors are named palette slots rendered as
// `var(--wysiwyg-fg-<slot>)` / `var(--wysiwyg-bg-<slot>)` — NEVER raw hex.

import { Schema, type NodeSpec, type MarkSpec, type Node as PMNode } from "prosemirror-model";
import { tableNodes } from "prosemirror-tables";

export const SCHEMA_VERSION = "wysiwyg-v1";
export const PROTOCOL_VERSION = 1;

// §3 — the allowed named color slots. textColor.color / bgColor.color MUST be one
// of these; the value stored in JSON is the SLOT NAME (theme-portable), not hex.
export const COLOR_SLOTS = [
  "default",
  "gray",
  "brown",
  "orange",
  "yellow",
  "green",
  "blue",
  "purple",
  "pink",
  "red",
];

const COLOR_SLOT_SET = new Set(COLOR_SLOTS);

/** Coerce a color slot name to a valid slot, falling back to "default". */
export function colorSlot(value: unknown): string {
  const v = String(value || "").toLowerCase();
  return COLOR_SLOT_SET.has(v) ? v : "default";
}

// Allowed callout variants (schema §1).
export const CALLOUT_VARIANTS = ["info", "warn", "success", "danger", "note"];
export function calloutVariant(value: unknown): string {
  const v = String(value || "").toLowerCase();
  return CALLOUT_VARIANTS.includes(v) ? v : "info";
}

const px = (value: string | null) => (value == null || value === "" ? null : Number(value) || null);

// ---------------------------------------------------------------------------
// Node specs

const doc: NodeSpec = {
  content: "block+",
};

// Text alignment (schema §1 extension): a `left`(default)/`center`/`right`/
// `justify` attr on the textblocks that carry running text. `left` emits no
// style (the default), so existing docs and the JSON stay clean.
const ALIGNS = ["left", "center", "right", "justify"];
export function normalizeAlign(value: unknown): string {
  const v = String(value || "").toLowerCase();
  return ALIGNS.includes(v) ? v : "left";
}
function alignAttrs(dom: HTMLElement) {
  const a = normalizeAlign(dom.style.textAlign || dom.getAttribute("data-align"));
  return { align: a };
}
function alignDOMAttrs(node: PMNode): Record<string, string> {
  const a = normalizeAlign(node.attrs.align);
  return a === "left" ? {} : { style: `text-align:${a}`, "data-align": a };
}

const paragraph: NodeSpec = {
  group: "block",
  content: "inline*",
  attrs: { align: { default: "left", validate: "string" } },
  parseDOM: [{ tag: "p", getAttrs: (dom) => alignAttrs(dom as HTMLElement) }],
  toDOM(node) {
    return ["p", alignDOMAttrs(node), 0];
  },
};

const heading: NodeSpec = {
  group: "block",
  content: "inline*",
  attrs: {
    level: { default: 1, validate: "number" },
    align: { default: "left", validate: "string" },
  },
  defining: true,
  parseDOM: [1, 2, 3, 4, 5, 6].map((level) => ({
    tag: `h${level}`,
    getAttrs: (dom) => ({ level, ...alignAttrs(dom as HTMLElement) }),
  })),
  toDOM(node) {
    return [`h${node.attrs.level}`, alignDOMAttrs(node), 0];
  },
};

const blockquote: NodeSpec = {
  group: "block",
  content: "block+",
  defining: true,
  parseDOM: [{ tag: "blockquote" }],
  toDOM() {
    return ["blockquote", 0];
  },
};

// `code: true` ⇒ text carries NO marks (schema §1 note). `language` is stored as
// a data-attr so round-trip is lossless; the fenced markdown uses ```lang.
const code_block: NodeSpec = {
  group: "block",
  content: "text*",
  marks: "",
  code: true,
  defining: true,
  attrs: { language: { default: null, validate: "string|null" } },
  parseDOM: [
    {
      tag: "pre",
      preserveWhitespace: "full",
      getAttrs: (dom) => ({
        language: dom.getAttribute("data-language") || null,
      }),
    },
  ],
  toDOM(node) {
    return [
      "pre",
      node.attrs.language ? { "data-language": node.attrs.language } : {},
      ["code", 0],
    ];
  },
};

const divider: NodeSpec = {
  group: "block",
  content: "",
  selectable: true,
  atom: true,
  defining: true,
  parseDOM: [{ tag: "hr" }],
  toDOM() {
    return ["hr", { class: "wysiwyg-divider" }];
  },
};

// --- lists (local spec so ordered_list uses `start`, matching the contract) ---

const bullet_list: NodeSpec = {
  group: "block",
  content: "list_item+",
  parseDOM: [{ tag: "ul" }],
  toDOM() {
    return ["ul", 0];
  },
};

const ordered_list: NodeSpec = {
  group: "block",
  content: "list_item+",
  attrs: { start: { default: 1, validate: "number" } },
  parseDOM: [
    {
      tag: "ol",
      getAttrs: (dom) => ({
        start: dom.hasAttribute("start") ? Number(dom.getAttribute("start")) : 1,
      }),
    },
  ],
  toDOM(node) {
    return node.attrs.start === 1 ? ["ol", 0] : ["ol", { start: node.attrs.start }, 0];
  },
};

const list_item: NodeSpec = {
  content: "paragraph block*",
  parseDOM: [{ tag: "li" }],
  toDOM() {
    return ["li", 0];
  },
  defining: true,
};

// --- task lists (GFM task lists; checkbox is a non-editable decoration) ---

const task_list: NodeSpec = {
  group: "block",
  content: "task_item+",
  defining: true,
  parseDOM: [{ tag: "ul[data-type='wysiwyg-task']" }],
  toDOM() {
    return ["ul", { "data-type": "wysiwyg-task", class: "wysiwyg-task-list" }, 0];
  },
};

const task_item: NodeSpec = {
  content: "paragraph block*",
  attrs: { checked: { default: false, validate: "boolean" } },
  defining: true,
  parseDOM: [
    {
      tag: "li[data-type='wysiwyg-task-item']",
      getAttrs: (dom) => ({ checked: dom.getAttribute("data-checked") === "true" }),
    },
  ],
  toDOM(node) {
    return [
      "li",
      {
        "data-type": "wysiwyg-task-item",
        "data-checked": node.attrs.checked ? "true" : "false",
        class: "wysiwyg-task-item" + (node.attrs.checked ? " is-checked" : ""),
      },
      [
        "span",
        {
          class: "wysiwyg-task-checkbox",
          contenteditable: "false",
          role: "checkbox",
          "aria-checked": node.attrs.checked ? "true" : "false",
          tabindex: "0",
        },
      ],
      ["div", { class: "wysiwyg-task-content" }, 0],
    ];
  },
};

// --- callout (admonition); lossy in markdown as a marked blockquote ---

const CALLOUT_ATTR = "data-callout";
const callout: NodeSpec = {
  group: "block",
  content: "block+",
  defining: true,
  attrs: {
    variant: { default: "info", validate: "string" },
    icon: { default: null, validate: "string|null" },
  },
  parseDOM: [
    {
      tag: `div[${CALLOUT_ATTR}]`,
      getAttrs: (dom) => ({
        variant: calloutVariant(dom.getAttribute(CALLOUT_ATTR)),
        icon: dom.getAttribute("data-icon") || null,
      }),
    },
  ],
  toDOM(node) {
    return [
      "div",
      {
        class: `wysiwyg-callout wysiwyg-callout-${calloutVariant(node.attrs.variant)}`,
        [CALLOUT_ATTR]: calloutVariant(node.attrs.variant),
        "data-icon": node.attrs.icon || undefined,
      },
      ["div", { class: "wysiwyg-callout-body" }, 0],
    ];
  },
};

// --- toggle (collapsible <details>-style); HTML-passthrough in markdown ---

const toggle: NodeSpec = {
  group: "block",
  content: "toggle_summary content",
  defining: true,
  attrs: { open: { default: false, validate: "boolean" } },
  parseDOM: [
    {
      tag: "div[data-type='wysiwyg-toggle']",
      getAttrs: (dom) => ({ open: dom.getAttribute("data-open") !== "false" }),
    },
  ],
  toDOM(node) {
    return [
      "div",
      {
        "data-type": "wysiwyg-toggle",
        "data-open": node.attrs.open ? "true" : "false",
        class: "wysiwyg-toggle" + (node.attrs.open ? " is-open" : ""),
      },
      0,
    ];
  },
};

const toggle_summary: NodeSpec = {
  content: "inline*",
  defining: true,
  parseDOM: [{ tag: "div[data-type='wysiwyg-toggle-summary']" }],
  toDOM() {
    return [
      "div",
      {
        "data-type": "wysiwyg-toggle-summary",
        class: "wysiwyg-toggle-summary",
        contenteditable: "true",
      },
      ["span", { class: "wysiwyg-toggle-icon", contenteditable: "false", "aria-hidden": "true" }],
      // The content hole (0) MUST be the only child of its parent element, so it
      // gets its own wrapper span — it cannot sit as a sibling of the icon span
      // above (ProseMirror throws "Content hole must be the only child…").
      ["span", { class: "wysiwyg-toggle-summary-text" }, 0],
    ];
  },
};

// `content` = the toggle body wrapper (schema §1).
const content: NodeSpec = {
  content: "block+",
  defining: true,
  parseDOM: [{ tag: "div[data-type='wysiwyg-toggle-content']" }],
  toDOM() {
    return ["div", { "data-type": "wysiwyg-toggle-content", class: "wysiwyg-toggle-content" }, 0];
  },
};

// --- columns (multi-column row); flattens to sequential blocks in markdown ---

const columns: NodeSpec = {
  group: "block",
  content: "column+",
  defining: true,
  isolating: true,
  attrs: { count: { default: 2, validate: "number" } },
  parseDOM: [
    {
      tag: "div[data-type='wysiwyg-columns']",
      getAttrs: (dom) => ({ count: Number(dom.getAttribute("data-count")) || 2 }),
    },
  ],
  toDOM(node) {
    return [
      "div",
      {
        "data-type": "wysiwyg-columns",
        "data-count": node.attrs.count,
        class: `wysiwyg-columns wysiwyg-columns-${node.attrs.count}`,
      },
      0,
    ];
  },
};

const column: NodeSpec = {
  content: "block+",
  isolating: true,
  defining: true,
  parseDOM: [{ tag: "div[data-type='wysiwyg-column']" }],
  toDOM() {
    return ["div", { "data-type": "wysiwyg-column", class: "wysiwyg-column" }, 0];
  },
};

// --- image (block) via the upload:images capability ---

const image: NodeSpec = {
  group: "block",
  attrs: {
    src: { default: "", validate: "string" },
    alt: { default: "", validate: "string" },
    title: { default: null, validate: "string|null" },
    width: { default: null, validate: "number|null" },
  },
  inline: false,
  atom: true,
  selectable: true,
  draggable: true,
  parseDOM: [
    {
      tag: "img[src]",
      getAttrs: (dom) => ({
        src: dom.getAttribute("src"),
        alt: dom.getAttribute("alt") || "",
        title: dom.getAttribute("title") || null,
        width: px(dom.getAttribute("data-width") || dom.getAttribute("width")),
      }),
    },
  ],
  toDOM(node) {
    const attrs: Record<string, string> = {
      src: node.attrs.src,
      alt: node.attrs.alt || "",
    };
    if (node.attrs.title) attrs.title = node.attrs.title;
    if (node.attrs.width) {
      attrs["data-width"] = node.attrs.width;
      attrs.style = `width: ${node.attrs.width}px`;
    }
    return ["img", attrs];
  },
};

const text: NodeSpec = { group: "inline" };

// ---------------------------------------------------------------------------
// Tables — standard prosemirror-tables model. `tableNodes` yields table /
// table_row / table_cell / table_header with colspan/rowspan/colwidth attrs that
// the SSR renderer reads identically. We override group so they are top-level
// blocks and pass cellContent "block+" per the contract.

const tableSpecs = tableNodes({
  tableGroup: "block",
  cellContent: "block+",
  cellAttributes: {},
});

// ---------------------------------------------------------------------------
// Mark specs

const strong: MarkSpec = {
  parseDOM: [
    { tag: "strong" },
    { tag: "b" },
    { style: "font-weight=bold" },
    {
      style: "font-weight",
      getAttrs: (value) => (/^(bold(er)?|[7-9]\d{2})$/.test(value) && null) || false,
    },
  ],
  toDOM() {
    return ["strong", 0];
  },
};

const em: MarkSpec = {
  parseDOM: [
    { tag: "em" },
    { tag: "i" },
    { style: "font-style=italic" },
    { style: "font-style", getAttrs: (value) => (value === "italic" && null) || false },
  ],
  toDOM() {
    return ["em", 0];
  },
};

const code: MarkSpec = {
  parseDOM: [{ tag: "code" }],
  toDOM() {
    return ["code", { class: "wysiwyg-code" }, 0];
  },
  // inline code does not accumulate further formatting cleanly; keep it strict.
  excludes: "_",
};

const strike: MarkSpec = {
  parseDOM: [{ tag: "s" }, { tag: "del" }, { tag: "strike" }],
  toDOM() {
    return ["s", 0];
  },
};

const underline: MarkSpec = {
  parseDOM: [{ tag: "u" }, { style: "text-decoration=underline" }],
  toDOM() {
    return ["u", 0];
  },
};

// link — rel=noopener, target=_blank. href is sanitized by the host on save;
// the frame additionally drops javascript: schemes on parseDOM.
const link: MarkSpec = {
  attrs: {
    href: { default: "", validate: "string" },
    title: { default: null, validate: "string|null" },
  },
  inclusive: false,
  parseDOM: [
    {
      tag: "a[href]",
      getAttrs: (dom) => ({
        href: dom.getAttribute("href"),
        title: dom.getAttribute("title") || null,
      }),
    },
  ],
  toDOM(node) {
    return [
      "a",
      {
        href: node.attrs.href,
        title: node.attrs.title || undefined,
        rel: "noopener noreferrer",
        target: "_blank",
      },
      0,
    ];
  },
};

function colorStyleVar(prefix: string, slot: unknown): string {
  return `${prefix}-${colorSlot(slot)}`;
}

// textColor — token ref `var(--wysiwyg-fg-<slot>)` (schema §3). Stored slot name.
const textColor: MarkSpec = {
  attrs: { color: { default: "default", validate: "string" } },
  inclusive: false,
  parseDOM: [
    {
      tag: "span[data-wysiwyg-color]",
      getAttrs: (dom) => ({ color: dom.getAttribute("data-wysiwyg-color") || "default" }),
    },
    {
      style: "color",
      getAttrs: (value) => {
        const m = /var\(--wysiwyg-fg-([a-z]+)\)/.exec(String(value || ""));
        return m ? { color: m[1] } : false;
      },
    },
  ],
  toDOM(node) {
    const slot = colorSlot(node.attrs.color);
    return [
      "span",
      {
        "data-wysiwyg-color": slot,
        class: `wysiwyg-fg-${slot}`,
        style: `color: var(${colorStyleVar("--wysiwyg-fg", slot)})`,
      },
      0,
    ];
  },
};

// bgColor — highlight; token ref `var(--wysiwyg-bg-<slot>)`.
const bgColor: MarkSpec = {
  attrs: { color: { default: "default", validate: "string" } },
  inclusive: false,
  parseDOM: [
    {
      tag: "span[data-wysiwyg-bg]",
      getAttrs: (dom) => ({ color: dom.getAttribute("data-wysiwyg-bg") || "default" }),
    },
    {
      style: "background-color",
      getAttrs: (value) => {
        const m = /var\(--wysiwyg-bg-([a-z]+)\)/.exec(String(value || ""));
        return m ? { color: m[1] } : false;
      },
    },
  ],
  toDOM(node) {
    const slot = colorSlot(node.attrs.color);
    return [
      "span",
      {
        "data-wysiwyg-bg": slot,
        class: `wysiwyg-bg-${slot}`,
        // Highlight tints are light in both schemes, so the ink must be dark
        // regardless of the surrounding text color (white-on-yellow in dark
        // mode failed the dogfood contrast check). --wysiwyg-bg-ink lets a
        // host theme override it.
        style: `background-color: var(${colorStyleVar("--wysiwyg-bg", slot)}); color: var(--wysiwyg-bg-ink, #1b1f24)`,
      },
      0,
    ];
  },
};

// ---------------------------------------------------------------------------
export const schema = new Schema({
  nodes: {
    doc,
    paragraph,
    heading,
    blockquote,
    code_block,
    divider,
    bullet_list,
    ordered_list,
    list_item,
    task_list,
    task_item,
    callout,
    toggle,
    toggle_summary,
    content,
    columns,
    column,
    image,
    table: tableSpecs.table,
    table_row: tableSpecs.table_row,
    table_cell: tableSpecs.table_cell,
    table_header: tableSpecs.table_header,
    text,
  },
  marks: {
    strong,
    em,
    code,
    strike,
    underline,
    link,
    textColor,
    bgColor,
  },
});
