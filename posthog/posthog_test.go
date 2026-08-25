package posthog

// Unit suite for the packaged PostHog integration. No chromedp, no
// vendor account: every relay upstream is a loopback httptest server
// (newWithUpstreams is the unexported seam that makes that possible).
//
// What each test pins is in the per-test comments. The deep relay
// matrix (hostile tails, credential stripping, header hygiene) is owned
// by battery/relay's own suite; these tests cover only what THIS
// package adds: config validation, the rendered bootstrap, the identity
// endpoint, and the route table it declares.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DonaldMurillo/gofastr/battery/relay"
	"github.com/DonaldMurillo/gofastr/core/handler"
	"github.com/DonaldMurillo/gofastr/framework"
)

// fakeUpstream is a stand-in for one PostHog origin (assets or
// ingestion): records every request it receives and how many body
// bytes each carried.
type fakeUpstream struct {
	srv *httptest.Server

	mu    sync.Mutex
	reqs  []string
	bytes int64
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.RequestURI())
		f.bytes += n
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUpstream) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.reqs...)
}

func (f *fakeUpstream) received() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bytes
}

func testConfig() Config {
	return Config{Key: "phc_unit00000000000000000000"}
}

// bootApp builds a full framework.App with the plugin wired, exactly
// the production shape minus App.Start (the harness drives the router
// directly, the same pattern gofastr's site e2e uses).
func bootApp(t *testing.T, cfg Config, assets, ingest string) (*Plugin, *httptest.Server) {
	t.Helper()
	p := newWithUpstreams(cfg, assets, ingest)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "ph-unit"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	srv := httptest.NewServer(app.Router())
	t.Cleanup(srv.Close)
	return p, srv
}

// upstreamApp is bootApp without app wiring: enough for construction-
// and-rendering assertions that never serve.
func newPlugin(t *testing.T, cfg Config) *Plugin {
	t.Helper()
	return New(cfg)
}

func mustPanic(t *testing.T, wantSub string) {
	t.Helper()
	r := recover()
	if r == nil {
		t.Fatalf("expected a panic containing %q, got none", wantSub)
	}
	msg := fmt.Sprint(r)
	if !strings.Contains(msg, wantSub) {
		t.Fatalf("panic %q does not contain %q", msg, wantSub)
	}
}

// --- Config validation -------------------------------------------------

func TestNewPanicsOnEmptyKey(t *testing.T) {
	defer mustPanic(t, "posthog:")
	_ = New(Config{})
}

// The phx_/sk_ shapes are secrets; New must refuse to bake one into
// bytes served to every visitor.
func TestNewPanicsOnSecretKeys(t *testing.T) {
	for _, key := range []string{
		"phx_personal000000000000000",
		"sk_server000000000000000000",
	} {
		t.Run(key[:3], func(t *testing.T) {
			defer mustPanic(t, "posthog:")
			_ = New(Config{Key: key})
		})
	}
	// The public project key shape is accepted (no panic) — the
	// non-vacuity arm.
	if p := newPlugin(t, Config{Key: "phc_public0000000000000000"}); p == nil {
		t.Fatal("New returned nil for a valid phc_ key")
	}
}

func TestNewPanicsOnBadRegion(t *testing.T) {
	for _, region := range []string{"uk", "US", "eu ", "usa"} {
		t.Run(region, func(t *testing.T) {
			defer mustPanic(t, "posthog:")
			_ = New(Config{Key: "phc_k000000000000000000000", Region: region})
		})
	}
	// Both real regions — and the "" default — are accepted.
	for _, region := range []string{"", "us", "eu"} {
		t.Run("ok/"+region, func(t *testing.T) {
			_ = newPlugin(t, Config{Key: "phc_k000000000000000000000", Region: region})
		})
	}
}

// --- identity / naming surface ------------------------------------------

// Name must be "posthog" (the embedded relay would answer "relay"),
// Base the mount — default and override.
func TestNameAndBase(t *testing.T) {
	p := newPlugin(t, testConfig())
	if p.Name() != "posthog" {
		t.Fatalf("Name() = %q, want %q", p.Name(), "posthog")
	}
	if p.Base() != relay.DefaultPath {
		t.Fatalf("Base() = %q, want default %q", p.Base(), relay.DefaultPath)
	}
	p2 := newPlugin(t, Config{Key: "phc_k000000000000000000000", Path: "/fp"})
	if p2.Base() != "/fp" {
		t.Fatalf("Base() = %q, want /fp", p2.Base())
	}
}

// --- boot.js serving ----------------------------------------------------

// boot.js serves with the versioned-script policy: JavaScript content
// type, a strong ETag that answers 304, on both the default mount and a
// custom Path.
func TestBootJSServedWithETag(t *testing.T) {
	for _, mount := range []string{relay.DefaultPath, "/fp"} {
		t.Run(mount, func(t *testing.T) {
			_, srv := bootApp(t, Config{Key: "phc_k000000000000000000000", Path: mount}, "", "")

			resp, err := http.Get(srv.URL + mount + "/boot.js")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if ct := resp.Header.Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
				t.Fatalf("Content-Type = %q", ct)
			}
			etag := resp.Header.Get("ETag")
			if etag == "" {
				t.Fatal("no ETag on boot.js")
			}
			_, _ = io.Copy(io.Discard, resp.Body)

			req, _ := http.NewRequest(http.MethodGet, srv.URL+mount+"/boot.js", nil)
			req.Header.Set("If-None-Match", etag)
			resp2, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp2.Body.Close()
			if resp2.StatusCode != http.StatusNotModified {
				t.Fatalf("If-None-Match revalidation = %d, want 304", resp2.StatusCode)
			}
		})
	}
}

// ScriptURL and the served bytes must stay a pair: fetching the exact
// URL answers 200 with an immutable Cache-Control (its ?v= matches the
// content hash).
func TestScriptURLServesImmutable(t *testing.T) {
	p, srv := bootApp(t, testConfig(), "", "")
	url := p.ScriptURL()
	if !strings.HasPrefix(url, relay.DefaultPath+"/boot.js?v=") {
		t.Fatalf("ScriptURL() = %q, want prefix %q", url, relay.DefaultPath+"/boot.js?v=")
	}
	resp, err := http.Get(srv.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ScriptURL = %d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control = %q, want immutable", cc)
	}
}

// --- whoami --------------------------------------------------------------

// Anonymous shape over real HTTP: application/json, no-store, nosniff,
// {"id":null}.
func TestWhoamiAnonymousShape(t *testing.T) {
	_, srv := bootApp(t, testConfig(), "", "")
	resp, err := http.Get(srv.URL + relay.DefaultPath + "/whoami")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	if xo := resp.Header.Get("X-Content-Type-Options"); xo != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", xo)
	}
	if got, want := strings.TrimSpace(string(body)), `{"id":null}`; got != want {
		t.Fatalf("whoami body = %s, want %s", got, want)
	}
}

type strSeven struct{}

func (strSeven) String() string { return "7" }

// The recipes' default normalization: a string principal passes
// through, a fmt.Stringer is String()ed, anything else stays anonymous,
// and a context with no user at all is anonymous too.
func TestWhoamiPrincipals(t *testing.T) {
	p := newPlugin(t, testConfig())
	cases := []struct {
		name string
		user any
		want string
	}{
		{"string", "user-123", `{"id":"user-123"}`},
		{"stringer", strSeven{}, `{"id":"7"}`},
		{"opaque", 42, `{"id":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(handler.SetUser(req.Context(), tc.user))
			w := httptest.NewRecorder()
			p.whoami(w, req)
			if got := strings.TrimSpace(w.Body.String()); got != tc.want {
				t.Fatalf("whoami = %s, want %s", got, tc.want)
			}
		})
	}
	// No user in the context at all: GetUser's !ok arm.
	w := httptest.NewRecorder()
	p.whoami(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if got, want := strings.TrimSpace(w.Body.String()), `{"id":null}`; got != want {
		t.Fatalf("whoami(no user) = %s, want %s", got, want)
	}
}

// An Identify override wins over GetUser; ok=false means anonymous.
func TestWhoamiIdentifyOverride(t *testing.T) {
	p := newPlugin(t, Config{
		Key: "phc_k000000000000000000000",
		Identify: func(r *http.Request) (string, bool) {
			if v := r.Header.Get("X-Test-User"); v != "" {
				return v, true
			}
			return "", false
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Test-User", "from-header")
	w := httptest.NewRecorder()
	p.whoami(w, req)
	if got, want := strings.TrimSpace(w.Body.String()), `{"id":"from-header"}`; got != want {
		t.Fatalf("whoami = %s, want %s", got, want)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	w2 := httptest.NewRecorder()
	p.whoami(w2, req2)
	if got, want := strings.TrimSpace(w2.Body.String()), `{"id":null}`; got != want {
		t.Fatalf("whoami(ok=false) = %s, want %s", got, want)
	}
}

// --- relay route table ----------------------------------------------------

// Tails under no declared prefix 404; undeclared methods get 405 with
// an Allow header. (Spot-check; battery/relay owns the deep matrix.)
func TestRelay404And405(t *testing.T) {
	up := newFakeUpstream(t)
	_, srv := bootApp(t, testConfig(), up.srv.URL, up.srv.URL)

	resp, err := http.Get(srv.URL + relay.DefaultPath + "/nope/e")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown tail = %d, want 404", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+relay.DefaultPath+"/ph/e", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /ph/e = %d, want 405", resp2.StatusCode)
	}
	if allow := resp2.Header.Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "POST") {
		t.Fatalf("Allow = %q, want GET and POST", allow)
	}
	if n := len(up.requests()); n != 0 {
		t.Fatalf("upstream saw %d requests, want 0", n)
	}
}

// The sanitized tail and the query string map verbatim onto the
// declared upstreams: /ph/* lands on ingestion, /ph-assets/* on assets.
func TestRelayForwardsTailsAndQuery(t *testing.T) {
	assets := newFakeUpstream(t)
	ingest := newFakeUpstream(t)
	_, srv := bootApp(t, testConfig(), assets.srv.URL, ingest.srv.URL)
	base := srv.URL + relay.DefaultPath

	for _, tc := range []struct {
		url, want string
		up        *fakeUpstream
	}{
		{base + "/ph/e?ts=42", "GET /e?ts=42", ingest},
		{base + "/ph/batch", "GET /batch", ingest},
		{base + "/ph-assets/static/array.js", "GET /static/array.js", assets},
	} {
		resp, err := http.Get(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", tc.url, resp.StatusCode)
		}
	}
	if got := ingest.requests(); len(got) != 2 || got[0] != "GET /e?ts=42" || got[1] != "GET /batch" {
		t.Fatalf("ingest upstream saw %v", got)
	}
	if got := assets.requests(); len(got) != 1 || got[0] != "GET /static/array.js" {
		t.Fatalf("assets upstream saw %v", got)
	}
}

// --- region + body cap -----------------------------------------------------

// Each region resolves to its own three hosts; the test overrides
// replace only what they name.
func TestRegionHostsMapping(t *testing.T) {
	us, ok := regionHosts("us")
	if !ok {
		t.Fatal("region us not found")
	}
	if us.assets != "https://us-assets.i.posthog.com" ||
		us.ingest != "https://us.i.posthog.com" ||
		us.ui != "https://us.posthog.com" {
		t.Fatalf("us hosts = %+v", us)
	}
	eu, ok := regionHosts("eu")
	if !ok {
		t.Fatal("region eu not found")
	}
	if eu.assets != "https://eu-assets.i.posthog.com" ||
		eu.ingest != "https://eu.i.posthog.com" ||
		eu.ui != "https://eu.posthog.com" {
		t.Fatalf("eu hosts = %+v", eu)
	}
	if _, ok := regionHosts("uk"); ok {
		t.Fatal("region uk resolved")
	}
}

// The bootstrap carries the region's real ui_host (toolbar/replay
// player config, never relayed) and the DNT flag.
func TestRegionUIHostInBootstrap(t *testing.T) {
	for _, tc := range []struct{ region, ui string }{
		{"us", "https://us.posthog.com"},
		{"eu", "https://eu.posthog.com"},
	} {
		t.Run(tc.region, func(t *testing.T) {
			p := newPlugin(t, Config{Key: "phc_k000000000000000000000", Region: tc.region})
			if !bytes.Contains(p.js, []byte(`"uiHost":"`+tc.ui+`"`)) {
				t.Fatalf("rendered bootstrap does not name ui_host %s", tc.ui)
			}
		})
	}
}

// The rendered config is exactly the encoding/json encoding of this
// instance's bootConfig, for both DNT values — field order included.
func TestBootConfigRenderedExactly(t *testing.T) {
	for _, dnt := range []bool{false, true} {
		t.Run(fmt.Sprint(dnt), func(t *testing.T) {
			p := newPlugin(t, Config{
				Key:        "phc_rendered000000000000000",
				RespectDNT: dnt,
			})
			want, err := json.Marshal(bootConfig{
				APIKey:     "phc_rendered000000000000000",
				Mount:      relay.DefaultPath,
				UIHost:     "https://us.posthog.com",
				RespectDNT: dnt,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(p.js, want) {
				t.Fatalf("rendered bootstrap missing config %s", want)
			}
		})
	}
}

// A Key containing </script> must stay inert in the served bytes:
// encoding/json HTML-escapes it, so the closing-tag sequence never
// appears raw.
func TestRenderedKeyStaysInert(t *testing.T) {
	evil := "phc_</script><script>alert(1)</script>"
	p := newPlugin(t, Config{Key: evil})
	if bytes.Contains(p.js, []byte("</script>")) {
		t.Fatal("rendered bootstrap contains a raw </script>")
	}
	if !bytes.Contains(p.js, []byte(`\u003c`)) {
		t.Fatal("rendered bootstrap lacks the escaped form; key was not JSON-encoded")
	}
	want, _ := json.Marshal(bootConfig{
		APIKey:     evil,
		Mount:      relay.DefaultPath,
		UIHost:     "https://us.posthog.com",
		RespectDNT: false,
	})
	if !bytes.Contains(p.js, want) {
		t.Fatal("rendered bootstrap does not contain the JSON-encoded key")
	}
}

// Session replay raises the ingestion route's body cap to 64 MiB: a
// >8 MiB beacon is accepted and forwarded in full. Default config: the
// same beacon 413s before dialing upstream.
func TestSessionReplayBodyCap(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 9<<20) // 9 MiB > the 8 MiB default

	t.Run("replay", func(t *testing.T) {
		up := newFakeUpstream(t)
		_, srv := bootApp(t, Config{
			Key:           "phc_k000000000000000000000",
			SessionReplay: true,
		}, up.srv.URL, up.srv.URL)
		resp, err := http.Post(srv.URL+relay.DefaultPath+"/ph/e", "application/json", bytes.NewReader(big))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("9 MiB POST with SessionReplay = %d, want 200", resp.StatusCode)
		}
		if got := up.received(); got != int64(len(big)) {
			t.Fatalf("upstream received %d bytes, want %d", got, len(big))
		}
	})

	t.Run("default-413", func(t *testing.T) {
		up := newFakeUpstream(t)
		_, srv := bootApp(t, testConfig(), up.srv.URL, up.srv.URL)
		resp, err := http.Post(srv.URL+relay.DefaultPath+"/ph/e", "application/json", bytes.NewReader(big))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("9 MiB POST by default = %d, want 413", resp.StatusCode)
		}
		if n := len(up.requests()); n != 0 {
			t.Fatalf("upstream saw %d requests, want 0 (cap enforced pre-dial)", n)
		}
	})
}

// --- Attach ----------------------------------------------------------------

type stubHost struct {
	got string
	err error
}

func (s *stubHost) RegisterExternalScript(src string) error {
	s.got = src
	return s.err
}

// Attach is sugar for RegisterExternalScript(p.ScriptURL()) and
// propagates the host's error untouched.
func TestAttachRegistersAndPropagates(t *testing.T) {
	p := newPlugin(t, testConfig())

	ok := &stubHost{}
	if err := p.Attach(ok); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if ok.got != p.ScriptURL() {
		t.Fatalf("registered %q, want %q", ok.got, p.ScriptURL())
	}

	boom := errors.New("boom")
	bad := &stubHost{err: boom}
	if err := p.Attach(bad); err != boom {
		t.Fatalf("Attach error = %v, want %v", err, boom)
	}
}

func TestSelfHostRoutesEverything(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("self:" + r.URL.Path))
	}))
	t.Cleanup(up.Close)

	p, srv := bootApp(t, Config{Key: "phc_x", SelfHost: up.URL}, "", "")

	for _, tail := range []string{"/ph/e/batch", "/ph-assets/static/array.js"} {
		res, err := http.Get(srv.URL + p.Base() + tail)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "self:") {
			t.Fatalf("%s: code=%d body=%q, want proxied to self-host", tail, res.StatusCode, body)
		}
	}
	if !strings.Contains(string(p.js), up.URL) {
		t.Fatal("bootstrap does not carry the self-host origin as ui_host")
	}
}

func TestSelfHostExcludesRegion(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("SelfHost+Region did not panic")
		}
	}()
	New(Config{Key: "phc_x", SelfHost: "http://127.0.0.1:8000", Region: "eu"})
}

func TestServedBootHasNoPlaceholder(t *testing.T) {
	p := New(testConfig())
	if bytes.Contains(p.js, []byte(configPlaceholder)) {
		t.Fatal("served boot.js still contains the config placeholder")
	}
	if !bytes.Contains(p.js, []byte(`var CFG = {"apiKey"`)) {
		t.Fatal("config JSON did not land at the code position (var CFG = ...)")
	}
}
