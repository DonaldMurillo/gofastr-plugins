// Package pdf is the GoFastr PDF viewer / editor / redactor plugin. It mounts
// pdf.js (render) + pdf-lib (write) inside an opaque-origin sandboxed iframe,
// exactly like the richtext/mermaid/monaco plugins, and adds a fourth document
// shape: the canonical doc is an annotation OVERLAY (schema pdf-v1), never the
// file bytes. The PDF itself is an external resource the host resolves via
// [WithSource] and pushes over the postMessage bridge — the frame has
// connect-src 'none' and fetches nothing, which is the structural reason a
// confidential document opened for redaction cannot be exfiltrated.
//
// Mode (view / annotate / redact) is host-chosen and enforced on BOTH sides:
// the mode is bridged to the frame in init.config so it can hide its own UI,
// AND the Go handlers reject any payload the mode does not permit. UI-only
// gating is explicitly forbidden by the platform rules — a de-opaqued frame or
// a hand-crafted postMessage must not reach a mode-disallowed action. See
// docs/pdf.md and docs/plugin-platform.md.
package pdf

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js hard-code
// these exactly (protocol-v1.md §2/§10). The demo lives at /pdf.
//
// These mirror the pdf row in plugins.json; internal/registry tests pin
// Name + RoutePrefix against that row, so they MUST NOT drift.
const (
	Name             = "pdf"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/pdf"
	ViewerHTMLURL    = RoutePrefix + "/viewer.html"
	ViewerJSURL      = RoutePrefix + "/viewer.js"
	ViewerCSSURL     = RoutePrefix + "/viewer.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	ConfigScriptURL  = RoutePrefix + "/config.js"
	SamplePDFURL     = RoutePrefix + "/sample.pdf"
	SaveURL          = RoutePrefix + "/save"
	ExportURL        = RoutePrefix + "/export"
	DocRoute         = RoutePrefix + "/doc/{id}"
	DemoURL          = "/pdf"
	SchemaVersion    = "pdf-v1"

	// CapPDFExport gates the /export route. It is OPTIONAL — it is NOT in
	// [DefaultCapabilities]. [WithExportHandler] appends it (mirroring geomap's
	// WithSearch → geocode:search), because producing a PDF is egress the host
	// explicitly turned on. ModeRedact requires it (redaction produces a new
	// file), so constructing ModeRedact without it panics at [New].
	CapPDFExport = "pdf:export"

	defaultDocID     = "demo"
	defaultMinHeight = "480px"

	// Defaults for the host-enforced ceilings / export fidelity.
	defaultMaxBytes  int64 = 32 << 20 // 32 MiB — covers multi-page scanned PDFs
	defaultRedactDPI       = 200      // publication-quality rasterization floor
	minRedactDPI           = 72       // screen DPI — below this redactions blur
	maxRedactDPI           = 600      // archival scan ceiling; past it, bloat for no visible gain
)

// DefaultCapabilities is the always-on grant set advertised to the viewer.
// These mirror the pdf row's "capabilities" in plugins.json. pdf:export is
// deliberately absent here — it is an optionalCapability, appended by
// [WithExportHandler].
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

// Mode is a bitmask of the host-selected capabilities the frame may exercise.
// It is host-chosen only — never plugin- or user-selectable — and normalises
// to a lattice: ModeRedact ⊇ ModeAnnotate ⊇ ModeView. A host composes the
// surface it wants with [WithMode]; the handlers enforce each route against
// the bits actually set.
type Mode uint8

// The three mode bits. They are OR-composable ([WithMode] accepts
// ModeView|ModeAnnotate|ModeRedact), but [normalizeMode] folds any combination
// into the lattice so the enforcement helpers can test a single bit per route.
const (
	// ModeView permits rendering only. /save and /export are both rejected.
	ModeView Mode = 1 << iota
	// ModeAnnotate permits overlay edits (annotations, form fills, page ops)
	// and non-redacting export. Implies ModeView.
	ModeAnnotate
	// ModeRedact permits destructive redaction export on top of everything
	// annotate allows. Implies ModeAnnotate (and ModeView). Requires the
	// pdf:export capability — [New] panics if it is absent.
	ModeRedact
)

// String renders the mode as the highest tier its bits grant, which is what
// the frame advertises in init.config.mode ("view" | "annotate" | "redact").
// The frame shows exactly the UI that tier unlocks; the Go side still enforces
// the individual bits.
func (m Mode) String() string {
	switch {
	case m&ModeRedact != 0:
		return "redact"
	case m&ModeAnnotate != 0:
		return "annotate"
	default:
		return "view"
	}
}

// allowsAnnotate reports whether overlay edits (and non-redacting export) are
// permitted — i.e. the annotate OR redact bit is set.
func (m Mode) allowsAnnotate() bool { return m&(ModeAnnotate|ModeRedact) != 0 }

// allowsRedact reports whether destructive redaction export is permitted.
func (m Mode) allowsRedact() bool { return m&ModeRedact != 0 }

// normalizeMode folds a Mode value into the view ⊆ annotate ⊆ redact lattice:
// redact implies annotate implies view. A zero mode defaults to ModeView. This
// is the one place the lattice is repaired, so the enforcement helpers can test
// a single bit per route without re-deriving the inclusion everywhere.
func normalizeMode(m Mode) Mode {
	if m == 0 {
		return ModeView
	}
	if m&ModeRedact != 0 {
		m |= ModeAnnotate | ModeView
	}
	if m&ModeAnnotate != 0 {
		m |= ModeView
	}
	return m
}

// frameConfig is the instance-level config bridged to the frame via the
// host-page config.js script + the adapter's manifest config → init.config.
// Every field is always serialised (no omitempty) so the frame always receives
// a complete config and never has to guess a default.
type frameConfig struct {
	Mode      string `json:"mode"`      // Mode.String() — "view" | "annotate" | "redact"
	RedactDPI int    `json:"redactDPI"` // rasterization DPI for redacted pages
	MaxBytes  int64  `json:"maxBytes"`  // host ceiling; the frame sizes its upload windows to it
	// ExportEnabled reports whether the host wired [WithExportHandler], which
	// is what grants pdf:export. The adapter merges the capability into
	// init.capabilities from this, so the frame can offer export only when it
	// will actually succeed — rather than letting the user annotate, redact
	// and press Export before discovering the refusal. It is a REPORT of a
	// decision the host already made: POST /export re-checks the grant
	// regardless of what the frame believes.
	ExportEnabled bool   `json:"exportEnabled"`
	SchemaHash    string `json:"schemaHash"` // unused-reserved; documents the interchange version stably
}

// savedDoc is the in-memory persisted overlay (the demo / default store).
type savedDoc struct {
	DocJSON string // raw canonical overlay JSON (verbatim, authoritative)
	Rev     int    // last persisted revision (optimistic-concurrency check)
}

// Plugin is the PDF viewer / editor / redactor plugin. It implements
// [framework.Plugin] and mirrors the richtext/monaco shape (opaque-origin
// sandboxed iframe, protocol v1 over postMessage, go:embed'd frame bundle,
// capability gate, save/export handlers) with the pdf additions: modes, an
// optional export capability, and a host-resolved document source.
type Plugin struct {
	manifest     pluginhost.Manifest
	capabilities []string
	mode         Mode
	maxBytes     int64
	redactDPI    int
	devGrantAll  bool
	withDemoPage bool
	demoRoute    string

	// source resolves a doc id to its PDF bytes. The default serves the
	// embedded sample.pdf so the demo works with zero configuration; a real
	// host injects [WithSource] to reach its own document store. /doc/{id}
	// is the only route that calls it, and that route is called by the
	// privileged host adapter — never by the frame.
	source func(ctx context.Context, id string) ([]byte, error)

	saveHandler   func(ctx context.Context, req SaveRequest) error
	exportHandler func(ctx context.Context, req ExportRequest) (string, error)

	mu   sync.Mutex
	docs map[string]savedDoc
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithMode sets the host-selected capability surface. It is the ONLY way to
// choose a mode — modes are never plugin- or user-selectable. The value is
// normalised to the view⊆annotate⊆redact lattice. Default [ModeView].
func WithMode(m Mode) Option {
	return func(p *Plugin) { p.mode = normalizeMode(m) }
}

// WithMaxBytes is the host-enforced ceiling on the size of a PDF the plugin
// will move through /doc/{id} and /export. It is checked BEFORE bytes are
// relayed into the frame (413 rather than streaming a huge file into a
// postMessage) so an oversized document is rejected at the boundary. Default
// 32 MiB.
func WithMaxBytes(n int64) Option {
	return func(p *Plugin) { p.maxBytes = n }
}

// WithRedactDPI sets the rasterization DPI for pages that carry a redaction
// (pages without one are copied through losslessly). The valid range is 72..600;
// [New] panics outside it because a too-low DPI silently blurs redactions
// (content leaks visually) and a too-high one bloats the file for no gain.
// Default 200.
func WithRedactDPI(dpi int) Option {
	return func(p *Plugin) { p.redactDPI = dpi }
}

// WithSource installs the document resolver backing GET /doc/{id}. The default
// serves the embedded sample.pdf, which is enough for the demo and tests; a
// production host points this at its document store. The function runs in the
// host page with the session and CSRF token attached — the frame cannot call
// /doc/{id} (connect-src 'none'), so authorization stays here at the data
// layer.
func WithSource(fn func(ctx context.Context, id string) ([]byte, error)) Option {
	return func(p *Plugin) { p.source = fn }
}

// WithSaveHandler overrides the persistence hook. The default stores the
// canonical overlay JSON in an in-memory map keyed by DocID.
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// WithExportHandler overrides the export hook AND opts the plugin into the
// optional pdf:export capability (appended to the grant set if not already
// present — mirroring geomap's WithSearch → geocode:search). The default
// handler returns a data: URL echoing the bytes, enough to prove the round
// trip; a production host points this at its file store and returns a real URL.
// [ModeRedact] requires pdf:export, so a host selecting ModeRedact MUST also
// supply an export handler (or explicitly grant the capability).
func WithExportHandler(fn func(ctx context.Context, req ExportRequest) (string, error)) Option {
	return func(p *Plugin) { p.exportHandler = fn }
}

// WithDevGrantAll short-circuits the capability gate (Phase-0 demo / tests).
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithCapabilities overrides the grant set advertised to the viewer. Default:
// [DefaultCapabilities]. Note pdf:export is appended separately by
// [WithExportHandler] even when this fully replaces the set — the gate is on
// egress the host explicitly enabled, so silently dropping it would just break
// export with a 412.
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithDemoRoute overrides where [WithDemoPage] mounts the demo (default
// [DemoURL], "/pdf"). A host that wants "/pdf" for its own page moves the demo
// aside with this.
func WithDemoRoute(path string) Option {
	return func(p *Plugin) {
		if strings.TrimSpace(path) != "" {
			p.demoRoute = path
		}
	}
}

// New constructs a Plugin. All fail-loud validation runs here so a
// misconfiguration aborts construction rather than silently de-opaquing the
// frame, leaking a too-low redaction DPI, or mounting ModeRedact without the
// export capability it requires.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		mode:         ModeView,
		maxBytes:     defaultMaxBytes,
		redactDPI:    defaultRedactDPI,
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
	if p.saveHandler == nil {
		p.saveHandler = p.memSave
	}
	if p.source == nil {
		p.source = p.defaultSource
	}
	// WithExportHandler opts into pdf:export (the geomap WithSearch pattern).
	// Append after WithCapabilities so a host that fully replaced the set and
	// still wires an export handler keeps the gate pass — see CapPDFExport.
	if p.exportHandler != nil && !containsCap(p.capabilities, CapPDFExport) {
		p.capabilities = append(p.capabilities, CapPDFExport)
	}
	p.validateConfig()
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("pdf: invalid manifest: " + err.Error())
	}
	return p
}

// validateConfig is the fail-loud gate for the numeric / cross-field
// invariants [New] enforces. It panics on any violation so a bad config never
// reaches Init — same posture as [pluginhost.Manifest.Validate].
func (p *Plugin) validateConfig() {
	if p.maxBytes <= 0 {
		panic("pdf: WithMaxBytes must be positive")
	}
	if p.redactDPI < minRedactDPI || p.redactDPI > maxRedactDPI {
		panic("pdf: WithRedactDPI out of range (72..600)")
	}
	// ModeRedact produces a brand-new, redacted document — that is export
	// egress, so the capability must be declared. Constructing ModeRedact
	// without pdf:export is a silent-security hole (the handler would have
	// nowhere to send the bytes and the gate would not catch it), so fail
	// loud here, at construction.
	if p.mode.allowsRedact() && !containsCap(p.capabilities, CapPDFExport) {
		panic("pdf: ModeRedact requires the pdf:export capability — supply WithExportHandler (or WithCapabilities(\"" + CapPDFExport + "\"))")
	}
}

// containsCap is a tiny linear scan over the (always-tiny) capability list.
func containsCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Mode returns the host-selected, lattice-normalised mode. It is bridged to the
// frame via init.config.mode so the frame can hide UI the mode does not grant.
func (p *Plugin) Mode() Mode { return p.mode }

// MaxBytes returns the host-enforced byte ceiling.
func (p *Plugin) MaxBytes() int64 { return p.maxBytes }

// Init registers every asset and RPC route on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document + its JS/CSS sub-resources.
	// AssetServer applies the framing/CORP/CSP relaxation (DECISIONS.md gotcha #1)
	// and the fixed framedCSP (connect-src 'none', sandbox allow-scripts) to
	// exactly these.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "viewer.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "viewer.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "viewer.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) scripts + the SPIKE sample PDF. The adapter and
	// config.js are same-origin host-page fetches (no CORP relaxation); the
	// sample PDF is served directly only for legacy callers — the frame itself
	// receives its bytes over the bridge via /doc/{id}.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.AddBytes(ConfigScriptURL, "text/javascript; charset=utf-8", false, p.configScriptBytes())
	srv.AddBytes(SamplePDFURL, "application/pdf", false, samplePDFBytes())
	srv.Register(rt)

	// RPC routes (protocol-v1.md §10). Each gates on its capability and its
	// mode; see handlers.go for the per-route enforcement table.
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))
	rt.Post(ExportURL, http.HandlerFunc(p.handleExport))
	// /doc/{id} is the document resolver. It is called ONLY by the privileged
	// host adapter (same-origin, session/CSRF apply) — the frame has
	// connect-src 'none' and cannot call it. That is the reason authorisation
	// stays here at the data layer rather than in the frame.
	rt.Get(DocRoute, http.HandlerFunc(p.handleDoc))

	if p.withDemoPage {
		demoRoute := p.demoRoute
		if demoRoute == "" {
			demoRoute = DemoURL
		}
		rt.Get(demoRoute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The demo page is a top-level document; carry a plain app CSP so the
			// framed child (its own framedCSP) and the host scripts load cleanly.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}

// LoadDoc returns the last-saved overlay JSON for docID (demo round-trip). ok is
// false when the doc has never been saved.
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (docJSON string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, found := p.docs[docID]
	if !found {
		return "", false
	}
	return d.DocJSON, true
}

// Capabilities returns the grant set this plugin advertises to the viewer.
func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

// allow is the capability gate, delegated to the platform [pluginhost.Allow]:
// default-deny against the plugin's granted set, intersected with the caller's
// own authority (a scoped token restricts below the grant; a session caller is
// bound by the grant alone).
func (p *Plugin) allow(r *http.Request, capability string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so
		// BOTH gate sides (plugin grant AND caller authority) are skipped.
		// Production hosts never set this; the default-deny gate below rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, capability)
}

// memSave is the default persistence hook: an in-memory map keyed by DocID. It
// stores the raw overlay JSON verbatim and bumps the revision so a subsequent
// conflicting save can be observed through [ErrConflict] if a host wires one.
func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, ok := p.docs[req.DocID]
	if ok && req.Rev != 0 && prev.Rev != 0 && req.Rev != prev.Rev {
		// The default store honours optimistic concurrency when both sides
		// carry a rev, so the round-trip is observable without a custom
		// handler. A host that does not want this passes its own
		// [WithSaveHandler] (the tests do exactly this).
		return ErrConflict
	}
	next := req.Rev
	if next == 0 {
		next = prev.Rev + 1
	} else if !ok {
		// first save keeps the client's rev if any
	}
	p.docs[req.DocID] = savedDoc{DocJSON: req.DocJSON, Rev: next}
	return nil
}

// defaultSource serves the embedded SPIKE sample PDF for any id, so the demo
// works with zero configuration. A host wanting real documents injects
// [WithSource].
func (p *Plugin) defaultSource(_ context.Context, _ string) ([]byte, error) {
	return SampleDocument(), nil
}

// SampleDocument returns the embedded two-page sample PDF the demo renders —
// real selectable text (including the SPIKE_SECRET_ALPHA marker the tests look
// for) plus an embedded raster image.
//
// Exported for demos, probes and tests that wire their own [WithSource] and
// still want the stock document for every id they do not special-case. A
// WithSource hook fully REPLACES the default, and returning (nil, nil) means
// "no such document" (404) rather than "fall back to the sample" — a
// production host must never silently serve a demo file in place of a document
// it failed to find, so the fallback is opt-in and explicit.
//
// The returned slice is a copy; the embedded bytes are not mutable by callers.
func SampleDocument() []byte {
	b := samplePDFBytes()
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// configScriptBytes renders the host-page config script that publishes this
// plugin instance's frameConfig as window.__gofastrPdfConfig. The adapter
// (loaded after it via [UIHostOption] / the demo page) merges it into the
// manifest config it registers with the platform broker, which the generic
// broker then bridges to the frame as init.config — the bilateral enforcement
// channel for mode (the frame hides the UI its mode does not grant). JSON is a
// safe subset of a JS object literal and this is a standalone .js file (not
// inline), so no script-context escaping is required.
func (p *Plugin) configScriptBytes() []byte {
	cfg := frameConfig{
		Mode:          p.mode.String(),
		RedactDPI:     p.redactDPI,
		MaxBytes:      p.maxBytes,
		ExportEnabled: p.exportHandler != nil,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		// frameConfig is a plain struct of primitives; marshal cannot fail in
		// practice. Fail loud rather than ship an empty config.
		panic("pdf: marshal frame config: " + err.Error())
	}
	return []byte("window.__gofastrPdfConfig = " + string(b) + ";\n")
}

// UIHostOption injects the platform broker, this plugin's config script, and
// this plugin's adapter (in that order — the adapter reads the config global
// the config script publishes, and registers with the broker the former
// defines).
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, ConfigScriptURL, AdapterScriptURL)
}

// MountConfig configures [Mount].
type MountConfig struct {
	DocID     string // persistence key (default "demo")
	MinHeight string // initial iframe height before first resize (default "480px")
	Doc       string // optional initial overlay JSON, server-rendered for reload round-trip
}

// Mount renders the generic mount marker. The host adapter reads the doc id off
// the marker to fetch /doc/{id}; the overlay round-trips through the hidden
// field on a normal form POST. Drop it into a form. All interpolated values are
// HTML-escaped via [render.Escape] inside [pluginhost.MountMarker].
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

// decodeOverlay parses raw overlay JSON into a typed [Overlay] for inspection.
// It is best-effort: a malformed body is logged via the returned error and the
// caller falls back to the raw DocJSON (the authoritative record), so a
// forward-incompatible overlay never fails a save — the verbatim bytes survive.
func decodeOverlay(raw json.RawMessage) (Overlay, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return Overlay{}, nil
	}
	var o Overlay
	if err := json.Unmarshal(raw, &o); err != nil {
		return Overlay{}, err
	}
	return o, nil
}
