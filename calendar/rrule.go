package calendar

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rrule.go implements the recurrence subset this plugin ships: FREQ DAILY /
// WEEKLY / MONTHLY, INTERVAL, COUNT or UNTIL (exactly one), BYDAY (plain
// codes everywhere; ordinals like 2TU / -1FR in MONTHLY only).
//
// Two rules govern the whole file:
//
//   - Expansion is WALL-CLOCK arithmetic. A daily 09:00 stays 09:00 across a
//     DST change; the instants shift by 23h/25h between straddling days
//     because zones.go re-resolves each occurrence's wall time separately.
//     That is the property users expect from recurring meetings, and it is
//     the one table-tested in rrule_test.go.
//   - Anything outside the subset is rejected with an error naming the
//     offender — never approximated. A YEARLY rule quietly re-expanded as
//     MONTHLY, or a BYSETPOS silently dropped, is precisely the
//     "silently mis-expanding" failure the plugin refuses to ship.
const (
	// maxSeriesSteps bounds one series enumeration (COUNT-bounded rules hit
	// their count; open-ended rules stop at the caller's horizon). The cap
	// exists so a hostile or buggy event definition cannot spin the CPU.
	maxSeriesSteps = 250_000
	// maxCount bounds COUNT itself for the same reason.
	maxCount = 100_000
)

// byDayRule is one parsed BYDAY element: a weekday plus an ordinal that is
// only meaningful (and only allowed) for MONTHLY. Ordinal 0 = plain code.
type byDayRule struct {
	weekday time.Weekday
	ordinal int // 0 = any occurrence of the weekday; 1..5 = nth; -1 = last
}

// parseByDay parses one BYDAY element ("MO", "2TU", "-1FR").
func parseByDay(s string) (byDayRule, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return byDayRule{}, fmt.Errorf("empty BYDAY element")
	}
	ordinal := 0
	code := s
	if len(s) > 2 {
		n, err := strconv.Atoi(s[:len(s)-2])
		if err != nil || n == 0 {
			return byDayRule{}, fmt.Errorf("BYDAY %q is not a weekday code or an nth-weekday like 2TU/-1FR", s)
		}
		if n > 5 || n < -1 {
			return byDayRule{}, fmt.Errorf("BYDAY %q ordinal out of range (want -1..5)", s)
		}
		ordinal = n
		code = s[len(s)-2:]
	}
	wd, ok := weekdayFromCode(code)
	if !ok {
		return byDayRule{}, fmt.Errorf("BYDAY %q is not a weekday code (MO..SU)", s)
	}
	return byDayRule{weekday: wd, ordinal: ordinal}, nil
}

// validateRRule enforces the subset. `start` is the event's DTSTART wall
// time — UNTIL is checked against it, because a rule that excludes its own
// start would expand to nothing, which is always a definition bug.
func validateRRule(r RRule, start wall) error {
	freq := strings.ToUpper(r.Freq)
	switch freq {
	case "DAILY", "WEEKLY", "MONTHLY":
	default:
		return fmt.Errorf("rrule freq %q is not in the supported set DAILY|WEEKLY|MONTHLY "+
			"(everything else is rejected, not approximated)", r.Freq)
	}
	if r.Interval < 0 {
		return fmt.Errorf("rrule interval %d is negative", r.Interval)
	}
	if r.Count < 0 {
		return fmt.Errorf("rrule count %d is negative", r.Count)
	}
	if r.Count > maxCount {
		return fmt.Errorf("rrule count %d exceeds cap %d", r.Count, maxCount)
	}
	if r.Count > 0 && r.Until != "" {
		return fmt.Errorf("rrule carries both COUNT=%d and UNTIL=%q — RFC 5545 forbids the pair; pick one", r.Count, r.Until)
	}
	if r.Until != "" {
		u, err := parseWallDate(r.Until)
		if err != nil {
			return fmt.Errorf("rrule until: %w", err)
		}
		if u.before(start.atMidnight()) {
			return fmt.Errorf("rrule until %s is before the event start %s", r.Until, start.dateStr())
		}
	}
	if len(r.ByDay) > maxByDayRules {
		return fmt.Errorf("rrule has %d BYDAY rules (max %d)", len(r.ByDay), maxByDayRules)
	}
	for _, s := range r.ByDay {
		rule, err := parseByDay(s)
		if err != nil {
			return err
		}
		if rule.ordinal != 0 && freq != "MONTHLY" {
			return fmt.Errorf("BYDAY %q carries an ordinal, which is only valid with MONTHLY", s)
		}
	}
	return nil
}

// enumSeries enumerates the event's occurrence WALL start times in order
// from DTSTART, applying FREQ/INTERVAL/BYDAY/COUNT/UNTIL, and calls visit
// for each. visit returns false to stop early. Returns the number of
// instances emitted.
//
// `horizon` bounds enumeration: it stops once a candidate's ORIGINAL date
// passes it. Callers asking for a window pass to+maxMoveDays so an instance
// moved out of the window by an override is still generated — filtering on
// the EFFECTIVE date happens afterwards, in occurrence.go.
func enumSeries(ev Event, horizon wall, visit func(wall) bool) (int, error) {
	start, err := parseWallAuto(ev.Start)
	if err != nil {
		return 0, err
	}
	rule := *ev.RRule
	freq := strings.ToUpper(rule.Freq)
	interval := rule.Interval
	if interval == 0 {
		interval = 1
	}
	var until wall
	haveUntil := false
	if rule.Until != "" {
		u, _ := parseWallDate(rule.Until)
		until = u.endOfDay()
		haveUntil = true
	}
	count := rule.Count
	emitted := 0
	stopped := false

	// emit applies the shared bounds and hands the candidate to visit.
	// Returns false when enumeration should stop entirely.
	emit := func(candidate wall) bool {
		if count > 0 && emitted >= count {
			return false
		}
		if haveUntil && candidate.after(until) {
			return false
		}
		emitted++
		return visit(candidate)
	}

	switch freq {
	case "DAILY":
		allowed := map[time.Weekday]bool{}
		for _, s := range rule.ByDay {
			b, err := parseByDay(s)
			if err != nil {
				return 0, err
			}
			allowed[b.weekday] = true
		}
		for cursor := start; !stopped; cursor = cursor.addDays(interval) {
			if cursor.after(horizon) {
				break
			}
			if len(allowed) > 0 && !allowed[cursor.weekday()] {
				continue
			}
			if !emit(cursor) {
				stopped = true
			}
		}

	case "WEEKLY":
		// Weeks start Monday (RFC 5545 WKST default). Without BYDAY a WEEKLY
		// rule expands only DTSTART's weekday; with BYDAY exactly the listed
		// weekdays — in both cases from DTSTART onward.
		allowed := map[time.Weekday]bool{}
		var order []time.Weekday
		for _, s := range rule.ByDay {
			b, err := parseByDay(s)
			if err != nil {
				return 0, err
			}
			if !allowed[b.weekday] {
				allowed[b.weekday] = true
				order = append(order, b.weekday)
			}
		}
		if len(order) == 0 {
			order = append(order, start.weekday())
		}
		// `order` decides membership; `allowed` is the membership set. With
		// no BYDAY the set is DTSTART's weekday alone (RFC semantics).
		for _, wd := range order {
			allowed[wd] = true
		}
		weekStart := start.addDays(-((int(start.weekday()) + 6) % 7)) // Monday of DTSTART's week
		for ; !stopped; weekStart = weekStart.addDays(7 * interval) {
			for i := 0; i < 7 && !stopped; i++ {
				candidate := weekStart.addDays(i)
				if candidate.before(start) {
					continue
				}
				if candidate.after(horizon) {
					stopped = true
					break
				}
				if !allowed[candidate.weekday()] {
					continue
				}
				if !emit(candidate) {
					stopped = true
				}
			}
			if weekStart.after(horizon) {
				break
			}
		}

	case "MONTHLY":
		// Without BYDAY: DTSTART's day-of-month every interval-th month;
		// months lacking that day skip it (Jan 31 has no Feb 31). With
		// BYDAY: every listed nth-weekday of the month, chronologically.
		var rules []byDayRule
		for _, s := range rule.ByDay {
			b, err := parseByDay(s)
			if err != nil {
				return 0, err
			}
			rules = append(rules, b)
		}
		// The cursor is a (year, month) pair, NOT a wall: AddDate-style
		// month stepping normalizes Jan 31 → Mar 3, which would silently
		// corrupt the day-of-month this branch exists to preserve.
		year, month := start.year, start.month
		for !stopped {
			firstOfMonth := wall{year: year, month: month, day: 1, hour: start.hour, min: start.min}
			if firstOfMonth.after(horizon) {
				break
			}
			var candidates []wall
			if len(rules) == 0 {
				if start.day <= daysInMonth(year, month) {
					candidates = append(candidates, wall{
						year: year, month: month, day: start.day,
						hour: start.hour, min: start.min,
					})
				}
			} else {
				for _, b := range rules {
					candidates = append(candidates, nthWeekdayOfMonth(year, month, b, start.hour, start.min)...)
				}
				sort.Slice(candidates, func(i, j int) bool { return candidates[i].before(candidates[j]) })
			}
			for _, candidate := range candidates {
				if candidate.before(start) {
					continue // before DTSTART: not part of the series
				}
				if candidate.after(horizon) {
					stopped = true
					break
				}
				if !emit(candidate) {
					stopped = true
					break
				}
			}
			year, month = stepMonth(year, month, interval)
		}
	}
	return emitted, nil
}

// stepMonth advances a (year, month) cursor by n months.
func stepMonth(year int, month time.Month, n int) (int, time.Month) {
	m := int(month) - 1 + n
	return year + m/12, time.Month(m%12 + 1)
}

// daysInMonth is the calendar month length.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// nthWeekdayOfMonth resolves one BYDAY rule inside one month. Ordinal 0
// yields EVERY matching weekday (RFC: plain BYDAY in MONTHLY means "each
// such weekday of the month"); 1..5 the nth; -1 the last.
func nthWeekdayOfMonth(year int, month time.Month, b byDayRule, hour, min int) []wall {
	dim := daysInMonth(year, month)
	var matching []wall
	for d := 1; d <= dim; d++ {
		if time.Date(year, month, d, 0, 0, 0, 0, time.UTC).Weekday() == b.weekday {
			matching = append(matching, wall{year: year, month: month, day: d, hour: hour, min: min})
		}
	}
	switch {
	case b.ordinal == 0:
		return matching
	case b.ordinal > 0:
		if b.ordinal <= len(matching) {
			return matching[b.ordinal-1 : b.ordinal]
		}
		return nil // e.g. the 5th Tuesday of a month with four
	default:
		if len(matching) > 0 {
			return matching[len(matching)-1:]
		}
		return nil
	}
}
