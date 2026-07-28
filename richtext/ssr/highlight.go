package ssr

import "strings"

// Server-side syntax highlighting for code blocks — the Go mirror of the
// editor's tokenizer in js/src/highlight.ts. Both implement the SAME small,
// config-driven lexer (five token classes: comment, string, number, keyword,
// function) so the no-JS read view and the live editor emit identical
// `hl-<type>` classes and share one CSS theme. Keeping the two in lockstep is a
// deliberate cost: parity between the editor and the rendered document is a
// project value, and a shared class contract is cheaper than a shared runtime
// across two languages. See js/src/highlight.ts for the canonical rules; the
// parity tests (highlight_test.go ↔ highlight.test.ts) pin them on both sides.
//
// The scanner works on bytes: every delimiter it recognises (`//`, `/*`, quotes,
// digits, ASCII identifier chars) is ASCII, and UTF-8 continuation bytes are all
// >= 0x80, so multibyte runes inside strings/comments simply fall through as
// default text — never mis-split. Offsets are byte offsets into `code`.

// hlTokenType is the canonical set of highlight classes. THIS is the class-name
// contract, mirrored by the `TokenType` union in js/src/highlight.ts. The CSS
// class is `hl-`+value, emitted in exactly two places (ssr/render.go's
// highlightHTML for the read view, js/src/codehighlight.ts for the editor) and
// themed by the `--richtext-hl-<value>` tokens in ssr/style.go + frame/editor.css.
// A distinct type (not a bare string) makes a mistyped class a compile error on
// the Go side, matching the type-safety the TS union already gives.
type hlTokenType string

const (
	hlComment  hlTokenType = "comment"
	hlString   hlTokenType = "string"
	hlNumber   hlTokenType = "number"
	hlKeyword  hlTokenType = "keyword"
	hlFunction hlTokenType = "function"
)

type hlToken struct {
	From int
	To   int
	Type hlTokenType
}

type langDef struct {
	keywords    map[string]bool
	lineComment []string
	blockOpen   string // "" ⇒ no block comment
	blockClose  string
	strings     string // each rune is a string delimiter
}

func kwset(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

const hlCFamily = "true false null undefined void return if else for while do switch case break continue new delete typeof instanceof in of this super class extends import export from as default"

// hlLangs mirrors LANGS in js/src/highlight.ts. Keep the two in sync.
//
// MAINTENANCE — this feature is hand-mirrored across Go and TS; a change is only
// half-done until every site below is touched, or the parity/CSS tests fail:
//   - Add a LANGUAGE: this map + LANGS in js/src/highlight.ts; add aliases to
//     hlAliases + ALIASES; add it to CODE_LANGS in js/src/ui.ts so it appears in
//     the editor's language dropdown (a test asserts every CODE_LANGS entry is
//     supported); add a row to richtext/highlight-cases.json (both test suites
//     then assert it).
//   - Add a TOKEN CLASS: the hlTokenType consts above + the TokenType union in
//     js/src/highlight.ts; a scanner branch in highlightCode + tokenize; a
//     `--richtext-hl-<name>` token AND `.hl-<name>` rule in BOTH ssr/style.go and
//     js/frame/editor.css (TestHighlightCSSTokensParity guards the token
//     defaults, TestHighlightCSSRuleParity the rule bodies). The `hl-` prefix
//     and Decoration/span emit sites are generic — no change.
//   - After editing js/frame/editor.css, run `npm run build` in js/ — the editor
//     ships the GENERATED assets/editor.css, not the source, and the CSS parity
//     tests read the source; a missed rebuild would pass CI but ship stale CSS.
var hlLangs = map[string]langDef{
	"javascript": {
		keywords:    kwset(hlCFamily + " var let const function async await yield try catch finally throw NaN Infinity"),
		lineComment: []string{"//"},
		blockOpen:   "/*", blockClose: "*/",
		strings: "\"'`",
	},
	"typescript": {
		keywords: kwset(hlCFamily + " var let const function async await yield try catch finally throw NaN Infinity" +
			" interface type enum namespace declare readonly public private protected implements abstract keyof infer number string boolean any unknown never"),
		lineComment: []string{"//"},
		blockOpen:   "/*", blockClose: "*/",
		strings: "\"'`",
	},
	"go": {
		keywords:    kwset("func var const type struct interface map chan package import return if else for range switch case default break continue goto fallthrough defer go select nil true false iota make new len cap append copy delete panic recover string int int64 int32 float64 bool byte rune error"),
		lineComment: []string{"//"},
		blockOpen:   "/*", blockClose: "*/",
		strings: "\"`",
	},
	"python": {
		keywords:    kwset("def class return if elif else for while break continue pass import from as with try except finally raise yield lambda global nonlocal assert del in is not and or None True False self async await print len range"),
		lineComment: []string{"#"},
		strings:     "\"'",
	},
	"rust": {
		keywords:    kwset("fn let mut const static struct enum trait impl mod use pub crate self super return if else match for while loop break continue where as dyn ref move unsafe async await true false None Some Ok Err Box Vec String str i32 i64 u32 u64 usize f64 bool char println"),
		lineComment: []string{"//"},
		blockOpen:   "/*", blockClose: "*/",
		strings: "\"",
	},
	"json": {
		keywords: kwset("true false null"),
		strings:  "\"",
	},
	"css": {
		keywords:  kwset("important inherit initial unset none auto"),
		blockOpen: "/*", blockClose: "*/",
		strings: "\"'",
	},
	"sql": {
		keywords:    kwset("SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE DROP ALTER ADD JOIN LEFT RIGHT INNER OUTER ON GROUP BY ORDER HAVING LIMIT OFFSET AS AND OR NOT NULL IS IN LIKE BETWEEN DISTINCT COUNT SUM AVG MIN MAX PRIMARY KEY FOREIGN REFERENCES DEFAULT INDEX UNIQUE"),
		lineComment: []string{"--"},
		blockOpen:   "/*", blockClose: "*/",
		strings: "'",
	},
	"bash": {
		keywords:    kwset("if then else elif fi for while do done case esac in function return break continue local export readonly declare echo cd exit set unset source true false"),
		lineComment: []string{"#"},
		strings:     "\"'",
	},
	"html": {
		keywords:  map[string]bool{},
		blockOpen: "<!--", blockClose: "-->",
		strings: "\"'",
	},
	"markdown": {keywords: map[string]bool{}},
}

// hlAliases mirrors ALIASES in js/src/highlight.ts.
var hlAliases = map[string]string{
	"js": "javascript", "jsx": "javascript", "mjs": "javascript", "node": "javascript",
	"ts": "typescript", "tsx": "typescript",
	"py": "python", "py3": "python",
	"rs": "rust",
	"sh": "bash", "shell": "bash", "zsh": "bash", "console": "bash",
	"golang": "go",
	"yml":    "json",
	"htm":    "html",
	"md":     "markdown",
}

func normalizeLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if a, ok := hlAliases[l]; ok {
		return a
	}
	return l
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isIdentStart(b byte) bool {
	return b == '_' || b == '$' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentPart(b byte) bool { return isIdentStart(b) || isDigit(b) }

func isHexDigit(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// maxHighlightBytes mirrors MAX_HIGHLIGHT_LEN in js/src/highlight.ts: a code
// block larger than this renders WITHOUT highlighting (no spans) to bound the
// generated HTML. Far larger than any real code block, so the byte-vs-UTF16
// unit difference between the two sides never matters in practice.
const maxHighlightBytes = 100_000

// highlightCode tokenizes code for lang, returning non-overlapping, ascending
// byte ranges for the styled classes. Unknown languages return nil.
func highlightCode(code, lang string) []hlToken {
	d, ok := hlLangs[normalizeLang(lang)]
	if !ok {
		return nil
	}
	if len(code) > maxHighlightBytes {
		return nil // oversized block → render plain
	}
	var out []hlToken
	n := len(code)
	i := 0

scan:
	for i < n {
		c := code[i]

		switch c {
		case ' ', '\t', '\n', '\r':
			i++
			continue
		}

		// line comment
		for _, lc := range d.lineComment {
			if lc != "" && strings.HasPrefix(code[i:], lc) {
				j := i + len(lc)
				for j < n && code[j] != '\n' {
					j++
				}
				out = append(out, hlToken{i, j, hlComment})
				i = j
				continue scan
			}
		}

		// block comment
		if d.blockOpen != "" && strings.HasPrefix(code[i:], d.blockOpen) {
			rest := code[i+len(d.blockOpen):]
			end := strings.Index(rest, d.blockClose)
			var to int
			if end < 0 {
				to = n
			} else {
				to = i + len(d.blockOpen) + end + len(d.blockClose)
			}
			out = append(out, hlToken{i, to, hlComment})
			i = to
			continue
		}

		// string
		if d.strings != "" && strings.IndexByte(d.strings, c) >= 0 {
			j := i + 1
			closed := false
			for j < n {
				cj := code[j]
				if cj == '\\' {
					j += 2
					continue
				}
				if cj == '\n' && c != '`' {
					break // template literals may span lines
				}
				if cj == c {
					j++
					closed = true
					break
				}
				j++
			}
			if closed || c == '`' {
				if j > n {
					j = n
				}
				out = append(out, hlToken{i, j, hlString})
				i = j
				continue
			}
			// unterminated single-line quote: treat delimiter as plain text
			i++
			continue
		}

		// number
		if isDigit(c) || (c == '.' && i+1 < n && isDigit(code[i+1])) {
			j := scanNumber(code, i)
			if j > i {
				out = append(out, hlToken{i, j, hlNumber})
				i = j
				continue
			}
		}

		// identifier → keyword or function call
		if isIdentStart(c) {
			j := i + 1
			for j < n && isIdentPart(code[j]) {
				j++
			}
			word := code[i:j]
			if d.keywords[word] {
				out = append(out, hlToken{i, j, hlKeyword})
			} else {
				k := j
				for k < n && (code[k] == ' ' || code[k] == '\t') {
					k++
				}
				if k < n && code[k] == '(' {
					out = append(out, hlToken{i, j, hlFunction})
				}
			}
			i = j
			continue
		}

		i++
	}

	return out
}

// scanNumber consumes a numeric literal starting at i and returns the end
// offset. Mirrors the NUMBER regex in js/src/highlight.ts: hex/bin/octal
// prefixes, decimals with `_` separators, a fractional part, and an exponent.
//
// Parity notes (the regex is subtly strict; scanNumber must match it exactly):
//   - a base prefix (0x/0b/0o) with NO valid digit after it is not a number —
//     `0x` and `0xZ` tokenize as just `0` (regex requires `[…]+`).
//   - a trailing `.` with no digit after is NOT consumed — `5.` → `5`, and
//     `1..2` → `1` then `.2` (regex requires a digit after the optional dot).
func scanNumber(code string, i int) int {
	n := len(code)
	j := i
	if code[j] == '0' && j+1 < n {
		switch code[j+1] {
		case 'x', 'X':
			k := j + 2
			for k < n && (isHexDigit(code[k]) || code[k] == '_') {
				k++
			}
			if k > j+2 {
				return k // at least one hex digit consumed
			}
		case 'b', 'B':
			k := j + 2
			for k < n && (code[k] == '0' || code[k] == '1' || code[k] == '_') {
				k++
			}
			if k > j+2 {
				return k
			}
		case 'o', 'O':
			k := j + 2
			for k < n && ((code[k] >= '0' && code[k] <= '7') || code[k] == '_') {
				k++
			}
			if k > j+2 {
				return k
			}
		}
		// prefix with no digits (e.g. `0x`): fall through so only `0` is the
		// number and the letter is scanned separately, matching the TS regex.
	}
	for j < n && (isDigit(code[j]) || code[j] == '_') {
		j++
	}
	// Only consume a fractional part when a digit actually follows the dot.
	if j+1 < n && code[j] == '.' && isDigit(code[j+1]) {
		j++
		for j < n && (isDigit(code[j]) || code[j] == '_') {
			j++
		}
	}
	if j < n && (code[j] == 'e' || code[j] == 'E') {
		k := j + 1
		if k < n && (code[k] == '+' || code[k] == '-') {
			k++
		}
		if k < n && isDigit(code[k]) {
			j = k
			for j < n && isDigit(code[j]) {
				j++
			}
		}
	}
	return j
}
