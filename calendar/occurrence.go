package calendar

import (
	"fmt"
	"sort"
	"time"
)

// occurrence.go is the pipeline that turns host-side event definitions into
// the resolved occurrences the frame renders:
//
//	series expansion (rrule.go, wall math)
//	  → override application (per-instance edits)
//	    → zone resolution (zones.go: gaps, folds, offsets)
//	      → conflict detection (interval overlap, instants)
//
// Every stage runs here, in Go. The frame receives the output of the last
// stage only and has no way to recompute any of it — that is the plugin's
// whole argument, made structural.

// occurrenceWindow is the resolved answer to one /occurrences request.
type occurrenceWindow struct {
	Occurrences []Occurrence `json:"occurrences"`
	Conflicts   [][2]string  `json:"conflicts"` // pairs of occurrence IDs
	Transitions []Transition `json:"transitions"`
	Zone        string       `json:"zone"`
	From        string       `json:"from"`
	To          string       `json:"to"`
}

// conflictsOf is the flattened conflict pair list (ids in sorted order per
// pair) for the wire. Kept alongside per-occurrence ConflictIDs so the frame
// can style both endpoints from one source of truth.
func (w occurrenceWindow) conflictPairs() [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	for _, occ := range w.Occurrences {
		for _, other := range occ.ConflictIDs {
			a, b := occ.ID, other
			if b < a {
				a, b = b, a
			}
			key := a + "|" + b
			if !seen[key] {
				seen[key] = true
				out = append(out, [2]string{a, b})
			}
		}
	}
	return out
}

// buildOccurrences expands every event over [from, to] (wall dates,
// inclusive), applies overrides, resolves zones, and computes conflicts.
//
// Window filtering happens on the EFFECTIVE (post-override) start date: an
// instance moved into March by an override must appear in a March request
// even though the series generated it in February. That is why enumSeries is
// given a horizon padded by maxMoveDays and the filter runs after overrides.
func buildOccurrences(events []Event, overrides map[overrideKey]Override, from, to wall) (*occurrenceWindow, error) {
	loc0 := time.UTC
	var zoneName string
	if len(events) > 0 {
		loc, err := loadZone(events[0].Zone)
		if err == nil {
			loc0 = loc
			zoneName = events[0].Zone
		}
	}

	// Transitions for the range, for the frame's markers and for wording
	// gap/fold notes (one zone's ladder is enough for the notes: notes are
	// per-event and re-derive their own transition below when needed).
	trs := transitionsInRange(loc0, from, to)

	horizon := to.addDays(maxMoveDays + 2)
	var occs []Occurrence
	for _, ev := range events {
		if err := ValidateEvent(ev); err != nil {
			return nil, err
		}
		loc, err := loadZone(ev.Zone)
		if err != nil {
			return nil, err
		}
		eventTrs := trs
		if ev.Zone != zoneName {
			eventTrs = transitionsInRange(loc, from, to)
		}

		startW, err := parseWallAuto(ev.Start)
		if err != nil {
			return nil, err
		}
		duration := 0
		endW := wall{}
		if ev.AllDay {
			endW, err = parseWallDate(ev.End)
			if err != nil {
				return nil, err
			}
		} else {
			endW, err = parseWall(ev.End)
			if err != nil {
				return nil, err
			}
			duration = endW.minutesFrom(startW)
		}

		visit := func(instanceStart wall) bool {
			identityDate := instanceStart.dateStr()
			effStart, effDuration := instanceStart, duration
			effAllDayEnd := endW
			exception := false
			if ov, ok := overrides[overrideKey{ev.ID, identityDate}]; ok {
				exception = true
				if ov.Start != "" {
					s, err := parseWallAuto(ov.Start)
					if err == nil {
						effStart = s
					}
				}
				if ov.End != "" {
					e, err := parseWallAuto(ov.End)
					if err == nil {
						if ev.AllDay {
							effAllDayEnd = e
						} else {
							effDuration = e.minutesFrom(effStart)
							if effDuration < 0 {
								effDuration = 0
							}
						}
					}
				}
			}

			// Effective-date window filter (timed: start date; all-day:
			// overlaps [from, to+1) at day granularity).
			if ev.AllDay {
				if effAllDayEnd.before(from) || !effStart.atMidnight().before(to.addDays(1)) {
					return true
				}
			} else {
				d := effStart.dateStr()
				if d < from.dateStr() || d > to.dateStr() {
					// An event can still intersect the window when it SPANS
					// into it (start before `from`, end after).
					if !(effStart.before(from.atMidnight()) && effStart.addMinutes(effDuration).after(from.atMidnight())) {
						return true
					}
				}
			}

			occ, err := resolveOccurrence(ev, loc, eventTrs, effStart, effDuration, effAllDayEnd, identityDate, exception)
			if err != nil {
				return true // a definition error surfaced by ValidateEvent already
			}
			occs = append(occs, occ)
			return true
		}

		if ev.RRule != nil {
			if _, err := enumSeries(ev, horizon, visit); err != nil {
				return nil, err
			}
		} else {
			visit(startW)
		}
	}

	sort.Slice(occs, func(i, j int) bool {
		if occs[i].StartUTC != occs[j].StartUTC {
			return occs[i].StartUTC < occs[j].StartUTC
		}
		return occs[i].ID < occs[j].ID
	})
	computeConflicts(occs)

	return &occurrenceWindow{
		Occurrences: occs,
		Conflicts:   conflictPairsOf(occs),
		Transitions: trs,
		Zone:        zoneName,
		From:        from.dateStr(),
		To:          to.dateStr(),
	}, nil
}

// resolveOccurrence turns one effective wall instance into a resolved
// Occurrence: instants from zones.go, wall strings derived back from the
// resolved instants (so a gap-carried end shows where it LANDED, not where
// the naive wall math put it), DST note when a resolution was not exact.
func resolveOccurrence(ev Event, loc *time.Location, trs []Transition, effStart wall, durationMin int, allDayEnd wall, identityDate string, exception bool) (Occurrence, error) {
	occ := Occurrence{
		ID:        ev.ID + "/" + identityDate,
		EventID:   ev.ID,
		Title:     ev.Title,
		AllDay:    ev.AllDay,
		Zone:      ev.Zone,
		Recurring: ev.RRule != nil,
		Exception: exception,
	}

	if ev.AllDay {
		startInstant, _ := resolveWall(effStart.atMidnight(), loc)
		endInstant, _ := resolveWall(allDayEnd.atMidnight(), loc)
		occ.StartUTC = startInstant.UTC().Format(time.RFC3339)
		occ.EndUTC = endInstant.UTC().Format(time.RFC3339)
		occ.StartWall = effStart.dateStr()
		occ.EndWall = allDayEnd.dateStr()
		occ.Days = allDayEnd.daysFrom(effStart)
		if occ.Days < 1 {
			occ.Days = 1
		}
		occ.ZoneAbbr, occ.OffsetMin = zoneAt(startInstant, loc)
		return occ, nil
	}

	effEnd := effStart.addMinutes(durationMin)
	startInstant, startRes := resolveWall(effStart, loc)
	endInstant, endRes := resolveWall(effEnd, loc)

	occ.StartUTC = startInstant.UTC().Format(time.RFC3339)
	occ.EndUTC = endInstant.UTC().Format(time.RFC3339)
	// Wall strings derive from the RESOLVED instants: a gap-carried 02:15
	// must display as 03:15 (where it landed), never as the nonexistent
	// time the naive duration produced.
	occ.StartWall = startInstant.In(loc).Format(wallLayout)
	occ.EndWall = endInstant.In(loc).Format(wallLayout)
	occ.ZoneAbbr, occ.OffsetMin = zoneAt(startInstant, loc)
	occ.SpansMidnight = occ.StartWall[:10] != occ.EndWall[:10]

	if startRes != resExact {
		if tr := transitionForWall(trs, effStart, startRes); tr != nil {
			occ.DSTNote = dstNote("start", effStart, startRes, tr, wallFromTime(startInstant.In(loc)))
		}
	} else if endRes != resExact {
		if tr := transitionForWall(trs, effEnd, endRes); tr != nil {
			occ.DSTNote = dstNote("end", effEnd, endRes, tr, wallFromTime(endInstant.In(loc)))
		}
	}
	return occ, nil
}

// computeConflicts marks overlapping occurrences. Overlap is on INSTANTS
// (a.start < b.end && b.start < a.end); all-day events participate through
// their whole-day instants. Instances of the SAME event never conflict with
// each other — consecutive instances of one series overlapping is a series
// definition problem, not a scheduling conflict between two commitments.
//
// Sorted-by-start sweep: each occurrence only needs to look at the ones
// still open ahead of it, so the cost is O(n log n) for the sort plus
// output-size for the sweep.
func computeConflicts(occs []Occurrence) {
	type entry struct {
		start, end time.Time
		occ        *Occurrence
	}
	entries := make([]entry, 0, len(occs))
	for i := range occs {
		s, err1 := time.Parse(time.RFC3339, occs[i].StartUTC)
		e, err2 := time.Parse(time.RFC3339, occs[i].EndUTC)
		if err1 != nil || err2 != nil {
			continue
		}
		entries = append(entries, entry{s, e, &occs[i]})
	}
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			if !entries[j].start.Before(entries[i].end) {
				break // sorted by start: nothing later can overlap i
			}
			if entries[i].occ.EventID == entries[j].occ.EventID {
				continue
			}
			a, b := entries[i].occ, entries[j].occ
			a.ConflictIDs = append(a.ConflictIDs, b.ID)
			b.ConflictIDs = append(b.ConflictIDs, a.ID)
		}
	}
}

// conflictPairsOf flattens per-occurrence ConflictIDs into unique sorted
// pairs for the wire.
func conflictPairsOf(occs []Occurrence) [][2]string {
	seen := map[string]bool{}
	var out [][2]string
	for _, occ := range occs {
		for _, other := range occ.ConflictIDs {
			a, b := occ.ID, other
			if b < a {
				a, b = b, a
			}
			key := a + "|" + b
			if !seen[key] {
				seen[key] = true
				out = append(out, [2]string{a, b})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	if out == nil {
		out = [][2]string{}
	}
	return out
}

// findInstance locates one occurrence by event ID + ORIGINAL series date and
// returns its effective wall times (override applied) plus its wall
// duration. It is the read side of a move: the server re-derives what the
// occurrence currently is rather than trusting the frame's copy of it.
func findInstance(ev Event, overrides map[overrideKey]Override, date string) (effStart wall, durationMin int, allDayEnd wall, exception bool, ok bool) {
	startW, err := parseWallAuto(ev.Start)
	if err != nil {
		return wall{}, 0, wall{}, false, false
	}
	var endW wall
	if ev.AllDay {
		endW, err = parseWallDate(ev.End)
	} else {
		endW, err = parseWall(ev.End)
	}
	if err != nil {
		return wall{}, 0, wall{}, false, false
	}
	duration := 0
	if !ev.AllDay {
		duration = endW.minutesFrom(startW)
	}

	match := func(candidate wall) bool { return candidate.dateStr() == date }
	if ev.RRule != nil {
		horizon := startW
		if d, err := parseWallDate(date); err == nil {
			horizon = d.addDays(1)
		}
		found := false
		_, err := enumSeries(ev, horizon, func(candidate wall) bool {
			if match(candidate) {
				found = true
				startW = candidate
				return false
			}
			return true
		})
		if err != nil || !found {
			return wall{}, 0, wall{}, false, false
		}
	} else if !match(startW) {
		return wall{}, 0, wall{}, false, false
	}

	effStart, effDuration, effAllDayEnd, exception := startW, duration, endW, false
	if ov, hit := overrides[overrideKey{ev.ID, date}]; hit {
		exception = true
		if ov.Start != "" {
			if s, err := parseWallAuto(ov.Start); err == nil {
				effStart = s
			}
		}
		if ov.End != "" {
			if e, err := parseWallAuto(ov.End); err == nil {
				if ev.AllDay {
					effAllDayEnd = e
				} else {
					effDuration = e.minutesFrom(effStart)
				}
			}
		}
	}
	return effStart, effDuration, effAllDayEnd, exception, true
}

// applyMove is the server half of a drag: given the occurrence's identity
// and the WALL-CLOCK delta the frame computed from its grid, it computes the
// new wall times, resolves them through the zone (this is where a drag that
// lands on a DST boundary gets its real answer), records the override, and
// returns the re-resolved occurrence with the numbers the demo readout
// shows: the delta the user asked for, the delta the wall clock actually
// moved, and the delta that will elapse on a real clock.
func applyMove(ev Event, overrides map[overrideKey]Override, req MoveRequest, events []Event) (*MoveResult, error) {
	curStart, duration, allDayEnd, _, ok := findInstance(ev, overrides, req.Date)
	if !ok {
		return nil, fmt.Errorf("occurrence %s/%s does not exist", ev.ID, req.Date)
	}
	loc, err := loadZone(ev.Zone)
	if err != nil {
		return nil, err
	}

	var newStart, newEnd wall
	if ev.AllDay {
		if req.MinuteDelta != 0 {
			return nil, fmt.Errorf("all-day events move by whole days only")
		}
		newStart = curStart.addDays(req.DayDelta)
		newEnd = allDayEnd.addDays(req.DayDelta)
	} else {
		// Wall-clock move: apply the delta to the LOCAL fields, then let
		// resolveWall decide what instant (if any) that names. A start that
		// lands inside a spring-forward gap is carried PAST the gap first —
		// the end must build on the normalized wall, or a 30-minute meeting
		// dragged onto 02:30 would render 3:30→3:00.
		newStart = curStart.addDays(req.DayDelta).addMinutes(req.MinuteDelta)
		startInstant, startRes := resolveWall(newStart, loc)
		if startRes == resGap {
			newStart = wallFromTime(startInstant.In(loc))
		}
		newEnd = newStart.addMinutes(duration)
	}

	// Record the override keyed by the ORIGINAL series date — identity is
	// stable across moves, and the series definition is untouched.
	endStr := ""
	if ev.AllDay {
		endStr = newEnd.dateStr()
	} else {
		endStr = newEnd.String()
	}
	startStr := newStart.dateStr()
	if !ev.AllDay {
		startStr = newStart.String()
	}
	ov := Override{EventID: ev.ID, Date: req.Date, Start: startStr, End: endStr}

	// Re-resolve through a window wide enough for conflicts + transitions.
	winFrom := newStart.addDays(-1)
	winTo := newStart.addDays(2)
	trs := transitionsInRange(loc, winFrom, winTo)
	var moved Occurrence
	resolved, err := resolveOccurrence(ev, loc, trs, newStart, duration, newEnd, req.Date, true)
	if err != nil {
		return nil, err
	}
	moved = resolved

	// Conflicts for the moved occurrence, computed against everything else
	// in its new vicinity — in Go, per the plugin's contract.
	overridden := make(map[overrideKey]Override, len(overrides)+1)
	for k, v := range overrides {
		overridden[k] = v
	}
	overridden[ov.key()] = ov
	win, err := buildOccurrences(events, overridden, winFrom, winTo)
	if err != nil {
		return nil, err
	}
	for i := range win.Occurrences {
		if win.Occurrences[i].ID == moved.ID {
			moved = win.Occurrences[i]
			break
		}
	}

	// The numbers. requestedWall: what the grid asked for. actualWall: how
	// far the wall clock really moved (differs when the target fell in a
	// gap). elapsed: real time between the old and new start instants.
	requestedWall := req.DayDelta*24*60 + req.MinuteDelta
	var oldOcc Occurrence
	if w, err := buildOccurrences([]Event{ev}, overrides, curStart.addDays(-1), curStart.addDays(2)); err == nil {
		for _, o := range w.Occurrences {
			if o.ID == ev.ID+"/"+req.Date {
				oldOcc = o
				break
			}
		}
	}
	actualWall := 0
	elapsed := 0
	note := moved.DSTNote
	if oldOcc.ID != "" {
		oldStart, _ := time.Parse(time.RFC3339, oldOcc.StartUTC)
		newStartInstant, _ := time.Parse(time.RFC3339, moved.StartUTC)
		elapsed = int(newStartInstant.Sub(oldStart).Minutes())
		oldW, _ := parseWall(oldOcc.StartWall)
		newW, _ := parseWall(moved.StartWall)
		actualWall = newW.minutesFrom(oldW)
		if note == "" && requestedWall != actualWall && oldOcc.DSTNote != "" {
			note = "the event already sat on a DST boundary: " + oldOcc.DSTNote
		}
	}

	return &MoveResult{
		Occurrence:           moved,
		Override:             ov,
		RequestedWallMinutes: requestedWall,
		ActualWallMinutes:    actualWall,
		ElapsedMinutes:       elapsed,
		Zone:                 ev.Zone,
		ZoneAbbr:             moved.ZoneAbbr,
		OffsetMinutes:        moved.OffsetMin,
		Note:                 note,
	}, nil
}

// MoveResult is the server's answer to one move intent. The frame renders
// the occurrence; the readout fields are the demo's proof.
type MoveResult struct {
	Occurrence Occurrence `json:"occurrence"`
	// Override is the persisted per-instance edit (start/end wall strings).
	// The adapter does not need it; it is in the payload so Go tests and the
	// demo can assert exactly what was recorded.
	Override             Override `json:"override"`
	RequestedWallMinutes int      `json:"requestedWallMinutes"`
	ActualWallMinutes    int      `json:"actualWallMinutes"`
	ElapsedMinutes       int      `json:"elapsedMinutes"`
	Zone                 string   `json:"zone"`
	ZoneAbbr             string   `json:"zoneAbbr"`
	OffsetMinutes        int      `json:"offsetMinutes"`
	Note                 string   `json:"note,omitempty"`
}
