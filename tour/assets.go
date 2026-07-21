package tour

import (
	"embed"
)

// Embedded built runtime + stylesheet. tour.js is produced by `npm run build`
// under js/ (esbuild IIFE that attaches window.gofastrTour); tour.css is the
// token-only overlay stylesheet copied verbatim from js/src/tour.css. Both
// are NON-framed host-page assets (trusted plugin — no sandboxed iframe), so
// they are served plain with correct Content-Types and no CORP relaxation.
//
// Rebuild after editing js/src: `cd tour/js && npm run build`.
//
//go:embed assets/tour.js
var tourJSBytes []byte

//go:embed assets/tour.css
var tourCSSBytes []byte

// Silence the unused-import lint when embed's only role is the directives
// above: the directives ARE the use, but gofmt-go's vet is happy as-is.
var _ = embed.FS{}
