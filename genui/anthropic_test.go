package genui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The composer is tested against a fake Messages API, never the real one. That
// is not only about cost: a test that calls a model is a test whose result
// depends on the model, and the thing under test here is what this code does
// with an answer — including the answers a model should not have given.

// fakeAPI serves canned tool_use responses in order, and records the requests.
func fakeAPI(t *testing.T, inputs ...string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		decoded["_apiKey"] = r.Header.Get("x-api-key")
		decoded["_version"] = r.Header.Get("anthropic-version")
		got = append(got, decoded)

		if i >= len(inputs) {
			t.Errorf("fake API called %d times, only %d responses canned", i+1, len(inputs))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		in := inputs[i]
		i++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","name":"` + composeToolName + `","input":` + in + `}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

const validTree = `{"schemaVersion":"genui-v1","root":{"component":"Stack","props":{"gap":"md","direction":"column"},` +
	`"children":[{"component":"Heading","props":{"text":"Hi","level":2}}]}}`

func TestAnthropicComposerReturnsAValidatedComposition(t *testing.T) {
	srv, reqs := fakeAPI(t, validTree)
	c := NewAnthropicComposer(AnthropicConfig{APIKey: "test-key", Endpoint: srv.URL})

	comp, err := c.Compose(context.Background(), "greet me", DefaultRegistry())
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if comp.Root == nil || comp.Root.Component != "Stack" {
		t.Fatalf("unexpected composition: %+v", comp)
	}
	if len(*reqs) != 1 {
		t.Fatalf("made %d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r["_apiKey"] != "test-key" {
		t.Errorf("api key header = %v", r["_apiKey"])
	}
	if r["_version"] != anthropicVersion {
		t.Errorf("anthropic-version header = %v, want %s", r["_version"], anthropicVersion)
	}
	// The tool must be FORCED. Without tool_choice the model may answer in
	// prose and the caller is back to scraping JSON out of a paragraph.
	tc, _ := r["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != composeToolName {
		t.Errorf("tool_choice = %v, want the composition tool forced", r["tool_choice"])
	}
}

// The point of the whole design: a model's output is not trusted because it
// came from a model. It goes through the same validator as everything else.
func TestAnthropicComposerRefusesOutsideTheRegistryThenRetries(t *testing.T) {
	bad := `{"schemaVersion":"genui-v1","root":{"component":"ScriptTag","props":{"src":"https://example.com/x.js"}}}`
	srv, reqs := fakeAPI(t, bad, validTree)
	c := NewAnthropicComposer(AnthropicConfig{APIKey: "k", Endpoint: srv.URL})

	comp, err := c.Compose(context.Background(), "sneak a script in", DefaultRegistry())
	if err != nil {
		t.Fatalf("Compose after retry: %v", err)
	}
	if comp.Root.Component != "Stack" {
		t.Fatalf("second attempt not used: %+v", comp)
	}
	if len(*reqs) != 2 {
		t.Fatalf("made %d requests, want 2 (one refusal, one correction)", len(*reqs))
	}
	// The correction must carry the validator's own complaint, naming the
	// offending path — that message is what a model needs to fix it, and it is
	// the same message a human would get.
	second, _ := json.Marshal((*reqs)[1]["messages"])
	if !strings.Contains(string(second), "ScriptTag") {
		t.Errorf("the retry did not tell the model what was refused:\n%s", second)
	}
}

func TestAnthropicComposerGivesUpRatherThanRepairing(t *testing.T) {
	bad := `{"schemaVersion":"genui-v1","root":{"component":"Marquee","props":{}}}`
	srv, _ := fakeAPI(t, bad, bad)
	c := NewAnthropicComposer(AnthropicConfig{APIKey: "k", Endpoint: srv.URL})

	// Quietly repairing a refused composition would hide exactly the case this
	// plugin exists to demonstrate. Two refusals is an error, not a fallback.
	if _, err := c.Compose(context.Background(), "x", DefaultRegistry()); err == nil {
		t.Fatal("expected an error after every attempt was refused")
	} else if !strings.Contains(err.Error(), "Marquee") {
		t.Errorf("the final error should carry the last refusal, got: %v", err)
	}
}

func TestAnthropicComposerValidatesActionsAgainstTheHostAllowList(t *testing.T) {
	tree := `{"schemaVersion":"genui-v1","root":{"component":"Button","props":{"label":"Wipe"},"action":"wipe-database"}}`
	srv, _ := fakeAPI(t, tree, tree)
	c := NewAnthropicComposer(AnthropicConfig{APIKey: "k", Endpoint: srv.URL})

	// No allow-list on the context: the conservative answer is that no action
	// is permitted, so a generated control cannot invent one.
	if _, err := c.Compose(context.Background(), "delete everything", DefaultRegistry()); err == nil {
		t.Fatal("an action outside the allow-list must be refused")
	}

	srv2, _ := fakeAPI(t, tree)
	c2 := NewAnthropicComposer(AnthropicConfig{APIKey: "k", Endpoint: srv2.URL})
	ctx := WithActionsContext(context.Background(), []string{"wipe-database"})
	if _, err := c2.Compose(ctx, "delete everything", DefaultRegistry()); err != nil {
		t.Fatalf("an allow-listed action must pass: %v", err)
	}
}

func TestAnthropicComposerNeedsAKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	c := NewAnthropicComposer(AnthropicConfig{Endpoint: "http://127.0.0.1:1"})
	_, err := c.Compose(context.Background(), "x", DefaultRegistry())
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("want a clear missing-key error, got %v", err)
	}
}

func TestAnthropicComposerDoesNotLeakTheBodyOnAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// A real error body can echo request content back.
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"key sk-ant-SECRET is invalid"}}`))
	}))
	defer srv.Close()
	c := NewAnthropicComposer(AnthropicConfig{APIKey: "sk-ant-SECRET", Endpoint: srv.URL})

	_, err := c.Compose(context.Background(), "x", DefaultRegistry())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("the error carries response body content, which can echo a key: %v", err)
	}
	if !strings.Contains(err.Error(), "authentication_error") {
		t.Errorf("the error should still name the API's error type, got: %v", err)
	}
}

func TestSystemPromptStatesEveryRegistryComponent(t *testing.T) {
	// The prompt is guidance and the validator is the rule, but a prompt that
	// omits a component guarantees the model never uses it.
	p := systemPrompt(DefaultRegistry())
	for _, name := range DefaultRegistry().Names() {
		if !strings.Contains(p, name) {
			t.Errorf("system prompt never mentions %q", name)
		}
	}
	if !strings.Contains(p, "cannot write HTML") {
		t.Error("the prompt should state the boundary, not only the vocabulary")
	}
}
