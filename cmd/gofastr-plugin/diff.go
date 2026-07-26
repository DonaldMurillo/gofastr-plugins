package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gofastrplugins "github.com/DonaldMurillo/gofastr-plugins"
	"github.com/DonaldMurillo/gofastr-plugins/internal/eject"
	"github.com/DonaldMurillo/gofastr-plugins/internal/registry"
)

// cmdDiff reports how vendored plugins have drifted from upstream (or from the
// user's last eject). It is the command a consumer runs before re-ejecting to
// see whether a re-add would be a clean update, a conflict, or a no-op.
//
// Exit code 0 means no drift; 1 means drift (so the command is usable as a CI
// gate). With no plugin argument it diffs every vendored plugin in the lock.
func cmdDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: gofastr-plugin diff [<plugin>]\n\n"+
			"  show how vendored files drifted from upstream, or from your last eject.\n"+
			"  exits 1 when there is drift, 0 otherwise.\n")
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	root, module, err := projectRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gofastr-plugin: %v\n", err)
		return 1
	}
	lockPath := filepath.Join(root, eject.LockFileName)
	lock, err := eject.LoadLock(lockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gofastr-plugin: %v\n", err)
		return 1
	}
	if len(lock.Plugins) == 0 {
		fmt.Println("nothing vendored yet (no plugins in the lock)")
		return 0
	}

	// Decide the scope: an explicit plugin arg, or every plugin in the lock.
	// An explicit name that is not vendored is an error — the user asked about
	// a specific thing and it is not there.
	var targets []string
	if fs.NArg() > 0 {
		name := fs.Arg(0)
		if _, ok := lock.Plugins[name]; !ok {
			fmt.Fprintf(os.Stderr, "gofastr-plugin: %q is not vendored (not in %s)\n", name, eject.LockFileName)
			return 1
		}
		targets = []string{name}
	} else {
		for name, lp := range lock.Plugins {
			if lp != nil && len(lp.Files) > 0 {
				targets = append(targets, name)
			}
		}
	}

	// The registry gives us the modulePath (→ source dir) for each plugin; the
	// lock's per-plugin entry carries the options it was ejected with, which
	// select the same file set diff walks.
	idx, err := registry.ParseIndex(gofastrplugins.RegistryJSON())
	if err != nil {
		fmt.Fprintf(os.Stderr, "gofastr-plugin: parsing embedded registry: %v\n", err)
		return 1
	}
	byName := make(map[string]registry.Plugin, len(idx.Plugins))
	for _, p := range idx.Plugins {
		byName[p.Name] = p
	}

	anyDrift := false
	for _, name := range targets {
		p, ok := byName[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "gofastr-plugin: %q is vendored but no longer in the registry; cannot diff\n", name)
			anyDrift = true
			continue
		}
		lp := lock.Plugins[name]
		o := eject.Options{
			Plugin:      name,
			ProjectRoot: root,
			DestModule:  module,
			DestDir:     lp.Dir,
			WithTests:   lp.WithTests,
			WithJS:      lp.WithJS,
		}
		// The diff scope is the plugin's vendored dir, so DestDir here is that
		// plugin's full dir (e.g. internal/plugins/mermaid) rather than the
		// parent. Reconstructing Options from the lock is what makes diff
		// reproduce the same file set as the original eject.
		o.DestDir = parentDir(lp.Dir)
		entries, derr := eject.Compare(gofastrplugins.Source(), p, o, lock)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "gofastr-plugin: diff %s: %v\n", name, derr)
			return 1
		}
		hasDrift := printDiff(name, entries)
		if hasDrift {
			anyDrift = true
		}
	}
	if anyDrift {
		return 1
	}
	fmt.Println("no drift")
	return 0
}

// printDiff renders one plugin's drift entries. It returns whether any entry
// drifted, so the caller can set the exit code. Drift-free entries are omitted
// entirely to keep the output focused on what moved.
func printDiff(name string, entries []eject.DriftEntry) bool {
	var drifted bool
	for _, e := range entries {
		if e.Drift == eject.DriftNone {
			continue
		}
		drifted = true
		fmt.Printf("  %-16s  %s  %s\n", name, e.Drift, e.Rel)
		if e.Diff != "" {
			for _, line := range splitLinesKeep(e.Diff) {
				fmt.Printf("    %s\n", line)
			}
		}
	}
	return drifted
}

// parentDir returns the parent of a vendored plugin dir (internal/plugins/
// mermaid → internal/plugins). Compare walks the source tree for the plugin and
// writes into <DestDir>/<srcDir>/..., so DestDir must be the parent.
func parentDir(pluginDir string) string {
	parent := filepath.Dir(pluginDir)
	if parent == "." {
		return ""
	}
	return parent
}

// splitLinesKeep splits on newlines and drops a trailing empty line (the
// artifact of a final newline), preserving every other line verbatim for
// indented reprint under the diff header.
func splitLinesKeep(s string) []string {
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}
