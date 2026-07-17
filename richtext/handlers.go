package richtext

import (
	"encoding/json"
	"io"
	"net/http"
)

// Request-body ceilings (DoS guard): a save envelope is JSON text; an upload is
// raw image bytes. Both are wrapped in http.MaxBytesReader so an oversized body
// is rejected before it is buffered in memory. Hosts needing larger docs/images
// override the handlers via WithSaveHandler / WithUploadHandler.
const (
	maxSaveBytes   = 4 << 20  // 4 MiB of canonical block-JSON
	maxUploadBytes = 12 << 20 // 12 MiB per image
)

// handleSave implements POST SaveURL (protocol-v1.md §10). It parses the
// {docId, doc, markdown, schemaVersion} envelope, gates on document:write,
// and delegates to the configured save handler.
//
// Upload encoding note: this is JSON. The host broker POSTs a JSON body with
// `doc` as the ProseMirror doc object; it is decoded as json.RawMessage so the
// canonical blob survives verbatim into SaveRequest.DocJSON.
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSaveBytes)
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		Markdown      string          `json:"markdown"`
		SchemaVersion string          `json:"schemaVersion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON", err.Error())
		return
	}
	if body.SchemaVersion == "" {
		body.SchemaVersion = SchemaVersion
	}
	req := SaveRequest{
		DocID:         body.DocID,
		DocJSON:       normalizeDocJSON(body.Doc),
		Markdown:      body.Markdown,
		SchemaVersion: body.SchemaVersion,
	}
	if err := p.saveHandler(r.Context(), req); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "docId": req.DocID})
}

// handleUpload implements POST UploadURL (protocol-v1.md §10). The chosen
// Phase-0 upload encoding is RAW BODY + HEADERS:
//
//	POST /__gofastr/plugin/richtext/upload
//	X-Upload-Name:  cat.png
//	X-Upload-Type:  image/png
//	Content-Type:   image/png
//	<raw image bytes>
//
// This matches the ArrayBuffer-centric requestUpload envelope and avoids the
// broker having to synthesize a multipart boundary. It gates on upload:images
// and delegates to the configured upload handler.
func (p *Plugin) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "upload:images") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	bytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "E_TOO_LARGE", err.Error())
		return
	}
	// upload:images means images: trust the SNIFFED type, not the caller's
	// header (a caller could otherwise smuggle a non-image MIME into the
	// echoed data: URL). Reject anything that doesn't sniff as an image.
	sniffed := http.DetectContentType(bytes)
	if len(sniffed) < 6 || sniffed[:6] != "image/" {
		writeJSONError(w, http.StatusUnsupportedMediaType, "E_NOT_IMAGE", "upload body is not an image")
		return
	}
	req := UploadRequest{
		Name:  r.Header.Get("X-Upload-Name"),
		Type:  sniffed,
		Bytes: bytes,
	}
	res, err := p.uploadHandler(r.Context(), req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_UPLOAD", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": res.URL})
}

// normalizeDocJSON turns the RawMessage doc into a stable canonical-JSON
// string, collapsing null / absent to "" so the editor starts empty and the
// demo round-trip stays clean.
func normalizeDocJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
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
