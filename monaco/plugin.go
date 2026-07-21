package monaco

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

// Identity and route constants. Both this plugin and host/adapter.js hard-code
// these exactly. The demo lives at /monaco (mirrors mermaid's /mermaid) so
// multiple plugins co-mount without colliding on "/".
const (
	Name             = "monaco"
	Version          = "0.1.0-phase0"
	RoutePrefix      = "/__gofastr/plugin/monaco"
	EditorHTMLURL    = RoutePrefix + "/editor.html"
	EditorJSURL      = RoutePrefix + "/editor.js"
	EditorCSSURL     = RoutePrefix + "/editor.css"
	AdapterScriptURL = RoutePrefix + "/adapter.js"
	// ConfigScriptURL is a tiny host-page script that publishes this plugin
	// instance's EditorConfig (set via Go options) as a global the adapter
	// merges into the manifest config it registers with the platform broker.
	// It is served non-framed (host-page) and is static per plugin instance.
	ConfigScriptURL = RoutePrefix + "/config.js"
	SaveURL         = RoutePrefix + "/save"
	DemoURL         = "/monaco"
	SchemaVersion   = "monaco-v1"

	defaultDocID         = "demo"
	defaultCodeField     = "code"
	defaultLanguageField = "language"
	defaultMinHeight     = "320px"
)

// DefaultCapabilities is the grant set advertised to the editor. Monaco has no
// upload path -- only document read/write + theme:read (same as mermaid).
func DefaultCapabilities() []string {
	return []string{"document:read", "document:write", "theme:read"}
}

// savedDoc is the canonical monaco-v1 document. The json tags are load-bearing:
// LoadDoc marshals this into the mount marker's data-fui-plugin-doc, and the
// frame's deriveDoc reads lowercase {code, language}. Without the tags Go emits
// {Code, Language} and the frame silently mounts an empty editor on load.
type savedDoc struct {
	Code     string `json:"code"`
	Language string `json:"language"`
}

// Plugin is the Monaco code-editor plugin. It implements framework.Plugin and
// mirrors the mermaid plugin's shape (opaque-origin sandboxed iframe, protocol
// v1 over postMessage, go:embed'd frame bundle, capability gate, save handler).
type Plugin struct {
	devGrantAll   bool
	withDemoPage  bool
	capabilities  []string
	defaultConfig EditorConfig
	saveHandler   func(ctx context.Context, req SaveRequest) error
	manifest      pluginhost.Manifest
	mu            sync.RWMutex
	docs          map[string]savedDoc
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
// canonical {code, language} doc in an in-memory map keyed by DocID.
func WithSaveHandler(fn func(ctx context.Context, req SaveRequest) error) Option {
	return func(p *Plugin) { p.saveHandler = fn }
}

// --- Editor-config options --------------------------------------------------
//
// These configure the editor defaults the plugin advertises. They are bridged
// to the frame via init.config (through the config.js host script + the
// adapter's manifest config). The frame applies them on mount; per-field
// options below each set one slot of EditorConfig.

// WithLanguage sets the default editor language (e.g. "go", "javascript").
func WithLanguage(lang string) Option {
	return func(p *Plugin) {
		if lang != "" {
			p.defaultConfig.Language = lang
		}
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

// WithReadOnly mounts the editor read-only by default.
func WithReadOnly() Option {
	return func(p *Plugin) { p.defaultConfig.ReadOnly = true }
}

// WithMinimap enables the minimap by default (off by default).
func WithMinimap() Option {
	return func(p *Plugin) { p.defaultConfig.Minimap = true }
}

// WithWordWrap enables word wrap by default (off by default).
func WithWordWrap() Option {
	return func(p *Plugin) { p.defaultConfig.WordWrap = true }
}

// WithoutLineNumbers hides the line-number gutter (shown by default).
func WithoutLineNumbers() Option {
	return func(p *Plugin) { p.defaultConfig.LineNumbers = false }
}

// WithFontSize sets the editor font size in pixels.
func WithFontSize(px int) Option {
	return func(p *Plugin) {
		if px > 0 {
			p.defaultConfig.FontSize = px
		}
	}
}

// WithWorkers OPTS IN to Monaco web workers (language services: richer TS/JS
// completions, diagnostics, formatting). OFF by default: under the opaque-
// origin sandbox (sandbox="allow-scripts" WITHOUT allow-same-origin) a worker
// loaded from a same-origin URL or a blob:/data: URL is restricted, so the
// editor boots worker-free by default and degrades gracefully (the monarch
// tokenizer that drives syntax highlighting runs on the main thread). When
// opted in, the frame attempts a worker and falls back to worker-free if the
// sandbox refuses it.
func WithWorkers() Option {
	return func(p *Plugin) { p.defaultConfig.Workers = true }
}

// WithDiff mounts the diff editor by default, showing original to modified.
// language defaults to the EditorConfig language when DiffConfig.Language is
// empty. WithDiff supersedes the normal editor mount for this plugin instance.
func WithDiff(original, modified, language string) Option {
	return func(p *Plugin) {
		p.defaultConfig.Diff = &DiffConfig{
			Original: original,
			Modified: modified,
			Language: language,
		}
	}
}

// WithEditorConfig replaces the full default EditorConfig. Use the field-
// specific options above for ergonomics; this is the escape hatch.
func WithEditorConfig(cfg EditorConfig) Option {
	return func(p *Plugin) { p.defaultConfig = cfg }
}

// New constructs a Plugin. The platform manifest is built and Validate()'d here
// so a bad isolation/sandbox config aborts construction rather than silently
// de-opaquing the frame at runtime.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		capabilities: DefaultCapabilities(),
		docs:         make(map[string]savedDoc),
		defaultConfig: EditorConfig{
			Language:    "plaintext",
			Theme:       "auto",
			LineNumbers: true,
			FontSize:    14,
			// ReadOnly, Minimap, WordWrap, Workers default to false.
		},
		manifest: pluginhost.Manifest{
			Entry:        EditorHTMLURL,
			Isolation:    pluginhost.IsolationSandboxOpaque,
			Sandbox:      []string{pluginhost.DefaultSandbox},
			Capabilities: DefaultCapabilities(),
			MinHeight:    defaultMinHeight,
			Schema:       SchemaVersion,
			Title:        "Monaco code editor",
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
	if err := p.manifest.Validate(); err != nil {
		panic("monaco: invalid manifest: " + err.Error())
	}
	return p
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Manifest() pluginhost.Manifest { return p.manifest }

// DefaultConfig returns the editor-config defaults this plugin instance will
// advertise (set via the With* options). The frame receives these through
// init.config and applies them on mount.
func (p *Plugin) DefaultConfig() EditorConfig { return p.defaultConfig }

// Init registers every asset and RPC route on the app's router.
func (p *Plugin) Init(app *framework.App) error {
	rt := app.Router()
	pluginhost.RegisterBrokerRoute(rt)
	srv := pluginhost.NewAssetServer(framedAssets(), RoutePrefix, []pluginhost.AssetSpec{
		{Name: "editor.html", ContentType: "text/html; charset=utf-8", Framed: true},
		{Name: "editor.js", ContentType: "text/javascript; charset=utf-8", Framed: true},
		{Name: "editor.css", ContentType: "text/css; charset=utf-8", Framed: true},
	})
	srv.AddBytes(AdapterScriptURL, "text/javascript; charset=utf-8", false, adapterJSBytes)
	srv.AddBytes(ConfigScriptURL, "text/javascript; charset=utf-8", false, p.configScriptBytes())
	srv.Register(rt)
	rt.Post(SaveURL, http.HandlerFunc(p.handleSave))
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
// plugin instance's EditorConfig as window.__gofastrMonacoConfig. The adapter
// (loaded after it via UIHostOption / the demo page) merges it into the manifest
// config it registers with the platform broker. JSON is a safe subset of a JS
// object literal and this is a standalone .js file (not inline), so no script-
// context escaping is required.
func (p *Plugin) configScriptBytes() []byte {
	b, err := json.Marshal(p.defaultConfig)
	if err != nil {
		// EditorConfig is a plain struct of primitives + a pointer; marshal
		// cannot fail in practice. Fail loud rather than ship an empty config.
		panic("monaco: marshal default config: " + err.Error())
	}
	return []byte("window.__gofastrMonacoConfig = " + string(b) + ";\n")
}

// LoadDoc returns the last-saved canonical {code, language} JSON for docID from
// the in-memory default store. ok is false when the doc has never been saved.
// The returned docJSON is the canonical interchange blob (schema monaco-v1).
func (p *Plugin) LoadDoc(ctx context.Context, docID string) (docJSON string, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	d, found := p.docs[docID]
	if !found {
		return "", false
	}
	b, _ := json.Marshal(savedDoc{Code: d.Code, Language: d.Language})
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
	p.docs[req.DocID] = savedDoc{Code: req.Code, Language: req.Language}
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
	DocID         string
	CodeField     string // hidden input name for the code text (default "code")
	LanguageField string // hidden input name for the language (default "language")
	MinHeight     string
	Doc           string // optional initial {code, language} JSON, server-rendered for reload round-trip
}

// Mount renders the mount marker div plus the two hidden inputs the host
// adapter syncs on docChanged (code + language). It wraps the platform
// pluginhost.MountMarker and adds the monaco-specific data-fui-plugin-for
// attribute naming the code + language hidden fields. Drop it into a form. All
// interpolated values are HTML-escaped via render.Escape inside MountMarker.
func Mount(cfg MountConfig) render.HTML {
	if cfg.DocID == "" {
		cfg.DocID = defaultDocID
	}
	if cfg.CodeField == "" {
		cfg.CodeField = defaultCodeField
	}
	if cfg.LanguageField == "" {
		cfg.LanguageField = defaultLanguageField
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
			{Name: "data-fui-plugin-for", Value: cfg.CodeField + "," + cfg.LanguageField},
		},
		Fields: []pluginhost.Field{
			{Name: cfg.CodeField},
			{Name: cfg.LanguageField},
		},
	})
}

// EditorConfig is the editor configuration bridged to the frame via init.config
// (through config.js + the adapter's manifest config). Every field is always
// serialized (no omitempty) so the frame always receives a complete config and
// never has to guess a default. The With* options above set individual slots.
type EditorConfig struct {
	Language    string      `json:"language"`
	Theme       string      `json:"theme"` // "light" | "dark" | "auto"
	ReadOnly    bool        `json:"readOnly"`
	Minimap     bool        `json:"minimap"`
	WordWrap    bool        `json:"wordWrap"`
	LineNumbers bool        `json:"lineNumbers"`
	FontSize    int         `json:"fontSize"`
	Workers     bool        `json:"workers"`
	Diff        *DiffConfig `json:"diff,omitempty"`
}

// DiffConfig selects the diff-editor mount. When present (non-nil) the frame
// mounts monaco.editor.createDiffEditor instead of the normal editor.
type DiffConfig struct {
	Original string `json:"original"`
	Modified string `json:"modified"`
	Language string `json:"language,omitempty"`
}

// ErrConflict is the sentinel a WithSaveHandler hook returns to signal that the
// save lost an optimistic-concurrency check — the stored document changed under
// the editor since it loaded. handleSave maps it to HTTP 409 (E_CONFLICT)
// rather than the generic 500 (E_SAVE), which is the one status the host
// adapter relays back to the frame as a distinct saveResult so the editor can
// keep the doc dirty and warn the user instead of silently dropping their
// edits. Wrap it (fmt.Errorf("...: %w", monaco.ErrConflict)) to add context;
// handleSave uses errors.Is.
var ErrConflict = errors.New("monaco: save conflict")

// SaveRequest is the persistence payload handed to the save handler.
type SaveRequest struct {
	DocID         string
	Code          string
	Language      string
	SchemaVersion string
}
