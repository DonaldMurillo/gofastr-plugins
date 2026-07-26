// Command spike serves ONLY the pdf plugin (with its demo page) for the throwaway
// WebKit probe at pdf/js/spike-webkit.mjs. It is not the product; it exists so the
// spike can point a real WebKit (Safari engine) at the exact framed setup without
// modifying example/main.go (which this spike is forbidden to touch).
//
// Run:  GOWORK=off go run ./pdf/cmd/spike   (PORT env pins the port; default :8099)
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/DonaldMurillo/gofastr-plugins/pdf"
	"github.com/DonaldMurillo/gofastr/framework"
)

func main() {
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "pdf-spike"}))
	app.RegisterPlugin(pdf.New(
		pdf.WithDevGrantAll(),
		pdf.WithDemoPage(),
	))
	if err := app.InitPlugins(); err != nil {
		fmt.Fprintln(os.Stderr, "InitPlugins:", err)
		os.Exit(1)
	}

	port := 8099
	if p := os.Getenv("PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	fmt.Printf("pdf spike listening on http://localhost:%d/pdf\n", ln.Addr().(*net.TCPAddr).Port)
	if err := http.Serve(ln, app.Router()); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
