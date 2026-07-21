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
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/DonaldMurillo/gofastr-plugins/mermaid"
	"github.com/DonaldMurillo/gofastr-plugins/monaco"
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

	if err := app.InitPlugins(); err != nil {
		return nil, err
	}

	// The gallery shell owns "/": a homepage + persistent sidebar that frames
	// each plugin's demo. Registered after InitPlugins so it sits alongside the
	// plugin routes on the same router.
	registerShell(app.Router())

	return app, nil
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
