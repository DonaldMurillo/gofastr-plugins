package eject

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gofastrplugins "github.com/DonaldMurillo/gofastr-plugins"
	"github.com/DonaldMurillo/gofastr-plugins/internal/registry"
)

// TestEjectEveryPluginBuilds is the completeness canary for the whole eject
// feature. It ejects EVERY plugin in the embedded registry into a temp dir
// laid out as a standalone Go module, then runs `go build ./...` in it. The
// ejected tree must compile against gofastr alone — no path back to
// gofastr-plugins may survive — so this is the test that fails the day someone
// adds a plugin whose source imports something the rewrite cannot resolve.
//
// It is skipped under -short (it shells out to `go` and may hit the module
// proxy) and when the `go` binary is absent. GOWORK=off forces the temp module
// to resolve gofastr from the proxy/cache exactly as a real consumer would,
// rather than silently picking up a sibling gofastr checkout via go.work.
func TestEjectEveryPluginBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eject integration build under -short")
	}
	// Opt-in, and it has to be: this test spawns a `go build` that compiles
	// gofastr plus every ejected package from cold. Under `go test ./...` that
	// runs CONCURRENTLY with the chromedp suites in example/ and pdf/, and on a
	// two-core CI runner it starves them — Chrome then misses its 20s websocket
	// handshake and the run fails as "chrome start (is Chrome installed?)",
	// which reads as a broken browser rather than as this test hogging the box.
	// It cost two red runs before the pattern was visible, so the compute is
	// fenced off into its own CI job instead of racing the browsers.
	if os.Getenv("GOFASTR_EJECT_BUILD") == "" {
		t.Skip("set GOFASTR_EJECT_BUILD=1 to run the eject build canary (it is heavy; CI runs it in its own job)")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not in PATH; cannot verify the ejected build")
	}
	_ = goBin // the command below uses "go" via PATH so LookPath is just the detector

	gofastrVersion := gofastrplugins.GoFastrVersion
	if gofastrVersion == "" {
		t.Fatalf("embedded go.mod did not yield a gofastr version; the test cannot pin the require")
	}

	// Read this repo's go.sum — it carries every hash the temp module needs to
	// resolve gofastr and its transitive deps without a second round-trip.
	repoRoot := filepath.Join("..", "..")
	goSum, err := os.ReadFile(filepath.Join(repoRoot, "go.sum"))
	if err != nil {
		t.Fatalf("reading repo go.sum: %v", err)
	}

	// Lay out the temp consumer module.
	tmp := t.TempDir()
	goMod := strings.Join([]string{
		"module ejecttest",
		"",
		"go 1.26.5",
		"",
		"require github.com/DonaldMurillo/gofastr " + gofastrVersion,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("writing temp go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.sum"), goSum, 0o644); err != nil {
		t.Fatalf("writing temp go.sum: %v", err)
	}

	// Eject every plugin the curated registry names. The lock is shared across
	// ejects (a real consumer's lock is one file) and accumulates one entry per
	// plugin — the same shape `gofastr-plugin add` produces across calls.
	idx, err := registry.ParseIndex(gofastrplugins.RegistryJSON())
	if err != nil {
		t.Fatalf("parsing embedded registry: %v", err)
	}
	lock := &Lock{Plugins: map[string]*LockPlugin{}}
	o := Options{
		ProjectRoot: tmp,
		DestModule:  "ejecttest",
		DestDir:     "internal/plugins",
		WithJS:      true, // default per the CLI; owning the bundle means owning its source
	}
	for _, p := range idx.Plugins {
		o.Plugin = p.Name
		plan, err := BuildPlan(gofastrplugins.Source(), p, o, lock)
		if err != nil {
			t.Fatalf("BuildPlan(%s): %v", p.Name, err)
		}
		if err := plan.Apply(o, lock, "v0.3.0"); err != nil {
			t.Fatalf("Apply(%s): %v", p.Name, err)
		}
	}

	// The moment of truth: does the ejected tree compile against gofastr alone?
	// -mod=mod lets go add the indirect requires the ejected packages pull in;
	// GOWORK=off forces resolution from the module proxy/cache.
	build := exec.Command("go", "build", "./...")
	build.Dir = tmp
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./... in ejected module failed:\n%s\n%v", out, err)
	}

	// Assert no gofastr-plugins require leaked into the temp go.mod. The whole
	// point of ejecting is "nothing you just wrote depends on gofastr-plugins";
	// a stray require means the rewrite missed a path and the build only
	// succeeded because the module graph still reached this repo.
	gotMod, err := os.ReadFile(filepath.Join(tmp, "go.mod"))
	if err != nil {
		t.Fatalf("re-reading temp go.mod: %v", err)
	}
	for _, line := range strings.Split(string(gotMod), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "//") {
			continue // indirect annotations, not requires
		}
		if strings.Contains(l, "gofastr-plugins") {
			t.Errorf("temp go.mod references gofastr-plugins after eject — the rewrite leaked:\n  %s", line)
		}
	}
}
