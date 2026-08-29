// Package sqlnotebook is the GoFastr SQL notebook plugin: a real SQLite
// engine (sql.js, SQLite compiled to WebAssembly) running INSIDE the
// opaque-origin sandboxed iframe, queried from a host-page notebook UI.
//
// The interesting constraint is the cage itself. The framed CSP fixes
// connect-src 'none', so the frame cannot fetch anything — not even its own
// engine. sql.js's documented locateFile option dies exactly there ("both
// async and sync fetching of the wasm failed"), so the architecture splits
// the engine in two the way pdf splits document bytes:
//
//   - sql-wasm.js (the JS glue) is a plain same-origin script the frame
//     loads by tag; it is loadable from the frame's own origin.
//   - sql-wasm.wasm (the engine) is fetched by the HOST adapter — which is
//     not framed and may fetch — and handed to the frame as BYTES over the
//     postMessage bridge. The frame calls initSqlJs({ wasmBinary }) and
//     never touches the network.
//
// Compiling the wasm at all needs the narrow CSP tier: this is the first
// plugin in the repo whose manifest declares CSP: ["'wasm-unsafe-eval'"].
// That keyword grants WebAssembly compilation and nothing else — string eval
// stays an EvalError, connect-src stays 'none', the origin stays opaque —
// and it only takes effect because [Plugin.Init] threads the manifest's own
// CSP slice into pluginhost.NewAssetServer(...).WithCSP (gofastr#300: a
// manifest that validates but is never threaded produces a frame whose wasm
// refuses to compile, silently). The regression test for that lives in
// plugin_test.go and asserts the SERVED header, not the manifest.
//
// The wire protocol is versioned and deliberately NOT the generic broker RPC
// (there is no host capability for the frame to call — it has no network and
// needs none). Host to frame: sqlnb/init carries the wasm bytes plus the
// seed SQL; sqlnb/query carries a query. Frame to host: sqlnb/ready,
// sqlnb/result (capped at 500 rows, truncated flag when the query produced
// more), sqlnb/error. Unknown types and mismatched versions are ignored
// silently on both sides. Results are computed in the frame and never
// persisted server-side: the notebook is session state, so this plugin
// registers no RPC routes at all — handlers.go is asset routes and the demo
// page only.
package sqlnotebook

import (
	"encoding/json"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js
// hard-code these exactly. The demo lives at /sqlnotebook.
//
// These mirror the sqlnotebook row in plugins.json; internal/registry tests
// pin Name + RoutePrefix against that row, so they MUST NOT drift.
const (
	Name          = "sqlnotebook"
	Version       = "0.1.0"
	SchemaVersion = "sqlnotebook.v1"
	RoutePrefix   = "/__gofastr/plugin/sqlnotebook"

	FrameHTMLURL     = RoutePrefix + "/frame.html"
	NotebookJSURL    = RoutePrefix + "/notebook.js"
	SqlWasmJSURL     = RoutePrefix + "/sql-wasm.js"
	SqlWasmWasmURL   = RoutePrefix + "/sql-wasm.wasm"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	DemoURL          = "/sqlnotebook"

	defaultDocID     = "demo"
	defaultMinHeight = "360px"
)

// DefaultCapabilities is the always-on grant set advertised to the frame:
// theme:read only, matching the adapter's broker registration exactly. The
// database lives inside the frame; no host resource is ever pulled, so even
// document:read would be a promise of nothing — the seed and the engine bytes
// are PUSHED in over sqlnb/init, and results never leave the frame's page.
func DefaultCapabilities() []string {
	return []string{"theme:read"}
}

// DefaultSeed is the SQL the frame runs at init before the first query, so
// the notebook opens on a table that exists rather than an empty engine. It
// seeds a small plugins table mirroring this repo's real registry rows
// (name + isolation, values as plugins.json has them), which gives the demo
// something true to query: a two-value isolation column worth grouping by.
// Kept ASCII and well under 15 rows on purpose.
const DefaultSeed = `CREATE TABLE plugins (
  name      TEXT PRIMARY KEY,
  isolation TEXT NOT NULL
);
INSERT INTO plugins (name, isolation) VALUES
  ('richtext', 'sandbox-iframe-opaque'),
  ('mermaid', 'sandbox-iframe-opaque'),
  ('monaco', 'sandbox-iframe-opaque'),
  ('pdf', 'sandbox-iframe-opaque'),
  ('datagrid', 'sandbox-iframe-opaque'),
  ('chart', 'sandbox-iframe-opaque'),
  ('calendar', 'sandbox-iframe-opaque'),
  ('whiteboard', 'sandbox-iframe-opaque'),
  ('scanner', 'sandbox-iframe-opaque'),
  ('genui', 'sandbox-iframe-opaque'),
  ('tour', 'trusted-host-page'),
  ('map', 'trusted-host-page');`

// Plugin is the SQL notebook plugin. It implements [framework.Plugin] and
// mirrors the pdf/calendar shape (opaque-origin sandboxed iframe, go:embed'd
// frame bundle, capability-gated grant set) with one first in the repo: the
// manifest declares the 'wasm-unsafe-eval' CSP tier, and Init threads it
// into the AssetServer so the frame can actually compile SQLite.
type Plugin struct {
	manifest     pluginhost.Manifest
	capabilities []string
	seed         string
	devGrantAll  bool
	withDemoPage bool
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithSeed overrides the SQL the frame executes at init (default:
// [DefaultSeed]). The frame runs it as a multi-statement script before the
// first query, so the notebook opens on tables the seed created. The value
// reaches the frame through the demo page's mount marker (data-fui-plugin-doc,
// JSON-encoded); a host mounting its own markers passes the seed per mount
// via [MountConfig.Seed]. An empty or whitespace-only seed panics at [New]:
// it is almost always a bug (a source that failed to load), and an engine
// with no schema is a blank page wearing a SQL badge. A host that truly
// wants an empty engine can seed a comment.
func WithSeed(sql string) Option {
	return func(p *Plugin) { p.seed = sql }
}

// WithDevGrantAll short-circuits the capability gate (Phase-0 demo / tests).
// This plugin mounts no capability-gated RPC routes — the frame exchanges
// data only over the postMessage bridge and the asset routes are public
// static bytes — so the flag currently gates nothing; it is kept for harness
// parity with every other plugin here and for the routes a persistence layer
// would add.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// New constructs a Plugin. Validation runs here so a misconfiguration aborts
// construction rather than mounting a frame whose engine cannot compile.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		seed:         DefaultSeed,
		manifest: pluginhost.Manifest{
			Entry:     FrameHTMLURL,
			Isolation: pluginhost.IsolationSandboxOpaque,
			Sandbox:   []string{pluginhost.DefaultSandbox},
			// The narrow wasm tier: WebAssembly compilation inside the frame,
			// nothing else. Declaring it is necessary but NOT sufficient —
			// see Init, which threads this exact slice into the AssetServer.
			CSP:          []string{"'wasm-unsafe-eval'"},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "SQL notebook",
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	p.validateConfig()
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("sqlnotebook: invalid manifest: " + err.Error())
	}
	return p
}

// validateConfig is the fail-loud gate for the invariants [New] enforces.
// It panics on any violation so a bad config never reaches Init — same
// posture as [pluginhost.Manifest.Validate].
func (p *Plugin) validateConfig() {
	if strings.TrimSpace(p.seed) == "" {
		panic("sqlnotebook: WithSeed requires non-empty SQL (seed a comment if the engine must start empty)")
	}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Seed returns the SQL the frame executes at init.
func (p *Plugin) Seed() string { return p.seed }

// Capabilities returns the grant set this plugin advertises to the frame.
func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

// UIHostOption injects the platform broker and this plugin's adapter (the
// broker first — the adapter registers with it). There is no config.js: the
// only instance state, the seed, rides the mount marker (see [Mount]), which
// is the channel the adapter reads.
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, AdapterScriptURL)
}

// MountConfig configures [Mount].
type MountConfig struct {
	DocID     string // mount key (default "demo"); the notebook has no persisted doc to round-trip
	Seed      string // SQL the frame executes at init; empty means the adapter's built-in default seed
	MinHeight string // initial iframe height before first resize (default "360px")
}

// Mount renders the generic mount marker. The host adapter scans it, builds
// the sandboxed frame, fetches the wasm bytes same-origin and relays them
// with the seed over the bridge.
//
// The seed rides the marker's generic data-fui-plugin-doc attribute,
// JSON-encoded so the adapter decodes it unambiguously (it also accepts raw
// SQL and {"sql": ...}, but the JSON string is the form this side emits).
// The demo page passes [Plugin.Seed] here, which is how [WithSeed] reaches
// the frame; a host mounting its own markers passes MountConfig.Seed per
// mount. An empty Seed produces no attribute, and the adapter then falls
// back to its own built-in default so the notebook is never empty. There is
// deliberately no hidden form field: the notebook is session state, nothing
// about it POSTs back. All interpolated values are HTML-escaped via
// [render.Escape] inside [pluginhost.MountMarker].
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.MinHeight == "" {
		cfg.MinHeight = defaultMinHeight
	}
	doc := ""
	if strings.TrimSpace(cfg.Seed) != "" {
		b, err := json.Marshal(cfg.Seed)
		if err != nil {
			// A plain string; marshal cannot fail in practice. Fail loud
			// rather than mount a marker whose seed silently drops.
			panic("sqlnotebook: marshal seed doc: " + err.Error())
		}
		doc = string(b)
	}
	return pluginhost.MountMarker(pluginhost.MountConfig{
		Plugin:    Name,
		DocID:     cfg.DocID,
		MinHeight: cfg.MinHeight,
		Doc:       doc,
	})
}
