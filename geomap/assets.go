package geomap

import (
	"embed"
)

// Embedded built runtime + overlay stylesheet. map.js is produced by `npm run
// build` under js/ (esbuild IIFE that scans for [data-fui-geomap] mount elements
// and renders a MapLibre map into each, bundling maplibre-gl + injecting its
// CSS); map.css is the token-only overlay stylesheet copied verbatim from
// js/src/map.css. Both are NON-framed host-page assets (trusted plugin — no
// sandboxed iframe), so they are served plain with correct Content-Types and no
// CORP relaxation.
//
// Rebuild after editing js/src: `cd geomap/js && npm run build`.
//
//go:embed assets/map.js
var mapJSBytes []byte

//go:embed assets/map.css
var mapCSSBytes []byte

// Silence the unused-import lint when embed's only role is the directives
// above: the directives ARE the use, but gofmt-go's vet is happy as-is.
var _ = embed.FS{}
