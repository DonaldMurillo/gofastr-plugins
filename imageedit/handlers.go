package imageedit

// handlers.go implements the four RPC routes (protocol-v1.md §10):
//
//   - GET  /img/{id}  resolve + serve the source image (gate document:read)
//   - POST /upload    a new source image in          (gate upload:images)
//   - POST /save      operation-list persist         (gate document:write)
//   - POST /export    authoritative re-render        (gate document:write)
//
// A capability denial is 403 + E_CAPABILITY_DENIED on every route, via the
// platform's pluginhost.WriteCapabilityDenied.
//
// pluginhost.Allow is a capability gate, NOT authentication: it passes for
// anonymous callers (and for unscoped sessions it is bounded only by the
// plugin's grant set). Any route that WRITES — /save, /export and /upload
// above all — therefore relies on the host's own handler to check the
// session before persisting. The demo's WithDevGrantAll skips the gate
// entirely and MUST NOT survive into a production mount.
//
// Every route also fails CLOSED on an unwired handler (a clear error
// response, never a nil-deref): WithDevGrantAll bypasses the grant side of
// the gate, so it must not be able to reach a nil handler either — a panic
// inside an HTTP handler is a denial of service on the whole host process.
//
// Enforcement order on the mutating routes is deliberate and uniform: the
// capability gate first (caller authority — no body read), then the body
// size ceiling, then payload validation. Each failure maps to a distinct
// status + code so the adapter and the tests can tell a policy refusal from
// a bad payload from an oversized one.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"

	// Decoder registration for the two accepted formats.
	_ "image/jpeg"
	_ "image/png"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// maxEnvelopeBytes caps the /save and /export JSON envelopes (the doc is an
// operation list — always tiny). Image bytes (/img, /upload, and /export's
// produced output) are bounded by the plugin's maxBytes instead.
const maxEnvelopeBytes int64 = 64 << 10 // 64 KiB

// --- /img/{id} --------------------------------------------------------------

// handleImage implements GET ImageRoute: resolve the doc id to image bytes
// via [WithSource] and return them, plus header-level facts (format, width,
// height) the adapter relays alongside the bytes.
//
// This route is called ONLY by the HOST PAGE adapter — never by the frame.
// The frame runs under connect-src 'none' and an opaque origin, so it cannot
// fetch /img/{id} even if it wanted to; the host adapter fetches it
// same-origin (session cookie + CSRF token attached) and relays the bytes
// over the postMessage bridge. That separation is the reason authorization
// stays here at the data layer, not in the frame — the frame is untrusted
// by construction.
//
// The dimension caps run on the decoded HEADER before the bytes ship: an
// oversized image is refused here (413), so the frame is never handed
// something the host would refuse to re-render at export time.
func (p *Plugin) handleImage(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:read") {
		writeJSONCapabilityDenied(w, "document:read")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "missing image id")
		return
	}
	src, err := p.source(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_DOC", err.Error())
		return
	}
	if len(src) == 0 {
		// (nil, nil) — or an empty slice — from the resolver means the id
		// resolves to nothing: a 404, distinct from a transient source error.
		writeJSONError(w, http.StatusNotFound, "E_NOT_FOUND", "unknown image id")
		return
	}
	if int64(len(src)) > p.maxBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE",
			"image exceeds MaxBytes")
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil || format == "" {
		writeJSONError(w, http.StatusUnsupportedMediaType, "E_BAD_FORMAT",
			"source is not a decodable png or jpeg")
		return
	}
	if cfg.Width > p.maxDim || cfg.Height > p.maxDim || cfg.Width*cfg.Height > p.maxPixels {
		// The 413 fires on the HEADER decode — the pixels are never
		// materialized server-side, and never shipped across the bridge.
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE",
			fmt.Sprintf("image is %d×%d; exceeds the dimension or pixel cap", cfg.Width, cfg.Height))
		return
	}
	if format == "jpg" {
		format = "jpeg"
	}
	w.Header().Set("Content-Type", "image/"+format)
	w.Header().Set("X-Image-Format", format)
	w.Header().Set("X-Image-Width", fmt.Sprint(cfg.Width))
	w.Header().Set("X-Image-Height", fmt.Sprint(cfg.Height))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(src)
}

// --- /upload ----------------------------------------------------------------

// handleUpload implements POST UploadURL, the host half of the frame's
// requestUpload → uploadResult round trip. The body is the raw image bytes
// (the richtext/pdf raw-body + headers encoding); the suggested name rides
// X-Image-Filename (sanitized here — a header is attacker-controllable
// regardless of what the frame sends).
//
// The gate checks upload:images; AUTHORIZATION is the handler's job — Allow
// passes for anonymous callers, so a production host's WithUploadHandler
// must check the session itself before persisting (see docs/imageedit.md).
func (p *Plugin) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapUploadImages) {
		writeJSONCapabilityDenied(w, CapUploadImages)
		return
	}
	if p.uploadHandler == nil {
		// Unreachable via New (a granted upload:images without a handler is
		// a construction panic), but the guard keeps the route failing
		// closed anyway: WithDevGrantAll bypasses the grant, never the
		// nil-check.
		writeJSONError(w, http.StatusInternalServerError, "E_UPLOAD",
			"no upload handler configured (supply WithUploadHandler)")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, p.maxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE", err.Error())
		return
	}
	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "empty upload body")
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || format == "" {
		writeJSONError(w, http.StatusUnsupportedMediaType, "E_BAD_FORMAT",
			"upload is not a decodable png or jpeg")
		return
	}
	if format == "jpg" {
		format = "jpeg"
	}
	if cfg.Width > p.maxDim || cfg.Height > p.maxDim || cfg.Width*cfg.Height > p.maxPixels {
		// Header-stage rejection again: refuse the upload without ever
		// decoding its pixels.
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE",
			fmt.Sprintf("image is %d×%d; exceeds the dimension or pixel cap", cfg.Width, cfg.Height))
		return
	}
	id, err := p.uploadHandler(r.Context(), UploadRequest{
		Bytes:  body,
		Format: format,
		Width:  cfg.Width,
		Height: cfg.Height,
	})
	if err != nil {
		if errors.Is(err, ErrConflict) {
			writeJSONError(w, http.StatusConflict, "E_CONFLICT", err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "E_UPLOAD", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// --- /save ------------------------------------------------------------------

// handleSave implements POST SaveURL: the operation-list persist signal.
// The persisted record is the VALIDATED doc (same bounds /export enforces)
// with the raw JSON kept verbatim as the authoritative record, so a
// forward-compatible doc still round-trips even if a future field stops
// decoding.
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	if p.saveHandler == nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE",
			"no save handler configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		SchemaVersion string          `json:"schemaVersion"`
		Rev           int             `json:"rev"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON", err.Error())
		return
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	if body.SchemaVersion == "" {
		body.SchemaVersion = SchemaVersion
	}
	var doc Doc
	if len(body.Doc) > 0 && !bytes.Equal(body.Doc, []byte("null")) {
		if err := json.Unmarshal(body.Doc, &doc); err != nil {
			writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC",
				"doc does not decode as imageedit-v1: "+err.Error())
			return
		}
	}
	if err := ValidateDoc(doc); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC", err.Error())
		return
	}
	req := SaveRequest{
		DocID:         body.DocID,
		Doc:           doc,
		DocJSON:       canonicalDocJSON(body.Doc),
		SchemaVersion: body.SchemaVersion,
		Rev:           body.Rev,
	}
	if err := p.saveHandler(r.Context(), req); err != nil {
		if errors.Is(err, ErrConflict) {
			writeJSONError(w, http.StatusConflict, "E_CONFLICT", err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docId": req.DocID})
}

// --- /export ----------------------------------------------------------------

// handleExport implements POST ExportURL — the plugin's whole claim in one
// route. The frame sends ONLY the operation list; Go resolves the source,
// re-renders it server-side (render.go: crop → rotate → annotate → redact),
// strips EXIF by full re-encode, walks the redaction verification, and only
// then hands the bytes to the host's export handler. A client that lied
// about what it did cannot change what gets stored: the stored bytes are a
// function of the doc, rendered here.
//
// Verification failure is a 500 E_REDACT_VERIFY (fail closed — no bytes are
// released, no URL returned), never a silent partial export.
func (p *Plugin) handleExport(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	if p.exportHandler == nil {
		writeJSONError(w, http.StatusInternalServerError, "E_EXPORT",
			"no export handler configured (supply WithExportHandler)")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	var body struct {
		DocID string          `json:"docId"`
		Doc   json.RawMessage `json:"doc"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON", err.Error())
		return
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	var doc Doc
	if len(body.Doc) > 0 && !bytes.Equal(body.Doc, []byte("null")) {
		if err := json.Unmarshal(body.Doc, &doc); err != nil {
			writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC",
				"doc does not decode as imageedit-v1: "+err.Error())
			return
		}
	}
	if err := ValidateDoc(doc); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC", err.Error())
		return
	}

	// Resolve the source the doc names. The ref is host-namespace: the
	// frame cannot pick an arbitrary URL, only an id [WithSource] knows.
	src, err := p.source(r.Context(), doc.Src.Ref)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_DOC", err.Error())
		return
	}
	if len(src) == 0 {
		writeJSONError(w, http.StatusNotFound, "E_NOT_FOUND",
			"unknown image id "+doc.Src.Ref)
		return
	}
	if int64(len(src)) > p.maxBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE",
			"source image exceeds MaxBytes")
		return
	}

	out, err := renderDoc(src, doc, strings.ToLower(doc.Src.SHA256), p.jpegQuality)
	if err != nil {
		switch {
		case errors.Is(err, ErrTooLarge):
			writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE", err.Error())
		case errors.Is(err, ErrSrcMismatch):
			writeJSONError(w, http.StatusBadRequest, "E_SRC_MISMATCH",
				"the image changed since this doc was authored — reload and re-apply")
		case errors.Is(err, ErrRedactionLeak):
			// Fail closed: the verifier could not prove the redactions took.
			writeJSONError(w, http.StatusInternalServerError, "E_REDACT_VERIFY",
				"redaction verification failed; no bytes were released")
		case errors.Is(err, ErrBadDoc), errors.Is(err, ErrCropOutside), errors.Is(err, ErrUnsupportedFormat):
			writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC", err.Error())
		default:
			writeJSONError(w, http.StatusInternalServerError, "E_RENDER", err.Error())
		}
		return
	}
	if int64(len(out.Bytes)) > p.maxBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE",
			"produced image exceeds MaxBytes")
		return
	}

	url, err := p.exportHandler(r.Context(), ExportRequest{
		DocID:  body.DocID,
		Doc:    doc,
		Bytes:  out.Bytes,
		Format: out.Format,
		Width:  out.Width,
		Height: out.Height,
		SHA256: out.SHA256,
		Report: out.Report,
	})
	if err != nil {
		if errors.Is(err, ErrConflict) {
			writeJSONError(w, http.StatusConflict, "E_CONFLICT", err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "E_EXPORT", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":    url,
		"format": out.Format,
		"width":  out.Width,
		"height": out.Height,
		"bytes":  len(out.Bytes),
		"sha256": out.SHA256,
		"report": out.Report,
		"verify": out.Report.Pass,
	})
}

// --- shared helpers ----------------------------------------------------------

// canonicalDocJSON normalises the raw doc JSON to a stable canonical string:
// null / absent collapses to "" so the editor starts empty and the demo
// round-trip stays clean (the same normalisation richtext/pdf apply).
func canonicalDocJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	return trimmed
}

// writeJSON emits a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError emits the canonical {error, message?} error envelope. Every
// route denies with a stable machine-readable code so the adapter and tests
// can branch on it without parsing free text.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	v := map[string]string{"error": code}
	if message != "" {
		v["message"] = message
	}
	writeJSON(w, status, v)
}

// writeJSONCapabilityDenied delegates to the platform helper so every route
// denies uniformly with the offending capability named.
func writeJSONCapabilityDenied(w http.ResponseWriter, capability string) {
	pluginhost.WriteCapabilityDenied(w, capability)
}

// digestHex is the shared sha256-hex helper (digest binding + tests).
func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}
