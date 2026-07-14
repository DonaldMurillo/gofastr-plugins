package wysiwyg

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr/framework"
)

// TestSSRReadView proves the no-JS read view end to end: a saved canonical
// block-JSON doc is rendered server-side (wysiwyg/ssr) to real, semantic,
// script-free HTML — the portable first-paint the editor iframe hydrates over.
func TestSSRReadView(t *testing.T) {
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "readview-test"}))
	app.RegisterPlugin(New(WithDevGrantAll()))
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// Save a doc with a heading + a paragraph (canonical ProseMirror block-JSON).
	doc := `{"type":"doc","content":[` +
		`{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Read View Works"}]},` +
		`{"type":"paragraph","content":[{"type":"text","text":"Hello from the server."}]}]}`
	payload := `{"docId":"demo","doc":` + doc + `,"markdown":"# Read View Works\n\nHello from the server.","schemaVersion":"wysiwyg-v1"}`
	resp, err := http.Post(srv.URL+SaveURL, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d", resp.StatusCode)
	}

	// Fetch the no-JS read view.
	rv, err := http.Get(srv.URL + ReadURL + "?doc=demo")
	if err != nil {
		t.Fatalf("GET read: %v", err)
	}
	defer rv.Body.Close()
	if rv.StatusCode != http.StatusOK {
		t.Fatalf("read status=%d", rv.StatusCode)
	}
	b, _ := io.ReadAll(rv.Body)
	html := string(b)

	// Real content, semantic tags — and NOT a script/iframe (pure SSR, no-JS).
	for _, want := range []string{"Read View Works", "Hello from the server.", "<h1"} {
		if !strings.Contains(html, want) {
			t.Errorf("read view missing %q\n---\n%s", want, html)
		}
	}
	for _, forbidden := range []string{"<script", "<iframe"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("read view must be no-JS but contains %q", forbidden)
		}
	}
}
