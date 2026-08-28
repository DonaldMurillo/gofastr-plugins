package formbuilder

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// handlers.go implements the one RPC route (protocol-v1.md §10):
//
//	POST /save   schema persist signal   (gate document:write)
//
// The route VALIDATES the schema before persisting anything: unknown field
// type, duplicate name, empty or invalid name, malformed rule, unknown
// version, markup in a label — each is a 400 with a specific error code, and
// nothing is persisted. A schema that gets past the frame still gets refused
// here; that refusal is the plugin's whole posture.
//
// A capability denial is 403 + E_CAPABILITY_DENIED via the platform's
// pluginhost.WriteCapabilityDenied.
//
// pluginhost.Allow is a capability gate, NOT authentication: it passes for
// anonymous callers. Any host that persists schemas for real must check the
// session in its own WithSaveHandler. The demo's WithDevGrantAll skips the
// gate entirely and MUST NOT survive into a production mount.
//
// The route also fails CLOSED on an unwired handler (a clear error response,
// never a nil-deref): WithDevGrantAll bypasses the grant side of the gate, so
// it must not be able to reach a nil handler either — a panic inside an HTTP
// handler is a denial of service on the whole host process.

// maxEnvelopeBytes caps the /save request body. A schema is tiny; anything
// near this cap is a mistake or an attack.
const maxEnvelopeBytes int64 = 64 << 10

// handleSave implements POST SaveURL. The persisted record is the VALIDATED,
// normalised doc — never the raw request body — so a schema that was accepted
// can never come back on the next load as something the renderer has to guess
// at.
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:write") {
		writeJSONCapabilityDenied(w, "document:write")
		return
	}
	if p.saveHandler == nil {
		// Unreachable via New (memSave defaults in); the guard keeps the
		// route failing closed anyway.
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE",
			"schema persistence is not wired (supply WithSaveHandler)")
		return
	}
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		SchemaVersion string          `json:"schemaVersion"`
	}
	if !decodeEnvelope(w, r, &body) {
		return
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	if body.SchemaVersion == "" {
		body.SchemaVersion = SchemaVersion
	}
	var doc Doc
	if len(body.Doc) > 0 && string(body.Doc) != "null" {
		if err := json.Unmarshal(body.Doc, &doc); err != nil {
			writeJSONError(w, http.StatusBadRequest, ErrBadJSON, err.Error())
			return
		}
	}
	if err := ValidateDoc(&doc); err != nil {
		if se, ok := err.(*SchemaError); ok {
			writeJSONError(w, http.StatusBadRequest, se.Code, se.Message)
			return
		}
		writeJSONError(w, http.StatusBadRequest, ErrBadRule, err.Error())
		return
	}
	// Persist what was validated: canonical JSON of the normalised doc.
	docJSON, err := json.Marshal(doc)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	if err := p.saveHandler(r.Context(), SaveRequest{
		DocID:         body.DocID,
		Doc:           doc,
		DocJSON:       string(docJSON),
		SchemaVersion: body.SchemaVersion,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	// The server's own view of the schema — what the demo's proof strip and
	// the builder's status line report. fields/rules come from the doc the
	// SERVER validated, not from anything the frame claimed.
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"docId":  body.DocID,
		"fields": len(doc.Fields),
		"rules":  doc.RuleCount(),
	})
}

// --- shared helpers --------------------------------------------------------

// decodeEnvelope reads ONE JSON value into dst under the envelope cap and
// rejects any non-whitespace after it — a second object or stray bytes.
// Trailing whitespace (the curl -d @body.json newline) is allowed; the DoS
// concern is bytes on the wire, and MaxBytesReader caps those. ok=false means
// the error response is already written.
func decodeEnvelope(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadJSON, err.Error())
		return false
	}
	rest, err := io.ReadAll(io.MultiReader(dec.Buffered(), r.Body))
	if err != nil || len(bytes.TrimSpace(rest)) > 0 {
		writeJSONError(w, http.StatusBadRequest, ErrBadJSON, "trailing data after the JSON value")
		return false
	}
	return true
}

// writeJSON emits a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError emits the canonical {error, message?} error envelope. Every
// refusal carries a stable machine-readable code so the adapter (and the
// builder's status line) can branch on it without parsing free text.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]string{"error": code}
	if message != "" {
		body["message"] = message
	}
	writeJSON(w, status, body)
}

// writeJSONCapabilityDenied delegates to the platform helper so every route
// denies uniformly with the offending capability named.
func writeJSONCapabilityDenied(w http.ResponseWriter, capability string) {
	pluginhost.WriteCapabilityDenied(w, capability)
}
