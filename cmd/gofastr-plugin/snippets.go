package main

import (
	"fmt"
	"strings"
)

// guidance is the per-plugin "what to do next" block printed after a successful
// add. The snippet is uniform (every plugin has New + WithDemoPage) because a
// generic snippet naming the wrong function would be worse than none — and
// here the function is genuinely the same across the set. What differs is the
// note: the host-side obligation each plugin carries that ejecting does not
// waive. A consumer who skips the note gets a plugin that compiles but does not
// run correctly, so it is surfaced next to the snippet rather than buried in
// docs the consumer may not open.
type guidance struct {
	// snippet is the mount code to paste into the consumer's app wiring. It
	// names the real constructor and option for this plugin.
	snippet string
	// note is the extra requirement (CSP allowlist, mandatory handler, route
	// collision) the consumer must satisfy for the plugin to actually work.
	// Empty means "nothing beyond the standard framed-asset CSP".
	note string
	// docsURL is a deep link into this repo's docs at the ejectedFrom ref, when
	// the registry row carried a docs path. Empty when the row had none.
	docsURL string
}

// guidanceFor builds the post-add guidance for one plugin. destImport is the
// consumer import path the rewrite produced (<module>/<dir>/<pkgDir>);
// ejectedFrom is the version stamp for the docs link.
func guidanceFor(name, pkgDir, destImport, ejectedFrom, docsPath string) guidance {
	// Every plugin's constructor is New and every plugin has WithDemoPage, so
	// the mount call is uniform. pkgDir is the Go package name (the directory),
	// which for geomap is "geomap" even though the registry name is "map".
	snippet := fmt.Sprintf("import %q\n\napp.RegisterPlugin(%s.New(%s.WithDemoPage()))",
		destImport, pkgDir, pkgDir)

	g := guidance{snippet: snippet}

	// Per-plugin notes: the load-bearing host-side obligation each carries.
	// These are drawn from the plugins' own docs and the registry descriptions;
	// ejecting copies the code but does not exempt anyone from the operational
	// requirement that surrounds it.
	switch name {
	case "richtext":
		g.note = "WithDemoPage serves the editor at \"/\"; use richtext.WithDemoRoute(\"/your/path\") " +
			"if your app owns the homepage."
	case "pdf":
		g.note = "Default mode is view-only. Redaction needs pdf.WithMode(pdf.ModeRedact) AND a " +
			"pdf.WithExportHandler — ModeRedact refuses to construct without somewhere for the " +
			"produced bytes to go. The frame's connect-src 'none' cage is enforced by the plugin itself."
	case "map":
		g.note = "Host-page CSP must allow connect-src/img-src https://tiles.openfreemap.org and " +
			"worker-src blob: for MapLibre to fetch OpenFreeMap tiles and spawn its worker. " +
			"The plugin runs trusted (host page), not sandboxed."
	case "tour":
		g.note = "Runs trusted in the host page (not sandboxed) so it can spotlight real DOM " +
			"elements. Register tours server-side with tour.WithTour(id, steps)."
	}

	if docsPath != "" {
		g.docsURL = fmt.Sprintf("https://github.com/DonaldMurillo/gofastr-plugins/blob/%s/%s",
			ejectedFrom, strings.TrimPrefix(docsPath, "/"))
	}
	return g
}
