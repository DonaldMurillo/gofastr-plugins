package genui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// The Anthropic Messages API composer.
//
// Written against the HTTP API with net/http rather than an SDK. The registry,
// the schema and the retry loop are the whole client; an SDK would add a
// dependency to this module and a version to track for about forty lines of
// request building. If a host wants the SDK it can implement [Composer] itself
// — that is what the interface is for.
//
// The model NEVER produces UI. It produces a composition tree naming registry
// components, which is then put through exactly the same [Validate] every other
// composition goes through. A model that emits something outside the registry
// gets the refusal text back and one more attempt; a model that fails twice
// fails the request. There is no path where unvalidated model output reaches a
// frame, and no path where a validation failure is "fixed up" instead of
// refused — quietly repairing a bad composition would teach nobody anything and
// hide the case this plugin exists to demonstrate.
const (
	// DefaultAnthropicModel is the model used when none is configured.
	DefaultAnthropicModel = "claude-sonnet-5"
	// DefaultAnthropicEndpoint is the Messages API endpoint.
	DefaultAnthropicEndpoint = "https://api.anthropic.com/v1/messages"
	// anthropicVersion is the required API version header.
	anthropicVersion = "2023-06-01"
	// composeToolName is the tool the model must call. Forcing a tool call is
	// how the answer arrives as a typed object instead of prose containing
	// JSON, which is the difference between parsing and guessing.
	composeToolName = "emit_composition"
)

// AnthropicComposer composes through the Anthropic Messages API.
//
// It is NOT the default: [FixtureComposer] is, so the demo and the whole test
// suite run with no credentials. A plugin whose tests need an API key is a
// plugin nobody can contribute to. Wire this one deliberately:
//
//	genui.New(genui.WithComposer(genui.NewAnthropicComposer(genui.AnthropicConfig{})))
type AnthropicComposer struct {
	cfg AnthropicConfig
}

// AnthropicConfig configures [AnthropicComposer]. The zero value reads the key
// from ANTHROPIC_API_KEY and uses the default model, endpoint and timeout.
type AnthropicConfig struct {
	// APIKey authenticates the call. Empty means read ANTHROPIC_API_KEY at
	// COMPOSE time, not at construction: a host that builds its plugin graph
	// before its secrets are loaded should still work.
	APIKey string
	// Model defaults to [DefaultAnthropicModel].
	Model string
	// Endpoint defaults to [DefaultAnthropicEndpoint]. Tests point this at an
	// httptest server, which is why this composer needs no key to be tested.
	Endpoint string
	// MaxTokens bounds the response. Compositions are small; the default is
	// deliberately modest so a runaway generation costs little.
	MaxTokens int
	// HTTPClient defaults to a client with a 60s timeout.
	HTTPClient *http.Client
	// Retries is how many EXTRA attempts a refused composition gets, each one
	// carrying the validator's complaint back to the model. Default 1: one
	// correction is worth having, an unbounded loop is a bill.
	Retries int
}

// NewAnthropicComposer builds a composer over the Messages API.
func NewAnthropicComposer(cfg AnthropicConfig) *AnthropicComposer {
	if cfg.Model == "" {
		cfg.Model = DefaultAnthropicModel
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultAnthropicEndpoint
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 2048
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	} else if cfg.Retries == 0 {
		cfg.Retries = 1
	}
	return &AnthropicComposer{cfg: cfg}
}

func (a *AnthropicComposer) key() string {
	if a.cfg.APIKey != "" {
		return a.cfg.APIKey
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// Compose asks the model for a composition and refuses anything the validator
// refuses. Implements [Composer].
func (a *AnthropicComposer) Compose(ctx context.Context, prompt string, r Registry) (Composition, error) {
	if a.key() == "" {
		return Composition{}, fmt.Errorf("genui: no Anthropic API key (set APIKey or ANTHROPIC_API_KEY)")
	}
	msgs := []anthropicMessage{{Role: "user", Content: []anthropicBlock{{Type: "text", Text: prompt}}}}

	var lastErr error
	for attempt := 0; attempt <= a.cfg.Retries; attempt++ {
		raw, err := a.call(ctx, r, msgs)
		if err != nil {
			return Composition{}, err
		}
		var c Composition
		if err := json.Unmarshal(raw, &c); err != nil {
			lastErr = fmt.Errorf("genui: model returned a tool input that is not a composition: %w", err)
		} else if verr := Validate(c, r, a.actions(ctx)); verr != nil {
			lastErr = verr
		} else {
			return c, nil
		}
		if attempt == a.cfg.Retries {
			break
		}
		// Hand the refusal back verbatim. The validator's message names the
		// offending path, which is exactly what a model needs to correct it —
		// and exactly what a human would need too.
		msgs = append(msgs,
			anthropicMessage{Role: "assistant", Content: []anthropicBlock{{Type: "text", Text: string(raw)}}},
			anthropicMessage{Role: "user", Content: []anthropicBlock{{Type: "text",
				Text: "That composition was refused: " + lastErr.Error() +
					"\n\nEmit a corrected composition. Use only the components and props in the tool schema."}}},
		)
	}
	return Composition{}, fmt.Errorf("genui: model produced no valid composition in %d attempts: %w", a.cfg.Retries+1, lastErr)
}

// actions returns the allow-list to validate against. The composer is handed
// the registry but not the plugin, so a host wiring this directly gets the
// conservative answer: no actions unless the plugin injected them.
func (a *AnthropicComposer) actions(ctx context.Context) []string {
	if v, ok := ctx.Value(actionsContextKey{}).([]string); ok {
		return v
	}
	return nil
}

// actionsContextKey carries the plugin's action allow-list to a Composer that
// wants to validate against it. The Composer interface takes a registry and not
// a plugin on purpose — a composer should not be able to reach into the plugin
// — so the one thing it legitimately needs rides on the context instead.
type actionsContextKey struct{}

// WithActionsContext returns ctx carrying the action allow-list. The plugin
// calls this before invoking a Composer; a host calling a Composer directly can
// use it too.
func WithActionsContext(ctx context.Context, actions []string) context.Context {
	return context.WithValue(ctx, actionsContextKey{}, append([]string{}, actions...))
}

// --- wire types --------------------------------------------------------------

type anthropicBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// call makes one request and returns the tool input the model emitted.
func (a *AnthropicComposer) call(ctx context.Context, r Registry, msgs []anthropicMessage) (json.RawMessage, error) {
	body := map[string]any{
		"model":      a.cfg.Model,
		"max_tokens": a.cfg.MaxTokens,
		"system":     systemPrompt(r),
		"messages":   msgs,
		"tools": []any{map[string]any{
			"name":         composeToolName,
			"description":  "Emit the composition to render. This is the only way to answer.",
			"input_schema": compositionJSONSchema(r),
		}},
		// Force the tool. Without this the model may answer in prose and the
		// caller is back to scraping JSON out of a paragraph.
		"tool_choice": map[string]any{"type": "tool", "name": composeToolName},
	}
	enc, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("genui: encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.Endpoint, bytes.NewReader(enc))
	if err != nil {
		return nil, fmt.Errorf("genui: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.key())
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("genui: calling the model: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The body can carry a key; report the status and the API's error type,
		// never the raw body.
		var e struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return nil, fmt.Errorf("genui: model returned %d (%s)", resp.StatusCode, e.Error.Type)
	}
	var out struct {
		Content []anthropicBlock `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("genui: decoding response: %w", err)
	}
	for _, b := range out.Content {
		if b.Type == "tool_use" && b.Name == composeToolName && len(b.Input) > 0 {
			return b.Input, nil
		}
	}
	return nil, fmt.Errorf("genui: model answered without calling %s", composeToolName)
}

// systemPrompt describes the job and the boundary. It states the registry, but
// the registry is enforced by [Validate] rather than by the prompt — a prompt
// is guidance and a validator is a rule, and only one of them is load-bearing.
func systemPrompt(r Registry) string {
	var b strings.Builder
	b.WriteString("You compose user interfaces by choosing components from a fixed registry.\n\n")
	b.WriteString("You cannot write HTML, CSS, JavaScript, or free-form styling. ")
	b.WriteString("You name components and set their declared props; anything else is refused.\n\n")
	b.WriteString("Available components:\n")
	for _, name := range r.Names() {
		spec, _ := r.Lookup(name)
		b.WriteString("- " + name)
		if len(spec.Props) > 0 {
			b.WriteString(" (props: ")
			parts := make([]string, 0, len(spec.Props))
			for _, p := range spec.Props {
				s := p.Name + ": " + string(p.Type)
				if len(p.Enum) > 0 {
					s += " one of " + strings.Join(p.Enum, "|")
				}
				if p.Required {
					s += ", required"
				}
				parts = append(parts, s)
			}
			b.WriteString(strings.Join(parts, "; ") + ")")
		}
		if spec.AcceptsChildren {
			b.WriteString(" [accepts children]")
		}
		if spec.CarriesAction {
			b.WriteString(" [may carry an action]")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nCompose something useful and readable for the request. ")
	b.WriteString("Prefer a Stack or Card at the root. Keep it under 40 nodes.")
	return b.String()
}

// compositionJSONSchema is the tool's input schema: the shape, not the rules.
// It deliberately does not try to encode every per-component prop constraint —
// a JSON Schema that enumerated eight components' prop sets would be a second
// copy of the registry, drifting from the first. The schema gets the model into
// the right shape; [Validate] is what decides whether the answer is acceptable.
func compositionJSONSchema(r Registry) map[string]any {
	node := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"component": map[string]any{"type": "string", "enum": r.Names()},
			"props":     map[string]any{"type": "object"},
			"action":    map[string]any{"type": "string"},
			"children":  map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		},
		"required": []string{"component"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"schemaVersion": map[string]any{"type": "string", "enum": []string{SchemaVersion}},
			"root":          node,
		},
		"required": []string{"schemaVersion", "root"},
	}
}
