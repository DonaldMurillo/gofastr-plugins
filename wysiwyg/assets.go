package wysiwyg

import (
	"embed"
	"io/fs"
)

// Embedded client assets. editor.html / editor.js / editor.css are produced by
// the editor build (Worker A/D) and live under assets/; host/broker.js is this
// plugin's adapter over the generic platform broker. All are served same-origin
// so the host's strict CSP is satisfied by default-src 'self' with zero core
// CSP edits.
//
// The framed editor assets and the host-page broker adapter are now served by
// the platform [pluginhost.AssetServer], which owns the framing/CORP/CSP header
// relaxation (the client-side isolation contract). This file only owns the
// embeds.
//
//go:embed assets
var assetsFS embed.FS

//go:embed host/broker.js
var brokerJSBytes []byte

// framedAssets returns the embedded editor asset filesystem (the assets/*
// subtree) for the platform AssetServer. The path is compile-time fixed by
// go:embed, so fs.Sub only fails on a build mistake — panic surfaces it loudly.
func framedAssets() fs.FS {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wysiwyg: embed assets sub: " + err.Error())
	}
	return sub
}
