// Package scanner is the GoFastr barcode-scanner plugin: @zxing/library
// inside an opaque-origin sandboxed iframe, fed GRAYSCALE pixels the HOST
// page captures and pushes across the postMessage bridge.
//
// Why this shape: a camera is unreachable from an opaque-origin frame.
// getUserMedia fails there with "SecurityError: Invalid security origin"
// under sandbox="allow-scripts", and adding allow="camera" to the sandbox
// does not change it (measured — the permission is bound to a real origin
// the frame cannot have without de-opaquing). So the HOST page owns the
// MediaStream and the permission prompt appears against the host's origin
// where a user can reason about it; the frame only ever decodes what it is
// handed. Pixels cross host→frame; only strings cross back; the frame still
// cannot open a socket (connect-src 'none' like every plugin here).
//
// The traffic profile is logstream's, inverted: a bounded open-ended push
// (one scanFrame in flight at a time — the frameDone ack is the flow
// control) rather than an unbounded one, because decode is the expensive
// side and stale frames are worthless. There is no document store, no
// upload path, no save route: a decoded string goes to the host page and
// nowhere else.
//
// The wire contract v1 (both scanner/assets/scan.js and host/adapter.js
// implement THIS; see the adapter header for the full table):
//
//	host → frame: init (config: {formats, scanRateHz}), scanFrame
//	               {seq, width, height, gray}, scanSample, teardown
//	frame → host: scanResult {seq, text, format, decodeMs}, frameDone
//	               {seq, decoded}, scanStats {framesSeen, decodes,
//	               lastDecodeMs, lastText}
//
// `gray` is grayscale LUMINANCE, exactly width*height bytes — NOT RGBA.
// Handing zxing an RGBA buffer fails inside MultiFormatReader with "No
// MultiFormat Readers were able to detect the code", which reads like a bad
// image rather than a bad call, so the mistake is invisible until someone
// measures it. (Measured.) The luminance conversion therefore lives in
// exactly one place: host/adapter.js, toGray().
//
// Capabilities: scan:decode + theme:read, nothing else. There is no host
// route to gate — the plugin's only crossings are pixels down and strings
// up over the broker's source-checked postMessage — so scan:decode is the
// grant ADVERTISED to the frame (init.capabilities) and the capability a
// future host-side route would gate on; p.allow enforces it for callers
// that check. pluginhost.Allow is a capability gate, NOT authentication:
// it passes for anonymous callers.
package scanner

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js hard-code
// these exactly. The demo lives at /scanner.
//
// These mirror the scanner row in plugins.json (added by the coordinator);
// internal/registry tests pin Name + RoutePrefix against that row, so they
// MUST NOT drift.
const (
	Name             = "scanner"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/scanner"
	FrameHTMLURL     = RoutePrefix + "/scan.html"
	ScanJSURL        = RoutePrefix + "/scan.js"
	ScanCSSURL       = RoutePrefix + "/scan.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	ConfigScriptURL  = RoutePrefix + "/config.js"
	DemoURL          = "/scanner"
	SchemaVersion    = "scanner-v1"

	// CapScanDecode is the decode grant advertised to the frame in
	// init.capabilities. The plugin has no host route to gate today — no
	// store, no upload, no save — so this is the capability a caller must
	// hold before a future host-side decode service would serve it.
	CapScanDecode = "scan:decode"

	defaultDocID     = "demo"
	defaultMinHeight = "460px"

	// defaultScanRate is the frames-per-second ceiling the host adapter
	// paces scanFrame at. 8 is comfortably above the ~30 ms worst-case
	// measured decode while leaving the cage's main thread mostly idle.
	defaultScanRate = 8
	minScanRate     = 1
	maxScanRate     = 30
)

// knownFormats is the wire vocabulary for `formats`: exactly the
// @zxing/library BarcodeFormat names MultiFormatReader can decode from a
// grayscale buffer. Both sides validate against a copy of this list — the
// Go side fail-loud in [New], the adapter when narrowing scanResult.format
// — so a typo on either end is caught rather than silently never matching.
var knownFormats = map[string]bool{
	"AZTEC":       true,
	"CODABAR":     true,
	"CODE_39":     true,
	"CODE_93":     true,
	"CODE_128":    true,
	"DATA_MATRIX": true,
	"EAN_8":       true,
	"EAN_13":      true,
	"ITF":         true,
	"PDF_417":     true,
	"QR_CODE":     true,
	"UPC_A":       true,
	"UPC_E":       true,
}

// SupportedFormats returns every format name [WithFormats] accepts, sorted.
func SupportedFormats() []string {
	out := make([]string, 0, len(knownFormats))
	for name := range knownFormats {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DefaultFormats returns the default decode set: every supported format.
// All of them is the least-surprise default for a "barcode scanner"; it
// costs decode time (MultiFormatReader tries every reader), so a host
// scanning one symbology should narrow with [WithFormats].
func DefaultFormats() []string {
	return SupportedFormats()
}

// DefaultCapabilities is the grant set advertised to the frame: decoding
// and bridging theme tokens. There are deliberately no optional
// capabilities — the plugin has no write surface at all, so there is no
// handler-vs-grant cross-check to fail loud about.
func DefaultCapabilities() []string {
	return []string{CapScanDecode, "theme:read"}
}

// frameConfig is the instance-level config bridged to the frame via the
// host-page config.js script + the adapter's manifest config → init.config.
// Every field is always serialised (no omitempty) so the frame always
// receives a complete config and never has to guess a default.
type frameConfig struct {
	Formats    []string `json:"formats"`    // zxing BarcodeFormat names, sorted
	ScanRateHz int      `json:"scanRateHz"` // host capture pace; the frame uses it for stats only
}

// Plugin is the barcode-scanner plugin. It implements [framework.Plugin]
// and mirrors the logstream shape (opaque-origin sandboxed iframe, protocol
// v1 over postMessage, go:embed'd frame bundle) with the scanner's
// inversion: the HOST is the producer (camera → grayscale scanFrame events)
// and the frame is the consumer (zxing decode), flow-controlled one frame
// in flight by frameDone acks.
type Plugin struct {
	manifest pluginhost.Manifest

	capabilities []string
	formats      []string
	scanRateHz   int
	devGrantAll  bool
	withDemoPage bool
	demoRoute    string
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithCapabilities overrides the grant set advertised to the frame. Default:
// [DefaultCapabilities]. There is nothing to expand into — the plugin has no
// optional capabilities — but the override exists for hosts that mint scoped
// tokens ("scan:*" implies scan:decode under the framework's wildcard
// grammar, and the runtime gate matches it).
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithFormats narrows the decode set advertised to the frame (default:
// [DefaultFormats], i.e. every supported symbology). Names are zxing
// BarcodeFormat strings — see [SupportedFormats]; an unknown name panics in
// [New] rather than silently never matching. Fewer formats also decodes
// faster: MultiFormatReader tries one reader per format.
func WithFormats(formats ...string) Option {
	return func(p *Plugin) { p.formats = append([]string{}, formats...) }
}

// WithScanRate sets the host capture pace in frames per second (default 8).
// Out-of-range values panic in [New]: the rate is a pacing bound, not a
// correctness knob, and a typo of 0 or 5000 should never mount quietly.
func WithScanRate(hz int) Option {
	return func(p *Plugin) { p.scanRateHz = hz }
}

// WithDevGrantAll short-circuits the capability gate (demo / tests). There
// is no gated route today, so it only loosens p.allow — kept for API
// symmetry with the rest of the repo and for whatever host-side decode
// service lands next.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithDemoRoute overrides where [WithDemoPage] mounts the demo (default
// [DemoURL], "/scanner").
func WithDemoRoute(path string) Option { return func(p *Plugin) { p.demoRoute = path } }

// New constructs a Plugin. The platform manifest is built and Validate()'d
// here, and the instance config (formats, rate) is fail-loud validated, so
// a bad isolation/sandbox config or a typo'd format aborts construction
// rather than surfacing as a scanner that never decodes.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		formats:      DefaultFormats(),
		scanRateHz:   defaultScanRate,
		manifest:     newManifest(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	p.validateConfig()
	if len(p.capabilities) == 0 {
		p.capabilities = DefaultCapabilities()
	}
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("scanner: invalid manifest: " + err.Error())
	}
	return p
}

// validateConfig is the fail-loud gate for the numeric / vocabulary
// invariants [New] enforces. It panics on any violation so a bad config
// never reaches Init — same posture as [pluginhost.Manifest.Validate].
func (p *Plugin) validateConfig() {
	switch {
	case p.scanRateHz == 0:
		p.scanRateHz = defaultScanRate
	case p.scanRateHz < minScanRate || p.scanRateHz > maxScanRate:
		panic("scanner: scan rate out of range " +
			"(WithScanRate wants 1..30 Hz)")
	}
	if len(p.formats) == 0 {
		p.formats = DefaultFormats()
	}
	sort.Strings(p.formats)
	p.formats = slices.Compact(p.formats)
	for _, f := range p.formats {
		if !knownFormats[f] {
			panic("scanner: unknown barcode format " + f +
				" (see scanner.SupportedFormats)")
		}
	}
}

// newManifest is the platform manifest every scanner plugin instance
// carries. Split out of New so the literal lives next to the invariants it
// pins.
func newManifest() pluginhost.Manifest {
	return pluginhost.Manifest{
		Entry:     FrameHTMLURL,
		Isolation: pluginhost.IsolationSandboxOpaque,
		Sandbox:   []string{pluginhost.DefaultSandbox},
		MinHeight: defaultMinHeight,
		Schema:    SchemaVersion,
		Title:     "Barcode scanner",
	}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Formats returns the decode set this instance advertises (sorted copy).
func (p *Plugin) Formats() []string { return append([]string{}, p.formats...) }

// ScanRateHz returns the host capture pace this instance configures.
func (p *Plugin) ScanRateHz() int { return p.scanRateHz }

// Init registers every asset on the app's router. There is deliberately no
// data route: pixels arrive over the bridge from the privileged host
// adapter, and the frame's CSP (connect-src 'none') is what keeps it that
// way.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document + its JS/CSS
	// sub-resources. AssetServer applies the framing/CORP/CSP relaxation
	// and the fixed framedCSP (connect-src 'none', sandbox allow-scripts,
	// script-src the EXPLICIT origin — never 'self', which an opaque-origin
	// frame resolves to null and Safari then blocks, measured) to exactly
	// these.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "scan.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "scan.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "scan.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) scripts: the adapter that owns the camera and
	// the capture loop, and config.js that publishes this instance's
	// formats + rate for the adapter to merge into the manifest it
	// registers.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.AddBytes(ConfigScriptURL, "text/javascript; charset=utf-8", false, p.configScriptBytes())
	srv.Register(rt)

	if p.withDemoPage {
		demoRoute := p.demoRoute
		if demoRoute == "" {
			demoRoute = DemoURL
		}
		rt.Get(demoRoute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The demo page is a top-level document; carry a plain app CSP so
			// the framed child (its own framedCSP) and the host scripts load
			// cleanly.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}

// Capabilities returns the grant set this plugin advertises to the frame.
func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

// allow is the capability gate, delegated to the platform
// [pluginhost.Allow]: default-deny against the plugin's granted set,
// intersected with the caller's own authority (a scoped token restricts
// below the grant; a session caller is bound by the grant alone). It is
// NOT authentication — see the package doc.
func (p *Plugin) allow(r *http.Request, capability string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so
		// BOTH gate sides (plugin grant AND caller authority) are skipped.
		// Production hosts never set this; the default-deny gate rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, capability)
}

// configScriptBytes renders the host-page config script that publishes this
// plugin instance's frameConfig as window.__gofastrScannerConfig. The
// adapter (loaded after it via [UIHostOption] / the demo page) merges it
// into the manifest config it registers with the platform broker, which the
// generic broker then bridges to the frame as init.config. JSON is a safe
// subset of a JS object literal and this is a standalone .js file (not
// inline), so no script-context escaping is required.
func (p *Plugin) configScriptBytes() []byte {
	cfg := frameConfig{
		Formats:    p.formats,
		ScanRateHz: p.scanRateHz,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		// frameConfig is a plain struct of primitives; marshal cannot fail
		// in practice. Fail loud rather than ship an empty config.
		panic("scanner: marshal frame config: " + err.Error())
	}
	return []byte("window.__gofastrScannerConfig = " + string(b) + ";\n")
}

// UIHostOption injects the platform broker, this plugin's config script,
// and this plugin's adapter (in that order — the adapter reads the config
// global the config script publishes, and registers with the broker the
// former defines).
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, ConfigScriptURL, AdapterScriptURL)
}

// MountConfig configures [Mount].
type MountConfig struct {
	// DocID is the scanner identity for this mount (logging/debug key; a
	// scanner has no persisted doc).
	DocID string
	// MinHeight is the scanner viewport height. Defaults to 460px.
	MinHeight string
	// Capabilities is an optional CSV grant override.
	Capabilities string
}

// Mount renders the generic mount marker. A scanner has no canonical doc
// and no hidden form field — nothing to round-trip on submit; the marker is
// the whole mount. All interpolated values are HTML-escaped inside
// [pluginhost.MountMarker].
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.MinHeight == "" {
		cfg.MinHeight = defaultMinHeight
	}
	return pluginhost.MountMarker(pluginhost.MountConfig{
		Plugin:       Name,
		DocID:        cfg.DocID,
		MinHeight:    cfg.MinHeight,
		Capabilities: cfg.Capabilities,
	})
}
