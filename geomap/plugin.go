// Package geomap is a GoFastr plugin that ships a TRUSTED host-page interactive
// vector map built on MapLibre GL + OpenFreeMap tiles. Unlike the richtext,
// mermaid, and monaco plugins it does NOT run inside a sandboxed opaque-origin
// iframe: a vector map MUST fetch() tiles and spawn the MapLibre web worker,
// both impossible under the opaque frame's `connect-src 'none'`. It therefore
// runs in the host page's own origin with the host page's own CSP, exactly like
// the tour plugin.
//
// OpenFreeMap (https://tiles.openfreemap.org) is MIT-licensed, free for
// commercial use, no API key, no rate limits, no cookies. Attribution (OSM +
// OpenMapTiles) is auto-added by MapLibre from the style — do not strip it.
//
// The plugin serves two NON-framed host-page assets (map.js + map.css) plus a
// save endpoint. The runtime is injected into host pages through [UIHostOption]
// (a host app mounts a UIHost with it) or loaded by the self-contained demo
// page (see [WithDemoPage]). There is no platform broker, no adapter, no tile
// proxy — MapLibre fetches OpenFreeMap directly.
package geomap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. The Go package / directory is `geomap` (the
// identifier `map` is a Go keyword), but the user-facing identity strings are
// "map" (Name, route prefix, demo URL, schema). Both this file and js/src/map.ts
// hard-code these exactly — they ARE the contract. The demo lives at /map so it
// co-mounts with the other plugin demos without colliding on "/".
const (
	Name        = "map"
	Version     = "0.3.0"
	RoutePrefix = "/__gofastr/plugin/map"
	MapJSURL    = RoutePrefix + "/map.js"
	MapCSSURL   = RoutePrefix + "/map.css"
	SaveURL     = RoutePrefix + "/save"
	// GeocodeURL is the SAME-ORIGIN place-search proxy, registered only when
	// [WithSearch] is set. The browser never calls a geocoder directly: routing
	// it through the plugin is what lets us set a policy-compliant User-Agent,
	// rate-limit, and cache — and keeps the host page CSP at connect-src 'self'.
	GeocodeURL       = RoutePrefix + "/geocode"
	DemoURL          = "/map"
	SchemaVersion    = "map-v1"
	defaultDocID     = "demo"
	defaultDocField  = "map_doc"
	defaultMinHeight = "360px"

	// defaultStyleBase is the upstream OpenFreeMap styles host. Every style,
	// tile, sprite, glyph and raster asset the styles reference lives under the
	// single host https://tiles.openfreemap.org, so a consumer's CSP host
	// allowlist is exactly that origin. Override with [WithStyleBaseURL] to
	// self-host OpenFreeMap or front it with your own CDN (and allowlist THAT
	// host in your page CSP instead).
	defaultStyleBase = "https://tiles.openfreemap.org/styles/"
)

// hostPageCSP is the Content-Security-Policy a host page rendering the map MUST
// advertise so MapLibre GL can fetch OpenFreeMap vector tiles, spawn its blob
// web worker, and load glyphs/sprites/raster insets. It does NOT require
// 'unsafe-eval' or WASM. The demo page sets this header verbatim; consumers
// embedding the mount must set an equivalent policy (see docs/geomap.md).
const hostPageCSP = "default-src 'self';" +
	"connect-src 'self' https://tiles.openfreemap.org;" +
	"img-src 'self' data: blob: https://tiles.openfreemap.org;" +
	"worker-src blob:;" +
	"child-src blob:;" +
	"style-src 'self' 'unsafe-inline';" +
	"script-src 'self' 'unsafe-inline';" +
	"frame-ancestors 'self';" +
	"base-uri 'self'"

// CapGeocode gates the place-search proxy. It is NOT in [DefaultCapabilities]:
// search is opt-in, so the capability is appended by [New] only when
// [WithSearch] is set. A host that overrides the grant set with
// [WithCapabilities] and still wants search gets it appended too — the gate is
// on egress the host explicitly enabled, so silently dropping it would just
// break search with a 403.
const CapGeocode = "geocode:search"

// DefaultCapabilities is the grant set advertised to the host. Geomap has no
// upload path — only document read/write + theme:read (same as monaco).
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

// mapDoc is the canonical map-v1 document. The json tags are load-bearing:
// LoadDoc marshals this into the mount element's data-doc, and map.js reads
// lowercase {lat,lng,zoom,markers}. Without the tags Go emits
// {Lat,Lng,Zoom,Markers} and the map silently mounts empty on reload (the exact
// regression that bit the monaco savedDoc).
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

// Plugin is the interactive vector-map plugin. It implements framework.Plugin
// and mirrors the tour plugin's shape: trusted host-page (no sandbox, no
// broker), capability gate, in-memory doc store with a save handler hook.
type Plugin struct {
	devGrantAll   bool
	withDemoPage  bool
	capabilities  []string
	defaultConfig MapConfig
	saveHandler   func(ctx context.Context, req SaveRequest) error
	mu            sync.RWMutex
	docs          map[string]mapDoc

	// Place search (opt-in via WithSearch). geocoder is the resolved lookup —
	// either a host-supplied one (WithGeocoder) or the built-in Nominatim proxy.
	searchEnabled   bool
	geocoder        Geocoder
	geocodeEndpoint string
	geocodeUA       string
	geocodeClient   *http.Client
	geoMu           sync.Mutex
	geoNext         time.Time
	geoCache        map[string]geoCacheEntry
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithDevGrantAll bypasses the auth.HasScope capability gate so the demo / tests
// run without standing up auth. Default OFF (enforcing).
func WithDevGrantAll() Option {
	return func(p *Plugin) { p.devGrantAll = true }
}

// WithCapabilities overrides the grant set advertised to the host. Default:
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

// --- MapConfig options ------------------------------------------------------
//
// These configure the map defaults the plugin advertises. They are marshalled
// into the mount element's data-config attribute, which map.js reads on mount.
// Per-field options below each set one slot of MapConfig.

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

// WithStyle sets the default base style. It is either an OpenFreeMap style name
// ("liberty", "positron", "dark", "bright", "fiord") or a full style URL. The
// default is "liberty". An empty value is rejected at New() — a Go-configured
// map always ships an explicit style (the JS theme-derivation path is a
// fallback for hand-written mounts only).
func WithStyle(name string) Option {
	return func(p *Plugin) {
		if name != "" {
			p.defaultConfig.Style = name
		}
	}
}

// WithStyleBaseURL is the CDN / self-host hook: the base URL joined onto a style
// NAME to form the style URL (default https://tiles.openfreemap.org/styles/).
// A host self-hosting OpenFreeMap or fronting it with their own CDN points this
// at their base and allows THAT host in their page CSP instead.
func WithStyleBaseURL(url string) Option {
	return func(p *Plugin) {
		if url != "" {
			p.defaultConfig.StyleBaseURL = url
		}
	}
}

// WithStyles sets the options offered by the in-map style switcher (the "layers"
// control). Defaults to ["liberty","positron","dark"]. Each must be non-empty.
func WithStyles(names ...string) Option {
	return func(p *Plugin) {
		p.defaultConfig.Styles = append([]string{}, names...)
	}
}

// WithReadOnly mounts the map read-only by default (click-to-add and marker
// dragging disabled).
func WithReadOnly() Option {
	return func(p *Plugin) { p.defaultConfig.ReadOnly = true }
}

// WithMarkers seeds the map with the given markers on first mount.
func WithMarkers(markers []mapMarker) Option {
	return func(p *Plugin) {
		p.defaultConfig.Markers = append([]mapMarker{}, markers...)
	}
}

// WithTheme sets the default theme strategy: "light", "dark", or "auto". Only
// consulted by map.js when no explicit style is set; the Go default always ships
// an explicit style ("liberty"). Default "auto".
func WithTheme(theme string) Option {
	return func(p *Plugin) {
		if theme != "" {
			p.defaultConfig.Theme = theme
		}
	}
}

// WithoutGeolocateControl hides MapLibre's GeolocateControl ("find me"). The
// control is shown by default; it never prompts for location permission on load,
// only on an explicit user click.
func WithoutGeolocateControl() Option {
	return func(p *Plugin) { p.defaultConfig.Geolocate = false }
}

// WithoutScaleControl hides MapLibre's ScaleControl (shown by default).
func WithoutScaleControl() Option {
	return func(p *Plugin) { p.defaultConfig.Scale = false }
}

// WithClustering renders pins as counted cluster bubbles at low zoom instead of
// one marker each. Off by default. Clusters are DOM markers (not circle/symbol
// layers), so individual pins stay draggable and editable and no style glyphs
// are required — see js/src/map.ts.
func WithClustering() Option {
	return func(p *Plugin) { p.defaultConfig.Cluster = true }
}

// WithClusterRadius sets the cluster radius in pixels (default 50).
func WithClusterRadius(px float64) Option {
	return func(p *Plugin) {
		if px > 0 {
			p.defaultConfig.ClusterRadius = px
		}
	}
}

// WithClusterMaxZoom sets the zoom above which pins stop clustering (default 14).
func WithClusterMaxZoom(z float64) Option {
	return func(p *Plugin) { p.defaultConfig.ClusterMaxZoom = z }
}

// WithMapConfig replaces the full default MapConfig. Use the field-specific
// options above for ergonomics; this is the escape hatch.
func WithMapConfig(cfg MapConfig) Option {
	return func(p *Plugin) { p.defaultConfig = cfg }
}

// New constructs a Plugin. There is no platform manifest (this is a trusted
// host-page plugin — no sandbox, no broker). Style config is sanity-checked
// (non-empty Style and Styles entries) so a misconfiguration fails loud at
// construction; a wrong-but-non-empty style name is left alone (it just 404s a
// tile, which is non-fatal).
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]mapDoc),
		defaultConfig: MapConfig{
			Center:         geoPoint{Lat: 20, Lng: 0},
			Zoom:           2,
			MinZoom:        0,
			MaxZoom:        19,
			Style:          "liberty",
			StyleBaseURL:   defaultStyleBase,
			Styles:         []string{"liberty", "positron", "dark"},
			ReadOnly:       false,
			Theme:          "auto",
			Geolocate:      true,
			Scale:          true,
			Cluster:        false,
			ClusterRadius:  50,
			ClusterMaxZoom: 14,
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
	p.initSearch()
	if strings.TrimSpace(p.defaultConfig.Style) == "" {
		panic("geomap: style must be a non-empty name or URL (set via WithStyle)")
	}
	if strings.TrimSpace(p.defaultConfig.StyleBaseURL) == "" {
		p.defaultConfig.StyleBaseURL = defaultStyleBase
	}
	if len(p.defaultConfig.Styles) == 0 {
		p.defaultConfig.Styles = []string{"liberty", "positron", "dark"}
	}
	for _, s := range p.defaultConfig.Styles {
		if strings.TrimSpace(s) == "" {
			panic("geomap: WithStyles must not contain empty entries")
		}
	}
	return p
}

func (p *Plugin) Name() string { return Name }

// DefaultConfig returns the map-config defaults this plugin instance will
// advertise (set via the With* options). map.js receives these through the mount
// element's data-config attribute.
func (p *Plugin) DefaultConfig() MapConfig { return p.defaultConfig }

// Init registers the host-page assets, the save endpoint, and (optionally) the
// demo page on the app's router. The assets are NON-framed (trusted host-page
// scripts), so the AssetServer emits them with no CORP / frame-ancestors
// relaxation — just correct Content-Types. There is no broker route and no tile
// proxy: MapLibre fetches OpenFreeMap directly from the host page.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	srv := pluginhost.NewAssetServer(nil, RoutePrefix, nil)
	srv.AddBytes(MapJSURL, "text/javascript; charset=utf-8", false, mapJSBytes)
	srv.AddBytes(MapCSSURL, "text/css; charset=utf-8", false, mapCSSBytes)
	srv.Register(rt)
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))
	if p.searchEnabled {
		rt.Get(GeocodeURL, http.HandlerFunc(p.handleGeocode))
	}
	if p.withDemoPage {
		rt.Get(DemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The demo page carries an inline <script> (the toolbar wiring) and
			// loads map.js as an external same-origin script. The CSP opens the
			// OpenFreeMap host for connect/img and the blob worker MapLibre needs.
			w.Header().Set("Content-Security-Policy", hostPageCSP)
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}

// LoadDoc returns the last-saved canonical {lat,lng,zoom,markers} JSON for
// docID from the in-memory default store. ok is false when the doc has never
// been saved. The returned docJSON is the canonical interchange blob (schema
// map-v1) with the lowercase json tags map.js reads.
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

// allow is the capability gate, delegated to the platform [pluginhost.Allow]:
// default-deny against the plugin's granted set, intersected with the caller's
// own authority. WithDevGrantAll short-circuits the gate for demos/dev.
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

// UIHostOption returns the [uihost.Option] that injects the map runtime into
// every UIHost-rendered page. Apps using a UIHost pass this to uihost.New; the
// runtime then scans for [data-fui-geomap] mount elements and renders a MapLibre
// map into each, injecting MapLibre's own CSS. The overlay map.css is NOT
// injected here — a host wanting the demo-style overlay links MapCSSURL itself
// (see docs/geomap.md).
//
// The platform broker is NOT needed (the map is a trusted host-page plugin, no
// sandboxed iframe), so only the runtime script is injected.
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(MapJSURL)
}

// MountConfig configures [Plugin.Mount].
type MountConfig struct {
	DocID     string
	DocField  string // hidden input name for the canonical doc JSON (default "map_doc")
	MinHeight string
	Doc       string // optional initial {lat,lng,zoom,markers} JSON, server-rendered for reload round-trip
}

// Mount renders a plain host-page mount element plus the hidden input map.js
// mirrors the canonical doc JSON into. It does NOT use the platform
// pluginhost.MountMarker (that builds the sandboxed-iframe broker marker) — this
// is a trusted host-page mount, so a plain <div data-fui-geomap ...> is enough;
// map.js finds it on DOMContentLoaded and constructs the MapLibre map. Drop it
// into any form. All interpolated values are HTML-escaped via [render.Escape].
//
// The instance's configured [MapConfig] (set via the With* options) is
// serialized into data-config; the saved doc (if any) into data-doc, which
// OVERRIDES config center/zoom/markers on reload.
func (p *Plugin) Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.DocField == "" {
		cfg.DocField = defaultDocField
	}
	if cfg.MinHeight == "" {
		cfg.MinHeight = defaultMinHeight
	}
	cfgJSON, err := json.Marshal(p.defaultConfig)
	if err != nil {
		// MapConfig is a plain struct of primitives + slices; marshal cannot
		// fail in practice. Fail loud rather than ship an empty config.
		panic("geomap: marshal mount config: " + err.Error())
	}
	var b strings.Builder
	b.WriteString(`<div data-fui-geomap`)
	b.WriteString(` data-doc-id="`)
	b.WriteString(render.Escape(cfg.DocID))
	b.WriteString(`" data-doc-field="`)
	b.WriteString(render.Escape(cfg.DocField))
	b.WriteString(`" data-save-url="`)
	b.WriteString(render.Escape(SaveURL))
	b.WriteString(`" data-min-height="`)
	b.WriteString(render.Escape(cfg.MinHeight))
	b.WriteString(`" data-config="`)
	b.WriteString(render.Escape(string(cfgJSON)))
	b.WriteByte('"')
	if cfg.Doc != "" {
		b.WriteString(` data-doc="`)
		b.WriteString(render.Escape(cfg.Doc))
		b.WriteByte('"')
	}
	b.WriteString(` style="min-height:` + render.Escape(cfg.MinHeight) + `"></div>`)
	b.WriteString(`<input type="hidden" name="`)
	b.WriteString(render.Escape(cfg.DocField))
	b.WriteString(`">`)
	return render.HTML(b.String())
}

// geoPoint is the JSON-serializable {lat,lng} pair used inside MapConfig.Center.
// Field names are lowercase on the wire to match map.js's reader.
type geoPoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// MapConfig is the map configuration serialized into the mount element's
// data-config attribute. Every field is always serialized (no omitempty) so
// map.js always receives a complete config and never has to guess a default.
// The With* options above set individual slots.
type MapConfig struct {
	Center       geoPoint    `json:"center"`
	Zoom         float64     `json:"zoom"`
	MinZoom      float64     `json:"minZoom"`
	MaxZoom      float64     `json:"maxZoom"`
	Style        string      `json:"style"`        // name ("liberty") or full URL
	StyleBaseURL string      `json:"styleBaseURL"` // default https://tiles.openfreemap.org/styles/
	Styles       []string    `json:"styles"`       // switcher options
	ReadOnly     bool        `json:"readOnly"`
	Markers      []mapMarker `json:"markers"`
	Theme        string      `json:"theme"` // light|dark|auto (only when style is empty)

	Geolocate bool `json:"geolocate"` // show MapLibre's GeolocateControl
	Scale     bool `json:"scale"`     // show MapLibre's ScaleControl
	// SearchURL is the same-origin geocode proxy. Set by New() to GeocodeURL when
	// WithSearch is enabled; empty means map.js renders no search control.
	SearchURL      string  `json:"searchURL"`
	Cluster        bool    `json:"cluster"`
	ClusterRadius  float64 `json:"clusterRadius"`
	ClusterMaxZoom float64 `json:"clusterMaxZoom"`
}

// ErrConflict is the sentinel a WithSaveHandler hook returns to signal that the
// save lost an optimistic-concurrency check — the stored document changed under
// the map since it loaded. handleSave maps it to HTTP 409 (E_CONFLICT) rather
// than the generic 500 (E_SAVE), so the map can warn the user instead of
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
