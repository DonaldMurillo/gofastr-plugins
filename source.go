// Package gofastrplugins embeds the plugin source tree that cmd/gofastr-plugin
// vendors into a consumer's repo, so the eject CLI is hermetic: no network, no
// module-cache archaeology, no `go` subprocess at eject time. The CLI reads
// every byte it writes from this package.
//
// It lives at the REPO ROOT for one reason: `//go:embed` can only reach paths
// inside the directory of the .go file that carries the directive (or below
// it), never a sibling or a parent. The repo root is the only directory that
// contains every plugin package, so it is the only place a single embed can
// reach richtext/, mermaid/, monaco/, pdf/, tour/, and geomap/ at once.
//
// Nothing else should import this package. It is roughly ten megabytes of
// embedded bundles and exists for the eject CLI alone; pulling it into a binary
// that does not eject would bloat that binary for nothing.
package gofastrplugins

import (
	"embed"
	"io/fs"
	"regexp"
	"strings"
)

// ModulePath is this repo's own Go module path — the import-prefix every
// ejected file rewrites away. It is a const (not parsed from go.mod at runtime)
// because it is structural: the embed patterns below are written against it,
// the lock file records it as "source", and the import rewriter anchors on it.
// If the module were ever renamed, every one of those would need to change in
// the same commit, so a literal that fails to compile-check is the wrong shape.
const ModulePath = "github.com/DonaldMurillo/gofastr-plugins"

// The embed patterns are enumerated per-plugin rather than swept up with a bare
// directory or `all:` pattern for one reason: node_modules. Several plugin js/
// trees grow a node_modules dir during development (tour/js/node_modules alone
// is ~100 MB), and a directory embed would silently pull it into the binary.
// Each pattern below is narrow enough that node_modules can never match it, and
// source_test.go asserts no embedded path contains "node_modules" so a future
// greedy pattern is caught at test time rather than ship time.
//
// Every pattern is also load-bearing against a second Go embed rule: a pattern
// that matches zero files is a compile error. So a directory a plugin does NOT
// have (tour and geomap have no host/ or js/frame/) is simply absent below
// rather than present-and-empty — adding it would break the build.
//
//go:embed plugins.json
//go:embed go.mod
//go:embed mermaid/*.go
//go:embed mermaid/assets
//go:embed mermaid/host
//go:embed mermaid/js/build.mjs
//go:embed mermaid/js/package.json
//go:embed mermaid/js/package-lock.json
//go:embed mermaid/js/tsconfig.json
//go:embed mermaid/js/src
//go:embed mermaid/js/frame
//go:embed richtext/*.go
//go:embed richtext/assets
//go:embed richtext/host
//go:embed richtext/ssr
//go:embed richtext/highlight-cases.json
//go:embed richtext/js/build.mjs
//go:embed richtext/js/package.json
//go:embed richtext/js/package-lock.json
//go:embed richtext/js/tsconfig.json
//go:embed richtext/js/sh.mjs
//go:embed richtext/js/src
//go:embed richtext/js/frame
//go:embed richtext/js/test
//go:embed monaco/*.go
//go:embed monaco/assets
//go:embed monaco/host
//go:embed monaco/js/build.mjs
//go:embed monaco/js/package.json
//go:embed monaco/js/package-lock.json
//go:embed monaco/js/tsconfig.json
//go:embed monaco/js/src
//go:embed monaco/js/frame
//go:embed pdf/*.go
//go:embed pdf/assets
//go:embed pdf/host
//go:embed pdf/js/build.mjs
//go:embed pdf/js/package.json
//go:embed pdf/js/package-lock.json
//go:embed pdf/js/tsconfig.json
//go:embed pdf/js/src
//go:embed pdf/js/frame
//go:embed pdf/js/scripts
//go:embed datagrid/*.go
//go:embed datagrid/assets
//go:embed datagrid/host
//go:embed datagrid/js/build.mjs
//go:embed datagrid/js/package.json
//go:embed datagrid/js/package-lock.json
//go:embed datagrid/js/tsconfig.json
//go:embed datagrid/js/src
//go:embed datagrid/js/frame
//go:embed tour/*.go
//go:embed tour/assets
//go:embed tour/js/build.mjs
//go:embed tour/js/package.json
//go:embed tour/js/package-lock.json
//go:embed tour/js/tsconfig.json
//go:embed tour/js/src
//go:embed geomap/*.go
//go:embed geomap/assets
//go:embed geomap/js/build.mjs
//go:embed geomap/js/package.json
//go:embed geomap/js/package-lock.json
//go:embed geomap/js/tsconfig.json
//go:embed geomap/js/src
//go:embed chart/*.go
//go:embed chart/assets
//go:embed chart/host
//go:embed chart/js/build.mjs
//go:embed chart/js/package.json
//go:embed chart/js/package-lock.json
//go:embed chart/js/tsconfig.json
//go:embed chart/js/src
//go:embed chart/js/frame
//go:embed logstream/*.go
//go:embed logstream/assets
//go:embed logstream/host
//go:embed logstream/js/build.mjs
//go:embed logstream/js/package.json
//go:embed logstream/js/package-lock.json
//go:embed logstream/js/tsconfig.json
//go:embed logstream/js/src
//go:embed logstream/js/frame
//go:embed genui/*.go
//go:embed genui/assets
//go:embed genui/host
//go:embed genui/js/build.mjs
//go:embed genui/js/package.json
//go:embed genui/js/package-lock.json
//go:embed genui/js/tsconfig.json
//go:embed genui/js/src
//go:embed genui/js/frame
//go:embed scanner/*.go
//go:embed scanner/assets
//go:embed scanner/host
//go:embed scanner/js/build.mjs
//go:embed scanner/js/package.json
//go:embed scanner/js/package-lock.json
//go:embed scanner/js/tsconfig.json
//go:embed scanner/js/src
//go:embed scanner/js/frame
//go:embed scanner/js/scripts
//go:embed formbuilder/*.go
//go:embed formbuilder/assets
//go:embed formbuilder/host
//go:embed formbuilder/js/build.mjs
//go:embed formbuilder/js/package.json
//go:embed formbuilder/js/package-lock.json
//go:embed formbuilder/js/tsconfig.json
//go:embed formbuilder/js/src
//go:embed formbuilder/js/frame
//go:embed chart/ssr
//go:embed imageedit/*.go
//go:embed imageedit/assets
//go:embed imageedit/host
//go:embed imageedit/js/build.mjs
//go:embed imageedit/js/package.json
//go:embed imageedit/js/package-lock.json
//go:embed imageedit/js/tsconfig.json
//go:embed imageedit/js/src
//go:embed imageedit/js/frame
//go:embed calendar/*.go
//go:embed calendar/assets
//go:embed calendar/host
//go:embed calendar/js/build.mjs
//go:embed calendar/js/package.json
//go:embed calendar/js/package-lock.json
//go:embed calendar/js/tsconfig.json
//go:embed calendar/js/src
//go:embed calendar/js/frame
//go:embed whiteboard/*.go
//go:embed whiteboard/assets
//go:embed whiteboard/host
//go:embed whiteboard/js/build.mjs
//go:embed whiteboard/js/package.json
//go:embed whiteboard/js/package-lock.json
//go:embed whiteboard/js/tsconfig.json
//go:embed whiteboard/js/src
//go:embed whiteboard/js/frame
var bundled embed.FS

//go:embed plugins.json
var registryJSON []byte

// RegistryJSON is the embedded curated index (plugins.json). It is the same
// bytes a host would fetch from the release asset — eject reuses it rather than
// re-deriving plugin metadata, so the registry the CLI sees and the registry a
// consumer vendors are always byte-identical.
func RegistryJSON() []byte { return registryJSON }

// Source returns the embedded upstream tree as a read-only fs.FS rooted so that
// "mermaid/plugin.go" or "richtext/ssr/render.go" resolve directly. The CLI
// reads from it; nothing writes to it.
func Source() fs.FS {
	// fs.Sub with "." hands back a plain fs.FS (dropping embed.FS's read-write
	// surface), enforcing the read-only contract at the type level.
	sub, err := fs.Sub(bundled, ".")
	if err != nil {
		// "." is always a valid sub-root for an embed.FS, so this is unreachable
		// in practice — but a panic here is far better than a nil dereference
		// downstream if the embed ever came back empty.
		panic("gofastrplugins: embed sub-root failed: " + err.Error())
	}
	return sub
}

// goMod is the embedded go.mod. It is the source of GoFastrVersion below —
// parsed at init so the version the CLI reports can never drift from the
// version the embedded bundles actually built against.
//
//go:embed go.mod
var goMod []byte

// gofastrVersionRE matches the gofastr require line in either of go.mod's two
// forms — single-line (`require X v`) or block (`require (\n X v\n ...)`). It
// captures the version token. The first match wins, which is the direct
// require; indirect lines live in a second block and use `// indirect`.
var gofastrVersionRE = regexp.MustCompile(`github\.com/DonaldMurillo/gofastr (v[0-9][^\s]+)`)

// GoFastrVersion is the published gofastr release this repo pins in go.mod —
// the version a consumer must `go get` to compile an ejected plugin, since the
// rewrite points every pluginhost import at it. Parsed once from the embedded
// go.mod at package init. Empty means the embedded go.mod did not match (a
// malformed module file); the CLI treats empty as "could not determine" and
// says so rather than guessing a version.
var GoFastrVersion = parseGoFastrVersion(goMod)

func parseGoFastrVersion(goMod []byte) string {
	m := gofastrVersionRE.FindSubmatch(goMod)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// goDirectiveRE captures the language version from go.mod's `go` directive.
var goDirectiveRE = regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)\s*$`)

// GoVersion is the Go language version the embedded sources require, read from
// the same go.mod. A consumer whose own `go` directive is older than this gets
// a genuinely confusing failure — the build stops on a TOOLCHAIN resolution
// error naming a transitive gofastr package ("toolchain upgrade needed to
// resolve .../core-ui/style"), which reads as a broken dependency rather than
// as "raise your go directive". Go will not auto-upgrade the toolchain for a
// module that asks for less than it needs, so nothing self-corrects.
//
// Surfacing the floor next to the `go get` is the cheap fix: the operator owns
// the go.mod edit, and this is the number they need to make it.
var GoVersion = parseGoVersion(goMod)

func parseGoVersion(goMod []byte) string {
	m := goDirectiveRE.FindSubmatch(goMod)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}
