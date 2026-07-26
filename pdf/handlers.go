package pdf

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// handlers.go implements the three RPC routes (protocol-v1.md §10):
//
//   - POST   /save            overlay JSON   (gate document:write; mode-then-cap)
//   - POST   /export          produced PDF   (gate pdf:export; mode-then-cap; 412/413)
//   - GET    /doc/{id}        resolved PDF   (gate document:read; MaxBytes; 413)
//
// Enforcement order is the same on every mutating route and is deliberate:
// mode first (the host's declared surface — cheapest, no body read), then the
// capability gate (caller authority), then the body size ceiling. Each check
// maps to a distinct status + error code so the host adapter and the tests can
// tell a policy refusal from a bad payload from an oversized one.
//
// Mode-vs-capability status codes: a capability denial is 403 +
// E_CAPABILITY_DENIED on ALL THREE routes, via the platform's
// pluginhost.WriteCapabilityDenied — see writeJSONCapabilityDenied for why this
// follows the platform's implementation rather than the 412 its prose promises.
// A mode refusal is 403 + E_MODE_DENIED, distinguished by the error code rather
// than the status, since both are policy refusals of an authenticated caller.

// maxEnvelopeBytes caps the /save JSON envelope so an oversized POST cannot be
// buffered wholesale before the size check fires. The PDF bytes on /export are
// bounded separately by [Plugin.maxBytes].
const maxEnvelopeBytes int64 = 1 << 20 // 1 MiB — overlay JSON is tiny

// --- /save -----------------------------------------------------------------

// handleSave implements POST SaveURL. It accepts the canonical pdf-v1 overlay
// (annotations / form fields / page ops / redactions) and delegates to the
// configured save handler. The overlay is decoded into a typed [Overlay] for
// inspection AND kept verbatim as DocJSON (the authoritative record that
// round-trips through the hidden field, so type-specific annotation extras
// never get dropped by a struct re-marshal).
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	// 1. Mode: ModeView rejects save entirely (view-only mount).
	if !p.mode.allowsAnnotate() {
		writeJSONError(w, http.StatusForbidden, "E_MODE_DENIED",
			"save requires annotate or redact mode")
		return
	}
	// 2. Capability: document:write (richtext/monaco contract → 403).
	if !p.allow(r, "document:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
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
	overlay, parseErr := decodeOverlay(body.Doc)
	if parseErr != nil {
		// The envelope parsed but the overlay did not. Do not fail the save:
		// DocJSON is the authoritative record and survives verbatim, so a
		// forward-incompatible overlay (a new annotation kind, say) still
		// persists. The handler inspects the zero-valued Doc + the raw JSON.
		overlay = Overlay{SchemaVersion: body.SchemaVersion, Rev: body.Rev}
	}
	req := SaveRequest{
		DocID:         body.DocID,
		Doc:           overlay,
		DocJSON:       canonicalDocJSON(body.Doc),
		SchemaVersion: body.SchemaVersion,
		Rev:           body.Rev,
	}
	if err := p.saveHandler(r.Context(), req); err != nil {
		// A conflict is a distinct outcome, not a server fault: the overlay's
		// rev is stale. Surface it as 409 so the adapter relays it to the frame
		// as a distinct saveResult (the editor keeps the doc dirty and warns)
		// instead of the frame treating the save as done. Mirrors richtext/monaco.
		if errors.Is(err, ErrConflict) {
			writeJSONError(w, http.StatusConflict, "E_CONFLICT", err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docId": req.DocID})
}

// --- /export ---------------------------------------------------------------

// handleExport implements POST ExportURL. The frame produces a PDF (pdf.js +
// pdf-lib, redacted pages rasterized) and POSTs the bytes with the in-frame
// verification report; the host stores them via [WithExportHandler] and
// returns a URL the frame can surface for download / print.
//
// The body is the raw PDF bytes; kind / docId / the verification report ride
// headers (the repo's established raw-body + headers encoding — see
// richtext/handlers.go handleUpload). MaxBytes bounds the body so an oversized
// produced file is rejected (413) before it is fully buffered.
func (p *Plugin) handleExport(w http.ResponseWriter, r *http.Request) {
	// 1. Mode: ModeView rejects export entirely; ModeAnnotate rejects redact.
	if !p.mode.allowsAnnotate() {
		writeJSONError(w, http.StatusForbidden, "E_MODE_DENIED",
			"export requires annotate or redact mode")
		return
	}
	kind := normalizeExportKind(r.Header.Get("X-Export-Kind"))
	if kind == "" {
		// An unknown kind is a bad request, not a policy refusal — the caller
		// sent garbage, not a mode-violating intent.
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST",
			"unknown X-Export-Kind "+r.Header.Get("X-Export-Kind"))
		return
	}
	if kind == ExportKindRedact && !p.mode.allowsRedact() {
		writeJSONError(w, http.StatusForbidden, "E_MODE_DENIED",
			"redact export requires redact mode")
		return
	}
	// 2. Capability: pdf:export — the grant that lets produced bytes leave the
	//    frame at all. 403 + E_CAPABILITY_DENIED, same as every other route.
	if !p.allow(r, CapPDFExport) {
		writeJSONCapabilityDenied(w)
		return
	}
	// 3. Body size: cap at the host ceiling BEFORE reading the whole file.
	r.Body = http.MaxBytesReader(w, r.Body, p.maxBytes)
	bytes, err := io.ReadAll(r.Body)
	if err != nil {
		// MaxBytesReader returns *http.MaxBytesError on overflow, which is the
		// too-large outcome the brief requires (413, not 500).
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE", err.Error())
		return
	}
	report := json.RawMessage{}
	if h := strings.TrimSpace(r.Header.Get("X-Export-Report")); h != "" {
		// Best-effort: keep the report verbatim. A malformed header must not
		// fail an otherwise-valid export — the bytes are already produced and
		// the frame is waiting on the URL.
		report = json.RawMessage(h)
	}
	docID := strings.TrimSpace(r.Header.Get("X-Export-DocID"))
	if docID == "" {
		docID = defaultDocID
	}
	if p.exportHandler == nil {
		// Unreachable in normal construction (ModeRedact panics without the
		// capability; annotate-only reaching here implies pdf:export was
		// declared, which implies a handler). Guard anyway so a misconfigured
		// host fails loud rather than nil-derefs.
		writeJSONError(w, http.StatusInternalServerError, "E_EXPORT",
			"no export handler configured (supply WithExportHandler)")
		return
	}
	url, err := p.exportHandler(r.Context(), ExportRequest{
		DocID:  docID,
		Kind:   kind,
		Bytes:  bytes,
		Report: report,
	})
	if err != nil {
		if errors.Is(err, ErrConflict) {
			writeJSONError(w, http.StatusConflict, "E_CONFLICT", err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "E_EXPORT", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

// normalizeExportKind maps the header value onto the canonical export kind
// constants. Empty → "export" (the common case). Unknown → "" (caller rejects).
func normalizeExportKind(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", ExportKindExport:
		return ExportKindExport
	case ExportKindDownload:
		return ExportKindDownload
	case ExportKindPrint:
		return ExportKindPrint
	case ExportKindRedact:
		return ExportKindRedact
	}
	return ""
}

// --- /doc/{id} -------------------------------------------------------------

// handleDoc implements GET DocRoute (/{prefix}/doc/{id}). It resolves the doc
// id to PDF bytes via [WithSource] and returns them.
//
// This route is called ONLY by the HOST PAGE adapter — never by the frame.
// The frame runs under connect-src 'none' and an opaque origin, so it cannot
// fetch /doc/{id} even if it wanted to; the host adapter fetches it
// same-origin (session cookie + CSRF token attached) and relays the bytes
// over the postMessage bridge in chunks. That separation is the reason
// authorization stays here at the data layer (a capability check + whatever
// the host's source function enforces), not in the frame — the frame is
// untrusted by construction.
func (p *Plugin) handleDoc(w http.ResponseWriter, r *http.Request) {
	// 1. Capability: document:read (403 — same code as save/doc elsewhere).
	if !p.allow(r, "document:read") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "missing doc id")
		return
	}
	bytes, err := p.source(r.Context(), id)
	if err != nil {
		// Any resolver error is a 500 — the adapter surfaces it as a
		// renderError. A "doc not found" outcome is signalled by the resolver
		// returning (nil, nil) (handled below), so there is no exported
		// sentinel a host must wrap.
		writeJSONError(w, http.StatusInternalServerError, "E_DOC", err.Error())
		return
	}
	if len(bytes) == 0 {
		// (nil, nil) — or an empty slice — from the resolver means the id
		// resolves to nothing: a 404, distinct from a transient source error.
		writeJSONError(w, http.StatusNotFound, "E_NOT_FOUND", "unknown doc id")
		return
	}
	// 2. Size ceiling BEFORE streaming the bytes back: a document larger than
	//    MaxBytes is refused with 413 rather than being relayed into a
	//    postMessage. (The source signature returns []byte, so the file is
	//    already in memory by the time we measure — a truly streaming source
	//    would need a size-aware resolver. The check still prevents the bytes
	//    from crossing the bridge.)
	if int64(len(bytes)) > p.maxBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE",
			"document exceeds MaxBytes")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes)
}

// --- shared helpers --------------------------------------------------------

// canonicalDocJSON normalises the raw overlay JSON to a stable canonical
// string: null / absent collapses to "" so the editor starts empty and the
// demo round-trip stays clean (the same normalisation richtext applies to its
// doc blob).
func canonicalDocJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// writeJSON emits a JSON response. It uses any for the encoder argument (a
// helper, not a payload struct); the payload types in overlay.go carry no any.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError emits the canonical {error, message?} error envelope. Every
// route denies with a stable machine-readable code so the adapter and tests can
// branch on it without parsing free text.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]any{"error": code}
	if message != "" {
		body["message"] = message
	}
	writeJSON(w, status, body)
}

// writeJSONCapabilityDenied emits the capability-denied response for /export,
// naming the offending capability so the adapter can surface it.
//
// It delegates to the platform helper deliberately. There is a real
// inconsistency upstream: docs/plugin-platform.md (and protocol-v1.md §5) say a
// denied capability is "HTTP 412 on the route side", while the platform's own
// pluginhost.WriteCapabilityDenied — the function every shipped plugin actually
// calls — writes 403. Splitting the difference *within one plugin* (403 on
// /save and /doc, 412 on /export) would be the worst of the three options: a
// host writing one error branch would silently mishandle whichever route it did
// not test against. So all three routes speak the platform's 403, and the
// doc/implementation divergence is recorded as an upstream thread in
// docs/DECISIONS.md rather than papered over here.
func writeJSONCapabilityDenied(w http.ResponseWriter) {
	pluginhost.WriteCapabilityDenied(w, CapPDFExport)
}
