package calendar

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	// Embed the IANA database: the plugin's whole claim is that the SERVER
	// answers zone questions, so it must answer them on a host with no system
	// tzdata (scratch/distroless containers, Windows). ~450 KB once per binary
	// that imports this package.
	_ "time/tzdata"
)

// zones.go resolves naive wall-clock times to instants. This is the file the
// brief means by "timezones belong in Go": every gap and ambiguity decision
// the frame would otherwise guess at is made here, once, with a policy that
// the tests pin down.
//
// Policy (documented in docs/calendar.md, tested in zones_test.go):
//
//   - EXACT      — the wall time maps to one instant. The normal case.
//   - AMBIGUOUS  — fall-back fold: the wall time occurs twice. The FIRST
//                  (earlier) occurrence wins — RFC 5545's choice, and the
//                  one calendar users expect ("the 1:30 meeting" means the
//                  first 1:30).
//   - GAP        — spring-forward gap: the wall time never occurs. It is
//                  carried by the PRE-transition offset, so it lands after
//                  the gap in display terms: 02:30 on 2026-03-08 in
//                  America/New_York becomes 07:30Z, which renders 03:30 EDT.
//                  A nonexistent time shifts forward past the gap rather
//                  than backward before it — an event "at 02:30" should not
//                  land before the wall clock it was given.
//
// The implementation never relies on time.Date's zone behaviour, which the
// standard library documents as "not guaranteed" for nonexistent and
// ambiguous inputs. Instead it probes the zone's offsets around the nominal
// instant and checks each candidate for self-consistency — deterministic by
// construction, and unit-testable against zones with 30-minute transitions
// (Lord Howe) and half-hour offsets (Kolkata), not just the US hour steps.

// zoneNameRE admits IANA-shaped zone names only: segments of letters,
// digits, '_', '-', '+', joined by '/'. This blocks path escape
// ("../Local"), the special name "Local", and anything LoadLocation would
// interpret surprisingly. The subsequent LoadLocation call is the real
// authority; the regex exists so rejections carry a useful message.
var zoneNameRE = regexp.MustCompile(`^[A-Za-z0-9_+\-]+(/[A-Za-z0-9_+\-]+)*$`)

// loadZone parses and caches an IANA zone name. "UTC" is accepted; "Local"
// and fixed-offset strings are refused — a calendar anchored to "whatever
// zone the server process happens to run in" is exactly the silent guess
// this plugin exists to eliminate.
func loadZone(name string) (*time.Location, error) {
	if !zoneNameRE.MatchString(name) || len(name) > 64 {
		return nil, fmt.Errorf("zone %q is not an IANA name", name)
	}
	if name == "Local" || strings.HasPrefix(name, "/") {
		return nil, fmt.Errorf("zone %q is not allowed (server-local zones are a silent guess)", name)
	}
	return time.LoadLocation(name)
}

// resolution says how a wall time mapped onto the zone.
type resolution string

const (
	resExact     resolution = "exact"
	resAmbiguous resolution = "ambiguous" // fold: first occurrence used
	resGap       resolution = "gap"       // nonexistent: pre-transition offset carried
)

// resolveWall maps a naive wall time in loc to the instant it names.
// See the policy comment at the top of this file.
func resolveWall(w wall, loc *time.Location) (time.Time, resolution) {
	nominal := w.t() // wall fields as a UTC instant — the probe anchor

	// Candidate offsets: probe the zone at instants well around `nominal`.
	// Real zone offsets are bounded (±24h) and transitions move the offset by
	// minutes-to-hours, so nominal±48h observes every offset that could
	// possibly apply to a candidate within ±24h of nominal.
	offs := map[int]bool{}
	for _, d := range [3]time.Duration{-48 * time.Hour, 0, 48 * time.Hour} {
		_, off := nominal.Add(d).In(loc).Zone()
		offs[off] = true
	}

	// A candidate instant u = nominal − offset is valid iff the zone agrees
	// that u sits at that offset (self-consistency: reading u's wall clock
	// back out yields exactly `w`).
	var valid []time.Time
	for off := range offs {
		u := nominal.Add(-time.Duration(off) * time.Second)
		if _, off2 := u.In(loc).Zone(); off2 == off {
			valid = append(valid, u.UTC())
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Before(valid[j]) })

	switch {
	case len(valid) >= 2:
		return valid[0], resAmbiguous // first occurrence
	case len(valid) == 1:
		return valid[0], resExact
	default:
		// Gap: no self-consistent instant. Carry the pre-transition offset —
		// in a spring-forward the offset INCREASES, so the pre-transition
		// value is the smallest observed. nominal − min(offsets) lands after
		// the gap in wall terms (02:30 EST-carried renders as 03:30 EDT).
		min := 1 << 30
		for off := range offs {
			if off < min {
				min = off
			}
		}
		return nominal.Add(-time.Duration(min) * time.Second).UTC(), resGap
	}
}

// zoneAt returns the zone's abbreviation and offset-in-minutes at an instant
// — used for Occurrence.ZoneAbbr / OffsetMin and the demo's readout.
func zoneAt(t time.Time, loc *time.Location) (abbr string, offsetMin int) {
	abbr, off := t.In(loc).Zone()
	return abbr, off / 60
}

// formatOffset renders an offset-in-minutes the way people read it:
// "UTC−05:00", "UTC+05:30", "UTC".
func formatOffset(offsetMin int) string {
	if offsetMin == 0 {
		return "UTC"
	}
	sign := "+"
	if offsetMin < 0 {
		sign = "-"
		offsetMin = -offsetMin
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, offsetMin/60, offsetMin%60)
}

// transitionsInRange finds every offset change whose LOCAL date intersects
// [from, to] (wall dates, inclusive). The transition instant is located by
// bisection to the minute — enough for every zone in tzdb (Lord Howe's
// 30-minute shift is the exotic end), and the frame only needs minute
// precision to draw the marker.
func transitionsInRange(loc *time.Location, from, to wall) []Transition {
	var out []Transition
	// Walk in instant space over the nominal range padded on both sides, so
	// offsets up to ±12h and a transition just outside the wall range are
	// still observed where they affect a local date inside it.
	start := from.atMidnight().t().Add(-14 * time.Hour)
	end := to.endOfDay().t().Add(14 * time.Hour)
	lo := start
	for lo.Before(end) {
		_, offLo := lo.In(loc).Zone()
		hi := lo.Add(24 * time.Hour)
		_, offHi := hi.In(loc).Zone()
		if offLo == offHi {
			lo = hi
			continue
		}
		// Offset changed inside (lo, hi]: bisect to the minute. Invariant:
		// offset(low) = offLo ≠ offset(high).
		low, high := lo, hi
		for high.Sub(low) > time.Minute {
			mid := low.Add(high.Sub(low) / 2 / time.Minute * time.Minute)
			if _, om := mid.In(loc).Zone(); om == offLo {
				low = mid
			} else {
				high = mid
			}
		}
		// `high` is the first minute at the new offset. The wall times the
		// transition maps FROM and TO are read in the zone itself; WallFrom
		// is WALL arithmetic (03:00 − 60min = 02:00), never instant
		// arithmetic — subtracting an hour from the instant would cross the
		// offset change and land on 01:00.
		_, offBefore := high.Add(-time.Minute).In(loc).Zone()
		_, offAfter := high.In(loc).Zone()
		deltaMin := (offAfter - offBefore) / 60
		localWall := high.In(loc)
		wallFrom := wallFromTime(localWall).addMinutes(-deltaMin).t().Format("15:04")
		tr := Transition{
			Date:         localWall.Format(wallDateLayout),
			InstantUTC:   high.UTC().Format(time.RFC3339),
			WallFrom:     wallFrom,
			WallTo:       localWall.Format("15:04"),
			DeltaMinutes: deltaMin,
		}
		if deltaMin > 0 {
			tr.Kind = "forward"
		} else {
			tr.Kind = "back"
		}
		if d, err := parseWallDate(tr.Date); err == nil && !d.before(from) && d.before(to.addDays(1)) {
			out = append(out, tr)
		}
		lo = high
	}
	return out
}

// dstNote builds the human explanation attached to an occurrence whose
// start or end resolved through a gap or fold. These notes are the demo
// page's script: the server saying, in words, why the wall clock moved more
// (or less) than the drag asked for.
func dstNote(field string, w wall, res resolution, tr *Transition, resolved wall) string {
	switch res {
	case resGap:
		return fmt.Sprintf(
			"%s %s on %s does not exist (clocks jump %s→%s, %s); resolved to %s",
			field, w.t().Format("15:04"), w.dateStr(), tr.WallFrom, tr.WallTo,
			tr.deltaText(), resolved.String())
	case resAmbiguous:
		return fmt.Sprintf(
			"%s %s on %s occurs twice (clocks fall back %s→%s, %s); using the first",
			field, w.t().Format("15:04"), w.dateStr(), tr.WallFrom, tr.WallTo,
			tr.deltaText())
	}
	return ""
}

// deltaText renders a transition's size for notes: "+1h", "−30m".
func (t Transition) deltaText() string {
	m := t.DeltaMinutes
	if m < 0 {
		m = -m
	}
	sign := "+"
	if t.DeltaMinutes < 0 {
		sign = "−"
	}
	if m%60 == 0 {
		return fmt.Sprintf("%s%dh", sign, m/60)
	}
	return fmt.Sprintf("%s%dh%02dm", sign, m/60, m%60)
}

// transitionForWall finds the transition affecting a wall time's resolution
// (the one whose local date matches and whose kind explains the resolution),
// or nil when there is none nearby. Used only to word the notes.
func transitionForWall(trs []Transition, w wall, res resolution) *Transition {
	for i := range trs {
		tr := &trs[i]
		sameDate := tr.Date == w.dateStr()
		adjacent := tr.Date == w.addDays(1).dateStr() // late-evening wall before a midnight transition
		if !sameDate && !adjacent {
			continue
		}
		if (res == resGap && tr.Kind == "forward") || (res == resAmbiguous && tr.Kind == "back") {
			return tr
		}
	}
	return nil
}
