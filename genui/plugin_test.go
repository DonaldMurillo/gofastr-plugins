package genui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "genui-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// fullTestApp wires the dev grant so the data routes answer without a
// scoped token. The demo page itself is genui/demo.go, owned by the
// coordinator and not part of this package yet.
func fullTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	return newTestApp(t, append([]Option{WithDevGrantAll()}, opts...)...)
}

// scriptComposer is a controllable Composer: it can block (for deterministic
// pending-state assertions), return a fixed composition, or fail.
type scriptComposer struct {
	block chan struct{}
	comp  Composition
	err   error
}

func (c *scriptComposer) Compose(_ context.Context, _ string, _ Registry) (Composition, error) {
	if c.block != nil {
		<-c.block
	}
	return c.comp, c.err
}

// postCompose posts a prompt through the app's router IN PROCESS (context
// values like scoped tokens do not survive a TCP hop) and decodes the JSON
// body. A prompt starting with "\x00" is posted raw — the bytes after the
// sentinel — so malformed-envelope cases reach the route verbatim.
func postCompose(t *testing.T, h http.Handler, ctx context.Context, prompt string) (int, map[string]any) {
	t.Helper()
	var body string
	if strings.HasPrefix(prompt, "\x00") {
		body = prompt[1:]
	} else {
		body = `{"prompt":` + mustJSON(prompt) + `}`
	}
	req := httptest.NewRequest(http.MethodPost, ComposeURL, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw := rec.Body.Bytes()
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return rec.Code, out
}

// getComposition GETs one composition record through the router in process.
func getComposition(t *testing.T, h http.Handler, ctx context.Context, id string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, CompositionBaseURL+"/"+id, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw := rec.Body.Bytes()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("GET /composition/%s: bad JSON %q", id, raw)
	}
	return rec.Code, out
}

// awaitState polls until the record leaves "pending" (or the deadline),
// then returns the final decoded record.
func awaitState(t *testing.T, h http.Handler, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, body := getComposition(t, h, context.Background(), id)
		if status != http.StatusOK {
			t.Fatalf("GET /composition/%s: status=%d body=%v", id, status, body)
		}
		if body["state"] != statePending {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("composition %s never left pending", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// composeNow starts a generation with the instant fixture composer and
// waits for its terminal state.
func composeNow(t *testing.T, h http.Handler, prompt string) (string, map[string]any) {
	t.Helper()
	status, body := postCompose(t, h, context.Background(), prompt)
	if status != http.StatusOK {
		t.Fatalf("POST /compose: status=%d body=%v", status, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("POST /compose: no id in %v", body)
	}
	return id, awaitState(t, h, id)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// canonicalJSON round-trips v so struct-field order and Go int literals
// (level: 2) compare equal to their decoded-JSON counterparts.
func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b2, err := json.Marshal(round)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return string(b2)
}

// briefExample is the contract's own example tree, exactly as written in
// the package doc: the base every rejection mutates.
// stackNode is a minimal valid Stack for chain/budget builders: the
// component's geometry props are required, so every synthetic Stack carries
// them.
func stackNode() *Node {
	return &Node{Component: "Stack", Props: map[string]any{"gap": "md", "direction": "column"}}
}

func briefExample() Composition {
	return Composition{
		SchemaVersion: SchemaVersion,
		Root: &Node{
			Component: "Stack",
			Props:     map[string]any{"gap": "lg", "direction": "column"},
			Children: []Node{
				{Component: "Heading", Props: map[string]any{"text": "Q3 revenue", "level": 2}},
				{Component: "Stat", Props: map[string]any{"label": "Revenue", "value": "$1.2M", "delta": 12.5}},
				{Component: "Button", Props: map[string]any{"label": "Export"}, Action: "export"},
			},
		},
	}
}

// --- the validator: one rejection per rule, path pinned ---------------------

// TestValidateRejections is the plugin's security story tested like one:
// every rule refuses with a stable code AND names the offending path, so a
// human debugging model output is told WHERE, not just that.
func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(c *Composition)
		wantCode string
		wantPath string
	}{
		{
			name:     "wrong schema version",
			mutate:   func(c *Composition) { c.SchemaVersion = "genui-v2" },
			wantCode: ErrBadVersion,
			wantPath: "schemaVersion",
		},
		{
			name:     "missing root",
			mutate:   func(c *Composition) { c.Root = nil },
			wantCode: ErrNoRoot,
			wantPath: "root",
		},
		{
			name:     "unknown component at root",
			mutate:   func(c *Composition) { c.Root.Component = "Marquee" },
			wantCode: ErrUnknownComponent,
			wantPath: "root",
		},
		{
			name:     "empty component name",
			mutate:   func(c *Composition) { c.Root.Children[1].Component = "" },
			wantCode: ErrUnknownComponent,
			wantPath: "root.children[1]",
		},
		{
			name: "unknown prop",
			mutate: func(c *Composition) {
				c.Root.Children[2].Props["tone"] = "good"
			},
			wantCode: ErrUnknownProp,
			wantPath: "root.children[2].props.tone",
		},
		{
			name: "wrong prop type string for int",
			mutate: func(c *Composition) {
				c.Root.Children[0].Props["level"] = "2"
			},
			wantCode: ErrPropType,
			wantPath: "root.children[0].props.level",
		},
		{
			name: "wrong prop type number for string",
			mutate: func(c *Composition) {
				c.Root.Children[0].Props["text"] = 7
			},
			wantCode: ErrPropType,
			wantPath: "root.children[0].props.text",
		},
		{
			name: "null prop value",
			mutate: func(c *Composition) {
				c.Root.Children[0].Props["text"] = nil
			},
			wantCode: ErrPropType,
			wantPath: "root.children[0].props.text",
		},
		{
			name: "non-integral int",
			mutate: func(c *Composition) {
				c.Root.Children[0].Props["level"] = 2.5
			},
			wantCode: ErrPropType,
			wantPath: "root.children[0].props.level",
		},
		{
			name: "int out of range",
			mutate: func(c *Composition) {
				c.Root.Children[0].Props["level"] = 9
			},
			wantCode: ErrPropValue,
			wantPath: "root.children[0].props.level",
		},
		{
			name: "enum value outside vocabulary",
			mutate: func(c *Composition) {
				c.Root.Props["gap"] = "huge"
			},
			wantCode: ErrPropValue,
			wantPath: "root.props.gap",
		},
		{
			name: "string over the rune cap",
			mutate: func(c *Composition) {
				c.Root.Children[0].Props["text"] = strings.Repeat("x", MaxStringRunes+1)
			},
			wantCode: ErrPropValue,
			wantPath: "root.children[0].props.text",
		},
		{
			name: "missing required prop",
			mutate: func(c *Composition) {
				delete(c.Root.Children[0].Props, "text")
			},
			wantCode: ErrRequiredProp,
			wantPath: "root.children[0].props.text",
		},
		{
			name: "missing required prop on empty props object",
			mutate: func(c *Composition) {
				c.Root.Children[0].Props = nil
			},
			wantCode: ErrRequiredProp,
			wantPath: "root.children[0].props.text",
		},
		{
			name: "children on a component that does not accept them",
			mutate: func(c *Composition) {
				c.Root.Children[2].Children = []Node{
					{Component: "Text", Props: map[string]any{"text": "inside a button"}},
				}
			},
			wantCode: ErrChildren,
			wantPath: "root.children[2].children",
		},
		{
			name: "action outside the allow-list",
			mutate: func(c *Composition) {
				c.Root.Children[2].Action = "delete-everything"
			},
			wantCode: ErrAction,
			wantPath: "root.children[2].action",
		},
		{
			name: "table cell of wrong type",
			mutate: func(c *Composition) {
				c.Root.Children[1].Component = "Table"
				c.Root.Children[1].Props = map[string]any{
					"columns": []any{"Plan", "Price"},
					"rows":    []any{[]any{"Team", 29}},
				}
			},
			wantCode: ErrPropType,
			wantPath: "root.children[1].props.rows[0][1]",
		},
		{
			name: "table row that is not an array",
			mutate: func(c *Composition) {
				c.Root.Children[1].Component = "Table"
				c.Root.Children[1].Props = map[string]any{
					"columns": []any{"Plan"},
					"rows":    []any{"Team"},
				}
			},
			wantCode: ErrPropType,
			wantPath: "root.children[1].props.rows[0]",
		},
		{
			name: "table column of wrong type",
			mutate: func(c *Composition) {
				c.Root.Children[1].Component = "Table"
				c.Root.Children[1].Props = map[string]any{
					"columns": []any{"Plan", 3},
					"rows":    []any{[]any{"Team"}},
				}
			},
			wantCode: ErrPropType,
			wantPath: "root.children[1].props.columns[1]",
		},
		{
			name: "action on a component that does not carry one",
			mutate: func(c *Composition) {
				c.Root.Children[0].Action = "export"
			},
			wantCode: ErrAction,
			wantPath: "root.children[0].action",
		},
		{
			name: "missing required numeric prop",
			mutate: func(c *Composition) {
				delete(c.Root.Children[0].Props, "level")
			},
			wantCode: ErrRequiredProp,
			wantPath: "root.children[0].props.level",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := briefExample()
			tc.mutate(&c)
			err := Validate(c, DefaultRegistry(), DefaultActions())
			if err == nil {
				t.Fatalf("mutated composition validated clean; want %s at %s", tc.wantCode, tc.wantPath)
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("error is %T, want *ValidationError", err)
			}
			if ve.Code != tc.wantCode {
				t.Errorf("code = %q, want %q (message: %s)", ve.Code, tc.wantCode, ve.Message)
			}
			if ve.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", ve.Path, tc.wantPath)
			}
			if !strings.Contains(ve.Error(), tc.wantPath) {
				t.Errorf("Error() %q does not name the offending path %q", ve.Error(), tc.wantPath)
			}
		})
	}
}

// TestValidateAcceptsBriefExample proves the contract's own example JSON
// decodes and validates clean — the wire shape and the validator agree.
func TestValidateAcceptsBriefExample(t *testing.T) {
	raw := `{
	  "schemaVersion": "genui-v1",
	  "root": {
	    "component": "Stack",
	    "props": { "gap": "lg", "direction": "column" },
	    "children": [
	      { "component": "Heading", "props": { "text": "Q3 revenue", "level": 2 } },
	      { "component": "Stat", "props": { "label": "Revenue", "value": "$1.2M", "delta": 12.5 } },
	      { "component": "Button", "props": { "label": "Export" }, "action": "export" }
	    ]
	  }
	}`
	var c Composition
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := Validate(c, DefaultRegistry(), DefaultActions()); err != nil {
		t.Fatalf("brief example must validate: %v", err)
	}
}

// TestValidateDepthBound pins the exact boundary: a chain 16 deep is a
// composition, 17 deep is a refusal naming the too-deep node.
func TestValidateDepthBound(t *testing.T) {
	build := func(depth int) Composition {
		c := Composition{SchemaVersion: SchemaVersion, Root: stackNode()}
		cur := c.Root
		for i := 1; i < depth; i++ {
			cur.Children = []Node{*stackNode()}
			cur = &cur.Children[0]
		}
		return c
	}
	if err := Validate(build(MaxDepth), DefaultRegistry(), DefaultActions()); err != nil {
		t.Fatalf("chain of %d must validate: %v", MaxDepth, err)
	}
	err := Validate(build(MaxDepth+1), DefaultRegistry(), DefaultActions())
	if err == nil {
		t.Fatalf("chain of %d must be refused", MaxDepth+1)
	}
	ve := err.(*ValidationError)
	if ve.Code != ErrDepth {
		t.Errorf("code = %q, want %s", ve.Code, ErrDepth)
	}
	wantPath := "root" + strings.Repeat(".children[0]", MaxDepth)
	if ve.Path != wantPath {
		t.Errorf("path = %q, want %q", ve.Path, wantPath)
	}
}

// TestValidateNodeCountBound pins the other budget: exactly 200 nodes is a
// composition, the 201st node is a refusal naming node #201.
func TestValidateNodeCountBound(t *testing.T) {
	build := func(children int) Composition {
		c := Composition{SchemaVersion: SchemaVersion, Root: stackNode()}
		for range children {
			c.Root.Children = append(c.Root.Children, Node{
				Component: "Text", Props: map[string]any{"text": "n"},
			})
		}
		return c
	}
	if err := Validate(build(MaxNodes-1), DefaultRegistry(), DefaultActions()); err != nil {
		t.Fatalf("%d nodes must validate: %v", MaxNodes, err)
	}
	err := Validate(build(MaxNodes), DefaultRegistry(), DefaultActions())
	if err == nil {
		t.Fatalf("%d nodes must be refused", MaxNodes+1)
	}
	ve := err.(*ValidationError)
	if ve.Code != ErrNodeCount {
		t.Errorf("code = %q, want %s", ve.Code, ErrNodeCount)
	}
	wantPath := fmt.Sprintf("root.children[%d]", MaxNodes-1)
	if ve.Path != wantPath {
		t.Errorf("path = %q, want %q", ve.Path, wantPath)
	}
}

// TestValidateUnknownPropPathOnDeepNode proves the path is the full descent,
// not just the prop name — a model's output is debugged by a human reading it.
func TestValidateUnknownPropPathOnDeepNode(t *testing.T) {
	c := briefExample()
	deep := c.Root
	for range 3 {
		deep.Children = []Node{*stackNode()}
		deep = &deep.Children[0]
	}
	deep.Props = map[string]any{"gap": "md", "dangerouslySetInnerHTML": "<b>hi</b>"}
	err := Validate(c, DefaultRegistry(), DefaultActions())
	if err == nil {
		t.Fatal("unknown prop must be refused")
	}
	ve := err.(*ValidationError)
	want := "root.children[0].children[0].children[0].props.dangerouslySetInnerHTML"
	if ve.Path != want {
		t.Errorf("path = %q, want %q", ve.Path, want)
	}
	if ve.Code != ErrUnknownProp {
		t.Errorf("code = %q, want %s", ve.Code, ErrUnknownProp)
	}
}

// --- the registry ------------------------------------------------------------

// TestRegistryVocabulary pins the fixed component set and the children
// flags — the containment story's table of contents.
func TestRegistryVocabulary(t *testing.T) {
	reg := DefaultRegistry()
	want := []string{"Stack", "Card", "Heading", "Text", "Stat", "Badge", "Table", "Button"}
	if got := reg.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	accepting := map[string]bool{"Stack": true, "Card": true}
	for _, name := range want {
		spec, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) miss", name)
		}
		if spec.AcceptsChildren != accepting[name] {
			t.Errorf("%s.AcceptsChildren = %v, want %v", name, spec.AcceptsChildren, accepting[name])
		}
	}
	if _, ok := reg.Lookup("Marquee"); ok {
		t.Error("Lookup(Marquee) must miss")
	}
	// The props every button-rendering path depends on:
	button, _ := reg.Lookup("Button")
	if len(button.Props) != 2 || button.Props[0].Name != "label" || !button.Props[0].Required {
		t.Errorf("Button props = %+v, want a required string prop label (plus optional variant)", button.Props)
	}
	if !button.CarriesAction {
		t.Error("Button must be the one entry that carries an action")
	}
}

// TestRegistryMatchesAdapterCopy asserts the Go registry and the host
// adapter's KNOWN_COMPONENTS table name the SAME components. They are two
// copies of one fact and will drift otherwise.
func TestRegistryMatchesAdapterCopy(t *testing.T) {
	re := regexp.MustCompile(`KNOWN_COMPONENTS = \[([^\]]*)\]`)
	m := re.FindSubmatch(adapterJSBytes)
	if m == nil {
		t.Fatal("adapter.js lost its KNOWN_COMPONENTS table")
	}
	nameRe := regexp.MustCompile(`"([A-Za-z]+)"`)
	got := nameRe.FindAllStringSubmatch(string(m[1]), -1)
	names := make([]string, 0, len(got))
	for _, g := range got {
		names = append(names, g[1])
	}
	want := DefaultRegistry().Names()
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("adapter components = %v, want the Go registry's %v", names, want)
	}
}

// TestFrameRegistryNamesMatch asserts the frame sources name the same eight
// components. The frame (genui/js, owned by the frame worker) carries its
// own registry copy; every name must appear as a quoted literal in the
// frame sources or the built bundle. Skips loudly while neither has landed.
func TestFrameRegistryNamesMatch(t *testing.T) {
	var sb strings.Builder
	seen := false
	collect := func(path string, b []byte) {
		if strings.Contains(string(b), "@gofastr-placeholder") {
			return // the embed stub, not the frame
		}
		sb.Write(b)
		seen = true
	}
	for _, dir := range []string{"js", "assets"} {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch filepath.Ext(path) {
			case ".js", ".ts", ".jsx", ".tsx", ".mjs":
				if b, rerr := os.ReadFile(path); rerr == nil {
					collect(path, b)
				}
			}
			return nil
		})
	}
	if !seen {
		t.Skip("frame sources not landed yet (genui/js is the frame worker's); name-drift assertion activates when they do")
	}
	src := sb.String()
	for _, name := range DefaultRegistry().Names() {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		if !re.MatchString(src) {
			t.Errorf("frame sources never name component %q — registry drift", name)
		}
	}
}

// --- the fixture composer ------------------------------------------------------

// TestFixturesAllValidate: every fixture the FixtureComposer can emit —
// including the fallback — must pass Validate against the default registry
// and allow-list. The table and the registry cannot be allowed to drift.
func TestFixturesAllValidate(t *testing.T) {
	prompts := []string{
		"show me Q3 revenue",                // dashboard
		"compare the plans",                 // table
		"what is the system status",         // status card
		"translationese quantum frobnicate", // fallback
	}
	fc := FixtureComposer{}
	seenRoots := map[string]bool{}
	for _, p := range prompts {
		comp, err := fc.Compose(context.Background(), p, DefaultRegistry())
		if err != nil {
			t.Fatalf("Compose(%q): %v", p, err)
		}
		if err := Validate(comp, DefaultRegistry(), DefaultActions()); err != nil {
			t.Fatalf("fixture for %q does not validate: %v", p, err)
		}
		seenRoots[comp.Root.Component] = true
	}
	// The fixtures exercise both accepting containers.
	for _, want := range []string{"Stack", "Card"} {
		if !seenRoots[want] {
			t.Errorf("no fixture rooted in %s; the coverage claim is false", want)
		}
	}
}

// TestFixtureComposerDeterministic: same prompt, same tree, every time —
// no network, no key, no randomness.
func TestFixtureComposerDeterministic(t *testing.T) {
	fc := FixtureComposer{}
	a, _ := fc.Compose(context.Background(), "Q3 revenue please", DefaultRegistry())
	b, _ := fc.Compose(context.Background(), "Q3 revenue please", DefaultRegistry())
	if string(mustJSON(a)) != string(mustJSON(b)) {
		t.Error("same prompt yielded different compositions")
	}
}

// TestFixtureComposerFallbackIsACard pins the fallback contract: anything
// unrecognized gets an "I did not understand that" card, never an error.
func TestFixtureComposerFallbackIsACard(t *testing.T) {
	fc := FixtureComposer{}
	comp, err := fc.Compose(context.Background(), "zzz-unmatched-zzz", DefaultRegistry())
	if err != nil {
		t.Fatalf("fallback must not error: %v", err)
	}
	if comp.Root.Component != "Card" {
		t.Fatalf("fallback root = %q, want Card", comp.Root.Component)
	}
	text := comp.Root.Children[0].Props["text"]
	if !strings.HasPrefix(text.(string), "I did not understand") {
		t.Errorf("fallback text = %v", text)
	}
}

// --- the routes ----------------------------------------------------------------

// TestCapabilityGate proves both gate sides at the unit level: an enforcing
// plugin denies a token whose scopes lack the capability, a scoped token
// carrying it passes, the wildcard grammar implies it, and WithDevGrantAll
// short-circuits the gate.
func TestCapabilityGate(t *testing.T) {
	enforcing := New()
	granted := New(WithDevGrantAll())

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	allowedCtx := auth.WithTokenScopes(context.Background(), []string{CapCompose})

	deniedReq := httptest.NewRequest(http.MethodPost, ComposeURL, nil).WithContext(deniedCtx)
	if enforcing.allow(deniedReq, CapCompose) {
		t.Error("enforcing plugin should DENY a non-granting token")
	}
	allowedReq := httptest.NewRequest(http.MethodPost, ComposeURL, nil).WithContext(allowedCtx)
	if !enforcing.allow(allowedReq, CapCompose) {
		t.Error("enforcing plugin should ALLOW a genui:compose token")
	}
	wildReq := httptest.NewRequest(http.MethodPost, ComposeURL, nil).
		WithContext(auth.WithTokenScopes(context.Background(), []string{"genui:*"}))
	if !enforcing.allow(wildReq, CapCompose) {
		t.Error("genui:* wildcard token should imply genui:compose under the scope grammar")
	}
	if !granted.allow(deniedReq, CapCompose) {
		t.Error("WithDevGrantAll should ALLOW regardless of token scopes")
	}
}

// TestComposeRouteGated proves the denial at the route level, both routes,
// with the canonical 403 envelope.
func TestComposeRouteGated(t *testing.T) {
	app, _ := newTestApp(t) // enforcing
	h := app.Router()

	ctx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	status, body := postCompose(t, h, ctx, "hello")
	if status != http.StatusForbidden {
		t.Fatalf("POST /compose status = %d body=%v, want 403", status, body)
	}
	if body["error"] != "E_CAPABILITY_DENIED" {
		t.Errorf("POST denial code = %v, want E_CAPABILITY_DENIED", body["error"])
	}
	status, body = getComposition(t, h, ctx, "whatever")
	if status != http.StatusForbidden || body["error"] != "E_CAPABILITY_DENIED" {
		t.Fatalf("GET /composition denial: status=%d body=%v, want 403 E_CAPABILITY_DENIED", status, body)
	}
	// pluginhost.Allow is a capability gate, NOT authentication: an
	// anonymous caller (no token in context) passes it — which is exactly
	// why the package doc tells real hosts to check the session themselves.
	status, _ = postCompose(t, h, context.Background(), "hello")
	if status != http.StatusOK {
		t.Errorf("anonymous POST status = %d, want 200 (Allow is not authn; documented)", status)
	}
}

// TestComposeLifecycle walks the async contract deterministically: id
// returns immediately, the record is pending while the composer holds, and
// the stored tree is the validated one the composer produced.
func TestComposeLifecycle(t *testing.T) {
	script := &scriptComposer{
		block: make(chan struct{}),
		comp:  briefExample(),
	}
	app, _ := fullTestApp(t, WithComposer(script))
	h := app.Router()
	status, body := postCompose(t, h, context.Background(), "anything")
	if status != http.StatusOK {
		t.Fatalf("POST /compose: status=%d body=%v", status, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("POST /compose: no id in %v", body)
	}
	if len(id) != 16 {
		t.Errorf("id = %q, want 16 hex chars", id)
	}

	// Pending while the composer holds the generation.
	status, body = getComposition(t, h, context.Background(), id)
	if status != http.StatusOK || body["state"] != statePending {
		t.Fatalf("before release: status=%d body=%v, want pending", status, body)
	}

	close(script.block)
	final := awaitState(t, h, id)
	if final["state"] != stateReady {
		t.Fatalf("after release: state=%v, want ready", final["state"])
	}
	want := canonicalJSON(t, script.comp)
	if got := canonicalJSON(t, final["tree"]); got != want {
		t.Errorf("served tree = %s, want the validated composition %s", got, want)
	}
	// Terminal states are sticky.
	_, again := getComposition(t, h, context.Background(), id)
	if again["state"] != stateReady {
		t.Errorf("re-read state = %v, want ready", again["state"])
	}
}

// TestComposeFailedState: a composer error surfaces as state=failed with
// the error string; a composition that fails VALIDATION fails with the
// path-bearing refusal — the validator's message is the product here.
func TestComposeFailedState(t *testing.T) {
	t.Run("composer error", func(t *testing.T) {
		script := &scriptComposer{err: fmt.Errorf("model unavailable")}
		app, _ := fullTestApp(t, WithComposer(script))
		h := app.Router()

		id, final := composeNow(t, h, "anything")
		if final["state"] != stateFailed {
			t.Fatalf("id=%s state=%v, want failed", id, final["state"])
		}
		if final["error"] != "model unavailable" {
			t.Errorf("error = %v", final["error"])
		}
	})
	t.Run("invalid composition names the path", func(t *testing.T) {
		bad := briefExample()
		bad.Root.Children[2].Action = "not-in-allowlist"
		app, _ := fullTestApp(t, WithComposer(&scriptComposer{comp: bad}))
		h := app.Router()

		id, final := composeNow(t, h, "anything")
		if final["state"] != stateFailed {
			t.Fatalf("id=%s state=%v, want failed", id, final["state"])
		}
		msg, _ := final["error"].(string)
		if !strings.Contains(msg, "root.children[2].action") {
			t.Errorf("refusal %q does not name the offending path", msg)
		}
	})
}

// TestCompositionNotFound: an unknown id is a 404 with a stable code.
func TestCompositionNotFound(t *testing.T) {
	app, _ := fullTestApp(t)
	h := app.Router()

	status, body := getComposition(t, h, context.Background(), "deadbeefdeadbeef")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["error"] != "E_NOT_FOUND" {
		t.Errorf("error = %v, want E_NOT_FOUND", body["error"])
	}
}

// TestComposeRequestValidation pins the /compose envelope contract.
func TestComposeRequestValidation(t *testing.T) {
	app, _ := fullTestApp(t)
	h := app.Router()

	ctx := context.Background()

	cases := []struct {
		name     string
		prompt   string // "\x00raw" escapes into a raw body
		wantCode string
	}{
		{"empty prompt", "", "E_BAD_PROMPT"},
		{"whitespace prompt", "   ", "E_BAD_PROMPT"},
		{"oversized prompt", strings.Repeat("a", maxPromptRunes+1), "E_BAD_PROMPT"},
		{"malformed JSON", "\x00raw" + `{prompt: no quotes}`, "E_BAD_JSON"},
		{"trailing data", "\x00raw" + `{"prompt":"hi"} {"prompt":"again"}`, "E_BAD_JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := postCompose(t, h, ctx, tc.prompt)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d body=%v, want 400", status, body)
			}
			if body["error"] != tc.wantCode {
				t.Errorf("error = %v, want %s", body["error"], tc.wantCode)
			}
		})
	}
}

// TestCompositionStoreIsBounded proves the no-database promise: the store
// caps at 64 records, oldest evicted, and never grows past the cap.
func TestCompositionStoreIsBounded(t *testing.T) {
	app, p := fullTestApp(t) // fixture composer: instant
	h := app.Router()

	var first, last string
	for i := range compositionCap + 6 {
		id, final := composeNow(t, h, fmt.Sprintf("prompt %d", i))
		if final["state"] != stateReady {
			t.Fatalf("prompt %d: state=%v", i, final["state"])
		}
		if i == 0 {
			first = id
		}
		last = id
	}
	if got := p.store.len(); got != compositionCap {
		t.Errorf("store holds %d records, want the cap of %d", got, compositionCap)
	}
	if status, _ := getComposition(t, h, context.Background(), first); status != http.StatusNotFound {
		t.Errorf("oldest composition still served (status=%d); eviction is broken", status)
	}
	if status, _ := getComposition(t, h, context.Background(), last); status != http.StatusOK {
		t.Errorf("newest composition gone (status=%d)", status)
	}
}

// --- assets and config ----------------------------------------------------------

// TestInitServesAssetsWithCorrectContentTypes pins the host-page scripts.
// The framed bundle (genui.html/genui.js/genui.css) is the frame worker's
// build output; its content types are pinned by the platform AssetServer
// tests and asserted here for genui.js once the real bundle lands.
func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{GenuiJSURL, "text/javascript; charset=utf-8"},
		{AdapterScriptURL, "text/javascript; charset=utf-8"},
		{ConfigScriptURL, "text/javascript; charset=utf-8"},
	}
	for _, c := range cases {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Content-Type") != c.wantCT {
			t.Errorf("%s: content-type=%q want %q", c.path, resp.Header.Get("Content-Type"), c.wantCT)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%s", c.path, resp.StatusCode, body)
		}
	}
}

// TestFramedAssetsCarryHeaderRelaxation proves the platform AssetServer
// carries the framing/CORP/CSP relaxation and the fixed framedCSP the cage
// depends on (connect-src 'none' above all: the frame that renders composed
// trees has no network, which is what keeps the model host-side).
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	origin := strings.TrimSuffix(srv.URL, "/")
	resp, err := http.Get(srv.URL + GenuiJSURL)
	if err != nil {
		t.Fatalf("GET %s: %v", GenuiJSURL, err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: status=%d", GenuiJSURL, resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'none'") {
		t.Errorf("framed CSP missing connect-src 'none': %q", csp)
	}
	if !strings.Contains(csp, "sandbox allow-scripts") {
		t.Errorf("framed CSP missing sandbox allow-scripts: %q", csp)
	}
	if !strings.Contains(csp, "script-src "+origin) {
		t.Errorf("framed CSP script-src must name the explicit origin %q: %q", origin, csp)
	}
	if strings.Contains(csp, "'self'") {
		t.Errorf("framed CSP must never carry 'self' (opaque-origin frames resolve it to null): %q", csp)
	}
	if resp.Header.Get("Cross-Origin-Resource-Policy") == "" {
		t.Errorf("missing CORP relaxation")
	}
}

// parseConfigScript extracts the JSON object config.js publishes
// (window.__gofastrGenuiConfig = {...};).
func parseConfigScript(t *testing.T, body string) map[string]any {
	t.Helper()
	const prefix = "window.__gofastrGenuiConfig = "
	if !strings.HasPrefix(body, prefix) {
		t.Fatalf("config.js does not start with %q: %q", prefix, body)
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(body, prefix), ";\n")
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		t.Fatalf("config.js JSON: %v", err)
	}
	return out
}

// TestConfigScriptPublishesActions: the allow-list the Go validator
// enforces is the same one published to the adapter and the frame.
func TestConfigScriptPublishesActions(t *testing.T) {
	app, _ := fullTestApp(t, WithActions("export", "refresh", "open"))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + ConfigScriptURL)
	if err != nil {
		t.Fatalf("GET config.js: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	cfg := parseConfigScript(t, string(body))
	actions, _ := cfg["actions"].([]any)
	got := make([]string, 0, len(actions))
	for _, a := range actions {
		got = append(got, a.(string))
	}
	want := []string{"export", "refresh", "open"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config actions = %v, want %v", got, want)
	}
	// The registry ids ride along so the frame can check host agreement
	// (init.config.registry → __genuiDebug.registryAgrees).
	regIds, _ := cfg["registry"].([]any)
	gotIds := make([]string, 0, len(regIds))
	for _, r := range regIds {
		gotIds = append(gotIds, r.(string))
	}
	if !reflect.DeepEqual(gotIds, DefaultRegistry().Names()) {
		t.Fatalf("config registry = %v, want the Go registry's %v", gotIds, DefaultRegistry().Names())
	}

	// The default instance publishes the default vocabulary, complete.
	app2, _ := fullTestApp(t)
	srv2 := httptest.NewServer(app2.Router())
	defer srv2.Close()
	resp2, _ := http.Get(srv2.URL + ConfigScriptURL)
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	cfg2 := parseConfigScript(t, string(body2))
	actions2, _ := cfg2["actions"].([]any)
	if len(actions2) != 1 || actions2[0] != "export" {
		t.Errorf("default config actions = %v, want [export]", actions2)
	}
}

// --- construction and manifest ---------------------------------------------------

// Fail loud at construction: an empty or duplicated allow-list entry
// silently never matches, which is the worst way to learn about it.
func TestNewPanicsOnBadConfig(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		{"empty action", []Option{WithActions("export", " ")}, "empty action"},
		{"duplicate action", []Option{WithActions("export", "export")}, "duplicate action"},
	}
	for _, c := range cases {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s: New should panic", c.name)
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, c.want) {
					t.Errorf("%s: panic message = %q, want it to mention %q", c.name, msg, c.want)
				}
			}()
			New(c.opts...)
		}()
	}
}

// TestManifestInvariants pins the platform contract the registry tests also
// enforce from plugins.json: opaque sandbox, no allow-same-origin, schema,
// and the plugin's own shape (460px viewport, titled frame).
func TestManifestInvariants(t *testing.T) {
	_, p := fullTestApp(t)
	m := p.Manifest()
	if m.Isolation != pluginhost.IsolationSandboxOpaque {
		t.Fatalf("isolation=%q", m.Isolation)
	}
	if got := m.SandboxString(); got != "allow-scripts" {
		t.Fatalf("sandbox=%q, want allow-scripts only", got)
	}
	if m.Schema != SchemaVersion {
		t.Fatalf("schema=%q", m.Schema)
	}
	if m.Entry != FrameHTMLURL {
		t.Fatalf("entry=%q", m.Entry)
	}
	if m.MinHeight != "360px" {
		t.Fatalf("minHeight=%q, want 360px (the frame announces 360)", m.MinHeight)
	}
	if m.Title != "Generative UI" {
		t.Fatalf("title=%q", m.Title)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := strings.Join(p.Capabilities(), ",")
	if got != "genui:compose,theme:read" {
		t.Fatalf("capabilities=%q, want genui:compose,theme:read", got)
	}
}

// TestDemoMountSeamInvoked pins the Init seam the coordinator-owned

// --- the adapter surface -----------------------------------------------------------

// The adapter is served verbatim from the embedded host/ directory; pin the
// surface the demo page and e2e suite drive, so a rename here fails Go-side
// rather than as a mystery undefined in the browser.
func TestAdapterExposesDemoSurface(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + AdapterScriptURL)
	if err != nil {
		t.Fatalf("GET adapter.js: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	js := string(raw)
	for _, want := range []string{
		"window.__gofastrGenuiDemo",
		"compose",
		"lastComposition",
		"state",
		"__genuiReady",
		"__genuiState",
		"__genuiLastId",
		"__genuiRenderResult",
		"__genuiLastAction",
		"__genuiProbes",
		"composition",
		"composePending",
		"composeFailed",
		"renderResult",
		"uiAction",
		"nodeCount",
		"__genuiRegistryAgrees",
		"__gofastrGenuiConfig",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("adapter.js missing %q", want)
		}
	}
	// The supersede guard is the pipeline's load-bearing invariant; pin it
	// by name so it cannot be silently widened away.
	if !strings.Contains(js, "myGen !== gen") {
		t.Error("adapter.js lost its superseded-generation guard")
	}
	// The registered entry must be the route the Go side actually serves.
	if !strings.Contains(js, `"`+FrameHTMLURL+`"`) {
		t.Errorf("adapter.js does not reference the served entry %q", FrameHTMLURL)
	}
	if !strings.Contains(js, `"`+SchemaVersion+`"`) {
		t.Errorf("adapter.js does not reference the schema version %q", SchemaVersion)
	}
	// The frame is untrusted: its strings must be narrowed before mirroring.
	if !strings.Contains(js, "TEXT_CAP") {
		t.Error("adapter.js lost its frame-string narrowing cap")
	}
}
