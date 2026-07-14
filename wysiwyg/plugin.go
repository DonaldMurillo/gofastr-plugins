package wysiwyg

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr-plugins/wysiwyg/ssr"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity + route constants (protocol-v1.md §2 / §10). Both this plugin
// and host/broker.js hard-code these exactly — they ARE the contract.
const (
	Name            = "wysiwyg"
	Version         = "0.1.0-phase0"
	RoutePrefix     = "/__gofastr/plugin/wysiwyg"
	EditorHTMLURL   = RoutePrefix + "/editor.html"
	EditorJSURL     = RoutePrefix + "/editor.js"
	EditorCSSURL    = RoutePrefix + "/editor.css"
	BrokerScriptURL = RoutePrefix + "/broker.js"
	SaveURL         = RoutePrefix + "/save"
	UploadURL       = RoutePrefix + "/upload"
	ReadURL         = RoutePrefix + "/read" // no-JS SSR read view (?doc=<id>)
	DemoURL         = "/"                   // self-contained themed demo page (only with WithDemoPage)

	// Trusted in-page mount (DECISIONS.md "secure by default, opt out"). These
	// routes exist ONLY when the host opts in via [WithTrustedMount].
	InlineJSURL    = RoutePrefix + "/editor-inline.js"  // window.__gofastrWysiwyg mount API
	ScopedCSSURL   = RoutePrefix + "/editor-scoped.css" // stylesheet rescoped under .gofastr-wysiwyg-trusted
	TrustedDemoURL = RoutePrefix + "/trusted"           // frameless demo page
	SchemaVersion   = "wysiwyg-v1"

	defaultDocID     = "demo"
	defaultJSONField = "body_json"
	defaultMDField   = "body_md"
	defaultMinHeight = "240px"
)

// DefaultCapabilities is the Phase-0 grant set advertised to the editor in
// init.capabilities when WithCapabilities is not used.
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "upload:images", "theme:read"}
}

// savedDoc is an in-memory persisted document.
type savedDoc struct {
	JSON string // canonical ProseMirror doc JSON (opaque blob)
	MD   string // lossy markdown export
}

// Plugin is the Phase-0 WYSIWYG plugin. It implements [framework.Plugin].
type Plugin struct {
	devGrantAll   bool
	withDemoPage  bool
	trustedMount  bool
	capabilities  []string
	saveHandler   func(ctx context.Context, req SaveRequest) error
	uploadHandler func(ctx context.Context, req UploadRequest) (UploadResult, error)

	mu   sync.RWMutex
	docs map[string]savedDoc // in-memory default store, keyed by DocID
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithDevGrantAll bypasses the auth.HasScope gate on save/upload so the
// Phase-0 demo runs without standing up auth. Default OFF (enforcing).
// Phase 1 removes this.
func WithDevGrantAll() Option {
	return func(p *Plugin) { p.devGrantAll = true }
}

// WithCapabilities overrides the grant set advertised to the editor in
// init.capabilities. Default: [DefaultCapabilities].
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option {
	return func(p *Plugin) { p.withDemoPage = true }
}

// WithTrustedMount OPTS OUT of the sandbox for this plugin: it serves the
// in-page editor bundle + scoped stylesheet and a frameless demo page at
// [TrustedDemoURL]. The editor then runs with FULL page access — no opaque
// origin, no capability boundary the browser enforces. Per docs/DECISIONS.md
// ("secure by default, opt out") this is never a default and never
// plugin-selectable: only the app owner compiles this option in, vouching for
// the plugin bundle and its dependency tree.
func WithTrustedMount() Option {
	return func(p *Plugin) { p.trustedMount = true }
}

// WithSaveHandler overrides the persistence hook. The default stores the
// canonical doc JSON + markdown in an in-memory map keyed by DocID.
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// WithUploadHandler overrides the upload hook. The default returns a data:
// URL echoing the bytes, enough to prove the round trip.
func WithUploadHandler(fn func(ctx context.Context, req UploadRequest) (UploadResult, error)) Option {
	return func(p *Plugin) { p.uploadHandler = fn }
}

// New constructs a [Plugin] with the given options. Unset options fall back
// to Phase-0 defaults so the demo and tests work with zero configuration.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]savedDoc),
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
	if p.uploadHandler == nil {
		p.uploadHandler = p.memUpload
	}
	return p
}

// Name implements [framework.Plugin].
func (p *Plugin) Name() string { return Name }

// Init implements [framework.Plugin]. It registers every asset and RPC route
// from protocol-v1.md §10 on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()

	// Serve the generic platform host broker once for the whole app (idempotent
	// across plugins). The wysiwyg adapter (host/broker.js) is injected
	// alongside it on host pages — see [UIHostOption] / the demo page.
	pluginhost.RegisterBrokerRoute(rt)

	// Client assets via the platform AssetServer (protocol-v1.md §2). Framed
	// editor assets carry the framing/CORP/CSP relaxation; the broker adapter
	// is a non-framed host-page script. Correct Content-Types; no-cache in dev.
	specs := []pluginhost.AssetSpec{
		{Name: "editor.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "editor.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "editor.css", ContentType: "text/css; charset=utf-8", Framed: true},
	}
	if p.trustedMount {
		// Host-page (non-framed) assets for the opt-out in-page mount.
		specs = append(specs,
			pluginhost.AssetSpec{Name: "editor-inline.js", ContentType: "text/javascript; charset=utf-8"},
			pluginhost.AssetSpec{Name: "editor-scoped.css", ContentType: "text/css; charset=utf-8"},
		)
	}
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, specs)
	srv.AddBytes(BrokerScriptURL, "text/javascript; charset=utf-8", false, brokerJSBytes)
	srv.Register(rt)

	// RPC routes gated by the capability helper (protocol-v1.md §5/§10).
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))
	rt.Post(UploadURL, http.HandlerFunc(p.handleUpload))

	// No-JS SSR read view: the saved canonical block-JSON rendered server-side to
	// design-token HTML (wysiwyg/ssr). Real content on first paint, no editor
	// iframe, no scripts — the portable, SEO-safe view the editor hydrates over.
	rt.Get(ReadURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The read view discloses stored document content, so it is gated on
		// document:read like every other doc route (an enforcing host wraps
		// these routes with auth; the demo's WithDevGrantAll opens them).
		if !p.allow(r, "document:read") {
			writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
			return
		}
		docID := r.URL.Query().Get("doc")
		if docID == "" {
			docID = defaultDocID
		}
		docJSON, ok := p.LoadDoc(r.Context(), docID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, err := ssr.RenderJSON(docJSON)
		if err != nil {
			http.Error(w, "read-view render error", http.StatusInternalServerError)
			return
		}
		// The read page inlines its stylesheet (no runtime to auto-load it) and
		// SSR emits inline style= color spans, so style-src needs 'unsafe-inline'.
		// It ships NO <script> (a pinned invariant), so script-src stays strict
		// 'self' — no reason to widen the one page that echoes user content.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'self'; base-uri 'self'")
		render.RespondHTML(w, p.renderReadPage(r, body))
	}))

	if p.withDemoPage {
		rt.Get(DemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The self-contained demo page carries an inline <style> (the bridged
			// design tokens) and a small inline <script> (the theme toggle), both
			// refused by the app's default strict CSP. Relax THIS page's CSP to
			// permit them; the editor frame and broker stay strict + external-only.
			// A production host would render the editor through a UIHost (nonce'd)
			// rather than a self-contained demo page.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}

	if p.trustedMount {
		rt.Get(TrustedDemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Same CSP relaxation rationale as the framed demo page above; the
			// frameless page additionally has no frame-src to grant.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderTrustedDemo(r))
		}))
	}
	return nil
}

// LoadDoc returns the last-saved canonical JSON for docID from the in-memory
// default store. ok is false when the doc has never been saved.
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (docJSON string, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	d, found := p.docs[docID]
	if !found || strings.TrimSpace(d.JSON) == "" {
		// An empty stored blob (e.g. a save of doc:null, normalized to "") is
		// "no document" for every reader: the read view would otherwise 500 on
		// ssr.RenderJSON(""), and the demos would disagree on how to render it.
		return "", false
	}
	return d.JSON, true
}

// Capabilities returns the grant set this plugin advertises to the editor.
func (p *Plugin) Capabilities() []string {
	out := make([]string, len(p.capabilities))
	copy(out, p.capabilities)
	return out
}

// allow is the capability gate, delegated to the platform [pluginhost.Allow]
// (protocol-v1.md §5/§10): default-deny against the plugin's granted set,
// intersected with the caller's own authority.
func (p *Plugin) allow(r *http.Request, cap string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so
		// BOTH gate sides (plugin grant AND caller authority) are skipped.
		// Production hosts never set this; the default-deny gate below rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, cap)
}

// memSave is the default persistence hook: an in-memory map keyed by DocID.
func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs[req.DocID] = savedDoc{JSON: req.DocJSON, MD: req.Markdown}
	return nil
}

// memUpload is the default upload hook: a data: URL echoing the bytes.
func (p *Plugin) memUpload(_ context.Context, req UploadRequest) (UploadResult, error) {
	mime := req.Type
	if mime == "" {
		mime = http.DetectContentType(req.Bytes)
	}
	return UploadResult{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(req.Bytes)}, nil
}

// UIHostOption returns the [uihost.Option] that injects the host scripts into
// every UIHost-rendered page: the generic platform broker first, then this
// plugin's adapter (host/broker.js). Order matters — the adapter registers
// with the broker the former defines. Apps using a UIHost pass this to
// uihost.New; the self-contained demo page includes both <script>s itself.
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, BrokerScriptURL)
}

// MountConfig configures [Mount].
type MountConfig struct {
	DocID     string // persistence key (default "demo")
	JSONField string // hidden input name for canonical block-JSON (default "body_json")
	MDField   string // hidden input name for markdown export (default "body_md")
	MinHeight string // initial iframe height before first resize (default "240px")
	Doc       string // optional initial doc JSON, server-rendered for reload round-trip
}

// Mount renders the mount marker div plus the two hidden inputs the host
// broker syncs on docChanged (protocol-v1.md §6/§10). It wraps the platform
// [pluginhost.MountMarker] (generic data-fui-plugin* marker) and adds the
// wysiwyg-specific data-fui-plugin-for attribute naming the JSON + markdown
// hidden fields. Drop it into a form. All interpolated values are HTML-escaped
// via [render.Escape] inside [pluginhost.MountMarker].
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.JSONField == "" {
		cfg.JSONField = defaultJSONField
	}
	if cfg.MDField == "" {
		cfg.MDField = defaultMDField
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
			{Name: "data-fui-plugin-for", Value: cfg.JSONField + "," + cfg.MDField},
		},
		Fields: []pluginhost.Field{
			{Name: cfg.JSONField},
			{Name: cfg.MDField},
		},
	})
}

// SaveRequest is the persistence payload handed to the save handler.
type SaveRequest struct {
	DocID         string // persistence key
	DocJSON       string // canonical ProseMirror doc JSON (opaque blob)
	Markdown      string // lossy markdown export
	SchemaVersion string // interchange version ("wysiwyg-v1")
}

// UploadRequest is the upload payload handed to the upload handler. Bytes is
// the raw image body; Name/Type come from the X-Upload-Name / X-Upload-Type
// headers the host broker sends with the raw-body POST.
type UploadRequest struct {
	Name  string
	Type  string
	Bytes []byte
}

// UploadResult is the upload handler's response. URL is what the editor
// embeds into the document.
type UploadResult struct {
	URL string
}
