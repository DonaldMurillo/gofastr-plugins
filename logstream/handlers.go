package logstream

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// handlers.go implements the single RPC route (protocol-v1.md §10):
//
//	GET /stream?after=N   the NDJSON line stream   (gate stream:read)
//
// One NDJSON record per line: {"seq":N,"text":"…"} — the record's `text`
// carries ANSI escapes intact (json.Marshal escapes any embedded newline, so
// the framing is always safe); the record ends at the physical newline.
//
// pluginhost.Allow is a capability gate, NOT authentication: it passes for
// anonymous callers (and for unscoped sessions it is bounded only by the
// plugin's grant set). A host exposing a SENSITIVE source must therefore
// check the session inside its own SourceFunc — see the package doc and
// docs/logstream.md.
//
// The route is read-only and fail-closed by construction: no handler state
// is mutated, and an unwired source cannot occur (New panics without
// WithSource), so there is no nil-handler path to guard here.

const (
	// maxLineBytes bounds one line before it crosses: a source that yields
	// unbounded "lines" (a concatenated log dump, say) must not be able to
	// stall the bridge with one giant record. Truncation is the honest
	// option — the alternative (rejecting the stream) kills the tail.
	maxLineBytes = 8192

	// stallWriteTimeout bounds one yield when the consumer stops reading:
	// a dead client (socket open, nobody home) would otherwise pin the
	// source goroutine forever. The request context cancels on clean
	// disconnects; this deadline catches the unclean ones.
	stallWriteTimeout = 10 * time.Second
)

// stallTimeout is a var so tests can shrink it (a 10 s real-world stall is
// correct; a 10 s test is slow).
var stallTimeout = stallWriteTimeout

// handleStream implements GET StreamURL: it gates on stream:read, then hands
// the connection to the host's source, which pushes lines until the consumer
// goes away. The response is chunked NDJSON flushed per line — the HTTP shape
// of "the host pushes without being asked".
func (p *Plugin) handleStream(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapStreamRead) {
		pluginhost.WriteCapabilityDenied(w, CapStreamRead)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "E_NO_STREAMING", "response writer cannot flush")
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", err.Error())
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// Disable proxy buffering (nginx and friends): a buffered "stream" is a
	// batch job, and it would silently break the push contract.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // ship headers before the first line blocks on the source

	// Per-line write deadline where the host stack supports it: the consumer
	// is expected to drain greedily (the host adapter does); a full socket
	// buffer means the client died without RST, and the source must be
	// released. Some host middleware wraps the ResponseWriter past what
	// http.NewResponseController can reach (gofastr's router does) — there
	// the deadline degrades to a no-op and disconnect detection falls back
	// to the request context, which fires on clean closes.
	rc := http.NewResponseController(w)
	deadlineSupported := rc.SetWriteDeadline(time.Now().Add(stallTimeout)) == nil

	_ = p.source(r.Context(), after, func(line Line) error {
		line.Text = sanitizeLine(line.Text)
		if line.Seq <= after {
			// The contract says the source filters; enforcing it here too
			// costs nothing and keeps reconnect replay exact.
			return nil
		}
		buf, mErr := json.Marshal(line)
		if mErr != nil {
			return mErr
		}
		buf = append(buf, '\n')
		if deadlineSupported {
			if dlErr := rc.SetWriteDeadline(time.Now().Add(stallTimeout)); dlErr != nil {
				return dlErr
			}
		}
		if _, wErr := w.Write(buf); wErr != nil {
			return wErr
		}
		flusher.Flush()
		return nil
	})
	// A cancelled context is the normal disconnect path, not an error; the
	// yield-error cases (dead client, stalled consumer) have no response
	// left to report — the stream just ends.
}

// parseAfter reads ?after=N — the last sequence number the frame
// acknowledged, sent on (re)connect so replay resumes exactly.
func parseAfter(r *http.Request) (uint64, error) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		return 0, nil
	}
	after, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("after must be a non-negative integer")
	}
	return after, nil
}

// sanitizeLine enforces the line contract before the text crosses: the
// stream is LINE-oriented, so embedded newlines/CRs become spaces (they
// would otherwise render as fake multi-line entries and skew the frame's
// scrollback accounting), and overlong lines are truncated at the byte
// bound. ANSI escapes are preserved untouched.
func sanitizeLine(text string) string {
	if strings.ContainsAny(text, "\r\n") {
		text = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(text)
	}
	if len(text) > maxLineBytes {
		text = text[:maxLineBytes]
	}
	return text
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]string{"error": code}
	if message != "" {
		body["message"] = message
	}
	_ = json.NewEncoder(w).Encode(body)
}
