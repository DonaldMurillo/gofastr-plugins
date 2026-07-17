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
	"net/http"
	"os"

	"github.com/DonaldMurillo/gofastr-plugins/mermaid"
	"github.com/DonaldMurillo/gofastr-plugins/richtext"
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
	app.RegisterPlugin(richtext.New(
		richtext.WithDevGrantAll(),
		richtext.WithDemoPage(),
		richtext.WithTrustedMount(),
	))

	// The second heavy-JS plugin — an isolated Mermaid diagram renderer — mounted
	// on the SAME pluginhost platform. It is the completeness canary: it proves
	// the extracted platform generalizes beyond the editor. Demo at /mermaid.
	app.RegisterPlugin(mermaid.New(
		mermaid.WithDevGrantAll(),
		mermaid.WithDemoPage(),
	))

	if err := app.InitPlugins(); err != nil {
		return nil, err
	}
	return app, nil
}

func main() {
	app, err := newApp()
	if err != nil {
		log.Fatalf("newApp: %v", err)
	}
	log.Printf("plugins available: %s@%s", richtext.Name, richtext.Version)

	addr := ":8090"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	fmt.Printf("gofastr-plugins example listening on http://localhost%s\n", addr)
	if err := http.ListenAndServe(addr, app.Router()); err != nil {
		log.Fatal(err)
	}
}
