package chart

import (
	"encoding/json"
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/chart/ssr"
)

// handleSave implements POST SaveURL. It gates on document:write, parses
// the {docId, doc, schemaVersion} envelope, VALIDATES the chart spec (a
// spec that cannot render must not be storable — the SSR path and the
// frame both read it back), and delegates to the configured save handler.
//
// SECURITY NOTE — the capability gate is NOT authentication.
// pluginhost.Allow passes for anonymous callers (a session/JWT in context
// is unscoped by design); it only narrows what a SCOPED API token may do.
// Any production host exposing this write route MUST check the session in
// its own middleware/handler first. The demo skips that because it runs
// WithDevGrantAll, which is demo-only.
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	// A chart spec is small by contract (≤ ssr.MaxPoints points), but a
	// hostile POST is not; cap the body so oversized payloads are rejected
	// before buffering (same DoS guard as the mermaid plugin).
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		SchemaVersion string          `json:"schemaVersion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", err.Error())
		return
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	if body.SchemaVersion == "" {
		body.SchemaVersion = SchemaVersion
	}
	// Normalize-and-validate: ParseSpec enforces the schema, the type set,
	// and the point/series caps. The re-marshaled bytes are what gets
	// stored, so a saved doc always round-trips through Mount → ssr.Render.
	spec, err := ssr.ParseSpec(body.Doc)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_SPEC", err.Error())
		return
	}
	normalized, err := json.Marshal(spec)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	req := SaveRequest{
		DocID:         body.DocID,
		Spec:          normalized,
		SchemaVersion: body.SchemaVersion,
	}
	if err := p.saveHandler(r.Context(), req); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "docId": req.DocID})
}

// writeJSON / writeJSONError mirror the mermaid plugin's helpers; the
// error envelope shape is part of the platform's route convention.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]string{"error": code}
	if message != "" {
		body["message"] = message
	}
	writeJSON(w, status, body)
}
