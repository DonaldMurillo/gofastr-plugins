package calendar

import (
	"strings"
	"testing"
	"time"
)

// rrule_test.go pins the recurrence subset: expansion is WALL-CLOCK
// arithmetic (the property that makes a daily 09:00 survive DST), and the
// subset's edges behave exactly as documented — COUNT counts emitted
// instances, UNTIL is inclusive, MONTHLY ordinals select nth weekdays, and
// months without DTSTART's day skip it.

// collect expands one rule over a horizon and returns the occurrence wall
// starts as "YYYY-MM-DDTHH:MM" strings.
func collect(t *testing.T, ev Event, horizon string) []string {
	t.Helper()
	h, err := parseWallDate(horizon)
	if err != nil {
		t.Fatalf("horizon: %v", err)
	}
	var out []string
	if _, err := enumSeries(ev, h, func(w wall) bool {
		out = append(out, w.String())
		return true
	}); err != nil {
		t.Fatalf("enumSeries: %v", err)
	}
	return out
}

func timed(id, start, end string, r *RRule) Event {
	return Event{ID: id, Title: id, Start: start, End: end, Zone: "America/New_York", RRule: r}
}

func join(ss []string) string { return strings.Join(ss, " ") }

func TestDailyExpansionWithCount(t *testing.T) {
	got := collect(t,
		timed("d", "2026-03-05T10:00", "2026-03-05T10:30", &RRule{Freq: "DAILY", Count: 5}),
		"2026-12-31")
	want := "2026-03-05T10:00 2026-03-06T10:00 2026-03-07T10:00 2026-03-08T10:00 2026-03-09T10:00"
	if join(got) != want {
		t.Fatalf("DAILY count=5 = %s, want %s", join(got), want)
	}
}

func TestDailyInterval(t *testing.T) {
	got := collect(t,
		timed("d", "2026-03-02T09:00", "2026-03-02T09:15", &RRule{Freq: "DAILY", Interval: 10, Count: 3}),
		"2026-12-31")
	want := "2026-03-02T09:00 2026-03-12T09:00 2026-03-22T09:00"
	if join(got) != want {
		t.Fatalf("DAILY interval=10 = %s, want %s", join(got), want)
	}
}

// BYDAY on DAILY is a filter, not a generator: only listed weekdays pass.
func TestDailyWithByDayFilter(t *testing.T) {
	got := collect(t,
		timed("d", "2026-03-02T09:00", "2026-03-02T09:15", &RRule{Freq: "DAILY", ByDay: []string{"MO", "WE", "FR"}, Count: 6}),
		"2026-12-31")
	want := "2026-03-02T09:00 2026-03-04T09:00 2026-03-06T09:00 2026-03-09T09:00 2026-03-11T09:00 2026-03-13T09:00"
	if join(got) != want {
		t.Fatalf("DAILY byday MO,WE,FR = %s, want %s", join(got), want)
	}
}

func TestWeeklyByDay(t *testing.T) {
	got := collect(t,
		timed("w", "2026-03-02T09:00", "2026-03-02T09:30", &RRule{Freq: "WEEKLY", ByDay: []string{"MO", "WE", "FR"}}),
		"2026-03-15")
	want := "2026-03-02T09:00 2026-03-04T09:00 2026-03-06T09:00 2026-03-09T09:00 2026-03-11T09:00 2026-03-13T09:00"
	if join(got) != want {
		t.Fatalf("WEEKLY MO,WE,FR = %s, want %s", join(got), want)
	}
}

// Without BYDAY, WEEKLY expands only DTSTART's weekday (RFC semantics).
func TestWeeklyWithoutByDayKeepsStartWeekday(t *testing.T) {
	got := collect(t,
		timed("w", "2026-03-04T09:00", "2026-03-04T09:30", &RRule{Freq: "WEEKLY"}),
		"2026-03-16")
	want := "2026-03-04T09:00 2026-03-11T09:00" // Wednesdays
	if join(got) != want {
		t.Fatalf("WEEKLY (no byday) = %s, want %s", join(got), want)
	}
}

func TestWeeklyIntervalTwo(t *testing.T) {
	got := collect(t,
		timed("w", "2026-03-02T09:00", "2026-03-02T09:30", &RRule{Freq: "WEEKLY", Interval: 2, ByDay: []string{"MO"}}),
		"2026-04-01")
	want := "2026-03-02T09:00 2026-03-16T09:00 2026-03-30T09:00"
	if join(got) != want {
		t.Fatalf("WEEKLY interval=2 = %s, want %s", join(got), want)
	}
}

func TestMonthlyByDayOfMonthSkipsShortMonths(t *testing.T) {
	got := collect(t,
		timed("m", "2026-01-31T10:00", "2026-01-31T11:00", &RRule{Freq: "MONTHLY"}),
		"2026-04-30")
	want := "2026-01-31T10:00 2026-03-31T10:00" // Feb and Apr have no 31st
	if join(got) != want {
		t.Fatalf("MONTHLY day-31 = %s, want %s", join(got), want)
	}
}

func TestMonthlyNthWeekday(t *testing.T) {
	got := collect(t,
		timed("m", "2026-01-13T10:00", "2026-01-13T11:00", &RRule{Freq: "MONTHLY", ByDay: []string{"2TU"}}),
		"2026-04-30")
	want := "2026-01-13T10:00 2026-02-10T10:00 2026-03-10T10:00 2026-04-14T10:00"
	if join(got) != want {
		t.Fatalf("MONTHLY 2TU = %s, want %s", join(got), want)
	}
}

func TestMonthlyLastWeekday(t *testing.T) {
	got := collect(t,
		timed("m", "2026-01-30T17:00", "2026-01-30T18:00", &RRule{Freq: "MONTHLY", ByDay: []string{"-1FR"}}),
		"2026-04-30")
	want := "2026-01-30T17:00 2026-02-27T17:00 2026-03-27T17:00 2026-04-24T17:00"
	if join(got) != want {
		t.Fatalf("MONTHLY -1FR = %s, want %s", join(got), want)
	}
}

// Plain BYDAY in MONTHLY means EVERY such weekday of the month.
func TestMonthlyEveryWeekday(t *testing.T) {
	got := collect(t,
		timed("m", "2026-02-03T08:00", "2026-02-03T08:30", &RRule{Freq: "MONTHLY", ByDay: []string{"TU"}}),
		"2026-02-28")
	want := "2026-02-03T08:00 2026-02-10T08:00 2026-02-17T08:00 2026-02-24T08:00"
	if join(got) != want {
		t.Fatalf("MONTHLY TU = %s, want %s", join(got), want)
	}
}

func TestUntilInclusive(t *testing.T) {
	got := collect(t,
		timed("w", "2026-03-02T09:00", "2026-03-02T09:30", &RRule{Freq: "WEEKLY", ByDay: []string{"MO"}, Until: "2026-03-09"}),
		"2026-12-31")
	want := "2026-03-02T09:00 2026-03-09T09:00" // UNTIL is inclusive
	if join(got) != want {
		t.Fatalf("WEEKLY until 03-09 = %s, want %s", join(got), want)
	}
}

func TestExpansionStartsAtDTSTART(t *testing.T) {
	// DTSTART on a BYDAY-listed weekday is the FIRST instance, and COUNT
	// includes it.
	got := collect(t,
		timed("w", "2026-03-04T09:00", "2026-03-04T09:30", &RRule{Freq: "WEEKLY", ByDay: []string{"MO", "WE"}, Count: 3}),
		"2026-12-31")
	want := "2026-03-04T09:00 2026-03-09T09:00 2026-03-11T09:00"
	if join(got) != want {
		t.Fatalf("COUNT includes DTSTART = %s, want %s", join(got), want)
	}
}

// --- the loud-rejection half of the subset -----------------------------------

func TestValidateRRuleRejectsUnsupportedShapes(t *testing.T) {
	start, _ := parseWall("2026-03-02T09:00")
	cases := []struct {
		name string
		r    RRule
		want string
	}{
		{"yearly", RRule{Freq: "YEARLY"}, "not in the supported set"},
		{"hourly", RRule{Freq: "HOURLY"}, "not in the supported set"},
		{"empty freq", RRule{}, "not in the supported set"},
		{"count and until", RRule{Freq: "DAILY", Count: 3, Until: "2026-04-01"}, "both COUNT"},
		{"negative interval", RRule{Freq: "DAILY", Interval: -1}, "negative"},
		{"negative count", RRule{Freq: "DAILY", Count: -2}, "negative"},
		{"huge count", RRule{Freq: "DAILY", Count: maxCount + 1}, "exceeds cap"},
		{"bad until", RRule{Freq: "DAILY", Until: "04/01/2026"}, "until"},
		{"until before start", RRule{Freq: "DAILY", Until: "2026-03-01"}, "before the event start"},
		{"bad byday code", RRule{Freq: "WEEKLY", ByDay: []string{"XX"}}, "not a weekday code"},
		{"bad byday ordinal", RRule{Freq: "MONTHLY", ByDay: []string{"9TU"}}, "out of range"},
		{"zero ordinal", RRule{Freq: "MONTHLY", ByDay: []string{"0TU"}}, "not a weekday code or an nth-weekday"},
		{"ordinal outside monthly", RRule{Freq: "WEEKLY", ByDay: []string{"2TU"}}, "only valid with MONTHLY"},
		{"too many byday", RRule{Freq: "WEEKLY", ByDay: []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU", "MO"}}, "max"},
	}
	for _, c := range cases {
		err := validateRRule(c.r, start)
		if err == nil {
			t.Errorf("%s: validateRRule accepted %v (want loud rejection)", c.name, c.r)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err.Error(), c.want)
		}
	}
}

func TestValidateRRuleAcceptsTheSubset(t *testing.T) {
	start, _ := parseWall("2026-03-02T09:00")
	for _, r := range []RRule{
		{Freq: "DAILY"},
		{Freq: "daily"}, // case-insensitive freq
		{Freq: "DAILY", Interval: 3},
		{Freq: "DAILY", Count: 10},
		{Freq: "WEEKLY", ByDay: []string{"MO", "FR"}},
		{Freq: "MONTHLY", ByDay: []string{"2TU", "-1FR"}},
		{Freq: "MONTHLY", Until: "2027-01-01"},
	} {
		if err := validateRRule(r, start); err != nil {
			t.Errorf("validateRRule(%v) rejected a documented shape: %v", r, err)
		}
	}
}

func TestWeekdayCodeRoundTrip(t *testing.T) {
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		code := weekdayCode[wd]
		got, ok := weekdayFromCode(code)
		if !ok || got != wd {
			t.Fatalf("weekday code %s round-trip failed: %v %v", code, got, ok)
		}
	}
}

func TestDaysInMonth(t *testing.T) {
	cases := []struct {
		y    int
		m    time.Month
		want int
	}{
		{2026, time.February, 28},
		{2024, time.February, 29},
		{2026, time.April, 30},
		{2026, time.January, 31},
	}
	for _, c := range cases {
		if got := daysInMonth(c.y, c.m); got != c.want {
			t.Errorf("daysInMonth(%d, %s) = %d, want %d", c.y, c.m, got, c.want)
		}
	}
}
