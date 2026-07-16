// Markdown serialize + parse for the full block schema (schema-v1 §4).
//
// Serialization NEVER throws — every node has a handler; unmapped marks/nodes
// degrade via `strict:false` (textColor/bgColor drop to plain text; colors lost
// is documented). Markdown is a lossy projection; the canonical format is the
// block JSON.
//
// Round-trip class (export → re-import identity):
//   ✅ paragraph, heading, blockquote, divider, code_block (fenced), bullet/
//      ordered/task lists, image (block-promoted), table (single-para cells),
//      link/strong/em/code/strike
//   ⚠️ underline (<u>), callout (→ > **[variant]**), toggle (<details>) are
//      HTML-passthrough on export; best-effort on import
//   ❌ columns (flattened), textColor/bgColor (dropped) — fidelity lives in JSON
//
// Parsing uses markdown-it (commonmark + table + html) with a token-normalizing
// pass so standalone images become BLOCK nodes, GFM table cells get paragraph
// wrappers (our cells hold block+), and GFM task lists (- [ ] / - [x]) map to
// task_list/task_item. Unknown/unsupported tokens degrade to text, never throw.

import MarkdownIt from "markdown-it";
import { MarkdownParser, MarkdownSerializer } from "prosemirror-markdown";
import type { MarkdownSerializerState } from "prosemirror-markdown";
import type { Node as PMNode, Mark } from "prosemirror-model";
import { schema } from "./schema.ts";
import { sanitizeHref } from "./commands.ts";

// commonmark + GFM tables; html:true so <u>/<details> passthrough round-trips.
// Schema-level sanitization (ProseMirror) keeps the parsed doc safe; the SSR
// renderer re-sanitizes link/image on read.
const md = MarkdownIt("commonmark", {
  html: true,
  linkify: false,
  typographer: false,
  breaks: false,
}).enable(["table", "strikethrough"]);

// prosemirror-markdown's own link serializer stashes `inAutolink` on the state
// object between the open/close callbacks; the property is not in the public
// typings, so widen the state type where we mirror that behavior.
type AutolinkState = MarkdownSerializerState & { inAutolink?: boolean };

// ---------------------------------------------------------------------------
// Serialization

function backticksFor(node: PMNode, side: number): string {
  let ticks = /`+/g;
  let m: RegExpExecArray | null;
  let len = 0;
  if (node.isText) {
    while ((m = ticks.exec(node.text!))) len = Math.max(len, m[0].length);
  }
  let result = len > 0 && side > 0 ? " `" : "`";
  for (let i = 0; i < len; i++) result += "`";
  if (len > 0 && side < 0) result += " ";
  return result;
}

function isPlainURL(link: Mark, parent: PMNode, index: number): boolean {
  if (link.attrs.title || !/^\w+:/.test(link.attrs.href)) return false;
  const content = parent.child(index);
  if (!content.isText || content.text != link.attrs.href || content.marks[content.marks.length - 1] != link)
    return false;
  return index === parent.childCount - 1 || !link.isInSet(parent.child(index + 1).marks);
}

function escHtml(s: unknown): string {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

const serializer = new MarkdownSerializer(
  {
    paragraph(state, node) {
      state.renderInline(node);
      state.closeBlock(node);
    },
    heading(state, node) {
      state.write(state.repeat("#", node.attrs.level) + " ");
      state.renderInline(node, false);
      state.closeBlock(node);
    },
    text(state, node) {
      state.text(node.text!, !(state as AutolinkState).inAutolink);
    },
    code_block(state, node) {
      // Pick a fence longer than any backtick run inside the code.
      const runs = node.textContent.match(/`{3,}/gm);
      const fence = runs ? runs.sort().slice(-1)[0] + "`" : "```";
      state.write(fence + (node.attrs.language || "") + "\n");
      state.text(node.textContent, false);
      state.write("\n");
      state.write(fence);
      state.closeBlock(node);
    },
    divider(state, node) {
      state.write("---");
      state.closeBlock(node);
    },
    blockquote(state, node) {
      state.wrapBlock("> ", null, node, () => state.renderContent(node));
    },
    bullet_list(state, node) {
      state.renderList(node, "  ", () => "- ");
    },
    ordered_list(state, node) {
      const start = node.attrs.start || 1;
      const maxW = String(start + node.childCount - 1).length;
      const space = state.repeat(" ", maxW + 2);
      state.renderList(node, space, (i) => {
        const nStr = String(start + i);
        return state.repeat(" ", maxW - nStr.length) + nStr + ". ";
      });
    },
    list_item(state, node) {
      state.renderContent(node);
    },
    task_list(state, node) {
      state.renderList(node, "  ", (i) => {
        const item = node.child(i);
        return "- " + (item.attrs.checked ? "[x] " : "[ ] ");
      });
    },
    task_item(state, node) {
      state.renderContent(node);
    },
    callout(state, node) {
      // ⚠️ lossy: render as a blockquote with a variant marker line.
      state.wrapBlock("> ", null, node, () => {
        state.write("**[" + node.attrs.variant + "]**");
        state.ensureNewLine();
        state.renderContent(node);
      });
    },
    toggle(state, node) {
      // ⚠️ lossy: HTML passthrough <details>/<summary>.
      let summaryText = "";
      let body: PMNode | null = null;
      node.forEach((child) => {
        if (child.type.name === "toggle_summary") summaryText = child.textContent;
        else if (child.type.name === "content") body = child;
      });
      state.write("<details" + (node.attrs.open ? " open" : "") + ">");
      state.ensureNewLine();
      state.write("<summary>" + escHtml(summaryText) + "</summary>");
      state.ensureNewLine();
      if (body) state.renderContent(body);
      state.ensureNewLine();
      state.write("</details>");
      state.closeBlock(node);
    },
    toggle_summary(state, node) {
      state.renderInline(node);
      state.closeBlock(node);
    },
    content(state, node) {
      state.renderContent(node);
    },
    // ❌ columns: flatten to sequential blocks (layout lost, content kept).
    columns(state, node) {
      state.renderContent(node);
    },
    column(state, node) {
      state.renderContent(node);
    },
    image(state, node) {
      const alt = state.esc(node.attrs.alt || "");
      const src = (node.attrs.src || "").replace(/[\(\)]/g, "\\$&");
      const title = node.attrs.title
        ? ' "' + node.attrs.title.replace(/"/g, '\\"') + '"'
        : "";
      state.write("![" + alt + "](" + src + title + ")");
      state.closeBlock(node);
    },
    table(state, node) {
      // GFM table; cells rendered as their first paragraph's text (single-para
      // cells round-trip; richer cell content is flattened to text on export).
      const rowTexts: string[] = [];
      let ncols = 0;
      node.forEach((row) => {
        const cells: string[] = [];
        row.forEach((cell) => {
          let txt =
            cell.firstChild && cell.firstChild.type.name === "paragraph"
              ? cell.firstChild.textContent
              : cell.textContent;
          txt = txt.replace(/\|/g, "\\|").replace(/\n/g, " ");
          cells.push(txt);
        });
        if (cells.length > ncols) ncols = cells.length;
        rowTexts.push("| " + cells.join(" | ") + " |");
      });
      if (!rowTexts.length || !ncols) {
        state.closeBlock(node);
        return;
      }
      const sep = "| " + Array(ncols).fill("---").join(" | ") + " |";
      rowTexts.splice(1, 0, sep);
      state.write(rowTexts.join("\n"));
      state.closeBlock(node);
    },
    table_row(state, node) {
      state.renderContent(node);
    },
    table_cell(state, node) {
      state.renderContent(node);
    },
    table_header(state, node) {
      state.renderContent(node);
    },
  },
  {
    strong: { open: "**", close: "**", mixable: true, expelEnclosingWhitespace: true },
    em: { open: "*", close: "*", mixable: true, expelEnclosingWhitespace: true },
    strike: { open: "~~", close: "~~", mixable: true, expelEnclosingWhitespace: true },
    underline: { open: "<u>", close: "</u>", mixable: true },
    code: {
      open(_s, _m, parent, index) {
        return backticksFor(parent.child(index), -1);
      },
      close(_s, _m, parent, index) {
        return backticksFor(parent.child(index - 1), 1);
      },
      escape: false,
    },
    link: {
      open(state, mark, parent, index) {
        (state as AutolinkState).inAutolink = isPlainURL(mark, parent, index);
        return (state as AutolinkState).inAutolink ? "<" : "[";
      },
      close(state, mark) {
        const { inAutolink } = state as AutolinkState;
        (state as AutolinkState).inAutolink = undefined;
        const href = sanitizeHref(mark.attrs.href).replace(/[\(\)"]/g, "\\$&");
        return inAutolink
          ? ">"
          : "](" +
              href +
              (mark.attrs.title ? ` "${mark.attrs.title.replace(/"/g, '\\"')}"` : "") +
              ")";
      },
      mixable: true,
    },
    // textColor / bgColor intentionally OMITTED → dropped on export (color lost).
  },
  { strict: false }
);

export function serializeMarkdown(doc: PMNode): string {
  try {
    return serializer.serialize(doc);
  } catch (err) {
    // The contract: never fail on export. Fall back to plain text content.
    console.warn("[richtext] markdown serialize failed, degrading to text:", err);
    try {
      return doc.textContent || "";
    } catch (e) {
      return "";
    }
  }
}

// ---------------------------------------------------------------------------
// Parsing — token normalization makes the GFM/token model match our schema.

// Structural view of a markdown-it Token. Normalization synthesizes plain
// object tokens (no Token class methods), so the real `Token` class type can't
// be used directly; markdown-it's `Token` is assignable to this shape.
type Tok = {
  type: string;
  tag?: string;
  attrs?: [string, string][] | null;
  content?: string;
  children?: Tok[] | null;
  level: number;
  hidden?: boolean;
  info?: string;
  attrSet?: (name: string, value: string) => void;
};

function tokenAttrs(tok: Tok): Record<string, string> {
  const out: Record<string, string> = {};
  const attrs = tok.attrs || [];
  for (let i = 0; i < attrs.length; i++) {
    const pair = attrs[i];
    out[pair[0]] = pair[1];
  }
  return out;
}

/** Promote a paragraph whose only child is an image to a block-level image. */
function promoteImages(tokens: Tok[]): Tok[] {
  const out: Tok[] = [];
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i];
    const next1 = tokens[i + 1];
    const next2 = tokens[i + 2];
    if (
      t.type === "paragraph_open" &&
      next2 &&
      next2.type === "paragraph_close" &&
      next1 &&
      next1.type === "inline" &&
      next1.children &&
      next1.children.length === 1 &&
      next1.children[0].type === "image"
    ) {
      const img = next1.children[0];
      const a = tokenAttrs(img);
      out.push({
        type: "image",
        attrs: Object.entries(a),
        content: img.content || "",
        level: t.level,
      });
      i += 2;
    } else {
      out.push(t);
    }
  }
  return out;
}

/** Strip thead/tbody wrappers and wrap each cell's inline content in a paragraph. */
function normalizeTables(tokens: Tok[]): Tok[] {
  const out: Tok[] = [];
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i];
    if (
      t.type === "thead_open" ||
      t.type === "thead_close" ||
      t.type === "tbody_open" ||
      t.type === "tbody_close"
    ) {
      continue;
    }
    if (t.type === "th_open" || t.type === "td_open") {
      out.push(t);
      const next = tokens[i + 1];
      if (next && next.type === "inline") {
        out.push({ type: "paragraph_open", level: t.level + 1, hidden: true });
        out.push(next);
        out.push({ type: "paragraph_close", level: t.level + 1, hidden: true });
        i += 1;
      } else {
        out.push({ type: "paragraph_open", level: t.level + 1, hidden: true });
        out.push({ type: "inline", children: [], content: "", level: t.level + 1 });
        out.push({ type: "paragraph_close", level: t.level + 1, hidden: true });
      }
      continue;
    }
    out.push(t);
  }
  return out;
}

const TASK_MARKER = /^\[([ xX])\]\s+/;

/** Convert GFM task bullet lists (- [ ] / - [x]) into task_list/task_item. */
function normalizeTaskLists(tokens: Tok[]): Tok[] {
  // 1. find bullet_list spans
  const spans: [number, number][] = [];
  const stack: number[] = [];
  for (let i = 0; i < tokens.length; i++) {
    if (tokens[i].type === "bullet_list_open") stack.push(i);
    else if (tokens[i].type === "bullet_list_close" && stack.length)
      spans.push([stack.pop()!, i]);
  }
  // 2. decide which are task lists (first item starts with a task marker)
  const taskSpans: [number, number][] = [];
  for (const [open, close] of spans) {
    if (firstItemIsTask(tokens, open, close)) taskSpans.push([open, close]);
  }
  if (!taskSpans.length) return tokens;

  // 3. rewrite in place (mutate token types/attrs/text)
  for (const [open, close] of taskSpans) {
    tokens[open].type = "task_list_open";
    tokens[close].type = "task_list_close";
    for (let i = open + 1; i < close; i++) {
      const t = tokens[i];
      if (t.type === "list_item_open") {
        t.type = "task_item_open";
        const inline = firstInline(tokens, i, close);
        const m = inline ? TASK_MARKER.exec(inline.content || "") : null;
        const checked = !!(m && (m[1] === "x" || m[1] === "X"));
        setTokenAttr(t, "checked", checked ? "true" : "false");
        if (m && inline) {
          inline.content = inline.content!.slice(m[0].length);
          if (inline.children && inline.children[0] && inline.children[0].type === "text") {
            inline.children[0].content = inline.children[0].content!.slice(m[0].length);
          }
        }
      } else if (t.type === "list_item_close") {
        t.type = "task_item_close";
      }
    }
  }
  return tokens;
}

function firstItemIsTask(tokens: Tok[], open: number, close: number): boolean {
  for (let i = open + 1; i < close; i++) {
    if (tokens[i].type === "list_item_open") {
      const inline = firstInline(tokens, i, close);
      return !!(inline && TASK_MARKER.test(inline.content || ""));
    }
  }
  return false;
}

function firstInline(tokens: Tok[], from: number, to: number): Tok | null {
  for (let i = from + 1; i < to; i++) {
    if (tokens[i].type === "inline") return tokens[i];
  }
  return null;
}

function setTokenAttr(tok: Tok, name: string, value: string): void {
  if (tok.attrSet) {
    tok.attrSet(name, value);
    return;
  }
  if (!tok.attrs) tok.attrs = [];
  tok.attrs.push([name, value]);
}

function normalizeTokens(tokens: Tok[]): Tok[] {
  let out = promoteImages(tokens);
  out = normalizeTables(out);
  out = normalizeTaskLists(out);
  return out;
}

// A tokenizer wrapper that normalizes markdown-it's token stream before the
// ProseMirror parser sees it. MarkdownParser only ever calls `.parse()`, but
// its constructor demands the full MarkdownIt interface — hence the cast.
const normalizedTokenizer = {
  parse(text: string, env?: object) {
    return normalizeTokens(md.parse(text, env || {}));
  },
} as unknown as MarkdownIt;

const parser = new MarkdownParser(schema, normalizedTokenizer, {
  paragraph: { block: "paragraph" },
  heading: { block: "heading", getAttrs: (tok) => ({ level: Number(tok.tag.slice(1)) }) },
  blockquote: { block: "blockquote" },
  bullet_list: { block: "bullet_list" },
  ordered_list: {
    block: "ordered_list",
    getAttrs: (tok) => ({ start: Number(tokenAttrs(tok).start) || 1 }),
  },
  list_item: { block: "list_item" },
  task_list: { block: "task_list" },
  task_item: { block: "task_item", getAttrs: (tok) => ({ checked: tokenAttrs(tok).checked === "true" }) },
  code_block: { block: "code_block", noCloseToken: true },
  fence: {
    block: "code_block",
    noCloseToken: true,
    getAttrs: (tok) => ({ language: (tok.info && tok.info.trim()) || null }),
  },
  hr: { node: "divider" },
  image: {
    node: "image",
    getAttrs: (tok) => {
      const a = tokenAttrs(tok);
      return {
        src: a.src || "",
        alt: tok.content || "",
        title: a.title || null,
        width: null,
      };
    },
  },
  table: { block: "table" },
  tr: { block: "table_row" },
  th: { block: "table_header" },
  td: { block: "table_cell" },
  strong: { mark: "strong" },
  em: { mark: "em" },
  s: { mark: "strike" },
  code_inline: { mark: "code", noCloseToken: true },
  link: {
    mark: "link",
    getAttrs: (tok) => {
      const a = tokenAttrs(tok);
      return { href: sanitizeHref(a.href) || "", title: a.title || null };
    },
  },
  // underline passthrough → underline mark
  u_open: { mark: "underline" },
  u_close: { mark: "underline" },
  // HTML we can't map: ignore the tags, keep text content
  html_block: { ignore: true, noCloseToken: true },
  html_inline: { ignore: true, noCloseToken: true },
});

export function parseMarkdown(text: unknown): PMNode | null {
  try {
    const doc = parser.parse(String(text || ""));
    return doc || null;
  } catch (err) {
    console.warn("[richtext] markdown parse failed:", err);
    return null;
  }
}

export { md as markdownTokenizer };
