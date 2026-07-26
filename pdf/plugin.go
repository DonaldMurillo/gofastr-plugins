// Package pdf is the SPIKE plugin that proves pdf.js can render inside GoFastr's
// opaque-origin sandboxed iframe under the fixed framed CSP
// (connect-src 'none'; no blob: worker-src; no 'unsafe-eval').
//
// It is deliberately the thinnest possible thing on the mermaid shape: a framed
// asset server for viewer.html/js/css, the platform broker route, a host adapter
// that fetches the same-origin sample PDF and forwards its bytes OVER THE BRIDGE
// (the frame itself has connect-src 'none' and fetches nothing), and a demo page
// that mounts the marker. pdf.js runs WORKER-FREE on the main thread
// (globalThis.pdfjsWorker.WorkerMessageHandler).
package pdf

import (
	"context"
	"net/http"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js hard-code
// these exactly (protocol-v1.md §2/§10). The demo lives at /pdf.
const (
	Name             = "pdf"
	Version          = "0.1.0-spike"
	RoutePrefix      = "/__gofastr/plugin/pdf"
	ViewerHTMLURL    = RoutePrefix + "/viewer.html"
	ViewerJSURL      = RoutePrefix + "/viewer.js"
	ViewerCSSURL     = RoutePrefix + "/viewer.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	SamplePDFURL     = RoutePrefix + "/sample.pdf"
	SaveURL          = RoutePrefix + "/save"
	DemoURL          = "/pdf"
	SchemaVersion    = "pdf-v1"

	defaultDocID     = "demo"
	defaultMinHeight = "480px"
)

// DefaultCapabilities is the grant set advertised to the viewer. The spike is
// read-only for rendering, but document:write is advertised so the (no-op) save
// path mirrors the platform contract; theme:read bridges tokens.
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

// Plugin is the PDF viewer spike plugin. It implements framework.Plugin.
type Plugin struct {
	manifest     pluginhost.Manifest
	capabilities []string
	devGrantAll  bool
	withDemoPage bool

	mu          sync.Mutex
	docs        map[string]savedDoc
	saveHandler func(ctx context.Context, req SaveRequest) error
}

type savedDoc struct{ Source string }

// Option configures a Plugin.
type Option func(*Plugin)

// WithDevGrantAll short-circuits the capability gate (Phase-0 demo / tests).
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithCapabilities overrides the default capability grant set.
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage serves the self-contained themed demo at DemoURL.
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithSaveHandler installs a custom save sink (tests can observe saves).
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// New constructs a Plugin. The platform manifest is built and Validate()'d here
// so a bad isolation/sandbox config aborts construction rather than silently
// de-opaquing the frame at runtime.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]savedDoc),
		manifest: pluginhost.Manifest{
			Entry:        ViewerHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "PDF viewer",
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	if len(p.capabilities) == 0 {
		p.capabilities = DefaultCapabilities()
	}
	p.manifest.Capabilities = p.capabilities
	if p.saveHandler == nil {
		p.saveHandler = p.memSave
	}
	if err := p.manifest.Validate(); err != nil {
		panic("pdf: invalid manifest: " + err.Error())
	}
	return p
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Init registers every asset and RPC route on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document + its JS/CSS sub-resources.
	// AssetServer applies the framing/CORP/CSP relaxation (DECISIONS.md gotcha #1)
	// and the fixed framedCSP (connect-src 'none', etc.) to exactly these.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "viewer.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "viewer.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "viewer.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page adapter (non-framed: same-origin fetch by the host, no CORP relax).
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	// The SPIKE sample PDF — fetched same-origin by the host adapter and forwarded
	// over postMessage. Non-framed (the frame never loads it directly).
	srv.AddBytes(SamplePDFURL, "application/pdf", false, samplePDFBytes())
	srv.Register(rt)

	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))

	if p.withDemoPage {
		rt.Get(DemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The demo page is a top-level document; carry a plain app CSP so the
			// framed child (its own framedCSP) and the host scripts load cleanly.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}

// LoadDoc returns the last-saved doc for docID (demo round-trip).
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (source string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, found := p.docs[docID]
	return d.Source, found
}

func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}
func (p *Plugin) allow(r *http.Request, capability string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so BOTH
		// gate sides (plugin grant AND caller authority) are skipped. Production
		// hosts never set this; the default-deny gate below rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, capability)
}

func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs[req.DocID] = savedDoc{Source: req.Source}
	return nil
}

// UIHostOption injects the platform broker and this plugin's adapter. Pass to
// framework.NewApp(uihost.WithExtraScripts…) or the equivalent app option.
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, AdapterScriptURL)
}

// Mount renders the generic mount marker. The host adapter does not need a hidden
// field for the spike (read-only), but the marker still carries the doc id.
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.MinHeight == "" {
		cfg.MinHeight = defaultMinHeight
	}
	return pluginhost.MountMarker(pluginhost.MountConfig{
		Plugin:    Name,
		DocID:     cfg.DocID,
		MinHeight: cfg.MinHeight,
		Doc:       cfg.Doc,
	})
}

// MountConfig is the pdf-specific mount configuration (a thin wrapper over the
// generic pluginhost.MountConfig).
type MountConfig struct {
	DocID     string
	MinHeight string
	Doc       string
}

// SaveRequest is the PDF save envelope (mirrors the platform shape; the spike
// store is in-memory).
type SaveRequest struct {
	DocID         string
	Source        string
	SchemaVersion string
}
