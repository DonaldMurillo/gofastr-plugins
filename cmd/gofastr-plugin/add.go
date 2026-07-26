package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	gofastrplugins "github.com/DonaldMurillo/gofastr-plugins"
	"github.com/DonaldMurillo/gofastr-plugins/internal/eject"
	"github.com/DonaldMurillo/gofastr-plugins/internal/registry"
)

// cmdAdd vendors one or more plugins into the consumer's repo. It is the core
// of the CLI: plan each plugin against disk + lock, then (unless --dry-run)
// write the files and the lock, and print what to do next.
//
// The plan/apply split is deliberate: BuildPlan never writes, so a --dry-run
// sees exactly what a real add would do, and a human can read the plan before
// trusting the command with their working tree.
func cmdAdd(args []string) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "parent dir for vendored plugins (default \"internal/plugins\", or the lock's value)")
	moduleFlag := fs.String("module", "", "consumer module path (default: read from the nearest go.mod)")
	withTests := fs.Bool("with-tests", false, "also vendor the plugin's *_test.go files (pulls chromedp into your go.mod)")
	noJS := fs.Bool("no-js", false, "skip the TypeScript sources; take only the prebuilt bundle")
	force := fs.Bool("force", false, "overwrite files you have edited since ejecting")
	dryRun := fs.Bool("dry-run", false, "print the plan, write nothing")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: gofastr-plugin add <plugin>... [flags]\n\n"+
			"  vendor one or more plugins into this repo.\n\n"+
			"  --dir DIR         parent dir for vendored plugins\n"+
			"  --module PATH     consumer module path (default: nearest go.mod)\n"+
			"  --with-tests      also vendor *_test.go (pulls chromedp into go.mod)\n"+
			"  --no-js           skip TypeScript sources; take only the bundle\n"+
			"  --force           overwrite files you have edited\n"+
			"  --dry-run         print the plan, write nothing\n")
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "gofastr-plugin add: specify at least one plugin (see `gofastr-plugin list`)")
		return 2
	}

	root, modFromGoMod, err := projectRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gofastr-plugin: %v\n", err)
		return 1
	}
	module := pickString(*moduleFlag, modFromGoMod)

	// Lock carries the prior eject's dir and hashes; it is the source of the
	// --dir default and the conflict-detection input.
	lockPath := filepath.Join(root, eject.LockFileName)
	lock, err := eject.LoadLock(lockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gofastr-plugin: %v\n", err)
		return 1
	}

	// Resolve --dir: explicit flag wins; else the lock's recorded dir if it has
	// one; else the conventional "internal/plugins". Pinning to the lock's dir
	// on re-eject keeps a repo from accidentally sprouting two plugin trees.
	destDir := pickString(*dirFlag, lock.Dir, "internal/plugins")

	idx, err := registry.ParseIndex(gofastrplugins.RegistryJSON())
	if err != nil {
		fmt.Fprintf(os.Stderr, "gofastr-plugin: parsing embedded registry: %v\n", err)
		return 1
	}
	byName := make(map[string]registry.Plugin, len(idx.Plugins))
	for _, p := range idx.Plugins {
		byName[p.Name] = p
	}

	// Validate every requested plugin up front so a typo in the third arg does
	// not eject the first two and then abort — all-or-nothing is safer.
	for _, name := range fs.Args() {
		if _, ok := byName[name]; !ok {
			fmt.Fprintf(os.Stderr, "gofastr-plugin: unknown plugin %q (run `gofastr-plugin list`)\n", name)
			return 1
		}
	}

	cliVer := cliVersion()
	o := eject.Options{
		ProjectRoot: root,
		DestModule:  module,
		DestDir:     destDir,
		WithTests:   *withTests,
		WithJS:      !*noJS, // default true: owning the bundle means owning its source
		Force:       *force,
	}

	exit := 0
	for _, name := range fs.Args() {
		p := byName[name]
		o.Plugin = name
		plan, perr := eject.BuildPlan(gofastrplugins.Source(), p, o, lock)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "gofastr-plugin: %s: %v\n", name, perr)
			exit = 1
			continue
		}
		if *dryRun {
			printPlan(name, plan)
			continue
		}
		if aerr := plan.Apply(o, lock, cliVer); aerr != nil {
			// Apply refuses the whole plan on conflict-without-force; surface
			// the file list it names so the user knows exactly what to review.
			fmt.Fprintf(os.Stderr, "gofastr-plugin: %s: %v\n", name, aerr)
			exit = 1
			continue
		}
		printApplied(name, plan, p, o, cliVer)
	}

	if !*dryRun && exit == 0 {
		// One trailing newline separates the per-plugin blocks from the shared
		// footer; the footer is the line that matters most.
		fmt.Println()
		fmt.Println("This code is yours now; nothing you just wrote depends on gofastr-plugins.")
	}
	return exit
}

// printPlan renders a --dry-run plan: each file and the action Apply would take.
// The statuses are the same words Apply classifies, so a dry-run and a real run
// speak the same vocabulary.
func printPlan(name string, plan *eject.Plan) {
	fmt.Printf("%s  →  %s  (%d file%s)\n", name, plan.Dir, len(plan.Files), plural(len(plan.Files)))
	for _, f := range plan.Files {
		fmt.Printf("    %-10s  %s\n", f.Status, f.Rel)
	}
	fmt.Println()
}

// printApplied renders a successful eject: where it landed, the go get it needs,
// the mount snippet, any per-plugin note, and a docs link. This is the block a
// user pastes into their app; getting it right (real constructor, real import
// path) is what makes the command actionable rather than decorative.
func printApplied(name string, plan *eject.Plan, p registry.Plugin, o eject.Options, cliVer string) {
	pkgDir := strings.TrimPrefix(p.ModulePath, gofastrplugins.ModulePath+"/")
	destImport := path.Join(o.DestModule, o.DestDir, pkgDir)

	var created, updated, unchanged int
	for _, f := range plan.Files {
		switch f.Status {
		case eject.StatusCreate:
			created++
		case eject.StatusUpdate, eject.StatusConflict:
			// Conflict only reaches Apply under --force; either way the file is
			// (re)written, so it counts as an update in the human report.
			updated++
		case eject.StatusUnchanged:
			unchanged++
		}
	}
	fmt.Printf("ejected %s  →  %s  (%d created, %d updated, %d unchanged)\n",
		name, plan.Dir, created, updated, unchanged)

	g := guidanceFor(name, pkgDir, destImport, cliVer, p.Docs)
	fmt.Println("  mount:")
	for _, line := range strings.Split(g.snippet, "\n") {
		if line == "" {
			fmt.Println() // don't indent a blank line into trailing whitespace
			continue
		}
		fmt.Printf("    %s\n", line)
	}
	if g.note != "" {
		fmt.Printf("  note: %s\n", g.note)
	}

	// Dependency resolution is the operator's, not ours: they own the lockfiles,
	// the module proxy, the CI cache, and the choice of which gofastr patch to
	// move to. This command writes files and prints the rest. Anything that
	// silently mutated go.mod or dropped a node_modules/ into the tree would be
	// doing work nobody asked for, invisibly, in a diff nobody reviewed.
	fmt.Println("  install (yours to run — this command does not):")
	// The go directive floor comes first because getting it wrong fails in a
	// way that does not name itself: the build stops on a toolchain error about
	// a transitive gofastr package, not on "your go directive is too old".
	if gv := gofastrplugins.GoVersion; gv != "" {
		fmt.Printf("    # go.mod needs: go %s or newer\n", gv)
	}
	// Pinned to the same gofastr this repo built the vendored bytes against, so
	// the copy and the framework agree.
	if v := gofastrplugins.GoFastrVersion; v != "" {
		fmt.Printf("    go get github.com/DonaldMurillo/gofastr@%s\n", v)
	}
	fmt.Println("    go mod tidy")
	if o.WithJS {
		// Only needed to REBUILD the bundle. assets/ ships prebuilt and go:embed
		// serves it, so a plugin nobody intends to modify needs no Node at all —
		// saying so here stops a consumer installing a toolchain for nothing.
		fmt.Printf("    cd %s/js && npm ci   # only if you want to rebuild the bundle\n", plan.Dir)
	}
	if g.docsURL != "" {
		fmt.Printf("  docs: %s\n", g.docsURL)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// pickString returns the first non-empty argument, falling through left to
// right. Used to layer defaults (flag → lock → convention) without a chain of
// if/else that obscures the precedence.
func pickString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
