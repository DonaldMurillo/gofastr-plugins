package main

// HTTP-level smoke tests over the real app builder, newApp: the real
// router, the real middleware chain, the real plugin registration. No
// browser — the pins here (routes, the gate, the identity chain) are all
// observable over plain HTTP against a fake PostHog. Driving the
// JavaScript side needs a real browser and a real PostHog; see the
// bot-detection note in README.md for why that is not this suite.

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/DonaldMurillo/gofastr/sqlite/stdlib"
)

// testDB opens a fresh sqlite database in the test's temp dir.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "relayboard.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// serve builds the app through newApp and serves it, exactly the way
// main does, minus the env round-trip.
func serve(t *testing.T, cfg config) (*httptest.Server, *relayboard) {
	t.Helper()
	db := testDB(t)
	fw, rb, err := newApp(db, cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	srv := httptest.NewServer(fw.Router())
	t.Cleanup(srv.Close)
	return srv, rb
}

// fakePostHog stands in for a self-hosted PostHog: it answers the v2
// local-evaluation flags endpoint the way the real one does for a
// enabled beta-access flag, and 200 for anything else the bootstrap or
// SDK might ask it for.
func fakePostHog(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flags/" { // the store POSTs /flags/?v=2
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"flags":{"beta-access":{"enabled":true}}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// client keeps cookies and does NOT follow redirects, so a test can
// assert on the 303 itself and still carry the session cookie it set.
func client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func get(t *testing.T, c *http.Client, target string) (int, string) {
	t.Helper()
	resp, err := c.Get(target)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func post(t *testing.T, c *http.Client, target string, form url.Values) int {
	t.Helper()
	resp, err := c.PostForm(target, form)
	if err != nil {
		t.Fatalf("POST %s: %v", target, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestPagesServeWithoutPostHogKey(t *testing.T) {
	srv, rb := serve(t, config{}) // no key: the degraded mode
	if rb.phMount != "" {
		t.Fatal("phMount set although no POSTHOG_KEY was wired")
	}

	c := client(t)
	for _, path := range []string{"/", "/pricing", "/account", "/beta", "/__site/ab.js"} {
		t.Run(path, func(t *testing.T) {
			status, body := get(t, c, srv.URL+path)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if strings.TrimSpace(body) == "" {
				t.Fatal("empty body")
			}
		})
	}

	// No flag store was wired, so the framework's lazily-created empty
	// default answers, every key is false, and /beta must say
	// invite-only rather than error or panic.
	_, body := get(t, c, srv.URL+"/beta")
	if !strings.Contains(body, `data-beta="no"`) {
		t.Fatalf("degraded /beta did not render the invite-only branch")
	}
}

func TestGateAndBootstrapWithFakePostHog(t *testing.T) {
	key := "phc_0000000000000000000000000000000000000000"
	ph := fakePostHog(t)
	srv, rb := serve(t, config{postHogKey: key, postHogHost: ph.URL})
	if rb.phMount == "" {
		t.Fatal("phMount empty although a key was wired")
	}

	c := client(t) // fresh jar: an anonymous visitor

	// whoami answers anonymous exactly: {"id":null}, never a guess and
	// never an error, because nothing about identity is configurable
	// from the browser.
	status, body := get(t, c, srv.URL+rb.phMount+"/whoami")
	if status != http.StatusOK {
		t.Fatalf("whoami status = %d, want 200", status)
	}
	var who struct {
		ID any `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &who); err != nil {
		t.Fatalf("whoami body %q: %v", body, err)
	}
	if who.ID != nil {
		t.Fatalf("anonymous whoami id = %v, want null", who.ID)
	}

	// The bootstrap is served by this origin with the key baked into its
	// bytes (that is how the config reaches it: no script attributes).
	status, body = get(t, c, srv.URL+rb.phMount+"/boot.js")
	if status != http.StatusOK {
		t.Fatalf("boot.js status = %d, want 200", status)
	}
	if !strings.Contains(body, key) {
		t.Fatalf("boot.js does not carry the project key")
	}

	// The gate asked the fake PostHog and rendered the welcome branch.
	// This pins the whole chain: flagContextMiddleware supplied the
	// subject, phFlagStore POSTed /flags/?v=2, the evaluator reproduced
	// the vendor's enabled:true, and the handler branched on it.
	status, body = get(t, c, srv.URL+"/beta")
	if status != http.StatusOK {
		t.Fatalf("/beta status = %d, want 200", status)
	}
	if !strings.Contains(body, `data-beta="yes"`) {
		t.Fatalf("gated /beta did not render the welcome branch")
	}
}

func TestRegisterPinsWhoamiIdentity(t *testing.T) {
	ph := fakePostHog(t)
	srv, rb := serve(t, config{
		postHogKey:  "phc_0000000000000000000000000000000000000000",
		postHogHost: ph.URL,
	})

	c := client(t)
	// The account screen's form, posted the way a browser posts it.
	// 303: form register auto-logins and redirects, session cookie set.
	status := post(t, c, srv.URL+"/auth/register", url.Values{
		"email":    {"demo@relayboard.test"},
		"password": {"demo-password-1"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("register status = %d, want 303", status)
	}

	// whoami now returns the registered user's id. That pins the GetID
	// chain in the recipe's own context: SessionMiddleware annotated the
	// context with the battery/auth user, and the posthog integration's
	// default Identify resolved it through its GetID arm — the same id
	// the flag store uses as its subject.
	status, body := get(t, c, srv.URL+rb.phMount+"/whoami")
	if status != http.StatusOK {
		t.Fatalf("whoami status = %d, want 200", status)
	}
	var who struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &who); err != nil {
		t.Fatalf("whoami body %q: %v", body, err)
	}
	if who.ID == nil || *who.ID == "" {
		t.Fatalf("whoami id = %v after register, want the user id", who.ID)
	}
}
