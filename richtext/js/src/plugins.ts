// ProseMirror plugin assembly for the Rich Text editor (schema richtext-v1).
//
// Combines: history, input rules (markdown shortcuts), the full keymap (marks,
// block types, lists, table nav), gap cursor (for code/table boundaries), drop
// cursor, table editing (cell selection/resize/keyboard nav), and the optional
// UI overlay plugins (slash menu / bubble toolbar / drag handles) injected by
// the editor entry.
//
// Editing stays IN-FRAME: every binding operates on the local ProseMirror view.
// No per-keystroke postMessage — only Mod-S emits, and it does so on save, not
// on the latency-sensitive keystroke path.

import { keymap } from "prosemirror-keymap";
import { Plugin as PMPlugin } from "prosemirror-state";
import type { Command, EditorState, Plugin, Transaction } from "prosemirror-state";
import { Decoration, DecorationSet } from "prosemirror-view";
import { history, undo, redo } from "prosemirror-history";
import { inputRules, undoInputRule } from "prosemirror-inputrules";
import { gapCursor } from "prosemirror-gapcursor";
import { dropCursor } from "prosemirror-dropcursor";
import { baseKeymap, chainCommands } from "prosemirror-commands";
import { tableEditing, columnResizing, goToNextCell } from "prosemirror-tables";
import { schema } from "./schema.ts";
import { findReplacePlugin, openFindReplace } from "./findreplace.ts";
import { codeHighlightPlugin } from "./codehighlight.ts";
import * as cmd from "./commands.ts";

const isMac =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || "");

// ---------------------------------------------------------------------------
// Per-block Enter handling: split list/task items, exit containers on empty Enter.

function enterForLists(state: EditorState, dispatch?: (tr: Transaction) => void): boolean {
  const { selection, schema: sch } = state;
  const { $from } = selection;
  if (!selection.empty) return false;
  // Empty list item / task item at the end of a list → lift out (exit the list).
  for (let d = $from.depth; d > 0; d--) {
    const node = $from.node(d);
    if (
      (node.type === sch.nodes.list_item || node.type === sch.nodes.task_item) &&
      node.childCount === 1 &&
      node.firstChild !== null &&
      node.firstChild.type === sch.nodes.paragraph &&
      node.firstChild.content.size === 0
    ) {
      // Only exit when this is the last item; otherwise split.
      const parent = $from.node(d - 1);
      const idx = $from.index(d - 1);
      if (idx === parent.childCount - 1) {
        return cmd.chain(cmd.liftListItemCmd(), cmd.liftTaskItemCmd())(state, dispatch);
      }
    }
  }
  return false;
}

// Split a list/task item on Enter inside a non-empty item.
function splitItemOnEnter(state: EditorState, dispatch?: (tr: Transaction) => void): boolean {
  const { selection, schema: sch } = state;
  const { $from } = selection;
  if (!selection.empty) return false;
  for (let d = $from.depth; d > 0; d--) {
    const node = $from.node(d);
    if (node.type === sch.nodes.task_item) {
      return cmd.splitTaskItemCmd()(state, dispatch);
    }
    if (node.type === sch.nodes.list_item) {
      return cmd.splitListItemCmd()(state, dispatch);
    }
  }
  return false;
}

// ---------------------------------------------------------------------------
// Keymap

function buildKeys(onSave: () => void): Record<string, Command> {
  const keys: Record<string, Command> = {
    // formatting marks
    "Mod-b": cmd.toggleBold(),
    "Mod-i": cmd.toggleItalic(),
    "Mod-u": cmd.toggleUnderline(),
    "Mod-Shift-x": cmd.toggleStrike(),
    "Mod-Shift-c": cmd.toggleInlineCode(),
    "Mod-e": cmd.toggleInlineCode(),
    "Mod-Shift-k": cmd.unsetLink(),

    // save (flushes metrics + emits save; NOT on the keystroke latency path)
    "Mod-s": () => {
      onSave();
      return true;
    },

    // block types
    "Mod-Alt-1": cmd.setHeading(1),
    "Mod-Alt-2": cmd.setHeading(2),
    "Mod-Alt-3": cmd.setHeading(3),
    "Mod-Alt-4": cmd.setHeading(4),
    "Mod-Alt-5": cmd.setHeading(5),
    "Mod-Alt-6": cmd.setHeading(6),
    "Mod-Alt-0": cmd.setParagraph(),
    "Mod-Alt-c": cmd.setCodeBlock(),
    "Mod-Alt-q": cmd.toggleBlockquote(),

    // lists
    "Mod-Shift-7": cmd.toggleOrderedList(),
    "Mod-Shift-8": cmd.toggleBulletList(),
    "Mod-Shift-9": cmd.toggleTaskList(),

    // Tab: in a table move to the next cell (append a row at the last cell),
    // else indent a list/task item.
    Tab: cmd.chain(cmd.tabAppendsRow(), cmd.sinkListItemCmd(), cmd.sinkTaskItemCmd()),
    "Shift-Tab": cmd.chain(cmd.liftListItemCmd(), cmd.liftTaskItemCmd(), goToNextCell(-1)),

    // Alt-Arrow: move the current top-level block up/down (keyboard reorder —
    // the equivalent of the drag handle, which is pointer-only).
    "Alt-ArrowUp": cmd.moveBlock(-1),
    "Alt-ArrowDown": cmd.moveBlock(1),

    // Mod-Enter: toggle the enclosing to-do item's checked state (Notion/Todo
    // convention; the keyboard path for the checkbox decoration).
    "Mod-Enter": cmd.toggleTaskItem(),

    // Table structure (only fire inside a table; fall through otherwise).
    "Mod-Alt-ArrowRight": cmd.addColumnAfter,
    "Mod-Alt-ArrowDown": cmd.addRowAfter,
    "Mod-Alt-Backspace": cmd.chain(cmd.deleteRow),

    // Enter: exit an empty trailing list item, else split the current
    // list/task item. Without this binding, Enter falls through to
    // baseKeymap's splitBlock, which splits the PARAGRAPH inside the item
    // (two <p> in one <li>) instead of creating a new item.
    Enter: chainCommands(enterForLists, splitItemOnEnter),

    // find & replace
    "Mod-f": (state, dispatch, view) => (view ? openFindReplace(view) : false),

    // history
    "Mod-z": undo,
    "Mod-Shift-z": redo,
    Backspace: chainCommands(undoInputRule, cmd.noop()),
    "Mod-Backspace": undoInputRule,
  };
  keys[isMac ? "Mod-Shift-z" : "Mod-y"] = redo;
  return keys;
}

/**
 * Build the full plugin set.
 * @param {{ onSave: () => void, uiPlugins?: any[] }} opts
 */
// Placeholder: when the doc is a single empty paragraph, show ghost prompt text
// via a decoration (the .richtext-placeholder CSS already exists). Purely visual;
// contributes no content and never touches the serialized doc.
function placeholderPlugin(text: string): Plugin {
  return new PMPlugin({
    props: {
      decorations(state) {
        const doc = state.doc;
        const empty =
          doc.childCount === 1 &&
          doc.firstChild != null &&
          doc.firstChild.type.name === "paragraph" &&
          doc.firstChild.content.size === 0;
        if (!empty) return null;
        const deco = Decoration.node(0, doc.firstChild!.nodeSize, {
          class: "richtext-placeholder",
          "data-placeholder": text,
        });
        return DecorationSet.create(doc, [deco]);
      },
    },
  }) as unknown as Plugin;
}

export function buildPlugins({ onSave, uiPlugins = [] }: { onSave: () => void; uiPlugins?: Plugin[] }): Plugin[] {
  return [
    placeholderPlugin("Type \u2018/\u2019 for blocks, or just start writing\u2026"),
    // UI overlays (slash menu, bubble toolbar, drag handles) FIRST: their
    // handleKeyDown must intercept Enter / ArrowUp / ArrowDown while a menu is
    // open, BEFORE the keymaps below would split the block or move the caret.
    // When no menu is open they return false and keys fall through normally.
    ...uiPlugins,
    findReplacePlugin(),
    codeHighlightPlugin(),
    history(),
    inputRules({ rules: cmd.buildInputRules() }),
    keymap(buildKeys(onSave)),
    gapCursor(),
    dropCursor({ color: "var(--color-primary, #2f6feb)", width: 2 }),
    columnResizing(),
    tableEditing(),
    keymap(baseKeymap),
  ];
}

export { isMac };
