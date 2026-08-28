package whiteboard

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// handlers.go implements the two room routes (protocol-v1.md §10):
//
//	POST /room/publish   one update or presence relay   (gate sync:room)
//	GET  /room/stream    the SSE room stream             (gate sync:room)
//
// The SSE stream carries named events: hello (identity), sync (base64 Yjs
// update), presence (a remote cursor, no name), participants (occupancy),
// plus `: keepalive` comments the hub emits as ping events. The host
// adapter (host/adapter.js) translates between these and the postMessage
// bridge events the frame speaks.
//
// pluginhost.Allow is a capability gate, NOT authentication: it passes for
// anonymous callers (and for unscoped sessions it is bounded only by the
// plugin's grant set). A host exposing a SENSITIVE board must therefore
// check the session inside its own hub functions — see the package doc and
// docs/whiteboard.md.
//
// Both routes fail closed by construction: without [WithRoomHub] the
// sync:room capability is absent from the grant set, so the gate denies
// every caller. The handlers additionally guard the nil-hub case with a
// 503 (unreachable via New's validation, but a route that could panic on a
// nil func is a denial of service on the host, and the guard is one line).

// maxPublishBytes caps one POST body. A single stroke update is a few hundred
// bytes; a reconnect snapshot is the accumulated state and can reach tens of
// KiB. 1 MiB is two orders of headroom while still bounding the buffer.
const maxPublishBytes = 1 << 20

// handlePublish implements POST PublishURL: parse {docId, pid, kind,
// update|x,y,down}, gate on sync:room, delegate to the host-wired hub.
func (p *Plugin) handlePublish(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapSyncRoom) {
		pluginhost.WriteCapabilityDenied(w, CapSyncRoom)
		return
	}
	if p.publish == nil {
		// Unreachable through New (grantsCapability check), kept because a
		// nil-func call in an HTTP handler is a panic = host DoS.
		writeJSONError(w, http.StatusServiceUnavailable, "E_NO_ROOM_HUB", "sync:room granted but no hub wired")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPublishBytes)
	var body struct {
		DocID  string  `json:"docId"`
		PID    string  `json:"pid"`
		Kind   string  `json:"kind"`
		Update string  `json:"update"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Down   bool    `json:"down"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", err.Error())
		return
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	if body.PID == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "pid is required (assigned via the stream's hello event)")
		return
	}

	var ev RoomEvent
	switch body.Kind {
	case "sync":
		update, err := base64.StdEncoding.DecodeString(body.Update)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "E_BAD_UPDATE", "update must be base64: "+err.Error())
			return
		}
		if len(update) == 0 {
			writeJSONError(w, http.StatusBadRequest, "E_BAD_UPDATE", "update is empty")
			return
		}
		ev = RoomEvent{Kind: EventSync, Update: update}
	case "presence":
		// PID rides the event so a conformant hub can fan it out without a
		// second lookup; the hub attaches the ASSIGNED colour (a client-chosen
		// colour must never reach another participant's board).
		ev = RoomEvent{Kind: EventPresence, PID: body.PID, X: body.X, Y: body.Y, Down: body.Down}
	default:
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "kind must be \"sync\" or \"presence\"")
		return
	}

	if err := p.publish(r.Context(), body.DocID, body.PID, ev); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_PUBLISH", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleStream implements GET StreamURL: gate on sync:room, then hand the
// connection to the host-wired hub, which pushes hello → replay → live
// events until the consumer goes away. Each event is one SSE frame; sync
// payloads are base64 because EventSource data is text.
func (p *Plugin) handleStream(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapSyncRoom) {
		pluginhost.WriteCapabilityDenied(w, CapSyncRoom)
		return
	}
	if p.subscribe == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "E_NO_ROOM_HUB", "sync:room granted but no hub wired")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "E_NO_STREAMING", "response writer cannot flush")
		return
	}
	room := r.URL.Query().Get("docId")
	if room == "" {
		room = defaultDocID
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// Disable proxy buffering (nginx and friends): a buffered "stream" is a
	// batch job, and it would silently break the collaboration contract.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // ship headers before the first event blocks on the hub

	_ = p.subscribe(r.Context(), room, func(ev RoomEvent) error {
		var frame string
		switch ev.Kind {
		case EventHello:
			data, _ := json.Marshal(map[string]any{"pid": ev.PID, "color": ev.Color, "participants": ev.Count})
			frame = "event: hello\ndata: " + string(data) + "\n\n"
		case EventSync:
			frame = "event: sync\ndata: " + base64.StdEncoding.EncodeToString(ev.Update) + "\n\n"
		case EventPresence:
			data, _ := json.Marshal(map[string]any{
				"pid": ev.PID, "color": ev.Color, "x": ev.X, "y": ev.Y, "down": ev.Down,
			})
			frame = "event: presence\ndata: " + string(data) + "\n\n"
		case EventParticipants:
			data, _ := json.Marshal(map[string]any{"count": ev.Count})
			frame = "event: participants\ndata: " + string(data) + "\n\n"
		case EventPing:
			frame = ": keepalive\n\n"
		default:
			return nil // unknown hub event: drop, never kill the stream
		}
		if _, err := w.Write([]byte(frame)); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	// A cancelled context is the normal disconnect path; yield errors mean
	// the consumer is gone and there is no response left to report on.
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
