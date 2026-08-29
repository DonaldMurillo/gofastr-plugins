// Package genui is the GoFastr generative-UI plugin: a model composes a
// view out of a FIXED registry of React components, and nothing else.
//
// The bounded registry is the entire containment story. A composition is a
// tree of {component, props, action?, children?} where the component MUST
// name a registry entry, the props MUST match that entry's declared schema
// exactly (unknown key → rejected, wrong type → rejected; there is no
// passthrough, no style, no className, no dangerouslySetInnerHTML — no
// entry declares them), an optional action MUST name an entry in the
// host-supplied allow-list, and children only exist where the entry accepts
// them. Depth is bounded (16) and node count is bounded (200): a model that
// emits a runaway tree fails validation, never the renderer.
//
// Where the model runs: in Go, never in the frame. The composition is
// produced HOST-side and arrives at the frame already validated; the frame
// renders a tree and never talks to a model, holds a key, or opens a socket
// — the framed CSP still says connect-src 'none'. That direction is the
// design: an API key in a browser is not a key, and a frame that could call
// a model could exfiltrate the document it was composing over.
//
// only thing ever served. The frame validates again before rendering,
// because "the host already checked it" is exactly the assumption that
// makes a second bug fatal; its copy is cheap (same rules, same registry,
// no trust in the bridge).
//
// Async by design: POST /compose starts a generation and returns an id
// immediately; the host polls GET /composition/{id} and pushes the finished
// tree over the bridge when it is ready. No streaming tokens into the DOM.
//
// Capabilities: genui:compose and theme:read. The registry, the allow-list
// and the composer all live host-side, so the frame's grants stay minimal.
// pluginhost.Allow is a capability gate, NOT authentication: it passes for
// anonymous callers, so a host that persists or acts on compositions for
// real must check the session in its own wrapper — WithDevGrantAll skips
// the gate entirely and MUST NOT survive into a production mount.
package genui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// Identity and route constants. Both this plugin and host/adapter.js
// hard-code these exactly. The demo lives at /genui.
//
// These mirror the genui row in plugins.json (added by the coordinator);
// internal/registry tests pin Name + RoutePrefix against that row, so they
// MUST NOT drift.
const (
	Name             = "genui"
	Version          = "0.1.0"
	RoutePrefix      = "/__gofastr/plugin/genui"
	FrameHTMLURL     = RoutePrefix + "/genui.html"
	GenuiJSURL       = RoutePrefix + "/genui.js"
	GenuiCSSURL      = RoutePrefix + "/genui.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	ConfigScriptURL  = RoutePrefix + "/config.js"
	ComposeURL       = RoutePrefix + "/compose"
	// CompositionBaseURL carries the per-id route
	// CompositionBaseURL + "/{id}".
	CompositionBaseURL = RoutePrefix + "/composition"
	DemoURL            = "/genui"
	SchemaVersion      = "genui-v1"

	// CapCompose gates both routes: starting a generation and reading one
	// back. The frame never calls either (connect-src 'none'); the
	// privileged host adapter does, on the page's authority.
	CapCompose = "genui:compose"

	defaultDocID     = "demo"
	defaultMinHeight = "360px"

	// maxEnvelopeBytes caps the /compose request body. A prompt is tiny;
	// anything near this cap is a mistake or an attack.
	maxEnvelopeBytes int64 = 16 << 10
	// maxPromptRunes caps the prompt itself once decoded.
	maxPromptRunes = 2000
	// compositionCap bounds the in-memory composition store (oldest
	// evicted). This plugin has no database and must not grow without
	// limit.
	compositionCap = 64
)

// DefaultCapabilities is the grant set advertised to the frame: composing
// and bridging theme tokens. There are deliberately no optional
// capabilities — the frame has no write surface at all.
func DefaultCapabilities() []string {
	return []string{CapCompose, "theme:read"}
}

// DefaultActions is the default action allow-list: the actions a generated
// Button may name. A generated button cannot point anywhere the host did
// not name; override with [WithActions] when the host's vocabulary differs.
func DefaultActions() []string {
	return []string{"export"}
}

// Plugin is the generative-UI plugin. It implements [framework.Plugin] and
// mirrors the scanner shape (opaque-origin sandboxed iframe, protocol v1
// over postMessage, go:embed'd frame bundle) with the genui inversion: the
// EXPENSIVE side (the model) runs host-side behind [Composer], and the
// frame only ever renders trees that already passed [Validate].
type Plugin struct {
	manifest     pluginhost.Manifest
	capabilities []string
	actions      []string
	composer     Composer
	store        compositionStore
	devGrantAll  bool

	withDemoPage bool
}

// Option configures a [Plugin].
type Option func(*Plugin)

// WithCapabilities overrides the grant set advertised to the frame. Default:
// [DefaultCapabilities]. There is nothing to expand into — the frame has no
// optional capabilities — but the override exists for hosts that mint scoped
// tokens ("genui:*" implies genui:compose under the framework's wildcard
// grammar, and the runtime gate matches it).
func WithCapabilities(caps ...string) Option {
	return func(p *Plugin) { p.capabilities = append([]string{}, caps...) }
}

// WithActions overrides the action allow-list (default: [DefaultActions]).
// Every action named in a composition must be in this list or the
// composition is refused at validation; the same list is published to the
// frame via config.js so both sides enforce one vocabulary. An empty or
// duplicated name panics in [New]: a typo'd allow-list entry silently never
// matches, which is the worst way to learn about it.
func WithActions(actions ...string) Option {
	return func(p *Plugin) { p.actions = append([]string{}, actions...) }
}

// WithComposer swaps the composition producer (default: [FixtureComposer],
// deterministic and offline). This is the seam a real model client goes
// behind later; its output is validated exactly like any other composer's.
func WithComposer(c Composer) Option {
	return func(p *Plugin) { p.composer = c }
}

// WithDevGrantAll short-circuits the capability gate (demo / tests). Both
// routes stay gated for real mounts; production hosts never set this.
func WithDevGrantAll() Option { return func(p *Plugin) { p.devGrantAll = true } }

// New constructs a Plugin. The platform manifest is built and Validate()'d
// here, and the action allow-list is fail-loud validated, so a bad
// isolation/sandbox config or a typo'd action aborts construction rather
// than surfacing as a composition that can never validate.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		actions:      DefaultActions(),
		composer:     FixtureComposer{},
		manifest: pluginhost.Manifest{
			Entry:     FrameHTMLURL,
			Isolation: pluginhost.IsolationSandboxOpaque,
			Sandbox:   []string{pluginhost.DefaultSandbox},
			MinHeight: defaultMinHeight,
			Schema:    SchemaVersion,
			Title:     "Generative UI",
		},
	}
	p.store.reset()
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	p.validateConfig()
	if len(p.capabilities) == 0 {
		p.capabilities = DefaultCapabilities()
	}
	if p.composer == nil {
		p.composer = FixtureComposer{}
	}
	p.manifest.Capabilities = p.capabilities
	if err := p.manifest.Validate(); err != nil {
		panic("genui: invalid manifest: " + err.Error())
	}
	return p
}

// validateConfig is the fail-loud gate for the invariants [New] enforces.
// It panics on violation so a bad allow-list never reaches Init — same
// posture as [pluginhost.Manifest.Validate].
func (p *Plugin) validateConfig() {
	for i, a := range p.actions {
		if strings.TrimSpace(a) == "" {
			panic("genui: empty action name in allow-list (WithActions)")
		}
		if slices.Contains(p.actions[i+1:], a) {
			panic("genui: duplicate action in allow-list: " + a)
		}
	}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// Init registers every asset and route on the app's router. The frame's
// assets are framed (AssetServer applies the framing/CORP/CSP relaxation
// and the fixed framedCSP — connect-src 'none', sandbox allow-scripts — to
// exactly those); the adapter and config.js are host-page scripts. The two
// data routes are capability-gated on [CapCompose]; see handlers below for
// the auth posture.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt) // idempotent across plugins

	// Framed first-party assets: the frame document + its JS/CSS
	// sub-resources (built from genui/js into genui/assets).
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "genui.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "genui.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "genui.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	// Host-page (non-framed) scripts: the adapter that owns the compose
	// pipeline, and config.js that publishes this instance's action
	// allow-list for the adapter's narrowing and the frame's validator.
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.AddBytes(ConfigScriptURL, "text/javascript; charset=utf-8", false, p.configScriptBytes())
	srv.Register(rt)

	// Data routes. POST /compose starts a generation and returns an id
	// immediately; GET /composition/{id} reports its state. Both are
	// capability-gated; neither is reachable from the frame.
	rt.Post(ComposeURL, http.HandlerFunc(p.handleCompose))
	rt.Get(CompositionBaseURL+"/{id}", http.HandlerFunc(p.handleGetComposition))

	if p.withDemoPage {
		rt.Get(DemoURL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The demo page is a top-level document; carry a plain app CSP so
			// the framed child (its own framedCSP) and the host scripts load
			// cleanly. Same shape as scanner's and logstream's.
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

// Actions returns the action allow-list this instance enforces (copy).
func (p *Plugin) Actions() []string {
	return append([]string{}, p.actions...)
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
// plugin instance's action allow-list as window.__gofastrGenuiConfig. The
// adapter (loaded after it via [UIHostOption] / the demo page) narrows
// frame-sourced action names against it, and merges it into the manifest
// config the broker bridges to the frame as init.config, so the frame's
// validator enforces the SAME vocabulary the Go side does. JSON is a safe
// subset of a JS object literal and this is a standalone .js file (not
// inline), so no script-context escaping is required.
func (p *Plugin) configScriptBytes() []byte {
	cfg := struct {
		Actions  []string `json:"actions"`
		Registry []string `json:"registry"`
	}{
		Actions:  p.actions,
		Registry: DefaultRegistry().Names(),
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		// A struct of two []strings; marshal cannot fail in practice. Fail
		// loud rather than ship an empty allow-list.
		panic("genui: marshal frame config: " + err.Error())
	}
	return []byte("window.__gofastrGenuiConfig = " + string(b) + ";\n")
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
	// DocID is the genui identity for this mount (logging/debug key; a
	// composition is ephemeral state, not a persisted doc).
	DocID string
	// MinHeight is the composition viewport height. Defaults to 460px.
	MinHeight string
	// Capabilities is an optional CSV grant override.
	Capabilities string
}

// Mount renders the generic mount marker. A genui mount has no hidden form
// field — nothing round-trips on submit; the marker is the whole mount and
// the adapter drives the frame from the page. All interpolated values are
// HTML-escaped inside [pluginhost.MountMarker].
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

// --- the composition store -------------------------------------------------

// composition states. A record is pending from POST /compose until the
// composer returns; then ready (with the validated tree) or failed (with
// the refusal reason, path-bearing when the validator produced it).
const (
	statePending = "pending"
	stateReady   = "ready"
	stateFailed  = "failed"
)

// compositionRecord is one stored generation.
type compositionRecord struct {
	state string
	tree  Composition
	err   string
}

// compositionStore is the bounded in-memory store: a map plus the insertion
// order that decides who gets evicted. Cap [compositionCap] (64), oldest
// out. This plugin has no database and must not grow without limit.
type compositionStore struct {
	mu    sync.Mutex
	order []string
	byID  map[string]compositionRecord
}

func (s *compositionStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = nil
	s.byID = make(map[string]compositionRecord)
}

// add inserts a pending record for id, evicting the oldest records beyond
// the cap.
func (s *compositionStore) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, id)
	s.byID[id] = compositionRecord{state: statePending}
	for len(s.order) > compositionCap {
		delete(s.byID, s.order[0])
		s.order = s.order[1:]
	}
}

// set transitions id to a terminal state. A record evicted while its
// generation was in flight simply no-ops: nobody can poll it anymore.
func (s *compositionStore) set(id string, rec compositionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return
	}
	s.byID[id] = rec
}

func (s *compositionStore) get(id string) (compositionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	return rec, ok
}

func (s *compositionStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// newCompositionID mints an unguessable id for a generation.
func newCompositionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("genui: composition id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// --- the routes --------------------------------------------------------------
//
// POST /compose          {prompt} → {id}   gate genui:compose
// GET  /composition/{id} → {state: "pending"|"ready"|"failed", tree?, error?}
//                                             gate genui:compose
//
// The generation runs in a goroutine with a background context: the request
// that started it is gone by the time the composer answers, and the plugin
// has no lifecycle hook to cancel into — the fixture composer is instant
// and a real client is expected to carry its own timeout.
//
// pluginhost.Allow is a capability gate, NOT authentication: it passes for
// anonymous callers. POST /compose creates state (a stored composition), so
// a host that mounts this for real must check the session in its own
// wrapper — the same posture formbuilder documents for POST /save.

// handleCompose implements POST /compose.
func (p *Plugin) handleCompose(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapCompose) {
		writeJSONCapabilityDenied(w, CapCompose)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if !decodeEnvelope(w, r, &body) {
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_PROMPT", "prompt is empty")
		return
	}
	if len([]rune(prompt)) > maxPromptRunes {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_PROMPT",
			"prompt exceeds the %d-rune cap", maxPromptRunes)
		return
	}

	id := newCompositionID()
	p.store.add(id)
	go p.runComposition(id, prompt)

	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// runComposition is the background generation: compose, validate against
// the fixed registry and THIS instance's allow-list, store the outcome. The
// stored tree is always the validated one — a composition that does not
// pass [Validate] is never persisted, exactly as the contract demands.
func (p *Plugin) runComposition(id, prompt string) {
	comp, err := p.composer.Compose(context.Background(), prompt, DefaultRegistry())
	if err != nil {
		p.store.set(id, compositionRecord{state: stateFailed, err: err.Error()})
		return
	}
	if verr := Validate(comp, DefaultRegistry(), p.actions); verr != nil {
		p.store.set(id, compositionRecord{state: stateFailed, err: verr.Error()})
		return
	}
	p.store.set(id, compositionRecord{state: stateReady, tree: comp})
}

// handleGetComposition implements GET /composition/{id}.
func (p *Plugin) handleGetComposition(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapCompose) {
		writeJSONCapabilityDenied(w, CapCompose)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_ID", "composition id is required")
		return
	}
	rec, ok := p.store.get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "E_NOT_FOUND", "no such composition")
		return
	}
	switch rec.state {
	case statePending:
		writeJSON(w, http.StatusOK, map[string]string{"state": statePending})
	case stateReady:
		writeJSON(w, http.StatusOK, struct {
			State string      `json:"state"`
			Tree  Composition `json:"tree"`
		}{State: stateReady, Tree: rec.tree})
	default:
		writeJSON(w, http.StatusOK, struct {
			State string `json:"state"`
			Error string `json:"error"`
		}{State: stateFailed, Error: rec.err})
	}
}

// --- shared helpers --------------------------------------------------------

// decodeEnvelope reads ONE JSON value into dst under the envelope cap and
// rejects any non-whitespace after it — a second object or stray bytes.
// Trailing whitespace (the curl -d @body.json newline) is allowed; the DoS
// concern is bytes on the wire, and MaxBytesReader caps those. ok=false
// means the error response is already written.
func decodeEnvelope(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON", "%s", err.Error())
		return false
	}
	rest, err := io.ReadAll(io.MultiReader(dec.Buffered(), r.Body))
	if err != nil || len(strings.TrimSpace(string(rest))) > 0 {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON", "trailing data after the JSON value")
		return false
	}
	return true
}

// writeJSON emits a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError emits the canonical {error, message?} error envelope.
// Every refusal carries a stable machine-readable code so the adapter (and
// the demo's status line) can branch on it without parsing free text.
func writeJSONError(w http.ResponseWriter, status int, code, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": fmt.Sprintf(format, args...),
	})
}

// writeJSONCapabilityDenied delegates to the platform helper so every route
// denies uniformly with the offending capability named.
func writeJSONCapabilityDenied(w http.ResponseWriter, capability string) {
	pluginhost.WriteCapabilityDenied(w, capability)
}
