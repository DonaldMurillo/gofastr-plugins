package posthog

// Real-SDK e2e scenarios for the packaged PostHog integration: the REAL
// posthog-js 1.418.17 bundle (testdata/) runs in a real headless Chrome
// against the plugin's relay, with ONE httptest server standing in for
// both relay upstreams. No network, no vendor account.
//
// What each test pins — the four bugs the live dogfooding session found,
// plus the two wire contracts they clarified:
//
//   - TestDeepURLPageviewCarriesUTM: a deep link with UTM query params
//     produces a $pageview whose $current_url keeps the query and whose
//     properties carry utm_source — first-touch attribution is automatic
//     in the real SDK, no host code needed.
//   - TestAttributionSurvivesSPAToPurchase: the ad-attribution
//     guarantee. UTM + gclid landed once on "/", then SPA navigation to
//     /pricing dropped them from the URL — the purchase event STILL
//     carries them (the SDK persists campaign params per session).
//   - TestRealAuthIdentifyMerge: a real battery/auth register form (the
//     browser CLICKS the submit button; the runtime's form interceptor
//     follows the 303), then the bootstrap's whoami refresh identifies
//     the visitor: $identify arrives with distinct_id == the sqlite
//     auth_users.id and $anon_distinct_id set — the GetID fix pinned
//     end-to-end against a real auth principal.
//   - TestBotAutomationCapturesDropped: the documented gotcha — in a
//     browser with navigator.webdriver visible (no
//     --disable-blink-features=AutomationControlled), the SDK loads
//     (the array.js fetch hits the vendor) but ZERO ingestion beacons
//     fire. Nobody should debug that blind again.
//   - TestFlagVariantExposureThroughRelay: a multivariate flag served
//     through the relayed /flags call both paints the page (the h1 gets
//     data-ab-variant="punchy") and records the $feature_flag_called
//     exposure event PostHog's experiment analysis keys on.
//   - TestIngestPathsArriveVerbatim: the ingestion request the vendor
//     receives keeps its trailing slash (/e/, /i/v0/e/) — the SDK-level
//     regression pin for the relay's trailing-slash handling.
//
// Every test is -short gated (headless Chrome) and the suite runs
// serialized: go test runs a package's tests sequentially by default and
// these deliberately do not call t.Parallel — the shared browser is
// single-tenant.

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonaldMurillo/gofastr/battery/auth"
	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework"
	uitheme "github.com/DonaldMurillo/gofastr/framework/ui/theme"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
	_ "github.com/DonaldMurillo/gofastr/sqlite/stdlib"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// e2eKey is the public project key baked into the served bootstrap for
// these tests. phc_ shape, never a secret.
const e2eKey = "phc_e2e0000000000000000000"

// fixtureJS is the pinned real posthog-js bundle, loaded once. The
// filename carries the version; the loader is the single place to bump.
var fixtureJS = sync.OnceValues(func() ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", "posthog-js-1.418.17.min.js"))
})

// ─── the fake vendor upstream ──────────────────────────────────────────

// vendorReq is one request the fake vendor received, with its ingestion
// payload decoded (see decodeBeacon).
type vendorReq struct {
	Method string
	Path   string // exactly as the vendor saw it, trailing slash included
	Query  url.Values
	Events []vendorEvent // nil for non-ingestion requests
	Body   string        // raw body, first 2 KiB, for logs
}

// vendorEvent is one decoded posthog-js event as it rides a beacon.
type vendorEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
}

// beaconEnvelope is the JSON shape posthog-js POSTs: either a bare array
// (sendBeacon paths) or the {api_key, batch, sent_at} object.
type beaconEnvelope struct {
	APIKey string        `json:"api_key"`
	Batch  []vendorEvent `json:"batch"`
}

// decodeBeacon unpacks one request body the SDK sent into its events,
// whichever of the three wire forms posthog-js 1.418 uses:
//
//   - compression=gzip-js (the default once config.js advertises it): the
//     raw body IS a gzip stream (binary; no query param marks it), or
//     form-encoded data=<base64(gzip(json))> on the sendBeacon path.
//   - compression=base64: form-encoded data=<base64(json)>; the SDK's
//     btoa(encodeURIComponent(...).unescape) round-trips to the exact
//     original UTF-8 JSON bytes.
//   - no compression: the body is JSON directly (config.js absent).
func decodeBeacon(body []byte, query url.Values) ([]vendorEvent, error) {
	payload := body
	if form, err := url.ParseQuery(string(body)); err == nil && form.Has("data") {
		if d := form.Get("data"); d != "" {
			raw, err := base64.StdEncoding.DecodeString(d)
			if err != nil {
				return nil, fmt.Errorf("base64 data param: %w", err)
			}
			payload = raw
			if query.Get("compression") == "gzip-js" || (len(payload) > 2 && payload[0] == 0x1f && payload[1] == 0x8b) {
				if zr, err := gzip.NewReader(bytes.NewReader(payload)); err == nil {
					if plain, err := io.ReadAll(zr); err == nil {
						payload = plain
					}
				}
			}
		}
	} else if len(payload) > 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("gzip body: %w", err)
		}
		if plain, err := io.ReadAll(zr); err != nil {
			return nil, fmt.Errorf("gunzip: %w", err)
		} else {
			payload = plain
		}
	}
	var env beaconEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		// Bare-array sendBeacon form.
		var events []vendorEvent
		if err2 := json.Unmarshal(payload, &events); err2 != nil {
			return nil, fmt.Errorf("beacon is neither envelope (%v) nor array (%v)", err, err2)
		}
		return events, nil
	}
	return env.Batch, nil
}

// fakeVendor is the single httptest server standing in for BOTH relay
// upstreams (assets and ingestion): serves the pinned SDK, answers the
// /flags evaluation call with a configurable variant, swallows ingestion
// beacons, and records everything it ever saw.
type fakeVendor struct {
	mu      sync.Mutex
	reqs    []vendorReq
	srv     *httptest.Server
	variant string // served flag variant; empty disables the flag
}

func newFakeVendor(t *testing.T) *fakeVendor {
	t.Helper()
	f := &fakeVendor{variant: "punchy"}
	mux := http.NewServeMux()

	// The SDK loader: the relay's ph-assets route maps /static/array.js
	// here verbatim.
	mux.HandleFunc("/static/array.js", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		js, err := fixtureJS()
		if err != nil {
			t.Errorf("fixture: %v", err)
			http.Error(w, "fixture missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(js)
	})

	// Remote config. The live wire shape: ok + config.supportedCompression
	// advertises gzip-js, which makes beacons real gzip streams — the
	// exact format the decoder above must survive.
	mux.HandleFunc("/array/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"config":{"supportedCompression":["gzip-js","base64"]}}`)
	})

	// Feature flag evaluation (POST /flags/?v=2&...). Empirically-derived
	// response shape (see the suite header comment in the report): a v2
	// object whose flags map values carry {key, enabled, variant}; the
	// SDK resolves getFeatureFlag to flag.variant ?? flag.enabled.
	mux.HandleFunc("/flags/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		w.Header().Set("Content-Type", "application/json")
		if f.variant == "" {
			fmt.Fprint(w, `{"flags":{},"errorsWhileComputingFlags":false}`)
			return
		}
		fmt.Fprintf(w, `{"flags":{"hero-copy-test":{"key":"hero-copy-test","enabled":true,"variant":%q}},"errorsWhileComputingFlags":false,"requestId":"e2e-req-1"}`, f.variant)
	})

	// Ingestion beacons: /e/ (this SDK version) and /i/v0/e/ (newer
	// versions) — recorded verbatim, answered minimally.
	ingest := func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":1}`)
	}
	mux.HandleFunc("/e/", ingest)
	mux.HandleFunc("/i/v0/e/", ingest)
	mux.HandleFunc("/i/v0/e", ingest)

	// Everything else: recorded (visible in test logs) and 404ed. If the
	// SDK grows a new endpoint, the log names it before the assertion
	// does.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		http.Error(w, "unexpected path", http.StatusNotFound)
	})

	// When a test fails, show everything the vendor ever saw — the
	// forensics for a timed-out waitFor.
	t.Cleanup(func() {
		if t.Failed() {
			dumpRequests(t, f)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// dumpRequests logs everything the vendor saw — the CI forensics when a
// waitFor times out: which paths were hit, in what order, with which
// decoded events.
func dumpRequests(t *testing.T, vendor *fakeVendor) {
	t.Helper()
	for i, q := range vendor.requests() {
		evs, _ := json.Marshal(q.Events)
		t.Logf("vendor[%d] %s %s query=%v events=%s", i, q.Method, q.Path, q.Query, evs)
	}
}

func (f *fakeVendor) record(r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	_ = r.Body.Close()
	req := vendorReq{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
	}
	preview := string(body)
	if len(preview) > 2048 {
		preview = preview[:2048]
	}
	req.Body = preview
	if strings.HasPrefix(r.URL.Path, "/e") || strings.HasPrefix(r.URL.Path, "/i/") || strings.HasPrefix(r.URL.Path, "/flags/") {
		if events, err := decodeBeacon(body, r.URL.Query()); err == nil {
			req.Events = events
		} else {
			req.Body = fmt.Sprintf("decode error: %v; body %q", err, preview)
		}
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
}

func (f *fakeVendor) requests() []vendorReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]vendorReq(nil), f.reqs...)
}

// eventsNamed returns every decoded event with that name, across all
// ingestion requests, in arrival order.
func (f *fakeVendor) eventsNamed(name string) []vendorEvent {
	var out []vendorEvent
	for _, q := range f.requests() {
		for _, e := range q.Events {
			if e.Event == name {
				out = append(out, e)
			}
		}
	}
	return out
}

// ingestionPaths returns the paths of every request that carried events
// (or hit an ingestion endpoint), for the verbatim-path assertions.
func (f *fakeVendor) ingestionPaths() []string {
	var out []string
	for _, q := range f.requests() {
		if strings.HasPrefix(q.Path, "/e") || strings.HasPrefix(q.Path, "/i/") {
			out = append(out, q.Path)
		}
	}
	return out
}

// sawPath reports whether any recorded request used exactly that path.
func (f *fakeVendor) sawPath(path string) bool {
	for _, q := range f.requests() {
		if q.Path == path {
			return true
		}
	}
	return false
}

// ─── the real test app ────────────────────────────────────────────────

// landingScreen backs "/": the h1 the A/B test paints and the link the
// SPA-navigation tests click.
type landingScreen struct{}

func (s *landingScreen) ScreenTitle() string { return "Home" }

func (s *landingScreen) Render() render.HTML {
	return render.HTML(`<h1 id="hero">Ship first-party</h1>` +
		`<p><a href="/pricing" id="nav-pricing">Pricing</a></p>`)
}

// pricingScreen backs "/pricing": the purchase button the page script
// captures on click.
type pricingScreen struct{}

func (s *pricingScreen) ScreenTitle() string { return "Pricing" }

func (s *pricingScreen) Render() render.HTML {
	return render.HTML(`<h1>Pricing</h1>` +
		`<button type="button" data-buy="pro" id="buy-pro">Buy Pro</button>`)
}

// registerScreen backs "/register": a real form posting to battery/auth's
// register handler. The browser fills it and CLICKS the button —
// chromedp.Submit does not fire the form's submit path, a click on the
// real button does.
type registerScreen struct{}

func (s *registerScreen) ScreenTitle() string { return "Create account" }

func (s *registerScreen) Render() render.HTML {
	return render.HTML(`<h1>Create account</h1>` +
		`<form method="post" action="/auth/register">` +
		`<input type="email" name="email" id="reg-email" autocomplete="email" required>` +
		`<input type="password" name="password" id="reg-password" autocomplete="new-password" required>` +
		`<button type="submit" id="register-submit">Create account</button>` +
		`</form>`)
}

// pageScriptJS is the host page script served the CSP-clean way (external,
// via uihost.ScriptHandler + RegisterExternalScript — the strict default
// CSP forbids inline). It carries the two page-side behaviors the README
// documents: purchase capture on [data-buy] click, and the A/B snippet
// that paints the assigned variant onto the h1. window.posthog appears
// only once array.js executes (boot.js loads it async), so the flag part
// polls — the same shape a host's own script would need.
const pageScriptJS = `(function () {
  'use strict';
  document.addEventListener('click', function (e) {
    var t = e.target;
    var el = t && t.closest ? t.closest('[data-buy]') : null;
    if (el && window.posthog && window.posthog.capture) {
      window.posthog.capture('purchase', { plan: el.getAttribute('data-buy') });
    }
  });
  function whenPosthog(fn) {
    if (window.posthog) return fn();
    setTimeout(function () { whenPosthog(fn); }, 50);
  }
  function exposeVariant() {
    whenPosthog(function () {
      window.posthog.onFeatureFlags(function () {
        var v = window.posthog.getFeatureFlag('hero-copy-test');
        var h1 = document.querySelector('h1');
        if (h1) h1.setAttribute('data-ab-variant', v === undefined || v === null ? 'off' : String(v));
      });
    });
  }
  exposeVariant();
  window.addEventListener('gofastr:navigate', exposeVariant);
})();`

// e2eApp is one running real gofastr app: uihost + appui screens, the
// posthog plugin relayed at the fake vendor, battery/auth on sqlite, and
// the page script — the full production shape, per test.
type e2eApp struct {
	base string
	db   *sql.DB
}

func newE2EApp(t *testing.T, vendor *fakeVendor) *e2eApp {
	t.Helper()

	// sqlite file under t.TempDir: both the auth entity stores and the
	// framework's own tables share it. The pure-Go driver keeps CI free
	// of a cgo toolchain.
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	uiApp := appui.NewApp("posthog-e2e")
	uiApp.WithTheme(uitheme.Default())
	layout := appui.NewLayout("main").WithContainer()
	// Screens must be POINTERS: value screens fail DI at render and every
	// page 404s.
	uiApp.Register("/", &landingScreen{}, layout)
	uiApp.Register("/pricing", &pricingScreen{}, layout)
	uiApp.Register("/register", &registerScreen{}, layout)

	host := uihost.New(uiApp, uihost.WithDescription("posthog e2e"))

	app := framework.NewUIHostApp(host,
		framework.WithDB(db),
		framework.WithConfig(framework.AppConfig{Name: "posthog-e2e"}),
	)

	// The integration under test, pointed at the fake vendor through the
	// test-only seam.
	p := newWithUpstreams(Config{Key: e2eKey}, vendor.srv.URL, vendor.srv.URL)
	app.RegisterPlugin(p)
	if err := host.RegisterExternalScript(p.ScriptURL()); err != nil {
		t.Fatalf("register boot script: %v", err)
	}

	// The page script, served the same CSP-clean way.
	js := []byte(pageScriptJS)
	app.Router().Get("/__e2e/page.js", uihost.ScriptHandler(js))
	if err := host.RegisterExternalScript(uihost.ScriptURL("/__e2e/page.js", js)); err != nil {
		t.Fatalf("register page script: %v", err)
	}

	// battery/auth: real register/login against sqlite-backed entity
	// stores. DevMode: HTTP-friendly session cookie. The harness drives
	// the router directly (no App.Start), so Init runs explicitly — the
	// same call Start makes.
	mgr := auth.New(auth.AuthConfig{
		DevMode:      true,
		UserStore:    auth.NewEntityUserStore(db, "auth_users"),
		SessionStore: auth.NewEntitySessionStore(db, "auth_sessions"),
	})
	mgr.Use(auth.NewCorePlugin())
	if err := mgr.Init(nil); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	mgr.RegisterRoutes(app.Router())

	// SessionMiddleware app-wide: it annotates the request context the
	// plugin's whoami reads (GetUser), which is the whole identity path.
	app.Use(auth.SessionMiddleware(mgr))

	if err := app.InitPlugins(); err != nil {
		t.Fatalf("init plugins: %v", err)
	}

	srv := httptest.NewServer(app.Router())
	t.Cleanup(srv.Close)
	return &e2eApp{base: srv.URL, db: db}
}

// waitFor polls cond until true or the deadline, then fatals. Beacons
// flush asynchronously (the SDK batches); 8s covers it.
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

// ─── the shared browser ───────────────────────────────────────────────

// posthog-js ships bot detection and silently drops every capture when
// navigator.webdriver is set — which a stock chromedp browser always has.
// The main shared browser runs with --disable-blink-features=
// AutomationControlled and a normal Mozilla UA (the documented remedy,
// see README "Testing with browser automation"); TestBotAutomation*
// launches its own browser WITHOUT them on purpose.
var (
	browserOnce   sync.Once
	browserRoot   context.Context
	browserErr    error
	browserKill   context.CancelFunc
	allocatorKill context.CancelFunc
)

// e2eBrowserCtx returns a fresh tab of the shared anti-detection Chrome.
func e2eBrowserCtx(t *testing.T) context.Context {
	t.Helper()
	browserOnce.Do(func() {
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-blink-features", "AutomationControlled"),
			chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
			chromedp.WSURLReadTimeout(90*time.Second),
			chromedp.WindowSize(1280, 800),
		)
		allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
		browserCtx, browserCancel := chromedp.NewContext(allocCtx)
		if err := chromedp.Run(browserCtx); err != nil {
			browserErr = err
			browserCancel()
			allocCancel()
			return
		}
		browserRoot, browserKill, allocatorKill = browserCtx, browserCancel, allocCancel
	})
	if browserErr != nil {
		t.Fatalf("shared browser failed to start: %v", browserErr)
	}
	tabCtx, tabCancel := chromedp.NewContext(browserRoot)
	t.Cleanup(tabCancel)
	ctx, cancel := context.WithTimeout(tabCtx, 60*time.Second)
	t.Cleanup(cancel)
	// Foreground the tab: Chrome throttles background tabs, which would
	// starve every timing-sensitive assertion here.
	if err := chromedp.Run(ctx, page.BringToFront()); err != nil {
		t.Fatalf("bring tab to front: %v", err)
	}
	return ctx
}

// botBrowserCtx launches a SEPARATE Chrome without the anti-detection
// flags: navigator.webdriver stays true, which is the exact condition
// TestBotAutomationCapturesDropped pins.
func botBrowserCtx(t *testing.T) context.Context {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.WSURLReadTimeout(90*time.Second),
		chromedp.WindowSize(1280, 800),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(allocCancel)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	t.Cleanup(browserCancel)
	ctx, cancel := context.WithTimeout(browserCtx, 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestMain(m *testing.M) {
	code := m.Run()
	if browserKill != nil {
		browserKill()
	}
	if allocatorKill != nil {
		allocatorKill()
	}
	os.Exit(code)
}

// navigate loads a full page and waits for first paint.
func navigate(t *testing.T, ctx context.Context, target string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(target),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("navigate %s: %v", target, err)
	}
}

// prop returns an event property as a string, "" when absent.
func prop(e vendorEvent, key string) string {
	if e.Properties == nil {
		return ""
	}
	v, ok := e.Properties[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

// ─── the scenarios ────────────────────────────────────────────────────

// Scenario 1: a deep link with UTM params still in the URL. The $pageview
// the bootstrap fires must carry the full URL (query included) AND the
// utm_source property — first-touch attribution is automatic in the real
// SDK, no host code involved.
func TestDeepURLPageviewCarriesUTM(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short")
	}
	vendor := newFakeVendor(t)
	app := newE2EApp(t, vendor)
	ctx := e2eBrowserCtx(t)

	navigate(t, ctx, app.base+"/pricing?utm_source=newsletter&utm_campaign=aug-launch")

	waitFor(t, "a $pageview", func() bool { return len(vendor.eventsNamed("$pageview")) > 0 })

	var got *vendorEvent
	for _, e := range vendor.eventsNamed("$pageview") {
		if strings.Contains(prop(e, "$current_url"), "utm_source=newsletter") {
			got = &e
			break
		}
	}
	if got == nil {
		t.Fatalf("no $pageview whose $current_url keeps the utm query; pageviews: %+v", vendor.eventsNamed("$pageview"))
	}
	if src := prop(*got, "utm_source"); src != "newsletter" {
		t.Errorf("utm_source property = %q, want newsletter", src)
	}
	if camp := prop(*got, "utm_campaign"); camp != "aug-launch" {
		t.Errorf("utm_campaign property = %q, want aug-launch", camp)
	}
}

// Scenario 2, the ad-attribution guarantee: UTM + gclid arrive on "/",
// SPA navigation to /pricing drops them from the URL, and the purchase
// captured afterwards STILL carries them — posthog-js persists campaign
// params per session, and that persistence must survive the whole
// first-party journey.
func TestAttributionSurvivesSPAToPurchase(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short")
	}
	vendor := newFakeVendor(t)
	app := newE2EApp(t, vendor)
	ctx := e2eBrowserCtx(t)

	navigate(t, ctx, app.base+"/?utm_source=twitter&utm_campaign=launch-week&gclid=G123")
	waitFor(t, "initial $pageview", func() bool { return len(vendor.eventsNamed("$pageview")) > 0 })

	// SPA navigation: click the link, wait for the pricing screen's DOM.
	if err := chromedp.Run(ctx,
		chromedp.Click("#nav-pricing", chromedp.ByQuery),
		chromedp.WaitVisible("#buy-pro", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("SPA nav to /pricing: %v", err)
	}
	var pathname string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.pathname + location.search`, &pathname)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pathname, "utm_source") || strings.Contains(pathname, "gclid") {
		t.Fatalf("precondition: URL still carries campaign params: %q", pathname)
	}

	if err := chromedp.Run(ctx,
		chromedp.Click("#buy-pro", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click buy: %v", err)
	}

	waitFor(t, "purchase event", func() bool { return len(vendor.eventsNamed("purchase")) > 0 })
	p := vendor.eventsNamed("purchase")[0]
	if src := prop(p, "utm_source"); src != "twitter" {
		t.Errorf("purchase utm_source = %q, want twitter", src)
	}
	if g := prop(p, "gclid"); g != "G123" {
		t.Errorf("purchase gclid = %q, want G123", g)
	}
	if c := prop(p, "utm_campaign"); c != "launch-week" {
		t.Errorf("purchase utm_campaign = %q, want launch-week", c)
	}
	if plan := prop(p, "plan"); plan != "pro" {
		t.Errorf("purchase plan = %q, want pro (host property must survive the ride)", plan)
	}
}

// Scenario 3, the GetID fix end-to-end: the browser registers through the
// REAL battery/auth form (clicking the button — the runtime's form
// interceptor follows the 303), lands authenticated, and the bootstrap's
// whoami refresh identifies. The $identify beacon must name the real
// auth_users.id as distinct_id and carry $anon_distinct_id — the
// anonymous→identified merge PostHog's person timeline is built from.
func TestRealAuthIdentifyMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short")
	}
	vendor := newFakeVendor(t)
	app := newE2EApp(t, vendor)
	ctx := e2eBrowserCtx(t)

	navigate(t, ctx, app.base+"/register")
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible("#reg-email", chromedp.ByQuery),
		chromedp.Click("#reg-email", chromedp.ByQuery),
		chromedp.SendKeys("#reg-email", "buyer@example.com", chromedp.ByQuery),
		chromedp.Click("#reg-password", chromedp.ByQuery),
		chromedp.SendKeys("#reg-password", "correct-horse-battery", chromedp.ByQuery),
		chromedp.Click("#register-submit", chromedp.ByQuery), // Click, never Submit
	); err != nil {
		t.Fatalf("fill and submit register form: %v", err)
	}

	// The 303 lands on "/" and the SPA navigate fires whoami refresh →
	// identify. Wait for the redirect first so the sqlite read below is
	// not racing the insert.
	waitFor(t, "redirect to /", func() bool {
		var pathname string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`location.pathname`, &pathname)); err != nil {
			return false
		}
		return pathname == "/"
	})

	var userID string
	waitFor(t, "auth_users row", func() bool {
		_ = app.db.QueryRow(`SELECT id FROM auth_users WHERE email = ?`, "buyer@example.com").Scan(&userID)
		return userID != ""
	})

	waitFor(t, "$identify event", func() bool { return len(vendor.eventsNamed("$identify")) > 0 })
	id := vendor.eventsNamed("$identify")[0]
	// On the wire (posthog-js batch events) the acting distinct_id rides
	// in properties, alongside the $anon_distinct_id merge key.
	if got := prop(id, "distinct_id"); got != userID {
		t.Errorf("$identify properties.distinct_id = %q, want auth user id %q", got, userID)
	}
	if anon := prop(id, "$anon_distinct_id"); anon == "" {
		t.Errorf("$identify is missing $anon_distinct_id (no anonymous merge); properties: %v", id.Properties)
	}
}

// Scenario 4, the documented gotcha: in a browser whose navigator.webdriver
// is visible (chromedp WITHOUT --disable-blink-features=
// AutomationControlled), posthog-js's bot detection loads the SDK — the
// vendor sees the array.js fetch — but drops every capture. Zero
// ingestion beacons, ever.
func TestBotAutomationCapturesDropped(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short")
	}
	vendor := newFakeVendor(t)
	app := newE2EApp(t, vendor)
	ctx := botBrowserCtx(t)

	var webdriver bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`navigator.webdriver`, &webdriver)); err != nil {
		t.Fatal(err)
	}
	if !webdriver {
		t.Fatal("precondition failed: navigator.webdriver is false in the bot browser; the flag list drifted")
	}

	navigate(t, ctx, app.base+"/")
	waitFor(t, "the SDK fetch through the relay", func() bool { return vendor.sawPath("/static/array.js") })

	// 8s of slack: the SDK batches beacons; if bot detection were NOT
	// dropping them, the pageview lands well inside it.
	if err := chromedp.Run(ctx, chromedp.Sleep(8*time.Second)); err != nil {
		t.Fatal(err)
	}

	if paths := vendor.ingestionPaths(); len(paths) > 0 {
		t.Errorf("bot browser produced %d ingestion request(s): %v; bot detection should have dropped every capture", len(paths), paths)
	}
	if got := len(vendor.eventsNamed("$pageview")); got != 0 {
		t.Errorf("bot browser recorded %d $pageview(s); want 0", got)
	}
}

// Scenario 5: a multivariate flag served through the relayed /flags call.
// The page paints the variant (h1[data-ab-variant=punchy]) AND the
// $feature_flag_called exposure arrives with $feature_flag_response —
// PostHog's experiment analysis keys on that event, so both halves must
// ride the same first-party wire.
func TestFlagVariantExposureThroughRelay(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short")
	}
	vendor := newFakeVendor(t)
	app := newE2EApp(t, vendor)
	ctx := e2eBrowserCtx(t)

	navigate(t, ctx, app.base+"/")

	// The page script's onFeatureFlags callback paints the h1 once flags
	// resolve through the relay.
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`h1[data-ab-variant="punchy"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("h1 never got data-ab-variant=\"punchy\": %v", err)
	}

	waitFor(t, "$feature_flag_called", func() bool { return len(vendor.eventsNamed("$feature_flag_called")) > 0 })
	exposure := vendor.eventsNamed("$feature_flag_called")[0]
	if resp := prop(exposure, "$feature_flag_response"); resp != "punchy" {
		t.Errorf("$feature_flag_response = %q, want punchy", resp)
	}
	if key := prop(exposure, "$feature_flag"); key != "hero-copy-test" {
		t.Errorf("$feature_flag = %q, want hero-copy-test", key)
	}
}

// Scenario 6, the wire regression pin: whatever ingestion path this SDK
// version speaks, the vendor receives it verbatim — trailing slash
// included. (The relay's trailing-slash fix at the SDK level: a naive
// path.Join drops it, and a server that then 404s "/e" loses beacons
// silently.)
func TestIngestPathsArriveVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short")
	}
	vendor := newFakeVendor(t)
	app := newE2EApp(t, vendor)
	ctx := e2eBrowserCtx(t)

	navigate(t, ctx, app.base+"/")
	waitFor(t, "a $pageview (proof a beacon rode an ingest path)", func() bool {
		return len(vendor.eventsNamed("$pageview")) > 0
	})

	slashForms := 0
	for _, p := range vendor.ingestionPaths() {
		switch p {
		case "/e/", "/i/v0/e/":
			slashForms++
		case "/e", "/i/v0/e":
			t.Errorf("ingestion path %q lost its trailing slash somewhere in the relay", p)
		}
	}
	if slashForms == 0 {
		t.Errorf("no trailing-slash ingestion path (/e/ or /i/v0/e/) among %v", vendor.ingestionPaths())
	}
}
