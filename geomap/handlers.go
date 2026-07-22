package geomap

import (
	"encoding/json"
	"errors"
	"net/http"
)

// maxSaveBytes caps the save envelope (DoS guard — a markers payload is small
// JSON; the map autosaves on debounce). Hosts with very large marker sets
// override the handler via WithSaveHandler.
const maxSaveBytes = 4 << 20 // 4 MiB

// handleSave implements POST SaveURL. It parses the
// {docId, doc, lat, lng, zoom, markers, schemaVersion} envelope, gates on
// document:write, and delegates to the configured save handler.
//
// Encoding note: this is JSON. The host adapter POSTs a JSON body with `doc` as
// the canonical {lat,lng,zoom,markers} object and `lat`/`lng`/`zoom`/`markers`
// as the convenience top-level fields; `doc` is decoded as json.RawMessage so
// the canonical blob survives verbatim. The handler normalizes either form into
// SaveRequest.{Lat,Lng,Zoom,Markers} (preferring the explicit top-level
// fields). Mirrors monaco's handleSave exactly for the conflict/500 mapping.
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "document:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSaveBytes)
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		Lat           float64         `json:"lat"`
		Lng           float64         `json:"lng"`
		Zoom          float64         `json:"zoom"`
		Markers       []mapMarker     `json:"markers"`
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
	// Prefer the explicit top-level fields; fall back to {lat,lng,zoom,markers}
	// inside `doc` so either envelope form round-trips cleanly. A missing/empty
	// doc blob leaves the zero values in place, which the frame never sends for
	// a real save (it always sends the full doc).
	if len(body.Doc) > 0 && string(body.Doc) != "null" {
		if dl, dlng, dz, dm, ok := docFromRaw(body.Doc); ok {
			if body.Lat == 0 && body.Lng == 0 && body.Zoom == 0 && len(body.Markers) == 0 {
				body.Lat, body.Lng, body.Zoom, body.Markers = dl, dlng, dz, dm
			}
		}
	}
	req := SaveRequest{
		DocID:         body.DocID,
		Lat:           body.Lat,
		Lng:           body.Lng,
		Zoom:          body.Zoom,
		Markers:       body.Markers,
		SchemaVersion: body.SchemaVersion,
	}
	if err := p.saveHandler(r.Context(), req); err != nil {
		// A conflict is a distinct outcome, not a server fault: the doc changed
		// under the map. Surface it as 409 so the adapter can relay it to the
		// frame (which keeps the doc dirty and warns) instead of the frame
		// treating the save as done. Every other error stays a generic 500.
		// Mirrors monaco's/richtext's conflict mapping (errors.Is → 409).
		if errors.Is(err, ErrConflict) {
			writeJSONError(w, http.StatusConflict, "E_CONFLICT", err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "docId": req.DocID})
}

// docFromRaw extracts {lat,lng,zoom,markers} from a canonical doc blob, collapsing
// null / absent to zero values so a save with no top-level fields still round-trips.
func docFromRaw(raw json.RawMessage) (lat, lng, zoom float64, markers []mapMarker, ok bool) {
	var doc mapDoc
	if err := json.Unmarshal(raw, &doc); err == nil {
		return doc.Lat, doc.Lng, doc.Zoom, doc.Markers, true
	}
	return 0, 0, 0, nil, false
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
