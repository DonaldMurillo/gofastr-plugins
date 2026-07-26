package eject

import (
	"bytes"
	"fmt"
	"strings"
)

// unifiedDiff produces a unified-diff rendering of the change from a to b. It
// is a small, dependency-free line diff: an O(n*m) LCS over split lines, then
// hunk grouping with up to three lines of context. Plugin files are tiny, so
// the quadratic cost is irrelevant; the point is to give `gofastr-plugin diff`
// something a person can read without shelling out to an external diff binary
// (which would be a new platform dependency for a CLI that currently has none).
//
// labelA and labelB name the two sides on the --- / +++ lines. They are named
// rather than derived because a bare "(a)"/"(b)" forces the reader to work out
// which side is theirs; "(as ejected)" vs "(your copy)" answers it in place.
// Both empty suppresses the header. The result is empty when a and b are
// identical.
func unifiedDiff(a, b []byte, labelA, labelB string) string {
	la := splitLines(a)
	lb := splitLines(b)
	ops := diffOps(la, lb)
	if !opsHasChange(ops) {
		return ""
	}
	var out strings.Builder
	if labelA != "" || labelB != "" {
		fmt.Fprintf(&out, "--- %s\n+++ %s\n", labelA, labelB)
	}
	for _, hunk := range groupHunks(ops, la, lb, 3) {
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunk.aRange, hunk.bRange)
		out.WriteString(hunk.body)
	}
	return out.String()
}

// op is one step of the LCS walk: equal, delete (in a only), or insert (in b
// only). The diff is the sequence of these over the paired line streams.
type op struct {
	kind byte // '=', '-', '+'
	line string
}

// diffOps computes the edit script turning la into lb via a textbook LCS dynamic
// program. It returns a flat op slice a caller can scan for change regions.
func diffOps(la, lb []string) []op {
	n, m := len(la), len(lb)
	// dp[i][j] = LCS length of la[i:] and lb[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if la[i] == lb[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []op
	i, j := 0, 0
	for i < n && j < m {
		if la[i] == lb[j] {
			out = append(out, op{kind: '=', line: la[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			out = append(out, op{kind: '-', line: la[i]})
			i++
		} else {
			out = append(out, op{kind: '+', line: lb[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, op{kind: '-', line: la[i]})
	}
	for ; j < m; j++ {
		out = append(out, op{kind: '+', line: lb[j]})
	}
	return out
}

func opsHasChange(ops []op) bool {
	for _, o := range ops {
		if o.kind != '=' {
			return true
		}
	}
	return false
}

// hunk is one contiguous diff region with context lines on each side.
type hunk struct {
	aRange, bRange string
	body           string
}

// groupHunks walks the op stream, finds change regions, expands each by `ctx`
// unchanged lines on both sides, merges overlapping expanded regions, and
// formats each as a unified-diff hunk. The @@ line numbers are 1-based per the
// unified-diff convention.
func groupHunks(ops []op, la, lb []string, ctx int) []hunk {
	type region struct{ start, end int } // half-open op-index range of a change + its context
	var regions []region

	changeStart := -1
	for i := range ops {
		if ops[i].kind != '=' {
			if changeStart == -1 {
				// Start at the last unchanged line within ctx, clamped to 0, so
				// the hunk carries up to ctx lines of leading context.
				start := i
				for k := 0; k < ctx && start > 0 && ops[start-1].kind == '='; k++ {
					start--
				}
				changeStart = start
			}
		} else if changeStart != -1 {
			// Look ahead: if another change is within 2*ctx, merge rather than
			// emit two abutting hunks. Otherwise close the region here.
			merged := false
			for k := 1; k <= 2*ctx && i+k < len(ops); k++ {
				if ops[i+k].kind != '=' {
					merged = true
					break
				}
			}
			if !merged {
				end := i
				for k := 0; k < ctx && end < len(ops) && ops[end].kind == '='; k++ {
					end++
				}
				regions = append(regions, region{changeStart, end})
				changeStart = -1
			}
		}
	}
	if changeStart != -1 {
		regions = append(regions, region{changeStart, len(ops)})
	}

	var hunks []hunk
	for _, r := range regions {
		hunks = append(hunks, formatHunk(ops, r.start, r.end))
	}
	return hunks
}

// formatHunk renders one region. It walks ops[start:end], counting consumed
// lines from each side to compute the @@ line numbers, and prefixing each line
// with its unified-diff marker.
func formatHunk(ops []op, start, end int) hunk {
	// aLine/bLine are the 1-based line numbers in a/b at which the hunk starts.
	aLine, bLine := 1, 1
	for i := range start {
		switch ops[i].kind {
		case '=', '-':
			aLine++
		}
		switch ops[i].kind {
		case '=', '+':
			bLine++
		}
	}
	var body strings.Builder
	aCount, bCount := 0, 0
	for i := start; i < end; i++ {
		o := ops[i]
		switch o.kind {
		case '=':
			body.WriteString(" " + o.line)
			aCount++
			bCount++
		case '-':
			body.WriteString("-" + o.line)
			aCount++
		case '+':
			body.WriteString("+" + o.line)
			bCount++
		}
	}
	return hunk{
		aRange: rangeStr(aLine, aCount),
		bRange: rangeStr(bLine, bCount),
		body:   body.String(),
	}
}

// rangeStr renders a hunk side's line range. Unified diff convention: a count
// of 0 is written with the starting line one less (per GNU diff), and a count
// of 1 omits the ",count" entirely.
func rangeStr(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start-1)
	}
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

// splitLines splits bytes into lines, each entry retaining its terminator so
// rejoining is a concatenation and the diff is byte-faithful. A trailing
// empty entry is dropped (it is an artifact of a final newline, not a line).
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	lines := bytes.Split(b, []byte("\n"))
	out := make([]string, 0, len(lines))
	for i, ln := range lines {
		if i == len(lines)-1 && len(ln) == 0 {
			break
		}
		out = append(out, string(ln)+"\n")
	}
	return out
}
