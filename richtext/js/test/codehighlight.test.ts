// Editor-side highlighting: maps tokenizer output onto ProseMirror document
// coordinates so the decoration plugin can paint code blocks live. These tests
// pin the coordinate math (a token at string offset o inside a code_block at
// node position `pos` lands at doc position pos+1+o) and the node targeting
// (only code_block, only supported languages).

// @ts-ignore -- node:test has no declarations without @types/node
import { test as nodeTest } from "node:test";
// @ts-ignore -- node:assert/strict has no declarations without @types/node
import nodeAssert from "node:assert/strict";

type TestFn = (name: string, fn: () => void | Promise<void>) => void;
interface StrictAssert {
  equal(actual: unknown, expected: unknown, message?: string): void;
  deepEqual(actual: unknown, expected: unknown, message?: string): void;
  ok(value: unknown, message?: string): asserts value;
}
const test: TestFn = nodeTest;
const assert: StrictAssert = nodeAssert;

import { schema } from "../src/schema.ts";
import { codeTokenRanges } from "../src/codehighlight.ts";
import type { Node as PMNode } from "prosemirror-model";

function codeDoc(language: string | null, code: string): PMNode {
  return schema.nodeFromJSON({
    type: "doc",
    content: [
      {
        type: "code_block",
        attrs: { language },
        content: code ? [{ type: "text", text: code }] : [],
      },
    ],
  });
}

/** Ranges as [class, substringOfCode] for readable assertions. */
function spans(language: string | null, code: string): Array<[string, string]> {
  const doc = codeDoc(language, code);
  // The code block opens at pos 0; its text content starts at doc pos 1, i.e.
  // string offset o ↔ doc pos 1+o. Recover the substring to prove the mapping.
  return codeTokenRanges(doc).map((r) => [r.cls, code.slice(r.from - 1, r.to - 1)]);
}

test("code block decorations land on the right characters", () => {
  assert.deepEqual(spans("javascript", `const x = 1`), [
    ["hl-keyword", "const"],
    ["hl-number", "1"],
  ]);
});

test("doc coordinates are pos+1+offset (block not at position 0)", () => {
  // A paragraph before the code block shifts every code position.
  const doc = schema.nodeFromJSON({
    type: "doc",
    content: [
      { type: "paragraph", content: [{ type: "text", text: "hi" }] },
      { type: "code_block", attrs: { language: "javascript" }, content: [{ type: "text", text: "const" }] },
    ],
  });
  const ranges = codeTokenRanges(doc);
  assert.equal(ranges.length, 1);
  // paragraph = <p>(1) + "hi"(2) + </p>(1) = 4 positions; code_block opens at 4,
  // text starts at 5, so `const` is [5, 10).
  assert.deepEqual(ranges[0], { from: 5, to: 10, cls: "hl-keyword" });
});

test("unsupported / null language yields no decorations", () => {
  assert.deepEqual(spans(null, `const x = 1`), []);
  assert.deepEqual(spans("plaintext", `const x = 1`), []);
});

test("non-code blocks are never decorated", () => {
  const doc = schema.nodeFromJSON({
    type: "doc",
    content: [{ type: "paragraph", content: [{ type: "text", text: "const x = 1" }] }],
  });
  assert.deepEqual(codeTokenRanges(doc), []);
});

test("multiple code blocks each get their own ranges", () => {
  const doc = schema.nodeFromJSON({
    type: "doc",
    content: [
      { type: "code_block", attrs: { language: "javascript" }, content: [{ type: "text", text: "let a" }] },
      { type: "code_block", attrs: { language: "go" }, content: [{ type: "text", text: "func f()" }] },
    ],
  });
  const classes = codeTokenRanges(doc).map((r) => r.cls);
  assert.deepEqual(classes, ["hl-keyword", "hl-keyword", "hl-function"]);
});
