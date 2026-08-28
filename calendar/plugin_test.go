package calendar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// --- test fixtures ----------------------------------------------------------

// dstEvents is the demo seed's shape at a smaller scale: every hard case in
// one week (spring-forward Sunday), a weekly series straddling the
// transition, a conflict pair, an all-day event and a midnight spanner.
func dstEvents() []Event {
	return []Event{
		nyEvent("gapend", "2026-03-08T01:30", "2026-03-08T02:00", nil),
		nyEvent("standup", "2026-03-02T09:00", "2026-03-02T09:30",
			&RRule{Freq: "WEEKLY", ByDay: []string{"MO", "TU", "WE", "TH", "FR"}, Until: "2026-03-13"}),
		nyEvent("board", "2026-03-11T13:00", "2026-03-11T15:00", nil),
		nyEvent("one2one", "2026-03-11T14:30", "2026-03-11T15:30", nil),
		{ID: "offsite", Title: "Offsite", Start: "2026-03-12", End: "2026-03-14", AllDay: true, Zone: "America/New_York"},
		nyEvent("deploy", "2026-03-07T23:30", "2026-03-08T00:30", nil),
	}
}

func staticEvents(evts []Event) EventsSource {
	return func(context.Context) ([]Event, error) { return evts, nil }
}

func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	all := append([]Option{WithEventsSource(staticEvents(dstEvents()))}, opts...)
	p := New(all...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "calendar-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// fullTestApp wires the demo page so the routes + demo exist.
func fullTestApp(t *testing.T) (*framework.App, *Plugin) {
	t.Helper()
	return newTestApp(t, WithDevGrantAll(), WithDemoPage())
}

func postJSON(t *testing.T, url string, body any) (*http.Response, string) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// postRaw posts a literal body string — for envelope-strictness tests that
// need bytes a struct marshal can never produce.
func postRaw(t *testing.T, url string, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// --- construction -------------------------------------------------------------

func TestNewPanicsWithoutEventsSource(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("New without WithEventsSource did not panic — a calendar with no server-side events has nothing for Go to be right about")
		}
	}()
	New()
}

func TestManifestIsolationInvariants(t *testing.T) {
	_, p := newTestApp(t)
	m := p.Manifest()
	if m.Entry != AppHTMLURL || m.Isolation != pluginhost.IsolationSandboxOpaque {
		t.Errorf("manifest entry/isolation = %s/%s", m.Entry, m.Isolation)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("manifest.Validate: %v", err)
	}
	for _, tok := range m.Sandbox {
		if tok == "allow-same-origin" {
			t.Errorf("sandbox carries allow-same-origin — the opaque-origin guarantee is gone")
		}
	}
}

// --- assets ---------------------------------------------------------------------

func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{AppHTMLURL, "text/html; charset=utf-8"},
		{AppJSURL, "text/javascript; charset=utf-8"},
		{AppCSSURL, "text/css; charset=utf-8"},
		{AdapterScriptURL, "text/javascript; charset=utf-8"},
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
// carries the framing/CORP/CSP relaxation AND the fixed framedCSP's
// connect-src 'none' + sandbox allow-scripts — the directives that force
// every occurrence across the postMessage bridge.
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{AppHTMLURL, AppJSURL, AppCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d", path, resp.StatusCode)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "connect-src 'none'") {
			t.Errorf("%s: framed CSP missing connect-src 'none': %q", path, csp)
		}
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: framed CSP missing sandbox allow-scripts: %q", path, csp)
		}
		if resp.Header.Get("Cross-Origin-Resource-Policy") == "" {
			t.Errorf("%s: missing CORP relaxation", path)
		}
	}
}

func TestDemoPageContainsMountAndBroker(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	for _, want := range []string{
		`data-fui-plugin="calendar"`,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
		// The demo-page beats (docs/demo-page-design.md).
		`data-jump="2026-03-08"`,
		`data-jump="2026-11-01"`,
		`id="cal-req"`,
		`id="cal-wall"`,
		`id="cal-elapsed"`,
		`sandbox="allow-scripts"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

func TestMountPublishesFieldName(t *testing.T) {
	custom := Mount(MountConfig{DocID: "team", Field: "team_view", Doc: `{"view":{"date":"2026-03-08","mode":"week"}}`})
	html := string(custom)
	for _, want := range []string{
		`data-fui-plugin-field="team_view"`,
		`name="team_view"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("Mount(custom field) missing %q in:\n%s", want, html)
		}
	}
	def := Mount(MountConfig{DocID: "team"})
	if !strings.Contains(string(def), `data-fui-plugin-field="calendar_doc"`) {
		t.Errorf("Mount(default) missing data-fui-plugin-field=\"calendar_doc\":\n%s", def)
	}
}

// --- /occurrences ----------------------------------------------------------------

func TestOccurrencesRoundTrip(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+OccurrencesURL, map[string]any{
		"docId": "demo", "from": "2026-03-02", "to": "2026-03-15",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("occurrences status=%d body=%s", resp.StatusCode, raw)
	}
	var win occurrenceWindow
	if err := json.Unmarshal([]byte(raw), &win); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The gap event resolves to the derived wall time, instants explicit.
	var gap *Occurrence
	for i := range win.Occurrences {
		if win.Occurrences[i].ID == "gapend/2026-03-08" {
			gap = &win.Occurrences[i]
		}
	}
	if gap == nil {
		t.Fatalf("gapend missing from window (%d occurrences)", len(win.Occurrences))
	}
	if gap.EndWall != "2026-03-08T03:00" || gap.StartUTC != "2026-03-08T06:30:00Z" {
		t.Errorf("gapend = %s → %s (%s), want server-resolved gap semantics", gap.StartWall, gap.EndWall, gap.StartUTC)
	}
	if !strings.Contains(gap.DSTNote, "does not exist") {
		t.Errorf("gapend note = %q", gap.DSTNote)
	}
	if len(win.Transitions) != 1 || win.Transitions[0].Date != "2026-03-08" {
		t.Errorf("transitions = %+v, want the Mar 8 boundary for the frame's marker", win.Transitions)
	}
	// The frame never receives an RRULE: the wire payload must not contain
	// the rule that generated the standup series.
	if strings.Contains(raw, `"rrule"`) || strings.Contains(raw, `"freq"`) {
		t.Errorf("occurrences payload leaks recurrence rules to the frame: %s", raw)
	}
}

func TestOccurrencesRangeCaps(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct {
		name       string
		from, to   string
		wantStatus int
		wantCode   string
	}{
		{"to before from", "2026-03-10", "2026-03-01", http.StatusBadRequest, "E_BAD_RANGE"},
		{"bad date", "03/01/2026", "2026-03-31", http.StatusBadRequest, "E_BAD_RANGE"},
		{"window too large", "2025-01-01", "2026-12-31", http.StatusBadRequest, "E_RANGE_TOO_LARGE"},
	}
	for _, c := range cases {
		resp, raw := postJSON(t, srv.URL+OccurrencesURL, map[string]any{
			"docId": "demo", "from": c.from, "to": c.to,
		})
		if resp.StatusCode != c.wantStatus || !strings.Contains(raw, c.wantCode) {
			t.Errorf("%s: status=%d body=%s, want %d %s", c.name, resp.StatusCode, raw, c.wantStatus, c.wantCode)
		}
	}
}

func TestOccurrencesFailsClosedOnBadSource(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(),
		WithEventsSource(func(context.Context) ([]Event, error) {
			return nil, context.DeadlineExceeded
		}))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	resp, raw := postJSON(t, srv.URL+OccurrencesURL, map[string]any{
		"docId": "demo", "from": "2026-03-02", "to": "2026-03-15",
	})
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(raw, "E_SOURCE") {
		t.Errorf("source error: status=%d body=%s, want 500 E_SOURCE (fail closed, never a panic)", resp.StatusCode, raw)
	}
}

// --- /move -------------------------------------------------------------------------

func TestMoveRouteSpringForwardRoundTrip(t *testing.T) {
	app, p := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// Drag the 01:30 event (30 minutes before the gap) one hour down the
	// wall grid: the server answers 02:30→03:30 with all three deltas.
	resp, raw := postJSON(t, srv.URL+MoveURL, map[string]any{
		"docId": "demo", "eventId": "gapend", "date": "2026-03-08", "dayDelta": 0, "minuteDelta": 60,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move status=%d body=%s", resp.StatusCode, raw)
	}
	var res MoveResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("decode move result: %v", err)
	}
	if res.RequestedWallMinutes != 60 || res.ActualWallMinutes != 120 || res.ElapsedMinutes != 60 {
		t.Errorf("deltas = %d/%d/%d, want 60/120/60", res.RequestedWallMinutes, res.ActualWallMinutes, res.ElapsedMinutes)
	}
	if res.Occurrence.StartWall != "2026-03-08T03:30" {
		t.Errorf("resolved = %s, want 2026-03-08T03:30 EDT", res.Occurrence.StartWall)
	}
	if !strings.Contains(res.Note, "does not exist") {
		t.Errorf("note = %q, want the gap explanation the demo readout shows", res.Note)
	}

	// The override persisted in the plugin's store and a subsequent window
	// reflects it — the edit survives a reload of the page (same process).
	resp, raw = postJSON(t, srv.URL+OccurrencesURL, map[string]any{
		"docId": "demo", "from": "2026-03-07", "to": "2026-03-09",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("occurrences status=%d", resp.StatusCode)
	}
	if !strings.Contains(raw, `"startWall":"2026-03-08T03:30"`) {
		t.Errorf("window after move does not show the moved occurrence: %s", raw)
	}
	if len(p.Overrides()) != 1 {
		t.Errorf("override store has %d entries, want 1", len(p.Overrides()))
	}
}

func TestMoveRouteValidation(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantCode   string
	}{
		{"missing event", map[string]any{"docId": "demo", "eventId": "", "date": "2026-03-08", "dayDelta": 0, "minuteDelta": 30}, http.StatusBadRequest, "E_BAD_MOVE"},
		{"unknown event", map[string]any{"docId": "demo", "eventId": "nope", "date": "2026-03-08", "dayDelta": 0, "minuteDelta": 30}, http.StatusNotFound, "E_NO_EVENT"},
		{"bad date", map[string]any{"docId": "demo", "eventId": "gapend", "date": "yesterday", "dayDelta": 0, "minuteDelta": 30}, http.StatusBadRequest, "E_BAD_MOVE"},
		{"day out of range", map[string]any{"docId": "demo", "eventId": "gapend", "date": "2026-03-08", "dayDelta": 400, "minuteDelta": 0}, http.StatusBadRequest, "E_DELTA_OUT_OF_RANGE"},
		{"minutes out of range", map[string]any{"docId": "demo", "eventId": "gapend", "date": "2026-03-08", "dayDelta": 0, "minuteDelta": 1500}, http.StatusBadRequest, "E_DELTA_OUT_OF_RANGE"},
		{"all-day with minutes", map[string]any{"docId": "demo", "eventId": "offsite", "date": "2026-03-12", "dayDelta": 0, "minuteDelta": 30}, http.StatusUnprocessableEntity, "E_BAD_MOVE"},
		{"nonexistent occurrence", map[string]any{"docId": "demo", "eventId": "board", "date": "2026-03-20", "dayDelta": 0, "minuteDelta": 30}, http.StatusUnprocessableEntity, "E_BAD_MOVE"},
	}
	for _, c := range cases {
		resp, raw := postJSON(t, srv.URL+MoveURL, c.body)
		if resp.StatusCode != c.wantStatus || !strings.Contains(raw, c.wantCode) {
			t.Errorf("%s: status=%d body=%s, want %d %s", c.name, resp.StatusCode, raw, c.wantStatus, c.wantCode)
		}
	}
}

// --- /save ---------------------------------------------------------------------------

func TestSavePersistsNormalizedDoc(t *testing.T) {
	app, p := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+SaveURL, map[string]any{
		"docId": "demo",
		"doc":   `{"schemaVersion":"calendar-v1","view":{"date":"2026-11-01","mode":"day"}}`,
	})
	if resp.StatusCode != http.StatusOK || !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("save status=%d body=%s", resp.StatusCode, raw)
	}
	got, ok := p.LoadDoc(context.Background(), "demo")
	if !ok || !strings.Contains(got, `"mode":"day"`) || !strings.Contains(got, `"date":"2026-11-01"`) {
		t.Fatalf("LoadDoc after save = %q ok=%v", got, ok)
	}

	// Bad docs are refused, not repaired.
	for _, bad := range []string{
		`{"schemaVersion":"calendar-v1","view":{"date":"2026-11-01","mode":"year"}}`,       // unknown mode
		`{"schemaVersion":"calendar-v1","view":{"date":"not-a-date","mode":"week"}}`,       // malformed date
		`{"schemaVersion":"datavis-v1","view":{"date":"2026-11-01","mode":"week"}}`,        // wrong schema
		`{"schemaVersion":"calendar-v1","view":{"date":"2026-11-01","mode":"week"},"x":1}`, // unknown field
	} {
		resp, raw := postJSON(t, srv.URL+SaveURL, map[string]any{"docId": "demo", "doc": bad})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(raw, "E_BAD_DOC") {
			t.Errorf("save of %s: status=%d body=%s, want 400 E_BAD_DOC", bad, resp.StatusCode, raw)
		}
	}
}

func TestEnvelopeStrictness(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []string{
		`{"docId":"demo","from":"2026-03-02","to":"2026-03-15"} {"docId":"demo"}`, // two values
		`{"docId":"demo","from":"2026-03-02","to":"2026-03-15"} garbage`,          // trailing bytes
		`{not json`, // malformed
	}
	for _, body := range cases {
		resp, raw := postRaw(t, srv.URL+OccurrencesURL, body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(raw, "E_BAD_JSON") {
			t.Errorf("envelope %q: status=%d body=%s, want 400 E_BAD_JSON", body, resp.StatusCode, raw)
		}
	}
	// Trailing whitespace is fine (curl -d @file sends a newline).
	resp, _ := postRaw(t, srv.URL+OccurrencesURL, `{"docId":"demo","from":"2026-03-02","to":"2026-03-15"}
`)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("envelope with trailing newline: status=%d, want 200", resp.StatusCode)
	}
}

// --- capability gate -------------------------------------------------------------------

// TestRoutesDeniedWithoutCapability proves the gate is wired into every
// route: a token whose scopes lack the capability gets 403 +
// E_CAPABILITY_DENIED, while an anonymous caller (Allow's documented
// pass-for-anonymous semantics, bounded by the plugin grant) gets through.
func TestRoutesDeniedWithoutCapability(t *testing.T) {
	app, _ := newTestApp(t) // no WithDevGrantAll
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	post := func(url string, body string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body)).WithContext(deniedCtx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.Router().ServeHTTP(w, req)
		return w.Result()
	}

	var body struct {
		Error      string `json:"error"`
		Capability string `json:"capability"`
	}
	for _, url := range []string{OccurrencesURL, MoveURL, SaveURL} {
		payload := `{"docId":"demo","from":"2026-03-02","to":"2026-03-15"}`
		if url == MoveURL {
			payload = `{"docId":"demo","eventId":"gapend","date":"2026-03-08","dayDelta":0,"minuteDelta":30}`
		}
		resp := post(url, payload)
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || body.Error != "E_CAPABILITY_DENIED" {
			t.Errorf("%s: status=%d error=%q capability=%q, want 403 E_CAPABILITY_DENIED", url, resp.StatusCode, body.Error, body.Capability)
		}
	}

	// Anonymous callers pass the gate (bounded by the default grant set).
	resp, raw := postJSON(t, srv.URL+OccurrencesURL, map[string]any{
		"docId": "demo", "from": "2026-03-02", "to": "2026-03-15",
	})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("anonymous /occurrences: status=%d body=%s, want 200 (Allow passes for anonymous callers)", resp.StatusCode, raw)
	}
}

// A wildcard grant is legal (the framework's scope grammar) and must not
// change behavior on any route — there are no handler-gated optional
// capabilities here to desync against.
func TestWildcardCapabilitiesWork(t *testing.T) {
	app, p := newTestApp(t, WithCapabilities("*:*"))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	resp, raw := postJSON(t, srv.URL+MoveURL, map[string]any{
		"docId": "demo", "eventId": "gapend", "date": "2026-03-08", "dayDelta": 0, "minuteDelta": 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wildcard grant move: status=%d body=%s", resp.StatusCode, raw)
	}
	if !p.grantsCapability(CapDocWrite) || !p.grantsCapability(CapDocRead) {
		t.Errorf("grantsCapability under *:* returned false")
	}
}

// The move handler hook is where a production host authorizes the edit; the
// default in-memory store accepts everything, which is exactly why the
// package doc says the check belongs in WithMoveHandler.
func TestMoveHandlerHookReceivesOverrides(t *testing.T) {
	var seen []Override
	app, _ := newTestApp(t, WithDevGrantAll(),
		WithMoveHandler(func(_ context.Context, ov Override) error {
			seen = append(seen, ov)
			return nil
		}))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+MoveURL, map[string]any{
		"docId": "demo", "eventId": "gapend", "date": "2026-03-08", "dayDelta": 0, "minuteDelta": 60,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move status=%d body=%s", resp.StatusCode, raw)
	}
	if len(seen) != 1 || seen[0].EventID != "gapend" || seen[0].Date != "2026-03-08" {
		t.Fatalf("move handler saw %+v, want the gapend override", seen)
	}
}

// --- doc normalization edge ------------------------------------------------------------

func TestNormalizeDocDefaults(t *testing.T) {
	doc, err := normalizeDoc([]byte(`{"schemaVersion":"calendar-v1"}`))
	if err != nil {
		t.Fatalf("normalizeDoc empty: %v", err)
	}
	if doc.View.Mode != "week" {
		t.Errorf("default mode = %s, want week", doc.View.Mode)
	}
	if doc.View.Date == "" {
		t.Errorf("default date missing")
	}
}
