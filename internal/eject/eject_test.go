package eject

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DonaldMurillo/gofastr-plugins/internal/registry"
)

// The unit tests build a synthetic plugin tree (not the real embed) so they
// exercise the engine without pulling 11 MB of bundled JS into the test binary.
// fstest.MapFS stands in for gofastrplugins.Source(); the engine cannot tell
// the difference because it only sees an fs.FS.

const (
	edplugPluginGo = `package edplug

import (
	"github.com/DonaldMurillo/gofastr-plugins/edplug/ssr"
	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

const Name = "edplug"

var _ = ssr.Render
var _ = pluginhost.Manifest{}
`
	ssrRenderGo = `package ssr

// Render is the SSR entry. ssr does not import pluginhost — it is a pure-Go
// read view — so this file passes through the rewrite unchanged.
func Render() string { return "" }
`
	adapterJS = `// edplug host adapter (non-framed, same-origin).
window.__gofastrPluginHost = window.__gofastrPluginHost || {};
`
)

// fakeFS is a minimal but realistic plugin tree: a .go package, a sibling
// subpackage (mirrors richtext/ssr), framed assets, a host adapter, and js/
// sources. It is enough to exercise every copy rule and both import rewrites.
func fakeFS() fstest.MapFS {
	return fstest.MapFS{
		"edplug/plugin.go":       {Data: []byte(edplugPluginGo)},
		"edplug/doc.go":          {Data: []byte("package edplug\n\n// Package edplug is a test fixture.\n")},
		"edplug/plugin_test.go":  {Data: []byte("package edplug\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")},
		"edplug/ssr/render.go":   {Data: []byte(ssrRenderGo)},
		"edplug/assets/app.html": {Data: []byte("<!doctype html><html><body></body></html>")},
		"edplug/assets/app.js":   {Data: []byte("console.log('edplug');")},
		"edplug/host/adapter.js": {Data: []byte(adapterJS)},
		"edplug/js/build.mjs":    {Data: []byte("export default {};")},
		"edplug/js/package.json": {Data: []byte(`{"name":"edplug"}`)},
		"edplug/js/src/app.ts":   {Data: []byte("export function app() {}")},
	}
}

func edplugPlugin() registry.Plugin {
	return registry.Plugin{
		Name:       "edplug",
		ModulePath: SourceModulePath + "/edplug",
		Version:    "0.1.0",
	}
}

func testOpts(t *testing.T) Options {
	t.Helper()
	return Options{
		Plugin:      "edplug",
		ProjectRoot: t.TempDir(),
		DestModule:  "ejecttest",
		DestDir:     "internal/plugins",
		WithTests:   false,
		WithJS:      true,
	}
}

// findFile returns the File in plan whose Rel matches, t.Fataling otherwise.
func findFile(t *testing.T, plan *Plan, rel string) File {
	t.Helper()
	for _, f := range plan.Files {
		if f.Rel == rel {
			return f
		}
	}
	t.Fatalf("plan missing file %s", rel)
	return File{}
}

// TestRewriteImports covers the two rewrites that make an ejected plugin
// depend only on gofastr core: pluginhost → framework/pluginhost, and the
// plugin's own package (incl. the richtext/ssr-style sibling) → the consumer's
// dest path. This is the structural reason ejection leaves no dependency on
// this repo behind; if it regresses, the whole feature is a lie.
func TestRewriteImports(t *testing.T) {
	plan, err := BuildPlan(fakeFS(), edplugPlugin(), testOpts(t), &Lock{Plugins: map[string]*LockPlugin{}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	plug := findFile(t, plan, "internal/plugins/edplug/plugin.go")
	got := string(plug.Content)
	if strings.Contains(got, SourceModulePath) {
		t.Errorf("plugin.go still references gofastr-plugins:\n%s", got)
	}
	if !strings.Contains(got, `"github.com/DonaldMurillo/gofastr/framework/pluginhost"`) {
		t.Errorf("plugin.go did not rewrite pluginhost → framework/pluginhost:\n%s", got)
	}
	if !strings.Contains(got, `"ejecttest/internal/plugins/edplug/ssr"`) {
		t.Errorf("plugin.go did not rewrite edplug/ssr → consumer dest:\n%s", got)
	}
	// The sibling subpackage file has no gofastr-plugins import of its own, so
	// it must pass through verbatim (format may reindent, but the body is fixed).
	ssr := findFile(t, plan, "internal/plugins/edplug/ssr/render.go")
	if strings.Contains(string(ssr.Content), SourceModulePath) {
		t.Errorf("ssr/render.go picked up a spurious gofastr-plugins ref:\n%s", ssr.Content)
	}
	if !strings.Contains(string(ssr.Content), "func Render() string") {
		t.Errorf("ssr/render.go body corrupted by rewrite:\n%s", ssr.Content)
	}
}

// TestFilters affirms WithTests and WithJS gate the two optional subtrees: by
// default no *_test.go and no js/; with the flags, both come through. The
// assets/ bundle is always copied (it is the shipped artifact, not source).
func TestFilters(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		o := testOpts(t)
		o.WithJS = false
		plan, err := BuildPlan(fakeFS(), edplugPlugin(), o, &Lock{Plugins: map[string]*LockPlugin{}})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		assertHasFile(t, plan, "internal/plugins/edplug/plugin.go")
		assertHasFile(t, plan, "internal/plugins/edplug/assets/app.js")
		assertNoFile(t, plan, "internal/plugins/edplug/plugin_test.go")
		assertNoFile(t, plan, "internal/plugins/edplug/js/src/app.ts")
	})
	t.Run("with_tests_and_js", func(t *testing.T) {
		o := testOpts(t)
		o.WithTests = true
		plan, err := BuildPlan(fakeFS(), edplugPlugin(), o, &Lock{Plugins: map[string]*LockPlugin{}})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		assertHasFile(t, plan, "internal/plugins/edplug/plugin_test.go")
		assertHasFile(t, plan, "internal/plugins/edplug/js/src/app.ts")
	})
}

func assertHasFile(t *testing.T, plan *Plan, rel string) {
	t.Helper()
	for _, f := range plan.Files {
		if f.Rel == rel {
			return
		}
	}
	t.Errorf("plan missing expected file %s", rel)
}
func assertNoFile(t *testing.T, plan *Plan, rel string) {
	t.Helper()
	for _, f := range plan.Files {
		if f.Rel == rel {
			t.Errorf("plan unexpectedly includes %s", rel)
		}
	}
}

// TestStatusCreate is the clean-eject case: nothing on disk, no lock entry →
// every file is StatusCreate.
func TestStatusCreate(t *testing.T) {
	o := testOpts(t)
	plan, err := BuildPlan(fakeFS(), edplugPlugin(), o, &Lock{Plugins: map[string]*LockPlugin{}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, f := range plan.Files {
		if f.Status != StatusCreate {
			t.Errorf("%s: want create, got %s", f.Rel, f.Status)
		}
	}
}

// TestApplyThenUpdate is the round-trip: a fresh Apply writes every file and a
// lock; re-planning against that lock classifies every file as StatusUnchanged
// (the new vendored output equals what we last wrote). It is the proof that
// re-running `add` with no changes is a no-op, which is what makes the command
// safe to re-run.
func TestApplyThenUnchanged(t *testing.T) {
	o := testOpts(t)
	src := fakeFS()
	p := edplugPlugin()
	lock := &Lock{Plugins: map[string]*LockPlugin{}}

	plan, err := BuildPlan(src, p, o, lock)
	if err != nil {
		t.Fatalf("BuildPlan #1: %v", err)
	}
	if err := plan.Apply(o, lock, "v0.3.0"); err != nil {
		t.Fatalf("Apply #1: %v", err)
	}

	// The lock now records every file; re-planning sees them on disk matching.
	plan2, err := BuildPlan(src, p, o, lock)
	if err != nil {
		t.Fatalf("BuildPlan #2: %v", err)
	}
	for _, f := range plan2.Files {
		if f.Status != StatusUnchanged {
			t.Errorf("%s: want unchanged on re-run, got %s", f.Rel, f.Status)
		}
	}
}

// TestConflictOnLocalEdit is the one rule that makes the command trustworthy:
// a file the user edited (disk no longer matches the recorded vendored hash)
// must classify as StatusConflict, and Apply must refuse the whole plan without
// --force rather than clobber the only copy of the user's work.
func TestConflictOnLocalEdit(t *testing.T) {
	o := testOpts(t)
	src := fakeFS()
	p := edplugPlugin()
	lock := &Lock{Plugins: map[string]*LockPlugin{}}

	plan, err := BuildPlan(src, p, o, lock)
	if err != nil {
		t.Fatalf("BuildPlan #1: %v", err)
	}
	if err := plan.Apply(o, lock, "v0.3.0"); err != nil {
		t.Fatalf("Apply #1: %v", err)
	}

	// Simulate a user edit: rewrite one vendored file on disk.
	edited := filepath.Join(o.ProjectRoot, "internal/plugins/edplug/host/adapter.js")
	if err := os.WriteFile(edited, []byte("// MY EDIT\n"), 0o644); err != nil {
		t.Fatalf("editing adapter.js: %v", err)
	}

	plan2, err := BuildPlan(src, p, o, lock)
	if err != nil {
		t.Fatalf("BuildPlan #2: %v", err)
	}
	adapter := findFile(t, plan2, "internal/plugins/edplug/host/adapter.js")
	if adapter.Status != StatusConflict {
		t.Errorf("adapter.js: want conflict after local edit, got %s", adapter.Status)
	}
	// Without --force, Apply must refuse.
	if err := plan2.Apply(o, lock, "v0.3.0"); err == nil {
		t.Fatalf("Apply succeeded without --force on a conflict; the user's edit would be lost")
	}

	// With --force, Apply proceeds and the file is overwritten.
	o.Force = true
	if err := plan2.Apply(o, lock, "v0.3.0"); err != nil {
		t.Fatalf("Apply with --force: %v", err)
	}
	got, err := os.ReadFile(edited)
	if err != nil {
		t.Fatalf("re-reading forced file: %v", err)
	}
	if strings.Contains(string(got), "MY EDIT") {
		t.Errorf("--force did not overwrite the local edit")
	}
}

// TestUntrackedFileConfirms asserts an untracked file occupying a dest path the
// eject wants to write is a Conflict too (no lock entry, file exists) — the
// matrix entry for "the user hand-created a file where the plugin would land".
func TestUntrackedFileConflicts(t *testing.T) {
	o := testOpts(t)
	// Pre-create a file where the plugin would land, with no lock entry.
	target := filepath.Join(o.ProjectRoot, "internal/plugins/edplug/plugin.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("package edplug\n// hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(fakeFS(), edplugPlugin(), o, &Lock{Plugins: map[string]*LockPlugin{}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	plug := findFile(t, plan, "internal/plugins/edplug/plugin.go")
	if plug.Status != StatusConflict {
		t.Errorf("plugin.go: want conflict (untracked), got %s", plug.Status)
	}
}

// TestLockRoundTrip writes a lock via Apply, reloads it from disk, and checks
// the recorded hashes match what a fresh BuildPlan would compute. A lock that
// lies about its hashes would break drift detection silently — the worst kind
// of regression — so this pins the (upstream, vendored) pair against the files.
func TestLockRoundTrip(t *testing.T) {
	o := testOpts(t)
	src := fakeFS()
	p := edplugPlugin()
	lock := &Lock{Plugins: map[string]*LockPlugin{}}

	plan, err := BuildPlan(src, p, o, lock)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if err := plan.Apply(o, lock, "v0.3.0"); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	lockPath := filepath.Join(o.ProjectRoot, LockFileName)
	reloaded, err := LoadLock(lockPath)
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	lp := reloaded.Plugins["edplug"]
	if lp == nil {
		t.Fatalf("reloaded lock has no edplug entry")
	}
	if lp.EjectedFrom != "v0.3.0" {
		t.Errorf("EjectedFrom = %q, want v0.3.0", lp.EjectedFrom)
	}
	if lp.Dir != "internal/plugins/edplug" {
		t.Errorf("Dir = %q, want internal/plugins/edplug", lp.Dir)
	}
	if !lp.WithJS || lp.WithTests {
		t.Errorf("WithJS=%v WithTests=%v, want true/false", lp.WithJS, lp.WithTests)
	}
	// Every plan file must have a recorded hash pair matching the bytes.
	for _, f := range plan.Files {
		h := lp.Files[f.Rel]
		if h == nil {
			t.Errorf("lock missing entry for %s", f.Rel)
			continue
		}
		if h.Upstream != sha256hex(f.Upstream) {
			t.Errorf("%s: upstream hash mismatch", f.Rel)
		}
		if h.Vendored != sha256hex(f.Content) {
			t.Errorf("%s: vendored hash mismatch", f.Rel)
		}
	}
}

// TestDriftClassification drives Compare through all five Drift states on one
// synthetic tree, by mutating disk and upstream in the four combinations plus
// the missing case. This is the proof that `diff` can tell the cases apart,
// which is the whole reason the lock carries two hashes per file.
func TestDriftClassification(t *testing.T) {
	o := testOpts(t)
	src := fakeFS()
	p := edplugPlugin()
	lock := &Lock{Plugins: map[string]*LockPlugin{}}

	// Baseline eject.
	plan, err := BuildPlan(src, p, o, lock)
	if err != nil {
		t.Fatalf("BuildPlan baseline: %v", err)
	}
	if err := plan.Apply(o, lock, "v0.3.0"); err != nil {
		t.Fatalf("Apply baseline: %v", err)
	}

	// Case 1: nothing moved → DriftNone.
	entries, err := Compare(src, p, o, lock)
	if err != nil {
		t.Fatalf("Compare baseline: %v", err)
	}
	if e := findEntry(t, entries, "internal/plugins/edplug/host/adapter.js"); e.Drift != DriftNone {
		t.Errorf("baseline: want none, got %s", e.Drift)
	}

	// Case 2: local edit only → DriftLocal.
	adapterPath := filepath.Join(o.ProjectRoot, "internal/plugins/edplug/host/adapter.js")
	os.WriteFile(adapterPath, []byte("// local edit\n"), 0o644)
	entries, err = Compare(src, p, o, lock)
	if err != nil {
		t.Fatalf("Compare local: %v", err)
	}
	e := findEntry(t, entries, "internal/plugins/edplug/host/adapter.js")
	if e.Drift != DriftLocal {
		t.Errorf("local edit: want local-edit, got %s", e.Drift)
	}
	if e.Diff == "" {
		t.Errorf("local edit: expected a non-empty diff")
	}

	// Reset the local edit, then Case 3: upstream changed → DriftUpstream.
	os.WriteFile(adapterPath, []byte(adapterJS), 0o644)
	srcUp := fakeFS()
	srcUp["edplug/host/adapter.js"].Data = []byte("// upstream changed adapter\n")
	entries, err = Compare(srcUp, p, o, lock)
	if err != nil {
		t.Fatalf("Compare upstream: %v", err)
	}
	if e := findEntry(t, entries, "internal/plugins/edplug/host/adapter.js"); e.Drift != DriftUpstream {
		t.Errorf("upstream change: want upstream-changed, got %s", e.Drift)
	}

	// Case 4: both moved → DriftBoth.
	os.WriteFile(adapterPath, []byte("// local edit on top of upstream change\n"), 0o644)
	entries, err = Compare(srcUp, p, o, lock)
	if err != nil {
		t.Fatalf("Compare both: %v", err)
	}
	if e := findEntry(t, entries, "internal/plugins/edplug/host/adapter.js"); e.Drift != DriftBoth {
		t.Errorf("both moved: want both, got %s", e.Drift)
	}

	// Case 5: file missing from disk → DriftMissing.
	os.Remove(adapterPath)
	entries, err = Compare(fakeFS(), p, o, lock)
	if err != nil {
		t.Fatalf("Compare missing: %v", err)
	}
	if e := findEntry(t, entries, "internal/plugins/edplug/host/adapter.js"); e.Drift != DriftMissing {
		t.Errorf("missing: want missing, got %s", e.Drift)
	}
}

func findEntry(t *testing.T, entries []DriftEntry, rel string) DriftEntry {
	t.Helper()
	for _, e := range entries {
		if e.Rel == rel {
			return e
		}
	}
	t.Fatalf("no drift entry for %s", rel)
	return DriftEntry{}
}

// TestNoGofastrPluginsSurvives is the promise-keeping guard: a plugin whose
// source imports a SIBLING plugin package (which the two fixed rewrites do not
// cover) must fail BuildPlan rather than ship a vendored copy that still
// reaches back into this repo.
func TestNoGofastrPluginsSurvives(t *testing.T) {
	crossFS := fstest.MapFS{
		"cross/plugin.go": {Data: []byte(`package cross

import (
	"github.com/DonaldMurillo/gofastr-plugins/mermaid"
	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

var _ = mermaid.Name
var _ = pluginhost.Manifest{}
`)},
		"cross/assets/x.html": {Data: []byte("<html></html>")},
	}
	p := registry.Plugin{Name: "cross", ModulePath: SourceModulePath + "/cross", Version: "0.1.0"}
	_, err := BuildPlan(crossFS, p, testOpts(t), &Lock{Plugins: map[string]*LockPlugin{}})
	if err == nil {
		t.Fatalf("BuildPlan succeeded; a gofastr-plugins/mermaid reference survived the rewrite")
	}
	if !strings.Contains(err.Error(), "still references") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestUnifiedDiff is a focused check of the diff helper: an insertion, a
// deletion, and identity. The drift tests above exercise it indirectly; this
// one pins the format so a future refactor of the LCS does not silently change
// what `diff` prints.
func TestUnifiedDiff(t *testing.T) {
	t.Run("identical", func(t *testing.T) {
		if got := unifiedDiff([]byte("a\nb\n"), []byte("a\nb\n"), "f (a)", "f (b)"); got != "" {
			t.Errorf("identical input should produce no diff, got:\n%s", got)
		}
	})
	t.Run("change", func(t *testing.T) {
		got := unifiedDiff([]byte("a\nb\nc\n"), []byte("a\nB\nc\n"), "f (a)", "f (b)")
		if !strings.Contains(got, "-b") || !strings.Contains(got, "+B") {
			t.Errorf("diff missing -b/+B lines:\n%s", got)
		}
		if !strings.Contains(got, "@@") {
			t.Errorf("diff missing hunk header:\n%s", got)
		}
	})
}

// TestSrcDirOfGeomap pins the one wrinkle in the source-dir derivation: geomap
// is named "map" in the registry but its package dir is geomap/. The eject must
// copy by directory, so srcDirOf reads modulePath, not Name.
func TestSrcDirOfGeomap(t *testing.T) {
	got, err := srcDirOf(SourceModulePath + "/geomap")
	if err != nil {
		t.Fatalf("srcDirOf: %v", err)
	}
	if got != "geomap" {
		t.Errorf("srcDirOf(geomap modulePath) = %q, want geomap", got)
	}
}

// TestLoadMissingLockReturnsEmpty verifies a missing lock is not an error — the
// first eject starts from a clean slate. A half-broken first-run UX over a
// missing lock file would be a poor introduction to the command.
func TestLoadMissingLockReturnsEmpty(t *testing.T) {
	d := t.TempDir()
	lock, err := LoadLock(filepath.Join(d, LockFileName))
	if err != nil {
		t.Fatalf("LoadLock on missing file: %v", err)
	}
	if lock.Plugins == nil {
		t.Errorf("Plugins map is nil; expected empty map")
	}
}

// _ keeps io/fs referenced for future helpers without a per-test import dance.
var _ = fs.WalkDir
