package mermaid

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
// these exactly. The demo lives at /mermaid (not "/", which richtext owns) so
// both plugins co-mount.
const (
	Name             = "mermaid"
	Version          = "0.1.0-phase0"
	RoutePrefix      = "/__gofastr/plugin/mermaid"
	DiagramHTMLURL   = RoutePrefix + "/diagram.html"
	DiagramJSURL     = RoutePrefix + "/diagram.js"
	DiagramCSSURL    = RoutePrefix + "/diagram.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	SaveURL          = RoutePrefix + "/save"
	DemoURL          = "/mermaid"
	SchemaVersion    = "mermaid-v1"

	defaultDocID       = "demo"
	defaultSourceField = "diagram_source"
	defaultMinHeight   = "320px"
)

// DefaultCapabilities is the grant set advertised to the editor. Mermaid has no
// upload path -- only document read/write + theme:read.
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

type savedDoc struct{ Source string }

// Plugin is the Mermaid diagram plugin. It implements framework.Plugin.
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

// New constructs a Plugin. The platform manifest is built and Validate()'d here
// so a bad isolation/sandbox config aborts construction rather than silently
// de-opaquing the frame at runtime.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]savedDoc),
		manifest: pluginhost.Manifest{
			Entry:        DiagramHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Mermaid diagram editor",
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
		panic("mermaid: invalid manifest: " + err.Error())
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
		{Name: "diagram.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "diagram.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "diagram.css", ContentType: "text/css; charset=utf-8", Framed: true},
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

// LoadDoc returns the last-saved diagram source for docID.
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (source string, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	d, found := p.docs[docID]
	if !found {
		return "", false
	}
	return d.Source, true
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
	p.docs[req.DocID] = savedDoc{Source: req.Source}
	return nil
}

// UIHostOption injects the platform broker and this plugin's adapter.
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, AdapterScriptURL)
}

type MountConfig struct {
	DocID       string
	SourceField string
	MinHeight   string
	Doc         string
}

// Mount renders the mount marker div plus the hidden input the host adapter
// syncs on docChanged.
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.SourceField == "" {
		cfg.SourceField = defaultSourceField
	}
	if cfg.MinHeight == "" {
		cfg.MinHeight = defaultMinHeight
	}
	return pluginhost.MountMarker(pluginhost.MountConfig{
		Plugin:    Name,
		DocID:     cfg.DocID,
		MinHeight: cfg.MinHeight,
		Doc:       cfg.Doc,
		Attributes: []pluginhost.Attribute{
			{Name: "data-fui-plugin-for", Value: cfg.SourceField},
		},
		Fields: []pluginhost.Field{
			{Name: cfg.SourceField},
		},
	})
}

type SaveRequest struct {
	DocID         string
	Source        string
	SchemaVersion string
}
