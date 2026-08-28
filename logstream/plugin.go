// Package logstream is the GoFastr log-tail plugin: xterm.js inside an
// opaque-origin sandboxed iframe, fed by a line source the HOST pushes
// across the postMessage bridge without ever being asked.
//
// Why this shape: every other plugin in this repo is turn-based — load a
// document, save a document; even the datagrid, which moves 100,000 rows,
// moves them one request at a time in answer to a question the frame asked.
// A log tail is the opposite: open-ended, host-initiated, and produced
// faster than it can be rendered. This plugin exists to prove the bridge
// carries that, and to make the answer to "what happens when it cannot keep
// up" EXPLICIT instead of silent:
//
//   - The host adapter (host/adapter.js) keeps at most 4 unacknowledged
//     batches in flight; the frame acks each rendered batch with a
//     streamAck event carrying the last sequence number it rendered.
//   - When the producer outruns the frame, the host drops from the OLDEST
//     end of its bounded line buffer and counts; the count travels with the
//     next batch and the frame renders a visible "N lines dropped" marker.
//     A gap the user cannot see is worse than a gap labelled
//     "1,432 lines dropped".
//   - The frame's scrollback is bounded (10,000 lines, published in every
//     ack next to the live depth) and its consumption is paced at one batch
//     per ~16 ms tick, so a burst cannot monopolise its main thread.
//
// Read-only by design: no PTY, no shell, no command input, no writes, no
// uploads. The host supplies a line source ([WithSource]); the frame renders
// it. A terminal that can send input is a different plugin with a different
// security review.
//
// Capabilities: stream:read + theme:read, nothing else. The single route,
// GET /stream, is the producer's side of the bridge and gates on
// stream:read. pluginhost.Allow is a capability gate, NOT authentication: it
// passes for anonymous callers, so a host exposing a sensitive source must
// check the session in its own [SourceFunc]. See docs/logstream.md.
package logstream

import (
	"context"
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js hard-code
// these exactly (protocol-v1.md §2/§10). The demo lives at /logstream.
//
// These mirror the logstream row in plugins.json; internal/registry tests pin
// Name + RoutePrefix against that row, so they MUST NOT drift.
const (
	Name             = "logstream"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/logstream"
	TermHTMLURL      = RoutePrefix + "/term.html"
	TermJSURL        = RoutePrefix + "/term.js"
	TermCSSURL       = RoutePrefix + "/term.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	StreamURL        = RoutePrefix + "/stream"
	DemoURL          = "/logstream"
	SchemaVersion    = "logstream-v1"

	// CapStreamRead gates GET StreamURL — the producer side of the bridge.
	// It is the plugin's only route: logstream has no write surface at all.
	CapStreamRead = "stream:read"

	defaultDocID     = "demo"
	defaultMinHeight = "560px"
)

// DefaultCapabilities is the grant set advertised to the frame: reading the
// stream and bridging theme tokens. There are deliberately no optional
// capabilities — the plugin is read-only by construction, so there is no
// handler-vs-grant cross-check to fail loud about (the datagrid pattern
// exists for plugins with optional WRITE surface).
func DefaultCapabilities() []string {
	return []string{CapStreamRead, "theme:read"}
}

// Line is one log line crossing the stream: a sequence number assigned by
// the host's source plus the raw text with ANSI escapes INTACT — the frame
// interprets colour, the host never does. Seq must be strictly increasing
// within a stream (the ack/drop accounting is seq-ordered).
type Line struct {
	Seq  uint64 `json:"seq"`
	Text string `json:"text"`
}

// SourceFunc is the host-supplied line producer behind GET StreamURL. It is
// the streaming twin of datagrid's WithRowsSource: instead of answering a
// pull, it PUSHES lines to yield until the consumer goes away.
//
// Contract:
//
//   - It runs once per connected stream consumer, with a context cancelled
//     on disconnect; it MUST return promptly after ctx is done or yield
//     returns an error.
//   - Only lines with Seq > after may be yielded (reconnects resume from
//     the last sequence number the frame acknowledged).
//   - yield blocks until the line is on the wire; a stalled consumer
//     therefore backpressures the source rather than being silently
//     overrun. Dropping is the ADAPTER's job (visible, counted); the Go
//     side stays lossless or loud.
//   - AUTHENTICATION is the source's own responsibility: Allow passes for
//     anonymous callers, so a host exposing a sensitive log must check the
//     session here before yielding anything.
type SourceFunc func(ctx context.Context, after uint64, yield func(Line) error) error

// Plugin is the log-stream plugin. It implements [framework.Plugin] and
// mirrors the datagrid/pdf shape: opaque-origin sandboxed iframe, protocol
// v1 over postMessage, go:embed'd frame bundle, capability-gated route. The
// difference is the traffic profile — an open-ended push, host-initiated,
// with backpressure carried by frame acks.
type Plugin struct {
	manifest pluginhost.Manifest

	capabilities []string
	source       SourceFunc
	devGrantAll  bool
	withDemoPage bool
	demoRoute    string
	// demoControlURL, when set, is the host-app route the demo page's rate
	// switch POSTs to ({"rate":"calm"|"fast"}). The plugin itself is
	// read-only, so the producer's controls belong to the app that owns the
	// producer — see WithDemoControlURL.
	demoControlURL string
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithSource installs the line producer behind GET StreamURL. There is NO
// default: like datagrid's rows source, a log viewer without a source is a
// blank rectangle, so [New] panics without this option.
func WithSource(fn SourceFunc) Option {
	return func(p *Plugin) { p.source = fn }
}

// WithCapabilities overrides the grant set advertised to the frame. Default:
// [DefaultCapabilities]. There is nothing to expand into — the plugin has no
// optional capabilities — but the override exists for hosts that mint scoped
// tokens ("stream:*" implies stream:read under the framework's wildcard
// grammar, and the runtime gate matches it).
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDevGrantAll short-circuits the capability gate (Phase-0 demo / tests).
// It bypasses BOTH gate sides, so the stream route behind it still serves
// anyone — which is exactly why a real host must keep authentication in its
// own SourceFunc.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithDemoRoute overrides where [WithDemoPage] mounts the demo (default
// [DemoURL], "/logstream").
func WithDemoRoute(path string) Option { return func(p *Plugin) { p.demoRoute = path } }

// WithDemoControlURL points the demo page's rate switch at a host-app route
// (POST {"rate":"calm"|"fast"}). The plugin is read-only — no capability
// exists for changing a producer's rate — so the control belongs to the app
// that owns the producer, typically the same one that wired [WithSource].
// Unset (the default), the demo page renders without the rate switch.
func WithDemoControlURL(url string) Option { return func(p *Plugin) { p.demoControlURL = url } }

// New constructs a Plugin. The platform manifest is built and Validate()'d
// here so a bad isolation/sandbox config aborts construction rather than
// silently de-opaquing the frame at runtime.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		manifest:     newManifest(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	if len(p.capabilities) == 0 {
		p.capabilities = DefaultCapabilities()
	}
	if p.source == nil {
		panic("logstream: no source configured — supply WithSource " +
			"(a log viewer without a line source is a blank rectangle)")
	}
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("logstream: invalid manifest: " + err.Error())
	}
	return p
}

// newManifest is the platform manifest every logstream plugin instance
// carries. Split out of New so the literal lives next to the invariants it
// pins.
func newManifest() pluginhost.Manifest {
	return pluginhost.Manifest{
		Entry:     TermHTMLURL,
		Isolation: pluginhost.IsolationSandboxOpaque,
		Sandbox:   []string{pluginhost.DefaultSandbox},
		MinHeight: defaultMinHeight,
		Schema:    SchemaVersion,
		Title:     "Log stream",
	}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Init registers every asset and the stream route on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document + its JS/CSS
	// sub-resources. AssetServer applies the framing/CORP/CSP relaxation
	// and the fixed framedCSP (connect-src 'none', sandbox allow-scripts)
	// to exactly these — the frame can never fetch the log, which is the
	// structural reason every line crosses the bridge.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "term.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "term.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "term.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) script: the adapter that owns the push side
	// and all of the backpressure.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.Register(rt)

	// The one route: the producer side of the bridge, gated on stream:read.
	rt.Get(StreamURL, http.HandlerFunc(p.handleStream))

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
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
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
		// Production hosts never set this; the default-deny gate rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, capability)
}

// UIHostOption injects the platform broker and this plugin's adapter (in
// that order — the adapter registers with the registry the former defines).
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, AdapterScriptURL)
}

// MountConfig configures [Mount].
type MountConfig struct {
	// DocID is the stream identity for this mount (logging/debug key; a
	// log tail has no persisted doc).
	DocID string
	// MinHeight is the terminal viewport height. Defaults to 560px.
	MinHeight string
	// Capabilities is an optional CSV grant override.
	Capabilities string
}

// Mount renders the generic mount marker. A log tail has no canonical doc and
// no hidden form field — nothing to round-trip on submit; the marker is the
// whole mount. All interpolated values are HTML-escaped inside
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
