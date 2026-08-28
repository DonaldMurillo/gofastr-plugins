// Package datagrid is the GoFastr data-grid plugin: AG Grid Community's
// infinite row model inside an opaque-origin sandboxed iframe, with sort,
// filter and paging executed by the HOST's Go rows source instead of the
// frame.
//
// Why this shape: every other plugin in this repo moves ONE small document
// across the postMessage bridge. A grid moves rows by the thousand, and the
// framed CSP sets connect-src 'none' (the frame can never fetch its own rows),
// so the plugin is only interesting if the data arrives page-by-page from the
// host. Server-side sort/filter/paging over a correlated fire-and-forget event
// pair — requestRows → rowsResult, exactly the richtext requestUpload →
// uploadResult pattern — IS the product here; a grid that loads all rows up
// front would prove nothing about the platform.
//
// The canonical doc (schema datagrid-v1) is VIEW STATE ONLY:
// {columns[], sort, filter, pageSize}. Rows are never part of the doc, never
// saved into it and never echoed back out of it — the doc round-trips through
// the hidden form field like every other plugin's, while the data keeps
// flowing one page per bridge round trip.
//
// Capabilities: data:read + theme:read are always granted; data:write (cell
// edits and view-state saves) and data:export (CSV egress) are optional,
// granted exactly when the host supplies the matching handler option — the
// same shape as pdf's pdf:export. pluginhost.Allow is a capability gate, NOT
// authentication: it passes for anonymous callers, so any route that WRITES
// (POST /cell) must be documented as requiring the host to check the session
// in its own handler. See docs/datagrid.md.
package datagrid

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/access"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js hard-code
// these exactly (protocol-v1.md §2/§10). The demo lives at /datagrid.
//
// These mirror the datagrid row in plugins.json; internal/registry tests pin
// Name + RoutePrefix against that row, so they MUST NOT drift.
const (
	Name             = "datagrid"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/datagrid"
	GridHTMLURL      = RoutePrefix + "/grid.html"
	GridJSURL        = RoutePrefix + "/grid.js"
	GridCSSURL       = RoutePrefix + "/grid.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	ConfigScriptURL  = RoutePrefix + "/config.js"
	RowsURL          = RoutePrefix + "/rows"
	CellWriteURL     = RoutePrefix + "/cell"
	ExportURL        = RoutePrefix + "/export"
	SaveURL          = RoutePrefix + "/save"
	DemoURL          = "/datagrid"
	SchemaVersion    = "datagrid-v1"

	// CapDataWrite gates POST /cell and POST /save. It is OPTIONAL — it is
	// NOT in [DefaultCapabilities]. [WithCellWriteHandler] appends it (the
	// pdf WithExportHandler pattern), because editing rows is egress the
	// host explicitly turned on.
	CapDataWrite = "data:write"

	// CapDataExport gates POST /export. Optional for the same reason: CSV
	// export produces bytes on the host (a sandboxed frame cannot start a
	// download), and [WithExportHandler] is what turns that on.
	CapDataExport = "data:export"

	defaultDocID     = "demo"
	defaultDocField  = "datagrid_doc"
	defaultMinHeight = "560px"

	// defaultPageSize is the bridge page size the frame requests when its
	// doc carries none. 100 matches AG Grid's cacheBlockSize default.
	defaultPageSize = 100
)

// DefaultCapabilities is the always-on grant set advertised to the grid:
// reading pages of rows and bridging theme tokens. data:write and data:export
// are deliberately absent — they are optionalCapabilities, appended by
// [WithCellWriteHandler] / [WithExportHandler].
func DefaultCapabilities() []string {
	return []string{"data:read", "theme:read"}
}

// Column describes one grid column. Columns are part of the canonical
// (view-state) doc: the host declares the schema, the frame renders it, and
// the rows requests carry it back so the host's rows source knows which
// fields are numeric when sorting server-side.
type Column struct {
	Field  string `json:"field"`
	Header string `json:"header"`
	// Width is the initial column width in px (0 = AG Grid default).
	Width int `json:"width,omitempty"`
	// Type is "text" (default) or "number". Numbers right-align in the
	// frame and sort numerically in the host's rows source.
	Type string `json:"type,omitempty"`
	// Sortable / Editable toggle the obvious frame affordances. Sortable
	// columns trigger server-side sorts; editable ones (only when data:write
	// is granted) open a cell editor whose result round-trips through
	// POST /cell.
	Sortable bool `json:"sortable,omitempty"`
	// Filterable is accepted for symmetry with the doc shape; the demo
	// filter box filters every column, so it is currently unused by the
	// frame.
	Filterable bool `json:"filterable,omitempty"`
	Editable   bool `json:"editable,omitempty"`
}

// SortModel is one active server-side sort: a column field plus a direction.
type SortModel struct {
	Field string `json:"field"`
	Dir   string `json:"dir"` // "asc" | "desc"
}

// Doc is the canonical datagrid-v1 view-state document. It describes HOW the
// table is viewed — never the rows themselves.
type Doc struct {
	SchemaVersion string      `json:"schemaVersion"`
	Columns       []Column    `json:"columns"`
	Sort          []SortModel `json:"sort,omitempty"`
	Filter        string      `json:"filter,omitempty"`
	PageSize      int         `json:"pageSize,omitempty"`
}

// RowsQuery is one server-side page request, the Go twin of the frame's
// requestRows params. Columns ride along so the source can sort typed columns
// correctly (numbers numerically) without the plugin hard-coding a schema.
type RowsQuery struct {
	DocID    string
	StartRow int
	EndRow   int
	Sort     []SortModel
	Filter   string
	Columns  []Column
}

// Row is one data row as it crosses the bridge: a stable id plus string
// cells keyed by column field. Cell typing is a host concern — the bridge
// carries display strings, and the rows source sorts its own typed data.
type Row struct {
	ID    string            `json:"id"`
	Cells map[string]string `json:"cells"`
}

// RowsPage is one server-side page. LastRow is the total row count under the
// current sort/filter, or -1 when unknown (AG Grid's infinite model contract).
type RowsPage struct {
	Rows    []Row `json:"rows"`
	LastRow int   `json:"lastRow"`
}

// CellWriteRequest is one cell edit relayed from the frame via POST /cell.
type CellWriteRequest struct {
	DocID string
	RowID string
	Field string
	Value string
}

// ExportRequest is handed to the host's export handler: the CSV the plugin
// generated host-side from the rows source under the request's sort/filter,
// plus the view-state context. The handler stores the CSV (io.Copy or
// io.ReadAll into whatever storage the host uses) and returns a URL the host
// page can download from — the frame cannot download.
type ExportRequest struct {
	DocID  string
	Format string // always "csv" today
	// Columns the export covers, normalised by /export before the scan.
	Columns []Column
	Sort    []SortModel
	Filter  string
	// CSV streams the export body. The scan pages through the rows source
	// in 5,000-row chunks and spills to a temp file as it goes, so peak
	// memory stays at one chunk regardless of table size, and a mid-scan
	// source error aborts the whole export (an error response, never a
	// short file). The handler MUST fully consume CSV before returning —
	// the underlying temp file is removed once the handler returns.
	CSV io.Reader
	// RowCount is the number of data rows in CSV (the header is excluded).
	RowCount int
}

// SaveRequest is the view-state persist signal (POST /save).
type SaveRequest struct {
	DocID string
	Doc   Doc
	// DocJSON is the canonical JSON of the VALIDATED, normalised doc — the
	// same shape /save hands the save handler. The raw request body's JSON
	// is never persisted: a doc that failed a bound (page size, sort keys,
	// filter length) cannot be saved verbatim and reloaded later.
	DocJSON       string
	SchemaVersion string
}

// savedDoc is the in-memory persisted view state (the demo / default store).
type savedDoc struct {
	DocJSON string
}

// Plugin is the data-grid plugin. It implements [framework.Plugin] and
// mirrors the mermaid/pdf shape: opaque-origin sandboxed iframe, protocol v1
// over postMessage, go:embed'd frame bundle, capability gate, host-side RPC
// routes. The difference is the traffic profile — many small correlated
// event round trips instead of one document push.
type Plugin struct {
	capabilities   []string
	manifest       pluginhost.Manifest
	devGrantAll    bool
	withDemoPage   bool
	demoRoute      string
	demoDoc        *Doc
	rowsSource     func(ctx context.Context, q RowsQuery) (RowsPage, error)
	cellWriteHandl func(ctx context.Context, req CellWriteRequest) error
	exportHandler  func(ctx context.Context, req ExportRequest) (string, error)
	saveHandler    func(ctx context.Context, req SaveRequest) error

	mu   sync.RWMutex
	docs map[string]savedDoc
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithRowsSource installs the server-side data source backing POST /rows and
// the CSV export scan. There is NO default: unlike pdf (which ships a sample
// document), a grid has no meaningful embedded dataset, so [New] panics
// without this option. The function runs in the host process with the
// caller's context — sorting, filtering and paging are the point, so the
// source must apply them, not return everything.
func WithRowsSource(fn func(ctx context.Context, q RowsQuery) (RowsPage, error)) Option {
	return func(p *Plugin) { p.rowsSource = fn }
}

// WithCellWriteHandler installs the persistence hook behind POST /cell AND
// opts the plugin into the optional data:write capability (appended to the
// grant set if not already present — the pdf WithExportHandler pattern).
// The handler is the host's chance to authorize the write against the real
// session: pluginhost.Allow is a capability gate, not authentication, and it
// passes for anonymous callers.
func WithCellWriteHandler(fn func(ctx context.Context, req CellWriteRequest) error) Option {
	return func(p *Plugin) { p.cellWriteHandl = fn }
}

// WithExportHandler installs the export hook behind POST /export AND opts
// the plugin into the optional data:export capability. The plugin generates
// the CSV host-side from the rows source (streamed through [ExportRequest]'s
// CSV reader); the handler stores it and returns a URL the host page turns
// into a download.
func WithExportHandler(fn func(ctx context.Context, req ExportRequest) (string, error)) Option {
	return func(p *Plugin) { p.exportHandler = fn }
}

// WithSaveHandler overrides the view-state persistence hook. The default
// stores the canonical doc JSON in an in-memory map keyed by DocID.
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// WithDemoDoc sets the view state the demo page mounts when nothing has been
// saved yet — the demo's column set, since the plugin itself owns no schema.
func WithDemoDoc(doc Doc) Option {
	return func(p *Plugin) { d := doc; p.demoDoc = &d }
}

// WithDevGrantAll short-circuits the capability gate (Phase-0 demo / tests).
// It bypasses BOTH gate sides, so the write routes behind it must still fail
// closed on an unwired handler (a clear error, never a panic) — see
// handlers.go.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// WithCapabilities overrides the grant set advertised to the grid. Default:
// [DefaultCapabilities]. data:write / data:export are appended separately by
// their handler options even when this fully replaces the set — the gate is
// on egress the host explicitly enabled, so silently dropping it would just
// break editing with a 403.
//
// Grants are matched with the framework's scope grammar at runtime, so
// wildcards are legal here: "data:*" and "*:*" imply data:write and
// data:export. [New] requires the matching handler for anything the grant
// set implies — a wildcard that implies an optional capability without its
// handler is a construction panic, exactly like the literal capability.
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage registers the self-contained themed demo page at [DemoURL].
func WithDemoPage() Option { return func(p *Plugin) { p.withDemoPage = true } }

// WithDemoRoute overrides where [WithDemoPage] mounts the demo (default
// [DemoURL], "/datagrid").
func WithDemoRoute(path string) Option { return func(p *Plugin) { p.demoRoute = path } }
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]savedDoc),
		manifest: pluginhost.Manifest{
			Entry:        GridHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Data grid",
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
	// The handler options opt into their capabilities AFTER WithCapabilities
	// so a host that fully replaced the set and still wired handlers keeps
	// the gates passing (same ordering note as pdf's CapPDFExport).
	if p.cellWriteHandl != nil && !containsCap(p.capabilities, CapDataWrite) {
		p.capabilities = append(p.capabilities, CapDataWrite)
	}
	if p.exportHandler != nil && !containsCap(p.capabilities, CapDataExport) {
		p.capabilities = append(p.capabilities, CapDataExport)
	}
	p.validateConfig()
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("datagrid: invalid manifest: " + err.Error())
	}
	return p
}

// validateConfig is the fail-loud gate for the cross-field invariants [New]
// enforces. It panics on any violation so a bad config never reaches Init —
// same posture as [pluginhost.Manifest.Validate] and pdf's validateConfig.
func (p *Plugin) validateConfig() {
	if p.rowsSource == nil {
		panic("datagrid: no rows source configured — supply WithRowsSource " +
			"(unlike pdf's embedded sample, a grid has no meaningful default dataset)")
	}
	// A granted capability with no handler behind it is a silent hole: the
	// frame would offer editing/export, the user would do the work, and the
	// route would fail at nil-deref (or worse, succeed doing nothing). Fail
	// at construction instead. The check uses the SAME wildcard grammar the
	// runtime gate matches with (pluginhost.Allow → auth.ScopeMatch →
	// access.ScopeMatch), so a wildcard grant ("data:*", "*:*") implying
	// data:write / data:export requires its handler here too — string
	// equality here would let exactly those wildcard grants compile, pass
	// the gate on the request, and then reach a nil handler.
	if p.grantsCapability(CapDataWrite) && p.cellWriteHandl == nil {
		panic("datagrid: data:write granted but no cell write handler " +
			"(a wildcard grant like data:* implies it) — supply WithCellWriteHandler " +
			"or drop the capability from WithCapabilities")
	}
	if p.grantsCapability(CapDataExport) && p.exportHandler == nil {
		panic("datagrid: data:export granted but no export handler " +
			"(a wildcard grant like data:* implies it) — supply WithExportHandler " +
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
// [Plugin.allow] enforces at request time (pluginhost.Allow →
// auth.ScopeMatch → access.ScopeMatch). Construction checks and the runtime
// gate must agree, or wildcard grants slip between them: string equality
// here let WithCapabilities("data:*") compile, pass the gate on the request,
// and then reach a nil handler behind /cell and /export.
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
	// to exactly these — the frame can never fetch its own rows, which is
	// the structural reason every page crosses the bridge.
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "grid.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "grid.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "grid.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) scripts: the adapter and this instance's
	// config.js, which publishes whether the optional capabilities were
	// wired so the adapter can merge them into the manifest capabilities
	// it registers with the generic broker.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.AddBytes(ConfigScriptURL, "text/javascript; charset=utf-8", false, p.configScriptBytes())
	srv.Register(rt)

	// RPC routes. Each gates on its capability; see handlers.go for the
	// per-route enforcement notes.
	rt.Post(RowsURL, http.HandlerFunc(p.handleRows))
	rt.Post(CellWriteURL, http.HandlerFunc(p.handleCellWrite))
	rt.Post(ExportURL, http.HandlerFunc(p.handleExport))
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

// Capabilities returns the grant set this plugin advertises to the grid.
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

// memSave is the default view-state persistence hook: an in-memory map keyed
// by DocID, storing the validated canonical doc JSON /save produced.
func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs[req.DocID] = savedDoc{DocJSON: req.DocJSON}
	return nil
}

// configScriptBytes renders the host-page config script that publishes this
// plugin instance's optional-capability wiring as
// window.__gofastrDatagridConfig. The adapter (loaded after it via
// [UIHostOption] / the demo page) merges it into the capabilities it
// registers with the platform broker, which the generic broker then bridges
// to the frame as init.capabilities — so the frame never offers editing or
// export the host did not wire. The frame still cannot grant itself
// anything: POST /cell and /export re-check the gate regardless of what
// the frame believes.
func (p *Plugin) configScriptBytes() []byte {
	// grantsCapability, not containsCap: a wildcard grant that implies the
	// capability must advertise it too (and construction guaranteed the
	// handler exists).
	writeEnabled := p.grantsCapability(CapDataWrite)
	exportEnabled := p.grantsCapability(CapDataExport)
	// Hand-rolled two-field JSON (a json.Marshal call would pull
	// encoding/json into this file for two booleans; the values are
	// compile-time bools, so nothing here can need escaping).
	write := "false"
	if writeEnabled {
		write = "true"
	}
	export := "false"
	if exportEnabled {
		export = "true"
	}
	return []byte("window.__gofastrDatagridConfig={\"writeEnabled\":" + write +
		",\"exportEnabled\":" + export + "}\n")
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
	// DocID is the persistence key for the view-state doc.
	DocID string
	// Doc is the initial view-state doc JSON server-rendered into the
	// marker (data-fui-plugin-doc) — the frame reads columns, sort, filter
	// and pageSize out of it on init.
	Doc string
	// Field is the hidden-input name the adapter mirrors the current
	// view-state doc into on docChanged, so a normal form POST round-trips
	// it. Defaults to "datagrid_doc". The name is published on the marker
	// as data-fui-plugin-field, which is how the adapter finds it — the
	// input itself is rendered after the marker, not inside it.
	Field string
	// MinHeight is the initial iframe height before the grid sizes itself.
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
		// a custom Field must not silently lose its view state on submit.
		Attributes: []pluginhost.Attribute{
			{Name: "data-fui-plugin-field", Value: cfg.Field},
		},
		Fields: []pluginhost.Field{
			{Name: cfg.Field},
		},
	})
}
