// Package calendar is the GoFastr calendar plugin: a month/week/day calendar
// written from scratch — no FullCalendar, no upstream JS library at all —
// where the parts everyone gets wrong live in Go, not in the frame.
//
// Why this shape: wrapping a library proves the wrapper works, not the
// platform. A calendar is the sharpest available test of that claim because
// its hard parts are all server-shaped: RRULE expansion, timezone
// conversion and conflict detection are correctness questions, and the
// frame is an untrusted rendering surface. So the split is structural:
//
//   - The events source (host-supplied, like datagrid's rows source) owns
//     the event definitions — wall-clock strings plus an IANA zone, never
//     instants. The frame never receives an RRULE; it cannot mis-expand a
//     rule it is never told.
//   - The plugin expands, resolves and conflict-checks (rrule.go,
//     zones.go, occurrence.go) and ships the frame resolved occurrences
//     with explicit instants AND wall clocks.
//   - The frame sends INTENTS, not results: "move occurrence X by N wall
//     minutes" goes to POST /move; the host re-resolves through the zone
//     and returns what actually happened — including a wall-clock delta
//     different from the one dragged when the target lands in a
//     spring-forward gap or a fall-back fold. The frame renders the answer.
//
// The canonical doc (schema calendar-v1) is VIEW STATE ONLY: {view:{date,
// mode}}. Like datagrid's, it round-trips through the hidden form field;
// the data never enters it. Per-instance edits (moves) are stored as
// overrides in the plugin's store — keyed by event + ORIGINAL series date,
// so moving one occurrence of a series never touches the series.
//
// Capabilities: document:read (occurrence reads), document:write (moves +
// view-state saves) and theme:read, per the platform's resource:verb
// grammar. pluginhost.Allow is a capability gate, NOT authentication: it
// passes for anonymous callers, so both write routes rely on the host's own
// move/save handlers to check the session in production mounts. See
// docs/calendar.md.
package calendar

import (
	"context"
	"net/http"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/access"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js
// hard-code these exactly (protocol-v1.md §2/§10). The demo lives at
// /calendar.
//
// These mirror the calendar row in plugins.json; internal/registry tests pin
// Name + RoutePrefix against that row, so they MUST NOT drift.
const (
	Name             = "calendar"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/calendar"
	AppHTMLURL       = RoutePrefix + "/app.html"
	AppJSURL         = RoutePrefix + "/app.js"
	AppCSSURL        = RoutePrefix + "/app.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	OccurrencesURL   = RoutePrefix + "/occurrences"
	MoveURL          = RoutePrefix + "/move"
	SaveURL          = RoutePrefix + "/save"
	DemoURL          = "/calendar"
	SchemaVersion    = "calendar-v1"

	CapDocRead   = "document:read"
	CapDocWrite  = "document:write"
	CapThemeRead = "theme:read"

	defaultDocID     = "demo"
	defaultDocField  = "calendar_doc"
	defaultMinHeight = "620px"
)

// DefaultCapabilities is the always-on grant set: reading occurrences,
// sending move intents + saving view state, and bridging theme tokens.
// Unlike datagrid there are no handler-gated optional capabilities — the
// move and save hooks have in-memory defaults — so the set is fixed.
func DefaultCapabilities() []string {
	return []string{CapDocRead, CapDocWrite, CapThemeRead}
}

// EventsSource supplies the calendar's event definitions. It runs in the
// host process; the returned slice is validated on every request
// (ValidateEvent) and never trusted past the first bad definition. There is
// no default: a calendar with no events source is a museum exhibit, so
// [New] panics without this option — the datagrid WithRowsSource rule.
type EventsSource func(ctx context.Context) ([]Event, error)

// MoveHandler persists a per-instance override after the plugin has
// resolved it. The default keeps overrides in memory (enough for the demo
// and for tests that reload the page); a production host wires its database
// here and checks the SESSION first — pluginhost.Allow is a capability
// gate, not authentication, and passes for anonymous callers.
type MoveHandler func(ctx context.Context, ov Override) error

// SaveHandler persists the view-state doc. Default: in-memory map keyed by
// DocID (the datagrid memSave pattern).
type SaveHandler func(ctx context.Context, req SaveRequest) error

// Plugin is the calendar plugin. It implements [framework.Plugin] and
// mirrors the datagrid/mermaid shape: opaque-origin sandboxed iframe,
// protocol v1 over postMessage, go:embed'd frame bundle, capability gate,
// host-side RPC routes.
type Plugin struct {
	capabilities []string
	manifest     pluginhost.Manifest

	eventsSource EventsSource
	moveHandler  MoveHandler
	saveHandler  SaveHandler
	demoDoc      *Doc
	withDemoPage bool
	demoRoute    string
	devGrantAll  bool

	mu         sync.RWMutex
	docs       map[string]savedDoc
	overrides  map[overrideKey]Override
	eventsOnce sync.Once
	cachedEvts []Event
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithEventsSource installs the server-side event source. REQUIRED — [New]
// panics without it.
func WithEventsSource(fn EventsSource) Option {
	return func(p *Plugin) { p.eventsSource = fn }
}

// WithMoveHandler overrides the override-persistence hook (default:
// in-memory map). This is the host's chance to authorize the edit against
// the real session and write it to real storage.
func WithMoveHandler(fn MoveHandler) Option {
	return func(p *Plugin) { p.moveHandler = fn }
}

// WithSaveHandler overrides the view-state persistence hook (default:
// in-memory map keyed by DocID).
func WithSaveHandler(fn SaveHandler) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// WithDemoDoc sets the view state the demo page mounts when nothing has
// been saved yet.
func WithDemoDoc(doc Doc) Option {
	return func(p *Plugin) { d := doc; p.demoDoc = &d }
}

// WithDevGrantAll short-circuits the capability gate (Phase-0 demo / tests).
// It bypasses BOTH gate sides, so the write routes behind it must still fail
// closed on invalid input (a clear error, never a panic) — see handlers.go.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithCapabilities overrides the grant set advertised to the frame. Default:
// [DefaultCapabilities]. Grants are matched with the framework's wildcard
// scope grammar at runtime, so "document:*" and "*:*" are legal here.
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithDemoRoute overrides where [WithDemoPage] mounts the demo (default
// [DemoURL], "/calendar").
func WithDemoRoute(path string) Option { return func(p *Plugin) { p.demoRoute = path } }

func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]savedDoc),
		overrides:    make(map[overrideKey]Override),
		manifest: pluginhost.Manifest{
			Entry:        AppHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Calendar",
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
	if p.moveHandler == nil {
		p.moveHandler = p.memMove
	}
	if p.saveHandler == nil {
		p.saveHandler = p.memSave
	}
	p.validateConfig()
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("calendar: invalid manifest: " + err.Error())
	}
	return p
}

// validateConfig is the fail-loud gate for the cross-field invariants [New]
// enforces. It panics on any violation so a bad config never reaches Init —
// same posture as [pluginhost.Manifest.Validate] and datagrid's
// validateConfig.
func (p *Plugin) validateConfig() {
	if p.eventsSource == nil {
		panic("calendar: no events source configured — supply WithEventsSource " +
			"(a calendar with no server-side events has nothing for Go to be right about)")
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
// [Plugin.allow] enforces at request time (pluginhost.Allow →
// auth.ScopeMatch → access.ScopeMatch). Construction checks and the runtime
// gate must agree; see datagrid's grantsCapability for the wildcard-grant
// history behind that rule.
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
	// sub-resources. AssetServer applies the framing/CORP/CSP relaxation
	// and the fixed framedCSP (connect-src 'none', sandbox allow-scripts)
	// to exactly these — the frame can never fetch anything, which is why
	// every occurrence crosses the bridge.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "app.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "app.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "app.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) script: the adapter. No config.js — there are
	// no handler-gated optional capabilities to publish.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.Register(rt)

	// RPC routes. Each gates on its capability; see handlers.go for the
	// per-route enforcement notes.
	rt.Post(OccurrencesURL, http.HandlerFunc(p.handleOccurrences))
	rt.Post(MoveURL, http.HandlerFunc(p.handleMove))
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))

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

// events resolves the events source once per process and caches it: the
// demo's source is a pure function of nothing, and re-validating identical
// definitions on every window request is waste. A source that ERRORS is not
// cached — the next request retries it.
func (p *Plugin) events(ctx context.Context) ([]Event, error) {
	p.eventsOnce.Do(func() {
		evts, err := p.eventsSource(ctx)
		if err != nil {
			return // leave cache empty; fall through to a live call below
		}
		p.cachedEvts = evts
	})
	if p.cachedEvts != nil {
		return p.cachedEvts, nil
	}
	return p.eventsSource(ctx)
}

// currentOverrides snapshots the override store for a read.
func (p *Plugin) currentOverrides() map[overrideKey]Override {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[overrideKey]Override, len(p.overrides))
	for k, v := range p.overrides {
		out[k] = v
	}
	return out
}

// memMove is the default override-persistence hook: in-memory map keyed by
// (event, original series date) — identity-stable, series-untouched.
func (p *Plugin) memMove(_ context.Context, ov Override) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.overrides[ov.key()] = ov
	return nil
}

// memSave is the default view-state persistence hook.
func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs[req.DocID] = savedDoc{DocJSON: req.DocJSON}
	return nil
}

// savedDoc is the in-memory persisted view state.
type savedDoc struct {
	DocJSON string
}

// LoadDoc returns the last-saved view-state JSON for docID (demo round-trip).
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

// Overrides returns the recorded per-instance edits (demo/test surface; the
// frame never sees this store directly).
func (p *Plugin) Overrides() []Override {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Override, 0, len(p.overrides))
	for _, ov := range p.overrides {
		out = append(out, ov)
	}
	return out
}

// Capabilities returns the grant set this plugin advertises to the frame.
func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

// allow is the capability gate, delegated to the platform [pluginhost.Allow]:
// default-deny against the plugin's granted set, intersected with the
// caller's own authority (a scoped token restricts below the grant; a
// session caller is bound by the grant alone). It is NOT authentication —
// see the package doc.
func (p *Plugin) allow(r *http.Request, capability string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so
		// BOTH gate sides (plugin grant AND caller authority) are skipped.
		// Production hosts never set this; the default-deny gate rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, capability)
}

// UIHostOption injects the platform broker and this plugin's adapter (the
// broker first — the adapter registers with it).
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, AdapterScriptURL)
}

// MountConfig configures [Mount].
type MountConfig struct {
	// DocID is the persistence key for the view-state doc.
	DocID string
	// Doc is the initial view-state doc JSON server-rendered into the
	// marker (data-fui-plugin-doc) — the frame reads view.date/mode from
	// it on init.
	Doc string
	// Field is the hidden-input name the adapter mirrors the current
	// view-state doc into on docChanged. Defaults to "calendar_doc".
	Field string
	// MinHeight is the initial iframe height before the calendar sizes
	// itself.
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
		// the doc into the field THIS mount named, not a hard-coded one —
		// the datagrid rule.
		Attributes: []pluginhost.Attribute{
			{Name: "data-fui-plugin-field", Value: cfg.Field},
		},
		Fields: []pluginhost.Field{
			{Name: cfg.Field},
		},
	})
}
