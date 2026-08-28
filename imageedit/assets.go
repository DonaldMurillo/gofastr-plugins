package imageedit

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
		panic("imageedit: embed assets sub: " + err.Error())
	}
	return sub
}
