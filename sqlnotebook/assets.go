package sqlnotebook

import (
	"embed"
	"io/fs"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// assetsFS holds the frame bundle: frame.html + notebook.js (built by
// `npm run build` in sqlnotebook/js) plus the two sql.js dist files copied
// verbatim (sql-wasm.js glue + sql-wasm.wasm engine, SQLite 3.49.1 as
// shipped by sql.js 1.14.2). The Go plugin go:embed's them and serves them
// same-origin via pluginhost.AssetServer.
//
// The glob (not the directory form pdf/calendar use) so a stray dotfile in
// assets/ can never ride into the binary unnoticed.
//
//go:embed assets/*
var assetsFS embed.FS

// adapterJSBytes is the host-page adapter: the privileged side that fetches
// the wasm bytes same-origin and relays them plus the seed into the frame
// over the postMessage bridge. It is NOT a framed asset (the host page is a
// first-class same-origin document; it needs no CORP/CSP relaxation), which
// is the exact treatment pdf and calendar give their adapters.
//
//go:embed host/adapter.js
var adapterJSBytes []byte

// framedAssets returns the embedded frame assets sub-tree. AssetServer serves
// each spec under RoutePrefix with the framed CSP/CORP relaxation.
func framedAssets() fs.FS {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("sqlnotebook: embed assets sub: " + err.Error())
	}
	return sub
}

// assetSpecs is the complete declaration of the framed bundle. EVERY spec
// carries an explicit ContentType: the platform sets X-Content-Type-Options
// nosniff unconditionally, so an empty type serves a 200 with correct bytes
// that the browser refuses to parse, with no error anywhere (gofastr#303).
// plugin_test.go walks the embedded tree against this list, so an asset
// added to assets/ without a spec here fails the build's tests rather than
// shipping unparsed.
//
// All four are Framed: true. Even the wasm, which the HOST adapter fetches
// (the frame cannot, and that is the point): framing it keeps the whole
// engine bundle under one policy, and the framed treatment on a bytes
// response costs nothing the host fetch does not already have.
func assetSpecs() []pluginhost.AssetSpec {
	return []pluginhost.AssetSpec{
		{Name: "frame.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "notebook.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "sql-wasm.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "sql-wasm.wasm", ContentType: "application/wasm", Framed: true},
	}
}
