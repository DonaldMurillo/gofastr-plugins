package calendar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// handlers.go implements the three RPC routes (protocol-v1.md §10):
//
//	POST /occurrences  resolved window   (gate document:read)
//	POST /move         one move intent   (gate document:write)
//	POST /save         view-state doc    (gate document:write)
//
// A capability denial is 403 + E_CAPABILITY_DENIED on every route, via the
// platform's pluginhost.WriteCapabilityDenied.
//
// pluginhost.Allow is a capability gate, NOT authentication: it passes for
// anonymous callers. Both write routes therefore rely on the host's own
// move/save handlers to check the session before persisting for real. The
// demo's WithDevGrantAll skips the gate entirely and MUST NOT survive into
// a production mount.
//
// Every route fails CLOSED on bad input (a clear 400 with a stable code,
// never a panic): a panic inside an HTTP handler is a denial of service on
// the whole host process.

// Bounds on the request envelopes.
const (
	maxEnvelopeBytes = 64 << 10
)

// occurrencesBody is the decoded POST /occurrences envelope.
type occurrencesBody struct {
	DocID string `json:"docId"`
	From  string `json:"from"` // wall date "2006-01-02", inclusive
	To    string `json:"to"`   // wall date, inclusive
}

// MoveRequest is one move intent: the occurrence's identity (event + its
// ORIGINAL series date) and the WALL-CLOCK delta the frame's grid computed —
// dayDelta days plus minuteDelta minutes. The frame sends what its pixels
// said; the server decides what that means in the target zone.
type MoveRequest struct {
	DocID       string `json:"docId"`
	EventID     string `json:"eventId"`
	Date        string `json:"date"`
	DayDelta    int    `json:"dayDelta"`
	MinuteDelta int    `json:"minuteDelta"`
}

// SaveRequest is the decoded POST /save envelope.
type SaveRequest struct {
	DocID         string `json:"docId"`
	DocJSON       string `json:"doc"`
	SchemaVersion string `json:"schemaVersion"`
}

// --- /occurrences -----------------------------------------------------------

// handleOccurrences implements POST OccurrencesURL, the host half of the
// frame's requestOccurrences → occurrencesResult round trip. The answer is
// fully resolved: instants, wall clocks, zone abbreviations, conflicts and
// the range's DST transitions. The frame renders exactly this and computes
// nothing.
func (p *Plugin) handleOccurrences(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapDocRead) {
		writeJSONCapabilityDenied(w, CapDocRead)
		return
	}
	var body occurrencesBody
	if !decodeEnvelope(w, r, &body) {
		return
	}
	from, err := parseWallDate(body.From)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_RANGE", "from: "+err.Error())
		return
	}
	to, err := parseWallDate(body.To)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_RANGE", "to: "+err.Error())
		return
	}
	if to.before(from) {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_RANGE", "to precedes from")
		return
	}
	if to.daysFrom(from) > maxRangeDays {
		writeJSONError(w, http.StatusBadRequest, "E_RANGE_TOO_LARGE",
			fmt.Sprintf("window is %d days; the cap is %d", to.daysFrom(from)+1, maxRangeDays))
		return
	}

	events, err := p.events(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SOURCE", err.Error())
		return
	}
	if len(events) > maxEventsWarn {
		writeJSONError(w, http.StatusBadRequest, "E_SOURCE_TOO_LARGE",
			fmt.Sprintf("events source returned %d events (cap %d)", len(events), maxEventsWarn))
		return
	}
	win, err := buildOccurrences(events, p.currentOverrides(), from, to)
	if err != nil {
		// A definition error from the host's own source: loud, 422, named.
		writeJSONError(w, http.StatusUnprocessableEntity, "E_BAD_EVENT", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, win)
}

// --- /move ------------------------------------------------------------------

// handleMove implements POST MoveURL, the host half of the frame's
// requestMove → moveResult round trip. This route is where the plugin's
// claim lives: the frame reports a wall-clock delta, Go resolves it through
// the event's zone, records the override, re-expands, re-checks conflicts,
// and answers with what ACTUALLY happened — requested delta next to the
// wall-clock result next to the elapsed time, plus a plain-language note
// whenever they diverge.
func (p *Plugin) handleMove(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapDocWrite) {
		writeJSONCapabilityDenied(w, CapDocWrite)
		return
	}
	var req MoveRequest
	if !decodeEnvelope(w, r, &req) {
		return
	}
	if req.EventID == "" || req.Date == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_MOVE", "eventId and date are required")
		return
	}
	if _, err := parseWallDate(req.Date); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_MOVE", "date: "+err.Error())
		return
	}
	if req.DayDelta > maxMoveDays || req.DayDelta < -maxMoveDays {
		writeJSONError(w, http.StatusBadRequest, "E_DELTA_OUT_OF_RANGE",
			fmt.Sprintf("dayDelta %d outside ±%d", req.DayDelta, maxMoveDays))
		return
	}
	if req.MinuteDelta > maxMoveMinutes || req.MinuteDelta < -maxMoveMinutes {
		writeJSONError(w, http.StatusBadRequest, "E_DELTA_OUT_OF_RANGE",
			fmt.Sprintf("minuteDelta %d outside ±%d", req.MinuteDelta, maxMoveMinutes))
		return
	}

	events, err := p.events(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SOURCE", err.Error())
		return
	}
	var ev *Event
	for i := range events {
		if events[i].ID == req.EventID {
			e := events[i]
			ev = &e
			break
		}
	}
	if ev == nil {
		writeJSONError(w, http.StatusNotFound, "E_NO_EVENT", "no event "+req.EventID)
		return
	}
	if err := ValidateEvent(*ev); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, "E_BAD_EVENT", err.Error())
		return
	}

	overrides := p.currentOverrides()
	res, err := applyMove(*ev, overrides, req, events)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, "E_BAD_MOVE", err.Error())
		return
	}
	// Persist through the host's hook (session check belongs there).
	if err := p.moveHandler(r.Context(), res.Override); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_MOVE_PERSIST", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- /save ------------------------------------------------------------------

// handleSave implements POST SaveURL: the view-state doc persist signal.
// The persisted record is the VALIDATED, normalised doc — mode whitelisted,
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapDocWrite) {
		writeJSONCapabilityDenied(w, CapDocWrite)
		return
	}
	// The doc rides as a JSON object from the frame (the adapter relays
	// params.doc verbatim) but as a string from curl/tests — accept both
	// shapes (json.RawMessage, then unwrap a quoted string if present).
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		SchemaVersion string          `json:"schemaVersion"`
	}
	if !decodeEnvelope(w, r, &body) {
		return
	}
	docRaw := body.Doc
	if len(docRaw) > 0 && docRaw[0] == '"' {
		var s string
		if err := json.Unmarshal(docRaw, &s); err == nil {
			docRaw = []byte(s)
		}
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	doc, err := normalizeDoc(docRaw)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC", err.Error())
		return
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_BAD_DOC", err.Error())
		return
	}
	if err := p.saveHandler(r.Context(), SaveRequest{
		DocID:         body.DocID,
		DocJSON:       string(canonical),
		SchemaVersion: SchemaVersion,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func decodeEnvelope(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON", err.Error())
		return false
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		msg := "trailing data after the JSON value"
		if err != nil {
			msg = err.Error()
		}
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON", msg)
		return false
	}
	return true
}

// decodeStrict unmarshals with unknown-field rejection — used for the
// canonical doc, where a stray key means the sender and receiver disagree
// about the schema.
func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

// writeJSONCapabilityDenied delegates to the platform helper so every route
// denies uniformly with the offending capability named.
func writeJSONCapabilityDenied(w http.ResponseWriter, capability string) {
	pluginhost.WriteCapabilityDenied(w, capability)
}
