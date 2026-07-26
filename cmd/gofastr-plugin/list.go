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

// cmdList prints every plugin in the embedded registry: name, version,
// isolation, a one-line description, and a marker for the ones already vendored
// per this repo's lock. It needs a project root only to locate the lock; absent
// one (running outside a module), it still lists the registry unmarked.
func cmdList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: gofastr-plugin list\n\n  prints every plugin in the embedded registry\n")
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	idx, err := registry.ParseIndex(gofastrplugins.RegistryJSON())
	if err != nil {
		fmt.Fprintf(os.Stderr, "gofastr-plugin: parsing embedded registry: %v\n", err)
		return 1
	}

	// Vendored set comes from the lock, if one is sitting next to a go.mod.
	vendored := map[string]bool{}
	if root, _, rErr := projectRoot(); rErr == nil {
		lock, lErr := eject.LoadLock(filepath.Join(root, eject.LockFileName))
		if lErr == nil {
			for name, lp := range lock.Plugins {
				if lp != nil && len(lp.Files) > 0 {
					vendored[name] = true
				}
			}
		}
	}

	// Column widths: derived from the data so the table aligns without
	// hard-coded widths that would break the day a name grows past them.
	nameW, verW, isoW := 0, 0, 0
	for _, p := range idx.Plugins {
		if len(p.Name) > nameW {
			nameW = len(p.Name)
		}
		if len(p.Version) > verW {
			verW = len(p.Version)
		}
		if len(p.Isolation) > isoW {
			isoW = len(p.Isolation)
		}
	}

	for _, p := range idx.Plugins {
		desc := oneLine(p.Description)
		mark := ""
		if vendored[p.Name] {
			mark = " [vendored]"
		}
		fmt.Printf("  %-*s  %-*s  %-*s  %s%s\n",
			nameW, p.Name, verW, p.Version, isoW, p.Isolation, desc, mark)
	}
	return 0
}

// oneLine collapses a registry description to its first sentence so the list
// stays scannable. The split is on a period followed by space/newline rather
// than any lone ".", so names like "pdf.js" do not cut the sentence short. The
// full description lives in `diff` and the docs; `list` is a directory, not a
// manual.
func oneLine(s string) string {
	for _, sep := range []string{". ", ".\n"} {
		if i := strings.Index(s, sep); i > 0 {
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}
