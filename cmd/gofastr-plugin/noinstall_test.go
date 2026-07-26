package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLINeverRunsAProcess is the guard behind the promise printed by every
// successful add: "install (yours to run — this command does not)".
//
// Dependency resolution belongs to whoever runs the project. They own the
// lockfiles, the module proxy, the CI cache, and the decision about which
// gofastr patch to move to. A vendoring tool that quietly shells out to
// `go mod tidy` or `npm install` does work nobody asked for, in a tree nobody
// reviewed — and it does it on a machine whose network posture it knows nothing
// about. So the CLI writes files and prints commands; it never executes one.
//
// That is currently true by inspection, and inspection rots: the guard exists so
// the day someone adds a convenience `exec.Command("go", "mod", "tidy")`, a test
// fails instead of a consumer's go.mod changing under them.
//
// exec.LookPath is deliberately NOT banned. It only asks PATH whether a binary
// exists (cmd/gofastr-plugin/main.go uses it to decide whether `diff` can
// suggest a build step); it starts nothing. Banning process CREATION is the
// precise rule — banning the import would be a blunter one that forces a
// workaround the next time a lookup is genuinely wanted.
func TestCLINeverRunsAProcess(t *testing.T) {
	// Both halves of the feature: the CLI itself and the engine underneath it.
	// The engine is the one a future caller might be tempted to make "helpful".
	roots := []string{".", filepath.Join("..", "..", "internal", "eject")}

	// Every stdlib spelling of "start a process". os/exec is the usual route;
	// syscall.Exec and os.StartProcess are the ones a determined workaround
	// reaches for once exec.Command is being watched.
	banned := []string{
		"exec.Command",
		"exec.CommandContext",
		"os.StartProcess",
		"syscall.Exec",
		"syscall.ForkExec",
	}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Tests may shell out, and one must: the eject integration canary
			// runs `go build` in a temp module, which is the whole proof that an
			// ejected plugin compiles against gofastr alone.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, b := range banned {
				if strings.Contains(string(src), b) {
					t.Errorf("%s calls %s: the eject CLI must not start processes. "+
						"It writes files and PRINTS the install commands; running them is the "+
						"operator's call, not ours.", path, b)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
}
