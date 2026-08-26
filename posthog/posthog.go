// Package posthog is the packaged, one-call version of the PostHog
// recipe from gofastr's analytics-recipes docs: PostHog's script and
// ingestion endpoints served first-party through battery/relay, a
// host-authored bootstrap that loads the real posthog-js loader through
// the relay, identity from the app's session via a same-origin whoami
// endpoint, and pageviews that track GoFastr's client-side navigation.
//
// This is an integration, not one of this repo's sandboxed heavy-JS
// plugins: posthog-js instruments the whole host document, so it runs
// in the host page, unfenced by design. The isolation story here is the
// relay's — the visitor's browser talks only to your origin, the strict
// default CSP stays untouched, and no third-party cookie ever lands on
// it. See posthog/README.md for the full traffic map.
package posthog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr/battery/relay"
	"github.com/DonaldMurillo/gofastr/core/handler"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

//go:embed boot.js
var bootTemplate []byte

// configPlaceholder is the token boot.js carries that New replaces with
// the instance's JSON-encoded config. The placeholder must never appear
// in the rendered bytes as-is; New panics if the template lost it.
const configPlaceholder = "__GOFASTR_POSTHOG_CONFIG__"

// Config constructs a Plugin with New. Key is the only required field;
// the zero value of the rest is the recommended posture.
type Config struct {
	// Key is the PostHog project API key (phc_...). Required.
	//
	// It is a public project identifier the browser ships on every
	// beacon anyway, not a secret — which is why New panics on the key
	// shapes that ARE secrets (phx_ personal, sk_ server): one of
	// those baked into the served bootstrap leaks to every visitor.
	Key string

	// Region is the project's region: "us" (the default) or "eu". It
	// picks both relay upstreams and the ui_host the bootstrap
	// configures. Anything else panics at New.
	Region string

	// SelfHost points every route — assets, ingestion, and the
	// bootstrap's ui_host — at one self-hosted PostHog origin
	// (e.g. "http://localhost:8000" for the docker hobby deploy;
	// loopback http is the only http the relay accepts). Mutually
	// exclusive with Region: a self-hosted instance has no region.
	SelfHost string

	// Path overrides the relay mount. Default relay.DefaultPath
	// ("/__gofastr/t"). Every route this package serves — ph/, ph-assets/,
	// boot.js, whoami — lives under it; relay.New validates it.
	Path string

	// SessionReplay raises the ingestion route's request-body cap from
	// the relay's 8 MiB default to 64 MiB, the size PostHog
	// session-replay uploads can reach. Off by default, deliberately:
	// the cap is an egress number, and every accepted byte is billed
	// to your bandwidth.
	SessionReplay bool

	// RespectDNT makes the bootstrap a no-op for visitors whose browser
	// reports Do-Not-Track: no SDK script loads, no beacon fires.
	RespectDNT bool

	// PersonProfiles sets posthog-js's person_profiles init option:
	// "" (the default) omits it and the SDK uses its own default
	// ("identified_only"), or one of "identified_only", "always",
	// "never". Anything else panics at New. Read it as a billing
	// question: "always" creates a person for every anonymous visitor,
	// which is exactly what "never" is for avoiding.
	PersonProfiles string

	// Identify resolves the visitor's identity for the whoami endpoint.
	// Default: handler.GetUser with the recipes' normalization — a
	// string principal passes through, a fmt.Stringer is String()ed,
	// anything else (or nobody) is anonymous. Return ok=false to answer
	// anonymous regardless of the session.
	Identify func(*http.Request) (string, bool)
}

// hosts is one region's three PostHog endpoints.
type hosts struct{ assets, ingest, ui string }

// regionHosts is the verified PostHog layout per region: static/array.js
// assets on the -assets host, everything else (/e, /s, /i/v0/e, /flags,
// /decide, /batch) on the ingestion host, and the region UI host that
// toolbar/replay tooling loads from directly (never relayed).
func regionHosts(region string) (hosts, bool) {
	switch region {
	case "us":
		return hosts{
			assets: "https://us-assets.i.posthog.com",
			ingest: "https://us.i.posthog.com",
			ui:     "https://us.posthog.com",
		}, true
	case "eu":
		return hosts{
			assets: "https://eu-assets.i.posthog.com",
			ingest: "https://eu.i.posthog.com",
			ui:     "https://eu.posthog.com",
		}, true
	}
	return hosts{}, false
}

// Plugin is the packaged PostHog integration: a framework.Plugin that
// embeds the battery/relay instance it is built on (so Base() returns
// the mount) and adds the rendered bootstrap, its serving route, and
// the identity endpoint. Construct with New, register with
// App.RegisterPlugin.
type Plugin struct {
	*relay.Relay

	cfg Config
	ui  string // the region's UI host, for the bootstrap's ui_host
	js  []byte // boot.js rendered with this instance's config
	url string // ScriptURL, computed once at New
}

// New validates cfg and constructs the Plugin. It panics on invalid
// configuration with a message prefixed "posthog:" — a mistyped key
// shape or region is a construction-time programmer error, the same
// posture as relay.New. Path validation (and everything else about the
// relay table) panics inside relay.New with its own "relay:" prefix.
func New(cfg Config) *Plugin {
	return newWithUpstreams(cfg, "", "")
}

// newWithUpstreams is New with the two relay upstreams overridable, so
// in-package tests can point them at loopback httptest servers (the
// relay accepts http:// only for loopback, which is what makes this
// seam test-only). Production callers always go through New and get the
// region's real hosts.
func newWithUpstreams(cfg Config, assetsUpstream, ingestUpstream string) *Plugin {
	if cfg.Key == "" {
		panic("posthog: Config.Key is required: the project API key (phc_...)")
	}

	switch cfg.PersonProfiles {
	case "", "identified_only", "always", "never":
	default:
		panic(fmt.Sprintf("posthog: Config.PersonProfiles %q is invalid: use \"identified_only\", \"always\", or \"never\" (empty for the posthog-js default)", cfg.PersonProfiles))
	}
	if strings.HasPrefix(cfg.Key, "phx_") || strings.HasPrefix(cfg.Key, "sk_") {
		panic("posthog: Config.Key looks like a personal (phx_) or server (sk_) key: " +
			"those are secrets and would ship in the served bootstrap; use the public project API key (phc_...)")
	}
	if cfg.SelfHost != "" && cfg.Region != "" {
		panic("posthog: Config.SelfHost and Config.Region are mutually exclusive: a self-hosted instance has no region")
	}
	region := cfg.Region
	if region == "" {
		region = "us"
	}
	h, ok := regionHosts(region)
	if !ok {
		panic(fmt.Sprintf("posthog: Config.Region %q is invalid: use \"us\" or \"eu\"", cfg.Region))
	}
	if cfg.SelfHost != "" {
		// One origin serves everything on a self-hosted deploy,
		// including the UI the toolbar and replay player open.
		h = hosts{assets: cfg.SelfHost, ingest: cfg.SelfHost, ui: cfg.SelfHost}
	}
	assets, ingest := h.assets, h.ingest
	if assetsUpstream != "" {
		assets = assetsUpstream
	}
	if ingestUpstream != "" {
		ingest = ingestUpstream
	}

	// Session-replay uploads reach 64 MB bodies; without replay the
	// relay's 8 MiB default is the right egress brake. MaxBodyBytes 0
	// means "relay default", so this stays zero unless asked for.
	var maxBody int64
	if cfg.SessionReplay {
		maxBody = 64 << 20
	}

	p := &Plugin{
		cfg: cfg,
		ui:  h.ui,
	}
	p.Relay = relay.New(relay.Config{
		Path: cfg.Path,
		Routes: []relay.Route{
			// The SDK and its loader assets. CacheOK: posthog versions
			// its static assets, so upstream cache headers are honest.
			{Prefix: "ph-assets/", Upstream: assets,
				Methods: []string{http.MethodGet, http.MethodPost}, CacheOK: true},
			// Everything else posthog-js calls: /e, /s, /i/v0/e, /flags,
			// /decide, /batch. A subtree covers it: posthog-js always
			// tails the mount, never requests it bare.
			{Prefix: "ph/", Upstream: ingest,
				Methods: []string{http.MethodGet, http.MethodPost}, MaxBodyBytes: maxBody},
		},
	})

	cfgJSON, err := json.Marshal(bootConfig{
		APIKey:         cfg.Key,
		Mount:          p.Base(),
		UIHost:         h.ui,
		RespectDNT:     cfg.RespectDNT,
		PersonProfiles: cfg.PersonProfiles,
	})
	if err != nil {
		panic(fmt.Sprintf("posthog: encoding boot config: %v", err)) // string/bool fields; unreachable
	}
	rendered := bytes.Replace(bootTemplate, []byte(configPlaceholder), cfgJSON, 1)
	if bytes.Equal(rendered, bootTemplate) {
		// The placeholder is the package's own invariant; losing it is
		// a build-time edit gone wrong, not a runtime condition.
		panic("posthog: boot.js no longer carries the " + configPlaceholder + " token")
	}
	p.js = rendered
	p.url = uihost.ScriptURL(p.Base()+"/boot.js", p.js)
	return p
}

// bootConfig is the JSON the bootstrap reads. encoding/json keeps
// declaration order and HTML-escapes <, >, & (\u003c …) — the escaping
// is load-bearing: it is what keeps a hostile Key value inert in the
// served bytes.
type bootConfig struct {
	APIKey         string `json:"apiKey"`
	Mount          string `json:"mount"`
	UIHost         string `json:"uiHost"`
	RespectDNT     bool   `json:"respectDNT"`
	PersonProfiles string `json:"personProfiles,omitempty"`
}

// Name implements framework.Plugin. It shadows the embedded relay's
// "relay" so the plugin registers (and fails, and logs) under its own
// name.
func (p *Plugin) Name() string { return "posthog" }

// Init wires the integration: the embedded relay's routes (ph-assets/,
// ph/), the rendered bootstrap served at {mount}/boot.js with the
// framework's versioned-script policy (strong ETag, immutable on a
// matching ?v=), and the identity endpoint at {mount}/whoami.
func (p *Plugin) Init(app *framework.App) error {
	if err := p.Relay.Init(app); err != nil {
		return err
	}
	app.Router().Get(p.Base()+"/boot.js", uihost.ScriptHandler(p.js))
	app.Router().Get(p.Base()+"/whoami", http.HandlerFunc(p.whoami))
	return nil
}

// ScriptURL returns the versioned URL of the rendered bootstrap: the
// value to pass to (*uihost.UIHost).RegisterExternalScript — or just
// call Attach, which does exactly that. Computed at New from the
// rendered bytes, so editing nothing but the config cache-busts it.
func (p *Plugin) ScriptURL() string { return p.url }

// Attach registers the bootstrap on the host in one call:
// h.RegisterExternalScript(p.ScriptURL()). The parameter is the
// one-method interface *uihost.UIHost already satisfies, so hosts wire
// this without this package reaching for the concrete host type.
func (p *Plugin) Attach(h interface{ RegisterExternalScript(string) error }) error {
	return h.RegisterExternalScript(p.ScriptURL())
}

// whoami answers the bootstrap's identity question: {"id":"..."} or
// {"id":null}, never a guess. no-store because identity can change on
// any login/logout; nosniff because the bytes are JSON.
func (p *Plugin) whoami(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	var id any // nil marshals to JSON null
	if p.cfg.Identify != nil {
		if s, ok := p.cfg.Identify(r); ok {
			id = s
		}
	} else if u, ok := handler.GetUser(r.Context()); ok && u != nil {
		switch v := u.(type) {
		case string:
			id = v
		case interface{ GetID() string }:
			// battery/auth principals (*auth.BasicUser and friends)
			// carry their identity here — checked before Stringer so a
			// type with both never leaks a display string as its id.
			id = v.GetID()
		case fmt.Stringer:
			id = v.String()
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
}
