// Command gofastr-plugin vendors a GoFastr plugin into a consumer's own repo,
// shadcn-style: it copies the plugin's source so the consumer owns and can edit
// it, with no dependency on gofastr-plugins left behind.
//
// The vendored copy is the whole point — a consumer who ejects owns the code,
// can patch it, and depends only on gofastr itself. That works because every
// plugin package imports exactly one path from this repo (pluginhost, a
// compatibility alias whose symbols forward to gofastr/framework/pluginhost),
// plus richtext which also imports its own sibling richtext/ssr. The eject
// rewrites both away, so the vendored tree reaches only gofastr core.
//
// The source tree and the curated registry are embedded into this binary at
// build time (see package gofastrplugins), so the CLI is hermetic: no network,
// no module-cache archaeology, no `go` subprocess at eject time.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches a subcommand and returns the process exit code. Exit codes:
//   - 0: success (including `diff` finding no drift)
//   - 1: a recoverable failure the caller may want to script around — conflict
//     without --force, unknown plugin, missing go.mod, or drift found by `diff`
//   - 2: usage error
func run(args []string) int {
	if len(args) == 0 {
		printRootHelp(os.Stdout)
		return 0
	}
	switch args[0] {
	case "list":
		return cmdList(args[1:])
	case "add":
		return cmdAdd(args[1:])
	case "diff":
		return cmdDiff(args[1:])
	case "-h", "--help", "help":
		printRootHelp(os.Stdout)
		return 0
	case "-v", "--version":
		fmt.Println(cliVersion())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "gofastr-plugin: unknown command %q\n", args[0])
		printRootHelp(os.Stderr)
		return 2
	}
}

// printRootHelp is the top-level usage. It mirrors the brief's USAGE block and
// is the single source of truth for what the command does — a user reading only
// this should be able to run list/add/diff without consulting docs.
func printRootHelp(w *os.File) {
	fmt.Fprint(w, `gofastr-plugin — vendor a GoFastr plugin into your own repo

USAGE
  gofastr-plugin list
  gofastr-plugin add <plugin>... [flags]
  gofastr-plugin diff [<plugin>]

COMMANDS
  list              print every plugin in the embedded registry, marking those
                    already vendored in this repo's lock
  add <plugin>...   copy one or more plugins' source into this repo, rewriting
                    imports so nothing left behind depends on gofastr-plugins
  diff [<plugin>]   show how vendored files have drifted from upstream, or from
                    your last eject (exits 1 when there is drift, 0 otherwise)

FLAGS (add)
  --dir string      parent dir for vendored plugins
                    (default "internal/plugins", or the lock's value if one exists)
  --module string   consumer module path (default: read from the nearest go.mod)
  --with-tests      also vendor the plugin's *_test.go files
                    (pulls chromedp into your go.mod)
  --no-js           skip the TypeScript sources; take only the prebuilt bundle
  --force           overwrite files you have edited since ejecting
  --dry-run         print the plan, write nothing

The CLI embeds the plugin source tree and the curated registry, so it runs
offline. After a successful `+"`add`"+`, nothing you just wrote imports
gofastr-plugins — the code is yours.
`)
}

// cliVersion is the provenance stamp recorded in the lock as "ejectedFrom".
// Under `go run github.com/DonaldMurillo/gofastr-plugins/cmd/gofastr-plugin@vX`
// it is the tagged version; from a checkout it is "(devel)". The lock records
// whichever it is so a later `diff` knows which upstream the eject came from.
func cliVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "(devel)"
}

// projectRoot walks up from the cwd to the nearest directory containing a
// go.mod, returning its absolute path and the module line. The command only
// makes sense inside a Go module: there is nowhere else to put a vendored
// package whose import path must resolve.
func projectRoot() (root, module string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	dir := cwd
	for {
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !info.IsDir() {
			mod, mErr := parseModuleLine(filepath.Join(dir, "go.mod"))
			if mErr != nil {
				return "", "", fmt.Errorf("reading %s: %w", filepath.Join(dir, "go.mod"), mErr)
			}
			return dir, mod, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("no go.mod found walking up from %s — "+
				"gofastr-plugin must run inside a Go module", cwd)
		}
		dir = parent
	}
}

// parseModuleLine reads just the `module` directive from a go.mod. The full
// file is golang.org/x/mod's job; for the CLI a line scan is enough and keeps
// the dependency surface at zero.
func parseModuleLine(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "module ")), nil
		}
	}
	return "", fmt.Errorf("no module directive in %s", path)
}

// hasGo reports whether the go tool is on PATH. Used by `diff` to decide
// whether to offer a build step; `add` does not need go (the embed is hermetic).
func hasGo() bool {
	_, err := exec.LookPath("go")
	return err == nil
}
