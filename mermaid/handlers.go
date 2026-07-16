package mermaid

import (
	"encoding/json"
	"net/http"
)

// handleSave implements POST SaveURL (protocol-v1.md §10). It parses the
// {docId, doc, source, schemaVersion} envelope, gates on document:write, and
// delegates to the configured save handler.
//
// Encoding note: this is JSON. The host adapter POSTs a JSON body with `doc` as
// the canonical {source:"…"} object and `source` as the raw mermaid text; `doc`
// is decoded as json.RawMessage so the canonical blob survives verbatim. The
// handler normalizes either form into SaveRequest.Source.
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	// A mermaid source is small text; cap the body so an oversized POST can't
	// be buffered wholesale (DoS guard — same rationale as the richtext plugin).
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		Source        string          `json:"source"`
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
	// Prefer the explicit `source` field; fall back to {source} inside `doc`.
	source := body.Source
	if source == "" {
		source = sourceFromDoc(body.Doc)
	}
	req := SaveRequest{
		DocID:         body.DocID,
		Source:        source,
		SchemaVersion: body.SchemaVersion,
	}
	if err := p.saveHandler(r.Context(), req); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "docId": req.DocID})
}

// sourceFromDoc extracts the diagram source from a canonical {source:"…"} doc
// blob, collapsing null / absent to "" so the editor starts empty and the demo
// round-trip stays clean.
func sourceFromDoc(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var doc struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(raw, &doc); err == nil {
		return doc.Source
	}
	return ""
}

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
