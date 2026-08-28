// Package whiteboard is the GoFastr collaborative-whiteboard plugin: a small
// canvas board inside an opaque-origin sandboxed iframe, whose document is a
// Yjs CRDT relayed BETWEEN BROWSERS by the host.
//
// Why this shape: the framed CSP sets connect-src 'none' — the exfiltration
// guard the whole isolation design rests on — and a collaborative plugin
// looks like it needs that guard removed. It does not. CRDT updates are
// order-insensitive binary blobs, so they can cross the postMessage bridge
// and the HOST can own the only network connection: the frame emits update
// blobs, the host fans them out to every other participant, and the frame
// collaborates with people it cannot reach. This is the Phase-4
// collaboration/presence idea from docs/DECISIONS.md landed without
// weakening the isolation contract by one directive.
//
// The room hub is deliberately NOT in this package. The plugin defines the
// CONTRACT (SubscribeFunc/PublishFunc, [WithRoomHub]) and the capability
// gate; a host wires its own hub — the example app ships one
// (example/whiteboard.go) with SSE fan-out, replay-on-join and presence.
// Identity is the host's to decide: the hub assigns each participant an
// opaque id and a colour, and NOTHING ELSE crosses — presence carries no
// names, because the frame is untrusted (see docs/whiteboard.md).
//
// Capabilities: theme:read is always granted. sync:room — the capability
// that opens the publish/stream routes — appears exactly when the host wires
// a hub ([WithRoomHub]), the datagrid optional-capability pattern. Routes
// fail closed without it. pluginhost.Allow is a capability gate, NOT
// authentication: it passes for anonymous callers, so a host exposing a
// sensitive board must check the session in its own hub. See
// docs/whiteboard.md.
package whiteboard

import (
	"context"
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/access"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js hard-code
// these exactly (protocol-v1.md §2/§10). The demo lives at /whiteboard.
//
// These mirror the whiteboard row in plugins.json; internal/registry tests pin
// Name + RoutePrefix against that row, so they MUST NOT drift.
const (
	Name             = "whiteboard"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/whiteboard"
	BoardHTMLURL     = RoutePrefix + "/board.html"
	BoardJSURL       = RoutePrefix + "/board.js"
	BoardCSSURL      = RoutePrefix + "/board.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	ConfigScriptURL  = RoutePrefix + "/config.js"
	StreamURL        = RoutePrefix + "/room/stream"  // SSE: replay + live room events
	PublishURL       = RoutePrefix + "/room/publish" // POST: one update/presence relay
	DemoURL          = "/whiteboard"
	SchemaVersion    = "whiteboard-v1"

	// CapSyncRoom gates both room routes. It is OPTIONAL — it is NOT in
	// [DefaultCapabilities]. [WithRoomHub] appends it, because relaying
	// strokes to other browsers is egress the host explicitly turned on.
	CapSyncRoom = "sync:room"

	defaultDocID     = "demo"
	defaultMinHeight = "480px"
)

// RoomEvent kinds crossing the hub → subscriber direction.
const (
	EventHello        = "hello"        // host-assigned identity: {PID, Color, Count}
	EventSync         = "sync"         // one Yjs update blob: {Update}
	EventPresence     = "presence"     // one remote cursor: {PID, Color, X, Y, Down}
	EventParticipants = "participants" // room occupancy changed: {Count}
	EventPing         = "ping"         // SSE keepalive (never reaches the frame)
)

// DefaultCapabilities is the always-on grant set advertised to the board:
// bridging theme tokens. sync:room is deliberately absent — it is the
// optional capability [WithRoomHub] appends.
func DefaultCapabilities() []string {
	return []string{"theme:read"}
}

// RoomEvent is one collaboration payload a hub hands to a subscriber (or
// receives from a publisher). Updates are opaque Yjs blobs: the host never
// interprets them, which is exactly why relaying them is safe.
type RoomEvent struct {
	Kind   string
	PID    string
	Color  string
	X, Y   float64
	Down   bool
	Count  int
	Update []byte
}

// SubscribeFunc is the host-wired room membership behind GET StreamURL. It
// runs once per connected SSE consumer with a context cancelled on
// disconnect, and MUST:
//
//   - first yield a hello event assigning this subscriber its identity (an
//     opaque PID + colour — identity is the host's to decide; never a name),
//   - then replay the room's persisted Yjs state as sync events, so a joiner
//     converges instead of starting empty,
//   - then yield live events until the consumer goes away (return promptly
//     after ctx is done or yield returns an error).
//
// AUTHENTICATION is the hub's own responsibility: Allow passes for anonymous
// callers, so a host exposing a sensitive board must check the session before
// yielding anything.
type SubscribeFunc func(ctx context.Context, room string, yield func(RoomEvent) error) error

// PublishFunc is the host-wired relay behind POST PublishURL. fromPID is the
// publishing participant (assigned via its own hello); a hub fans the event
// out to every OTHER subscriber and, for sync events, merges it into the
// room's persisted state so late joiners receive it on replay.
type PublishFunc func(ctx context.Context, room, fromPID string, ev RoomEvent) error

// Plugin is the whiteboard plugin. It implements [framework.Plugin] and
// mirrors the datagrid/logstream shape: opaque-origin sandboxed iframe,
// protocol v1 over postMessage, go:embed'd frame bundle, capability-gated
// routes. The difference is the traffic direction of the product: updates
// leave the frame as opaque blobs and the HOST carries them to other
// browsers — the frame's own network stays forbidden.
type Plugin struct {
	devGrantAll  bool
	withDemoPage bool
	demoRoute    string
	capabilities []string
	subscribe    SubscribeFunc
	publish      PublishFunc
	manifest     pluginhost.Manifest
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithRoomHub wires the collaboration hub behind the room routes AND opts
// the plugin into the optional sync:room capability (appended to the grant
// set if not already present — the datagrid WithCellWriteHandler pattern).
// Without it there is no collaboration: the board still mounts and draws,
// but the routes fail closed and the frame shows "no room hub — local only".
func WithRoomHub(sub SubscribeFunc, pub PublishFunc) Option {
	return func(p *Plugin) { p.subscribe = sub; p.publish = pub }
}

// WithDevGrantAll short-circuits the capability gate (Phase-0 demo / tests).
// It bypasses BOTH gate sides, so the write route behind it must still fail
// closed on an unwired handler (a clear error, never a panic) — see
// handlers.go.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithCapabilities overrides the grant set advertised to the board. Default:
// [DefaultCapabilities]. sync:room is appended separately by [WithRoomHub]
// even when this fully replaces the set — the gate is on egress the host
// explicitly enabled, so silently dropping it would just break collaboration
// with a 403.
//
// Grants are matched with the framework's scope grammar at runtime, so
// wildcards are legal here: "sync:*" and "*:*" imply sync:room. [New]
// requires the hub for anything the grant set implies — a wildcard that
// implies sync:room without one is a construction panic, exactly like the
// literal capability.
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithDemoRoute overrides where [WithDemoPage] mounts the demo (default
// [DemoURL], "/whiteboard").
func WithDemoRoute(path string) Option { return func(p *Plugin) { p.demoRoute = path } }

// New constructs a Plugin. The platform manifest is built and Validate()'d
// here so a bad isolation/sandbox config aborts construction rather than
// silently de-opaquing the frame at runtime.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		manifest: pluginhost.Manifest{
			Entry:        BoardHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Whiteboard",
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
	// The hub option opts into its capability AFTER WithCapabilities so a
	// host that fully replaced the set and still wired a hub keeps the gate
	// passing (same ordering note as datagrid's optional caps).
	if p.subscribe != nil && p.publish != nil && !containsCap(p.capabilities, CapSyncRoom) {
		p.capabilities = append(p.capabilities, CapSyncRoom)
	}
	p.validateConfig()
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("whiteboard: invalid manifest: " + err.Error())
	}
	return p
}

// validateConfig is the fail-loud gate for the cross-field invariants [New]
// enforces. It panics on any violation so a bad config never reaches Init.
//
// The sync:room check uses the SAME wildcard grammar the runtime gate
// matches with (pluginhost.Allow → auth.ScopeMatch → access.ScopeMatch), so
// a wildcard grant ("sync:*", "*:*") implying sync:room requires the hub
// here too — string equality would let exactly those grants compile, pass
// the gate on the request, and then reach a nil hub behind the routes.
func (p *Plugin) validateConfig() {
	if (p.subscribe == nil) != (p.publish == nil) {
		panic("whiteboard: room hub is half-wired — supply both halves of WithRoomHub")
	}
	if p.grantsCapability(CapSyncRoom) && (p.subscribe == nil || p.publish == nil) {
		panic("whiteboard: sync:room granted but no room hub wired " +
			"(a wildcard grant like sync:* or *:* implies it) — supply WithRoomHub " +
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

// Init registers every asset and room route on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document + its JS/CSS
	// sub-resources. AssetServer applies the framing/CORP/CSP relaxation and
	// the fixed framedCSP (connect-src 'none', sandbox allow-scripts) to
	// exactly these — the frame can never open a connection, which is the
	// structural reason every update crosses the bridge.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "board.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "board.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "board.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) scripts: this instance's config.js (publishes
	// whether the hub was wired) and the adapter (relays bridge ↔ room).
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.AddBytes(ConfigScriptURL, "text/javascript; charset=utf-8", false, p.configScriptBytes())
	srv.Register(rt)

	rt.Get(StreamURL, http.HandlerFunc(p.handleStream))
	rt.Post(PublishURL, http.HandlerFunc(p.handlePublish))

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

// Capabilities returns the grant set this plugin advertises to the board.
func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

// allow is the capability gate, delegated to the platform [pluginhost.Allow]:
// default-deny against the plugin's granted set, intersected with the
// caller's own authority. It is NOT authentication — see the package doc.
func (p *Plugin) allow(r *http.Request, capability string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so
		// BOTH gate sides (plugin grant AND caller authority) are skipped.
		// Production hosts never set this; the default-deny gate below rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, capability)
}

// configScriptBytes renders the host-page config script that publishes this
// instance's optional-capability wiring as window.__gofastrWhiteboardConfig
// (the datagrid config.js pattern). The adapter merges it into the
// capabilities it registers with the platform broker — so the frame never
// offers collaboration the host did not wire. The frame still cannot grant
// itself anything: the room routes re-check the gate regardless of what the
// frame believes.
func (p *Plugin) configScriptBytes() []byte {
	syncEnabled := p.subscribe != nil && p.publish != nil
	if syncEnabled {
		return []byte("window.__gofastrWhiteboardConfig = { syncEnabled: true };\n")
	}
	return []byte("window.__gofastrWhiteboardConfig = { syncEnabled: false };\n")
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
	// DocID names the ROOM this board joins (default "demo"). Two mounts
	// with the same DocID collaborate; different DocIDs never see each other.
	DocID string
	// MinHeight is the initial iframe height before the frame settles.
	MinHeight string
}

// Mount renders the generic mount marker. A shared board has no canonical
// form field to round-trip — the room hub owns persistence, not the form —
// so the marker is the whole mount (the logstream shape). All interpolated
// values are HTML-escaped inside [pluginhost.MountMarker].
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
	})
}
