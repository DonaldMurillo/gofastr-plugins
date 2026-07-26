package gofastrplugins

import (
	"io/fs"
	"path"
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
