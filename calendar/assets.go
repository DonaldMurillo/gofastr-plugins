package calendar

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assetsFS embed.FS

//go:embed host/adapter.js
var adapterJSBytes []byte

func framedAssets() fs.FS {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("calendar: embed assets sub: " + err.Error())
	}
	return sub
}
