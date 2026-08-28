package main

// The logstream demo's producer: a deterministic synthetic log generator
// (fixed formulas, no filesystem, no network, no real subprocess) plus the
// rate switch the demo page POSTs to.
//
// Deterministic on purpose — the e2e journey recomputes the same formulas in
// TypeScript and asserts exact line content, which only works if line N is a
// pure function of N.
//
// The formulas (mirrored in e2e/tests/logstream-journeys.spec.ts — keep in
// sync):
//
//	ts    = "%02d:%02d:%02d" % (n/3600%24, n/60%60, n%60)
//	level = levels[n%5]      // {name, ansi} — INFO/WARN/ERROR/DEBUG/TRACE
//	svc   = services[(n*3)%5]
//	msg   = messages[(n*5)%7]
//	line  = ts + " " + ansi(level) + " " + svc + " " + msg + " seq=%06d"
//
// Rates: Calm 5 lines/s (a quiet service); Flood 6,000 lines/s — far past
// what the frame's declared consumption rate (~60 batches/s × 24 lines)
// can absorb, so the overflow path (host drops oldest, frame marks the gap)
// is always live at Flood. That lopsidedness is the demo.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DonaldMurillo/gofastr-plugins/logstream"
)

// demoLogRates are the two producer rates, in lines per second.
const (
	demoCalmLinesPerSec = 5
	demoFastLinesPerSec = 6000
)

// demoLogControlPath is the example app's rate-switch route. It is owned by
// the EXAMPLE, not the plugin: logstream is read-only (stream:read is its
// only capability), so the producer's controls belong to the app that owns
// the producer. The plugin's demo page learns it via WithDemoControlURL.
const demoLogControlPath = "/demo/logstream/rate"

var demoLogLevels = [5]struct{ name, ansi string }{
	{"INFO", "\x1b[32m"},  // green
	{"WARN", "\x1b[33m"},  // yellow
	{"ERROR", "\x1b[31m"}, // red
	{"DEBUG", "\x1b[36m"}, // cyan
	{"TRACE", "\x1b[90m"}, // bright-black (dim)
}

var demoLogServices = [5]string{"api-gateway", "auth-svc", "billing", "search", "worker"}

var demoLogMessages = [7]string{
	"request served",
	"cache hit for profile",
	"upstream latency high, retrying",
	"connection pool at 82% capacity",
	"token refreshed for session",
	"queue depth draining",
	"background job completed",
}

// demoLogLine builds line n purely from the formulas above.
func demoLogLine(n uint64) string {
	lvl := demoLogLevels[n%5]
	ts := fmt.Sprintf("%02d:%02d:%02d", (n/3600)%24, (n/60)%60, n%60)
	return fmt.Sprintf("%s %s%s\x1b[0m %s %s seq=%06d",
		ts, lvl.ansi, lvl.name,
		demoLogServices[(n*3)%5], demoLogMessages[(n*5)%7], n)
}

// demoLogGenerator is the demo's SourceFunc plus the rate switch. One
// goroutine mints lines on a ticker at the current rate and blocks on the
// consumer's channel when the reader lags — the Go side stays lossless (a
// stalled stream stalls the mint, it never silently skips seqs); dropping
// is the host ADAPTER's job, visibly and counted. A short replay history
// lets a reconnecting reader resume from the last acked seq.
type demoLogGenerator struct {
	mu      sync.Mutex
	rate    int // lines per second
	current uint64
	// history is a bounded replay ring of recent lines (connect-late /
	// reconnect consumers). sized generously: the flood rate fills it in
	// ~1s, which is exactly the reconnect window worth covering.
	history [1024]logstream.Line
	histLen int
	histOff int
}

var demoLogs = &demoLogGenerator{rate: demoCalmLinesPerSec}

func (g *demoLogGenerator) setRate(rate int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rate = rate
}

func (g *demoLogGenerator) rateNow() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rate
}

// next mints the next line (rate-independent: seq order is global).
func (g *demoLogGenerator) next() logstream.Line {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.current++
	line := logstream.Line{Seq: g.current, Text: demoLogLine(g.current)}
	g.history[g.histOff] = line
	g.histOff = (g.histOff + 1) % len(g.history)
	if g.histLen < len(g.history) {
		g.histLen++
	}
	return line
}

// replay yields buffered lines with Seq > after, oldest first.
func (g *demoLogGenerator) replay(after uint64, yield func(logstream.Line) error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range g.histLen {
		idx := (g.histOff - g.histLen + i + len(g.history)) % len(g.history)
		line := g.history[idx]
		if line.Seq <= after {
			continue
		}
		if err := yield(line); err != nil {
			return err
		}
	}
	return nil
}

// source is the plugin's WithSource: replay what the consumer has not acked,
// then tail the live mint at the current rate.
func (g *demoLogGenerator) source(ctx context.Context, after uint64, yield func(logstream.Line) error) error {
	if err := g.replay(after, yield); err != nil {
		return err
	}
	for {
		batch, interval := mintSchedule(g.rateNow())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
			for range batch {
				if err := yield(g.next()); err != nil {
					return err
				}
			}
		}
	}
}

// mintSchedule converts a lines-per-second rate into a (batch, interval) pair
// that a real machine can actually keep.
//
// Minting one line per tick looks right and is not: the flood rate of 6,000
// lines/s works out to a 166µs timer, and no scheduler wakes a goroutine that
// often under load. A GitHub runner achieved a small fraction of the nominal
// rate, which meant the producer never outran the frame's render loop, nothing
// was ever dropped, and the e2e overflow journey timed out waiting for a drop
// that could not happen. The demo page's own claim — switch to Flood and watch
// the counter climb — was false on the same machines for the same reason.
//
// Ticking no faster than every 5ms and minting a batch per tick keeps the
// achieved rate equal to the nominal one on fast and slow machines alike:
// 6,000 lines/s becomes 30 lines every 5ms, which is arithmetic rather than a
// race against the scheduler.
func mintSchedule(rate int) (batch int, interval time.Duration) {
	if rate < 1 {
		rate = 1
	}
	const minInterval = 5 * time.Millisecond
	interval = time.Second / time.Duration(rate)
	if interval >= minInterval {
		return 1, interval
	}
	batch = int(minInterval / interval)
	if batch < 1 {
		batch = 1
	}
	return batch, minInterval
}

// registerDemoLogControlRoute mounts the rate switch: POST {"rate":"calm"|"fast"}.
// Ungated demo convenience — same posture as the rest of the example app
// (which runs unauthenticated with WithDevGrantAll); a production host owns
// its producer and its authorization together.
func registerDemoLogControlRoute(rt interface {
	Get(string, http.Handler)
	Post(string, http.Handler)
}) {
	rt.Post(demoLogControlPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Rate string `json:"rate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Rate == "" {
			http.Error(w, `{"error":"E_BAD_REQUEST","message":"body must be {\"rate\":\"calm|fast\"}"}`, http.StatusBadRequest)
			return
		}
		switch body.Rate {
		case "calm":
			demoLogs.setRate(demoCalmLinesPerSec)
		case "fast":
			demoLogs.setRate(demoFastLinesPerSec)
		default:
			http.Error(w, `{"error":"E_BAD_RATE","message":"rate must be calm or fast"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rate":           body.Rate,
			"linesPerSecond": demoLogs.rateNow(),
		})
	}))
}
