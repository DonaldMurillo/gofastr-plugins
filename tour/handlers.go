package tour

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// handleGetTour implements GET ToursBaseURL/{id}: returns the registered tour
// definition as {id, steps:[...]} JSON. 404 for an unknown tour id.
func (p *Plugin) handleGetTour(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "tour:read") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "missing tour id")
		return
	}
	t, ok := p.Tour(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "E_NOT_FOUND", "unknown tour id")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleMarkSeen implements POST SeenURL: body {tourId} records the tour as
// seen for the caller. Returns 200 {status:"ok"}. Empty tourId is a 400.
func (p *Plugin) handleMarkSeen(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "tour:write") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	// Tour ids are tiny; cap the body so an oversized POST can't be buffered
	// wholesale (DoS guard — same rationale as mermaid/handlers.go).
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64 KiB
	var body struct {
		TourID string `json:"tourId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", err.Error())
		return
	}
	id := strings.TrimSpace(body.TourID)
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "missing tourId")
		return
	}
	if err := p.seen.Mark(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SEEN", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleQuerySeen implements GET SeenURL?tourId=...: returns {seen: bool} for
// the caller. Missing tourId is a 400; an unknown tour id is reported as
// seen=false (the runtime may still attempt to run it).
func (p *Plugin) handleQuerySeen(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "tour:read") {
		writeJSONError(w, http.StatusForbidden, "E_CAPABILITY_DENIED", "")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("tourId"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "missing tourId")
		return
	}
	ok, err := p.seen.IsSeen(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SEEN", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"seen": ok})
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

// memSeen is the default in-memory SeenHandler. It is keyed by tour id (no
// user dimension); a production host supplies [WithSeenHandler] to key by
// user/session. Safe for concurrent use.
type memSeen struct {
	mu   sync.RWMutex
	seen map[string]struct{}
}

func newMemSeen() *memSeen {
	return &memSeen{seen: make(map[string]struct{})}
}

func (m *memSeen) Mark(_ context.Context, tourID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[tourID] = struct{}{}
	return nil
}

func (m *memSeen) IsSeen(_ context.Context, tourID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.seen[tourID]
	return ok, nil
}
