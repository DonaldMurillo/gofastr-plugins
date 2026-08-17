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
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/geomap"
	"github.com/DonaldMurillo/gofastr-plugins/mermaid"
	"github.com/DonaldMurillo/gofastr-plugins/monaco"
	"github.com/DonaldMurillo/gofastr-plugins/pdf"
	"github.com/DonaldMurillo/gofastr-plugins/richtext"
	"github.com/DonaldMurillo/gofastr-plugins/tour"
	"github.com/DonaldMurillo/gofastr/framework"
)

// newApp builds the example application with every plugin mounted. It is shared
// by main and the e2e tests so they exercise the exact same wiring.
func newApp() (*framework.App, error) {
	app := framework.NewApp(
		framework.WithConfig(framework.AppConfig{Name: "gofastr-plugins-example"}),
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

	if err := app.InitPlugins(); err != nil {
		return nil, err
	}

	// The gallery shell owns "/": a homepage + persistent sidebar that frames
	// each plugin's demo. Registered after InitPlugins so it sits alongside the
	// plugin routes on the same router.
	registerShell(app.Router())
	registerRecipePages(app.Router())
	registerDemoExportRoute(app.Router())

	return app, nil
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
