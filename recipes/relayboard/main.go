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
// Every visible surface composes framework/ui and core-ui/app: the app
// ships zero CSS and zero hand-rolled structural markup. Identity comes
// from theme tokens (ui/theme Overrides), never from local styles.
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

// ─── chrome: header + footer ───────────────────────────────────────

// siteHeader is the ctx-aware page header: the nav is the same for
// everyone, the action cluster switches between "Sign in" and sign-out
// on the session that auth.SessionMiddleware put in the context.
func siteHeader(ctx context.Context) render.HTML {
	nav := []ui.SiteHeaderLink{
		{Label: "Pricing", Href: "/pricing"},
		{Label: "Beta", Href: "/beta"},
	}
	var actions render.HTML
	if u, ok := handler.GetUser(ctx); ok && u != nil {
		nav = append(nav, ui.SiteHeaderLink{Label: "Account", Href: "/account"})
		actions = ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM, Align: ui.AlignCenter, NoWrap: true},
			ui.SignOut(ui.SignOutConfig{Next: "/", Ctx: ctx}),
			ui.ThemeToggle(ui.ThemeToggleConfig{Variant: ui.ThemeToggleIcon}),
		)
	} else {
		actions = ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM, Align: ui.AlignCenter, NoWrap: true},
			ui.LinkButton(ui.LinkButtonConfig{Label: "Sign in", Href: "/account", Variant: ui.ButtonSecondary, Size: ui.ButtonSizeSmall}),
			ui.ThemeToggle(ui.ThemeToggleConfig{Variant: ui.ThemeToggleIcon}),
		)
	}
	return ui.SiteHeader(ui.SiteHeaderConfig{
		Brand:    ui.Link(ui.LinkConfig{Href: "/", Text: "RelayBoard"}),
		NavItems: nav,
		Drawer:   ui.SiteHeaderDrawerSheet,
		Actions:  actions,
		Ctx:      ctx,
	})
}

func siteFooter() render.HTML {
	return ui.SiteFooter(ui.SiteFooterConfig{
		Lead: ui.Link(ui.LinkConfig{Href: "/", Text: "RelayBoard"}),
		Columns: []ui.SiteFooterColumn{
			{Title: "Product", Links: []ui.SiteFooterLink{
				{Label: "Pricing", Href: "/pricing"},
				{Label: "Beta", Href: "/beta"},
			}},
			{Title: "Account", Links: []ui.SiteFooterLink{
				{Label: "Sign in", Href: "/account"},
			}},
		},
	})
}

// ─── screens ───────────────────────────────────────────────────────

type landing struct{}

func (*landing) ScreenTitle() string { return "" }
func (*landing) Render() render.HTML {
	return render.Join(
		ui.Hero(ui.HeroConfig{
			// The A/B script below swaps this heading per variant of the
			// hero-copy-test flag; this is the control copy.
			Title:    "RelayBoard",
			Subtitle: "Dashboards that never leak a byte to a third party.",
			Actions: []render.HTML{
				ui.LinkButton(ui.LinkButtonConfig{Label: "See pricing", Href: "/pricing"}),
				ui.LinkButton(ui.LinkButtonConfig{Label: "Sign up", Href: "/account", Variant: ui.ButtonSecondary}),
			},
		}),
		ui.Section(ui.SectionConfig{Heading: "Why RelayBoard"},
			ui.Grid(ui.GridConfig{Min: "14rem"},
				ui.Card(ui.CardConfig{Heading: "First-party wire",
					Description: "Analytics scripts and beacons ride your own origin through the relay. The strict CSP stays exactly as shipped."}),
				ui.Card(ui.CardConfig{Heading: "Real identity",
					Description: "Sign-ups merge the anonymous visitor into the person your vendor already tracked. No manual stitching."}),
				ui.Card(ui.CardConfig{Heading: "Server-side gates",
					Description: "Feature flags answer before the page renders, through a forty-line stdlib adapter. No flash of the wrong page."}),
			),
		),
	)
}

type pricing struct{}

func (*pricing) ScreenTitle() string { return "Pricing" }
func (*pricing) Render() render.HTML {
	return render.Join(
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    "Pricing",
			Subtitle: "Every plan keeps your traffic on your origin.",
		}),
		// data-buy and data-price are what the A/B script reads when a
		// visitor converts; the attributes are the whole contract. Plan
		// copy rides the card BODY (not Description) so the body's
		// flex-stretch pins every footer to the card's bottom edge and
		// the three buy rows align across the grid.
		ui.Grid(ui.GridConfig{Min: "15rem"},
			ui.Card(ui.CardConfig{Heading: "Free", HeadingLevel: 2},
				html.Paragraph(html.TextConfig{}, render.Text("Relay one vendor. Forever free."))),
			ui.Card(ui.CardConfig{Heading: "Pro — $19/mo", HeadingLevel: 2,
				Footer: ui.Button(ui.ButtonConfig{Label: "Buy Pro", ExtraAttrs: html.Attrs{"data-buy": "pro", "data-price": "19"}})},
				html.Paragraph(html.TextConfig{}, render.Text("Unlimited vendors, session replay, priority relay."))),
			ui.Card(ui.CardConfig{Heading: "Team — $49/mo", HeadingLevel: 2,
				Footer: ui.Button(ui.ButtonConfig{Label: "Buy Team", ExtraAttrs: html.Attrs{"data-buy": "team", "data-price": "49"}})},
				html.Paragraph(html.TextConfig{}, render.Text("Everything in Pro, five seats."))),
		),
	)
}

// account renders per request: the signed-in view shows who the session
// belongs to (the same identity the whoami endpoint hands posthog-js),
// the anonymous view offers the two battery/auth forms. Both forms POST
// to the core plugin's routes and work before any script loads; the
// register/login handlers auto-login and redirect (303) with the
// session cookie set.
type account struct{}

func (*account) ScreenTitle() string { return "Account" }
func (a *account) Render() render.HTML {
	return a.RenderCtx(context.Background())
}
func (*account) RenderCtx(ctx context.Context) render.HTML {
	if u, ok := handler.GetUser(ctx); ok && u != nil {
		items := []ui.DetailItem{}
		if eu, ok := u.(interface{ GetEmail() string }); ok {
			items = append(items, ui.DetailItem{Label: "Email", Value: render.Text(eu.GetEmail())})
		}
		if iu, ok := u.(interface{ GetID() string }); ok {
			items = append(items, ui.DetailItem{Label: "User id", Value: render.Text(iu.GetID())})
		}
		return render.Join(
			ui.PageHeader(ui.PageHeaderConfig{Title: "Account",
				Subtitle: "This is the identity PostHog sees: the whoami endpoint returns the same id."}),
			ui.Card(ui.CardConfig{Heading: "Signed in", HeadingLevel: 2,
				Footer: ui.SignOut(ui.SignOutConfig{Next: "/", Ctx: ctx})},
				ui.DetailList(ui.DetailListConfig{Items: items}),
			),
		)
	}
	emailField := func(id, autocomplete string) render.HTML {
		return ui.FormField(ui.FormFieldConfig{Label: "Email", For: id, Required: true,
			Input: html.Input(html.InputConfig{Type: "email", Name: "email", ID: id,
				ExtraAttrs: html.Attrs{"required": "", "autocomplete": autocomplete}})})
	}
	passwordField := func(id, autocomplete string) render.HTML {
		return ui.FormField(ui.FormFieldConfig{Label: "Password", For: id, Required: true,
			Input: html.Input(html.InputConfig{Type: "password", Name: "password", ID: id,
				ExtraAttrs: html.Attrs{"required": "", "autocomplete": autocomplete}})})
	}
	return render.Join(
		ui.PageHeader(ui.PageHeaderConfig{Title: "Account",
			Subtitle: "Registering merges your anonymous analytics profile into a real person."}),
		ui.Grid(ui.GridConfig{Min: "18rem"},
			ui.Card(ui.CardConfig{Heading: "Create an account", HeadingLevel: 2},
				ui.Form(ui.FormConfig{Action: "/auth/register", SubmitLabel: "Create account", Ctx: ctx},
					emailField("reg-email", "email"),
					passwordField("reg-password", "new-password"),
				),
			),
			ui.Card(ui.CardConfig{Heading: "Log in", HeadingLevel: 2},
				ui.Form(ui.FormConfig{Action: "/auth/login", SubmitLabel: "Log in", Ctx: ctx},
					emailField("login-email", "email"),
					passwordField("login-password", "current-password"),
				),
			),
		),
	)
}

// ─── the server-side gate ──────────────────────────────────────────

// beta is the gated screen. Screens render server-side on every
// request, so RenderCtx asks the flag store — through the enabled
// closure newApp fills in once the framework app exists — and only the
// winning branch ever reaches the browser. The data-beta marker is the
// contract the tests and the README's curl examples read.
type beta struct {
	enabled func(ctx context.Context) bool
}

func (*beta) ScreenTitle() string { return "Beta" }
func (b *beta) Render() render.HTML {
	return b.RenderCtx(context.Background())
}
func (b *beta) RenderCtx(ctx context.Context) render.HTML {
	if b.enabled != nil && b.enabled(ctx) {
		return render.Join(
			ui.PageHeader(ui.PageHeaderConfig{Title: "Welcome to the beta", Eyebrow: "Beta"}),
			html.Div(html.DivConfig{ExtraAttrs: html.Attrs{"data-beta": "yes"}},
				ui.Callout(ui.CalloutConfig{Title: "You're in", Variant: ui.StatusSuccess},
					render.Text("PostHog said yes to this user: the render was gated server-side, before any HTML left the process."),
				),
			),
		)
	}
	return render.Join(
		ui.PageHeader(ui.PageHeaderConfig{Title: "Beta is invite-only", Eyebrow: "Beta"}),
		html.Div(html.DivConfig{ExtraAttrs: html.Attrs{"data-beta": "no"}},
			ui.Callout(ui.CalloutConfig{Title: "Not yet", Variant: ui.StatusNeutral},
				render.Text("The beta-access flag decided this server-side, before the page rendered. Sign in and ask PostHog nicely."),
			),
		),
	)
}

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
// annotation it reads — the store and the beta screen then agree on the
// subject because both look at the same context value.
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
    var h1 = document.querySelector('.ui-hero__title');
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
	// Identity lives in theme tokens, never in CSS the app would ship.
	uiApp.WithTheme(uitheme.Default(uitheme.Overrides{
		Primary:    "#0F766E",
		DarkColors: map[string]string{"primary": "#5EEAD4"},
	}))
	layout := appui.NewLayout("site").
		WithContainer().
		WithHeader(appui.NewContextComponent(siteHeader)).
		WithFooter(appui.NewStaticComponent(siteFooter()))
	uiApp.SetDefaultLayout(layout)
	// Screens register as pointers (&screen{}): the host resolves them
	// through dependency injection, and a value screen fails that
	// resolution — every page then falls through to the host's
	// not-found, which looks like a routing bug and isn't.
	gate := &beta{}
	uiApp.Register("/", &landing{}, layout)
	uiApp.Register("/pricing", &pricing{}, layout)
	uiApp.Register("/account", &account{}, layout)
	uiApp.Register("/beta", gate, layout)

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
	// one link later and adds the flag subject; the beta screen and the
	// whoami endpoint read both.
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

	// The screen was registered before fw existed; requests only start
	// after Start, so filling the closure here is not a race.
	gate.enabled = func(ctx context.Context) bool {
		return fw.IsEnabled(ctx, "beta-access")
	}

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
