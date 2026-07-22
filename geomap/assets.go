package geomap

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assetsFS embed.FS

//go:embed host/adapter.js
var adapterJSBytes []byte

// framedAssets returns the embedded frame bundle as a sub-FS rooted at the
// plugin's assets/ directory. The asset server serves map.html / map.js /
// map.css from here with the framed CSP/CORP relaxation the opaque-origin
// iframe requires.
func framedAssets() fs.FS {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("geomap: embed assets sub: " + err.Error())
	}
	return sub
}
