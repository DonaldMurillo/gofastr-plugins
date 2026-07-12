// Command example is the single GoFastr app that imports and mounts every
// plugin in this repo. It is the integration host, the visual/e2e test
// surface, and the completeness canary: a plugin that cannot mount cleanly
// here is a platform gap, not a plugin bug.
//
// Run with:
//
//	go run ./example
//
// Today it is a bare GoFastr server — the wysiwyg plugin is still scaffolding
// (Phase 0). Once the editor's isolation spike lands, this app will mount it on
// a page and exercise the full third-party contract.
package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/wysiwyg"
	"github.com/DonaldMurillo/gofastr/framework"
)

func main() {
	app := framework.NewApp(
		framework.WithConfig(framework.AppConfig{Name: "gofastr-plugins-example"}),
	)

	// TODO(phase-0): app.RegisterPlugin(wysiwyg.New(...)) once the editor
	// plugin exists. For now we just prove the wiring compiles and links.
	log.Printf("plugins available: %s@%s", wysiwyg.Name, wysiwyg.Version)

	if err := app.InitPlugins(); err != nil {
		log.Fatalf("InitPlugins: %v", err)
	}

	const addr = ":8090"
	fmt.Printf("gofastr-plugins example listening on http://localhost%s\n", addr)
	if err := http.ListenAndServe(addr, app.Router()); err != nil {
		log.Fatal(err)
	}
}
