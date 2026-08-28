package whiteboard

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newTestApp stands up a fresh framework.App with the plugin registered and
// initialized (the mermaid/datagrid test harness shape).
func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "whiteboard-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// testHub is the smallest hub that satisfies the contract: it assigns
// p-N identities, keeps the update history, fans out to other subscribers,
// and never echoes to the publisher.
type testHub struct {
	mu      sync.Mutex
	pidSeq  int
	history [][]byte
	subs    map[string]chan RoomEvent
	colors  map[string]string
}

func newTestHub() *testHub {
	return &testHub{subs: make(map[string]chan RoomEvent), colors: make(map[string]string)}
}

func (h *testHub) Subscribe(ctx context.Context, _ string, yield func(RoomEvent) error) error {
	h.mu.Lock()
	h.pidSeq++
	pid := fmt.Sprintf("p-%d", h.pidSeq)
	color := fmt.Sprintf("color-%d", h.pidSeq)
	ch := make(chan RoomEvent, 16)
	h.subs[pid] = ch
	h.colors[pid] = color
	replay := append([][]byte{}, h.history...)
	count := len(h.subs)
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.subs, pid)
		delete(h.colors, pid)
		h.mu.Unlock()
	}()

	if err := yield(RoomEvent{Kind: EventHello, PID: pid, Color: color, Count: count}); err != nil {
		return err
	}
	for _, u := range replay {
		if err := yield(RoomEvent{Kind: EventSync, Update: u}); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := yield(ev); err != nil {
				return err
			}
		}
	}
}

func (h *testHub) Publish(_ context.Context, _, fromPID string, ev RoomEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ev.Kind == EventSync {
		h.history = append(h.history, ev.Update)
	}
	if ev.Kind == EventPresence {
		// Same rule as the example hub: the ASSIGNED colour crosses, never a
		// client-supplied one.
		ev.Color = h.colors[fromPID]
	}
	for pid, ch := range h.subs {
		if pid == fromPID {
			continue // never echo to the publisher
		}
		select {
		case ch <- ev:
		default: // test hub: drop rather than block; not under assertions
		}
	}
	return nil
}

// --- construction -------------------------------------------------------------

func TestNewDefaults(t *testing.T) {
	p := New()
	if got := p.Capabilities(); len(got) != 1 || got[0] != "theme:read" {
		t.Fatalf("default capabilities = %v, want [theme:read] (sync:room is optional)", got)
	}
	m := p.Manifest()
	if m.Entry != BoardHTMLURL || m.Isolation != pluginhost.IsolationSandboxOpaque {
		t.Fatalf("manifest entry/isolation = %q/%q", m.Entry, m.Isolation)
	}
	if strings.Join(m.Sandbox, ",") != "allow-scripts" {
		t.Fatalf("sandbox = %v, want [allow-scripts] only", m.Sandbox)
	}
	if m.Schema != SchemaVersion {
		t.Fatalf("schema = %q want %q", m.Schema, SchemaVersion)
	}
}

func TestWithRoomHubGrantsSyncRoom(t *testing.T) {
	hub := newTestHub()
	p := New(WithRoomHub(hub.Subscribe, hub.Publish))
	if !containsCap(p.Capabilities(), CapSyncRoom) {
		t.Fatalf("WithRoomHub must append %s, got %v", CapSyncRoom, p.Capabilities())
	}
}

// TestNewPanicsWhenSyncRoomImpliedWithoutHub pins the trap the wildcard
// grammar creates: a grant like "sync:*" or "*:*" IMPLIES sync:room, so
// constructing with it and no hub must fail loudly at New — string equality
// would let it compile, pass the runtime gate, and then reach nil hub
// functions behind the routes (the datagrid lesson).
func TestNewPanicsWhenSyncRoomImpliedWithoutHub(t *testing.T) {
	for _, grant := range []string{CapSyncRoom, "sync:*", "*:*"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("New(WithCapabilities(%q)) without hub must panic", grant)
				}
			}()
			_ = New(WithCapabilities(grant))
		}()
	}
}

func TestNewPanicsOnHalfWiredHub(t *testing.T) {
	hub := newTestHub()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("half-wired hub (subscribe only) must panic")
			}
		}()
		_ = New(WithRoomHub(hub.Subscribe, nil))
	}()
}

// --- routes: fail closed, content types -----------------------------------------

func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithDemoPage(), WithRoomHub(newTestHub().Subscribe, newTestHub().Publish))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{BoardHTMLURL, "text/html; charset=utf-8"},
		{BoardJSURL, "text/javascript; charset=utf-8"},
		{BoardCSSURL, "text/css; charset=utf-8"},
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
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%q", c.path, resp.StatusCode, body)
		}
		if got := resp.Header.Get("Content-Type"); got != c.wantCT {
			t.Errorf("%s: Content-Type=%q want %q", c.path, got, c.wantCT)
		}
		if len(body) == 0 {
			t.Errorf("%s: empty body", c.path)
		}
	}
}

// TestFramedCSPForbidsConnections is the load-bearing assertion of the whole
// plugin: the framed assets ship connect-src 'none', so the frame structurally
// cannot open the connection collaboration would naively need.
func TestFramedCSPForbidsConnections(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithRoomHub(newTestHub().Subscribe, newTestHub().Publish))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{BoardHTMLURL, BoardJSURL, BoardCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "connect-src 'none'") {
			t.Errorf("%s: framed CSP must pin connect-src 'none', got %q", path, csp)
		}
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: framed CSP must carry sandbox allow-scripts, got %q", path, csp)
		}
	}
}

// TestRoutesFailClosedWithoutHub proves a hub-less mount is not an open
// relay, on BOTH gate sides:
//   - the real gate (no dev bypass): sync:room is absent from the grant set,
//     so pluginhost.Allow denies every caller with 403;
//   - the dev-bypass escape hatch: WithDevGrantAll skips the gate, so the
//     nil-hub guard is what fails closed (503 E_NO_ROOM_HUB) — a clear error,
//     never a panic.
func TestRoutesFailClosedWithoutHub(t *testing.T) {
	app, _ := newTestApp(t, WithDemoPage()) // enforcing gate, no hub wired
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	body := `{"pid":"p-1","kind":"sync","update":"AAAA"}`
	resp, err := http.Post(srv.URL+PublishURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST publish: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("publish without hub (enforcing gate): status=%d want 403", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + StreamURL)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("stream without hub (enforcing gate): status=%d want 403", resp2.StatusCode)
	}

	appDev, _ := newTestApp(t, WithDevGrantAll()) // gate bypassed, still no hub
	srvDev := httptest.NewServer(appDev.Router())
	defer srvDev.Close()
	resp3, err := http.Post(srvDev.URL+PublishURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST publish (dev bypass): %v", err)
	}
	b, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("publish without hub (dev bypass): status=%d body=%q want 503", resp3.StatusCode, b)
	}
	if !strings.Contains(string(b), "E_NO_ROOM_HUB") {
		t.Fatalf("dev-bypass denial must name E_NO_ROOM_HUB, got %q", b)
	}
}

// --- the room contract over real HTTP --------------------------------------------

// sseReader wraps ONE body with ONE scanner: a fresh scanner per read would
// buffer past the event it was waiting for and swallow the next one, so all
// reads on a stream must share it.
type sseReader struct {
	sc    *bufio.Scanner
	event string
}

func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &sseReader{sc: sc}
}

// wait consumes lines until the named event's data line arrives (or the
// deadline does). Events other than wantEvent are skipped.
func (sr *sseReader) wait(t *testing.T, wantEvent string, timeout time.Duration) string {
	t.Helper()
	type result struct {
		data string
		ok   bool
	}
	done := make(chan result, 1)
	go func() {
		for sr.sc.Scan() {
			line := sr.sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				sr.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if sr.event == wantEvent {
					done <- result{data: strings.TrimPrefix(line, "data: "), ok: true}
					return
				}
			}
		}
		done <- result{}
	}()
	select {
	case res := <-done:
		if !res.ok {
			t.Fatalf("stream ended before a %q event arrived", wantEvent)
		}
		return res.data
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %q SSE event", wantEvent)
		return ""
	}
}

func TestRoomRoundTripReplayAndLive(t *testing.T) {
	hub := newTestHub()
	app, _ := newTestApp(t, WithDevGrantAll(), WithRoomHub(hub.Subscribe, hub.Publish))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// Participant 1 joins.
	resp1, err := http.Get(srv.URL + StreamURL + "?docId=demo")
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp1.Body.Close()
	if ct := resp1.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("stream Content-Type=%q", ct)
	}
	hello1 := newSSEReader(resp1.Body).wait(t, "hello", 3*time.Second)
	if !strings.Contains(hello1, `"pid":"p-1"`) || !strings.Contains(hello1, `"color":"color-1"`) {
		t.Fatalf("hello for first joiner = %q, want pid p-1 + assigned color", hello1)
	}
	if !strings.Contains(hello1, `"participants":1`) {
		t.Fatalf("hello must report participants, got %q", hello1)
	}

	// p-1 publishes a stroke update; the hub keeps it.
	stroke := []byte{0x01, 0x02, 0x03, 0x04}
	pub := func(pid string, body string) *http.Response {
		t.Helper()
		resp, err := http.Post(srv.URL+PublishURL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST publish: %v", err)
		}
		return resp
	}
	r := pub("p-1", fmt.Sprintf(`{"docId":"demo","pid":"p-1","kind":"sync","update":%q}`, base64.StdEncoding.EncodeToString(stroke)))
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("publish status=%d", r.StatusCode)
	}

	// Participant 2 joins AFTER the stroke: hello, then the REPLAYED state.
	resp2, err := http.Get(srv.URL + StreamURL + "?docId=demo")
	if err != nil {
		t.Fatalf("GET stream 2: %v", err)
	}
	defer resp2.Body.Close()
	sr2 := newSSEReader(resp2.Body)
	hello2 := sr2.wait(t, "hello", 3*time.Second)
	if !strings.Contains(hello2, `"pid":"p-2"`) {
		t.Fatalf("second joiner hello = %q, want pid p-2 (opaque, hub-assigned)", hello2)
	}
	replay := sr2.wait(t, "sync", 3*time.Second)
	if got, err := base64.StdEncoding.DecodeString(replay); err != nil || string(got) != string(stroke) {
		t.Fatalf("replayed update = %q (err %v), want the exact published blob", replay, err)
	}

	// A live publish from p-2 reaches p-1's stream...
	live := []byte{0x09, 0x08}
	r2 := pub("p-2", fmt.Sprintf(`{"docId":"demo","pid":"p-2","kind":"sync","update":%q}`, base64.StdEncoding.EncodeToString(live)))
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("live publish status=%d", r2.StatusCode)
	}
	sr1 := newSSEReader(resp1.Body)
	liveData := sr1.wait(t, "sync", 3*time.Second)
	if got, _ := base64.StdEncoding.DecodeString(liveData); string(got) != string(live) {
		t.Fatalf("live fan-out = %q, want the exact published blob", liveData)
	}

	// ...and presence crosses with the publisher's ASSIGNED colour, never a
	// client-supplied one and never a name.
	r3 := pub("p-2", `{"docId":"demo","pid":"p-2","kind":"presence","x":0.5,"y":0.25,"down":true}`)
	r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("presence publish status=%d", r3.StatusCode)
	}
	pres := sr1.wait(t, "presence", 3*time.Second)
	if !strings.Contains(pres, `"color":"color-2"`) {
		t.Fatalf("presence must carry the hub-assigned color, got %q", pres)
	}
	if strings.Contains(strings.ToLower(pres), "name") {
		t.Fatalf("presence must never carry a name field, got %q", pres)
	}
}

// TestPublishEchoExclusion pins the no-echo half of the contract: a
// publisher must not receive its own update back (the adapter would double
// the traffic and the demo's byte counters would lie).
func TestPublishEchoExclusion(t *testing.T) {
	hub := newTestHub()
	app, _ := newTestApp(t, WithDevGrantAll(), WithRoomHub(hub.Subscribe, hub.Publish))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + StreamURL)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	sr1 := newSSEReader(resp.Body)
	sr1.wait(t, "hello", 3*time.Second)

	// Publish as p-1 (this stream's own pid): nothing may come back.
	body := fmt.Sprintf(`{"pid":"p-1","kind":"sync","update":%q}`, base64.StdEncoding.EncodeToString([]byte{1, 2, 3}))
	r, err := http.Post(srv.URL+PublishURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST publish: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("publish status=%d", r.StatusCode)
	}

	// Give the hub a beat to (wrongly) echo, then prove silence by having a
	// SECOND subscriber publish and seeing only that event arrive.
	resp2, err := http.Get(srv.URL + StreamURL)
	if err != nil {
		t.Fatalf("GET stream 2: %v", err)
	}
	defer resp2.Body.Close()
	newSSEReader(resp2.Body).wait(t, "hello", 3*time.Second)

	// No sync event may be pending on stream 1 for its OWN publish; assert
	// indirectly: p-2's publish must be the FIRST sync stream 1 ever sees.
	// (A wrongly-echoed own publish would surface here as the wrong bytes.)
	r2, _ := http.Post(srv.URL+PublishURL, "application/json",
		strings.NewReader(fmt.Sprintf(`{"pid":"p-2","kind":"sync","update":%q}`, base64.StdEncoding.EncodeToString([]byte{9, 9, 9}))))
	r2.Body.Close()
	first := sr1.wait(t, "sync", 3*time.Second)
	if got, _ := base64.StdEncoding.DecodeString(first); string(got) != "\x09\x09\x09" {
		t.Fatalf("first sync on stream 1 = %q — own publish was echoed back", first)
	}
}

// TestPublishRejectsBadPayloads covers the 400 paths: unknown kind, bad
// base64, missing pid. A broken client must get a clear error, never a
// panic (a panic in an HTTP handler is a DoS on the host).
func TestPublishRejectsBadPayloads(t *testing.T) {
	hub := newTestHub()
	app, _ := newTestApp(t, WithDevGrantAll(), WithRoomHub(hub.Subscribe, hub.Publish))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct {
		name, body string
	}{
		{"unknown kind", `{"pid":"p-1","kind":"chat","update:""AAAA"}`},
		{"bad base64", `{"pid":"p-1","kind":"sync","update":"!!!not-base64!!!"}`},
		{"missing pid", `{"kind":"sync","update":"AAAA"}`},
		{"empty update", `{"pid":"p-1","kind":"sync","update":""}`},
		{"not json", `pid=p-1`},
	}
	for _, c := range cases {
		resp, err := http.Post(srv.URL+PublishURL, "application/json", strings.NewReader(c.body))
		if err != nil {
			t.Fatalf("%s: POST: %v", c.name, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status=%d want 400", c.name, resp.StatusCode)
		}
	}
}

// TestPublishCapsBodySize proves the DoS bound: a body over maxPublishBytes
// is refused, not buffered.
func TestPublishCapsBodySize(t *testing.T) {
	hub := newTestHub()
	app, _ := newTestApp(t, WithDevGrantAll(), WithRoomHub(hub.Subscribe, hub.Publish))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	big := make([]byte, maxPublishBytes+4096)
	body := fmt.Sprintf(`{"pid":"p-1","kind":"sync","update":%q}`, base64.StdEncoding.EncodeToString(big))
	resp, err := http.Post(srv.URL+PublishURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized publish status=%d want 400", resp.StatusCode)
	}
}

// TestMountMarker pins the mount shape the broker scans for.
func TestMountMarker(t *testing.T) {
	m := string(Mount(MountConfig{DocID: "room-7", MinHeight: "500px"}))
	if !strings.Contains(m, `data-fui-plugin="whiteboard"`) {
		t.Fatalf("marker missing plugin name: %s", m)
	}
	if !strings.Contains(m, `data-fui-plugin-docid="room-7"`) {
		t.Fatalf("marker missing room id: %s", m)
	}
	if strings.Contains(m, "allow-same-origin") {
		t.Fatalf("marker must never carry allow-same-origin: %s", m)
	}
}

// TestMountDefaults verifies the default room id and height apply.
func TestMountDefaults(t *testing.T) {
	m := string(Mount(MountConfig{}))
	if !strings.Contains(m, `data-fui-plugin-docid="demo"`) {
		t.Fatalf("default docId must be demo: %s", m)
	}
	if !strings.Contains(m, `data-fui-plugin-minheight="480px"`) {
		t.Fatalf("default minHeight must be 480px: %s", m)
	}
}

// TestConfigScriptReflectsWiring pins the bilateral channel: config.js must
// tell the adapter the truth about whether the hub was wired, or the frame
// would offer collaboration the host never enabled.
func TestConfigScriptReflectsWiring(t *testing.T) {
	unwired := New(WithDevGrantAll())
	if !strings.Contains(string(unwired.configScriptBytes()), "syncEnabled: false") {
		t.Fatal("unwired instance must publish syncEnabled: false")
	}
	hub := newTestHub()
	wired := New(WithDevGrantAll(), WithRoomHub(hub.Subscribe, hub.Publish))
	if !strings.Contains(string(wired.configScriptBytes()), "syncEnabled: true") {
		t.Fatal("wired instance must publish syncEnabled: true")
	}
}
