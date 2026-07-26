package pdf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/chromedp"
)

// enforcement_test.go covers the Go-side gates the brief adds: mode + capability
// enforcement (table-driven), the MaxBytes ceiling on both byte-moving routes,
// the ErrConflict → 409 contract, the fail-loud ModeRedact construction guard,
// the config.js bilateral-enforcement channel, and the scanned-document
// regression (chromedp). The helpers at the bottom of plugin_test.go
// (newTestApp, newChrome, consoleCapture, waitTrue) are reused.

// stubExportHandler is a no-op export sink returning a fixed URL — enough for
// the mode / handler tests to get a 200 from /export without standing up storage.
func stubExportHandler(_ context.Context, _ ExportRequest) (string, error) {
	return "https://example/stored.pdf", nil
}

// TestModeEnforcement is the table-driven {mode × route} gate: the Go handlers
// must reject any payload the host-selected mode does not permit, REGARDLESS of
// capability (mode is checked first, before the body is read). UI-only gating is
// explicitly forbidden by the platform rules, so this is the load-bearing
// bilateral enforcement on the Go side. devGrantAll is on so the capability gate
// always passes — these cases isolate the MODE refusal.
func TestModeEnforcement(t *testing.T) {
	cases := []struct {
		name     string
		mode     Mode
		route    string
		body     string
		ct       string
		redact   bool // kind=redact on export
		want     int
		wantCode string
	}{
		// ModeView rejects both write surfaces entirely (view-only mount).
		{"view/save", ModeView, SaveURL, `{"docId":"d","doc":null,"schemaVersion":"pdf-v1"}`, "application/json", false, http.StatusForbidden, "E_MODE_DENIED"},
		{"view/export", ModeView, ExportURL, "x", "application/pdf", false, http.StatusForbidden, "E_MODE_DENIED"},
		// ModeAnnotate accepts save + non-redacting export…
		{"annotate/save", ModeAnnotate, SaveURL, `{"docId":"d","doc":null,"schemaVersion":"pdf-v1"}`, "application/json", false, http.StatusOK, ""},
		{"annotate/export", ModeAnnotate, ExportURL, "x", "application/pdf", false, http.StatusOK, ""},
		// …but a redacting export is still refused.
		{"annotate/export-redact", ModeAnnotate, ExportURL, "x", "application/pdf", true, http.StatusForbidden, "E_MODE_DENIED"},
		// ModeRedact accepts everything (incl. kind=redact).
		{"redact/export-redact", ModeRedact, ExportURL, "x", "application/pdf", true, http.StatusOK, ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// WithExportHandler satisfies ModeRedact's pdf:export requirement
			// AND gives the 200-Export cases a handler so the route can answer
			// (otherwise a nil handler would 500 and mask the mode result).
			app, _ := newTestApp(t, WithDevGrantAll(), WithMode(c.mode), WithExportHandler(stubExportHandler))
			srv := httptest.NewServer(app.Router())
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodPost, srv.URL+c.route, strings.NewReader(c.body))
			req.Header.Set("Content-Type", c.ct)
			if c.route == ExportURL {
				kind := ExportKindExport
				if c.redact {
					kind = ExportKindRedact
				}
				req.Header.Set("X-Export-Kind", kind)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			respBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(respBytes, &body)
			if resp.StatusCode != c.want {
				t.Errorf("status=%d want %d (code=%q); body=%s", resp.StatusCode, c.want, body.Error, respBytes)
			}
			if c.wantCode != "" && body.Error != c.wantCode {
				t.Errorf("error=%q want %q", body.Error, c.wantCode)
			}
		})
	}
}

// TestCapabilityEnforcement is the table-driven {route × capability} gate. The
// mode ALLOWS the action but the capability is NOT in the plugin's own grant
// set, so pluginhost.Allow denies on the module-grant side (ScopeMatch fails
// before caller authority is consulted) — the same E_CAPABILITY_DENIED outcome a
// scoped token lacking the capability would produce. All three routes answer
// 403 + E_CAPABILITY_DENIED via the platform helper — uniformity matters more
// here than protocol-v1.md's 412 prose, which the platform's own
// WriteCapabilityDenied does not implement (see handlers.go).
func TestCapabilityEnforcement(t *testing.T) {
	cases := []struct {
		name     string
		opts     []Option // grant set deliberately OMITS the required capability
		method   string
		route    string
		body     string
		kind     string
		want     int
		wantCode string
	}{
		{
			name:     "save/no-document-write",
			opts:     []Option{WithMode(ModeAnnotate), WithCapabilities("document:read", "theme:read")},
			method:   http.MethodPost,
			route:    SaveURL,
			body:     `{"docId":"d","doc":null,"schemaVersion":"pdf-v1"}`,
			want:     http.StatusForbidden,
			wantCode: "E_CAPABILITY_DENIED",
		},
		{
			// No WithExportHandler ⇒ pdf:export is NOT in the grant set.
			name:     "export/no-pdf-export",
			opts:     []Option{WithMode(ModeAnnotate)},
			method:   http.MethodPost,
			route:    ExportURL,
			body:     "x",
			kind:     ExportKindExport,
			want:     http.StatusForbidden,
			wantCode: "E_CAPABILITY_DENIED",
		},
		{
			name:     "doc/no-document-read",
			opts:     []Option{WithCapabilities("document:write", "theme:read")},
			method:   http.MethodGet,
			route:    RoutePrefix + "/doc/scan-jpx",
			want:     http.StatusForbidden,
			wantCode: "E_CAPABILITY_DENIED",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// Enforcing (no WithDevGrantAll). The plugin's grant set omits the
			// required capability, so Allow denies regardless of caller.
			app, _ := newTestApp(t, c.opts...)
			var body io.Reader
			if c.body != "" {
				body = strings.NewReader(c.body)
			}
			req := httptest.NewRequest(c.method, c.route, body)
			if c.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/json")
			}
			if c.kind != "" {
				req.Header.Set("X-Export-Kind", c.kind)
			}
			rr := httptest.NewRecorder()
			app.Router().ServeHTTP(rr, req)
			var resp struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			if rr.Code != c.want {
				t.Errorf("status=%d want %d; body=%s", rr.Code, c.want, rr.Body.String())
			}
			if resp.Error != c.wantCode {
				t.Errorf("error=%q want %q", resp.Error, c.wantCode)
			}
		})
	}
}

// TestMaxBytesRejection proves the host ceiling bites on BOTH byte-moving
// routes: /doc/{id} (resolved source larger than MaxBytes → 413 before the
// bytes are relayed into a postMessage) and /export (produced body larger than
// MaxBytes → 413 before the handler runs).
func TestMaxBytesRejection(t *testing.T) {
	t.Run("doc", func(t *testing.T) {
		big := bytes.Repeat([]byte("x"), 100)
		app, _ := newTestApp(t,
			WithDevGrantAll(),
			WithMaxBytes(50),
			WithSource(func(context.Context, string) ([]byte, error) { return big, nil }),
		)
		srv := httptest.NewServer(app.Router())
		defer srv.Close()
		resp, err := http.Get(srv.URL + RoutePrefix + "/doc/anything")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("status=%d want 413", resp.StatusCode)
		}
	})
	t.Run("export", func(t *testing.T) {
		app, _ := newTestApp(t,
			WithDevGrantAll(),
			WithMode(ModeAnnotate),
			WithMaxBytes(50),
			WithExportHandler(stubExportHandler),
		)
		srv := httptest.NewServer(app.Router())
		defer srv.Close()
		body := bytes.Repeat([]byte("x"), 100)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+ExportURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/pdf")
		req.Header.Set("X-Export-Kind", ExportKindExport)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("status=%d want 413", resp.StatusCode)
		}
	})
}

// TestSaveConflictMapsTo409 mirrors richtext's / monaco's contract: a save
// handler returning ErrConflict (bare or wrapped) surfaces as 409/E_CONFLICT,
// the one status the adapter relays to the frame as a distinct saveResult so
// the editor keeps the doc dirty and warns; any other error stays 500/E_SAVE.
func TestSaveConflictMapsTo409(t *testing.T) {
	postSave := func(t *testing.T, saveErr error) (int, string) {
		t.Helper()
		app, _ := newTestApp(t, WithDevGrantAll(), WithMode(ModeAnnotate), WithSaveHandler(
			func(context.Context, SaveRequest) error { return saveErr },
		))
		srv := httptest.NewServer(app.Router())
		defer srv.Close()
		payload := `{"docId":"demo","doc":null,"schemaVersion":"pdf-v1"}`
		resp, err := http.Post(srv.URL+SaveURL, "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("POST save: %v", err)
		}
		defer resp.Body.Close()
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body.Error
	}
	t.Run("bare ErrConflict", func(t *testing.T) {
		if s, c := postSave(t, ErrConflict); s != http.StatusConflict || c != "E_CONFLICT" {
			t.Errorf("got status=%d code=%q, want 409/E_CONFLICT", s, c)
		}
	})
	t.Run("wrapped ErrConflict", func(t *testing.T) {
		wrapped := fmt.Errorf("doc %q changed under the editor: %w", "demo", ErrConflict)
		if s, c := postSave(t, wrapped); s != http.StatusConflict || c != "E_CONFLICT" {
			t.Errorf("got status=%d code=%q, want 409/E_CONFLICT", s, c)
		}
	})
	t.Run("other error stays 500", func(t *testing.T) {
		if s, c := postSave(t, fmt.Errorf("disk full")); s != http.StatusInternalServerError || c != "E_SAVE" {
			t.Errorf("got status=%d code=%q, want 500/E_SAVE", s, c)
		}
	})
}

// TestModeRedactRequiresExportCapability proves the fail-loud construction
// guard: ModeRedact produces a brand-new redacted document, which is export
// egress, so constructing it without pdf:export declared panics at New() rather
// than silently shipping a half-wired redactor. Same posture as
// pluginhost.Manifest.Validate. Also pins the numeric-arg guards.
func TestModeRedactRequiresExportCapability(t *testing.T) {
	t.Run("redact without export panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic for ModeRedact without pdf:export")
			}
		}()
		_ = New(WithMode(ModeRedact))
	})
	t.Run("redact with export handler is fine", func(t *testing.T) {
		// WithExportHandler appends pdf:export, so ModeRedact should NOT panic.
		p := New(WithMode(ModeRedact), WithExportHandler(stubExportHandler))
		if !p.Mode().allowsRedact() {
			t.Error("Mode() should report redact after WithMode(ModeRedact)")
		}
		if !containsCap(p.Capabilities(), CapPDFExport) {
			t.Error("WithExportHandler should append pdf:export")
		}
	})
	t.Run("redact with explicit cap is fine", func(t *testing.T) {
		// A host can also declare the capability directly without a handler.
		p := New(WithMode(ModeRedact), WithCapabilities("document:read", "document:write", "theme:read", CapPDFExport))
		if !containsCap(p.Capabilities(), CapPDFExport) {
			t.Error("explicit pdf:export capability should be retained")
		}
	})
	t.Run("bad redact DPI panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic for out-of-range redact DPI")
			}
		}()
		_ = New(WithRedactDPI(10))
	})
	t.Run("zero max bytes panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic for zero MaxBytes")
			}
		}()
		_ = New(WithMaxBytes(0))
	})
}

// TestConfigScriptReflectsMode proves the bilateral-enforcement channel: the
// instance's mode reaches the frame via init.config (through config.js + the
// adapter merging window.__gofastrPdfConfig). The Go options marshal straight
// into the published global.
func TestConfigScriptReflectsMode(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithMode(ModeAnnotate|ModeRedact), WithExportHandler(stubExportHandler))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	resp, err := http.Get(srv.URL + ConfigScriptURL)
	if err != nil {
		t.Fatalf("GET config.js: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	got := string(body)
	for _, want := range []string{
		"window.__gofastrPdfConfig = ",
		`"mode":"redact"`, // highest tier the bits grant
		`"redactDPI":200`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.js missing %q; body=%q", want, got)
		}
	}
}

// TestScannedDocumentRendering guards the silent-failure mode the brief calls
// out: JPEG 2000 / JBIG2 scans decode through WebAssembly, which cannot
// instantiate under the framed CSP. The build inlines pdf.js's pure-JS
// fallbacks; before that fix a scanned page rendered as a BLANK WHITE PAGE with
// no error, no console message and no CSP violation — a user would mistake it
// for a redacted page. Per fixture we assert the rendered canvas is NON-BLANK
// (a healthy non-white pixel floor — JPX ≈181k, JBIG2 ≈316k at the spike's
// render scale — asserting > 10000 so a codec regression fails loud without
// being brittle to exact pixel counts).
func TestScannedDocumentRendering(t *testing.T) {
	fixtures := []struct {
		name string
		id   string
		path string
	}{
		{"jpx", "scan-jpx", filepath.Join("testdata", "scan-jpx.pdf")},
		{"jbig2", "scan-jbig2", filepath.Join("testdata", "scan-jbig2.pdf")},
	}
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			fixtureBytes, err := os.ReadFile(fx.path)
			if err != nil {
				t.Fatalf("read fixture %s: %v", fx.path, err)
			}
			// Serve the fixture through the WithSource seam: the demo mount
			// carries ?doc=<id>, the adapter fetches /doc/<id>, the source
			// returns these bytes. Never replace sample.pdf.
			app, _ := newTestApp(t,
				WithDevGrantAll(),
				WithDemoPage(),
				WithSource(func(_ context.Context, id string) ([]byte, error) {
					if id == fx.id {
						return fixtureBytes, nil
					}
					return nil, nil
				}),
			)
			srv := httptest.NewServer(app.Router())
			defer srv.Close()

			ctx, cancel := newChrome(t)
			defer cancel()
			cap := &consoleCapture{}
			chromedp.ListenTarget(ctx, cap.handle)
			if err := chromedp.Run(ctx, log.Enable()); err != nil {
				t.Fatalf("log.Enable: %v", err)
			}
			if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+DemoURL+"?doc="+fx.id)); err != nil {
				t.Fatalf("navigate: %v", err)
			}

			ready := waitTrue(ctx, t, `(function(){
				var f = document.querySelector('iframe');
				if (!f) return null;
				if (f.__pdfError) return { error: f.__pdfError };
				return { ready: !!f.__pdfRendered };
			})()`, 25*time.Second)
			if ready == nil {
				t.Fatalf("timed out waiting for %s render; console:\n%s",
					fx.name, strings.Join(cap.snapshotMessages(), "\n"))
			}
			if e, ok := ready["error"].(string); ok && e != "" {
				t.Fatalf("frame reported renderError for %s: %s\nconsole:\n%s",
					fx.name, e, strings.Join(cap.snapshotMessages(), "\n"))
			}
			var stats struct {
				NonBlank   bool    `json:"nonBlank"`
				NonWhitePx float64 `json:"nonWhitePixels"`
			}
			if err := chromedp.Run(ctx, chromedp.Evaluate(
				`(function(){var f=document.querySelector('iframe');return {nonBlank:f.__pdfNonBlank, nonWhitePixels:f.__pdfNonWhitePixels};})()`,
				&stats)); err != nil {
				t.Fatalf("read stats: %v", err)
			}
			// A scanned page decoded through the pure-JS fallback renders real
			// ink. The floor (10000) is well below both fixtures' healthy counts
			// (≈181k / ≈316k) so it does not flake on render-scale jitter, but
			// catches the blank-white-page regression hard.
			if !stats.NonBlank || stats.NonWhitePx <= 10000 {
				t.Errorf("%s: canvas blank or near-blank: nonBlank=%v nonWhitePixels=%v — the pure-JS codec fallback likely regressed (a blank scan is the dangerous silent failure this guards)",
					fx.name, stats.NonBlank, stats.NonWhitePx)
			} else {
				t.Logf("%s: %.0f non-white pixels (non-blank OK)", fx.name, stats.NonWhitePx)
			}
		})
	}
}
