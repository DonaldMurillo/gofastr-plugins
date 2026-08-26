// Command relayboard is the analytics recipe: a three-screen product
// whose funnel is measured end to end through this repository's posthog
// integration — campaign attribution on the landing page, identified
// users from real accounts, an A/B-tested hero, and a server-side gate
// that asks PostHog per request.
//
// It is the runnable half of posthog/README.md: every analytics behavior
// described there happens in this app, against a self-hosted PostHog,
// with nothing invented locally.
//
//   - The wire is first-party. The browser only ever talks to this
//     origin: posthog-js loads through the relay, beacons go through it,
//     and the strict default CSP needs no exceptions.
//   - Identity is real. battery/auth backs the integration's whoami
//     endpoint, so an anonymous visitor is a person without an id, and
//     registering or logging in merges that visitor into the person
//     PostHog already tracked anonymously.
//   - Flags gate server-side. /beta asks PostHog, per request, through a
//     featureflag.Store that is forty lines of stdlib HTTP.
//   - Degradation is clean. With POSTHOG_KEY unset the app runs the
//     same: no plugin, no flag store, /beta answers invite-only, and
//     the A/B script no-ops because window.posthog never appears.
//
// Run with:
//
//	go run ./recipes/relayboard
//
// Then open http://localhost:8099/. ADDR pins the address, RELAYBOARD_DB
// the sqlite file, POSTHOG_KEY and POSTHOG_HOST the analytics wiring.
// See README.md for the PostHog setup.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/DonaldMurillo/gofastr-plugins/posthog"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core-ui/html"
	"github.com/DonaldMurillo/gofastr/core/featureflag"
	"github.com/DonaldMurillo/gofastr/core/handler"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/ui"
	uitheme "github.com/DonaldMurillo/gofastr/framework/ui/theme"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
	_ "github.com/DonaldMurillo/gofastr/sqlite/stdlib"
)

// ─── configuration ─────────────────────────────────────────────────

// config is everything main reads from the environment. newApp takes it
// as a value so the tests can build the app against a fake PostHog
// without touching the process environment.
type config struct {
	addr        string
	dbPath      string
	postHogKey  string
	postHogHost string
}

// configFromEnv reads the environment. A POSTHOG_KEY of "" selects the
// degraded mode: same app, no analytics.
func configFromEnv() config {
	host := os.Getenv("POSTHOG_HOST")
	if host == "" {
		// A self-hosted PostHog on the same machine. Loopback http is
		// the only plaintext the relay accepts, so the default is legal
		// by construction. The docker hobby deploy listens on :8000.
		host = "http://localhost"
	}
	return config{
		addr:        envDefault("ADDR", ":8099"),
		dbPath:      envDefault("RELAYBOARD_DB", filepath.Join(os.TempDir(), "relayboard.db")),
		postHogKey:  os.Getenv("POSTHOG_KEY"),
		postHogHost: host,
	}
}

func envDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// openDB opens sqlite through gofastr's pure-Go driver — the same
// posture as recipes/blogapp: no cgo toolchain, no new module
// dependency. The :memory: pool cap is that recipe's lesson: each pooled
// connection would otherwise get its own private database.
func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	return db, nil
}

// ─── screens ───────────────────────────────────────────────────────

type landing struct{}

func (landing) ScreenTitle() string { return "" }
func (landing) Render() render.HTML {
	return ui.Hero(ui.HeroConfig{
		// The A/B script below swaps this heading per variant of the
		// hero-copy-test flag; this is the control copy.
		Title:    "RelayBoard",
		Subtitle: "Dashboards that never leak a byte to a third party.",
		Actions: []render.HTML{
			ui.LinkButton(ui.LinkButtonConfig{Label: "See pricing", Href: "/pricing"}),
			ui.LinkButton(ui.LinkButtonConfig{Label: "Sign up", Href: "/account", Variant: ui.ButtonSecondary}),
		},
	})
}

type pricing struct{}

func (pricing) ScreenTitle() string { return "Pricing" }
func (pricing) Render() render.HTML {
	return render.Join(
		ui.PageHeader(ui.PageHeaderConfig{Title: "Pricing"}),
		ui.Card(ui.CardConfig{Heading: "Free", Description: "Relay one vendor. Forever free."}),
		// data-buy and data-price are what the A/B script reads when a
		// visitor converts; the attributes are the whole contract.
		ui.Card(ui.CardConfig{Heading: "Pro — $19/mo", Description: "Unlimited vendors, session replay, priority relay.",
			Footer: ui.Button(ui.ButtonConfig{Label: "Buy Pro", ExtraAttrs: html.Attrs{"data-buy": "pro", "data-price": "19"}})}),
		ui.Card(ui.CardConfig{Heading: "Team — $49/mo", Description: "Everything in Pro, five seats.",
			Footer: ui.Button(ui.ButtonConfig{Label: "Buy Team", ExtraAttrs: html.Attrs{"data-buy": "team", "data-price": "49"}})}),
	)
}

type account struct{}

func (account) ScreenTitle() string { return "Account" }
func (account) Render() render.HTML {
	// Plain forms posting to battery/auth's core plugin routes. No
	// JavaScript: the page works before any script loads, and the
	// register/login forms auto-login and redirect (303) with the
	// session cookie set.
	return render.Join(
		ui.PageHeader(ui.PageHeaderConfig{Title: "Account"}),
		render.HTML(`<div data-account-panel>
<form action="/auth/register" method="POST" class="ui-stack">
  <h2>Sign up</h2>
  <label>Email <input name="email" type="email" required></label>
  <label>Password <input name="password" type="password" required></label>
  <button type="submit">Create account</button>
</form>
<form action="/auth/login" method="POST" class="ui-stack">
  <h2>Log in</h2>
  <label>Email <input name="email" type="email" required></label>
  <label>Password <input name="password" type="password" required></label>
  <button type="submit">Log in</button>
</form>
<form action="/auth/logout" method="POST"><button type="submit">Log out</button></form>
</div>`),
	)
}

// ─── the server-side gate ──────────────────────────────────────────

// The /beta page is a plain handler, not a screen. A screen renders the
// same HTML for every request, and the whole point of this page is that
// the server decides per request, from the flag store, which branch to
// send. The markup stays minimal: the interesting part is which half
// renders, and that is PostHog's call.
const betaInviteOnly = `<!doctype html>
<html><head><meta charset="utf-8"><title>Beta</title></head>
<body><h1 data-beta="no">Beta is invite-only</h1>
<p>The beta-access flag decided this server-side, before the page rendered.</p></body></html>`

const betaWelcome = `<!doctype html>
<html><head><meta charset="utf-8"><title>Beta</title></head>
<body><h1 data-beta="yes">Welcome to the beta</h1>
<p>PostHog said yes to this user: the render was gated server-side.</p></body></html>`

// phFlagStore is the featureflag.Store adapter from gofastr's
// analytics-recipes doc, running for real. It answers "is this flag on
// for this subject" by asking the self-hosted PostHog directly, with
// nothing but stdlib HTTP.
//
// The endpoint is POST {host}/flags/?v=2, the local-evaluation flags
// call. Not /decide: current self-hosted PostHog answers /decide with
// 403, and the flags endpoint is the supported replacement.
//
// Three contract points, each matching featureflag's Store docs:
//
//   - an unknown key returns (nil, nil): "provably absent", which
//     preserves BoolDefault's fallback semantics,
//   - any error returns the error: the evaluator fails closed (Bool
//     answers false) rather than guessing,
//   - a known flag comes back with Rollout 100 so the evaluator
//     reproduces the vendor's decision instead of re-bucketing the
//     subject itself.
type phFlagStore struct {
	host string // the self-hosted PostHog origin
	key  string // the public project API key, phc_...
}

func (s phFlagStore) Get(ctx context.Context, key string) (*featureflag.Flag, error) {
	// The subject is whoever flagContextMiddleware put in the context:
	// the authenticated user's id, or a shared "anonymous" id. For a
	// percentage flag every anonymous visitor therefore shares one
	// vendor-side bucket — the vendor's semantics, kept intact.
	ec := featureflag.FromContext(ctx)
	subject := ec.UserID
	if subject == "" {
		subject = "anonymous"
	}
	body, _ := json.Marshal(map[string]any{"api_key": s.key, "distinct_id": subject})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.host+"/flags/?v=2", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Flags map[string]struct {
			Enabled bool `json:"enabled"`
		} `json:"flags"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	v, found := out.Flags[key]
	if !found {
		return nil, nil
	}
	return &featureflag.Flag{Key: key, Enabled: v.Enabled, Rollout: 100}, nil
}

// flagContextMiddleware annotates every request with the EvalContext
// the flag store reads: the authenticated user's id when there is one,
// empty otherwise. It must run after auth.SessionMiddleware, whose
// annotation it reads — the store and /beta then agree on the subject
// because both look at the same context value.
func flagContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ec featureflag.EvalContext
		if u, ok := handler.GetUser(r.Context()); ok && u != nil {
			if au, ok := u.(auth.User); ok {
				ec.UserID = au.GetID()
			}
		}
		next.ServeHTTP(w, r.WithContext(featureflag.WithContext(r.Context(), ec)))
	})
}

// ─── the A/B and purchase script ───────────────────────────────────

// abJS is the page half of the client-side work: apply the hero-copy
// variant once flags load (and again after each client-side
// navigation), and capture a purchase when a data-buy button is
// clicked. getFeatureFlag records the $feature_flag_called exposure
// PostHog's experiment analysis keys on, so there is no manual capture
// for the A/B side.
//
// It is served as an external script (ScriptHandler +
// RegisterExternalScript), never inlined into the page: the host's
// default CSP has no script-src exceptions to inline under, and
// ScriptHandler gives it an ETag and an immutable ?v= for free.
//
// Every PostHog touch is guarded, which is the degradation contract:
// without POSTHOG_KEY there is no bootstrap, window.posthog never
// exists, and this script is a no-op.
var abJS = []byte(`(function () {
  'use strict';
  function applyVariant() {
    var ph = window.posthog;
    if (!ph || !ph.getFeatureFlag) return;
    var v = ph.getFeatureFlag('hero-copy-test');
    if (!v || location.pathname !== '/') return;
    var h1 = document.querySelector('h1');
    if (!h1) return;
    h1.textContent = v === 'punchy' ? 'Ship analytics without leaving your origin' : 'RelayBoard';
    h1.setAttribute('data-ab-variant', v);
  }
  var t = setInterval(function () {
    if (window.posthog && window.posthog.onFeatureFlags) {
      clearInterval(t);
      window.posthog.onFeatureFlags(applyVariant);
      applyVariant();
    }
  }, 200);
  window.addEventListener('gofastr:navigate', function () { setTimeout(applyVariant, 100); });
  document.addEventListener('click', function (e) {
    var b = e.target && e.target.closest && e.target.closest('[data-buy]');
    if (!b || !window.posthog) return;
    window.posthog.capture('purchase', {
      plan: b.getAttribute('data-buy'),
      value: Number(b.getAttribute('data-price')),
    });
    b.textContent = 'Purchased ✓';
  });
})();`)

// ─── wiring ────────────────────────────────────────────────────────

// relayboard is the handle newApp returns: what callers need beyond the
// router. phMount is the posthog mount ("/__gofastr/t" by default), or
// "" when analytics is off.
type relayboard struct {
	phMount string
}

// newApp builds the whole application ready to serve. Shared by main
// and the tests so both exercise identical wiring.
//
// cfg.postHogKey == "" is the degraded mode and nothing branches on it
// except the analytics wiring: the screens, accounts, and gate route
// are the same app either way.
func newApp(db *sql.DB, cfg config) (*framework.App, *relayboard, error) {
	uiApp := appui.NewApp("RelayBoard")
	uiApp.WithTheme(uitheme.Default())
	layout := appui.NewLayout("site").WithContainer()
	uiApp.SetDefaultLayout(layout)
	// Screens register as pointers (&screen{}): the host resolves them
	// through dependency injection, and a value screen fails that
	// resolution — every page then falls through to the host's
	// not-found, which looks like a routing bug and isn't.
	uiApp.Register("/", &landing{}, layout)
	uiApp.Register("/pricing", &pricing{}, layout)
	uiApp.Register("/account", &account{}, layout)

	host := uihost.New(uiApp)
	fw := framework.NewUIHostApp(host,
		framework.WithDB(db),
		framework.WithConfig(framework.AppConfig{Name: "relayboard"}),
	)

	// Real accounts on the app's own sqlite: durable users and sessions
	// through battery/auth's entity stores (the manager creates the
	// tables), with the core plugin supplying /auth/register, /auth/
	// login, /auth/logout. DevMode mints a per-process cookie key and
	// keeps cookies http-friendly for a local demo; production wants a
	// real JWTSecret and Secure cookies.
	mgr := auth.New(auth.AuthConfig{
		DevMode:      true,
		UserStore:    auth.NewEntityUserStore(db, "auth_users"),
		SessionStore: auth.NewEntitySessionStore(db, "auth_sessions"),
	})
	mgr.Use(auth.NewCorePlugin())
	if err := mgr.Init(nil); err != nil {
		return nil, nil, err
	}
	mgr.RegisterRoutes(fw.Router())

	// Order matters. SessionMiddleware only annotates the context with
	// the logged-in user; flagContextMiddleware reads that annotation
	// one link later and adds the flag subject; /beta and the whoami
	// endpoint read both.
	fw.Use(auth.SessionMiddleware(mgr), flagContextMiddleware)

	rb := &relayboard{}
	if cfg.postHogKey != "" {
		ph := posthog.New(posthog.Config{Key: cfg.postHogKey, SelfHost: cfg.postHogHost})
		fw.RegisterPlugin(ph)
		// The bootstrap rides the host like any external script: one
		// script tag per page, same-origin, CSP-clean.
		if err := ph.Attach(host); err != nil {
			return nil, nil, err
		}
		// The vendor store replaces the default before any request can
		// touch the evaluator. SetFlagStore panics if the lazy default
		// has already fired, and that guard is wanted here: a store
		// swapped in after decisions were made is a silent race, not a
		// configuration.
		fw.SetFlagStore(phFlagStore{host: cfg.postHogHost, key: cfg.postHogKey})
		rb.phMount = ph.Base()
	}
	// Without a key no store is set at all. The first IsEnabled then
	// lazily wires the framework's empty in-memory default, every key
	// answers false, and /beta renders invite-only. No panic path.

	fw.Router().Get("/beta", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if fw.IsEnabled(r.Context(), "beta-access") {
			fmt.Fprint(w, betaWelcome)
			return
		}
		fmt.Fprint(w, betaInviteOnly)
	}))

	fw.Router().Get("/__site/ab.js", uihost.ScriptHandler(abJS))
	if err := host.RegisterExternalScript(uihost.ScriptURL("/__site/ab.js", abJS)); err != nil {
		return nil, nil, err
	}

	if err := fw.InitPlugins(); err != nil {
		return nil, nil, err
	}
	return fw, rb, nil
}

func main() {
	cfg := configFromEnv()
	db, err := openDB(cfg.dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	fw, rb, err := newApp(db, cfg)
	if err != nil {
		log.Fatalf("newApp: %v", err)
	}

	if rb.phMount == "" {
		log.Printf("relayboard: POSTHOG_KEY unset; running without analytics (/beta stays invite-only)")
	} else {
		log.Printf("relayboard: posthog %s through %s", cfg.postHogHost, rb.phMount)
	}
	log.Printf("relayboard: serving on %s", cfg.addr)
	log.Fatal(fw.Start(cfg.addr))
}
