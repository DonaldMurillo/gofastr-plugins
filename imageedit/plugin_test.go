package imageedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// --- test harness -----------------------------------------------------------

func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "imageedit-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// fullTestApp wires every handler so the demo page and all routes exist.
func fullTestApp(t *testing.T) (*framework.App, *Plugin) {
	t.Helper()
	app, p := newTestApp(t,
		WithDevGrantAll(),
		WithDemoPage(),
		WithUploadHandler(func(_ context.Context, req UploadRequest) (string, error) {
			return "up-test", nil
		}),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) {
			return "/imageedit/exported/test", nil
		}),
	)
	return app, p
}

func postJSON(t *testing.T, url string, body any) (*http.Response, string) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

func postRaw(t *testing.T, url string, contentType string, body []byte) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

func getBody(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// decodePNG decodes exported bytes for pixel assertions.
func decodePNG(t *testing.T, b []byte) *image.NRGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode exported png: %v", err)
	}
	return toNRGBA(img)
}

func sampleDoc() Doc {
	return Doc{
		SchemaVersion: SchemaVersion,
		Src:           SrcRef{Kind: "id", Ref: "demo"},
		Rotate:        0,
		Annotations:   []Annotation{},
		Redactions:    []Redaction{},
	}
}

// --- assets -----------------------------------------------------------------

func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{EditorHTMLURL, "text/html; charset=utf-8"},
		{EditorJSURL, "text/javascript; charset=utf-8"},
		{EditorCSSURL, "text/css; charset=utf-8"},
		{AdapterScriptURL, "text/javascript; charset=utf-8"},
		{ConfigScriptURL, "text/javascript; charset=utf-8"},
	}
	for _, c := range cases {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Content-Type") != c.wantCT {
			t.Errorf("%s: content-type=%q want %q", c.path, resp.Header.Get("Content-Type"), c.wantCT)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%s", c.path, resp.StatusCode, body)
		}
	}
}

// TestFramedAssetsCarryHeaderRelaxation proves the platform AssetServer
// carries the framing/CORP/CSP relaxation AND that the fixed framedCSP sets
// connect-src 'none' + sandbox allow-scripts — the directives that make the
// image cross the bridge instead of being fetched by the frame.
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{EditorHTMLURL, EditorJSURL, EditorCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d", path, resp.StatusCode)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "connect-src 'none'") {
			t.Errorf("%s: framed CSP missing connect-src 'none': %q", path, csp)
		}
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: framed CSP missing sandbox allow-scripts: %q", path, csp)
		}
		if resp.Header.Get("Cross-Origin-Resource-Policy") == "" {
			t.Errorf("%s: missing CORP relaxation", path)
		}
	}
}

func TestDemoPageContainsMountAndBroker(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	for _, want := range []string{
		`data-fui-plugin="imageedit"`,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
		ConfigScriptURL,
		"connect-src 'none'",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

func TestMountPublishesFieldName(t *testing.T) {
	custom := Mount(MountConfig{DocID: "photos", Field: "photo_ops"})
	html := string(custom)
	for _, want := range []string{
		`data-fui-plugin-field="photo_ops"`,
		`name="photo_ops"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("Mount(custom field) missing %q in:\n%s", want, html)
		}
	}
	def := Mount(MountConfig{DocID: "photos"})
	if !strings.Contains(string(def), `data-fui-plugin-field="imageedit_doc"`) {
		t.Errorf("Mount(default) missing data-fui-plugin-field=\"imageedit_doc\":\n%s", def)
	}
}

// --- the shared fixture: THE agreement test's Go half ------------------------
//
// fixtureDoc is a fixed operation list over the sample image exercising every
// op: crop, rotate, a rect annotation, an arrow, text, and one redaction
// over the token. fixturePoints are sample points (in OUTPUT coordinates)
// with the RGBA the correct pipeline must produce. The e2e journey mirrors
// the same ops through the UI and compares the frame's 1:1 render against
// the server's export at its own sample points; this test pins the SERVER
// side against exact values so the two halves anchor the same pipeline.

func fixtureDoc() Doc {
	tok := SampleTokenRect()
	return Doc{
		SchemaVersion: SchemaVersion,
		Src:           SrcRef{Kind: "id", Ref: "demo"},
		Crop:          &Rect{X: 20, Y: 40, W: 900, H: 560},
		Rotate:        90,
		Annotations: []Annotation{
			{ID: "a1", Type: "rect", Color: "#D0342C", Width: 6, X: 120, Y: 160, W: 200, H: 120},
			{ID: "a2", Type: "arrow", Color: "#1C7ED6", Width: 5, X: 700, Y: 500, X2: 820, Y2: 420},
			{ID: "a3", Type: "text", Color: "#2F9E44", Width: 4, Size: 6, X: 200, Y: 560, Text: "APPROVED 042"},
		},
		Redactions: []Redaction{
			// The token rect, expressed in source coordinates. Under crop
			// (x+20,y+40) and rotate 90 it maps into the output — the test
			// below samples inside it expecting pure black.
			{ID: "r1", Rect: tok, Fill: "#000000"},
		},
	}
}

func TestPreviewServerAgreementGolden(t *testing.T) {
	// A dedicated source so the fixture is independent of the demo sample's
	// evolution: a deterministic gradient + blocks image.
	src := fixtureSource(t)
	doc := fixtureDoc()
	doc.Src = SrcRef{Kind: "id", Ref: "fixture"}

	out, err := renderDoc(src, doc, "", 90)
	if err != nil {
		t.Fatalf("renderDoc(fixture): %v", err)
	}

	// Composed dims: crop 900×560 rotated 90° CW → 560×900.
	if out.Width != 560 || out.Height != 900 {
		t.Fatalf("composed dims = %d×%d, want 560×900 (crop 900×560 rotated 90°)", out.Width, out.Height)
	}
	img := decodePNG(t, out.Bytes)
	px := func(x, y int) (uint8, uint8, uint8) {
		i := (y*img.Rect.Dx() + x) * 4
		return img.Pix[i], img.Pix[i+1], img.Pix[i+2]
	}

	// (1) Rotation mapping: source (20+0, 40+0) is fixtureSource's top-left
	// RED block. Under crop+rotate90 it lands at output (560-1-0, 0).
	// The red block is 100×100 at the crop origin, so output (559,0)..(559,99)
	// x (0..99 horizontal span? no: a source 100×100 block maps to a
	// 100(w)×100(h) region at output x ∈ [560-100, 560), y ∈ [0, 100).
	r, g, b := px(510, 50)
	if r < 200 || g > 60 || b > 60 {
		t.Errorf("rotated red block at (510,50) = (%d,%d,%d), want red", r, g, b)
	}

	// (2) The source's GREEN block (0..100, 500..600) straddles the crop's
	// left edge (crop.X=20) and bottom (crop.Y+H=600): the surviving half
	// (local x∈[0,80), y∈[460,560)) maps under rotate90 to outX∈(0..99],
	// outY∈[0,80). Sample the middle of that region.
	r, g, b = px(49, 40)
	if g < 200 || r > 60 || b > 60 {
		t.Errorf("source green block (cropped in) at output (49,40) = (%d,%d,%d), want green", r, g, b)
	}

	// (3) Rect annotation: source rect (120,160,200×120) → local
	// (100,120)-(300,240); rotate90 maps it to outX∈[319,439), outY∈[100,300).
	// Stroke width 6 → the top border occupies outY∈[100,106) fully across.
	r, g, b = px(380, 103)
	if r < 180 || g > 90 {
		t.Errorf("rect annotation top border at (380,103) = (%d,%d,%d), want #D0342C red", r, g, b)
	}
	// And the interior must NOT be the annotation color (stroked, not filled).
	r, g, b = px(380, 200)
	if r < 180 && g <= 90 {
		t.Errorf("rect annotation interior at (380,200) = (%d,%d,%d), want background (stroked, not filled)", r, g, b)
	}

	// (4) Text annotation renders the bitmap font: "APPROVED 042" at source
	// (200,560) → local (180,520) → output anchor (559-520, 180) = (39,180).
	// 'A' glyph row 3 is the full-width "#####" row; its first ink block at
	// scale 6 covers (39..44, 198..203).
	r, g, b = px(42, 200)
	if g < 130 || r > 90 {
		t.Errorf("text annotation 'A' crossbar at (42,200) = (%d,%d,%d), want #2F9E44 green", r, g, b)
	}

	// (5) Redaction: the token region must be pure black in the output.
	rep := out.Report
	if !rep.Pass || rep.RedactionsChecked != 1 || rep.EXIFStripped != true {
		t.Fatalf("report = %+v, want pass with 1 checked redaction + exif stripped", rep)
	}
	if len(rep.Vacuous) != 0 {
		t.Errorf("redaction reported vacuous (%v) — the fixture token region must have content", rep.Vacuous)
	}
	// Sample inside the mapped token rect (output space).
	tok := SampleTokenRect()
	m := mapRect(tok, effectiveCrop(mustDecode(t, src), doc.Crop), doc.Rotate)
	for _, d := range [][2]int{{m.X + 5, m.Y + 5}, {m.X + m.W/2, m.Y + m.H/2}, {m.X + m.W - 5, m.Y + m.H - 5}} {
		r, g, b = px(d[0], d[1])
		if r != 0 || g != 0 || b != 0 {
			t.Errorf("redacted token pixel at %v = (%d,%d,%d), want (0,0,0)", d, r, g, b)
		}
	}
}

func mustDecode(t *testing.T, src []byte) *image.NRGBA {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("decode fixture source: %v", err)
	}
	return toNRGBA(img)
}

// fixtureSource is a deterministic 920×600 PNG: a red block at the top-left,
// a blue block at the top-right, a green block straddling the crop bottom
// edge, and a gradient background. Blocks are 100×100 at (0,0), (820,0) and
// (0,500).
func fixtureSource(t *testing.T) []byte {
	t.Helper()
	const w, h = 920, 600
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			i := (y*w + x) * 4
			img.Pix[i] = uint8(x * 255 / (w - 1))
			img.Pix[i+1] = uint8(y * 255 / (h - 1))
			img.Pix[i+2] = 128
			img.Pix[i+3] = 255
		}
	}
	red := rgba{R: 220, G: 30, B: 30, A: 255}
	blue := rgba{R: 30, G: 60, B: 220, A: 255}
	green := rgba{R: 30, G: 200, B: 60, A: 255}
	fillRect(img, 0, 0, 100, 100, red)
	fillRect(img, 820, 0, 100, 100, blue)
	fillRect(img, 0, 500, 100, 100, green)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture source: %v", err)
	}
	return buf.Bytes()
}

// --- redaction actually removes (the brief's test #2) -------------------------

func TestRedactionActuallyRemoves(t *testing.T) {
	tok := SampleTokenRect()
	doc := sampleDoc()
	doc.Redactions = []Redaction{{ID: "r1", Rect: tok, Fill: "#000000"}}

	out, err := renderDoc(SampleImage(), doc, "", 90)
	if err != nil {
		t.Fatalf("renderDoc: %v", err)
	}
	img := decodePNG(t, out.Bytes)

	// The token's ORIGINAL ink is a saturated red (#C02E22-ish) on white.
	// After redaction every pixel in the rect must be the fill (black)…
	src := mustDecode(t, SampleImage())
	originalNonBlack := 0
	for y := tok.Y; y < tok.Y+tok.H; y++ {
		for x := tok.X; x < tok.X+tok.W; x++ {
			i := (y*src.Rect.Dx() + x) * 4
			if src.Pix[i] > 40 || src.Pix[i+1] > 40 || src.Pix[i+2] > 40 {
				originalNonBlack++
			}
		}
	}
	if originalNonBlack < 100 {
		t.Fatalf("fixture token region has only %d non-black source pixels — the assertion would be vacuous", originalNonBlack)
	}
	// …and the output region is uniformly black.
	for y := tok.Y; y < tok.Y+tok.H; y += 3 {
		for x := tok.X; x < tok.X+tok.W; x += 3 {
			i := (y*img.Rect.Dx() + x) * 4
			if img.Pix[i] != 0 || img.Pix[i+1] != 0 || img.Pix[i+2] != 0 {
				t.Fatalf("output pixel (%d,%d) in redacted rect = (%d,%d,%d), want (0,0,0)",
					x, y, img.Pix[i], img.Pix[i+1], img.Pix[i+2])
			}
		}
	}
}

// TestRedactionVerifierRejectsLeak grades the verifier itself: compose
// WITHOUT redactions (the "covered but present" failure mode — exactly what
// a cosmetic redaction would produce), then require verifyRedactions to
// fail it. A verifier that only checks its own successful pipeline proves
// nothing.
func TestRedactionVerifierRejectsLeak(t *testing.T) {
	tok := SampleTokenRect()
	leakDoc := sampleDoc() // NO redactions applied
	src := mustDecode(t, SampleImage())
	res, err := compose(src, leakDoc)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	// Claim a redaction that was never painted.
	claimed := Doc{Redactions: []Redaction{{ID: "r1", Rect: tok, Fill: "#000000"}}}
	rep := verifyRedactions(res.Out, res.Pre, []Rect{tok}, claimed)
	if rep.Pass {
		t.Fatal("verifier passed an image whose redacted region still carries the original pixels")
	}
	if len(rep.Failed) != 1 || rep.Failed[0] != "r1" {
		t.Errorf("verifier failed-ids = %v, want [r1]", rep.Failed)
	}
}

// --- EXIF stripped (the brief's test #3) --------------------------------------

// exifJPEG builds a real JPEG whose bytes carry an EXIF APP1 segment
// (including the "Exif\0\0" signature and a TIFF header). Go's decoder
// ignores the segment; the plugin's output must not contain it.
func exifJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	fillRect(img, 10, 10, 40, 40, rgba{R: 200, G: 40, B: 40, A: 255})
	var clean bytes.Buffer
	if err := jpeg.Encode(&clean, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	src := clean.Bytes()
	// Splice an APP1 Exif segment right after the SOI marker (FFD8).
	segment := []byte{0xFF, 0xE1, 0x00, 0x14} // APP1, length 20
	segment = append(segment, []byte("Exif\x00\x00")...)
	segment = append(segment, []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00}...)
	withExif := append([]byte{0xFF, 0xD8}, segment...)
	withExif = append(withExif, src[2:]...)
	if !bytes.Contains(withExif, []byte("Exif\x00\x00")) {
		t.Fatal("test fixture failed to embed an EXIF signature")
	}
	return withExif
}

func TestEXIFStrippedFromOutput(t *testing.T) {
	withExif := exifJPEG(t)
	doc := sampleDoc()
	out, err := renderDoc(withExif, doc, "", 90)
	if err != nil {
		t.Fatalf("renderDoc(jpeg with exif): %v", err)
	}
	if bytes.Contains(out.Bytes, []byte("Exif\x00\x00")) {
		t.Error("output JPEG still carries an Exif APP1 signature — EXIF strip failed")
	}
	if !out.Report.EXIFStripped {
		t.Error("report.EXIFStripped = false, want true")
	}
	// And the output is still a decodable JPEG of the right size.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out.Bytes))
	if err != nil || cfg.Width != 64 || cfg.Height != 64 {
		t.Errorf("output decode = %v %dx%d, want 64×64 jpeg", err, cfg.Width, cfg.Height)
	}
	if out.Format != "jpeg" {
		t.Errorf("output format = %q, want jpeg (source-family preserved)", out.Format)
	}
}

// --- size and dimension caps (the brief's test #4) ----------------------------

// oversizedPNGHeader is a PNG whose IHDR declares 20000×20000 with no pixel
// data — enough for image.DecodeConfig, and the point of the cap: rejecting
// at the header means the pixels are never allocated. Built by rewriting a
// real 1×1 encode's IHDR and fixing its CRC.
func oversizedPNGHeader() []byte {
	tiny := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	if err := png.Encode(&buf, tiny); err != nil {
		panic(err)
	}
	out := buf.Bytes()
	// IHDR data (width,height) sits at bytes 16..24; overwrite with 20000s.
	binary.BigEndian.PutUint32(out[16:20], 20000)
	binary.BigEndian.PutUint32(out[20:24], 20000)
	// Fix the IHDR CRC (chunk = length+type+data = out[12:29], CRC at 29:33):
	// image/png verifies chunk checksums even in DecodeConfig.
	binary.BigEndian.PutUint32(out[29:33], crc32.ChecksumIEEE(out[12:29]))
	return out
}

func TestCapsRejectOversizedBeforeDecode(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithSource(func(_ context.Context, id string) ([]byte, error) {
		return oversizedPNGHeader(), nil
	}))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// GET /img/big: refused at the header stage with 413, never shipped.
	resp, body := getBody(t, srv.URL+RoutePrefix+"/img/big")
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("GET /img/big status = %d body=%s, want 413", resp.StatusCode, body)
	}
	if !strings.Contains(body, "E_TOO_LARGE") {
		t.Errorf("GET /img/big body missing E_TOO_LARGE: %s", body)
	}

	// POST /export naming the same oversized source: same refusal, same
	// code, BEFORE any decode.
	doc := sampleDoc()
	doc.Src.Ref = "big"
	resp2, body2 := postJSON(t, srv.URL+ExportURL, map[string]any{"docId": "demo", "doc": doc})
	if resp2.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body2, "E_TOO_LARGE") {
		t.Fatalf("export oversized status=%d body=%s, want 413 E_TOO_LARGE", resp2.StatusCode, body2)
	}
}

// --- the image route ----------------------------------------------------------

func TestImageRouteServesSampleWithDims(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + RoutePrefix + "/img/demo")
	if err != nil {
		t.Fatalf("GET /img/demo: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("content-type = %q, want image/png", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Image-Width") != "960" || resp.Header.Get("X-Image-Height") != "640" {
		t.Errorf("dims headers = %q×%q, want 960×640", resp.Header.Get("X-Image-Width"), resp.Header.Get("X-Image-Height"))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width != 960 || cfg.Height != 640 {
		t.Errorf("served bytes decode = %v %d×%d, want 960×640 png", err, cfg.Width, cfg.Height)
	}
}

func TestImageRouteUnknownIDIs404(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithSource(func(_ context.Context, id string) ([]byte, error) {
		return nil, nil // (nil, nil) = no such image
	}))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	resp, body := getBody(t, srv.URL+RoutePrefix+"/img/nope")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "E_NOT_FOUND") {
		t.Fatalf("status=%d body=%s, want 404 E_NOT_FOUND", resp.StatusCode, body)
	}
}

// --- capability gates ---------------------------------------------------------

func TestRoutesDenyWithoutGrant(t *testing.T) {
	// A scoped token whose scopes lack every imageedit capability: the gate
	// default-denies on the caller-authority side. (An anonymous caller
	// PASSES by design — Allow is a capability gate, not authentication —
	// which is exactly why this test uses a token.)
	app, _ := newTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	do := func(method, url string, body []byte) (*http.Response, string) {
		req := httptest.NewRequest(method, srv.URL+url, bytes.NewReader(body)).
			WithContext(deniedCtx)
		rec := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(rec, req) //nolint — direct handler use keeps ctx scopes
		resp := rec.Result()
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(raw)
	}

	resp, body := do(http.MethodGet, RoutePrefix+"/img/demo", nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "E_CAPABILITY_DENIED") {
		t.Errorf("GET img status=%d body=%s, want 403 E_CAPABILITY_DENIED", resp.StatusCode, body)
	}
	for _, route := range []string{SaveURL, ExportURL, UploadURL} {
		payload := []byte(`{"docId":"demo","doc":null}`)
		if route == UploadURL {
			payload = SampleImage()
		}
		resp, body := do(http.MethodPost, route, payload)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "E_CAPABILITY_DENIED") {
			t.Errorf("POST %s status=%d body=%s, want 403 E_CAPABILITY_DENIED", route, resp.StatusCode, body)
		}
	}
}

func TestUploadFailsClosedWithoutHandler(t *testing.T) {
	// WithDevGrantAll bypasses the grant side; the route must still refuse
	// (500 E_UPLOAD) rather than nil-deref when no handler was wired.
	app, _ := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, body := postRaw(t, srv.URL+UploadURL, "image/png", SampleImage())
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "E_UPLOAD") {
		t.Fatalf("upload without handler status=%d body=%s, want 500 E_UPLOAD (fail closed)", resp.StatusCode, body)
	}
}

func TestUploadRoundTripStoresAndServes(t *testing.T) {
	uploads := map[string][]byte{}
	app, _ := newTestApp(t,
		WithDevGrantAll(),
		WithSource(func(_ context.Context, id string) ([]byte, error) {
			if b, ok := uploads[id]; ok {
				return b, nil
			}
			return nil, nil
		}),
		WithUploadHandler(func(_ context.Context, req UploadRequest) (string, error) {
			id := "up-" + digestHex(req.Bytes)[:8]
			uploads[id] = req.Bytes
			return id, nil
		}),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, body := postRaw(t, srv.URL+UploadURL, "image/png", SampleImage())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, body)
	}
	var got struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.ID == "" {
		t.Fatalf("upload body = %s (%v), want {id}", body, err)
	}

	// The uploaded id resolves through /img/{id} — the doc.src loop closes.
	resp2, err := http.Get(srv.URL + RoutePrefix + "/img/" + got.ID)
	if err != nil {
		t.Fatalf("GET uploaded: %v", err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || !bytes.Equal(b2, SampleImage()) {
		t.Errorf("uploaded id served %d bytes (status %d), want the exact uploaded bytes", len(b2), resp2.StatusCode)
	}
}

func TestUploadRejectsUndecodableAndCapsDimensions(t *testing.T) {
	app, _ := newTestApp(t,
		WithDevGrantAll(),
		WithUploadHandler(func(_ context.Context, req UploadRequest) (string, error) {
			return "never", nil
		}),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, body := postRaw(t, srv.URL+UploadURL, "image/png", []byte("definitely not an image"))
	if resp.StatusCode != http.StatusUnsupportedMediaType || !strings.Contains(body, "E_BAD_FORMAT") {
		t.Errorf("garbage upload status=%d body=%s, want 415 E_BAD_FORMAT", resp.StatusCode, body)
	}
	resp, body = postRaw(t, srv.URL+UploadURL, "image/png", oversizedPNGHeader())
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body, "E_TOO_LARGE") {
		t.Errorf("oversized upload status=%d body=%s, want 413 E_TOO_LARGE", resp.StatusCode, body)
	}
}

// --- /save --------------------------------------------------------------------

func TestSaveValidatesAndPersistsDoc(t *testing.T) {
	app, p := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	doc := sampleDoc()
	doc.Rotate = 90
	doc.Annotations = []Annotation{{ID: "a1", Type: "arrow", Color: "#111111", Width: 3, X: 10, Y: 10, X2: 40, Y2: 40}}
	resp, body := postJSON(t, srv.URL+SaveURL, map[string]any{"docId": "p1", "doc": doc, "schemaVersion": SchemaVersion})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d body=%s", resp.StatusCode, body)
	}
	got, ok := p.LoadDoc(nil, "p1")
	if !ok || !strings.Contains(got, `"rotate":90`) {
		t.Errorf("LoadDoc = %q ok=%v, want persisted doc with rotate 90", got, ok)
	}

	// Structural refusals: bad rotate, bad color, unknown type, too many ops.
	bad := sampleDoc()
	bad.Rotate = 45
	resp, body = postJSON(t, srv.URL+SaveURL, map[string]any{"docId": "p1", "doc": bad})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "E_BAD_DOC") {
		t.Errorf("rotate 45 status=%d body=%s, want 400 E_BAD_DOC", resp.StatusCode, body)
	}
	bad = sampleDoc()
	bad.Annotations = []Annotation{{ID: "a1", Type: "rect", Color: "red", Width: 3, W: 10, H: 10}}
	resp, body = postJSON(t, srv.URL+SaveURL, map[string]any{"docId": "p1", "doc": bad})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "E_BAD_DOC") {
		t.Errorf("color 'red' status=%d body=%s, want 400 E_BAD_DOC", resp.StatusCode, body)
	}
	bad = sampleDoc()
	bad.Annotations = make([]Annotation, 100)
	for i := range bad.Annotations {
		bad.Annotations[i] = Annotation{ID: "a", Type: "rect", Color: "#111111", Width: 2, W: 5, H: 5}
	}
	resp, body = postJSON(t, srv.URL+SaveURL, map[string]any{"docId": "p1", "doc": bad})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "E_BAD_DOC") {
		t.Errorf("100 annotations status=%d body=%s, want 400 E_BAD_DOC", resp.StatusCode, body)
	}
}

// --- /export -------------------------------------------------------------------

func TestExportAppliesOpsServerSide(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	doc := sampleDoc()
	doc.Crop = &Rect{X: 0, Y: 0, W: 480, H: 320}
	doc.Rotate = 180
	tok := SampleTokenRect()
	doc.Redactions = []Redaction{{ID: "r1", Rect: tok, Fill: "#000000"}}

	resp, body := postJSON(t, srv.URL+ExportURL, map[string]any{"docId": "demo", "doc": doc})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export status=%d body=%s", resp.StatusCode, body)
	}
	var got struct {
		URL    string `json:"url"`
		Format string `json:"format"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
		Verify bool   `json:"verify"`
		Report struct {
			Pass              bool     `json:"pass"`
			RedactionsChecked int      `json:"redactionsChecked"`
			EXIFStripped      bool     `json:"exifStripped"`
			Failed            []string `json:"failed"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("export body decode: %v\n%s", err, body)
	}
	if got.URL == "" || got.Format != "png" || got.Width != 480 || got.Height != 320 {
		t.Errorf("export facts = %+v, want png 480×320 with url", got)
	}
	if !got.Verify || !got.Report.Pass || got.Report.RedactionsChecked != 1 || !got.Report.EXIFStripped || len(got.Report.Failed) != 0 {
		t.Errorf("export report = %+v, want pass / 1 checked / exif stripped / no failures", got.Report)
	}
	if got.SHA256 == "" || got.Bytes == 0 {
		t.Errorf("export digest facts missing: %+v", got)
	}
}

func TestExportRefusesDigestMismatch(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	doc := sampleDoc()
	doc.Src.SHA256 = "deadbeef" // not the sample's digest
	resp, body := postJSON(t, srv.URL+ExportURL, map[string]any{"docId": "demo", "doc": doc})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "E_SRC_MISMATCH") {
		t.Fatalf("digest mismatch status=%d body=%s, want 400 E_SRC_MISMATCH", resp.StatusCode, body)
	}
}

func TestExportWithCorrectDigestPasses(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	doc := sampleDoc()
	doc.Src.SHA256 = digestHex(SampleImage())
	resp, body := postJSON(t, srv.URL+ExportURL, map[string]any{"docId": "demo", "doc": doc})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("matching digest export status=%d body=%s", resp.StatusCode, body)
	}
}

func TestExportRejectsCropOutsideImage(t *testing.T) {
	app, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	doc := sampleDoc()
	doc.Crop = &Rect{X: 5000, Y: 5000, W: 100, H: 100}
	resp, body := postJSON(t, srv.URL+ExportURL, map[string]any{"docId": "demo", "doc": doc})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "E_BAD_DOC") {
		t.Fatalf("crop outside status=%d body=%s, want 400 E_BAD_DOC", resp.StatusCode, body)
	}
}

// --- construction guards --------------------------------------------------------

func TestWildcardGrantWithoutHandlerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(WithCapabilities(\"*:*\")) without an upload handler must panic at construction")
		}
	}()
	_ = New(WithCapabilities("*:*"))
}

func TestWildcardGrantWithHandlerConstructs(t *testing.T) {
	p := New(
		WithCapabilities("*:*"),
		WithUploadHandler(func(_ context.Context, req UploadRequest) (string, error) { return "ok", nil }),
	)
	found := false
	for _, c := range p.Capabilities() {
		if c == CapUploadImages {
			found = true
		}
	}
	if !found {
		t.Errorf("capabilities = %v, want upload:images appended (wildcard implies it)", p.Capabilities())
	}
}

func TestBadJPEGQualityPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(WithJPEGQuality(0)) must panic")
		}
	}()
	_ = New(WithJPEGQuality(0))
}

// --- the sample and the font -----------------------------------------------------

func TestSampleImageDeterministic(t *testing.T) {
	a, b := SampleImage(), SampleImage()
	if !bytes.Equal(a, b) {
		t.Fatal("SampleImage() is not deterministic across calls")
	}
	img := mustDecode(t, a)
	if img.Rect.Dx() != 960 || img.Rect.Dy() != 640 {
		t.Fatalf("sample dims = %d×%d, want 960×640", img.Rect.Dx(), img.Rect.Dy())
	}
	// The token region carries non-background ink to redact.
	tok := SampleTokenRect()
	ink := 0
	for y := tok.Y; y < tok.Y+tok.H; y++ {
		for x := tok.X; x < tok.X+tok.W; x++ {
			i := (y*img.Rect.Dx() + x) * 4
			// The secret renders in #C02E22: red-dominant.
			if img.Pix[i] > 150 && img.Pix[i+1] < 90 {
				ink++
			}
		}
	}
	if ink < 200 {
		t.Fatalf("token region has only %d red-ink pixels — the sample lost its secret", ink)
	}
}

func TestFontTableWellFormed(t *testing.T) {
	if len(bitmapFont) < 40 {
		t.Fatalf("font table has %d glyphs, want the full A-Z 0-9 punctuation set", len(bitmapFont))
	}
	for ch, rows := range bitmapFont {
		for row, bits := range rows {
			if bits > 0x1F {
				t.Errorf("glyph %q row %d = %#x exceeds 5 bits", ch, row, bits)
			}
		}
	}
	// The sample's token text must be fully renderable.
	for _, ch := range tokenText {
		if _, ok := bitmapFont[ch]; !ok {
			t.Errorf("token character %q missing from the font table", ch)
		}
	}
	if want := len(tokenText) * 6 * tokenScale; TextWidth(tokenText, tokenScale) != want {
		t.Errorf("TextWidth(%q) = %d, want %d", tokenText, TextWidth(tokenText, tokenScale), want)
	}
}

// TestMapPointRotateInverse pins the exact forward/inverse rotation math the
// frame's displayToSource relies on (its 270° case was wrong once — this is
// the tripwire for that class of bug).
func TestMapPointRotateInverse(t *testing.T) {
	crop := Rect{X: 10, Y: 20, W: 300, H: 200}
	for _, rot := range []int{0, 90, 180, 270} {
		for _, p := range [][2]int{{10, 20}, {15, 25}, {309, 219}, {150, 120}} {
			ox, oy := mapPoint(p[0], p[1], crop, rot)
			// The frame-side inverse (displayToSource) logic, restated here:
			var lx, ly int
			switch rot {
			case 90:
				lx, ly = oy, crop.H-1-ox
			case 180:
				lx, ly = crop.W-1-ox, crop.H-1-oy
			case 270:
				lx, ly = crop.W-1-oy, ox
			default:
				lx, ly = ox, oy
			}
			if lx+crop.X != p[0] || ly+crop.Y != p[1] {
				t.Errorf("rot %d: forward(%v)=%v inverse=(%d,%d) ≠ source", rot, p, [2]int{ox, oy}, lx, ly)
			}
		}
	}
}

// TestRenderDocVerifiesDimsAndEXIFFields proves the report's boolean fields
// are actually derived from the produced bytes (not hard-coded true).
func TestRenderDocReportFieldsDerived(t *testing.T) {
	doc := sampleDoc()
	out, err := renderDoc(SampleImage(), doc, "", 90)
	if err != nil {
		t.Fatalf("renderDoc: %v", err)
	}
	if !out.Report.DimensionsMatch {
		t.Error("DimensionsMatch = false on a clean render")
	}
	// Feed the scan a doctored "output" to show it can fail: scanEXIF over
	// bytes carrying the signature must report false.
	if scanEXIF("jpeg", exifJPEG(t)) {
		t.Error("scanEXIF(jpeg with Exif signature) = true, want false")
	}
	if !scanEXIF("png", out.Bytes) {
		t.Error("scanEXIF(clean png) = false, want true")
	}
}

func TestRenderDocRejectsUnknownFormat(t *testing.T) {
	doc := sampleDoc()
	if _, err := renderDoc([]byte("not an image at all"), doc, "", 90); err != ErrUnsupportedFormat {
		t.Errorf("renderDoc(garbage) err = %v, want ErrUnsupportedFormat", err)
	}
}

var _ = fmt.Sprintf // keep fmt for future assertions without churn
