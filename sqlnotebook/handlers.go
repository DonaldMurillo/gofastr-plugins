package sqlnotebook

import (
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
)

// handlers.go is route registration and nothing else. The wire protocol
// (sqlnb/init, sqlnb/query, sqlnb/ready, sqlnb/result, sqlnb/error) runs
// entirely over postMessage between the host adapter and the frame, and the
// notebook persists nothing server-side, so there are no RPC routes to gate.
// What remains is the one load-bearing registration this plugin exists to
// prove: the AssetServer must be handed the manifest's CSP, or the frame
// gets a policy without 'wasm-unsafe-eval' and SQLite silently refuses to
// compile (gofastr#300 — the manifest validating proves nothing; the SERVED
// header is the contract, and plugin_test.go asserts it).

// Init registers every asset route (and the demo page, when requested) on
// the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document, the notebook bundle,
	// and the sql.js engine pair. AssetServer applies the framing/CORP/CSP
	// relaxation and the fixed framedCSP (connect-src 'none', sandbox
	// allow-scripts) to exactly these.
	//
	// Built FROM THE MODULE, which is gofastr#300's fix (v0.75.0): the
	// constructor reads the module's assets and threads Manifest.CSP through
	// WithCSP itself, so the manifest that declares the wasm tier and the
	// server that answers for the frame cannot disagree. The hand-threaded
	// NewAssetServer(...).WithCSP(...) this replaces was correct, but it was
	// correct by remembering a line, and forgetting it produced a frame that
	// validated, mounted, and refused to compile WebAssembly with nothing
	// naming the cause.
	mod := pluginhost.ClientModule{
		Name:     Name,
		Manifest: p.manifest,
		Assets:   framedAssets(),
	}
	srv := mod.AssetServer(RoutePrefix, assetSpecs())

	// Host-page (non-framed) script: the adapter (embedded in assets.go, the
	// same treatment pdf and calendar give theirs). No config.js — the only
	// instance state, the seed, rides the mount marker's data-fui-plugin-doc
	// attribute, which is the channel the adapter reads.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.Register(rt)

	if p.withDemoPage {
		rt.Get(DemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The demo page is a top-level document; carry a plain app CSP so
			// the framed child (its own framedCSP) and the host scripts load
			// cleanly.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}
