package calendar

import (
	"testing"
	"time"
)

// zones_test.go pins the wall-clock resolution policy: exact, spring-forward
// gap, fall-back fold — plus the exotic zones that break assumptions built
// only on US hour steps (Lord Howe's 30-minute shift, Kolkata's half-hour
// offset). These tables are the load-bearing tests of the plugin's "Go owns
// timezones" claim.

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := loadZone(name)
	if err != nil {
		t.Fatalf("loadZone(%q): %v", name, err)
	}
	return loc
}

func TestResolveWallExact(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	cases := []struct {
		wall string
		zone string
		want string // RFC3339 UTC instant
	}{
		{"2026-03-09T09:00", "America/New_York", "2026-03-09T13:00:00Z"}, // EDT (UTC−4)
		{"2026-03-06T09:00", "America/New_York", "2026-03-06T14:00:00Z"}, // EST (UTC−5)
		{"2026-03-08T01:30", "America/New_York", "2026-03-08T06:30:00Z"}, // 30 min before the gap, still EST
		{"2026-11-01T02:30", "America/New_York", "2026-11-01T07:30:00Z"}, // after the fold, EST
		{"2026-03-08T09:00", "Asia/Kolkata", "2026-03-08T03:30:00Z"},     // fixed +05:30
		{"2026-06-15T12:00", "UTC", "2026-06-15T12:00:00Z"},
	}
	for _, c := range cases {
		loc := mustZone(t, c.zone)
		w, err := parseWall(c.wall)
		if err != nil {
			t.Fatalf("parseWall(%q): %v", c.wall, err)
		}
		got, res := resolveWall(w, loc)
		if got.Format(time.RFC3339) != c.want {
			t.Errorf("resolveWall(%q, %s) = %s, want %s", c.wall, c.zone, got.Format(time.RFC3339), c.want)
		}
		if res != resExact {
			t.Errorf("resolveWall(%q, %s): resolution %q, want exact", c.wall, c.zone, res)
		}
	}
	_ = ny
}

// The spring-forward gap: 02:30 on 2026-03-08 in America/New_York never
// happens (clocks jump 02:00 EST → 03:00 EDT). Policy: carry the
// pre-transition offset (EST, −5) → 07:30Z, which DISPLAYS as 03:30 EDT —
// the nonexistent time lands after the gap, not before it.
func TestResolveWallSpringForwardGap(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	w, _ := parseWall("2026-03-08T02:30")
	got, res := resolveWall(w, ny)
	if res != resGap {
		t.Fatalf("resolution = %q, want gap", res)
	}
	if want := "2026-03-08T07:30:00Z"; got.Format(time.RFC3339) != want {
		t.Fatalf("gap instant = %s, want %s (carried by EST)", got.Format(time.RFC3339), want)
	}
	// And it displays AFTER the gap:
	if disp := got.In(ny).Format(wallLayout); disp != "2026-03-08T03:30" {
		t.Fatalf("gap display = %s, want 2026-03-08T03:30 (EDT)", disp)
	}
}

// The fall-back fold: 01:30 on 2026-11-01 occurs twice (01:30 EDT, then
// 01:30 EST). Policy: the FIRST occurrence.
func TestResolveWallFallBackFold(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	w, _ := parseWall("2026-11-01T01:30")
	got, res := resolveWall(w, ny)
	if res != resAmbiguous {
		t.Fatalf("resolution = %q, want ambiguous", res)
	}
	if want := "2026-11-01T05:30:00Z"; got.Format(time.RFC3339) != want {
		t.Fatalf("fold instant = %s, want %s (first occurrence, EDT)", got.Format(time.RFC3339), want)
	}
	if abbr, _ := zoneAt(got, ny); abbr != "EDT" {
		t.Fatalf("fold abbr = %s, want EDT", abbr)
	}
}

// Lord Howe: a 30-minute DST step (+10:30 ↔ +11:00). Any implementation
// that assumes hour-granularity transitions breaks here.
func TestResolveWallLordHoweHalfHourTransition(t *testing.T) {
	lh := mustZone(t, "Australia/Lord_Howe")
	// Spring forward: 2026-10-04 02:00 +10:30 → 03:00 +11:00; 02:15 is in the gap.
	w, _ := parseWall("2026-10-04T02:15")
	got, res := resolveWall(w, lh)
	if res != resGap {
		t.Fatalf("gap resolution = %q, want gap", res)
	}
	if want := "2026-10-03T15:45:00Z"; got.Format(time.RFC3339) != want {
		t.Fatalf("Lord Howe gap instant = %s, want %s (carried by +10:30)", got.Format(time.RFC3339), want)
	}
	// Fall back: 2026-04-05 02:00 +11:00 → 01:00 +10:30; 01:45 is ambiguous.
	w2, _ := parseWall("2026-04-05T01:45")
	got2, res2 := resolveWall(w2, lh)
	if res2 != resAmbiguous {
		t.Fatalf("fold resolution = %q, want ambiguous", res2)
	}
	if want := "2026-04-04T14:45:00Z"; got2.Format(time.RFC3339) != want {
		t.Fatalf("Lord Howe fold instant = %s, want %s (first occurrence, +11:00)", got2.Format(time.RFC3339), want)
	}
}

func TestLoadZoneRejectsNonIANANames(t *testing.T) {
	for _, bad := range []string{"", "Local", "/etc/localtime", "../America/New_York", "UTC+2", "America//New_York"} {
		if _, err := loadZone(bad); err == nil {
			t.Errorf("loadZone(%q) accepted a non-IANA name", bad)
		}
	}
	for _, ok := range []string{"UTC", "America/New_York", "Australia/Lord_Howe", "Asia/Kolkata"} {
		if _, err := loadZone(ok); err != nil {
			t.Errorf("loadZone(%q): %v", ok, err)
		}
	}
}

func TestTransitionsInRangeNewYork(t *testing.T) {
	ny := mustZone(t, "America/New_York")

	from, _ := parseWallDate("2026-03-01")
	to, _ := parseWallDate("2026-03-31")
	trs := transitionsInRange(ny, from, to)
	if len(trs) != 1 {
		t.Fatalf("March 2026 NY: %d transitions, want 1 (%v)", len(trs), trs)
	}
	tr := trs[0]
	if tr.Date != "2026-03-08" || tr.Kind != "forward" || tr.DeltaMinutes != 60 {
		t.Fatalf("March transition = %+v, want 2026-03-08 forward +60", tr)
	}
	if tr.InstantUTC != "2026-03-08T07:00:00Z" {
		t.Fatalf("transition instant = %s, want 2026-03-08T07:00:00Z (02:00 EST)", tr.InstantUTC)
	}
	if tr.WallFrom != "02:00" || tr.WallTo != "03:00" {
		t.Fatalf("transition walls = %s→%s, want 02:00→03:00", tr.WallFrom, tr.WallTo)
	}

	from, _ = parseWallDate("2026-10-25")
	to, _ = parseWallDate("2026-11-08")
	trs = transitionsInRange(ny, from, to)
	if len(trs) != 1 {
		t.Fatalf("late-2026 NY: %d transitions, want 1 (%v)", len(trs), trs)
	}
	tr = trs[0]
	if tr.Date != "2026-11-01" || tr.Kind != "back" || tr.DeltaMinutes != -60 {
		t.Fatalf("November transition = %+v, want 2026-11-01 back −60", tr)
	}
	if tr.InstantUTC != "2026-11-01T06:00:00Z" {
		t.Fatalf("transition instant = %s, want 2026-11-01T06:00:00Z (02:00 EDT)", tr.InstantUTC)
	}
}

func TestTransitionsInRangeLordHoweThirtyMinutes(t *testing.T) {
	lh := mustZone(t, "Australia/Lord_Howe")
	from, _ := parseWallDate("2026-10-01")
	to, _ := parseWallDate("2026-10-31")
	trs := transitionsInRange(lh, from, to)
	if len(trs) != 1 {
		t.Fatalf("October 2026 Lord Howe: %d transitions, want 1 (%v)", len(trs), trs)
	}
	if trs[0].DeltaMinutes != 30 || trs[0].Kind != "forward" {
		t.Fatalf("Lord Howe transition = %+v, want forward +30", trs[0])
	}
}
