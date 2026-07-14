// Editing commands + input rules for the WYSIWYG editor (schema wysiwyg-v1).
//
// All block-type toggles, list operations, color marks, links, and structural
// inserts (divider/callout/toggle/columns/image/table) live here. Input rules
// give markdown-style shortcuts (# heading, - list, > quote, ``` code, --- hr,
// [] task). List operations reuse prosemirror-schema-list's node-typed commands
// (wrapInList/liftListItem/splitListItem/sinkListItem), which work for both the
// standard list_item and our task_item since findWrapping derives the item type
// from the schema content spec.

import { TextSelection } from "prosemirror-state";
import type { EditorState, Transaction, Selection, Command } from "prosemirror-state";
import type { NodeType, Node as PMNode, Mark } from "prosemirror-model";
import type { EditorView } from "prosemirror-view";

type DispatchFn = (tr: Transaction) => void;
import { toggleMark, setBlockType, wrapIn, chainCommands } from "prosemirror-commands";
import { undo as pmUndo, redo as pmRedo } from "prosemirror-history";
import {
  wrapInList,
  liftListItem,
  sinkListItem,
  splitListItem,
} from "prosemirror-schema-list";
import { InputRule, wrappingInputRule, textblockTypeInputRule } from "prosemirror-inputrules";
import { addRowAfter as pmAddRowAfter, goToNextCell as pmGoToNextCell, isInTable } from "prosemirror-tables";
import {
  schema,
  colorSlot,
  calloutVariant,
} from "./schema.ts";

const { nodes, marks } = schema;

// ---------------------------------------------------------------------------
// Helpers

/** If the (collapsed) selection sits in an empty paragraph, return its span. */
function emptyParagraphRange(state: EditorState) {
  const { $from, empty } = state.selection;
  if (!empty) return null;
  for (let d = $from.depth; d > 0; d--) {
    const node = $from.node(d);
    if (node.type === nodes.paragraph && node.childCount === 0) {
      return { start: $from.before(d), end: $from.after(d) };
    }
  }
  return null;
}

/** Position immediately after the top-level block holding the selection head. */
function afterTopBlock(selection: Selection) {
  return selection.$from.after(Math.max(1, Math.min(selection.$from.depth, 1)));
}

/** Resolve the first editable text position at or after `from`. */
function firstTextPos(doc: PMNode, from: number) {
  let found = -1;
  doc.nodesBetween(from, doc.content.size, (node: PMNode, pos: number) => {
    if (found > -1) return false;
    if (node.isTextblock) {
      found = pos + 1;
      return false;
    }
  });
  return found > -1 ? found : from;
}

/** Find the nearest ancestor of the selection of a given node type. */
function findAncestor(state: EditorState, type: NodeType) {
  const { $from } = state.selection;
  for (let d = $from.depth; d > 0; d--) {
    if ($from.node(d).type === type) return { node: $from.node(d), pos: $from.before(d), depth: d };
  }
  return null;
}

// ---------------------------------------------------------------------------
// Marks

export const toggleBold = () => toggleMark(marks.strong);
export const toggleItalic = () => toggleMark(marks.em);
export const toggleStrike = () => toggleMark(marks.strike);
export const toggleUnderline = () => toggleMark(marks.underline);
export const toggleInlineCode = () => toggleMark(marks.code);

/** Apply textColor to the selection with a named slot (schema §3). Toggles off. */
export function toggleTextColor(slot: string) {
  return toggleMark(marks.textColor, { color: colorSlot(slot) });
}

/** Apply bgColor (highlight) with a named slot. Toggles off. */
export function toggleBgColor(slot: string) {
  return toggleMark(marks.bgColor, { color: colorSlot(slot) });
}

/** Remove any color mark (textColor/bgColor) from the selection. */
export function clearColor() {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const { tr, selection } = state;
    tr.removeMark(selection.from, selection.to, marks.textColor);
    tr.removeMark(selection.from, selection.to, marks.bgColor);
    if (dispatch) dispatch(tr);
    return true;
  };
}

// ---------------------------------------------------------------------------
// Links — sanitize: drop script-bearing schemes (the host re-checks on save).

export function sanitizeHref(href: unknown) {
  const h = String(href || "").trim();
  if (!h) return "";
  if (/^(javascript|vbscript|file):/i.test(h)) return "";
  // data: only for inline images is risky; block for links.
  if (/^data:/i.test(h)) return "";
  return h;
}

/** Normalize user link input for the popover: auto-prefix https:// on a
 * schemeless host (so "example.com/x" is not a broken RELATIVE link), keep
 * anchors/mailto/tel/protocol-relative as-is, and reject dangerous schemes with
 * a message the UI shows inline instead of silently closing. */
export function normalizeLinkInput(raw: string): { href: string; error?: string } {
  const h = String(raw || "").trim();
  if (!h) return { href: "", error: "Enter a URL" };
  if (/^(javascript|vbscript|file|data):/i.test(h)) return { href: "", error: "That link scheme isn't allowed" };
  // Already has a scheme, is an in-page anchor, mailto/tel, or protocol-relative.
  if (/^([a-z][a-z0-9+.-]*:|#|\/\/|mailto:|tel:)/i.test(h)) return { href: sanitizeHref(h) };
  // Looks like a bare domain/path → default to https.
  return { href: sanitizeHref("https://" + h) };
}

export function setLink(attrs: { href?: unknown; title?: string | null } = {}) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const { tr, selection } = state;
    const href = sanitizeHref(attrs.href);
    if (!href) return false;
    if (selection.empty) return false;
    const mark = marks.link.create({ href, title: attrs.title || null });
    tr.removeMark(selection.from, selection.to, marks.link);
    tr.addMark(selection.from, selection.to, mark);
    if (dispatch) dispatch(tr.scrollIntoView());
    return true;
  };
}

export function unsetLink() {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const { tr, selection } = state;
    tr.removeMark(selection.from, selection.to, marks.link);
    if (dispatch) dispatch(tr.scrollIntoView());
    return true;
  };
}

/** The active link mark at the cursor/selection, if any (for toolbar state). */
export function activeLinkMark(state: EditorState) {
  const { selection } = state;
  if (selection.empty) {
    const m = (state.storedMarks || selection.$from.marks()).filter(
      (mk: Mark) => mk.type === marks.link
    )[0];
    return m || null;
  }
  let found: Mark | null = null;
  state.doc.nodesBetween(selection.from, selection.to, (node: PMNode) => {
    if (found) return false;
    if (node.isText) {
      const m = node.marks.filter((mk: Mark) => mk.type === marks.link)[0];
      if (m) found = m;
    }
  });
  return found;
}

// ---------------------------------------------------------------------------
// Block type toggles

export function setHeading(level: number) {
  return setBlockType(nodes.heading, { level });
}
export function setParagraph() {
  return setBlockType(nodes.paragraph);
}
export function setCodeBlock(language = null) {
  return setBlockType(nodes.code_block, { language });
}
export const toggleBlockquote = () => wrapIn(nodes.blockquote);

// ---------------------------------------------------------------------------
// Alignment: set `align` on every paragraph/heading intersecting the selection.

export function setAlign(align: "left" | "center" | "right" | "justify") {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const { from, to } = state.selection;
    let tr = state.tr;
    let changed = false;
    state.doc.nodesBetween(from, to, (node, pos) => {
      if (node.type === nodes.paragraph || node.type === nodes.heading) {
        if (node.attrs.align !== align) {
          tr = tr.setNodeMarkup(pos, undefined, { ...node.attrs, align });
          changed = true;
        }
      }
    });
    if (!changed) return false;
    if (dispatch) dispatch(tr);
    return true;
  };
}

/** The align of the block holding the selection head (for toolbar state). */
export function currentAlign(state: EditorState): string {
  const { $from } = state.selection;
  for (let d = $from.depth; d > 0; d--) {
    const n = $from.node(d);
    if (n.type === nodes.paragraph || n.type === nodes.heading) return n.attrs.align || "left";
  }
  return "left";
}

// ---------------------------------------------------------------------------
// Lists (bullet / ordered / task). wrapInList works for task_list too: the
// wrapping chain (task_list > task_item) is derived from the content spec.

export const toggleBulletList = () => toggleList(nodes.bullet_list, nodes.list_item);
export const toggleOrderedList = () => toggleList(nodes.ordered_list, nodes.list_item);
export const toggleTaskList = () => toggleList(nodes.task_list, nodes.task_item);

function toggleList(listType: NodeType, itemType: NodeType) {
  return (state: EditorState, dispatch?: DispatchFn, view?: EditorView) => {
    const { selection } = state;
    const range = selection.$from.blockRange(selection.$to);
    if (!range) return false;
    let inItem = false;
    for (let d = range.depth; d > 0; d--) {
      if (selection.$from.node(d).type === itemType) {
        inItem = true;
        break;
      }
    }
    if (inItem) return liftListItem(itemType)(state, dispatch, view);
    return wrapInList(listType)(state, dispatch, view);
  };
}

export const splitListItemCmd = () => splitListItem(nodes.list_item);
export const splitTaskItemCmd = () => splitListItem(nodes.task_item);
export const liftListItemCmd = () => liftListItem(nodes.list_item);
export const liftTaskItemCmd = () => liftListItem(nodes.task_item);
export const sinkListItemCmd = () => sinkListItem(nodes.list_item);
export const sinkTaskItemCmd = () => sinkListItem(nodes.task_item);

/** Toggle the checked state of the task_item containing the selection. */
export function toggleTaskItem() {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const ctx = findAncestor(state, nodes.task_item);
    if (!ctx) return false;
    const tr = state.tr.setNodeMarkup(ctx.pos, nodes.task_item, {
      checked: !ctx.node.attrs.checked,
    });
    if (dispatch) dispatch(tr);
    return true;
  };
}

/** Flip a task_item's checked attr by DOCUMENT POSITION (click delegation): the
 * checkbox is a contenteditable=false decoration, so a click there does not
 * move the selection into the item — resolve the node at `pos` and toggle IT. */
export function toggleTaskItemAt(pos: number) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const found = nodeOfTypeAt(state, pos, nodes.task_item);
    if (!found) return false;
    if (dispatch)
      dispatch(state.tr.setNodeMarkup(found.pos, nodes.task_item, { checked: !found.node.attrs.checked }));
    return true;
  };
}

/** Flip a toggle's open attr by DOCUMENT POSITION (click delegation), same
 * rationale as toggleTaskItemAt — the summary click target is non-editable. */
export function toggleOpenAt(pos: number) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const found = nodeOfTypeAt(state, pos, nodes.toggle);
    if (!found) return false;
    if (dispatch)
      dispatch(state.tr.setNodeMarkup(found.pos, nodes.toggle, { open: !found.node.attrs.open }));
    return true;
  };
}

/** Nearest ancestor of `pos` (inclusive) whose type is `type`, with its start pos. */
function nodeOfTypeAt(state: EditorState, pos: number, type: NodeType) {
  const clamped = Math.max(0, Math.min(pos, state.doc.content.size));
  const $pos = state.doc.resolve(clamped);
  for (let d = $pos.depth; d >= 0; d--) {
    if ($pos.node(d).type === type) return { node: $pos.node(d), pos: d === 0 ? 0 : $pos.before(d) };
  }
  return null;
}

/** Set the open/closed state of the toggle containing the selection. */
export function setToggleOpen(open: boolean) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const ctx = findAncestor(state, nodes.toggle);
    if (!ctx) return false;
    const tr = state.tr.setNodeMarkup(ctx.pos, nodes.toggle, { open });
    if (dispatch) dispatch(tr);
    return true;
  };
}

export function setCalloutVariant(variant: string) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const ctx = findAncestor(state, nodes.callout);
    if (!ctx) return false;
    const tr = state.tr.setNodeMarkup(ctx.pos, nodes.callout, {
      variant: calloutVariant(variant),
      icon: ctx.node.attrs.icon,
    });
    if (dispatch) dispatch(tr);
    return true;
  };
}

// ---------------------------------------------------------------------------
// Structural inserts — replace an empty paragraph in place, else append after
// the current top-level block. Selection lands in the first editable cell.

function insertContainer(node: PMNode, state: EditorState, dispatch?: DispatchFn) {
  const tr = state.tr;
  const emp = emptyParagraphRange(state);
  let pos;
  if (emp) {
    tr.replaceWith(emp.start, emp.end, node);
    pos = emp.start;
  } else {
    const after = afterTopBlock(state.selection);
    tr.insert(after, node);
    pos = after;
  }
  const tp = firstTextPos(tr.doc, pos);
  tr.setSelection(TextSelection.near(tr.doc.resolve(tp)));
  if (dispatch) dispatch(tr.scrollIntoView());
  return true;
}

export function insertDivider() {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const divider = nodes.divider.create();
    const para = nodes.paragraph.create();
    const tr = state.tr;
    const emp = emptyParagraphRange(state);
    let pos;
    if (emp) {
      tr.replaceWith(emp.start, emp.end, [divider, para]);
      pos = emp.start;
    } else {
      const after = afterTopBlock(state.selection);
      tr.insert(after, [divider, para]);
      pos = after;
    }
    const tp = firstTextPos(tr.doc, pos + divider.nodeSize);
    tr.setSelection(TextSelection.near(tr.doc.resolve(tp)));
    if (dispatch) dispatch(tr.scrollIntoView());
    return true;
  };
}

export function insertCallout(variant = "info") {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const callout = nodes.callout.create(
      { variant: calloutVariant(variant) },
      nodes.paragraph.create()
    );
    return insertContainer(callout, state, dispatch);
  };
}

export function insertToggle() {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const toggle = nodes.toggle.create(
      { open: true },
      [
        nodes.toggle_summary.create(),
        nodes.content.create(null, nodes.paragraph.create()),
      ]
    );
    return insertContainer(toggle, state, dispatch);
  };
}

export function insertColumns(count = 2) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const n = count === 3 ? 3 : 2;
    const cols = [];
    for (let i = 0; i < n; i++) {
      cols.push(nodes.column.create(null, nodes.paragraph.create()));
    }
    const columns = nodes.columns.create({ count: n }, cols);
    return insertContainer(columns, state, dispatch);
  };
}

/** Insert an image. If `pos` is given, insert there (used by the upload flow);
 * otherwise replace the empty paragraph at the cursor or append after. */
export function insertImage(
  attrs: { src?: unknown; alt?: string; title?: string | null; width?: number | null } = {},
  pos: number | null = null
) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const node = nodes.image.create({
      src: sanitizeSrc(attrs.src),
      alt: attrs.alt || "",
      title: attrs.title || null,
      width: attrs.width || null,
    });
    if (!node.attrs.src) return false;
    const tr = state.tr;
    let at;
    if (pos != null) {
      at = pos;
      tr.insert(at, node);
    } else {
      const emp = emptyParagraphRange(state);
      if (emp) {
        tr.replaceWith(emp.start, emp.end, node);
        at = emp.start;
      } else {
        at = afterTopBlock(state.selection);
        tr.insert(at, node);
      }
    }
    if (dispatch) dispatch(tr.scrollIntoView());
    return true;
  };
}

function sanitizeSrc(src: unknown): string {
  const s = String(src || "").trim();
  if (!s) return "";
  // Block script schemes; allow http(s), data:image/, relative, blob:.
  if (/^(javascript|vbscript|file):/i.test(s)) return "";
  if (/^data:/i.test(s) && !/^data:image\//i.test(s)) return "";
  return s;
}

export function insertTable(rows = 3, cols = 3) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const headerCell = nodes.table_header;
    const cell = nodes.table_cell;
    const rowNodes = [];
    for (let r = 0; r < rows; r++) {
      const cellType = r === 0 ? headerCell : cell;
      const cells = [];
      for (let c = 0; c < cols; c++) {
        cells.push(cellType.create(null, nodes.paragraph.create()));
      }
      rowNodes.push(nodes.table_row.create(null, cells));
    }
    const table = nodes.table.create(null, rowNodes);
    return insertContainer(table, state, dispatch);
  };
}

// Re-export the table command set so the keymap/UI can reach them by name.
export {
  addColumnBefore,
  addColumnAfter,
  deleteColumn,
  addRowBefore,
  addRowAfter,
  deleteRow,
  deleteTable,
  mergeCells,
  splitCell,
  toggleHeaderRow,
  toggleHeaderColumn,
  toggleHeaderCell,
} from "prosemirror-tables";

// ---------------------------------------------------------------------------
// Input rules — markdown-style shortcuts.

export function buildInputRules() {
  return [
    // # .. ###### → heading
    textblockTypeInputRule(/^(#{1,6})\s$/, nodes.heading, (m) => ({
      level: m[1].length,
    })),
    // > → blockquote
    wrappingInputRule(/^\s*>\s$/, nodes.blockquote),
    // ```lang → code_block
    textblockTypeInputRule(/^```(\w*)$/, nodes.code_block, (m) => ({
      language: m[1] || null,
    })),
    // - * + → bullet list
    wrappingInputRule(/^\s*[-*+]\s$/, nodes.bullet_list),
    // 1. → ordered list (start = the number)
    wrappingInputRule(
      /^(\d+)\.\s$/,
      nodes.ordered_list,
      (m) => ({ start: Number(m[1]) }),
      () => true // always join into an adjacent ordered list
    ),
    // [] or [ ] → task list
    wrappingInputRule(/^\s*(\[\]|\[ \])\s$/, nodes.task_list),
    // --- / ___ / *** → divider
    dividerRule(),
  ];
}

function dividerRule() {
  return new InputRule(/^(---|___|\*\*\*)$/, (state, match, start, end) => {
    const $start = state.doc.resolve(start);
    if ($start.parent.type !== nodes.paragraph) return null;
    const pStart = $start.before($start.depth);
    const pEnd = $start.after($start.depth);
    const divider = nodes.divider.create();
    const para = nodes.paragraph.create();
    const tr = state.tr.replaceWith(pStart, pEnd, [divider, para]);
    const tp = firstTextPos(tr.doc, pStart + divider.nodeSize);
    tr.setSelection(TextSelection.near(tr.doc.resolve(tp)));
    return tr;
  });
}

// A no-op command used as a placeholder for incomplete bindings.
export const noop = () => () => false;

/** Tab inside a table: move to the next cell, or — at the last cell — APPEND a
 * row and land in its first cell (Notion/Docs convention). Returns false when
 * not in a table so the outer chain can fall through to list indent. */
export function tabAppendsRow() {
  return (state: EditorState, dispatch?: DispatchFn, view?: EditorView) => {
    if (!isInTable(state)) return false;
    if (pmGoToNextCell(1)(state, dispatch, view)) return true;
    // Last cell: add a row, then step into it.
    if (!dispatch) return true;
    if (!pmAddRowAfter(state, dispatch)) return false;
    // addRowAfter dispatched; move into the new row on the fresh state.
    if (view) pmGoToNextCell(1)(view.state, view.dispatch.bind(view), view);
    return true;
  };
}

export const undoCmd = () => pmUndo;
export const redoCmd = () => pmRedo;

/** Strip ALL marks from the selection (Clear formatting). Leaves block type. */
export function clearFormatting() {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const { from, to, empty } = state.selection;
    if (empty) return false;
    if (dispatch) dispatch(state.tr.removeMark(from, to, null));
    return true;
  };
}

/** Chain a list of commands; first truthy result wins. */
export function chain(...cmds: Array<Command | false | null | undefined>) {
  return chainCommands(...(cmds.filter(Boolean) as Command[]));
}

/** Move the top-level block holding the selection up (-1) or down (+1). The
 * keyboard equivalent of the drag handle (Alt-ArrowUp/Down), so block reorder
 * is reachable without a pointer. */
export function moveBlock(dir: -1 | 1) {
  return (state: EditorState, dispatch?: DispatchFn) => {
    const { $from } = state.selection;
    if ($from.depth < 1) return false;
    const index = $from.index(0);
    const to = index + dir;
    const doc = state.doc;
    if (to < 0 || to >= doc.childCount) return false;
    if (!dispatch) return true;
    const nodes: PMNode[] = [];
    doc.forEach((child) => nodes.push(child));
    const [moved] = nodes.splice(index, 1);
    nodes.splice(to, 0, moved);
    // Keep the caret with the moved block: its new start is the sum of the
    // sizes of the blocks now before it, +1 into its content.
    let start = 0;
    for (let i = 0; i < to; i++) start += nodes[i].nodeSize;
    const tr = state.tr.replaceWith(0, doc.content.size, nodes);
    const caret = Math.min(start + 1, tr.doc.content.size);
    tr.setSelection(TextSelection.near(tr.doc.resolve(caret)));
    dispatch(tr.scrollIntoView());
    return true;
  };
}
