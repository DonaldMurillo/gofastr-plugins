// Package formbuilder is the GoFastr form-builder plugin: an authoring tool
// for the framework itself, not another content editor.
//
// Every other plugin in this repo edits content — a document, a diagram, a
// chart spec, a table view. This one produces a FORM SCHEMA (formbuilder-v1)
// that GoFastr's own ui.Form renders and that the HOST re-validates on every
// submit: a form designed in an opaque-origin sandboxed iframe is enforced by
// the server with no client trust anywhere in the path. That round trip —
// design in the cage, enforcement in Go — is the entire argument; the demo's
// live-form route closes it visibly.
//
// The canonical doc is the schema and it is DATA ONLY: {version, fields[]},
// never markup (see schema.go — a label containing "<" is refused at save).
// The Go side validates every save and refuses bad schemas with specific 400
// codes: unknown field type, duplicate/empty/invalid name, malformed rule,
// unknown version, markup. A schema that somehow gets past the frame still
// gets refused here.
//
// Capabilities: document:read, document:write, theme:read (all always-on;
// there are no optional grants). pluginhost.Allow is a capability gate, NOT
// authentication: it passes for anonymous callers, so POST /save must be
// documented as requiring the host to check the session in its own handler —
// the demo's WithDevGrantAll skips the gate entirely and MUST NOT survive
// into a production mount. See docs/formbuilder.md.
package formbuilder

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
// these exactly (protocol-v1.md §2/§10). The design demo lives at /formbuilder;
// the live-form proof route at /formbuilder/live.
//
// These mirror the formbuilder row in plugins.json; internal/registry tests
// pin Name + RoutePrefix against that row, so they MUST NOT drift.
const (
	Name             = "formbuilder"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/formbuilder"
	BuilderHTMLURL   = RoutePrefix + "/builder.html"
	BuilderJSURL     = RoutePrefix + "/builder.js"
	BuilderCSSURL    = RoutePrefix + "/builder.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	SaveURL          = RoutePrefix + "/save"
	DemoURL          = "/formbuilder"
	LiveURL          = "/formbuilder/live"
	SchemaVersion    = "formbuilder-v1"

	defaultDocID     = "demo"
	defaultDocField  = "formbuilder_doc"
	defaultMinHeight = "560px"
)

// DefaultCapabilities is the always-on grant set: reading and writing the
// schema document plus theme bridging. document:write gates POST /save.
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

// SaveRequest is the schema persist signal handed to the save handler.
type SaveRequest struct {
	DocID         string
	Doc           Doc
	DocJSON       string
	SchemaVersion string
}

// savedDoc is the in-memory persisted schema (the demo / default store).
type savedDoc struct {
	DocJSON string
}

// Plugin is the form-builder plugin. It implements [framework.Plugin] and
// mirrors the mermaid/datagrid shape: opaque-origin sandboxed iframe,
// protocol v1 over postMessage, go:embed'd frame bundle, capability gate,
// one host-side RPC route (/save). The difference is what the doc IS: not
// content to display but a schema the server consumes and enforces.
type Plugin struct {
	capabilities []string
	manifest     pluginhost.Manifest
	devGrantAll  bool
	withDemoPage bool
	demoRoute    string
	demoDoc      *Doc
	saveHandler  func(ctx context.Context, req SaveRequest) error

	mu   sync.RWMutex
	docs map[string]savedDoc
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithSaveHandler overrides the schema persistence hook. The default stores
// the validated canonical doc JSON in an in-memory map keyed by DocID. This
// is the host's chance to authorize the write against the real session:
// pluginhost.Allow is a capability gate, not authentication.
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// WithDemoDoc sets the schema the demo page mounts when nothing has been
// saved yet — the starting canvas the visitor edits.
func WithDemoDoc(doc Doc) Option {
	return func(p *Plugin) { d := doc; p.demoDoc = &d }
}

// WithDevGrantAll short-circuits the capability gate (demo / tests only).
// It bypasses the gate on POST /save; the route still fails closed on an
// unwired handler.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithCapabilities overrides the grant set advertised to the builder.
// Default: [DefaultCapabilities]. Grants are matched with the framework's
// wildcard scope grammar at runtime.
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage registers the self-contained themed demo page at [DemoURL]
// AND the live-form proof route at [LiveURL] (GET renders the saved schema
// through ui.Form; POST validates in Go and answers).
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithDemoRoute overrides where [WithDemoPage] mounts the design demo
// (default [DemoURL], "/formbuilder"). The live route stays at [LiveURL].
func WithDemoRoute(path string) Option { return func(p *Plugin) { p.demoRoute = path } }

// New constructs the plugin.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]savedDoc),
		manifest: pluginhost.Manifest{
			Entry:        BuilderHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Form builder",
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
	if p.saveHandler == nil {
		p.saveHandler = p.memSave
	}
	if p.demoDoc == nil {
		p.demoDoc = &defaultDemoDoc
	}
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("formbuilder: invalid manifest: " + err.Error())
	}
	return p
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Init registers every asset and RPC route on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document + its JS/CSS
	// sub-resources. AssetServer applies the framing/CORP/CSP relaxation
	// and the fixed framedCSP (connect-src 'none', sandbox allow-scripts)
	// to exactly these — the frame cannot save the schema itself; every
	// save crosses the bridge.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "builder.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "builder.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "builder.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) script: the adapter.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.Register(rt)

	// RPC route: the schema save signal. /save validates in Go and refuses
	// bad schemas; see handlers.go.
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))

	if p.withDemoPage {
		demoRoute := p.demoRoute
		if demoRoute == "" {
			demoRoute = DemoURL
		}
		demoCSP := "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'"
		rt.Get(demoRoute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The design page is a top-level document with inline <style>/
			// <script> (theme toggle + live readout — the same demo-only
			// allowance richtext/datagrid make); carry a plain app CSP so the
			// framed child and host scripts load cleanly.
			w.Header().Set("Content-Security-Policy", demoCSP)
			render.RespondHTML(w, p.renderDemo(r))
		}))
		// The live-form proof route: GET renders the saved schema through
		// ui.Form; POST validates the submission in Go and answers. Same CSP
		// shape (the page inlines its shell + component styles).
		rt.Get(LiveURL, http.HandlerFunc(p.handleLive))
		rt.Post(LiveURL, http.HandlerFunc(p.handleLive))
	}
	return nil
}

// LoadDoc returns the last-saved schema JSON for docID (demo round-trip).
// ok is false when the doc has never been saved.
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (docJSON string, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	d, found := p.docs[docID]
	if !found {
		return "", false
	}
	return d.DocJSON, true
}

// Capabilities returns the grant set this plugin advertises to the builder.
func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

// allow is the capability gate, delegated to the platform [pluginhost.Allow]:
// default-deny against the plugin's granted set, intersected with the caller's
// own authority (a scoped token restricts below the grant; a session caller is
// bound by the grant alone). It is NOT authentication — see the package doc.
func (p *Plugin) allow(r *http.Request, capability string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so
		// BOTH gate sides are skipped. Production hosts never set this; the
		// default-deny gate below rules. The route still fails closed on an
		// unwired handler.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, capability)
}

// memSave is the default persistence hook: an in-memory map keyed by DocID,
// storing the validated canonical doc JSON /save produced.
func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs[req.DocID] = savedDoc{DocJSON: req.DocJSON}
	return nil
}

// UIHostOption injects the platform broker, then this plugin's adapter (the
// adapter registers with the broker the former defines).
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, AdapterScriptURL)
}

// MountConfig configures [Mount].
type MountConfig struct {
	// DocID is the persistence key for the schema doc. Defaults to "demo".
	DocID string
	// Field is the hidden input name the adapter mirrors the schema JSON
	// into, so a normal form POST round-trips it. Defaults to
	// "formbuilder_doc".
	Field string
	// Doc is optional initial schema JSON server-rendered into the marker
	// (data-fui-plugin-doc) for reload round-trip.
	Doc string
	// MinHeight is the initial iframe height. Defaults to 560px.
	MinHeight string
}

// Mount renders the generic mount marker plus the hidden input the adapter
// syncs on docChanged. Drop it into a form. All interpolated values are
// HTML-escaped inside [pluginhost.MountMarker].
func Mount(cfg MountConfig) render.HTML {
	field := cfg.Field
	if field == "" {
		field = defaultDocField
	}
	return pluginhost.MountMarker(pluginhost.MountConfig{
		Plugin:     Name,
		DocID:      cfg.DocID,
		MinHeight:  cfg.MinHeight,
		Doc:        cfg.Doc,
		Attributes: []pluginhost.Attribute{{Name: "data-fui-plugin-field", Value: field}},
		Fields:     []pluginhost.Field{{Name: field}},
	})
}
