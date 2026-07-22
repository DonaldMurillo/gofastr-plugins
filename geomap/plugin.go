package geomap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. The Go package / directory is `geomap` (the
// identifier `map` is a Go keyword), but the user-facing identity strings are
// "map" (Name, route prefix, demo URL, schema). Both this file and
// host/adapter.js hard-code these exactly. The demo lives at /map so it
// co-mounts with the other plugin demos without colliding on "/".
const (
	Name             = "map"
	Version          = "0.1.0-phase0"
	RoutePrefix      = "/__gofastr/plugin/map"
	MapHTMLURL       = RoutePrefix + "/map.html"
	MapJSURL         = RoutePrefix + "/map.js"
	MapCSSURL        = RoutePrefix + "/map.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	// ConfigScriptURL is a tiny host-page script that publishes this plugin
	// instance's MapConfig (set via Go options) as a global the adapter merges
	// into the manifest config it registers with the platform broker. Served
	// non-framed (host-page) and static per plugin instance.
	ConfigScriptURL = RoutePrefix + "/config.js"
	SaveURL         = RoutePrefix + "/save"
	// TilesRoutePattern is the same-origin tile-proxy route. It MUST be
	// same-origin so the opaque-origin frame can load raster tiles as <img>
	// under its CSP (img-src <origin> data:). External tile hosts are blocked
	// by that CSP; the proxy fetches upstream server-side from a fixed
	// allowlist (the SSRF guard). See tiles.go.
	TilesRoutePattern = RoutePrefix + "/tiles/{provider}/{z}/{x}/{y}"
	DemoURL           = "/map"
	SchemaVersion     = "map-v1"

	defaultDocID     = "demo"
	defaultDocField  = "map_doc"
	defaultMinHeight = "360px"
)

// DefaultCapabilities is the grant set advertised to the editor. Geomap has no
// upload path — only document read/write + theme:read (same as monaco).
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

// mapDoc is the canonical map-v1 document. The json tags are load-bearing:
// LoadDoc marshals this into the mount marker's data-fui-plugin-doc, and the
// frame's deriveDoc reads lowercase {lat,lng,zoom,markers}. Without the tags Go
// emits {Lat,Lng,Zoom,Markers} and the frame silently mounts an empty map on
// load (the exact regression that bit the monaco savedDoc).
type mapDoc struct {
	Lat     float64     `json:"lat"`
	Lng     float64     `json:"lng"`
	Zoom    float64     `json:"zoom"`
	Markers []mapMarker `json:"markers"`
}

// mapMarker is a single persisted pin. Label omits when empty so an unlabelled
// pin round-trips cleanly.
type mapMarker struct {
	ID    string  `json:"id"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Label string  `json:"label,omitempty"`
}

// Plugin is the Leaflet map plugin. It implements framework.Plugin and mirrors
// the monaco plugin's shape (opaque-origin sandboxed iframe, protocol v1 over
// postMessage, go:embed'd frame bundle, capability gate, save handler). The
// one structural addition over monaco is the same-origin tile proxy
// (tiles.go), required because the opaque-origin frame's CSP blocks external
// image hosts.
type Plugin struct {
	devGrantAll   bool
	withDemoPage  bool
	capabilities  []string
	defaultConfig MapConfig
	saveHandler   func(ctx context.Context, req SaveRequest) error
	tileProviders map[string]string
	tileCache     *tileCache
	tileClient    tileFetcher // test injection; nil → defaultTileClient
	manifest      pluginhost.Manifest
	mu            sync.RWMutex
	docs          map[string]mapDoc
}

type Option func(*Plugin)

// WithDevGrantAll bypasses the auth.HasScope gate on save so the demo / tests
// run without standing up auth. Default OFF (enforcing).
func WithDevGrantAll() Option {
	return func(p *Plugin) { p.devGrantAll = true }
}

// WithCapabilities overrides the grant set advertised to the editor. Default:
// DefaultCapabilities.
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithDemoPage registers the self-contained themed demo page at DemoURL.
func WithDemoPage() Option {
	return func(p *Plugin) { p.withDemoPage = true }
}

// WithSaveHandler overrides the persistence hook. The default stores the
// canonical {lat,lng,zoom,markers} doc in an in-memory map keyed by DocID.
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// WithTileProviders overrides or extends the built-in tile-provider allowlist.
// Each value MUST be a URL template containing literal {z}, {x}, {y}
// placeholders; an invalid template panics at New() (fail-loud at construction
// rather than 500ing at first tile request). This is the advanced host opt-in
// for private/terrain/satellite tile servers — the allowlist is the SSRF guard
// (the upstream host is never client-controlled, only chosen from this map).
func WithTileProviders(providers map[string]string) Option {
	return func(p *Plugin) {
		if p.tileProviders == nil {
			p.tileProviders = map[string]string{}
		}
		for k, v := range providers {
			p.tileProviders[k] = v
		}
	}
}

// --- MapConfig options ------------------------------------------------------
//
// These configure the map defaults the plugin advertises. They are bridged to
// the frame via init.config (through the config.js host script + the adapter's
// manifest config). The frame applies them on mount; per-field options below
// each set one slot of MapConfig.

// WithCenter sets the default map center (lat, lng).
func WithCenter(lat, lng float64) Option {
	return func(p *Plugin) {
		p.defaultConfig.Center = geoPoint{Lat: lat, Lng: lng}
	}
}

// WithZoom sets the default zoom level (0..22).
func WithZoom(z float64) Option {
	return func(p *Plugin) {
		if z >= 0 {
			p.defaultConfig.Zoom = z
		}
	}
}

// WithMinZoom sets the minimum zoom level.
func WithMinZoom(z float64) Option {
	return func(p *Plugin) { p.defaultConfig.MinZoom = z }
}

// WithMaxZoom sets the maximum zoom level.
func WithMaxZoom(z float64) Option {
	return func(p *Plugin) { p.defaultConfig.MaxZoom = z }
}

// WithProvider sets the default base-layer provider. MUST be a key in the
// tile-provider allowlist (default: osm, carto-light, carto-dark); an unknown
// provider panics at New().
func WithProvider(provider string) Option {
	return func(p *Plugin) {
		if provider != "" {
			p.defaultConfig.Provider = provider
		}
	}
}

// WithReadOnly mounts the map read-only by default (click-to-add and marker
// dragging disabled in the frame).
func WithReadOnly() Option {
	return func(p *Plugin) { p.defaultConfig.ReadOnly = true }
}

// WithMarkers seeds the map with the given markers on first mount.
func WithMarkers(markers []mapMarker) Option {
	return func(p *Plugin) {
		p.defaultConfig.Markers = append([]mapMarker{}, markers...)
	}
}

// WithTheme sets the default theme strategy: "light", "dark", or "auto" (the
// frame follows the bridged host scheme). Default "auto".
func WithTheme(theme string) Option {
	return func(p *Plugin) {
		if theme != "" {
			p.defaultConfig.Theme = theme
		}
	}
}

// WithMapConfig replaces the full default MapConfig. Use the field-specific
// options above for ergonomics; this is the escape hatch.
func WithMapConfig(cfg MapConfig) Option {
	return func(p *Plugin) { p.defaultConfig = cfg }
}

// New constructs a Plugin. The platform manifest is built and Validate()'d
// here so a bad isolation/sandbox config aborts construction rather than
// silently de-opaquing the frame at runtime. Tile-provider templates are
// validated for the {z}/{x}/{y} placeholders (an invalid template also panics
// — a host that wired a bad upstream URL would otherwise 500 on every tile).
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities:  DefaultCapabilities(),
		docs:          make(map[string]mapDoc),
		tileProviders: defaultTileProviders(),
		tileCache:     newTileCache(defaultTileCacheEntries),
		defaultConfig: MapConfig{
			Center:   geoPoint{Lat: 20, Lng: 0},
			Zoom:     2,
			MinZoom:  0,
			MaxZoom:  19,
			Provider: "osm",
			ReadOnly: false,
			Theme:    "auto",
		},
		manifest: pluginhost.Manifest{
			Entry:        MapHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Leaflet map",
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
	p.manifest.Capabilities = p.capabilities
	if p.saveHandler == nil {
		p.saveHandler = p.memSave
	}
	// Validate every provider template BEFORE first request. A typo here
	// would otherwise surface as a confusing 500 deep in tile streaming.
	for name, tpl := range p.tileProviders {
		if err := validateTileTemplate(tpl); err != nil {
			panic("geomap: tile provider " + name + ": " + err.Error())
		}
	}
	if _, ok := p.tileProviders[p.defaultConfig.Provider]; !ok {
		panic("geomap: default provider " + p.defaultConfig.Provider + " is not in the allowlist")
	}
	if err := p.manifest.Validate(); err != nil {
		panic("geomap: invalid manifest: " + err.Error())
	}
	return p
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// DefaultConfig returns the map-config defaults this plugin instance will
// advertise (set via the With* options). The frame receives these through
// init.config and applies them on mount.
func (p *Plugin) DefaultConfig() MapConfig { return p.defaultConfig }

// Init registers every asset, the save RPC, the same-origin tile proxy, and
// (optionally) the demo page on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt)
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "map.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "map.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "map.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.AddBytes(ConfigScriptURL, "text/javascript; charset=utf-8", false, p.configScriptBytes())
	srv.Register(rt)
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))
	rt.Get(TilesRoutePattern, http.HandlerFunc(p.handleTiles))
	if p.withDemoPage {
		rt.Get(DemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}

// configScriptBytes renders the host-page config script that publishes this
// plugin instance's MapConfig as window.__gofastrMapConfig. The adapter (loaded
// after it via UIHostOption / the demo page) merges it into the manifest config
// it registers with the platform broker. JSON is a safe subset of a JS object
// literal and this is a standalone .js file (not inline), so no script-context
// escaping is required.
func (p *Plugin) configScriptBytes() []byte {
	b, err := json.Marshal(p.defaultConfig)
	if err != nil {
		// MapConfig is a plain struct of primitives + a slice; marshal cannot
		// fail in practice. Fail loud rather than ship an empty config.
		panic("geomap: marshal default config: " + err.Error())
	}
	return []byte("window.__gofastrMapConfig = " + string(b) + ";\n")
}

// LoadDoc returns the last-saved canonical {lat,lng,zoom,markers} JSON for
// docID from the in-memory default store. ok is false when the doc has never
// been saved. The returned docJSON is the canonical interchange blob (schema
// map-v1) with the lowercase json tags the frame's deriveDoc reads.
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (docJSON string, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	d, found := p.docs[docID]
	if !found {
		return "", false
	}
	b, _ := json.Marshal(mapDoc{Lat: d.Lat, Lng: d.Lng, Zoom: d.Zoom, Markers: d.Markers})
	return string(b), true
}

func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

func (p *Plugin) allow(r *http.Request, cap string) bool {
	if p.devGrantAll {
		// Dev/demo bypass: the demo pages run with no auth wired at all, so
		// BOTH gate sides (plugin grant AND caller authority) are skipped.
		// Production hosts never set this; the default-deny gate below rules.
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, cap)
}

func (p *Plugin) memSave(_ context.Context, req SaveRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs[req.DocID] = mapDoc{Lat: req.Lat, Lng: req.Lng, Zoom: req.Zoom, Markers: req.Markers}
	return nil
}

// UIHostOption injects the platform broker, this plugin's config script, and
// this plugin's adapter (in that order — the adapter reads the config global
// the config script publishes, and registers with the broker the former
// defines).
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(pluginhost.BrokerScriptURL, ConfigScriptURL, AdapterScriptURL)
}

// MountConfig configures Mount.
type MountConfig struct {
	DocID     string
	DocField  string // hidden input name for the canonical doc JSON (default "map_doc")
	MinHeight string
	Doc       string // optional initial {lat,lng,zoom,markers} JSON, server-rendered for reload round-trip
}

// Mount renders the mount marker div plus the hidden input the host adapter
// syncs on docChanged (the canonical doc JSON). It wraps the platform
// pluginhost.MountMarker and adds the geomap-specific data-fui-plugin-for
// attribute naming the hidden field. Drop it into a form. All interpolated
// values are HTML-escaped via render.Escape inside MountMarker.
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.DocField == "" {
		cfg.DocField = defaultDocField
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
			{Name: "data-fui-plugin-for", Value: cfg.DocField},
		},
		Fields: []pluginhost.Field{
			{Name: cfg.DocField},
		},
	})
}

// geoPoint is the JSON-serializable {lat,lng} pair used inside MapConfig.Center
// and elsewhere. Field names are lowercase on the wire to match the frame's
// reader.
type geoPoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// MapConfig is the map configuration bridged to the frame via init.config
// (through config.js + the adapter's manifest config). Every field is always
// serialized (no omitempty) so the frame always receives a complete config and
// never has to guess a default. The With* options above set individual slots.
type MapConfig struct {
	Center   geoPoint    `json:"center"`
	Zoom     float64     `json:"zoom"`
	MinZoom  float64     `json:"minZoom"`
	MaxZoom  float64     `json:"maxZoom"`
	Provider string      `json:"provider"`
	ReadOnly bool        `json:"readOnly"`
	Markers  []mapMarker `json:"markers"`
	Theme    string      `json:"theme"` // "light" | "dark" | "auto"
}

// ErrConflict is the sentinel a WithSaveHandler hook returns to signal that the
// save lost an optimistic-concurrency check — the stored document changed under
// the map since it loaded. handleSave maps it to HTTP 409 (E_CONFLICT) rather
// than the generic 500 (E_SAVE), the one status the host adapter relays back to
// the frame as a distinct saveResult so the map can warn the user instead of
// silently dropping their pins. Wrap it (fmt.Errorf("...: %w", geomap.ErrConflict))
// to add context; handleSave uses errors.Is. Identical to monaco's contract.
var ErrConflict = errors.New("geomap: save conflict")

// SaveRequest is the persistence payload handed to the save handler.
type SaveRequest struct {
	DocID         string
	Lat           float64
	Lng           float64
	Zoom          float64
	Markers       []mapMarker
	SchemaVersion string
}
