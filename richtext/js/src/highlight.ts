// Syntax highlighting: a pure, DOM-free, dependency-free tokenizer.
//
// This is deliberately a small CONFIG-DRIVEN lexer, not a per-language grammar.
// A single generic scanner recognises the five highest-value lexical classes —
// comments, strings, numbers, keywords, and function calls — and each supported
// language contributes only a tiny config (keyword set + comment/string
// delimiters). That keeps it (a) tiny enough to bundle into the sandboxed iframe
// with no CDN, and (b) trivial to MIRROR in Go for the SSR read-view: the Go
// side (ssr/highlight.go) reimplements these exact rules so both surfaces emit
// the same token classes and share one CSS theme. Perfect per-language accuracy
// is a non-goal; consistent, useful, parity-preserving highlighting is the goal.
//
// The scanner returns non-overlapping ranges over the input, sorted ascending;
// any character not covered by a token renders as default (unstyled) text.
//
// MAINTENANCE: to add a language or a token class, follow the checklist at the
// top of `hlLangs` in ssr/highlight.go — a change here is only half-done until
// the Go mirror, the CODE_LANGS dropdown, the shared fixture, and the CSS tokens
// are updated too. The parity tests enforce this on both sides.

export type TokenType = "comment" | "string" | "number" | "keyword" | "function";

export interface Token {
  from: number;
  to: number;
  type: TokenType;
}

interface LangDef {
  keywords: Set<string>;
  lineComment: string[];
  blockComment: [string, string] | null;
  strings: string[];
}

function def(
  keywords: string,
  opts: { line?: string[]; block?: [string, string] | null; strings?: string[] } = {},
): LangDef {
  return {
    keywords: new Set(keywords.split(/\s+/).filter(Boolean)),
    lineComment: opts.line ?? ["//"],
    blockComment: opts.block === undefined ? ["/*", "*/"] : opts.block,
    strings: opts.strings ?? ['"', "'"],
  };
}

// Keyword lists are intentionally practical, not exhaustive: common control
// flow, declarations, and the literal keywords (true/false/null/…) that read as
// keywords in editors. Literals live here rather than a separate class so both
// the TS and Go scanners stay identical.
const cFamily = "true false null undefined void return if else for while do switch case break continue new delete typeof instanceof in of this super class extends import export from as default";

const LANGS: Record<string, LangDef> = {
  javascript: def(
    cFamily + " var let const function async await yield try catch finally throw NaN Infinity",
    { strings: ['"', "'", "`"] },
  ),
  typescript: def(
    cFamily +
      " var let const function async await yield try catch finally throw NaN Infinity interface type enum namespace declare readonly public private protected implements abstract keyof infer number string boolean any unknown never",
    { strings: ['"', "'", "`"] },
  ),
  go: def(
    "func var const type struct interface map chan package import return if else for range switch case default break continue goto fallthrough defer go select nil true false iota make new len cap append copy delete panic recover string int int64 int32 float64 bool byte rune error",
    { strings: ['"', "`"] },
  ),
  python: def(
    "def class return if elif else for while break continue pass import from as with try except finally raise yield lambda global nonlocal assert del in is not and or None True False self async await print len range",
    { line: ["#"], block: null, strings: ['"', "'"] },
  ),
  rust: def(
    "fn let mut const static struct enum trait impl mod use pub crate self super return if else match for while loop break continue where as dyn ref move unsafe async await true false None Some Ok Err Box Vec String str i32 i64 u32 u64 usize f64 bool char println",
    { strings: ['"'] },
  ),
  json: def("true false null", { line: [], block: null, strings: ['"'] }),
  css: def(
    "important inherit initial unset none auto",
    { line: [], block: ["/*", "*/"], strings: ['"', "'"] },
  ),
  sql: def(
    "SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE DROP ALTER ADD JOIN LEFT RIGHT INNER OUTER ON GROUP BY ORDER HAVING LIMIT OFFSET AS AND OR NOT NULL IS IN LIKE BETWEEN DISTINCT COUNT SUM AVG MIN MAX PRIMARY KEY FOREIGN REFERENCES DEFAULT INDEX UNIQUE",
    { line: ["--"], block: ["/*", "*/"], strings: ["'"] },
  ),
  bash: def(
    "if then else elif fi for while do done case esac in function return break continue local export readonly declare echo cd exit set unset source true false",
    { line: ["#"], block: null, strings: ['"', "'"] },
  ),
  html: def("", { line: [], block: ["<!--", "-->"], strings: ['"', "'"] }),
  markdown: def("", { line: [], block: null, strings: [] }),
};

const ALIASES: Record<string, string> = {
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  node: "javascript",
  ts: "typescript",
  tsx: "typescript",
  py: "python",
  py3: "python",
  rs: "rust",
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  console: "bash",
  golang: "go",
  yml: "json", // close enough for the common key: "value" shape
  htm: "html",
  md: "markdown",
};

export function normalizeLang(lang: string | null | undefined): string {
  const l = (lang || "").trim().toLowerCase();
  return ALIASES[l] || l;
}

/** Is `lang` one we can highlight? Used by callers to skip work early. */
export function isSupportedLang(lang: string | null | undefined): boolean {
  const l = normalizeLang(lang);
  return l !== "" && Object.prototype.hasOwnProperty.call(LANGS, l);
}

/**
 * The canonical set of highlightable language keys (not aliases). Mirrors
 * hlLangs in ssr/highlight.go; the shared fixture's `languages` list pins the
 * two together, and the editor's CODE_LANGS dropdown is asserted against this.
 */
export function supportedLanguages(): string[] {
  return Object.keys(LANGS);
}

/** The alias keys (js, ts, py, …). Mirrors hlAliases in ssr/highlight.go. */
export function languageAliases(): string[] {
  return Object.keys(ALIASES);
}

// Above this size a code block renders WITHOUT highlighting. The tokenizer is
// linear, but the editor rebuilds the whole DecorationSet on every keystroke and
// the SSR path emits one span per token, so a pathologically large single block
// (e.g. a multi-MB paste) would jank the editor / bloat the HTML. Standard
// editor behaviour (VS Code, CodeMirror cap similarly). Mirrored by
// maxHighlightBytes in ssr/highlight.go; the threshold is far larger than any
// real code block, so the byte-vs-UTF16 unit difference never matters in
// practice.
const MAX_HIGHLIGHT_LEN = 100_000;

const NUMBER = /0[xX][0-9a-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|(?:\d[\d_]*)?\.?\d[\d_]*(?:[eE][+-]?\d+)?/y;
const IDENT = /[A-Za-z_$][\w$]*/y;

function isIdentStart(ch: string): boolean {
  return /[A-Za-z_$]/.test(ch);
}

/**
 * Tokenize `code` for `lang`. Returns non-overlapping, ascending ranges for the
 * styled classes; anything not covered is default text. Unknown languages
 * return an empty array (caller renders plain).
 */
export function tokenize(code: string, lang: string | null | undefined): Token[] {
  const d = LANGS[normalizeLang(lang)];
  if (!d) return [];
  if (code.length > MAX_HIGHLIGHT_LEN) return []; // oversized block → render plain
  const out: Token[] = [];
  const n = code.length;
  let i = 0;

  scan: while (i < n) {
    const c = code[i];

    // whitespace: skip fast
    if (c === " " || c === "\t" || c === "\n" || c === "\r") {
      i++;
      continue;
    }

    // line comment
    for (const lc of d.lineComment) {
      if (lc && code.startsWith(lc, i)) {
        let j = i + lc.length;
        while (j < n && code[j] !== "\n") j++;
        out.push({ from: i, to: j, type: "comment" });
        i = j;
        continue scan;
      }
    }

    // block comment
    if (d.blockComment && code.startsWith(d.blockComment[0], i)) {
      const [open, close] = d.blockComment;
      const end = code.indexOf(close, i + open.length);
      const to = end < 0 ? n : end + close.length;
      out.push({ from: i, to, type: "comment" });
      i = to;
      continue;
    }

    // string: stops at matching delimiter, respects backslash escapes, and
    // never runs past a newline (an unterminated quote shouldn't eat the file).
    if (d.strings.includes(c)) {
      let j = i + 1;
      let closed = false;
      while (j < n) {
        const cj = code[j];
        if (cj === "\\") {
          j += 2;
          continue;
        }
        if (cj === "\n" && c !== "`") break; // template literals may span lines
        if (cj === c) {
          j++;
          closed = true;
          break;
        }
        j++;
      }
      if (closed || c === "`") {
        // A trailing `\` at EOF can push j past the end (j += 2); clamp so a
        // token never reports `to > code.length` — the invariant the callers
        // (and codeTokenRanges' doc-position math) rely on. Mirrors the Go
        // guard in ssr/highlight.go.
        const end = j > n ? n : j;
        out.push({ from: i, to: end, type: "string" });
        i = end;
        continue;
      }
      // Unterminated single-line quote: treat the delimiter as plain and move
      // on, so downstream tokens (numbers, keywords) still get found.
      i++;
      continue;
    }

    // number
    if ((c >= "0" && c <= "9") || (c === "." && code[i + 1] >= "0" && code[i + 1] <= "9")) {
      NUMBER.lastIndex = i;
      const m = NUMBER.exec(code);
      if (m && m.index === i && m[0].length > 0) {
        out.push({ from: i, to: i + m[0].length, type: "number" });
        i += m[0].length;
        continue;
      }
    }

    // identifier → keyword or function call
    if (isIdentStart(c)) {
      IDENT.lastIndex = i;
      const m = IDENT.exec(code);
      const word = m ? m[0] : c;
      const j = i + word.length;
      if (d.keywords.has(word)) {
        out.push({ from: i, to: j, type: "keyword" });
      } else {
        let k = j;
        while (k < n && (code[k] === " " || code[k] === "\t")) k++;
        if (code[k] === "(") out.push({ from: i, to: j, type: "function" });
      }
      i = j;
      continue;
    }

    i++;
  }

  return out;
}
