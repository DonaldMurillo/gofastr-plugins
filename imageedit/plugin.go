// Package imageedit is the GoFastr image crop / annotate / redact plugin.
// It applies the pdf plugin's design — the cage is the product, not the tax —
// to a second file format, and in doing so proves that shape was general
// rather than a one-off.
//
// Bytes arrive over the postMessage bridge: the host fetches the source image
// (GET /img/{id}, session + CSRF attached) and pushes it into the frame; the
// frame runs under connect-src 'none' and fetches nothing. A frame holding a
// confidential screenshot therefore cannot exfiltrate it.
//
// The canonical doc (schema imageedit-v1) is an OPERATION LIST, never pixels:
// {src, crop, rotate, annotations[], redactions[]} in source-image
// coordinates. The frame previews by applying that list to a canvas; the
// SERVER re-renders the same list with the standard library's image packages
// for every export, strips EXIF by full re-encode, enforces size and
// dimension caps, and verifies redactions against the produced bytes before
// releasing them. A client that lies about what it did cannot change what
// gets stored — the stored bytes are a function of the doc, rendered by Go.
//
// Operation order is FIXED and documented (docs/imageedit.md): crop → rotate
// → annotate → redact. Crop then rotate is not the same picture as rotate
// then crop; pinning the order (and having both renderers implement the exact
// same integer pipeline) is what keeps the preview and the server output in
// agreement.
//
// Capabilities: document:read, document:write, theme:read are always granted;
// upload:images is optional, appended exactly when the host wires
// [WithUploadHandler]. pluginhost.Allow is a capability gate, NOT
// authentication: it passes for anonymous callers, so any route that WRITES
// (POST /save, POST /export, POST /upload) must be documented as requiring
// the host to check the session in its own handler. See docs/imageedit.md.
package imageedit

import (
	"context"
	"encoding/base64"
	"net/http"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/access"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js hard-code
// these exactly (protocol-v1.md §2/§10). The demo lives at /imageedit.
//
// These mirror the imageedit row in plugins.json; internal/registry tests pin
// Name + RoutePrefix against that row, so they MUST NOT drift.
const (
	Name             = "imageedit"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/imageedit"
	EditorHTMLURL    = RoutePrefix + "/editor.html"
	EditorJSURL      = RoutePrefix + "/editor.js"
	EditorCSSURL     = RoutePrefix + "/editor.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	ConfigScriptURL  = RoutePrefix + "/config.js"
	ImageRoute       = RoutePrefix + "/img/{id}"
	UploadURL        = RoutePrefix + "/upload"
	SaveURL          = RoutePrefix + "/save"
	ExportURL        = RoutePrefix + "/export"
	DemoURL          = "/imageedit"
	SchemaVersion    = "imageedit-v1"

	// CapUploadImages gates POST /upload (a new source image entering the
	// plugin). It is OPTIONAL — it is NOT in [DefaultCapabilities].
	// [WithUploadHandler] appends it (the pdf WithExportHandler pattern):
	// reading bytes into the host's storage is ingress the host explicitly
	// turned on.
	CapUploadImages = "upload:images"

	defaultDocID     = "demo"
	defaultDocField  = "imageedit_doc"
	defaultMinHeight = "520px"

	// Host-enforced ceilings. Defaults cover a phone photo (12 MP JPEG ≈
	// 3–5 MiB) with room for a 24 MP camera file.
	defaultMaxBytes    int64 = 16 << 20   // 16 MiB — one image in transit
	defaultMaxPixels         = 24_000_000 // 24 MP — decode budget
	defaultMaxDim            = 8192       // per axis; 8K-side ceiling
	defaultJPEGQuality       = 90
)

// DefaultCapabilities is the always-on grant set advertised to the frame:
// reading the source image, persisting the operation list, and bridging theme
// tokens. upload:images is deliberately absent — it is an optionalCapability,
// appended by [WithUploadHandler].
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

// Rect is an axis-aligned integer rectangle in source-image pixels:
// inclusive origin (X, Y), exclusive extent (X+W, Y+H). 90° rotations keep
// rects axis-aligned, so the whole geometry vocabulary survives rotation.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// SrcRef identifies the image an operation list applies to. Kind is always
// "id": the host resolves Ref through [WithSource]. SHA256 optionally binds
// the doc to the exact source bytes (hex, lowercase); on mismatch the export
// is refused rather than applying coordinates to a different picture.
type SrcRef struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	SHA256 string `json:"sha256,omitempty"`
}

// Annotation is one non-destructive marking drawn above the (cropped,
// rotated) image. Geometry is authored in SOURCE-image pixels and mapped
// forward through crop+rotate at render time, so annotations stay pinned to
// image content across later crop/rotate edits.
//
// Type is "rect" (stroked rectangle, X/Y/W/H), "arrow" (X,Y tail → X2,Y2
// head) or "text" (X,Y top-left anchor, Size = glyph cell scale). Color is
// #RRGGBB — a content color drawn into the image, not a theme token. Width is
// the stroke thickness in output pixels (≥1).
type Annotation struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Color string `json:"color"`
	Width int    `json:"width"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	X2    int    `json:"x2"`
	Y2    int    `json:"y2"`
	Size  int    `json:"size"`
	Text  string `json:"text"`
}

// Redaction is one destructive removal: the region is FILLED in the final
// output (default black), never covered by a drawable object. Fill is #RRGGBB.
type Redaction struct {
	ID   string `json:"id"`
	Rect Rect   `json:"rect"`
	Fill string `json:"fill"`
}

// Doc is the canonical imageedit-v1 operation list. It round-trips through
// the hidden form field like every other plugin's doc; pixels never do.
type Doc struct {
	SchemaVersion string       `json:"schemaVersion"`
	Src           SrcRef       `json:"src"`
	Crop          *Rect        `json:"crop,omitempty"` // nil = uncropped
	Rotate        int          `json:"rotate"`         // 0 | 90 | 180 | 270, clockwise
	Annotations   []Annotation `json:"annotations"`
	Redactions    []Redaction  `json:"redactions"`
	Rev           int          `json:"rev"`
}

// UploadRequest is handed to the host's upload handler: the raw image bytes
// the frame read from a local file (the frame's file picker is the only thing
// the sandbox grants it; the bytes still cannot LEAVE except over the
// bridge), their sniffed format and header dimensions. The handler stores
// them and returns the id the doc's Src.Ref will reference.
type UploadRequest struct {
	Bytes  []byte
	Format string // "png" | "jpeg"
	Width  int
	Height int
}

// ExportRequest is handed to the host's export handler: the authoritative
// re-rendered bytes (Go composed them from the operation list, stripped EXIF
// and verified the redactions before encoding), plus the render facts and the
// verification report. The handler stores them and returns a URL.
type ExportRequest struct {
	DocID  string
	Doc    Doc
	Bytes  []byte
	Format string // "png" | "jpeg"
	Width  int
	Height int
	SHA256 string // hex digest of Bytes
	Report VerifyReport
}

// SaveRequest is the operation-list persist signal (POST /save).
type SaveRequest struct {
	DocID         string
	Doc           Doc
	DocJSON       string // raw canonical JSON, verbatim (the authoritative record)
	SchemaVersion string
	Rev           int
}

// savedDoc is the in-memory persisted operation list (the demo / default
// store).
type savedDoc struct {
	DocJSON string
}

// Plugin is the image editor. It implements [framework.Plugin] and mirrors
// the pdf/datagrid shape: opaque-origin sandboxed iframe, protocol v1 over
// postMessage, go:embed'd frame bundle, capability gate, host-side RPC
// routes. The difference is where the pixels come from: the server renders
// them from the doc, so the doc is the only thing that crosses.
type Plugin struct {
	manifest     pluginhost.Manifest
	capabilities []string
	maxBytes     int64
	maxPixels    int
	maxDim       int
	jpegQuality  int
	devGrantAll  bool
	withDemoPage bool
	demoRoute    string

	// source resolves a doc id to image bytes (png or jpeg). The default
	// serves the generated sample so the demo works with zero configuration;
	// a real host injects [WithSource] to reach its media store. /img/{id}
	// is the only route that calls it, and that route is fetched by the
	// privileged host adapter — never by the frame.
	source func(ctx context.Context, id string) ([]byte, error)

	uploadHandler func(ctx context.Context, req UploadRequest) (string, error)
	saveHandler   func(ctx context.Context, req SaveRequest) error
	exportHandler func(ctx context.Context, req ExportRequest) (string, error)

	mu   sync.Mutex
	docs map[string]savedDoc
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithSource installs the image resolver backing GET /img/{id}. The default
// serves [SampleImage]; a production host points this at its media store.
// The function runs in the host page with the session and CSRF token
// attached — the frame cannot call /img/{id} (connect-src 'none'), so
// authorization stays here at the data layer. Returning (nil, nil) means "no
// such image" (404).
func WithSource(fn func(ctx context.Context, id string) ([]byte, error)) Option {
	return func(p *Plugin) { p.source = fn }
}

// WithUploadHandler installs the ingress hook behind POST /upload AND opts
// the plugin into the optional upload:images capability (appended to the
// grant set if not already present). The handler is the host's chance to
// authorize the upload against the real session: pluginhost.Allow is a
// capability gate, not authentication, and it passes for anonymous callers.
func WithUploadHandler(fn func(ctx context.Context, req UploadRequest) (string, error)) Option {
	return func(p *Plugin) { p.uploadHandler = fn }
}

// WithSaveHandler overrides the operation-list persistence hook. The default
// stores the canonical doc JSON in an in-memory map keyed by DocID.
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// WithExportHandler overrides the export hook behind POST /export. The plugin
// re-renders the doc server-side (render.go), verifies the redactions against
// the produced bytes, and hands the handler the result; the handler stores it
// and returns a URL. The default handler echoes a data: URL — enough to prove
// the round trip; a production host points this at its file store.
func WithExportHandler(fn func(ctx context.Context, req ExportRequest) (string, error)) Option {
	return func(p *Plugin) { p.exportHandler = fn }
}

// WithMaxBytes sets the host-enforced ceiling on image bytes in transit
// (source, upload and produced output). Checked before decode/buffer.
func WithMaxBytes(n int64) Option { return func(p *Plugin) { p.maxBytes = n } }

// WithMaxPixels sets the decode budget: images whose header declares more
// pixels than this are refused (413) BEFORE image.Decode allocates them.
func WithMaxPixels(n int) Option { return func(p *Plugin) { p.maxPixels = n } }

// WithMaxDim sets the per-axis dimension ceiling, checked at the same
// header-read stage as WithMaxPixels.
func WithMaxDim(n int) Option { return func(p *Plugin) { p.maxDim = n } }

// WithJPEGQuality sets the re-encode quality for JPEG sources (1–95).
// [New] panics outside 1..95 because a zero quality silently produces a
// mud brick and a >95 file bloats for no visible gain.
func WithJPEGQuality(q int) Option { return func(p *Plugin) { p.jpegQuality = q } }

// WithDevGrantAll short-circuits the capability gate (demo / tests). It
// bypasses BOTH gate sides, so the write routes behind it must still fail
// closed on an unwired handler (a clear error, never a panic) — see
// handlers.go.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithCapabilities overrides the grant set advertised to the frame. Default:
// [DefaultCapabilities]. upload:images is appended separately by
// [WithUploadHandler] even when this fully replaces the set — the gate is on
// ingress the host explicitly enabled, so silently dropping it would just
// break loading with a 403.
//
// Grants are matched with the framework's scope grammar at runtime, so
// wildcards are legal here: "upload:*" and "*:*" imply upload:images. [New]
// requires the matching handler for anything the grant set implies — a
// wildcard that implies an optional capability without its handler is a
// construction panic, exactly like the literal capability (see
// [Plugin.grantsCapability]).
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithDemoRoute overrides where [WithDemoPage] mounts the demo (default
// [DemoURL], "/imageedit").
func WithDemoRoute(path string) Option { return func(p *Plugin) { p.demoRoute = path } }

// New constructs a Plugin. All fail-loud validation runs here so a
// misconfiguration aborts construction rather than mounting a silent hole.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		maxBytes:     defaultMaxBytes,
		maxPixels:    defaultMaxPixels,
		maxDim:       defaultMaxDim,
		jpegQuality:  defaultJPEGQuality,
		docs:         make(map[string]savedDoc),
		manifest: pluginhost.Manifest{
			Entry:        EditorHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Image editor",
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
	if p.exportHandler == nil {
		p.exportHandler = p.dataURLExport
	}
	// WithUploadHandler opts into upload:images (the pdf WithExportHandler
	// pattern). Append after WithCapabilities so a host that fully replaced
	// the set and still wired an upload handler keeps the gate passing.
	if p.uploadHandler != nil && !containsCap(p.capabilities, CapUploadImages) {
		p.capabilities = append(p.capabilities, CapUploadImages)
	}
	p.validateConfig()
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("imageedit: invalid manifest: " + err.Error())
	}
	return p
}

// validateConfig is the fail-loud gate for the cross-field invariants [New]
// enforces. It panics on any violation so a bad config never reaches Init —
// same posture as [pluginhost.Manifest.Validate] and pdf's validateConfig.
func (p *Plugin) validateConfig() {
	if p.maxBytes <= 0 {
		panic("imageedit: WithMaxBytes must be positive")
	}
	if p.maxPixels <= 0 {
		panic("imageedit: WithMaxPixels must be positive")
	}
	if p.maxDim <= 0 {
		panic("imageedit: WithMaxDim must be positive")
	}
	if p.jpegQuality < 1 || p.jpegQuality > 95 {
		panic("imageedit: WithJPEGQuality out of range (1..95)")
	}
	// A granted capability with no handler behind it is a silent hole: the
	// frame would offer image loading, the user would pick a file, and the
	// route would fail at nil-deref. The check uses the SAME wildcard grammar
	// the runtime gate matches with (pluginhost.Allow → auth.ScopeMatch →
	// access.ScopeMatch), so a wildcard grant ("upload:*", "*:*") implying
	// upload:images requires its handler here too — string equality here
	// would let exactly those wildcard grants compile, pass the gate on the
	// request, and then reach a nil handler.
	if p.grantsCapability(CapUploadImages) && p.uploadHandler == nil {
		panic("imageedit: upload:images granted but no upload handler " +
			"(a wildcard grant like *:* implies it) — supply WithUploadHandler " +
			"or drop the capability from WithCapabilities")
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

// grantsCapability reports whether the grant set implies capability under
// the framework's resource:verb wildcard grammar — the SAME matcher
// [Plugin.allow] enforces at request time. Construction checks and the
// runtime gate must agree, or wildcard grants slip between them.
func (p *Plugin) grantsCapability(capability string) bool {
	granted := make([]access.Permission, len(p.capabilities))
	for i, c := range p.capabilities {
		granted[i] = access.Permission(c)
	}
	return access.ScopeMatch(granted, access.Permission(capability))
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Init registers every asset and RPC route on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document + its JS/CSS
	// sub-resources. AssetServer applies the framing/CORP/CSP relaxation and
	// the fixed framedCSP (connect-src 'none', sandbox allow-scripts) to
	// exactly these — the frame can never fetch its own image, which is the
	// structural reason the bytes cross the bridge.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "editor.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "editor.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "editor.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) scripts: the adapter and this instance's
	// config.js, which publishes whether the optional upload:images
	// capability was wired so the adapter can merge it into the capabilities
	// it registers with the generic broker.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.AddBytes(ConfigScriptURL, "text/javascript; charset=utf-8", false, p.configScriptBytes())
	srv.Register(rt)

	// RPC routes (protocol-v1.md §10). Each gates on its capability; see
	// handlers.go for the per-route enforcement notes.
	rt.Get(ImageRoute, http.HandlerFunc(p.handleImage))
	rt.Post(UploadURL, http.HandlerFunc(p.handleUpload))
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))
	rt.Post(ExportURL, http.HandlerFunc(p.handleExport))

	if p.withDemoPage {
		demoRoute := p.demoRoute
		if demoRoute == "" {
			demoRoute = DemoURL
		}
		rt.Get(demoRoute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The demo page is a top-level document; carry a plain app CSP so
			// the framed child (its own framedCSP) and the host scripts load
			// cleanly. img-src allows data: because the default export
			// handler answers with a data: URL.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}

// LoadDoc returns the last-saved operation-list JSON for docID (demo
// round-trip). ok is false when the doc has never been saved.
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (docJSON string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, found := p.docs[docID]
	if !found {
		return "", false
	}
	return d.DocJSON, true
}

// Capabilities returns the grant set this plugin advertises to the frame.
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
		// BOTH gate sides (plugin grant AND caller authority) are skipped.
		// Production hosts never set this; the default-deny gate below rules.
		// The routes still fail closed on an unwired handler, so the bypass
		// cannot reach a nil handler either.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, capability)
}

// memSave is the default persistence hook: an in-memory map keyed by DocID,
// storing the raw canonical doc JSON /save produced.
func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs[req.DocID] = savedDoc{DocJSON: req.DocJSON}
	return nil
}

// defaultSource serves the generated sample for any id, so the demo works
// with zero configuration. A host wanting real images injects [WithSource].
func (p *Plugin) defaultSource(_ context.Context, _ string) ([]byte, error) {
	return SampleImage(), nil
}

// dataURLExport is the default export hook: echo the produced bytes as a
// data: URL, enough to prove the round trip with no storage wired. A
// production host replaces it via [WithExportHandler].
func (p *Plugin) dataURLExport(_ context.Context, req ExportRequest) (string, error) {
	mime := "image/png"
	if req.Format == "jpeg" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(req.Bytes), nil
}

// configScriptBytes renders the host-page config script that publishes this
// plugin instance's optional-capability wiring as
// window.__gofastrImageeditConfig. The adapter (loaded after it via
// [UIHostOption] / the demo page) merges it into the capabilities it
// registers with the platform broker, which the generic broker then bridges
// to the frame as init.capabilities — so the frame never offers image
// loading the host did not wire. The frame still cannot grant itself
// anything: POST /upload re-checks the gate regardless of what the frame
// believes.
func (p *Plugin) configScriptBytes() []byte {
	// grantsCapability, not containsCap: a wildcard grant that implies the
	// capability must advertise it too (and construction guaranteed the
	// handler exists).
	upload := "false"
	if p.grantsCapability(CapUploadImages) {
		upload = "true"
	}
	return []byte("window.__gofastrImageeditConfig={\"uploadEnabled\":" + upload + "}\n")
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
	// DocID is the persistence key for the operation-list doc.
	DocID string
	// Doc is the initial doc JSON server-rendered into the marker
	// (data-fui-plugin-doc) — the frame reads crop/rotate/annotations/
	// redactions out of it on init and requests the image it names.
	Doc string
	// Field is the hidden-input name the adapter mirrors the current doc
	// into on docChanged, so a normal form POST round-trips it. Defaults to
	// "imageedit_doc". The name is published on the marker as
	// data-fui-plugin-field, which is how the adapter finds it — the input
	// itself is rendered after the marker, not inside it.
	Field string
	// MinHeight is the initial iframe height before the editor sizes itself.
	MinHeight string
	// Capabilities is an optional CSV grant override.
	Capabilities string
}

// Mount renders the generic mount marker plus the hidden input the adapter
// syncs on docChanged. Drop it into a form. All interpolated values are
// HTML-escaped inside [pluginhost.MountMarker].
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.Field == "" {
		cfg.Field = defaultDocField
	}
	if cfg.MinHeight == "" {
		cfg.MinHeight = defaultMinHeight
	}
	return pluginhost.MountMarker(pluginhost.MountConfig{
		Plugin:       Name,
		DocID:        cfg.DocID,
		MinHeight:    cfg.MinHeight,
		Doc:          cfg.Doc,
		Capabilities: cfg.Capabilities,
		// Publish the hidden-input name on the marker so the adapter mirrors
		// the doc into the field THIS mount named, not a hard-coded one — a
		// custom Field must not silently lose its view state on submit.
		Attributes: []pluginhost.Attribute{
			{Name: "data-fui-plugin-field", Value: cfg.Field},
		},
		Fields: []pluginhost.Field{
			{Name: cfg.Field},
		},
	})
}
