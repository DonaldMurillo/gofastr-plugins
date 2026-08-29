package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/internal/registry"
)

// TestGalleryListsEveryShippedPlugin stops the gallery from silently falling
// behind the repo.
//
// demoEntries in shell.go drives both the sidebar and the home grid, and it is
// a hand-written list. A plugin can therefore ship complete — registered,
// routed, tested, documented — and still be invisible from "/", with its own
// demo page's "Gallery" link pointing at a page that does not mention it. That
// is what happened to the scanner: it merged fully wired and unlisted.
//
// plugins.json is the repo's own answer to "what ships", so it is the list the
// gallery must cover. The registry's name for a plugin is not always its
// directory (geomap is registered as "map"), so this matches on the demo path's
// last segment as well as the name.
func TestGalleryListsEveryShippedPlugin(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "plugins.json"))
	if err != nil {
		t.Fatalf("reading plugins.json: %v", err)
	}
	idx, err := registry.ParseIndex(raw)
	if err != nil {
		t.Fatalf("parsing plugins.json: %v", err)
	}

	listed := map[string]bool{}
	for _, e := range demoEntries {
		listed[e.Slug] = true
		listed[filepath.Base(e.Path)] = true
	}

	for _, p := range idx.Plugins {
		dir := filepath.Base(p.ModulePath)
		if listed[p.Name] || listed[dir] {
			continue
		}
		t.Errorf("plugin %q (%s) ships in plugins.json but has no gallery entry in "+
			"example/shell.go, so it is unreachable from \"/\" and its own demo page's "+
			"Gallery link leads somewhere that does not list it", p.Name, dir)
	}

	if len(demoEntries) == 0 {
		t.Fatal("the gallery lists nothing at all; this guard is not testing anything")
	}
}

// TestGalleryListsEveryShippedRecipe is the recipe half of the guard above.
//
// The plugin half exists because the scanner merged fully wired and unlisted.
// The same thing then happened to a recipe: relayboard shipped into
// recipes/README.md and the changelog while every list that the gallery builds
// from stayed at two entries. A recipe can be complete — routed, tested,
// documented in its own README — and still be absent from "/", from its own
// landing page, and from the screenshot sweep that is this repo's only visual
// review.
//
// The directories under recipes/ are the answer to "what ships" here, the way
// plugins.json is for plugins. Both lists have to cover them: recipePages
// serves the landing page, recipeEntries puts it in the sidebar and home grid.
func TestGalleryListsEveryShippedRecipe(t *testing.T) {
	ents, err := os.ReadDir(filepath.Join("..", "recipes"))
	if err != nil {
		t.Fatalf("reading recipes/: %v", err)
	}

	paged := map[string]bool{}
	for _, p := range recipePages {
		paged[p.Slug] = true
	}
	listed := map[string]bool{}
	for _, e := range recipeEntries {
		listed[e.Slug] = true
		listed[filepath.Base(e.Path)] = true
	}

	var found int
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		found++
		slug := ent.Name()
		if !paged[slug] {
			t.Errorf("recipes/%s has no recipePages entry in recipes.go, so /recipes/%s 404s", slug, slug)
		}
		if !listed[slug] {
			t.Errorf("recipes/%s is not in recipeEntries in shell.go, so the gallery never links it", slug)
		}
	}
	if found == 0 {
		t.Error("found no recipe directories; this guard would pass on an empty repo")
	}
}
