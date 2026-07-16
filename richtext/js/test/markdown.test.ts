// Markdown round-trip tests for the full block set (schema-v1 §4).
// Runs under `node --test test/`. Covers the LOSSLESS subset (identity on
// export → re-import) and asserts the lossy/HTML-passthrough blocks degrade
// gracefully (never throw, never drop plain text).

// @types/node is not installed in this browser-lib package (DOM-only tsconfig),
// so the two Node built-ins used here are imported untyped and immediately
// pinned to minimal local types covering exactly what this file calls. All of
// this is erasable syntax; Node's type stripping runs the file as-is.
// @ts-ignore -- node:test has no declarations without @types/node
import { test as nodeTest } from "node:test";
// @ts-ignore -- node:assert/strict has no declarations without @types/node
import nodeAssert from "node:assert/strict";

type TestFn = (name: string, fn: () => void | Promise<void>) => void;
interface StrictAssert {
  equal(actual: unknown, expected: unknown, message?: string): void;
  ok(value: unknown, message?: string): asserts value;
  match(value: string, regExp: RegExp, message?: string): void;
}
const test: TestFn = nodeTest;
const assert: StrictAssert = nodeAssert;

import { serializeMarkdown, parseMarkdown } from "../src/markdown.ts";
import { schema } from "../src/schema.ts";

/** serialize a ProseMirror doc JSON straight to markdown. */
function ser(json: unknown): string {
  return serializeMarkdown(schema.nodeFromJSON(json));
}

/** round-trip: markdown → doc → markdown; returns the re-serialized string. */
function rt(md: string): string {
  const doc = parseMarkdown(md);
  assert.ok(doc, `parseMarkdown returned null for: ${md}`);
  return serializeMarkdown(doc).trim();
}

// ---------------------------------------------------------------------------
// Lossless subset: export → re-import is identity.

test("paragraph + marks round-trip", () => {
  const md = "bold *italic* `code` ~~strike~~";
  assert.equal(rt(md), md);
});

test("headings 1–6 round-trip", () => {
  for (const lvl of [1, 2, 3, 4, 5, 6]) {
    const md = "#".repeat(lvl) + " Heading";
    assert.equal(rt(md), md, `heading ${lvl}`);
  }
});

test("bullet + ordered lists round-trip", () => {
  assert.equal(rt("- a\n\n- b"), "- a\n\n- b");
  assert.equal(rt("1. one\n\n2. two"), "1. one\n\n2. two");
  assert.equal(rt("3. third"), "3. third");
});

test("GFM task list round-trips to task_list/task_item", () => {
  const md = "- [x] done\n\n- [ ] todo";
  assert.equal(rt(md), md);
  // the parsed doc must carry task_list/task_item + checked attrs
  const doc = parseMarkdown(md)!;
  assert.equal(doc.firstChild!.type.name, "task_list");
  assert.equal(doc.firstChild!.firstChild!.type.name, "task_item");
  assert.equal(doc.firstChild!.firstChild!.attrs.checked, true);
  assert.equal(doc.firstChild!.lastChild!.attrs.checked, false);
});

test("blockquote round-trips", () => {
  assert.equal(rt("> quoted"), "> quoted");
});

test("fenced code block round-trips with language", () => {
  const md = "```js\nconst x = 1;\n```";
  assert.equal(rt(md), md);
  const doc = parseMarkdown(md)!;
  assert.equal(doc.firstChild!.type.name, "code_block");
  assert.equal(doc.firstChild!.attrs.language, "js");
});

test("divider round-trips", () => {
  assert.equal(rt("---"), "---");
});

test("link round-trips", () => {
  assert.equal(rt("[here](https://go.dev)"), "[here](https://go.dev)");
  assert.equal(rt('[t](https://x "title")'), '[t](https://x "title")');
});

test("standalone image becomes a BLOCK image and round-trips", () => {
  const md = '![pic](https://x/y.png "t")';
  assert.equal(rt(md), md);
  const doc = parseMarkdown(md)!;
  assert.equal(doc.firstChild!.type.name, "image", "image promoted to block level");
  assert.equal(doc.firstChild!.attrs.src, "https://x/y.png");
  assert.equal(doc.firstChild!.attrs.alt, "pic");
});

test("GFM table round-trips", () => {
  const md = "| H1 | H2 |\n| --- | --- |\n| c1 | c2 |";
  assert.equal(rt(md), md);
  const doc = parseMarkdown(md)!;
  assert.equal(doc.firstChild!.type.name, "table");
  assert.equal(doc.firstChild!.childCount, 2, "header + body row");
  assert.equal(doc.firstChild!.firstChild!.firstChild!.type.name, "table_header");
});

test("a full mixed document round-trips", () => {
  const md = [
    "## Title",
    "",
    "para with **bold**",
    "",
    "- one",
    "",
    "```js",
    "code()",
    "```",
    "",
    "> quote",
    "",
    "[link](https://go.dev)",
  ].join("\n");
  assert.equal(rt(md), md);
});

// ---------------------------------------------------------------------------
// Serialize: every block type has a handler (never throws).

test("serialize never throws on any block type", () => {
  const doc = schema.nodeFromJSON({
    type: "doc",
    content: [
      { type: "paragraph", content: [{ type: "text", text: "p" }] },
      { type: "heading", attrs: { level: 2 }, content: [{ type: "text", text: "h" }] },
      { type: "blockquote", content: [{ type: "paragraph", content: [{ type: "text", text: "q" }] }] },
      { type: "code_block", attrs: { language: "go" }, content: [{ type: "text", text: "x := 1" }] },
      { type: "divider" },
      { type: "bullet_list", content: [{ type: "list_item", content: [{ type: "paragraph", content: [{ type: "text", text: "li" }] }] }] },
      { type: "ordered_list", attrs: { start: 2 }, content: [{ type: "list_item", content: [{ type: "paragraph", content: [{ type: "text", text: "ol" }] }] }] },
      { type: "task_list", content: [{ type: "task_item", attrs: { checked: true }, content: [{ type: "paragraph", content: [{ type: "text", text: "t" }] }] }] },
      { type: "callout", attrs: { variant: "warn" }, content: [{ type: "paragraph", content: [{ type: "text", text: "c" }] }] },
      { type: "toggle", attrs: { open: true }, content: [
        { type: "toggle_summary", content: [{ type: "text", text: "sum" }] },
        { type: "content", content: [{ type: "paragraph", content: [{ type: "text", text: "body" }] }] },
      ] },
      { type: "columns", attrs: { count: 2 }, content: [
        { type: "column", content: [{ type: "paragraph", content: [{ type: "text", text: "L" }] }] },
        { type: "column", content: [{ type: "paragraph", content: [{ type: "text", text: "R" }] }] },
      ] },
      { type: "image", attrs: { src: "https://x/a.png", alt: "a" } },
      { type: "table", content: [{ type: "table_row", content: [
        { type: "table_header", content: [{ type: "paragraph", content: [{ type: "text", text: "H" }] }] },
        { type: "table_cell", content: [{ type: "paragraph", content: [{ type: "text", text: "d" }] }] },
      ] }] },
    ],
  });
  const out = serializeMarkdown(doc);
  assert.ok(typeof out === "string" && out.length > 0);
  // colors drop cleanly (no token leakage, text preserved)
  const colored = schema.nodeFromJSON({
    type: "doc",
    content: [{ type: "paragraph", content: [{ type: "text", text: "blue", marks: [{ type: "textColor", attrs: { color: "blue" } }, { type: "bgColor", attrs: { color: "yellow" } }] }] }],
  });
  assert.equal(serializeMarkdown(colored).trim(), "blue");
});

test("serialize with underline + strike emits HTML/markdown passthrough", () => {
  const doc = schema.nodeFromJSON({
    type: "doc",
    content: [{ type: "paragraph", content: [
      { type: "text", text: "u", marks: [{ type: "underline" }] },
      { type: "text", text: " " },
      { type: "text", text: "s", marks: [{ type: "strike" }] },
    ] }],
  });
  const out = serializeMarkdown(doc).trim();
  assert.match(out, /<u>u<\/u>/);
  assert.match(out, /~~s~~/);
});

// ---------------------------------------------------------------------------
// Lossy degradation: parse degrades, never returns null for safe input.

test("HTML passthrough (underline) degrades to text on import", () => {
  const doc = parseMarkdown("<u>underline</u> here");
  assert.ok(doc);
  // text content preserved even if the mark is dropped
  assert.equal(doc.textContent, "underline here");
});

test("dangerous links never produce a clickable link mark", () => {
  const doc = parseMarkdown("[bad](javascript:alert(1))");
  assert.ok(doc);
  let hasDangerLink = false;
  doc.descendants((node) => {
    if (node.isText && node.marks.some((m) => m.type.name === "link" && /javascript:/i.test(m.attrs.href))) {
      hasDangerLink = true;
    }
  });
  assert.equal(hasDangerLink, false, "no link mark may carry a javascript: href");
});
