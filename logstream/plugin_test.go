package logstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// --- test fixtures ----------------------------------------------------------

// testLine is the deterministic generator mirror: line n's content is a pure
// function of n, with an ANSI colour code riding in the middle (the bridge
// must carry escapes intact — the host never interprets them).
func testLine(n uint64) string {
	return fmt.Sprintf("12:00:%02d \x1b[32mINFO\x1b[0m svc-%d deterministic line seq=%06d",
		n%60, n%4, n)
}

// testSource yields lines first..last then returns nil, honouring `after`
// (the reconnect contract). ctxDone fires when the consumer disconnects.
func testSource(first, last uint64) (SourceFunc, *syncBool) {
	disconnected := &syncBool{}
	fn := func(ctx context.Context, after uint64, yield func(Line) error) error {
		defer disconnected.set(true)
		for n := first; n <= last; n++ {
			if n <= after {
				continue // reconnect replay: only lines the frame has not acked
			}
			if err := yield(Line{Seq: n, Text: testLine(n)}); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		return nil
	}
	return fn, disconnected
}

type syncBool struct {
	mu sync.Mutex
	v  bool
}

func (s *syncBool) set(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v = v
}

func (s *syncBool) get() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v
}

func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "logstream-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// fullTestApp wires the source + demo page (+ control URL) so every route
// and the demo exist.
func fullTestApp(t *testing.T) (*framework.App, *Plugin) {
	t.Helper()
	src, _ := testSource(1, 50)
	app, p := newTestApp(t,
		WithDevGrantAll(),
		WithDemoPage(),
		WithSource(src),
		WithDemoControlURL("/demo/logstream/rate"),
	)
	return app, p
}

// --- assets -----------------------------------------------------------------

func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{TermHTMLURL, "text/html; charset=utf-8"},
		{TermJSURL, "text/javascript; charset=utf-8"},
		{TermCSSURL, "text/css; charset=utf-8"},
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

// TestFramedAssetsCarryHeaderRelaxation proves the platform AssetServer carries
// the framing/CORP/CSP relaxation that lets the host frame its OWN terminal
// document, AND that the fixed framedCSP carries connect-src 'none' + sandbox
// allow-scripts — the directives that make every line cross the bridge
// instead of being fetched by the frame.
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{TermHTMLURL, TermJSURL, TermCSSURL} {
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
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(raw)
	for _, want := range []string{
		`data-fui-plugin="logstream"`,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
		`id="ls-live-dropped"`, // the live proof strip
		`id="ls-btn-pause"`,    // the affordance strip
		`id="ls-btn-fast"`,     // rate switch (control URL wired)
		"/demo/logstream/rate",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

// Without a control route the rate buttons must be GONE, not broken — a
// button that POSTs a 404 is a demo that looks busted.
func TestDemoPageWithoutControlURLOmitsRateSwitch(t *testing.T) {
	src, _ := testSource(1, 5)
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage(), WithSource(src))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(raw)
	if strings.Contains(page, `id="ls-btn-fast"`) {
		t.Error("demo page rendered the Flood button without a control route")
	}
	if !strings.Contains(page, `id="ls-btn-pause"`) {
		t.Error("demo page must always carry Pause/Resume (host-side adapter state)")
	}
}

// --- /stream ----------------------------------------------------------------

// The happy path: NDJSON on the wire, seqs in order, ANSI intact, streaming
// headers that keep intermediaries from batching the push.
func TestStreamEmitsNDJSONLines(t *testing.T) {
	src, _ := testSource(1, 10)
	app, _ := newTestApp(t, WithDevGrantAll(), WithSource(src))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + StreamURL)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson; charset=utf-8" {
		t.Errorf("content-type=%q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache-control=%q, want no-store", cc)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("X-Accel-Buffering=%q, want no (proxies must not batch the push)", resp.Header.Get("X-Accel-Buffering"))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	var got []Line
	for sc.Scan() {
		rec := sc.Bytes()
		if len(strings.TrimSpace(string(rec))) == 0 {
			t.Fatalf("blank record inside the stream: %q", rec)
		}
		var l Line
		if err := json.Unmarshal(rec, &l); err != nil {
			t.Fatalf("record is not one JSON value: %v (%q)", err, rec)
		}
		got = append(got, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d lines, want 10", len(got))
	}
	for i, l := range got {
		if l.Seq != uint64(i+1) {
			t.Errorf("line %d: seq=%d, want %d", i, l.Seq, i+1)
		}
		if l.Text != testLine(l.Seq) {
			t.Errorf("line %d: content=%q, want %q", i, l.Text, testLine(l.Seq))
		}
		if !strings.Contains(l.Text, "\x1b[32m") {
			t.Errorf("line %d: ANSI escape did not survive the crossing: %q", i, l.Text)
		}
	}
}

// ?after=N is the reconnect contract: only lines the frame has NOT acked.
func TestStreamAfterResumesFromSeq(t *testing.T) {
	src, _ := testSource(1, 10)
	app, _ := newTestApp(t, WithDevGrantAll(), WithSource(src))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + StreamURL + "?after=7")
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var seqs []uint64
	for sc.Scan() {
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("bad record: %v", err)
		}
		seqs = append(seqs, l.Seq)
	}
	if len(seqs) != 3 || seqs[0] != 8 || seqs[2] != 10 {
		t.Errorf("after=7 replay = %v, want [8 9 10]", seqs)
	}

	// The junk-After guard: not an integer → 400, not a guess.
	bad, err := http.Get(srv.URL + StreamURL + "?after=-3")
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("after=-3 status=%d, want 400", bad.StatusCode)
	}
}

// The line contract enforced host-side: embedded newlines/CRs collapse (a
// "line" that renders as three corrupts both the NDJSON framing and the
// frame's scrollback accounting — json escaping keeps the framing safe, this
// keeps the semantics honest), and overlong lines truncate at the bound.
func TestStreamSanitizesNewlinesAndCapsLength(t *testing.T) {
	long := strings.Repeat("x", maxLineBytes+100)
	src := SourceFunc(func(_ context.Context, _ uint64, yield func(Line) error) error {
		_ = yield(Line{Seq: 1, Text: "before\r\nafter\rmiddle\nend"})
		_ = yield(Line{Seq: 2, Text: long})
		return nil
	})
	app, _ := newTestApp(t, WithDevGrantAll(), WithSource(src))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + StreamURL)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	var lines []Line
	for sc.Scan() {
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("bad record: %v", err)
		}
		lines = append(lines, l)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d records, want 2", len(lines))
	}
	if strings.ContainsAny(lines[0].Text, "\r\n") {
		t.Errorf("embedded newline survived: %q", lines[0].Text)
	}
	if !strings.Contains(lines[0].Text, "before after middle end") {
		t.Errorf("newline sanitisation mangled the text: %q", lines[0].Text)
	}
	if len(lines[1].Text) != maxLineBytes {
		t.Errorf("long line len=%d, want capped at %d", len(lines[1].Text), maxLineBytes)
	}
}

// --- the gate ----------------------------------------------------------------

// TestCapabilityGate proves both gate sides at the unit level: an enforcing
// plugin denies a token whose scopes lack the capability, a scoped token
// carrying it passes, and WithDevGrantAll short-circuits the gate.
func TestCapabilityGate(t *testing.T) {
	src, _ := testSource(1, 5)
	enforcing := New(WithSource(src))
	granted := New(WithDevGrantAll(), WithSource(src))

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	allowedCtx := auth.WithTokenScopes(context.Background(), []string{"stream:read"})

	deniedReq := httptest.NewRequest(http.MethodGet, StreamURL, nil).WithContext(deniedCtx)
	if enforcing.allow(deniedReq, CapStreamRead) {
		t.Error("enforcing plugin should DENY a non-granting token")
	}
	allowedReq := httptest.NewRequest(http.MethodGet, StreamURL, nil).WithContext(allowedCtx)
	if !enforcing.allow(allowedReq, CapStreamRead) {
		t.Error("enforcing plugin should ALLOW a stream:read token")
	}
	wildReq := httptest.NewRequest(http.MethodGet, StreamURL, nil).
		WithContext(auth.WithTokenScopes(context.Background(), []string{"stream:*"}))
	if !enforcing.allow(wildReq, CapStreamRead) {
		t.Error("stream:* wildcard token should imply stream:read under the scope grammar")
	}
	if !granted.allow(deniedReq, CapStreamRead) {
		t.Error("WithDevGrantAll should ALLOW regardless of token scopes")
	}
}

// TestStreamDeniedWithoutCapability proves the gate is wired into the route:
// a token whose scopes lack the capability gets 403 + E_CAPABILITY_DENIED,
// before any line is yielded.
func TestStreamDeniedWithoutCapability(t *testing.T) {
	yielded := false
	src := SourceFunc(func(_ context.Context, _ uint64, yield func(Line) error) error {
		yielded = true
		_ = yield(Line{Seq: 1, Text: "should never cross"})
		return nil
	})
	app, _ := newTestApp(t, WithSource(src))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	req := httptest.NewRequest(http.MethodGet, StreamURL, nil).WithContext(deniedCtx)
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req) // direct handler use keeps ctx scopes
	resp := rec.Result()
	defer resp.Body.Close()
	var body struct {
		Error      string `json:"error"`
		Capability string `json:"capability"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusForbidden || body.Error != "E_CAPABILITY_DENIED" {
		t.Errorf("status=%d error=%q, want 403 E_CAPABILITY_DENIED", resp.StatusCode, body.Error)
	}
	if body.Capability != CapStreamRead {
		t.Errorf("capability=%q, want %q", body.Capability, CapStreamRead)
	}
	if yielded {
		t.Error("source yielded a line despite the denial — the gate must run first")
	}
}

// --- lifecycle ---------------------------------------------------------------

// A cancelled request context must release the source: the consumer went
// away, so the producer goroutine cannot be pinned.
func TestStreamStopsWhenClientDisconnects(t *testing.T) {
	release := make(chan struct{})
	src := SourceFunc(func(ctx context.Context, _ uint64, yield func(Line) error) error {
		defer close(release)
		n := uint64(0)
		for {
			n++
			if err := yield(Line{Seq: n, Text: testLine(n)}); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
	})
	app, _ := newTestApp(t, WithDevGrantAll(), WithSource(src))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+StreamURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	if err != nil || line == "" {
		t.Fatalf("first record missing: %q err=%v", line, err)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-release:
	case <-time.After(5 * time.Second):
		t.Fatal("source was not released after client disconnect")
	}
}

// NOTE: there is deliberately NO stalled-consumer deadline test. The write
// deadline is best-effort because gofastr's router wraps the ResponseWriter
// past what http.NewResponseController can reach — under this repo's own
// stack the deadline degrades to a no-op (disconnect detection falls back to
// the request context, covered by the disconnect test above). The emit test
// proves the degraded path still streams.

// TestNewPanicsWithoutSource: fail loud at construction, not blank at mount.
func TestNewPanicsWithoutSource(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("New without WithSource should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "WithSource") {
			t.Errorf("panic message = %q, want it to name WithSource", msg)
		}
	}()
	New()
}

// TestManifestInvariants pins the platform contract the registry tests also
// enforce from plugins.json: opaque sandbox, no allow-same-origin, schema.
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
	if m.Entry != TermHTMLURL {
		t.Fatalf("entry=%q", m.Entry)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
