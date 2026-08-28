package main

// The whiteboard demo's room hub — the HOST side of the collaboration the
// frame is forbidden to run itself.
//
// Shape (docs/whiteboard.md): every browser's adapter opens ONE SSE stream
// (GET /__gofastr/plugin/whiteboard/room/stream) and POSTs its update blobs
// (POST /room/publish). This hub is the meeting point:
//
//   - Subscribe assigns the participant an opaque pid (p-1, p-2, …) and a
//     colour from a fixed palette, yields hello, then replays the room's
//     persisted Yjs state, then forwards live events. IDENTITY IS THE
//     HOST'S TO DECIDE: an opaque id + a colour cross, nothing else — the
//     frame is untrusted, so presence never carries names.
//   - Publish fans each event out to every OTHER subscriber (the publisher
//     already has its own update) and appends sync updates to the room's
//     history. The history IS the persisted state: Yjs updates are
//     order-insensitive, so the accumulated blob list replays into a
//     converged board for any late joiner. Compaction would need Yjs in Go,
//     which is exactly the interpretation work the host is NOT doing — so
//     the hub bounds memory instead (roomHistoryCap) and fails loud, never
//     silent, when a demo room outgrows it.
//   - Presence is ephemeral: forwarded live, never persisted, no replay —
//     cursors refresh on their own within a second.
//
// A slow SSE consumer never blocks a publisher: each subscriber has a
// bounded queue and a consumer that overruns it is dropped (its stream
// ends; the reconnect handshake replays the history it missed). Updates are
// never lost because they live in the history, not in the queue.

import (
	"context"
	"sync"
	"time"

	whiteboard "github.com/DonaldMurillo/gofastr-plugins/whiteboard"
)

// wbPalette is the participant colour cycle the hub assigns from. These are
// DOCUMENT DATA (canvas stroke colours), not page CSS — the demo page itself
// stays token-only. Amber first so participant #1 matches the accent.
var wbPalette = []string{
	"oklch(0.82 0.155 78)", // amber (the demo accent)
	"oklch(0.72 0.13 250)", // blue
	"oklch(0.75 0.14 150)", // green
	"oklch(0.72 0.15 350)", // rose
	"oklch(0.74 0.13 300)", // violet
	"oklch(0.80 0.12 200)", // cyan
}

// wbQueueLen is each subscriber's outbound event queue. Reconnect replays
// the history, so an overrun costs a reconnect, not data.
const wbQueueLen = 64

// wbHistoryBytes caps one room's accumulated Yjs state. A demo board of
// ~2,000 strokes fits comfortably; beyond the cap the hub refuses the
// publish (E_ROOM_FULL) rather than grow without bound — loud, not silent.
const wbHistoryBytes = 32 << 20

// wbPingEvery keeps idle SSE connections alive through proxies.
const wbPingEvery = 15 * time.Second

type wbHubEvent struct {
	kind  string
	pid   string
	color string
	x, y  float64
	down  bool
	count int
	data  []byte
}

type wbSubscriber struct {
	pid    string
	color  string
	queue  chan wbHubEvent
	closed bool
}

type wbRoom struct {
	history    [][]byte
	historyLen int
	subs       []*wbSubscriber
}

type whiteboardHub struct {
	mu     sync.Mutex
	rooms  map[string]*wbRoom
	pidSeq int
}

func newWhiteboardHub() *whiteboardHub {
	return &whiteboardHub{rooms: make(map[string]*wbRoom)}
}

func (h *whiteboardHub) room(id string) *wbRoom {
	r, ok := h.rooms[id]
	if !ok {
		r = &wbRoom{}
		h.rooms[id] = r
	}
	return r
}

func (h *whiteboardHub) Subscribe(ctx context.Context, roomID string, yield func(whiteboard.RoomEvent) error) error {
	h.mu.Lock()
	r := h.room(roomID)
	h.pidSeq++
	pid := "p-" + itoa(h.pidSeq)
	color := wbPalette[(h.pidSeq-1)%len(wbPalette)]
	sub := &wbSubscriber{pid: pid, color: color, queue: make(chan wbHubEvent, wbQueueLen)}
	r.subs = append(r.subs, sub)
	count := len(r.subs)
	replay := make([][]byte, len(r.history))
	copy(replay, r.history)
	// Tell everyone else the room grew.
	for _, other := range r.subs {
		if other != sub {
			h.offer(other, wbHubEvent{kind: whiteboard.EventParticipants, count: len(r.subs)})
		}
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		for i, s := range r.subs {
			if s == sub {
				r.subs = append(r.subs[:i], r.subs[i+1:]...)
				break
			}
		}
		close(sub.queue)
		for _, other := range r.subs {
			h.offer(other, wbHubEvent{kind: whiteboard.EventParticipants, count: len(r.subs)})
		}
		if len(r.subs) == 0 && r.historyLen == 0 {
			delete(h.rooms, roomID) // empty rooms do not linger
		}
		h.mu.Unlock()
	}()

	// hello: identity is assigned HERE, by the host. An opaque id + a colour,
	// never a name — the frame is untrusted.
	if err := yield(whiteboard.RoomEvent{Kind: whiteboard.EventHello, PID: pid, Color: color, Count: count}); err != nil {
		return err
	}
	// Replay: the joiner converges on the existing board instead of starting
	// empty. Applying an update set twice is a no-op in Yjs, so replay is
	// safe even for a rejoining participant.
	for _, u := range replay {
		if err := yield(whiteboard.RoomEvent{Kind: whiteboard.EventSync, Update: u}); err != nil {
			return err
		}
	}

	ping := time.NewTicker(wbPingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ping.C:
			if err := yield(whiteboard.RoomEvent{Kind: whiteboard.EventPing}); err != nil {
				return err
			}
		case ev, ok := <-sub.queue:
			if !ok {
				return nil // dropped by publish (consumer too slow) — reconnect replays
			}
			if err := yield(whiteboard.RoomEvent{
				Kind: ev.kind, PID: ev.pid, Color: ev.color,
				X: ev.x, Y: ev.y, Down: ev.down, Count: ev.count, Update: ev.data,
			}); err != nil {
				return err
			}
		}
	}
}

func (h *whiteboardHub) Publish(_ context.Context, roomID, fromPID string, ev whiteboard.RoomEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(roomID)

	switch ev.Kind {
	case whiteboard.EventSync:
		if r.historyLen+len(ev.Update) > wbHistoryBytes {
			return errRoomFull
		}
		// The copy matters: the caller's buffer is the HTTP handler's decoded
		// base64 slice; retaining it directly would pin the request body.
		u := make([]byte, len(ev.Update))
		copy(u, ev.Update)
		r.history = append(r.history, u)
		r.historyLen += len(u)
		h.fanOut(r, fromPID, wbHubEvent{kind: ev.Kind, pid: fromPID, data: u})
	case whiteboard.EventPresence:
		// Attach the publisher's assigned colour so receivers never trust a
		// client-chosen one.
		var color string
		for _, s := range r.subs {
			if s.pid == fromPID {
				color = s.color
				break
			}
		}
		h.fanOut(r, fromPID, wbHubEvent{kind: ev.Kind, pid: fromPID, color: color, x: ev.X, y: ev.Y, down: ev.Down})
	default:
		return errBadKind
	}
	return nil
}

// fanOut delivers to every subscriber EXCEPT the originator (it already has
// its own update). A full queue means the consumer is dead or stalled: the
// subscriber is dropped — its data is safe in the history, and its reconnect
// replays everything it missed. h.mu must be held.
func (h *whiteboardHub) fanOut(r *wbRoom, fromPID string, ev wbHubEvent) {
	kept := r.subs[:0]
	for _, s := range r.subs {
		if s.pid == fromPID {
			kept = append(kept, s)
			continue
		}
		if s.closed {
			continue
		}
		select {
		case s.queue <- ev:
			kept = append(kept, s)
		default:
			s.closed = true
			close(s.queue) // the Subscribe loop sees !ok and ends the stream
		}
	}
	r.subs = kept
}

// offer is a best-effort enqueue for hub-originated events (participants
// counts); a dropped subscriber is already closed and skipped by fanOut.
// h.mu must be held.
func (h *whiteboardHub) offer(s *wbSubscriber, ev wbHubEvent) {
	if s.closed {
		return
	}
	select {
	case s.queue <- ev:
	default:
		// Queue full: the next sync fan-out will reap this subscriber.
	}
}

type wbHubError string

func (e wbHubError) Error() string { return string(e) }

const (
	errRoomFull = wbHubError("room history cap reached (32 MiB) — start a new room")
	errBadKind  = wbHubError("hub publishes only sync/presence events")
)

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
