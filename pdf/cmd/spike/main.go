// Command spike serves ONLY the pdf plugin (with its demo page) for the throwaway
// WebKit probe at pdf/js/spike-webkit.mjs. It is not the product; it exists so the
// spike can point a real WebKit (Safari engine) at the exact framed setup without
// modifying example/main.go (which this spike is forbidden to touch).
//
// MODE env selects the mount mode ("view" default, "annotate", "redact"), so
// one server covers every probe — the render probe wants view, the export
// round-trip wants annotate, the redaction proof wants redact. Anything but
// view also wires an in-memory export handler, since that is what grants
// pdf:export and ModeRedact refuses to construct without it.
//
// Run:  GOWORK=off go run ./pdf/cmd/spike   (PORT pins the port, default :8099)
//
//	MODE=redact GOWORK=off go run ./pdf/cmd/spike
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/pdf"
	"github.com/DonaldMurillo/gofastr/framework"
)

// exported holds produced bytes in memory so a probe can read back what it
// exported. A spike server never touches the filesystem.
var exported struct {
	mu   sync.Mutex
	byID map[string][]byte
	last []byte // the most recent export, for the round-trip probe
}

func exportHandler(_ context.Context, req pdf.ExportRequest) (string, error) {
	exported.mu.Lock()
	defer exported.mu.Unlock()
	if exported.byID == nil {
		exported.byID = map[string][]byte{}
	}
	sum := sha256.Sum256(req.Bytes)
	id := hex.EncodeToString(sum[:8])
	exported.byID[id] = req.Bytes
	exported.last = req.Bytes
	return "/spike/exported/" + id, nil
}

// spikeSource feeds the viewer. The docId "replay" resolves to the bytes most
// recently produced by exportHandler, which is what lets a probe export a
// document and then re-open the RESULT in the same viewer — the only way to
// prove an annotation (or a redaction) actually survived into the output rather
// than merely appearing on screen. Every other id gets the embedded sample.
func spikeSource(_ context.Context, id string) ([]byte, error) {
	if id == "replay" {
		exported.mu.Lock()
		defer exported.mu.Unlock()
		if len(exported.last) == 0 {
			return nil, nil // 404 until something has been exported
		}
		return exported.last, nil
	}
	return pdf.SampleDocument(), nil
}

func main() {
	opts := []pdf.Option{pdf.WithDevGrantAll(), pdf.WithDemoPage(), pdf.WithSource(spikeSource)}
	switch os.Getenv("MODE") {
	case "annotate":
		opts = append(opts, pdf.WithMode(pdf.ModeAnnotate), pdf.WithExportHandler(exportHandler))
	case "redact":
		opts = append(opts, pdf.WithMode(pdf.ModeRedact), pdf.WithExportHandler(exportHandler))
	}

	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "pdf-spike"}))
	app.RegisterPlugin(pdf.New(opts...))
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
	// The most recent export, for the round-trip probe. Separate from the
	// content-addressed route below because a probe wants "whatever I just
	// produced" without having to parse the URL out of the exportResult.
	app.Router().Get("/last-export", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		exported.mu.Lock()
		b := exported.last
		exported.mu.Unlock()
		if len(b) == 0 {
			http.Error(w, "no export yet", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(b)
	}))

	app.Router().Get("/spike/exported/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exported.mu.Lock()
		b, ok := exported.byID[r.PathValue("id")]
		exported.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(b)
	}))

	fmt.Printf("pdf spike listening on http://localhost:%d/pdf\n", ln.Addr().(*net.TCPAddr).Port)
	if err := http.Serve(ln, app.Router()); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
