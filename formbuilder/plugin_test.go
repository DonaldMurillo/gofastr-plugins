package formbuilder

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// --- test fixtures ----------------------------------------------------------

// sevenTypeDoc is a schema using every field type and most rules. It is the
// round-trip workhorse: what a save must accept, a reload must return, and
// the live form must render.
func sevenTypeDoc() Doc {
	return Doc{
		Version: SchemaVersion,
		Fields: []Field{
			{Type: "text", Name: "full_name", Label: "Full name", Required: true,
				Rules: Rules{MinLength: new(2.0), MaxLength: new(80.0), Pattern: `^[A-Z][a-z]+ [A-Z][a-z]+$`}},
			{Type: "email", Name: "email", Label: "Email", Required: true},
			{Type: "number", Name: "seats", Label: "Seats", Rules: Rules{Min: new(1.0), Max: new(20.0)}},
			{Type: "textarea", Name: "notes", Label: "Notes", Rules: Rules{MaxLength: new(200.0)}},
			{Type: "select", Name: "plan", Label: "Plan", Options: []string{"starter", "scale"}},
			{Type: "checkbox", Name: "terms", Label: "Accept terms", Required: true},
			{Type: "date", Name: "start", Label: "Start date"},
		},
	}
}

// validValues satisfies every rule in sevenTypeDoc.
func validValues() url.Values {
	return url.Values{
		"full_name": {"Ada Lovelace"},
		"email":     {"ada@example.com"},
		"seats":     {"5"},
		"notes":     {""},
		"plan":      {"starter"},
		"terms":     {"on"},
		"start":     {"2026-09-01"},
	}
}

func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "formbuilder-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// fullTestApp wires the demo pages so the design + live routes exist.
func fullTestApp(t *testing.T) (*framework.App, *Plugin) {
	t.Helper()
	return newTestApp(t, WithDevGrantAll(), WithDemoPage())
}

func postJSON(t *testing.T, srvURL, path string, body any) (*http.Response, string) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := http.Post(srvURL+path, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

func postForm(t *testing.T, srvURL, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := http.PostForm(srvURL+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

func getBody(t *testing.T, srvURL, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(srvURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// --- assets -----------------------------------------------------------------

func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{BuilderHTMLURL, "text/html; charset=utf-8"},
		{BuilderJSURL, "text/javascript; charset=utf-8"},
		{BuilderCSSURL, "text/css; charset=utf-8"},
		{AdapterScriptURL, "text/javascript; charset=utf-8"},
	}
	for _, c := range cases {
		resp, body := getBody(t, srv.URL, c.path)
		if resp.Header.Get("Content-Type") != c.wantCT {
			t.Errorf("%s: content-type=%q want %q", c.path, resp.Header.Get("Content-Type"), c.wantCT)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%s", c.path, resp.StatusCode, body)
		}
	}
}

// TestFramedAssetsCarryHeaderRelaxation proves the platform AssetServer carries
// the framing/CORP/CSP relaxation that lets the host frame its OWN builder
// document, AND that the fixed framedCSP carries connect-src 'none' + sandbox
// allow-scripts — the directives that force every save across the bridge
// instead of letting the frame persist anything itself.
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{BuilderHTMLURL, BuilderJSURL, BuilderCSSURL} {
		resp, _ := getBody(t, srv.URL, path)
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "connect-src 'none'") {
			t.Errorf("%s: framed CSP missing connect-src 'none': %q", path, csp)
		}
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: framed CSP missing sandbox allow-scripts: %q", path, csp)
		}
		if resp.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
			t.Errorf("%s: CORP=%q want cross-origin", path, resp.Header.Get("Cross-Origin-Resource-Policy"))
		}
	}
}

// --- demo pages + mount --------------------------------------------------------

func TestDemoPagesContainMountBrokerAndLiveLink(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	_, design := getBody(t, srv.URL, DemoURL)
	for _, want := range []string{
		`data-fui-plugin="formbuilder"`,
		`<script src="` + pluginhost.BrokerScriptURL,
		`<script src="` + AdapterScriptURL,
		`href="/formbuilder/live"`,
		"schema formbuilder-v1",
	} {
		if !strings.Contains(design, want) {
			t.Errorf("design page missing %q", want)
		}
	}

	resp, live := getBody(t, srv.URL, LiveURL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("live GET status=%d", resp.StatusCode)
	}
	for _, want := range []string{
		`id="fb-verdict" data-verdict="fresh"`,
		"<form",
		"ui-form",
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live page missing %q", want)
		}
	}
}

// TestMountPublishesFieldName pins the hidden-input contract: a mount with a
// custom Field name that never reaches the adapter silently loses the schema
// on submit.
func TestMountPublishesFieldName(t *testing.T) {
	custom := Mount(MountConfig{DocID: "orders", Field: "orders_schema", Doc: `{"version":"formbuilder-v1","fields":[]}`})
	html := string(custom)
	for _, want := range []string{
		`data-fui-plugin="formbuilder"`,
		`data-fui-plugin-docid="orders"`,
		`data-fui-plugin-field="orders_schema"`,
		`<input type="hidden" name="orders_schema">`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("mount missing %q:\n%s", want, html)
		}
	}
}

// --- /save --------------------------------------------------------------------

func TestSaveValidatesNormalisesAndRoundTrips(t *testing.T) {
	app, p := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	doc := sevenTypeDoc()
	resp, body := postJSON(t, srv.URL, SaveURL, map[string]any{
		"docId": "demo", "doc": doc, "schemaVersion": SchemaVersion,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d body=%s", resp.StatusCode, body)
	}
	var saved struct {
		Status string  `json:"status"`
		Fields int     `json:"fields"`
		Rules  float64 `json:"rules"`
	}
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatalf("decode save response: %v (%s)", err, body)
	}
	if saved.Fields != 7 {
		t.Errorf("fields=%d want 7", saved.Fields)
	}
	// required(full_name,email,terms)=3 + minlen/maxlen/pattern=3 + min/max=2
	// + notes maxlen=1 → 9 rules.
	if saved.Rules != 9 {
		t.Errorf("rules=%v want 9", saved.Rules)
	}

	got, ok := p.LoadDoc(nil, "demo")
	if !ok {
		t.Fatal("doc not persisted")
	}
	var back Doc
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("persisted doc does not parse: %v", err)
	}
	if len(back.Fields) != len(doc.Fields) {
		t.Fatalf("round trip lost fields: %d want %d", len(back.Fields), len(doc.Fields))
	}
	for i := range doc.Fields {
		if back.Fields[i].Name != doc.Fields[i].Name || back.Fields[i].Type != doc.Fields[i].Type {
			t.Errorf("field %d: got %+v want %+v", i, back.Fields[i], doc.Fields[i])
		}
	}

	// Normalisation: version stamped even when the frame omits it; an empty
	// label defaults to the name.
	resp2, _ := postJSON(t, srv.URL, SaveURL, map[string]any{
		"docId": "noversion",
		"doc":   Doc{Fields: []Field{{Type: "text", Name: "anon"}}},
	})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("versionless save failed")
	}
	nv, _ := p.LoadDoc(nil, "noversion")
	if !strings.Contains(nv, `"formbuilder-v1"`) {
		t.Errorf("version not stamped on save: %s", nv)
	}
	if !strings.Contains(nv, `"label":"anon"`) {
		t.Errorf("empty label not defaulted to the name: %s", nv)
	}
}

// TestSaveRefusesBadSchemas is the refusal table: every class of bad schema
// gets a SPECIFIC 400 code, and nothing is persisted — the doc the store held
// before the attempt is the doc it holds after.
func TestSaveRefusesBadSchemas(t *testing.T) {
	app, p := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// Seed a good doc; refusals must leave it untouched.
	if resp, body := postJSON(t, srv.URL, SaveURL, map[string]any{"docId": "demo", "doc": sevenTypeDoc()}); resp.StatusCode != 200 {
		t.Fatalf("seed save failed: %d %s", resp.StatusCode, body)
	}
	before, _ := p.LoadDoc(nil, "demo")

	f := func(mutate func(*Doc)) Doc {
		d := sevenTypeDoc()
		mutate(&d)
		return d
	}
	cases := []struct {
		name string
		doc  Doc
		code string
	}{
		{"unknown field type", f(func(d *Doc) { d.Fields[0].Type = "dropdown" }), ErrUnknownFieldType},
		{"duplicate name", f(func(d *Doc) { d.Fields[1].Name = "full_name" }), ErrDuplicateName},
		{"empty name", f(func(d *Doc) { d.Fields[0].Name = "" }), ErrEmptyName},
		{"invalid name (uppercase)", f(func(d *Doc) { d.Fields[0].Name = "FullName" }), ErrInvalidName},
		{"invalid name (leading digit)", f(func(d *Doc) { d.Fields[0].Name = "1name" }), ErrInvalidName},
		{"invalid name (hyphen)", f(func(d *Doc) { d.Fields[0].Name = "full-name" }), ErrInvalidName},
		{"uncompilable pattern", f(func(d *Doc) { d.Fields[0].Rules.Pattern = "([unclosed" }), ErrBadRule},
		{"inverted range", f(func(d *Doc) { d.Fields[2].Rules.Min, d.Fields[2].Rules.Max = new(20.0), new(1.0) }), ErrBadRule},
		{"inverted lengths", f(func(d *Doc) { d.Fields[0].Rules.MinLength, d.Fields[0].Rules.MaxLength = new(80.0), new(2.0) }), ErrBadRule},
		{"negative minLength", f(func(d *Doc) { d.Fields[0].Rules.MinLength = new(-1.0) }), ErrBadRule},
		{"fractional minLength", f(func(d *Doc) { d.Fields[0].Rules.MinLength = new(2.5) }), ErrBadRule},
		{"length rule on number", f(func(d *Doc) { d.Fields[2].Rules.MinLength = new(1.0) }), ErrBadRule},
		{"range rule on text", f(func(d *Doc) { d.Fields[0].Rules.Min = new(1.0) }), ErrBadRule},
		{"pattern on date", f(func(d *Doc) { d.Fields[6].Rules.Pattern = "^2" }), ErrBadRule},
		{"markup in label", f(func(d *Doc) { d.Fields[0].Label = "<b>bold</b>" }), ErrMarkup},
		{"markup in help", f(func(d *Doc) { d.Fields[0].Help = "use < 10 chars" }), ErrMarkup},
		{"markup in option", f(func(d *Doc) { d.Fields[4].Options = []string{"<img src=x>"} }), ErrMarkup},
		{"unknown version", Doc{Version: "formbuilder-v2", Fields: sevenTypeDoc().Fields[:1]}, ErrUnknownVersion},
		{"select without options", f(func(d *Doc) { d.Fields[4].Options = nil }), ErrBadSelect},
		{"select with empty option", f(func(d *Doc) { d.Fields[4].Options = []string{"starter", ""} }), ErrBadSelect},
		{"options on a text field", f(func(d *Doc) { d.Fields[0].Options = []string{"a"} }), ErrBadSelect},
		{"too many fields", Doc{Version: SchemaVersion, Fields: manyFields(maxFields + 1)}, ErrTooManyFields},
	}
	for _, c := range cases {
		resp, body := postJSON(t, srv.URL, SaveURL, map[string]any{"docId": "demo", "doc": c.doc})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status=%d want 400 (body %s)", c.name, resp.StatusCode, body)
			continue
		}
		if !strings.Contains(body, `"error":"`+c.code+`"`) {
			t.Errorf("%s: code mismatch: want %q in %s", c.name, c.code, body)
		}
		after, _ := p.LoadDoc(nil, "demo")
		if after != before {
			t.Errorf("%s: refusal was persisted — store mutated", c.name)
		}
	}
}

func manyFields(n int) []Field {
	out := make([]Field, 0, n)
	for i := range n {
		out = append(out, Field{Type: "text", Name: "f_" + string(rune('a'+i%26)) + "_" + itoa(i), Label: "F"})
	}
	return out
}

func itoa(i int) string {
	return string(rune('0'+i%10)) + string(rune('0'+(i/10)%10)) + string(rune('0'+(i/100)%10))
}

// TestSaveRejectsTrailingData pins the envelope contract: exactly one JSON
// value, trailing garbage refused, trailing whitespace accepted.
func TestSaveRejectsTrailingData(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Post(srv.URL+SaveURL, "application/json",
		strings.NewReader(`{"docId":"x","doc":null} {"docId":"y"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), ErrBadJSON) {
		t.Errorf("trailing object: status=%d body=%s", resp.StatusCode, raw)
	}

	resp2, err := http.Post(srv.URL+SaveURL, "application/json",
		strings.NewReader("{\"docId\":\"trailingws\",\"doc\":{\"version\":\"formbuilder-v1\",\"fields\":[]}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	raw2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("trailing newline should be accepted: status=%d body=%s", resp2.StatusCode, raw2)
	}
}

// TestSavedDocContainsNoMarkup is the data-only proof: after a full save, the
// persisted doc JSON contains no "<" anywhere and no markup-ish keys. A
// builder that ever emitted HTML — a preview string, a rich label, anything —
// fails here, and the plugin's whole claim goes with it.
func TestSavedDocContainsNoMarkup(t *testing.T) {
	app, p := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	postJSON(t, srv.URL, SaveURL, map[string]any{"docId": "demo", "doc": sevenTypeDoc()})
	got, ok := p.LoadDoc(nil, "demo")
	if !ok {
		t.Fatal("doc not persisted")
	}
	if strings.Contains(got, "<") {
		t.Errorf("persisted doc contains markup:\n%s", got)
	}
	for _, key := range []string{"html", "markup", "innerHTML"} {
		if strings.Contains(strings.ToLower(got), `"`+key+`"`) {
			t.Errorf("persisted doc carries a %q key:\n%s", key, got)
		}
	}
}

// --- the live form: the server-side enforcement proof ---------------------------

// TestLiveFormRendersAllSevenTypes — the live route must render every type
// the schema can declare, through the framework's own components.
func TestLiveFormRendersAllSevenTypes(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	postJSON(t, srv.URL, SaveURL, map[string]any{"docId": "demo", "doc": sevenTypeDoc()})
	_, body := getBody(t, srv.URL, LiveURL)
	for _, want := range []string{
		`type="text"`,
		`type="email"`,
		`type="number"`,
		`type="date"`,
		"<textarea",
		"<select",
		`type="checkbox"`,
		`name="full_name"`,
		`name="seats"`,
		`name="terms"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live form missing %q", want)
		}
	}
}

// TestLiveFormServerRejectsRuleViolations is THE test: a plain HTTP POST —
// the frame bypassed entirely — carrying values that violate rules the frame
// would have blocked. If the server accepted them, the plugin would prove
// nothing. Also proves the verdict page carries the refusal, and that a
// clean POST is accepted with its values echoed.
func TestLiveFormServerRejectsRuleViolations(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	postJSON(t, srv.URL, SaveURL, map[string]any{"docId": "demo", "doc": sevenTypeDoc()})

	bad := url.Values{
		"full_name": {""},                       // required, left empty
		"email":     {"not-an-email"},           // invalid email
		"seats":     {"99"},                     // max 20
		"notes":     {strings.Repeat("x", 201)}, // max length 200
		"plan":      {"enterprise"},             // not a listed option
		// terms: absent — required checkbox
		"start": {"31-12-2026"}, // wrong date shape
	}
	resp, body := postForm(t, srv.URL, LiveURL, bad)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid POST status=%d want 422", resp.StatusCode)
	}
	if !strings.Contains(body, `data-verdict="rejected"`) {
		t.Error("rejection page missing the rejected verdict banner")
	}
	for _, want := range []string{
		"This field is required.",
		"Enter a valid email address.",
		"Must be at most 20.",
		"Use at most 200 characters.",
		"Choose one of the listed options.",
		"This box must be checked.",
		"Enter a date as YYYY-MM-DD.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rejection page missing field error %q", want)
		}
	}
	if strings.Contains(body, `data-verdict="accepted"`) || strings.Contains(body, "accepted by Go") {
		t.Error("invalid POST produced the accepted view")
	}

	// The pattern rule, exercised on its own: everything else valid, the
	// name breaks the schema's regexp.
	patternBad := validValues()
	patternBad.Set("full_name", "ada lovelace")
	respP, bodyP := postForm(t, srv.URL, LiveURL, patternBad)
	if respP.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pattern-violating POST status=%d want 422", respP.StatusCode)
	}
	if !strings.Contains(bodyP, "does not match the required pattern") {
		t.Error("pattern violation not refused with the pattern error")
	}

	good := validValues()
	resp2, body2 := postForm(t, srv.URL, LiveURL, good)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("valid POST status=%d body=%s", resp2.StatusCode, body2)
	}
	if !strings.Contains(body2, `data-verdict="accepted"`) {
		t.Error("valid POST missing the accepted verdict banner")
	}
	if !strings.Contains(body2, "Ada Lovelace") || !strings.Contains(body2, "ada@example.com") {
		t.Error("accepted view missing the submitted values")
	}
}

// TestLiveFormEnforcesSelectMembershipOnOptionalField: membership is a schema
// constraint, not a UX hint — a crafted POST with a foreign value is refused
// even when the field is optional.
func TestLiveFormEnforcesSelectMembershipOnOptionalField(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	postJSON(t, srv.URL, SaveURL, map[string]any{"docId": "demo", "doc": sevenTypeDoc()})
	// plan is optional in sevenTypeDoc; a foreign value must still be refused.
	bad := validValues()
	bad.Set("plan", "not-a-plan")
	resp, body := postForm(t, srv.URL, LiveURL, bad)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("foreign option status=%d want 422", resp.StatusCode)
	}
	if !strings.Contains(body, "Choose one of the listed options.") {
		t.Error("foreign option not refused with the membership error")
	}
}

// TestLiveFormFallsBackToDemoDocThenSavedDoc: before anything is saved, the
// live route renders the demo canvas — and after a save, it renders THAT.
func TestLiveFormFallsBackToDemoDocThenSavedDoc(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	_, body := getBody(t, srv.URL, LiveURL)
	if !strings.Contains(body, `name="full_name"`) || !strings.Contains(body, `name="pitch"`) {
		t.Error("fresh live page does not render the demo canvas")
	}

	custom := Doc{Version: SchemaVersion, Fields: []Field{
		{Type: "text", Name: "only_field", Label: "Only field"},
	}}
	postJSON(t, srv.URL, SaveURL, map[string]any{"docId": "demo", "doc": custom})
	_, body2 := getBody(t, srv.URL, LiveURL)
	if !strings.Contains(body2, `name="only_field"`) || strings.Contains(body2, `name="pitch"`) {
		t.Error("live page did not switch to the saved schema")
	}
}

// --- capability gate -------------------------------------------------------------

// TestRouteDeniedWithoutCapability proves the gate is wired into the route: a
// token whose scopes lack the capability gets 403 + E_CAPABILITY_DENIED.
func TestRouteDeniedWithoutCapability(t *testing.T) {
	app, _ := newTestApp(t) // no WithDevGrantAll
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	req := httptest.NewRequest(http.MethodPost, SaveURL,
		strings.NewReader(`{"docId":"demo","doc":null}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req.WithContext(deniedCtx)) //nolint — direct handler use keeps ctx scopes
	resp := rec.Result()
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var body struct {
		Error      string `json:"error"`
		Capability string `json:"capability"`
	}
	_ = json.Unmarshal(raw, &body)
	if resp.StatusCode != http.StatusForbidden || body.Error != "E_CAPABILITY_DENIED" {
		t.Errorf("denied token POST save: status=%d error=%q capability=%q, want 403 E_CAPABILITY_DENIED",
			resp.StatusCode, body.Error, body.Capability)
	}
}

// TestManifestInvariants pins what internal/registry also enforces from
// plugins.json: opaque sandbox, no allow-same-origin, schema.
func TestManifestInvariants(t *testing.T) {
	_, p := fullTestApp(t)
	m := p.Manifest()
	if m.Isolation != pluginhost.IsolationSandboxOpaque {
		t.Errorf("isolation=%q", m.Isolation)
	}
	if len(m.Sandbox) != 1 || m.Sandbox[0] != "allow-scripts" {
		t.Errorf("sandbox=%v want exactly [allow-scripts]", m.Sandbox)
	}
	for _, tok := range m.Sandbox {
		if tok == "allow-same-origin" {
			t.Error("sandbox contains allow-same-origin")
		}
	}
	if m.Schema != SchemaVersion || m.Entry != BuilderHTMLURL {
		t.Errorf("manifest schema/entry drifted: %q %q", m.Schema, m.Entry)
	}
}
