package monaco

import (
	"encoding/json"
	"errors"
	"net/http"
)

// maxSaveBytes caps the save envelope (DoS guard — a code payload is text; the
// editor autosaves on debounce). Hosts with very large documents override the
// handler via WithSaveHandler.
const maxSaveBytes = 8 << 20 // 8 MiB

// handleSave implements POST SaveURL. It parses the
// {docId, doc, code, language, schemaVersion} envelope, gates on
// document:write, and delegates to the configured save handler.
//
// Encoding note: this is JSON. The host adapter POSTs a JSON body with `doc` as
// the canonical {code, language} object and `code`/`language` as the
// convenience top-level fields; `doc` is decoded as json.RawMessage so the
// canonical blob survives verbatim. The handler normalizes either form into
// SaveRequest.{Code,Language} (preferring the explicit top-level fields).
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSaveBytes)
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		Code          string          `json:"code"`
		Language      string          `json:"language"`
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
	// Prefer the explicit top-level fields; fall back to the {code, language}
	// inside `doc` so either envelope form round-trips cleanly.
	code, language := body.Code, body.Language
	if code == "" || language == "" {
		dc, dl := codeLanguageFromDoc(body.Doc)
		if code == "" {
			code = dc
		}
		if language == "" {
			language = dl
		}
	}
	req := SaveRequest{
		DocID:         body.DocID,
		Code:          code,
		Language:      language,
		SchemaVersion: body.SchemaVersion,
	}
	if err := p.saveHandler(r.Context(), req); err != nil {
		// A conflict is a distinct outcome, not a server fault: the doc changed
		// under the editor. Surface it as 409 so the adapter can relay it to the
		// frame (which keeps the doc dirty and warns) instead of the frame
		// treating the save as done. Every other error stays a generic 500.
		// Mirrors richtext's conflict mapping (errors.Is → 409 E_CONFLICT).
		if errors.Is(err, ErrConflict) {
			writeJSONError(w, http.StatusConflict, "E_CONFLICT", err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "docId": req.DocID})
}

// codeLanguageFromDoc extracts {code, language} from a canonical doc blob,
// collapsing null / absent to "" so the editor starts empty and the demo
// round-trip stays clean.
func codeLanguageFromDoc(raw json.RawMessage) (code, language string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	var doc struct {
		Code     string `json:"code"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(raw, &doc); err == nil {
		return doc.Code, doc.Language
	}
	return "", ""
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
