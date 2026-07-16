// Tokenizer tests for code-block syntax highlighting (schema richtext-v1).
//
// The tokenizer is a pure, DOM-free function so it runs under `node --test`
// and is the SHARED CONTRACT with the Go SSR highlighter (ssr/highlight.go):
// same lexical rules → same token classes → one CSS theme → editor/read-view
// parity. These tests pin the rules both sides must obey.

// @ts-ignore -- node:test has no declarations without @types/node
import { test as nodeTest } from "node:test";
// @ts-ignore -- node:assert/strict has no declarations without @types/node
import nodeAssert from "node:assert/strict";
// @ts-ignore -- node:fs has no declarations without @types/node
import { readFileSync as nodeReadFileSync } from "node:fs";

type TestFn = (name: string, fn: () => void | Promise<void>) => void;
interface StrictAssert {
  equal(actual: unknown, expected: unknown, message?: string): void;
  deepEqual(actual: unknown, expected: unknown, message?: string): void;
  ok(value: unknown, message?: string): asserts value;
}
const test: TestFn = nodeTest;
const assert: StrictAssert = nodeAssert;

import { tokenize, normalizeLang, supportedLanguages, languageAliases } from "../src/highlight.ts";
import type { Token } from "../src/highlight.ts";

/** Map tokens to [substring, type] pairs for readable assertions. */
function toks(code: string, lang: string): Array<[string, string]> {
  return tokenize(code, lang).map((t: Token) => [code.slice(t.from, t.to), t.type]);
}

// --- structural invariants ------------------------------------------------

test("tokens are sorted, non-overlapping, and within bounds", () => {
  const code = `func f() { return "hi" /* n */ } // done`;
  const ts = tokenize(code, "go");
  let prev = 0;
  for (const t of ts) {
    assert.ok(t.from >= prev, `token starts after previous end: ${JSON.stringify(t)}`);
    assert.ok(t.to > t.from, "token is non-empty");
    assert.ok(t.to <= code.length, "token within bounds");
    prev = t.to;
  }
});

test("an unknown language yields no tokens", () => {
  assert.deepEqual(tokenize(`anything at "all" // 1`, "brainfuck"), []);
});

test("an empty / null language yields no tokens", () => {
  assert.deepEqual(tokenize(`x = 1`, ""), []);
});

// --- comments -------------------------------------------------------------

test("line comment runs to end of line only", () => {
  assert.deepEqual(toks(`x // hi\ny`, "javascript"), [["// hi", "comment"]]);
});

test("hash starts a comment in python but not in javascript", () => {
  assert.deepEqual(toks(`x = 1 # note`, "python"), [
    ["1", "number"],
    ["# note", "comment"],
  ]);
  // In JS, `#` is not a line comment; the digit after it is still a number.
  assert.deepEqual(toks(`x = 1 # note 2`, "javascript"), [
    ["1", "number"],
    ["2", "number"],
  ]);
});

test("block comment spans lines and stops at the terminator", () => {
  assert.deepEqual(toks(`a /* one\ntwo */ b`, "javascript"), [["/* one\ntwo */", "comment"]]);
});

test("unterminated block comment consumes to end of input", () => {
  assert.deepEqual(toks(`/* never closed`, "javascript"), [["/* never closed", "comment"]]);
});

// --- strings --------------------------------------------------------------

test("double and single quoted strings", () => {
  assert.deepEqual(toks(`"a" + 'b'`, "javascript"), [
    [`"a"`, "string"],
    [`'b'`, "string"],
  ]);
});

test("escaped quote does not end the string", () => {
  assert.deepEqual(toks(`"a\\"b"`, "javascript"), [[`"a\\"b"`, "string"]]);
});

test("a single-quote string does not swallow the rest of the line if unterminated", () => {
  // apostrophe in prose-like code shouldn't eat everything to EOL: it stops at newline.
  const t = tokenize(`don't\n1`, "javascript");
  // The number on the next line must still be found.
  assert.ok(t.some((x) => x.type === "number"), "number after a stray quote is still tokenized");
});

// --- numbers --------------------------------------------------------------

test("integers, floats, hex, and separators", () => {
  assert.deepEqual(toks(`0xFF 3.14 42 1_000`, "javascript"), [
    ["0xFF", "number"],
    ["3.14", "number"],
    ["42", "number"],
    ["1_000", "number"],
  ]);
});

// --- keywords vs identifiers vs functions ---------------------------------

test("keywords match whole words only", () => {
  // `constant` must NOT be flagged because it contains `const`.
  assert.deepEqual(toks(`constant`, "javascript"), []);
});

test("a keyword is a keyword", () => {
  assert.deepEqual(toks(`const`, "javascript"), [["const", "keyword"]]);
});

test("identifier followed by ( is a function call", () => {
  assert.deepEqual(toks(`foo(bar)`, "javascript"), [["foo", "function"]]);
});

test("whitespace between name and ( still reads as a call", () => {
  assert.deepEqual(toks(`foo ()`, "javascript"), [["foo", "function"]]);
});

// --- per-language keyword sets --------------------------------------------

test("go: func + call + string + comment", () => {
  assert.deepEqual(toks(`func main() { print("hi") } // x`, "go"), [
    ["func", "keyword"],
    ["main", "function"],
    ["print", "function"],
    [`"hi"`, "string"],
    ["// x", "comment"],
  ]);
});

test("python: def, function name, hash comment", () => {
  assert.deepEqual(toks(`def f(): # c`, "python"), [
    ["def", "keyword"],
    ["f", "function"],
    ["# c", "comment"],
  ]);
});

test("json literals true/false/null are keywords", () => {
  assert.deepEqual(toks(`{"k": true, "n": null, "x": 3}`, "json"), [
    [`"k"`, "string"],
    ["true", "keyword"],
    [`"n"`, "string"],
    ["null", "keyword"],
    [`"x"`, "string"],
    ["3", "number"],
  ]);
});

// --- aliases --------------------------------------------------------------

test("language aliases resolve via normalizeLang", () => {
  // Assert the resolution itself (not tokenized output, which is identical
  // across the C-family and so can't tell js→javascript from a js→go misroute).
  const pairs: Record<string, string> = {
    js: "javascript", jsx: "javascript", mjs: "javascript", node: "javascript",
    ts: "typescript", tsx: "typescript",
    py: "python", py3: "python", rs: "rust",
    sh: "bash", shell: "bash", zsh: "bash", console: "bash",
    golang: "go", yml: "json", htm: "html", md: "markdown",
  };
  for (const [alias, canon] of Object.entries(pairs)) {
    assert.equal(normalizeLang(alias), canon, `${alias} → ${canon}`);
  }
  // Growth guard: adding an alias to ALIASES but not here would ship it
  // unverified (the loop only checks aliases it lists).
  assert.equal(
    Object.keys(pairs).length,
    languageAliases().length,
    "alias count drift: add the new ALIASES entry here with its expected target",
  );
});

// --- shared parity fixture ------------------------------------------------
// The SAME table drives ssr/highlight_test.go. Any row this side fails to
// reproduce means the TS scanner has drifted from the Go one (or vice-versa) —
// i.e. the editor and the read view would highlight that code differently.

interface FixtureCase {
  code: string;
  lang: string;
  tokens: Array<[string, string]>;
}

const FIXTURE: { languages: string[]; cases: FixtureCase[] } = JSON.parse(
  nodeReadFileSync(new URL("../../highlight-cases.json", import.meta.url), "utf8") as string,
);

for (const c of FIXTURE.cases) {
  test(`parity fixture: ${c.lang} ${JSON.stringify(c.code)}`, () => {
    assert.deepEqual(toks(c.code, c.lang), c.tokens);
  });
}

// The fixture's `languages` is the canonical supported set; the Go suite asserts
// hlLangs equals it too, so LANGS == hlLangs transitively (an unguarded edge
// before). Sorted compare against the TS tokenizer's own key set.
test("supported languages match the canonical fixture list", () => {
  assert.deepEqual([...supportedLanguages()].sort(), [...FIXTURE.languages].sort());
});

// --- dropdown ↔ tokenizer parity ------------------------------------------
// The code-block language dropdown (CODE_LANGS in ui.ts) is a hand-maintained
// list separate from the tokenizer's supported set. If they drift, a user can
// pick a language the highlighter can't tokenize (renders plain, silently). We
// read ui.ts as text rather than importing it (it pulls in the whole DOM-heavy
// editor); the regex extracts the array literal and each quoted entry.

test("the CODE_LANGS dropdown exactly matches the tokenizer's supported set", () => {
  const src = nodeReadFileSync(new URL("../src/ui.ts", import.meta.url), "utf8") as string;
  const m = src.match(/CODE_LANGS\s*=\s*\[([^\]]*)\]/);
  assert.ok(m, "could not find CODE_LANGS array literal in ui.ts");
  const dropdown = Array.from(m![1].matchAll(/"([^"]*)"/g))
    .map((x) => x[1])
    .filter((l) => l !== ""); // "" is the intentional "plain text" option

  // Assert the SET equality, not just forward containment. This closes three
  // gaps at once: (1) a dropdown entry the tokenizer can't highlight (forward),
  // (2) a supported language missing from the dropdown (reverse), and (3) a
  // refactor to `[..., ...spread]` that hides entries from the regex — that now
  // FAILS loudly here (the parsed set no longer equals the supported set)
  // instead of silently passing.
  assert.deepEqual(
    dropdown.slice().sort(),
    [...supportedLanguages()].sort(),
    "CODE_LANGS (ui.ts) must list exactly the tokenizer's supported languages — if this fails after a refactor of the array literal, update the regex or the list",
  );
});
