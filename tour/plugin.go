// Package tour is a GoFastr plugin that ships a trusted, host-page guided-tour
// runtime (Appcues-style product tours). Unlike the richtext/mermaid plugins it
// does NOT run inside a sandboxed opaque-origin iframe: a tour MUST reach the
// host page's real DOM to spotlight elements, so it cannot be opaque-origin.
//
// The plugin serves two non-framed host-page assets (tour.js + tour.css) plus
// three JSON endpoints (read a tour definition, mark a tour seen, query seen
// state). Tours themselves are registered server-side via [WithTour]. The
// runtime is injected into host pages through [UIHostOption] (a host app mounts
// a UIHost with it) or loaded by the self-contained demo page (see
// [WithDemoPage]).
package tour

import (
	"context"
	"net/http"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and js/src/tour.ts hard-code
// these exactly — they ARE the contract.
const (
	Name        = "tour"
	Version     = "0.1.0"
	RoutePrefix = "/__gofastr/plugin/tour"

	// Host-page runtime assets (NON-framed — same-origin scripts/stylesheets
	// the host page loads directly, no CORP relaxation, normal CSP).
	TourJSURL  = RoutePrefix + "/tour.js"
	TourCSSURL = RoutePrefix + "/tour.css"

	// JSON endpoints consumed by the runtime.
	ToursBaseURL = RoutePrefix + "/tours"
	SeenURL      = RoutePrefix + "/seen"

	// Demo page (only mounted under WithDemoPage).
	DemoURL = "/tour"

	SchemaVersion = "tour-v1"
)

// Placement is the side of the target element the tooltip bubble anchors to.
// "auto" picks the side with the most viewport room.
type Placement string

const (
	PlacementAuto   Placement = "auto"
	PlacementTop    Placement = "top"
	PlacementBottom Placement = "bottom"
	PlacementLeft   Placement = "left"
	PlacementRight  Placement = "right"
)

// Action is a UI action the runtime performs on the host page as part of a step
// — the mechanism for reaching BURIED targets: open a sidebar, expand a toggle,
// or navigate before spotlighting an element that isn't visible yet.
//
//	{"type": "click",    "selector": "#open-settings"}  // reveal a panel
//	{"type": "wait",     "selector": "#panel .item"}    // wait for it to appear
//	{"type": "navigate", "url": "/settings"}            // go to another page/route
type Action struct {
	Type     string `json:"type"`               // "click" | "wait" | "navigate"
	Selector string `json:"selector,omitempty"` // target for click / wait
	URL      string `json:"url,omitempty"`      // target for navigate
}

// Step is one ordered step of a tour. Selector is a CSS selector for the
// spotlighted element; Title/Body render in the tooltip bubble; Placement hints
// where the bubble sits relative to the target. Before runs when the step is
// entered (reveal the target); After runs when it is advanced past (e.g. close
// what Before opened).
type Step struct {
	Selector  string    `json:"selector"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Placement Placement `json:"placement"`
	Before    []Action  `json:"before,omitempty"`
	After     []Action  `json:"after,omitempty"`
	// HTML is trusted, app-authored bubble content that overrides Title/Body.
	// (Passing a live DOM node or a render function is a JS-API-only capability.)
	HTML      string `json:"html,omitempty"`
	ClassName string `json:"className,omitempty"` // extra class on the bubble
}

// TourOptions are tour-level toggles. Each is a *bool so "unset" means "use the
// runtime default" (all default ON) — a caller overrides only what it wants.
type TourOptions struct {
	ShowProgress  *bool  `json:"showProgress,omitempty"`  // "Step N of M" line
	ShowDots      *bool  `json:"showDots,omitempty"`      // progress dots
	AllowKeyboard *bool  `json:"allowKeyboard,omitempty"` // arrow/enter navigation
	CloseOnEscape *bool  `json:"closeOnEscape,omitempty"` // Esc dismisses
	Backdrop      *bool  `json:"backdrop,omitempty"`      // dim/scrim the page
	Accent        string `json:"accent,omitempty"`        // accent color (--gofastr-tour-accent)
	Width         string `json:"width,omitempty"`         // bubble max-width, e.g. "420px"
	ClassName     string `json:"className,omitempty"`     // extra class on every bubble
}

// Tour is a registered tour definition served by GET /tours/{id}.
type Tour struct {
	ID      string       `json:"id"`
	Steps   []Step       `json:"steps"`
	Options *TourOptions `json:"options,omitempty"`
}

// DefaultCapabilities is the grant set this plugin advertises. The runtime
// only needs to read tour definitions and persist completion state.
func DefaultCapabilities() []string {
	return []string{"tour:read", "tour:write"}
}

// seenRecord is a tour the user has completed / dismissed. The default
// in-memory store keys it by TourID; a production app keys by user+tour via
// [WithSeenHandler].
type seenRecord struct {
	TourID string
}

// SeenHandler is the persistence hook for tour completion. Mark records the
// tour as seen for the caller; IsSeen reports whether it was previously seen.
// Implementations must be safe for concurrent use.
type SeenHandler interface {
	Mark(ctx context.Context, tourID string) error
	IsSeen(ctx context.Context, tourID string) (bool, error)
}

// Plugin is the guided-tour plugin. It implements [framework.Plugin].
type Plugin struct {
	capabilities []string
	tours        map[string]Tour
	tourOrder    []string // stable JSON iteration / 404-vs-empty decisions
	seen         SeenHandler
	devGrantAll  bool
	withDemoPage bool

	mu sync.RWMutex
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithDevGrantAll bypasses the auth.HasScope capability gate so a host with no
// auth wired can still run tours (demos, dev). Default OFF (enforcing).
func WithDevGrantAll() Option {
	return func(p *Plugin) { p.devGrantAll = true }
}

// WithCapabilities overrides the grant set advertised by [DefaultCapabilities].
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithTour registers a tour definition the runtime can fetch by id. Multiple
// tours may be registered; the last WithTour for a given id wins its steps.
// Any options set via [WithTourOptions] are preserved regardless of call order.
// Empty steps are rejected at Init time (a step-less tour is a misconfiguration).
func WithTour(id string, steps []Step) Option {
	return func(p *Plugin) {
		if id == "" {
			return
		}
		existing, ok := p.tours[id]
		if !ok {
			p.tourOrder = append(p.tourOrder, id)
		}
		existing.ID = id
		existing.Steps = append([]Step{}, steps...)
		p.tours[id] = existing
	}
}

// WithTourOptions sets tour-level options for a tour id (registration order with
// [WithTour] does not matter — steps and options merge onto the same tour).
func WithTourOptions(id string, opts TourOptions) Option {
	return func(p *Plugin) {
		if id == "" {
			return
		}
		existing, ok := p.tours[id]
		if !ok {
			p.tourOrder = append(p.tourOrder, id)
		}
		existing.ID = id
		o := opts
		existing.Options = &o
		p.tours[id] = existing
	}
}

// WithSeenHandler overrides the default in-memory completion store. Use it to
// persist per-user tour state in a real database.
func WithSeenHandler(h SeenHandler) Option {
	return func(p *Plugin) { p.seen = h }
}

// WithDemoPage registers the self-contained demo page at [DemoURL]. The page
// injects the tour runtime + stylesheet and exposes a couple of demo tour
// trigger buttons.
func WithDemoPage() Option {
	return func(p *Plugin) { p.withDemoPage = true }
}

// New constructs a [Plugin] with the given options. Unset options fall back to
// defaults so the plugin works with zero configuration.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		tours:        make(map[string]Tour),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	if len(p.capabilities) == 0 {
		p.capabilities = DefaultCapabilities()
	}
	if p.seen == nil {
		p.seen = newMemSeen()
	}
	return p
}

// Name implements [framework.Plugin].
func (p *Plugin) Name() string { return Name }

// Init implements [framework.Plugin]. It registers the host-page assets and
// the three JSON endpoints on the app's router. The assets are NON-framed
// (trusted host-page scripts), so the AssetServer emits them with no CORP /
// frame-ancestors relaxation — just correct Content-Types.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()

	// Validate registered tours up front so a step-less tour aborts here
	// rather than rendering an empty walk-through at runtime.
	for _, id := range p.tourOrder {
		t := p.tours[id]
		if len(t.Steps) == 0 {
			panic("tour: WithTour(" + id + ") registered with no steps")
		}
		for i, s := range t.Steps {
			if s.Selector == "" {
				panic("tour: WithTour(" + id + ") step " + itoa(i) + " has empty selector")
			}
		}
	}

	srv := pluginhost.NewAssetServer(nil, RoutePrefix, nil)
	srv.AddBytes(TourJSURL, "text/javascript; charset=utf-8", false, tourJSBytes)
	srv.AddBytes(TourCSSURL, "text/css; charset=utf-8", false, tourCSSBytes)
	srv.Register(rt)

	// Capability-gated JSON endpoints.
	rt.Get(ToursBaseURL+"/{id}", http.HandlerFunc(p.handleGetTour))
	rt.Post(SeenURL, http.HandlerFunc(p.handleMarkSeen))
	rt.Get(SeenURL, http.HandlerFunc(p.handleQuerySeen))

	if p.withDemoPage {
		rt.Get(DemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The demo page carries inline <style>/<script> for the theme
			// toggle + demo trigger; relax this page's CSP to permit them.
			// The runtime itself stays an external same-origin script.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'self'; base-uri 'self'")
			render.RespondHTML(w, p.renderDemo(r))
		}))
	}
	return nil
}

// Capabilities returns the grant set this plugin advertises.
func (p *Plugin) Capabilities() []string {
	return append([]string{}, p.capabilities...)
}

// Tour returns the registered tour definition for id. ok is false if no tour
// was registered under that id.
func (p *Plugin) Tour(id string) (Tour, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	t, ok := p.tours[id]
	return t, ok
}

// Tours returns the ids of every registered tour in registration order.
func (p *Plugin) Tours() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.tourOrder))
	copy(out, p.tourOrder)
	return out
}

// allow is the capability gate, delegated to the platform [pluginhost.Allow]:
// default-deny against the plugin's granted set, intersected with the caller's
// own authority. WithDevGrantAll short-circuits the gate for demos/dev.
func (p *Plugin) allow(r *http.Request, cap string) bool {
	if p.devGrantAll {
		return true
	}
	return pluginhost.Allow(r.Context(), p.capabilities, cap)
}

// UIHostOption returns the [uihost.Option] that injects the tour runtime into
// every UIHost-rendered page. Apps using a UIHost pass this to uihost.New; the
// runtime then loads its own stylesheet via [TourCSSURL] and auto-runs any
// tour whose id is listed in window.gofastrTourAuto (or invoked explicitly).
//
// The platform broker is NOT needed (the tour is a trusted host-page plugin,
// no sandboxed iframe), so only the runtime script is injected.
func UIHostOption() uihost.Option {
	return uihost.WithExtraScripts(TourJSURL)
}

// itoa avoids importing strconv just for the panic path above.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
