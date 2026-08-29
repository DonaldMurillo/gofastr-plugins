// Command example is the single GoFastr app that imports and mounts every
// plugin in this repo. It is the integration host, the visual/e2e test surface,
// and the completeness canary: a plugin that cannot mount cleanly here is a
// platform gap, not a plugin bug.
//
// Run with:
//
//	go run ./example
//
// Then open http://localhost:8090/ for the Rich Text editor demo (served by the
// richtext plugin's self-contained themed demo page).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/calendar"
	"github.com/DonaldMurillo/gofastr-plugins/chart"
	"github.com/DonaldMurillo/gofastr-plugins/datagrid"
	"github.com/DonaldMurillo/gofastr-plugins/formbuilder"
	"github.com/DonaldMurillo/gofastr-plugins/genui"
	"github.com/DonaldMurillo/gofastr-plugins/geomap"
	"github.com/DonaldMurillo/gofastr-plugins/imageedit"
	"github.com/DonaldMurillo/gofastr-plugins/logstream"
	"github.com/DonaldMurillo/gofastr-plugins/mermaid"
	"github.com/DonaldMurillo/gofastr-plugins/monaco"
	"github.com/DonaldMurillo/gofastr-plugins/pdf"
	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr-plugins/richtext"
	"github.com/DonaldMurillo/gofastr-plugins/scanner"
	"github.com/DonaldMurillo/gofastr-plugins/sqlnotebook"
	"github.com/DonaldMurillo/gofastr-plugins/tour"
	"github.com/DonaldMurillo/gofastr-plugins/whiteboard"
	"github.com/DonaldMurillo/gofastr/core/middleware"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newApp builds the example application with every plugin mounted. It is shared
// by main and the e2e tests so they exercise the exact same wiring.
func newApp() (*framework.App, error) {
	app := framework.NewApp(
		framework.WithConfig(framework.AppConfig{
			Name: "gofastr-plugins-example",
			// The scanner needs the camera ON THIS PAGE. The framework's
			// default Permissions-Policy is
			// "geolocation=(), microphone=(), camera=()", which denies it to
			// the document itself — so getUserMedia fails on the HOST too, not
			// just in the cage, and the failure surfaces as a console error
			// rather than a prompt. A host that mounts the scanner has to opt
			// in like this; docs/scanner.md says so, and nothing else here
			// needs the camera, so the grant is 'self' and nothing wider.
			//
			// scanner's manifest DECLARES this requirement, and the boot check
			// at the end of this function reports a host that has not met it.
			// The declaration does not grant anything and cannot: this line is
			// still what actually opens the camera.
			SecurityHeaders: middleware.SecurityHeadersConfig{
				PermissionsPolicy: examplePermissionsPolicy,
			},
		}),
	)

	// The Rich Text editor plugin. WithDevGrantAll lets the unauthenticated demo
	// satisfy the document:write / upload:images capability gate (Phase 0 has no
	// login); WithDemoPage serves the self-contained themed editor page at "/".
	// WithTrustedMount is the EXPLICIT opt-out of the sandbox (DECISIONS.md
	// "secure by default, opt out"): it additionally serves the frameless
	// in-page demo at /__gofastr/plugin/richtext/trusted for comparison and e2e.
	// The demo mounts at /richtext (not "/") so the example's plugin gallery can
	// own the homepage. WithTrustedMount adds the frameless demo at
	// /__gofastr/plugin/richtext/trusted.
	app.RegisterPlugin(richtext.New(
		richtext.WithDevGrantAll(),
		richtext.WithDemoPage(),
		richtext.WithDemoRoute("/richtext"),
		richtext.WithTrustedMount(),
	))

	// The second heavy-JS plugin — an isolated Mermaid diagram renderer — mounted
	// on the SAME pluginhost platform. It is the completeness canary: it proves
	// the extracted platform generalizes beyond the editor. Demo at /mermaid.
	app.RegisterPlugin(mermaid.New(
		mermaid.WithDevGrantAll(),
		mermaid.WithDemoPage(),
	))

	// The Monaco code-editor plugin — the third sandboxed heavy-JS plugin. Same
	// opaque-origin iframe platform as richtext/mermaid; configurable language,
	// theme, and modes. Demo at /monaco.
	app.RegisterPlugin(monaco.New(
		monaco.WithDevGrantAll(),
		monaco.WithDemoPage(),
	))

	// The guided-tour ("app cues") plugin — the FIRST trusted host-page plugin
	// (no sandbox): it must reach the host DOM to spotlight elements. Demo at
	// /tour runs a self-registered three-step tour. WithDevGrantAll opens the
	// tour:read/write gate for the unauthenticated demo.
	app.RegisterPlugin(tour.New(
		tour.WithDevGrantAll(),
		tour.WithDemoPage(),
	))

	// The Geomap plugin — an interactive vector map built on MapLibre GL +
	// OpenFreeMap tiles. It is a TRUSTED host-page plugin (like tour), not a
	// sandbox: vector tiles need fetch() + a web worker, both impossible under
	// the opaque frame's connect-src 'none'. The host page CSP allows the
	// OpenFreeMap host + the blob worker. Demo at /map.
	//
	// Place search is wired to a FIXED local dataset rather than the default
	// Nominatim proxy. Two reasons: the e2e journeys must not depend on a third
	// party being reachable (or on its rate limit), and a demo app has no business
	// spending someone else's donated geocoding capacity. A real app calls
	// geomap.WithSearch() — plus WithGeocodeUserAgent to identify itself — and
	// gets the Nominatim path. See geomap/geocode.go.
	app.RegisterPlugin(geomap.New(
		geomap.WithDevGrantAll(),
		geomap.WithDemoPage(),
		geomap.WithGeocoder(demoGeocoder),
	))

	// The PDF viewer / editor / redactor — the fourth SANDBOXED heavy-JS plugin,
	// and the one whose cage is the product rather than the tax. The frame has
	// connect-src 'none', so a document opened for redaction structurally cannot
	// be exfiltrated: the host pushes its bytes in over the bridge and takes the
	// produced bytes back the same way. Demo at /pdf.
	//
	// The demo runs in the fullest mode so the gallery exercises the whole
	// surface; a real app picks the narrowest mode that fits, because mode is a
	// host decision enforced on both sides of the bridge (see docs/pdf.md).
	// WithExportHandler is what grants pdf:export, and ModeRedact refuses to
	// construct without it — so the demo has to say where produced bytes go.
	app.RegisterPlugin(pdf.New(
		pdf.WithDevGrantAll(),
		pdf.WithDemoPage(),
		pdf.WithMode(pdf.ModeRedact),
		pdf.WithExportHandler(demoExport),
	))

	// The data grid — the fifth SANDBOXED heavy-JS plugin, and the one whose
	// traffic profile differs from every other plugin here: not one document
	// but 100,000 rows, crossing the bridge one page at a time. The frame
	// has connect-src 'none' and can never fetch its own rows; sorting,
	// filtering and paging all run in the Go rows source below (see
	// example/datagrid.go — the dataset is deterministic so the e2e journey
	// can assert exact cells at row 50,000). Demo at /datagrid.
	//
	// WithCellWriteHandler / WithExportHandler grant the optional
	// data:write / data:export capabilities so the gallery exercises the
	// whole surface; a real app picks the narrowest set it needs.
	app.RegisterPlugin(datagrid.New(
		datagrid.WithDevGrantAll(),
		datagrid.WithDemoPage(),
		datagrid.WithRowsSource(demoGridDataset.rows),
		datagrid.WithCellWriteHandler(demoGridDataset.writeCell),
		datagrid.WithExportHandler(demoGridExport),
		datagrid.WithDemoDoc(demoGridDoc()),
	))

	// The chart plugin — one chart spec, two agreeing renderers. The server
	// renders a static SVG (chart/ssr) that stands on its own with JavaScript
	// off; the sandboxed Observable Plot frame hydrates it into an interactive
	// chart, and the host adapter hides the static node once the frame reports
	// ready. Both renderers share the same spec, theme tokens, and — via a
	// faithful port of d3-array's tick algorithm — the same axis tick labels.
	// Demo at /chart.
	app.RegisterPlugin(chart.New(
		chart.WithDevGrantAll(),
		chart.WithDemoPage(),
	))

	// The log stream — the eighth sandboxed heavy-JS plugin, and the only one
	// whose traffic is not turn-based: the host PUSHES log lines into the
	// frame without ever being asked, and the bridge's backpressure (a
	// 4-batch ack window, a bounded host buffer, oldest-end drops with a
	// visible marker in the terminal) is the product. The demo source is a
	// deterministic synthetic generator (example/logstream.go) with a page
	// control switching between 5 lines/s and a 6,000 lines/s flood that the
	// frame's ~60-batches/s render rate provably cannot absorb. Demo at
	// /logstream.
	app.RegisterPlugin(logstream.New(
		logstream.WithDevGrantAll(),
		logstream.WithDemoPage(),
		logstream.WithSource(demoLogs.source),
		logstream.WithDemoControlURL(demoLogControlPath),
	))

	// Generative UI — the plugin where the untrusted input is a MODEL'S
	// OUTPUT. It composes from a fixed registry of components rather than
	// emitting markup, and the composition is validated in Go before it is
	// stored and again in the frame before it is rendered. The composer here
	// is the deterministic fixture one: no key, no network, same answers every
	// run. Demo at /genui.
	app.RegisterPlugin(genui.New(
		genui.WithDevGrantAll(),
		genui.WithDemoPage(),
	))

	// The scanner — the plugin whose input is a DEVICE, and the one that
	// proves a capability the cage cannot hold can still be delivered to it.
	// An opaque-origin frame is refused getUserMedia outright, so this page
	// keeps the camera and pushes grayscale frames over the bridge; the
	// decoder runs inside the cage under connect-src 'none'. Demo at
	// /scanner.
	app.RegisterPlugin(scanner.New(
		scanner.WithDevGrantAll(),
		scanner.WithDemoPage(),
	))

	// The SQL notebook — the only plugin here that opts into the
	// 'wasm-unsafe-eval' tier, and the one that settled whether WebAssembly
	// can run in the cage at all. A real SQLite engine compiles inside an
	// opaque-origin frame with connect-src 'none', which means the frame
	// cannot fetch its own engine: the host reads the .wasm and hands it
	// across the bridge as bytes. A database that cannot phone home. Demo at
	// /sqlnotebook.
	app.RegisterPlugin(sqlnotebook.New(
		sqlnotebook.WithDevGrantAll(),
		sqlnotebook.WithDemoPage(),
	))

	// The image editor — the pdf plugin's design applied to a second file
	// format: the cage is the product, not the tax. The frame has
	// connect-src 'none' and receives the image bytes over the bridge; the
	// canonical doc is an OPERATION LIST (crop/rotate/annotate/redact), and
	// every export is re-rendered by Go from that list — EXIF stripped,
	// caps enforced, redactions verified against the produced bytes. Demo
	// at /imageedit.
	//
	// WithUploadHandler grants the optional upload:images capability so the
	// demo can load a local image (the bytes cross the bridge, never the
	// network); the uploads land in the in-memory store below and become
	// addressable ids the demo source resolves.
	app.RegisterPlugin(imageedit.New(
		imageedit.WithDevGrantAll(),
		imageedit.WithDemoPage(),
		imageedit.WithSource(demoImageeditSource),
		imageedit.WithUploadHandler(demoImageeditUpload),
		imageedit.WithExportHandler(demoImageeditExport),
	))
	// The form builder — the ninth sandboxed plugin, and the only one that
	// authors the framework itself: its output is a form schema the server
	// consumes and enforces, not content it displays. The design demo runs
	// at /formbuilder; /formbuilder/live renders the SAVED schema through
	// GoFastr's own ui.Form and re-validates every rule in Go on POST —
	// design a required field with a pattern in the cage, then try to get
	// garbage past the live form. That round trip is the whole argument.
	app.RegisterPlugin(formbuilder.New(
		formbuilder.WithDevGrantAll(),
		formbuilder.WithDemoPage(),
	))

	// The calendar — the plugin with no calendar library. Month/week/day
	// views are hand-written inside the sandboxed frame; RRULE expansion,
	// timezone conversion and conflict detection all run in the Go events
	// source below (see example/calendar.go — the seed contains every hard
	// case: a gap-spanner on the 2026 spring-forward, a weekly series
	// straddling both transitions, a conflict pair, all-day and
	// midnight-spanning events). The frame sends move INTENTS; the server
	// decides what a drag across a DST boundary actually means. Demo at
	// /calendar, with jump buttons straight to both 2026 DST weekends.
	app.RegisterPlugin(calendar.New(
		calendar.WithDevGrantAll(),
		calendar.WithDemoPage(),
		calendar.WithEventsSource(demoCalendarEvents),
		calendar.WithDemoDoc(demoCalendarDoc()),
	))

	// The whiteboard — the tenth sandboxed plugin, and the answer to "surely
	// collaboration needs a socket in the frame". It does not: strokes are
	// Yjs CRDT updates, opaque binary blobs that cross the postMessage
	// bridge, and the ROOM HUB below owns the only network leg (SSE fan-out
	// to other browsers, replay for late joiners). The frame keeps
	// connect-src 'none'. Identity is assigned hub-side: an opaque pid and a
	// colour, never a name. Demo at /whiteboard — open it in two windows.
	wbHub := newWhiteboardHub()
	app.RegisterPlugin(whiteboard.New(
		whiteboard.WithDevGrantAll(),
		whiteboard.WithDemoPage(),
		whiteboard.WithRoomHub(wbHub.Subscribe, wbHub.Publish),
	))

	if err := app.InitPlugins(); err != nil {
		return nil, err
	}

	registerDemoGridExportRoute(app.Router())

	registerDemoImageeditExportRoute(app.Router())

	// The gallery shell owns "/": a homepage + persistent sidebar that frames
	// each plugin's demo. Registered after InitPlugins so it sits alongside the
	// plugin routes on the same router.
	registerShell(app.Router())
	registerRecipePages(app.Router())
	registerDemoExportRoute(app.Router())
	registerDemoLogControlRoute(app.Router())

	// Report any plugin whose declared host-page requirement this app has not
	// met. It logs and never fails, by upstream's design: a plugin must not be
	// able to take an app down by declaring something. The value is that a host
	// which forgets the Permissions-Policy above is told which plugin needs it,
	// at boot, instead of meeting a getUserMedia error in the console with
	// nothing naming the cause.
	pluginhost.CheckHostRequirements(slog.Default(), examplePermissionsPolicy, hostRequirementModules()...)

	return app, nil
}

// examplePermissionsPolicy is named rather than inline so the boot check above
// and TestExamplePolicySatisfiesEveryDeclaredHostRequirement read the SAME
// string the app serves. Two copies would let the test pass while the app
// shipped a policy that denies the camera.
const examplePermissionsPolicy = "geolocation=(), microphone=(), camera=(self)"

// hostRequirementModules lists the plugins this app mounts that declare a
// host-page requirement. Kept beside the registrations above rather than
// derived, because framework.App exposes no module list to walk.
func hostRequirementModules() []pluginhost.ClientModule {
	return []pluginhost.ClientModule{
		{Name: scanner.Name, Manifest: scanner.New().Manifest()},
	}
}

// demoPlaces is the fixed dataset behind the geomap search box in this example.
// It is deliberately tiny and offline — see the WithGeocoder call above.
var demoPlaces = []geomap.GeocodeResult{
	{Label: "New York, United States", Lat: 40.7128, Lng: -74.0060},
	{Label: "Newark, New Jersey, United States", Lat: 40.7357, Lng: -74.1724},
	{Label: "London, United Kingdom", Lat: 51.5074, Lng: -0.1278},
	{Label: "Tokyo, Japan", Lat: 35.6762, Lng: 139.6503},
	{Label: "Sydney, Australia", Lat: -33.8688, Lng: 151.2093},
	{Label: "São Paulo, Brazil", Lat: -23.5505, Lng: -46.6333},
	{Label: "Reykjavík, Iceland", Lat: 64.1466, Lng: -21.9426},
	{Label: "Nairobi, Kenya", Lat: -1.2921, Lng: 36.8219},
}

// demoGeocoder is a substring match over demoPlaces. A geocoder returns an empty
// slice (not an error) when nothing matches — an error means the lookup failed,
// which the plugin surfaces to the browser as a 502.
func demoGeocoder(_ context.Context, query string) ([]geomap.GeocodeResult, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	var out []geomap.GeocodeResult
	for _, pl := range demoPlaces {
		if strings.Contains(strings.ToLower(pl.Label), q) {
			out = append(out, pl)
		}
	}
	return out, nil
}

func main() {
	app, err := newApp()
	if err != nil {
		log.Fatalf("newApp: %v", err)
	}
	log.Printf("plugins available: %s@%s", richtext.Name, richtext.Version)

	// Default to a random free port (":0" lets the OS pick one) so repeated dev
	// runs never collide on a fixed port; PORT still pins it when set (the e2e
	// harness relies on that).
	addr := ":0"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	fmt.Printf("gofastr-plugins example listening on http://localhost:%d/\n", ln.Addr().(*net.TCPAddr).Port)
	if err := http.Serve(ln, app.Router()); err != nil {
		log.Fatal(err)
	}
}

// demoExport is the example app's pdf.WithExportHandler: it keeps the produced
// bytes in memory and hands back a URL that serves them once. Two reasons not to
// touch the filesystem — a demo that writes redacted documents to disk is a bad
// example to copy, and the e2e journey only needs to read the bytes back to
// assert the redacted string is gone.
//
// A real app persists to its document store and returns a durable URL.
func demoExport(_ context.Context, req pdf.ExportRequest) (string, error) {
	demoExports.mu.Lock()
	defer demoExports.mu.Unlock()
	if demoExports.byID == nil {
		demoExports.byID = map[string][]byte{}
	}
	// Content-addressed so a repeated export is idempotent and the e2e suite can
	// predict nothing — it reads the URL out of the exportResult it was given.
	sum := sha256.Sum256(req.Bytes)
	id := hex.EncodeToString(sum[:8])
	demoExports.byID[id] = req.Bytes
	return "/pdf/exported/" + id, nil
}

var demoExports struct {
	mu   sync.Mutex
	byID map[string][]byte
}

// registerDemoExportRoute serves what demoExport stored. Registered alongside
// the gallery shell so it lives on the same router as the plugin routes. Takes
// a structural interface for the same reason registerShell does — the example
// needs one method, not the whole router type.
func registerDemoExportRoute(rt interface {
	Get(string, http.Handler)
}) {
	rt.Get("/pdf/exported/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		demoExports.mu.Lock()
		b, ok := demoExports.byID[r.PathValue("id")]
		demoExports.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", "inline; filename=\"export.pdf\"")
		_, _ = w.Write(b)
	}))
}
