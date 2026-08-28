package calendar

import (
	"strings"
	"testing"
	"time"
)

// occurrence_test.go is where hand-written calendars die, table-tested:
// spring forward, fall back, events spanning the transition, recurring
// series straddling it, and moves whose wall-clock answer differs from the
// drag. Every expectation below was derived from the IANA rules by hand.

func nyEvent(id, start, end string, r *RRule) Event {
	return Event{ID: id, Title: id, Start: start, End: end, Zone: "America/New_York", RRule: r}
}

func windowOf(t *testing.T, events []Event, from, to string, overrides map[overrideKey]Override) *occurrenceWindow {
	t.Helper()
	f, err := parseWallDate(from)
	if err != nil {
		t.Fatalf("from: %v", err)
	}
	tt, err := parseWallDate(to)
	if err != nil {
		t.Fatalf("to: %v", err)
	}
	if overrides == nil {
		overrides = map[overrideKey]Override{}
	}
	win, err := buildOccurrences(events, overrides, f, tt)
	if err != nil {
		t.Fatalf("buildOccurrences: %v", err)
	}
	return win
}

func findOcc(win *occurrenceWindow, id string) *Occurrence {
	for i := range win.Occurrences {
		if win.Occurrences[i].ID == id {
			return &win.Occurrences[i]
		}
	}
	return nil
}

// durationMinutes computes the real elapsed minutes between two RFC3339
// instants — the number the wall clock hides on transition days.
func durationMinutes(t *testing.T, startUTC, endUTC string) int {
	t.Helper()
	s, err := time.Parse(time.RFC3339, startUTC)
	if err != nil {
		t.Fatalf("start %q: %v", startUTC, err)
	}
	e, err := time.Parse(time.RFC3339, endUTC)
	if err != nil {
		t.Fatalf("end %q: %v", endUTC, err)
	}
	return int(e.Sub(s).Minutes())
}

// --- 1. recurring series straddling spring forward ---------------------------

func TestDailySeriesStraddlingSpringForward(t *testing.T) {
	// Daily 09:00–09:30 from Fri Mar 6 through Tue Mar 10. The WALL clock
	// stays 09:00 every day; the INSTANTS shift by 23h across the Mar 8
	// transition — exactly what users expect from "daily 9:00 standup".
	win := windowOf(t,
		[]Event{nyEvent("standup", "2026-03-06T09:00", "2026-03-06T09:30", &RRule{Freq: "DAILY", Until: "2026-03-10"})},
		"2026-03-06", "2026-03-10", nil)

	type row struct{ date, wall, utc string }
	want := []row{
		{"2026-03-06", "09:00", "2026-03-06T14:00:00Z"}, // EST (−5)
		{"2026-03-07", "09:00", "2026-03-07T14:00:00Z"}, // EST
		{"2026-03-08", "09:00", "2026-03-08T13:00:00Z"}, // EDT (−4) — 23h after Saturday's instant
		{"2026-03-09", "09:00", "2026-03-09T13:00:00Z"}, // EDT
		{"2026-03-10", "09:00", "2026-03-10T13:00:00Z"}, // EDT
	}
	if len(win.Occurrences) != len(want) {
		t.Fatalf("got %d occurrences, want %d (%+v)", len(win.Occurrences), len(want), win.Occurrences)
	}
	prev := time.Time{}
	for i, w := range want {
		occ := win.Occurrences[i]
		if !strings.HasPrefix(occ.StartWall, w.date+"T"+w.wall) {
			t.Errorf("occ[%d].StartWall = %s, want %sT%s (wall clock must not shift)", i, occ.StartWall, w.date, w.wall)
		}
		if occ.StartUTC != w.utc {
			t.Errorf("occ[%d].StartUTC = %s, want %s", i, occ.StartUTC, w.utc)
		}
		if !prev.IsZero() {
			gap := int(prev.Sub(prev).Minutes())
			_ = gap
			now, _ := time.Parse(time.RFC3339, occ.StartUTC)
			if d := now.Sub(prev); d != 23*time.Hour && d != 24*time.Hour {
				t.Errorf("occ[%d]: instant gap from previous = %v, want 23h or 24h", i, d)
			}
		}
		cur, _ := time.Parse(time.RFC3339, occ.StartUTC)
		prev = cur
		// Mar 8 is the transition day: the abbr flips EST→EDT.
		if w.date == "2026-03-08" && occ.ZoneAbbr != "EDT" {
			t.Errorf("occ[%d].ZoneAbbr = %s, want EDT", i, occ.ZoneAbbr)
		}
		if occ.DSTNote != "" {
			t.Errorf("occ[%d]: unexpected DSTNote %q (09:00 resolves exactly both sides)", i, occ.DSTNote)
		}
	}
	// The window's transition list flags the day for the frame's marker.
	if len(win.Transitions) != 1 || win.Transitions[0].Date != "2026-03-08" {
		t.Fatalf("transitions = %+v, want exactly 2026-03-08", win.Transitions)
	}
}

// --- 2. recurring series straddling fall back --------------------------------

func TestDailySeriesStraddlingFallBack(t *testing.T) {
	win := windowOf(t,
		[]Event{nyEvent("check", "2026-10-30T09:00", "2026-10-30T09:30", &RRule{Freq: "DAILY", Until: "2026-11-03"})},
		"2026-10-30", "2026-11-03", nil)

	// Sat Oct 31 09:00 EDT = 13:00Z; Sun Nov 1 09:00 EST = 14:00Z — a 25h
	// day-gap between consecutive daily occurrences.
	wantUTC := []string{
		"2026-10-30T13:00:00Z",
		"2026-10-31T13:00:00Z",
		"2026-11-01T14:00:00Z",
		"2026-11-02T14:00:00Z",
		"2026-11-11-03", // placeholder replaced below
	}
	wantUTC[4] = "2026-11-03T14:00:00Z"
	if len(win.Occurrences) != 5 {
		t.Fatalf("got %d occurrences, want 5 (%+v)", len(win.Occurrences), win.Occurrences)
	}
	for i, want := range wantUTC {
		if win.Occurrences[i].StartUTC != want {
			t.Errorf("occ[%d].StartUTC = %s, want %s", i, win.Occurrences[i].StartUTC, want)
		}
		if !strings.HasSuffix(win.Occurrences[i].StartWall, "T09:00") {
			t.Errorf("occ[%d].StartWall = %s, wall clock must stay 09:00", i, win.Occurrences[i].StartWall)
		}
	}
	sat, _ := time.Parse(time.RFC3339, win.Occurrences[1].StartUTC)
	sun, _ := time.Parse(time.RFC3339, win.Occurrences[2].StartUTC)
	if d := sun.Sub(sat); d != 25*time.Hour {
		t.Errorf("fall-back day-gap = %v, want 25h (a 25-hour Sunday)", d)
	}
	if win.Occurrences[2].ZoneAbbr != "EST" {
		t.Errorf("Nov 1 abbr = %s, want EST", win.Occurrences[2].ZoneAbbr)
	}
}

// --- 3. events spanning / inside the transition -------------------------------

func TestEventSpanningSpringForwardGap(t *testing.T) {
	// 01:30–02:00 on Mar 8: the end lands inside the 02:00→03:00 gap. The
	// resolved end displays 03:00 EDT and the REAL duration is 30 minutes —
	// the wall clock claims 90.
	win := windowOf(t,
		[]Event{nyEvent("gapend", "2026-03-08T01:30", "2026-03-08T02:00", nil)},
		"2026-03-07", "2026-03-09", nil)
	occ := findOcc(win, "gapend/2026-03-08")
	if occ == nil {
		t.Fatalf("gapend missing from window: %+v", win.Occurrences)
	}
	if occ.StartUTC != "2026-03-08T06:30:00Z" {
		t.Errorf("start = %s, want 2026-03-08T06:30:00Z (01:30 EST)", occ.StartUTC)
	}
	if occ.EndWall != "2026-03-08T03:00" {
		t.Errorf("end wall = %s, want 2026-03-08T03:00 (carried past the gap)", occ.EndWall)
	}
	if d := durationMinutes(t, occ.StartUTC, occ.EndUTC); d != 30 {
		t.Errorf("real duration = %d min, want 30 (wall claims 90)", d)
	}
	if !strings.Contains(occ.DSTNote, "end 02:00") || !strings.Contains(occ.DSTNote, "does not exist") {
		t.Errorf("DSTNote = %q, want the end-gap explanation", occ.DSTNote)
	}
}

func TestEventEntirelyInsideTheGap(t *testing.T) {
	// 02:10–02:40 never exists at all. Both ends carry the pre-transition
	// offset: the meeting keeps its 30 real minutes and displays 03:10–03:40.
	win := windowOf(t,
		[]Event{nyEvent("ghost", "2026-03-08T02:10", "2026-03-08T02:40", nil)},
		"2026-03-07", "2026-03-09", nil)
	occ := findOcc(win, "ghost/2026-03-08")
	if occ == nil {
		t.Fatalf("ghost missing: %+v", win.Occurrences)
	}
	if occ.StartUTC != "2026-03-08T07:10:00Z" || occ.EndUTC != "2026-03-08T07:40:00Z" {
		t.Errorf("instants = %s → %s, want 07:10Z → 07:40Z", occ.StartUTC, occ.EndUTC)
	}
	if occ.StartWall != "2026-03-08T03:10" || occ.EndWall != "2026-03-08T03:40" {
		t.Errorf("walls = %s → %s, want 03:10 → 03:40", occ.StartWall, occ.EndWall)
	}
	if d := durationMinutes(t, occ.StartUTC, occ.EndUTC); d != 30 {
		t.Errorf("real duration = %d, want 30", d)
	}
	if !strings.Contains(occ.DSTNote, "does not exist") {
		t.Errorf("DSTNote = %q, want the gap explanation", occ.DSTNote)
	}
}

func TestThreeHourMeetingAcrossTheGapIsTwoRealHours(t *testing.T) {
	// 01:30–04:30 spans cleanly OVER the gap: both ends resolve exactly, no
	// note, but 3 wall hours are 2 real hours.
	win := windowOf(t,
		[]Event{nyEvent("spanner", "2026-03-08T01:30", "2026-03-08T04:30", nil)},
		"2026-03-07", "2026-03-09", nil)
	occ := findOcc(win, "spanner/2026-03-08")
	if occ == nil {
		t.Fatalf("spanner missing: %+v", win.Occurrences)
	}
	if occ.StartUTC != "2026-03-08T06:30:00Z" || occ.EndUTC != "2026-03-08T08:30:00Z" {
		t.Errorf("instants = %s → %s, want 06:30Z → 08:30Z", occ.StartUTC, occ.EndUTC)
	}
	if d := durationMinutes(t, occ.StartUTC, occ.EndUTC); d != 120 {
		t.Errorf("real duration = %d, want 120 (3 wall hours)", d)
	}
	if occ.DSTNote != "" {
		t.Errorf("DSTNote = %q, want empty (both ends exact)", occ.DSTNote)
	}
}

func TestAmbiguousHourEventUsesFirstOccurrence(t *testing.T) {
	// 01:30–02:30 on Nov 1: the start is ambiguous (01:30 EDT and 01:30 EST
	// both exist), the end is exact. Policy: FIRST occurrence — 1 wall hour
	// is 2 real hours.
	win := windowOf(t,
		[]Event{nyEvent("fold", "2026-11-01T01:30", "2026-11-01T02:30", nil)},
		"2026-10-31", "2026-11-02", nil)
	occ := findOcc(win, "fold/2026-11-01")
	if occ == nil {
		t.Fatalf("fold missing: %+v", win.Occurrences)
	}
	if occ.StartUTC != "2026-11-01T05:30:00Z" {
		t.Errorf("start = %s, want 2026-11-01T05:30:00Z (first 01:30, EDT)", occ.StartUTC)
	}
	if occ.ZoneAbbr != "EDT" || occ.OffsetMin != -240 {
		t.Errorf("zone = %s %d, want EDT −240", occ.ZoneAbbr, occ.OffsetMin)
	}
	if d := durationMinutes(t, occ.StartUTC, occ.EndUTC); d != 120 {
		t.Errorf("real duration = %d, want 120 (1 wall hour)", d)
	}
	if !strings.Contains(occ.DSTNote, "occurs twice") || !strings.Contains(occ.DSTNote, "using the first") {
		t.Errorf("DSTNote = %q, want the fold explanation", occ.DSTNote)
	}
}

// --- 4. all-day events, midnight spanners -------------------------------------

func TestAllDayAndMidnightEvents(t *testing.T) {
	win := windowOf(t, []Event{
		{ID: "offsite", Title: "Offsite", Start: "2026-03-12", End: "2026-03-14", AllDay: true, Zone: "America/New_York"},
		nyEvent("deploy", "2026-03-07T23:30", "2026-03-08T00:30", nil),
	}, "2026-03-06", "2026-03-15", nil)

	allday := findOcc(win, "offsite/2026-03-12")
	if allday == nil {
		t.Fatalf("all-day missing: %+v", win.Occurrences)
	}
	if allday.Days != 2 {
		t.Errorf("all-day Days = %d, want 2 (End 03-14 is exclusive)", allday.Days)
	}
	if allday.StartUTC != "2026-03-12T04:00:00Z" || allday.EndUTC != "2026-03-14T04:00:00Z" {
		t.Errorf("all-day instants = %s → %s, want whole days in zone (04:00Z EDT)", allday.StartUTC, allday.EndUTC)
	}

	deploy := findOcc(win, "deploy/2026-03-07")
	if deploy == nil {
		t.Fatalf("deploy missing: %+v", win.Occurrences)
	}
	if !deploy.SpansMidnight {
		t.Errorf("deploy.SpansMidnight = false, want true (23:30 → 00:30)")
	}
	if deploy.StartUTC != "2026-03-08T04:30:00Z" || deploy.EndUTC != "2026-03-08T05:30:00Z" {
		t.Errorf("deploy instants = %s → %s, want 04:30Z → 05:30Z", deploy.StartUTC, deploy.EndUTC)
	}
}

// --- 5. conflicts --------------------------------------------------------------

func TestConflictDetectionOnInstants(t *testing.T) {
	win := windowOf(t, []Event{
		nyEvent("board", "2026-03-11T13:00", "2026-03-11T15:00", nil),
		nyEvent("one2one", "2026-03-11T14:30", "2026-03-11T15:30", nil),
		nyEvent("adjacent", "2026-03-11T15:30", "2026-03-11T16:00", nil), // touches one2one's end: NOT a conflict
		{ID: "holiday", Title: "Holiday", Start: "2026-03-11", End: "2026-03-12", AllDay: true, Zone: "America/New_York"},
	}, "2026-03-10", "2026-03-12", nil)

	byID := map[string]*Occurrence{}
	for i := range win.Occurrences {
		byID[win.Occurrences[i].ID] = &win.Occurrences[i]
	}
	// board ↔ one2one: overlap 14:30–15:00.
	if len(byID["board/2026-03-11"].ConflictIDs) != 2 {
		t.Errorf("board conflicts = %v, want exactly [one2one holiday]", byID["board/2026-03-11"].ConflictIDs)
	}
	if len(byID["one2one/2026-03-11"].ConflictIDs) != 2 {
		t.Errorf("one2one conflicts = %v, want exactly [board holiday]", byID["one2one/2026-03-11"].ConflictIDs)
	}
	// adjacent touches one2one's end exactly — start == end is not overlap.
	// It IS inside the all-day holiday, which covers the whole day.
	if len(byID["adjacent/2026-03-11"].ConflictIDs) != 1 || byID["adjacent/2026-03-11"].ConflictIDs[0] != "holiday/2026-03-11" {
		t.Errorf("adjacent conflicts = %v, want only [holiday] (end-touching is not overlap)", byID["adjacent/2026-03-11"].ConflictIDs)
	}
	// all-day holiday overlaps every timed event that day.
	if len(byID["holiday/2026-03-11"].ConflictIDs) != 3 {
		t.Errorf("holiday conflicts = %v, want all three timed events", byID["holiday/2026-03-11"].ConflictIDs)
	}
	if len(win.Conflicts) != 4 {
		t.Fatalf("conflict pairs = %v, want 4 unique pairs", win.Conflicts)
	}
}

func TestSameSeriesInstancesNeverConflictWithThemselves(t *testing.T) {
	// A daily 25-hour event overlaps ITSELF on consecutive days. That is a
	// series-definition problem, not a scheduling conflict between two
	// commitments — it must not light up the conflict styling.
	win := windowOf(t,
		[]Event{nyEvent("long", "2026-03-06T22:00", "2026-03-07T23:00", &RRule{Freq: "DAILY", Until: "2026-03-09"})},
		"2026-03-06", "2026-03-09", nil)
	for _, occ := range win.Occurrences {
		if len(occ.ConflictIDs) != 0 {
			t.Errorf("%s self-conflicted with %v", occ.ID, occ.ConflictIDs)
		}
	}
}

// --- 6. overrides: editing one occurrence --------------------------------------

func TestOverrideMovesOneOccurrenceNotTheSeries(t *testing.T) {
	standup := nyEvent("standup", "2026-03-02T09:00", "2026-03-02T09:30",
		&RRule{Freq: "WEEKLY", ByDay: []string{"MO", "TU", "WE", "TH", "FR"}, Until: "2026-03-13"})
	// Move Wednesday Mar 11's standup to 10:00.
	overrides := map[overrideKey]Override{
		{"standup", "2026-03-11"}: {EventID: "standup", Date: "2026-03-11", Start: "2026-03-11T10:00", End: "2026-03-11T10:30"},
	}
	win := windowOf(t, []Event{standup}, "2026-03-09", "2026-03-13", overrides)

	moved := findOcc(win, "standup/2026-03-11")
	if moved == nil {
		t.Fatalf("moved occurrence missing: %+v", win.Occurrences)
	}
	if !strings.HasPrefix(moved.StartWall, "2026-03-11T10:00") || !moved.Exception {
		t.Errorf("moved occurrence = %s exception=%v, want 10:00 exception", moved.StartWall, moved.Exception)
	}
	// The rest of the week is untouched — the SERIES was not rewritten.
	for _, d := range []string{"2026-03-09", "2026-03-10", "2026-03-12", "2026-03-13"} {
		occ := findOcc(win, "standup/"+d)
		if occ == nil {
			t.Fatalf("standup %s vanished from the series", d)
		}
		if occ.Exception || !strings.HasSuffix(occ.StartWall, "T09:00") {
			t.Errorf("standup %s was rewritten: %s exception=%v", d, occ.StartWall, occ.Exception)
		}
	}
}

func TestOverrideFiltersOnEffectiveDate(t *testing.T) {
	// An occurrence OVERRIDDEN OUT of the requested window (from February)
	// must still appear in a March window — filtering happens after
	// overrides, on the effective date.
	feb := nyEvent("audit", "2026-02-25T09:00", "2026-02-25T10:00", nil)
	overrides := map[overrideKey]Override{
		{"audit", "2026-02-25"}: {EventID: "audit", Date: "2026-02-25", Start: "2026-03-05T09:00", End: "2026-03-05T10:00"},
	}
	win := windowOf(t, []Event{feb}, "2026-03-01", "2026-03-10", overrides)
	if occ := findOcc(win, "audit/2026-02-25"); occ == nil {
		t.Fatalf("overridden occurrence missing from its effective window: %+v", win.Occurrences)
	} else if !strings.HasPrefix(occ.StartWall, "2026-03-05") {
		t.Errorf("effective start = %s, want 2026-03-05", occ.StartWall)
	}
}

// --- 7. the move tables ----------------------------------------------------------

func moveReq(eventID, date string, dd, dm int) MoveRequest {
	return MoveRequest{DocID: "demo", EventID: eventID, Date: date, DayDelta: dd, MinuteDelta: dm}
}

func TestMoveAcrossSpringForwardGap(t *testing.T) {
	// The demo's money shot. An event at 01:30 EST — 30 minutes before the
	// gap — dragged one hour down the grid. The naive answer (02:30) does
	// not exist; the server carries it past the gap to 03:30 EDT.
	ev := nyEvent("pre", "2026-03-08T01:30", "2026-03-08T02:00", nil)
	events := []Event{ev}
	res, err := applyMove(ev, map[overrideKey]Override{}, moveReq("pre", "2026-03-08", 0, 60), events)
	if err != nil {
		t.Fatalf("applyMove: %v", err)
	}
	if res.RequestedWallMinutes != 60 {
		t.Errorf("requested = %d, want 60", res.RequestedWallMinutes)
	}
	if res.ActualWallMinutes != 120 {
		t.Errorf("actual wall = %d, want 120 (01:30 → 03:30)", res.ActualWallMinutes)
	}
	if res.ElapsedMinutes != 60 {
		t.Errorf("elapsed = %d, want 60 (06:30Z → 07:30Z)", res.ElapsedMinutes)
	}
	if res.Occurrence.StartWall != "2026-03-08T03:30" {
		t.Errorf("resolved start = %s, want 2026-03-08T03:30 EDT", res.Occurrence.StartWall)
	}
	if res.Occurrence.EndWall != "2026-03-08T04:00" {
		t.Errorf("resolved end = %s, want 2026-03-08T04:00 (wall duration preserved from the normalized start — never 3:00)", res.Occurrence.EndWall)
	}
	if res.Occurrence.ZoneAbbr != "EDT" || res.Occurrence.OffsetMin != -240 {
		t.Errorf("resolved zone = %s %d, want EDT −240", res.Occurrence.ZoneAbbr, res.Occurrence.OffsetMin)
	}
	if !strings.Contains(res.Note, "does not exist") {
		t.Errorf("note = %q, want the gap explanation", res.Note)
	}
	// The override records the NORMALIZED wall time — the stored start is
	// always a time that exists, so re-expansion is stable by construction
	// rather than by re-carrying the gap every time.
	if res.Override.Start != "2026-03-08T03:30" {
		t.Errorf("override start = %s, want the normalized 2026-03-08T03:30", res.Override.Start)
	}
	win := windowOf(t, events, "2026-03-07", "2026-03-09", map[overrideKey]Override{res.Override.key(): res.Override})
	after := findOcc(win, "pre/2026-03-08")
	if after == nil || after.StartWall != "2026-03-08T03:30" {
		t.Fatalf("re-expanded occurrence = %+v, want 03:30 (stable re-resolution)", after)
	}
}

func TestMoveNormalDayIsBoring(t *testing.T) {
	ev := nyEvent("plain", "2026-03-09T10:00", "2026-03-09T11:00", nil)
	res, err := applyMove(ev, map[overrideKey]Override{}, moveReq("plain", "2026-03-09", 0, 90), []Event{ev})
	if err != nil {
		t.Fatalf("applyMove: %v", err)
	}
	if res.RequestedWallMinutes != 90 || res.ActualWallMinutes != 90 || res.ElapsedMinutes != 90 {
		t.Errorf("deltas = %d/%d/%d, want 90/90/90 (no transition, no surprises)", res.RequestedWallMinutes, res.ActualWallMinutes, res.ElapsedMinutes)
	}
	if res.Note != "" {
		t.Errorf("note = %q, want empty", res.Note)
	}
	if res.Occurrence.StartWall != "2026-03-09T11:30" || res.Occurrence.EndWall != "2026-03-09T12:30" {
		t.Errorf("moved = %s → %s, want 11:30 → 12:30 (duration preserved)", res.Occurrence.StartWall, res.Occurrence.EndWall)
	}
}

func TestMoveIntoAmbiguousHourUsesFirst(t *testing.T) {
	// Drag from 00:30 EDT into the repeated hour: wall asks for 01:30, which
	// exists twice; the server takes the first (EDT).
	ev := nyEvent("night", "2026-11-01T00:30", "2026-11-01T01:00", nil)
	res, err := applyMove(ev, map[overrideKey]Override{}, moveReq("night", "2026-11-01", 0, 60), []Event{ev})
	if err != nil {
		t.Fatalf("applyMove: %v", err)
	}
	if res.Occurrence.StartWall != "2026-11-01T01:30" || res.Occurrence.ZoneAbbr != "EDT" {
		t.Errorf("resolved = %s %s, want 01:30 EDT (first occurrence)", res.Occurrence.StartWall, res.Occurrence.ZoneAbbr)
	}
	if res.ElapsedMinutes != 60 {
		t.Errorf("elapsed = %d, want 60 (04:30Z → 05:30Z)", res.ElapsedMinutes)
	}
	if !strings.Contains(res.Note, "occurs twice") {
		t.Errorf("note = %q, want the fold explanation", res.Note)
	}
}

func TestMoveAllDayByDaysOnly(t *testing.T) {
	ev := Event{ID: "offsite", Title: "Offsite", Start: "2026-03-12", End: "2026-03-14", AllDay: true, Zone: "America/New_York"}
	res, err := applyMove(ev, map[overrideKey]Override{}, moveReq("offsite", "2026-03-12", 1, 0), []Event{ev})
	if err != nil {
		t.Fatalf("applyMove: %v", err)
	}
	if res.Occurrence.StartWall != "2026-03-13" || res.Occurrence.EndWall != "2026-03-15" || res.Occurrence.Days != 2 {
		t.Errorf("all-day move = %s → %s (%dd), want 03-13 → 03-15 (2d)", res.Occurrence.StartWall, res.Occurrence.EndWall, res.Occurrence.Days)
	}
	// Minute deltas are meaningless for all-day events — refuse loudly.
	if _, err := applyMove(ev, map[overrideKey]Override{}, moveReq("offsite", "2026-03-12", 0, 30), []Event{ev}); err == nil {
		t.Errorf("all-day move with minuteDelta accepted, want loud refusal")
	}
}

func TestMoveSeriesOccurrenceIdentityIsStable(t *testing.T) {
	// Move Wednesday's standup; move it AGAIN. Both moves address the same
	// identity (original series date), so the override list stays at ONE
	// entry — never a chain of edits chasing a moving target.
	standup := nyEvent("standup", "2026-03-02T09:00", "2026-03-02T09:30",
		&RRule{Freq: "WEEKLY", ByDay: []string{"MO", "TU", "WE", "TH", "FR"}, Until: "2026-03-13"})
	events := []Event{standup}
	overrides := map[overrideKey]Override{}

	res1, err := applyMove(standup, overrides, moveReq("standup", "2026-03-11", 1, 0), events)
	if err != nil {
		t.Fatalf("first move: %v", err)
	}
	overrides[res1.Override.key()] = res1.Override

	res2, err := applyMove(standup, overrides, moveReq("standup", "2026-03-11", 0, 60), events)
	if err != nil {
		t.Fatalf("second move (same identity): %v", err)
	}
	if res2.Occurrence.ID != "standup/2026-03-11" {
		t.Errorf("identity drifted: %s", res2.Occurrence.ID)
	}
	if len(overrides) != 1 {
		t.Fatalf("override count = %d after two moves of one occurrence, want 1", len(overrides))
	}

	// Thursday stayed put.
	win := windowOf(t, events, "2026-03-09", "2026-03-13", overrides)
	thu := findOcc(win, "standup/2026-03-12")
	if thu == nil || !strings.HasSuffix(thu.StartWall, "T09:00") || thu.Exception {
		t.Errorf("Thursday standup rewritten: %+v", thu)
	}
}

func TestMoveUpdatesConflictsServerSide(t *testing.T) {
	// Moving an event INTO an occupied slot must light up the conflict from
	// the SERVER's answer — the frame never computes it.
	a := nyEvent("a", "2026-03-09T10:00", "2026-03-09T11:00", nil)
	b := nyEvent("b", "2026-03-09T13:00", "2026-03-09T14:00", nil)
	events := []Event{a, b}
	res, err := applyMove(a, map[overrideKey]Override{}, moveReq("a", "2026-03-09", 0, 180), events)
	if err != nil {
		t.Fatalf("applyMove: %v", err)
	}
	if len(res.Occurrence.ConflictIDs) != 1 || res.Occurrence.ConflictIDs[0] != "b/2026-03-09" {
		t.Errorf("moved occurrence conflicts = %v, want [b/2026-03-09]", res.Occurrence.ConflictIDs)
	}
}

func TestMoveUnknownOccurrenceFailsLoudly(t *testing.T) {
	ev := nyEvent("solo", "2026-03-09T10:00", "2026-03-09T11:00", nil)
	if _, err := applyMove(ev, map[overrideKey]Override{}, moveReq("solo", "2026-03-11", 0, 30), []Event{ev}); err == nil {
		t.Errorf("moving a nonexistent occurrence succeeded, want loud failure")
	}
}

// --- 8. source validation surfaces through expansion ---------------------------

func TestBadEventDefinitionsFailLoudly(t *testing.T) {
	cases := []Event{
		{ID: "", Title: "x", Start: "2026-03-09T10:00", End: "2026-03-09T11:00", Zone: "America/New_York"},
		{ID: "z", Title: "x", Start: "2026-03-09T10:00", End: "2026-03-09T11:00", Zone: "Not/A Zone!"},
		{ID: "z", Title: "x", Start: "2026-03-09 10:00", End: "2026-03-09T11:00", Zone: "America/New_York"},
		{ID: "z", Title: "x", Start: "2026-03-09T11:00", End: "2026-03-09T10:00", Zone: "America/New_York"},   // end before start
		{ID: "z", Title: "x", Start: "2026-03-09", End: "2026-03-09", AllDay: true, Zone: "America/New_York"}, // zero-length
		{ID: "z", Title: "x", Start: "2026-03-09T10:00", End: "2026-03-09T11:00", Zone: "America/New_York", RRule: &RRule{Freq: "YEARLY"}},
	}
	for i, ev := range cases {
		if err := ValidateEvent(ev); err == nil {
			t.Errorf("case %d: ValidateEvent accepted a bad definition (%+v)", i, ev)
		}
	}
	if _, err := buildOccurrences(cases, nil, mustDate(t, "2026-03-01"), mustDate(t, "2026-03-31")); err == nil {
		t.Errorf("buildOccurrences accepted bad definitions, want loud failure")
	}
}

func mustDate(t *testing.T, s string) wall {
	t.Helper()
	w, err := parseWallDate(s)
	if err != nil {
		t.Fatalf("date %q: %v", s, err)
	}
	return w
}
