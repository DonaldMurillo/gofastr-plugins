package eject

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/internal/registry"
)

// Drift classifies how one ejected file has moved relative to its lock record
// since the last Apply. The five values encode the matrix of "did the local
// copy move" × "did upstream move" plus the absence case, which is what lets
// `gofastr-plugin diff` distinguish "you edited it" from "they shipped a new
// version" from "both" — the three cases that need different reconciliation.
type Drift int

const (
	// DriftNone: local and upstream both unchanged since eject.
	DriftNone Drift = iota
	// DriftLocal: the user edited the file on disk (vendored hash mismatch),
	// upstream did not move. A re-eject without --force would conflict.
	DriftLocal
	// DriftUpstream: upstream changed (the embedded bytes differ from the
	// recorded upstream hash); the user has not edited their copy. A re-eject
	// would update cleanly.
	DriftUpstream
	// DriftBoth: both sides moved. The hardest case — a re-eject conflicts and
	// a force would discard the local edit. The diff output shows both sides.
	DriftBoth
	// DriftMissing: the file is gone from disk. Either the user deleted it or
	// the lock references a path the working tree no longer carries.
	DriftMissing
)

// String renders a Drift for human-facing output. Short and lowercase so the
// `diff` command's per-file line stays scannable.
func (d Drift) String() string {
	switch d {
	case DriftNone:
		return "none"
	case DriftLocal:
		return "local-edit"
	case DriftUpstream:
		return "upstream-changed"
	case DriftBoth:
		return "both"
	case DriftMissing:
		return "missing"
	}
	return "?"
}

// DriftEntry is one file's drift verdict, plus (for text files) the unified
// diff between what is on disk and what a re-eject would now write. The diff is
// empty unless drift is non-none, so a quiet tree produces quiet output.
type DriftEntry struct {
	Rel   string
	Drift Drift
	Diff  string
}

// Compare classifies drift for every file of one ejected plugin. It walks the
// same embedded subtree BuildPlan does (so the comparison reflects what the
// current embedded tree holds, not a stale snapshot), and for each file pairs:
//
//   - the lock's recorded upstream hash vs the current embedded upstream hash
//     (did upstream move?), and
//   - the lock's recorded vendored hash vs the file now on disk (did the user
//     move?).
//
// Files the lock records but the current source no longer carries are reported
// as DriftUpstream too — upstream removing a file is still an upstream change
// a re-eject would propagate.
//
// Options matter here for the same reason they matter in BuildPlan: WithJS and
// WithTests select which files are in scope, so drift is reported against the
// same set of files a re-eject would touch.
func Compare(src fs.FS, p registry.Plugin, o Options, lock *Lock) ([]DriftEntry, error) {
	srcDir, err := srcDirOf(p.ModulePath)
	if err != nil {
		return nil, err
	}

	var lp *LockPlugin
	if lock != nil {
		lp = lock.Plugins[p.Name]
	}
	planDir := path.Join(path.Clean(o.DestDir), srcDir)

	// seen tracks every source-carried rel, so the second pass can spot
	// lock-only entries (upstream removed a file) without re-walking.
	seen := map[string]bool{}

	var out []DriftEntry
	walkErr := fs.WalkDir(src, srcDir, func(sp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(sp, srcDir+"/")
		base := d.Name()
		if !o.WithTests && strings.HasSuffix(base, "_test.go") {
			return nil
		}
		if !o.WithJS && strings.HasPrefix(rel, "js/") {
			return nil
		}

		upstream, err := fs.ReadFile(src, sp)
		if err != nil {
			return fmt.Errorf("eject: reading %s: %w", sp, err)
		}
		content := upstream
		if strings.HasSuffix(base, ".go") {
			destImport := destImportPath(o, srcDir)
			content, err = rewriteGo(upstream, srcDir, destImport, sp)
			if err != nil {
				return err
			}
		}
		rel = path.Join(planDir, rel)
		seen[rel] = true

		entry := lpEntry(lp, rel)
		out = append(out, classifyDrift(o, rel, entry, upstream, content))
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	// Second pass: lock entries the source no longer carries. Upstream removed
	// a file between ejects; a re-eject would delete it, which is a change the
	// user needs to see before re-running `add`.
	if lp != nil {
		var stale []string
		for rel := range lp.Files {
			if !seen[rel] {
				stale = append(stale, rel)
			}
		}
		sort.Strings(stale)
		for _, rel := range stale {
			out = append(out, DriftEntry{Rel: rel, Drift: DriftUpstream})
		}
	}
	return out, nil
}

// classifyDrift maps the four hash comparisons to a Drift value. The pair
// (upstreamChanged, localChanged) is the full matrix minus the absence case,
// which is handled separately by checking the disk read.
func classifyDrift(o Options, rel string, entry *FileHashes, upstream, vendored []byte) DriftEntry {
	diskHash, onDisk := diskFileHash(o, rel)

	if !onDisk {
		return DriftEntry{Rel: rel, Drift: DriftMissing}
	}
	if entry == nil {
		// On disk but not in the lock — someone hand-added a file where the
		// eject expected none. Treat as a local edit: a re-eject would conflict.
		return DriftEntry{Rel: rel, Drift: DriftLocal}
	}

	upstreamChanged := entry.Upstream != sha256hex(upstream)
	localChanged := entry.Vendored != diskHash
	// If nothing moved, also confirm the current vendored output still equals
	// disk — an upstream change that the rewrite absorbs into identical bytes
	// (rare, but possible for a comment-only upstream edit) is not a drift the
	// user needs to act on.
	currentVendored := sha256hex(vendored)

	// Each case orders the diff so that `+` lines are the ones the reader is
	// asking about. For a local edit that is what YOU added, so the baseline is
	// what the CLI wrote; rendering it the other way shows your own additions as
	// deletions, which reads as though the tool wants to revert you. For an
	// upstream change the question is instead "what would updating do to me", so
	// the baseline is your file and `+` is the incoming change.
	switch {
	case upstreamChanged && localChanged:
		// Both moved: show what --force would do, since that is the decision.
		return DriftEntry{Rel: rel, Drift: DriftBoth,
			Diff: diffFor(rel, diskBytes(o, rel), vendored, "your copy", "upstream, re-ejected")}
	case upstreamChanged && !localChanged:
		// Upstream moved; if the rewrite now yields identical bytes there is
		// nothing to do, otherwise surface the would-be update.
		if diskHash == currentVendored {
			return DriftEntry{Rel: rel, Drift: DriftNone}
		}
		return DriftEntry{Rel: rel, Drift: DriftUpstream,
			Diff: diffFor(rel, diskBytes(o, rel), vendored, "your copy", "upstream, re-ejected")}
	case !upstreamChanged && localChanged:
		return DriftEntry{Rel: rel, Drift: DriftLocal,
			Diff: diffFor(rel, vendored, diskBytes(o, rel), "as ejected", "your copy")}
	default:
		return DriftEntry{Rel: rel, Drift: DriftNone}
	}
}

// diffFor returns a unified diff for text files and "" for binary ones. The
// labels name each side so a reader knows which is theirs without context.
func diffFor(rel string, a, b []byte, labelA, labelB string) string {
	if !looksTextual(a) || !looksTextual(b) {
		return ""
	}
	return unifiedDiff(a, b, rel+" ("+labelA+")", rel+" ("+labelB+")")
}

// looksTextual reports whether b is plausibly text — no NUL byte in the first
// 512 bytes. Mirrors git's heuristic: avoiding a binary diff that would spew
// gibberish into the terminal.
func looksTextual(b []byte) bool {
	n := len(b)
	if n > 512 {
		n = 512
	}
	for i := range n {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

// diskBytes reads the file at ProjectRoot/rel. Empty result when absent; the
// caller has already classified absence via DriftMissing, so this is only
// called when the file exists.
func diskBytes(o Options, rel string) []byte {
	b, err := readDiskFile(o, rel)
	if err != nil {
		return nil
	}
	return b
}

func lpEntry(lp *LockPlugin, rel string) *FileHashes {
	if lp == nil {
		return nil
	}
	return lp.Files[rel]
}
