package ssr

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// tok renders the tokenizer output as [substring, type] pairs for readable
// assertions — the Go mirror of the TS test helper in js/test/highlight.test.ts.
func tok(code, lang string) [][2]string {
	out := [][2]string{}
	for _, t := range highlightCode(code, lang) {
		out = append(out, [2]string{code[t.From:t.To], string(t.Type)})
	}
	return out
}

func eqTokens(t *testing.T, got [][2]string, want [][2]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("token count: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d: got %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestHighlightGoParity(t *testing.T) {
	eqTokens(t, tok(`func main() { print("hi") } // x`, "go"), [][2]string{
		{"func", "keyword"},
		{"main", "function"},
		{"print", "function"},
		{`"hi"`, "string"},
		{"// x", "comment"},
	})
}

func TestHighlightJavaScriptParity(t *testing.T) {
	eqTokens(t, tok(`const x = 1 // c`, "javascript"), [][2]string{
		{"const", "keyword"},
		{"1", "number"},
		{"// c", "comment"},
	})
}

func TestHighlightPythonHashComment(t *testing.T) {
	eqTokens(t, tok(`def f(): # c`, "python"), [][2]string{
		{"def", "keyword"},
		{"f", "function"},
		{"# c", "comment"},
	})
}

func TestHighlightNumbersAndStrings(t *testing.T) {
	eqTokens(t, tok(`0xFF 3.14 "a\"b" 'c'`, "javascript"), [][2]string{
		{"0xFF", "number"},
		{"3.14", "number"},
		{`"a\"b"`, "string"},
		{`'c'`, "string"},
	})
}

func TestHighlightKeywordWholeWordOnly(t *testing.T) {
	// `constant` contains `const` but must not be flagged.
	if got := tok(`constant`, "javascript"); len(got) != 0 {
		t.Fatalf("expected no tokens, got %v", got)
	}
}

func TestHighlightBlockCommentSpansLines(t *testing.T) {
	eqTokens(t, tok("a /* one\ntwo */ b", "javascript"), [][2]string{
		{"/* one\ntwo */", "comment"},
	})
}

func TestHighlightAliases(t *testing.T) {
	// Assert the alias MAP directly via normalizeLang. A behavioral comparison
	// (tokenize under the alias vs the canonical name) is too weak: sample code
	// tokenizes identically across the C-family, so an alias mis-pointed to the
	// wrong same-family language (e.g. js→go) would pass silently. Checking the
	// resolution itself catches both a dropped alias and a mis-wired one.
	pairs := map[string]string{
		"js": "javascript", "jsx": "javascript", "mjs": "javascript", "node": "javascript",
		"ts": "typescript", "tsx": "typescript",
		"py": "python", "py3": "python", "rs": "rust",
		"sh": "bash", "shell": "bash", "zsh": "bash", "console": "bash",
		"golang": "go", "yml": "json", "htm": "html", "md": "markdown",
	}
	for alias, canon := range pairs {
		if got := normalizeLang(alias); got != canon {
			t.Errorf("normalizeLang(%q) = %q, want %q", alias, got, canon)
		}
	}
	// Growth guard: without this, ADDING an alias to hlAliases but not here would
	// ship it unverified (the loop above only checks the aliases it lists).
	if len(pairs) != len(hlAliases) {
		t.Errorf("alias count drift: this test lists %d pairs but hlAliases has %d — add the new alias here with its expected target", len(pairs), len(hlAliases))
	}
}

// cssCommentRe strips /* … */ blocks so the CSS-parity tests never parse a rule
// or token that only appears inside a comment (a `.hl-x{}` example in a comment
// would otherwise be read as an authoritative rule and, being last, silently
// overwrite the real one).
var cssCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

func readCSSNoComments(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return cssCommentRe.ReplaceAllString(string(b), "")
}

// TestHighlightCSSTokensParity guards the OTHER duplicated half of the feature:
// the `--richtext-hl-*` color defaults are defined identically in the editor's
// stylesheet (js/frame/editor.css) and the read-view stylesheet (ssr/style.go).
// The tokenizer fixture can't catch a color drift, so this does — if the two
// sets of token defaults diverge, the editor and read view would render the same
// class in different colors.
func TestHighlightCSSTokensParity(t *testing.T) {
	// Matches a DEFINITION (`--richtext-hl-x: #hex;`), not a var() reference
	// (which has `,` after the name, not `:`). Comments are stripped first.
	re := regexp.MustCompile(`--richtext-hl-([a-z]+):\s*([^;]+);`)
	extract := func(path string) map[string]string {
		m := map[string]string{}
		for _, mt := range re.FindAllStringSubmatch(readCSSNoComments(t, path), -1) {
			m[mt[1]] = strings.TrimSpace(mt[2])
		}
		return m
	}
	editor := extract("../js/frame/editor.css")
	readView := extract("style.go")
	if len(editor) == 0 {
		t.Fatal("no --richtext-hl-* token definitions found in editor.css (regex or path drift?)")
	}
	if !reflect.DeepEqual(editor, readView) {
		t.Errorf("--richtext-hl-* defaults differ between the two stylesheets:\n editor.css=%v\n style.go  =%v", editor, readView)
	}
}

// TestHighlightCSSRuleParity guards the `.hl-*` RULE bodies (not just the
// `--richtext-hl-*` variable definitions the sibling test covers). The two
// stylesheets use different selector prefixes (`.ProseMirror …` vs
// `[data-fui-comp=…] …`), but the declarations inside each `.hl-<name>{…}` block
// — the `color: var(…, #fallback)` and `font-style: italic` — must match, or a
// comment would render italic in one surface and upright in the other.
func TestHighlightCSSRuleParity(t *testing.T) {
	// Capture the class name and the declaration block of each `.hl-<name>{…}`.
	// Comments are stripped first, so a `.hl-x{}` inside a comment can't be read
	// as a real rule (and last-match-wins can't overwrite the true one).
	re := regexp.MustCompile(`\.hl-([a-z]+)\s*\{([^}]*)\}`)
	extract := func(path string) map[string]string {
		m := map[string]string{}
		for _, mt := range re.FindAllStringSubmatch(readCSSNoComments(t, path), -1) {
			// Normalize each declaration to whitespace-free, then sort — so
			// formatting differences (incl. spacing around `,`/`:`) never
			// false-fail, only real declaration-set differences do.
			decls := []string{}
			for _, d := range strings.Split(mt[2], ";") {
				if d = strings.Join(strings.Fields(d), ""); d != "" {
					decls = append(decls, d)
				}
			}
			sort.Strings(decls)
			m[mt[1]] = strings.Join(decls, ";")
		}
		return m
	}
	editor := extract("../js/frame/editor.css")
	readView := extract("style.go")
	if len(editor) == 0 {
		t.Fatal("no .hl-* rules found in editor.css (regex or path drift?)")
	}
	if !reflect.DeepEqual(editor, readView) {
		t.Errorf(".hl-* rule declarations differ between the two stylesheets:\n editor.css=%v\n style.go  =%v", editor, readView)
	}
}

// TestHighlightParityFixture asserts the Go tokenizer reproduces every row of
// the shared case table (../highlight-cases.json) — the same table the TS suite
// (js/test/highlight.test.ts) checks. A divergence here means the editor and
// the read view would highlight that code differently.
func TestHighlightParityFixture(t *testing.T) {
	raw, err := os.ReadFile("../highlight-cases.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		Cases []struct {
			Code   string      `json:"code"`
			Lang   string      `json:"lang"`
			Tokens [][2]string `json:"tokens"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fixture.Cases {
		got := tok(c.Code, c.Lang)
		want := c.Tokens
		if len(want) == 0 {
			want = [][2]string{} // normalize JSON null/[] for comparison
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s %q: got %v, want %v", c.Lang, c.Code, got, want)
		}
	}
}

// TestHighlightLanguageSetParity pins the supported-language SET across all the
// places it lives. The fixture's `languages` list is canonical: this asserts the
// Go tokenizer supports exactly those (the TS side asserts the same list against
// LANGS, so hlLangs == LANGS transitively — an unguarded edge before), and that
// every declared language has at least one behavioral case row.
func TestHighlightLanguageSetParity(t *testing.T) {
	raw, err := os.ReadFile("../highlight-cases.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		Languages []string `json:"languages"`
		Cases     []struct {
			Lang string `json:"lang"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fixture.Languages) == 0 {
		t.Fatal("fixture declares no languages")
	}

	// hlLangs keys must equal the canonical list.
	got := make([]string, 0, len(hlLangs))
	for k := range hlLangs {
		got = append(got, k)
	}
	want := append([]string(nil), fixture.Languages...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hlLangs keys != fixture.languages:\n hlLangs =%v\n fixture =%v", got, want)
	}

	// Every declared language needs at least one case row (so its Go↔TS parity
	// is actually exercised, not just its presence).
	covered := map[string]bool{}
	for _, c := range fixture.Cases {
		covered[c.Lang] = true
	}
	for _, lang := range fixture.Languages {
		if !covered[lang] {
			t.Errorf("language %q is declared but has no case row — its parity is untested", lang)
		}
	}
}

func TestHighlightUnknownLangNil(t *testing.T) {
	if got := highlightCode(`anything "here" // 1`, "brainfuck"); got != nil {
		t.Fatalf("unknown lang should yield nil, got %v", got)
	}
	if got := highlightCode(`x`, ""); got != nil {
		t.Fatalf("empty lang should yield nil, got %v", got)
	}
}

// --- render integration: spans + escaping --------------------------------

func TestRenderCodeBlockHighlighted(t *testing.T) {
	d := doc(node("code_block", map[string]any{"language": "go"}, text(`func f() { return "x" }`)))
	out := Render(d)
	mustContain(t, out,
		`<pre><code class="language-go">`,
		`<span class="hl-keyword">func</span>`,
		`<span class="hl-function">f</span>`,
		`<span class="hl-string">&quot;x&quot;</span>`,
	)
}

func TestRenderCodeBlockHighlightEscapesDefaultText(t *testing.T) {
	// A token PRESENT (`return`) with HTML metacharacters in the run AFTER it,
	// so the interleaving default-run escaping is exercised — not the zero-token
	// fallback. `a`/`b`/`c`/`d` are bare identifiers (no `(`), so the only token
	// is the keyword and everything else is a default run that must be escaped.
	d := doc(node("code_block", map[string]any{"language": "go"}, text(`return a < b && c > d`)))
	out := Render(d)
	mustContain(t, out, `<span class="hl-keyword">return</span>`)
	// The ` a < b && c > d` gap must be entity-escaped, never raw.
	if strings.Contains(string(out), "a < b") || strings.Contains(string(out), "c > d") {
		t.Errorf("default run between/after tokens not escaped:\n%s", out)
	}
	mustContain(t, out, "&lt;", "&amp;", "&gt;")
}

func TestRenderCodeBlockUnknownLangPlain(t *testing.T) {
	// Unknown language: no spans, plain escaped text (unchanged legacy behaviour).
	d := doc(node("code_block", map[string]any{"language": "<x>"}, text("a < b & c")))
	out := Render(d)
	mustContain(t, out, `class="language-x"`, "a &lt; b &amp; c")
	mustNotContain(t, out, `<span class="hl-`)
}
