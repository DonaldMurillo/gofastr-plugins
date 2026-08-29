package gofastrplugins

import (
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/internal/registry"
)

// The embed package is the eject CLI's entire view of the upstream tree, so a
// regression in what it captures is a regression in what the CLI can eject.
// These three guards catch the three ways the embed patterns can go wrong:
//
//   - a greedy pattern (or a future `all:` slip) swallows a node_modules dir
//     and ships a hundred-megabyte binary — assert no path contains it.
//   - a plugin row is added to plugins.json but the embed pattern for its dir
//     is missing or misspelled — assert every registry plugin has its
//     plugin.go and a non-empty assets/ in the tree.
//   - a pattern quietly bloats the binary past the budget — assert the total
//     embedded byte count stays under 20 MB.
func TestSourceCoversRegistryAndStaysSlim(t *testing.T) {
	src := Source()

	// (a) No embedded path may contain node_modules. The patterns are narrow
	// enough that none should match it; this is the trip-wire for a future
	// pattern that stops being narrow.
	var nodeModulesHits []string
	walkErr := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(p, "node_modules") {
			nodeModulesHits = append(nodeModulesHits, p)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking embedded source: %v", walkErr)
	}
	if len(nodeModulesHits) > 0 {
		t.Fatalf("embedded source contains node_modules paths (the embed "+
			"patterns must be narrowed): %v", nodeModulesHits)
	}

	// (b) Every plugin the curated registry names must resolve to a real source
	// dir in the embed. The dir is derived from modulePath, not Name — geomap
	// is named "map" in the registry but lives under geomap/, and the eject
	// copies by directory, not by display name.
	idx, err := registry.ParseIndex(RegistryJSON())
	if err != nil {
		t.Fatalf("parsing embedded registry: %v", err)
	}
	const modulePrefix = ModulePath + "/"
	var totalBytes int64
	for _, p := range idx.Plugins {
		dir, ok := strings.CutPrefix(p.ModulePath, modulePrefix)
		if !ok {
			t.Errorf("%s: modulePath %q does not sit under %s", p.Name, p.ModulePath, ModulePath)
			continue
		}
		// plugin.go is the anchor scanRepoPlugins looks for; without it the
		// plugin is invisible to the registry guards and un-ejectable.
		if _, err := fs.Stat(src, path.Join(dir, "plugin.go")); err != nil {
			t.Errorf("%s: embedded tree missing %s/plugin.go: %v", p.Name, dir, err)
		}
		// assets/ is what every plugin serves; an empty one means the embed
		// pattern for it was dropped or mistyped.
		entries, err := fs.ReadDir(src, path.Join(dir, "assets"))
		if err != nil {
			t.Errorf("%s: embedded tree missing %s/assets/: %v", p.Name, dir, err)
			continue
		}
		if len(entries) == 0 {
			t.Errorf("%s: embedded %s/assets/ is empty", p.Name, dir)
		}
	}

	// (c) Total size budget. The bundles ship prebuilt JS (pdf viewer.js is
	// ~2.7 MB, mermaid diagram.js ~2.5 MB, geomap map.js ~1.1 MB), so the
	// floor is high — but a sudden doubling means an embed pattern went greedy.
	const sizeBudget = 20 << 20 // 20 MB
	fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		totalBytes += info.Size()
		return nil
	})
	if totalBytes >= sizeBudget {
		t.Errorf("embedded source is %d bytes (>=%d MB budget); a pattern "+
			"likely swallowed something it should not", totalBytes, sizeBudget>>20)
	}
	t.Logf("embedded source totals %.1f MB", float64(totalBytes)/(1<<20))
}

// TestSourceEmbedsEveryTrackedJSSubdir is the guard for the failure mode the
// registry check above cannot see: a plugin's own dir is embedded, its
// assets/ is non-empty, and the eject still ships an incomplete tree because
// one js/ subdirectory lost its pattern.
//
// It happened while adding the datagrid plugin: the new patterns were spliced
// in over `//go:embed pdf/js/scripts`, which vanished. Every existing guard
// stayed green, because pdf still had plugin.go and a full assets/ dir.
//
// Directories under <plugin>/js/ are the right unit to check. Individual files
// there are deliberately selective (pdf/js/spike-*.mjs are tracked and NOT
// embedded on purpose), but every tracked SUBDIRECTORY is source the eject
// needs. git is the source of truth so build artifacts like js/test-results,
// which are untracked, do not count.
func TestSourceEmbedsEveryTrackedJSSubdir(t *testing.T) {
	tracked, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable, skipping embed-completeness guard: %v", err)
	}
	idx, err := registry.ParseIndex(RegistryJSON())
	if err != nil {
		t.Fatalf("parsing embedded registry: %v", err)
	}
	pluginDirs := map[string]bool{}
	for _, p := range idx.Plugins {
		if dir, ok := strings.CutPrefix(p.ModulePath, ModulePath+"/"); ok {
			pluginDirs[dir] = true
		}
	}

	src := Source()
	seen := map[string]bool{}
	for _, f := range strings.Split(string(tracked), "\x00") {
		parts := strings.Split(f, "/")
		// <plugin>/js/<subdir>/<file…> — anything shallower is a loose file.
		if len(parts) < 4 || parts[1] != "js" || !pluginDirs[parts[0]] {
			continue
		}
		dir := path.Join(parts[0], "js", parts[2])
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if _, err := fs.Stat(src, dir); err != nil {
			t.Errorf("%s is tracked in git but missing from the embedded tree: "+
				"add a //go:embed pattern for it in source.go (%v)", dir, err)
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no tracked <plugin>/js/<subdir> paths at all; the guard is not testing anything")
	}
}

// TestCIBundleMatrixCoversEveryPlugin stops the CI bundle guard from silently
// falling behind the repo.
//
// The per-plugin "bundle is up to date" job rebuilds <plugin>/js and fails if
// <plugin>/assets moves. It is what prevents a plugin shipping JS that no
// longer matches its sources — and it enumerates plugins by hand, so a plugin
// added without touching the matrix is simply unguarded. pdf was missing from
// it from the day it shipped until #27, and datagrid, chart, logstream,
// imageedit, formbuilder, calendar and whiteboard each had to be remembered.
//
// Every directory with a js/ subdir is a plugin that builds a bundle, so that
// is the set the matrix must cover.
func TestCIBundleMatrixCoversEveryPlugin(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("reading ci.yml: %v", err)
	}
	m := regexp.MustCompile(`(?m)^\s*plugin:\s*\[([^\]]*)\]`).FindSubmatch(workflow)
	if m == nil {
		t.Fatal("no `plugin: [...]` matrix found in ci.yml; if the bundle job was " +
			"restructured, update this guard to match")
	}
	inMatrix := map[string]bool{}
	for _, name := range strings.Split(string(m[1]), ",") {
		if n := strings.TrimSpace(name); n != "" {
			inMatrix[n] = true
		}
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading repo root: %v", err)
	}
	var checked int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.Name(), "js", "package.json")); err != nil {
			continue // not a bundle-building plugin
		}
		checked++
		if !inMatrix[e.Name()] {
			t.Errorf("%s builds a bundle (%s/js/package.json) but is not in the ci.yml "+
				"bundle matrix, so nothing would catch its assets going stale", e.Name(), e.Name())
		}
	}
	if checked == 0 {
		t.Fatal("found no <plugin>/js/package.json at all; the guard is not testing anything")
	}
}

// TestEveryChromedpAllocatorOverridesHeadlessDefaults keeps two fixes for
// recurring CI failures from rotting the way the CI bundle matrix did. Both
// come from headless Chrome defaults that are wrong for this repo, and both
// were fixed at one call site and had to be fixed again at the others.
//
// chromedp waits 20s by default for Chrome to print its DevTools websocket
// URL. A cold start on a loaded runner misses that and reports "chrome start
// (is Chrome installed?): websocket url timeout reached", which reads like a
// missing dependency and is not. Every allocator therefore raises it.
//
// The flake was fixed once in example/smoke_test.go and came straight back from
// pdf/plugin_test.go, because a fix applied to one call site is not applied to
// the others. This asserts all of them.
func TestEveryChromedpAllocatorOverridesHeadlessDefaults(t *testing.T) {
	tracked, err := exec.Command("git", "ls-files", "-z", "*.go").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}
	var checked int
	for _, f := range strings.Split(string(tracked), "\x00") {
		if f == "" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		body := string(src)
		if !strings.Contains(body, "NewExecAllocator") {
			continue
		}
		checked++
		if !strings.Contains(body, "WSURLReadTimeout") {
			t.Errorf("%s builds a chromedp allocator without WSURLReadTimeout, so a cold "+
				"Chrome start on a loaded runner will fail it with a misleading "+
				"\"is Chrome installed?\" error", f)
		}
		// Same shape of trap, found the same way. Headless Chrome's default
		// window is 756x413 — shorter than any real one. A mount below a demo
		// page's hero lands outside it, an unpainted frame gets no
		// requestAnimationFrame, and plugins that render on one (pdf.js among
		// them) do nothing at all while the frame still boots and the bridge
		// still answers. It reads as a hang with an empty console, and it cost
		// two demo-page rewrites before the geometry was dumped (#25).
		if !strings.Contains(body, "WindowSize") {
			t.Errorf("%s builds a chromedp allocator without WindowSize, so it runs at "+
				"headless Chrome's 756x413 default; anything below a page's hero is "+
				"offscreen there and never renders", f)
		}
	}
	if checked == 0 {
		t.Fatal("found no chromedp allocators at all; the guard is not testing anything")
	}
}
