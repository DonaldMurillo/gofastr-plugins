package wysiwyg

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr/battery/auth"
)

// authScopes returns a context carrying an API token with the given scopes.
func authScopes(scopes ...string) context.Context {
	return auth.WithTokenScopes(context.Background(), scopes)
}

// A minimal valid 1x1 PNG (sniffs as image/png).
var onePixelPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
	0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

// TestSaveRejectsOversizeBody proves the MaxBytesReader guard on save: a body
// past the ceiling is rejected, not buffered.
func TestSaveRejectsOversizeBody(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	huge := `{"docId":"demo","doc":"` + strings.Repeat("A", maxSaveBytes+1024) + `"}`
	resp, err := http.Post(srv.URL+SaveURL, "application/json", strings.NewReader(huge))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("oversize save was accepted (status %d); MaxBytesReader not enforced", resp.StatusCode)
	}
}

// TestUploadRejectsNonImage proves the sniff guard: upload:images must actually
// receive an image, regardless of the caller-supplied Content-Type header.
func TestUploadRejectsNonImage(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// A caller lying that a script is a PNG.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+UploadURL,
		strings.NewReader("<script>alert(1)</script>"))
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("X-Upload-Type", "image/png")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("non-image upload accepted with status %d; want 415", resp.StatusCode)
	}
}

// TestUploadAcceptsRealImage keeps the sniff guard honest — a genuine image
// still round-trips, and the echoed data: URL carries the SNIFFED type.
func TestUploadAcceptsRealImage(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+UploadURL, bytes.NewReader(onePixelPNG))
	// Lie about the type — the server must ignore it and sniff image/png.
	req.Header.Set("X-Upload-Type", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("real image rejected: status %d", resp.StatusCode)
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "data:image/png") {
		t.Errorf("echoed URL did not carry the sniffed image/png type: %s", body[:n])
	}
}

// TestReadViewGated proves the read route runs the document:read gate. Because
// the framework's HasScope treats an UNSCOPED caller (anonymous / plain
// session) as full authority, the gate bites a SCOPED API token that lacks
// document:read — the layer a host relies on once auth middleware is wired.
// (Anonymous denial is the host auth middleware's job, NOT this gate — that is
// security finding H1: "secure by default" means the token-scope layer, not
// authentication. Enforcing hosts MUST wrap these routes with RequireAuth.)
func TestReadViewGated(t *testing.T) {
	_, p := newTestApp(t) // enforcing (no devGrantAll)

	// A scoped token that does NOT grant document:read must be denied by the
	// gate; one that does must pass.
	noRead := httptest.NewRequest(http.MethodGet, ReadURL+"?doc=demo", nil).
		WithContext(authScopes("posts:read"))
	if p.allow(noRead, "document:read") {
		t.Error("read gate allowed a scoped token lacking document:read")
	}
	yesRead := httptest.NewRequest(http.MethodGet, ReadURL+"?doc=demo", nil).
		WithContext(authScopes("document:read"))
	if !p.allow(yesRead, "document:read") {
		t.Error("read gate denied a scoped token that grants document:read")
	}
}

// TestReadViewNoScriptCSP pins that the read route does NOT widen script-src to
// 'unsafe-inline' (it ships no scripts and echoes user content).
func TestReadViewNoScriptCSP(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	_, _ = http.Post(srv.URL+SaveURL, "application/json",
		strings.NewReader(`{"docId":"demo","doc":{"type":"doc","content":[{"type":"paragraph"}]}}`))

	resp, err := http.Get(srv.URL + ReadURL + "?doc=demo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("read view CSP still widens script-src: %q", csp)
	}
	if !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Errorf("read view CSP must keep style-src 'unsafe-inline' (SSR inline color spans): %q", csp)
	}
}
