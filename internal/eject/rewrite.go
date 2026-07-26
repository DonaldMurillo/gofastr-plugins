package eject

import (
	"bytes"
	"fmt"
	"go/format"
	"path"
	"strings"
)

// SourceModulePath is the import-path prefix of THIS repo. It is duplicated
// here (gofastrplugins also carries it) rather than imported from the root
// package, because internal/eject must stay decoupled from the 11 MB embed —
// its unit tests build a synthetic fs.FS, not the real one. The two constants
// are kept in sync by source_test.go's assertion that every registry
// modulePath sits under this prefix.
const SourceModulePath = "github.com/DonaldMurillo/gofastr-plugins"

// pluginhost is a compatibility alias: every symbol it re-exports forwards to
// the framework package. An ejected plugin wants the real thing, so this is the
// one import rewrite applied to every plugin regardless of its name.
const (
	pluginhostImport        = SourceModulePath + "/pluginhost"
	frameworkPluginhostPath = "github.com/DonaldMurillo/gofastr/framework/pluginhost"
)

// srcDirOf derives the on-disk source directory for a plugin from its
// modulePath: the segment after the repo module prefix. For every plugin except
// geomap this equals the registry name; geomap is named "map" in the registry
// but its package directory is geomap/ (Go reserves `map` as a keyword), so the
// eject must copy by directory, not by display name.
func srcDirOf(modulePath string) (string, error) {
	prefix := SourceModulePath + "/"
	dir, ok := strings.CutPrefix(modulePath, prefix)
	if !ok {
		return "", fmt.Errorf("eject: modulePath %q does not sit under %s", modulePath, SourceModulePath)
	}
	if dir == "" || strings.ContainsAny(dir, "/") {
		return "", fmt.Errorf("eject: modulePath %q does not name a top-level plugin directory", modulePath)
	}
	return dir, nil
}

// destImportPath is the consumer import path a plugin's own packages rewrite
// to: <DestModule>/<DestDir>/<srcDir>, always with forward slashes. DestDir is
// repo-relative and may have been typed with OS separators on Windows; an
// import path is not a filesystem path, so it is normalised here.
func destImportPath(o Options, srcDir string) string {
	return path.Join(o.DestModule, path.Clean(o.DestDir), srcDir)
}

// rewriteGo rewrites a single .go file's import paths so it depends only on
// gofastr core (never on this repo), then runs go/format.Source. format.Source
// both proves the file still parses AND re-sorts the import block (it calls
// ast.SortImports internally), which the rewrite usually knocks out of order.
//
// The two rewrites operate on the exact quoted import path — the opening quote
// is part of the match key — so they cannot bleed into comments or unquoted
// tokens. The second rule matches the plugin's package AND any subpackage
// prefix (richtext → richtext/ssr) because it stops matching at the opening
// quote and leaves the closing quote or the /sub suffix untouched.
//
// A parse failure is fatal and surfaces the file path: never write unparseable
// Go, and never silently drop a file whose imports could not be resolved.
func rewriteGo(src []byte, srcDir, destImport, rel string) ([]byte, error) {
	out := src
	out = bytes.ReplaceAll(out,
		[]byte(`"`+pluginhostImport+`"`),
		[]byte(`"`+frameworkPluginhostPath+`"`))
	// Two exact forms rather than one open-ended prefix. Replacing the bare
	// prefix `"…/gofastr-plugins/richtext` would also swallow a future sibling
	// whose directory merely STARTS with this one's name (`richtext2`,
	// `richtextlite`), silently rewriting it to `<dest>/richtext2` — a path that
	// happens to look plausible and would not be caught by
	// ensureNoGofastrPluginsRefs, since the gofastr-plugins prefix is gone by
	// then. Anchoring on the closing quote and on the subpackage separator keeps
	// the match to this plugin and its own subtree (richtext → richtext/ssr).
	out = bytes.ReplaceAll(out,
		[]byte(`"`+SourceModulePath+`/`+srcDir+`"`),
		[]byte(`"`+destImport+`"`))
	out = bytes.ReplaceAll(out,
		[]byte(`"`+SourceModulePath+`/`+srcDir+`/`),
		[]byte(`"`+destImport+`/`))

	formatted, err := format.Source(out)
	if err != nil {
		return nil, fmt.Errorf("eject: rewrite left %s unparseable: %w", rel, err)
	}
	return formatted, nil
}

// ensureNoGofastrPluginsRefs is the promise-keeping guard: after rewriting, no
// .go file in the plan may contain a gofastr-plugins reference. If one
// survives — say a future plugin imports a sibling plugin's package, which the
// two fixed rewrites above do not cover — the whole plan fails rather than ship
// a vendored copy that still reaches back into this repo. That reachability is
// exactly what "ejected code depends on nothing from here" exists to prevent.
func ensureNoGofastrPluginsRefs(files []File) error {
	const needle = SourceModulePath + "/"
	for _, f := range files {
		if !strings.HasSuffix(f.Src, ".go") {
			continue
		}
		if bytes.Contains(f.Content, []byte(needle)) {
			return fmt.Errorf("eject: %s still references %s after rewrite; "+
				"add a rewrite rule or drop the import", f.Rel, needle)
		}
	}
	return nil
}
