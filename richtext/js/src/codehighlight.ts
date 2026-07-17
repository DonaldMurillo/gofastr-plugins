// Editor-side syntax highlighting: a ProseMirror plugin that paints code_block
// nodes using the shared tokenizer (highlight.ts). It recomputes an inline
// DecorationSet whenever the document changes; each token becomes a
// Decoration.inline carrying an `hl-<type>` class that the frame CSS themes.
//
// The tokenizer works on the block's raw text; because code_block content is
// plain text with no marks or nested nodes (schema: `content: "text*"`), a
// string offset `o` inside a block whose node starts at doc position `pos` maps
// to doc position `pos + 1 + o` (the +1 steps past the node's opening token).
// codeTokenRanges() is factored out and pure (doc → ranges) so the coordinate
// math is unit-tested without a DOM.

import { Plugin, PluginKey } from "prosemirror-state";
import type { EditorState } from "prosemirror-state";
import { Decoration, DecorationSet } from "prosemirror-view";
import type { Node as PMNode } from "prosemirror-model";
import { tokenize, isSupportedLang } from "./highlight.ts";

export interface CodeRange {
  from: number;
  to: number;
  cls: string;
}

/**
 * Walk the doc and return, in document coordinates, one range per highlighted
 * token across every code_block with a supported language. Pure and DOM-free.
 */
export function codeTokenRanges(doc: PMNode): CodeRange[] {
  const ranges: CodeRange[] = [];
  doc.descendants((node: PMNode, pos: number) => {
    if (node.type.name !== "code_block") return true;
    const lang = node.attrs.language as string | null;
    if (isSupportedLang(lang)) {
      const text = node.textContent;
      const base = pos + 1;
      for (const t of tokenize(text, lang)) {
        ranges.push({ from: base + t.from, to: base + t.to, cls: "hl-" + t.type });
      }
    }
    // code_block holds only text; never recurse into it.
    return false;
  });
  return ranges;
}

function buildDecorations(doc: PMNode): DecorationSet {
  const decos = codeTokenRanges(doc).map((r) => Decoration.inline(r.from, r.to, { class: r.cls }));
  return DecorationSet.create(doc, decos);
}

const codeHighlightKey = new PluginKey<DecorationSet>("richtext-code-highlight");

export function codeHighlightPlugin(): Plugin {
  return new Plugin<DecorationSet>({
    key: codeHighlightKey,
    state: {
      init: (_config, state) => buildDecorations(state.doc),
      // Re-tokenize only when the document actually changed (typing inside a
      // block, or a language switch, both produce docChanged); pure selection
      // moves reuse the cached set.
      apply: (tr, old) => (tr.docChanged ? buildDecorations(tr.doc) : old),
    },
    props: {
      decorations(this: Plugin<DecorationSet>, state: EditorState) {
        return this.getState(state);
      },
    },
  });
}
