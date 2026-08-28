// Package chart is the chart plugin: one canonical chart-v1 spec, two
// renderers that must agree. The server renders a static SVG (chart/ssr)
// so the page works with JavaScript off; the sandboxed Observable Plot
// frame hydrates it into an interactive chart. The host adapter hides the
// SSR node when the frame reports ready — the decision that keeps this
// plugin core-free (pluginhost.MountConfig has no initial-children slot,
// and none was added).
package chart

import (
	"context"
	"net/http"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/chart/ssr"
	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. The Go plugin, host/adapter.js, and the
// frame bundle hard-code these exactly. The demo lives at /chart.
const (
	Name             = "chart"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/chart"
	ChartHTMLURL     = RoutePrefix + "/chart.html"
	ChartJSURL       = RoutePrefix + "/chart.js"
	ChartCSSURL      = RoutePrefix + "/chart.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	SaveURL          = RoutePrefix + "/save"
	DemoURL          = "/chart"
	SchemaVersion    = ssr.SchemaVersion

	defaultDocID     = "demo"
	defaultSpecField = "chart_spec"
	defaultMinHeight = "360px"
)

// DefaultCapabilities is the grant set: no uploads, no network — document
// read/write plus theme bridging is the whole surface.
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

// savedDoc is the canonical, NORMALIZED spec JSON for one docID. Saving a
// normalized form means LoadDoc → Mount → ssr.Render can never hit an
// invalid-spec path after a successful save.
type savedDoc struct {
	Spec []byte
}

// Plugin is the chart plugin. It implements framework.Plugin.
type Plugin struct {
	devGrantAll  bool
	withDemoPage bool
	capabilities []string
	saveHandler  func(ctx context.Context, req SaveRequest) error
	manifest     pluginhost.Manifest
	mu           sync.RWMutex
	docs         map[string]savedDoc
}

type Option func(*Plugin)

func WithDevGrantAll() Option {
	return func(p *Plugin) { p.devGrantAll = true }
}

func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

func WithDemoPage() Option {
	return func(p *Plugin) { p.withDemoPage = true }
}

func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// New constructs a Plugin. The platform manifest is built and Validate()'d
// here so a bad isolation/sandbox config aborts construction rather than
// silently de-opaquing the frame at runtime.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]savedDoc),
		manifest: pluginhost.Manifest{
			Entry:        ChartHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Chart",
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
		panic("chart: invalid manifest: " + err.Error())
	}
	return p
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Init registers every asset and RPC route on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt)
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "chart.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "chart.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "chart.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.Register(rt)
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))
	if p.withDemoPage {
		rt.Get(DemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}

// LoadDoc returns the last-saved normalized spec JSON for docID.
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (spec []byte, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	d, found := p.docs[docID]
	if !found {
		return nil, false
	}
	return d.Spec, true
}

func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

func (p *Plugin) allow(r *http.Request, cap string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so
		// BOTH gate sides (plugin grant AND caller authority) are skipped.
		// Production hosts never set this; the default-deny gate below rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, cap)
}

func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs[req.DocID] = savedDoc{Spec: req.Spec}
	return nil
}

// UIHostOption injects the platform broker and this plugin's adapter.
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, AdapterScriptURL)
}

// MountConfig configures Mount.
type MountConfig struct {
	// DocID is the persistence key.
	DocID string
	// SpecField is the hidden input name that carries the spec JSON.
	SpecField string
	// MinHeight is the initial iframe height before first resize.
	MinHeight string
	// Spec is the canonical chart JSON. It is rendered server-side into
	// the SSR wrapper AND placed on the marker for the frame's init.
	Spec []byte
}

// Mount renders the SSR chart in a wrapper element followed by the normal
// mount marker and hidden field. With JavaScript off the SSR SVG is the
// page; with JavaScript on, host/adapter.js hides the wrapper when the
// frame reports ready (and un-hides it if the frame fails to boot).
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.SpecField == "" {
		cfg.SpecField = defaultSpecField
	}
	if cfg.MinHeight == "" {
		cfg.MinHeight = defaultMinHeight
	}
	doc := string(cfg.Spec)
	marker := pluginhost.MountMarker(pluginhost.MountConfig{
		Plugin:    Name,
		DocID:     cfg.DocID,
		MinHeight: cfg.MinHeight,
		Doc:       doc,
		Attributes: []pluginhost.Attribute{
			{Name: "data-fui-plugin-for", Value: cfg.SpecField},
		},
		Fields: []pluginhost.Field{
			{Name: cfg.SpecField, Value: doc},
		},
	})
	return render.HTML(`<div class="gofastr-chart-ssr" data-fui-chart-ssr>`+string(ssrNode(cfg.Spec))+`</div>`) + marker
}

// ssrNode renders the spec to SVG when valid, or a visible, escaped
// placeholder when it is not — an invalid saved spec must not take the
// whole page down, and must not mount silently blank either.
func ssrNode(raw []byte) render.HTML {
	if len(raw) == 0 {
		return render.HTML(`<div class="gofastr-chart-empty" role="note">No chart saved yet.</div>`)
	}
	spec, err := ssr.ParseSpec(raw)
	if err != nil {
		return render.HTML(`<div class="gofastr-chart-error" role="alert">Chart spec invalid: ` +
			string(render.Text(err.Error())) + `</div>`)
	}
	return ssr.Render(spec)
}

type SaveRequest struct {
	DocID         string
	Spec          []byte // normalized canonical spec JSON
	SchemaVersion string
}
