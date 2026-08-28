package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/imageedit"
)

// The imageedit demo's in-memory stores — the pdf demoExport pattern, applied
// to both directions the plugin moves bytes: uploads IN (a local image the
// frame read and pushed over the bridge) and exports OUT (the server-rendered
// result). Nothing touches the filesystem; a demo that writes images to disk
// is a bad example to copy, and the e2e journey only needs to read the bytes
// back to assert the redacted pixels are gone.

var demoImageeditStores struct {
	mu      sync.Mutex
	uploads map[string][]byte          // id → uploaded source bytes
	exports map[string]imageeditExport // id → produced render
}

type imageeditExport struct {
	bytes  []byte
	format string // "png" | "jpeg"
}

// demoImageeditUpload is imageedit.WithUploadHandler: content-addressed so a
// repeated upload is idempotent, and the id is what the doc's src.ref points
// at afterwards.
func demoImageeditUpload(_ context.Context, req imageedit.UploadRequest) (string, error) {
	sum := sha256.Sum256(req.Bytes)
	id := hex.EncodeToString(sum[:8])
	demoImageeditStores.mu.Lock()
	defer demoImageeditStores.mu.Unlock()
	if demoImageeditStores.uploads == nil {
		demoImageeditStores.uploads = map[string][]byte{}
	}
	demoImageeditStores.uploads[id] = req.Bytes
	return id, nil
}

// demoImageeditSource is imageedit.WithSource: uploaded ids resolve to their
// bytes; anything else falls back to the plugin's generated sample — a
// deliberate demo choice so /imageedit always has an image on first load. A
// production host returns (nil, nil) for unknown ids (a 404) instead of
// serving a stand-in.
func demoImageeditSource(_ context.Context, id string) ([]byte, error) {
	demoImageeditStores.mu.Lock()
	b, ok := demoImageeditStores.uploads[id]
	demoImageeditStores.mu.Unlock()
	if ok {
		return b, nil
	}
	return imageedit.SampleImage(), nil
}

// demoImageeditExport is imageedit.WithExportHandler: keeps the produced
// bytes in memory and hands back a URL that serves them. The e2e journey
// reads the URL out of the exportResult it was given and asserts the
// redacted region is gone from the bytes.
func demoImageeditExport(_ context.Context, req imageedit.ExportRequest) (string, error) {
	sum := sha256.Sum256(req.Bytes)
	id := hex.EncodeToString(sum[:8])
	demoImageeditStores.mu.Lock()
	defer demoImageeditStores.mu.Unlock()
	if demoImageeditStores.exports == nil {
		demoImageeditStores.exports = map[string]imageeditExport{}
	}
	demoImageeditStores.exports[id] = imageeditExport{bytes: req.Bytes, format: req.Format}
	return "/imageedit/exported/" + id, nil
}

// registerDemoImageeditExportRoute serves what demoImageeditExport stored.
// Registered alongside the gallery shell so it lives on the same router as
// the plugin routes. Takes the same structural interface as
// registerDemoExportRoute — the example needs one method, not the whole
// router type.
func registerDemoImageeditExportRoute(rt interface {
	Get(string, http.Handler)
}) {
	rt.Get("/imageedit/exported/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		demoImageeditStores.mu.Lock()
		e, ok := demoImageeditStores.exports[r.PathValue("id")]
		demoImageeditStores.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		mime := "image/png"
		if e.format == "jpeg" {
			mime = "image/jpeg"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", "inline; filename=\"export."+e.format+"\"")
		_, _ = w.Write(e.bytes)
	}))
}
