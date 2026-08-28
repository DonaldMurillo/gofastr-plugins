package calendar

import (
	"fmt"
	"strings"
	"time"
)

// model.go holds the calendar's data model and the WALL-CLOCK arithmetic it is
// built on. Two representation decisions carry the whole plugin, and both are
// deliberate:
//
//   - Events are defined as NAIVE WALL-CLOCK strings ("2026-03-08T01:30") plus
//     an IANA zone name. There is no time.Time in the persisted model, because
//     a time.Time IS an instant and an event definition is not one — "daily
//     09:00 in New York" survives a DST change precisely because it is wall
//     anchored, and the instants are (re)derived at expansion time by zones.go.
//   - The wall type below does its date math through time.UTC and reads the
//     calendar fields back out, so adding a day across a month boundary is
//     real calendar arithmetic with zero zone awareness. Wall math must never
//     consult a zone: that is the entire bug class this plugin exists to not
//     have in the frame.

// wall is a naive wall-clock datetime: no zone, no offset, just fields.
// Zero minute precision (events are minute-granular by design).
type wall struct {
	year  int
	month time.Month
	day   int
	hour  int
	min   int
}

// wallLayout / wallDateLayout are the ONLY accepted wire forms. Timed events:
// "2006-01-02T15:04". All-day events: "2006-01-02" (all-day End is an
// EXCLUSIVE date, RFC 5545 DATE semantics — a two-day event is 03-12 → 03-14).
const (
	wallLayout     = "2006-01-02T15:04"
	wallDateLayout = "2006-01-02"
)

func parseWall(s string) (wall, error) {
	t, err := time.ParseInLocation(wallLayout, s, time.UTC)
	if err != nil {
		return wall{}, fmt.Errorf("not a wall datetime (%q, want %q)", s, wallLayout)
	}
	return wallFromTime(t), nil
}

func parseWallDate(s string) (wall, error) {
	t, err := time.ParseInLocation(wallDateLayout, s, time.UTC)
	if err != nil {
		return wall{}, fmt.Errorf("not a wall date (%q, want %q)", s, wallDateLayout)
	}
	return wallFromTime(t), nil
}

// parseWallAuto accepts either form. All-day events carry date-only strings;
// timed events carry datetimes. Used where the two shapes meet (overrides).
func parseWallAuto(s string) (wall, error) {
	if len(s) == len(wallDateLayout) {
		return parseWallDate(s)
	}
	return parseWall(s)
}

func wallFromTime(t time.Time) wall {
	return wall{year: t.Year(), month: t.Month(), day: t.Day(), hour: t.Hour(), min: t.Minute()}
}

// t renders the wall clock as a UTC instant whose fields are exactly w's —
// the "nominal" instant in resolveWall's probe math, and the vehicle for all
// calendar arithmetic (UTC, so no zone ever interferes with field math).
func (w wall) t() time.Time {
	return time.Date(w.year, w.month, w.day, w.hour, w.min, 0, 0, time.UTC)
}

func (w wall) String() string        { return w.t().Format(wallLayout) }
func (w wall) dateStr() string       { return w.t().Format(wallDateLayout) }
func (w wall) weekday() time.Weekday { return w.t().Weekday() }

func (w wall) equal(o wall) bool { return w == o }
func (w wall) before(o wall) bool {
	return w.year < o.year || w.year == o.year && (int(w.month) < int(o.month) ||
		int(w.month) == int(o.month) && (w.day < o.day || w.day == o.day && (w.hour < o.hour ||
			w.hour == o.hour && w.min < o.min)))
}

func (w wall) after(o wall) bool { return o.before(w) }

func (w wall) addDays(n int) wall     { return wallFromTime(w.t().AddDate(0, 0, n)) }
func (w wall) addMonths(n int) wall   { return wallFromTime(w.t().AddDate(0, n, 0)) }
func (w wall) addMinutes(n int) wall  { return wallFromTime(w.t().Add(time.Duration(n) * time.Minute)) }
func (w wall) minutesFrom(o wall) int { return int(w.t().Sub(o.t()).Minutes()) }
func (w wall) daysFrom(o wall) int    { return int(w.t().Sub(o.t()).Hours() / 24) }
func (w wall) minuteOfDay() int       { return w.hour*60 + w.min }
func (w wall) dayOfMonth() int        { return w.day }
func (w wall) isSameDate(o wall) bool { return w.dateStr() == o.dateStr() }
func (w wall) atMidnight() wall       { return wall{year: w.year, month: w.month, day: w.day} }
func (w wall) endOfDay() wall         { return w.addDays(1).atMidnight() }

// Doc is the canonical calendar-v1 document. It is VIEW STATE ONLY — the
// current date and mode. Events are HOST data (the events source owns them,
// like datagrid's rows), overrides live in the plugin's store, and the frame
// never receives an RRULE at all: it renders occurrences the server already
// resolved. That is the structural version of "the frame never computes a
// recurrence" — it cannot; it is never told the rule.
type Doc struct {
	SchemaVersion string `json:"schemaVersion"`
	View          View   `json:"view"`
}

// View is the live view state.
type View struct {
	Date string `json:"date"` // focused date, "2006-01-02"
	Mode string `json:"mode"` // "month" | "week" | "day"
}

// Event is one calendar entry: either a single occurrence or a series when
// RRule is set. Times are naive wall-clock strings in Zone. All-day events
// use date-only strings, with End EXCLUSIVE.
type Event struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Start  string `json:"start"`
	End    string `json:"end"`
	AllDay bool   `json:"allDay"`
	Zone   string `json:"zone"`
	RRule  *RRule `json:"rrule,omitempty"`
}

// RRule is the deliberate recurrence subset (docs/calendar.md states it in
// full): FREQ DAILY | WEEKLY | MONTHLY, INTERVAL, COUNT or UNTIL (exactly one
// — RFC 5545 declares both invalid, and silently picking one would be the
// "silently mis-expanding" failure this plugin refuses), and BYDAY. BYDAY is
// plain two-letter codes for WEEKLY/DAILY; MONTHLY additionally allows
// ordinals (1..5, -1 = last). Everything else — YEARLY, BYMONTH, BYSETPOS,
// WKST, sub-daily frequencies — is rejected loudly with E_RRULE_UNSUPPORTED
// rather than approximated.
type RRule struct {
	Freq     string   `json:"freq"`
	Interval int      `json:"interval,omitempty"`
	Count    int      `json:"count,omitempty"`
	Until    string   `json:"until,omitempty"` // inclusive wall date "2006-01-02"
	ByDay    []string `json:"byDay,omitempty"`
}

// Override is one instance edit: the occurrence of EventID whose ORIGINAL
// series date is Date, moved to new wall times. Keyed by the original date so
// the identity is stable across moves — moving an occurrence twice updates
// the same override instead of stacking, and the series itself is never
// touched. This is RECURRENCE-ID semantics shrunk to the subset.
type Override struct {
	EventID string `json:"eventId"`
	Date    string `json:"date"` // original series wall date
	Start   string `json:"start,omitempty"`
	End     string `json:"end,omitempty"`
}

// overrideKey is the map key for Override (EventID + original date).
type overrideKey struct {
	eventID string
	date    string
}

func (o Override) key() overrideKey { return overrideKey{o.EventID, o.Date} }

// Occurrence is one resolved instance as it crosses the bridge. Every time
// value is explicit: instants as RFC3339 UTC, wall clock as naive strings in
// the event's zone, plus the zone's abbreviation and offset at the start.
// The frame renders from these without ever consulting a timezone database —
// it cannot guess wrong because it is never asked to guess.
type Occurrence struct {
	ID        string `json:"id"` // "<eventID>/<original series date>"
	EventID   string `json:"eventId"`
	Title     string `json:"title"`
	AllDay    bool   `json:"allDay"`
	StartUTC  string `json:"startUtc"` // RFC3339, UTC
	EndUTC    string `json:"endUtc"`
	StartWall string `json:"startWall"` // timed: "2006-01-02T15:04"; all-day: "2006-01-02"
	EndWall   string `json:"endWall"`   // timed: inclusive end; all-day: EXCLUSIVE date
	Zone      string `json:"zone"`
	ZoneAbbr  string `json:"zoneAbbr"` // EST / EDT / +10:30 …
	OffsetMin int    `json:"startOffsetMinutes"`
	Recurring bool   `json:"recurring"`
	Exception bool   `json:"exception"` // this instance was moved (override applied)
	// Timed-only rendering hints (derived server-side, trusted by the frame):
	SpansMidnight bool     `json:"spansMidnight"` // wall end date > wall start date
	Days          int      `json:"days"`          // all-day: day count (End exclusive)
	ConflictIDs   []string `json:"conflictIds,omitempty"`
	DSTNote       string   `json:"dstNote,omitempty"` // set when start/end hit a gap or ambiguity
}

// Transition is one DST boundary inside a requested range. The server hands
// these to the frame so the UI can mark them — the frame would otherwise have
// no way to know a given Tuesday is an hour short.
type Transition struct {
	Date         string `json:"date"` // wall date the transition lands on
	InstantUTC   string `json:"instantUtc"`
	WallFrom     string `json:"wallFrom"`     // "02:00"
	WallTo       string `json:"wallTo"`       // "03:00"
	DeltaMinutes int    `json:"deltaMinutes"` // +60 spring forward, −60 fall back
	Kind         string `json:"kind"`         // "forward" | "back"
}

// --- model validation --------------------------------------------------------

// Bounds on host-supplied and wire-supplied values. The events source is
// host-owned (trusted), but the same validation runs on it anyway: a bad
// event definition is a bug wherever it came from, and failing at the source
// boundary is louder than failing per-request forever.
const (
	maxTitleLen    = 200
	maxByDayRules  = 7
	maxEventsWarn  = 5000 // sanity ceiling on one source call
	maxRangeDays   = 400  // occurrence-window request cap
	maxMoveDays    = 366  // |dayDelta|
	maxMoveMinutes = 1440 // |minuteDelta|
)

// ValidateEvent checks one event definition. It returns a descriptive error
// (never a silent fix): the plugin's contract is that bad definitions are
// rejected loudly, wherever they entered.
func ValidateEvent(ev Event) error {
	if ev.ID == "" {
		return fmt.Errorf("event id is empty")
	}
	if len(ev.Title) > maxTitleLen {
		return fmt.Errorf("event %s: title exceeds %d characters", ev.ID, maxTitleLen)
	}
	if _, err := loadZone(ev.Zone); err != nil {
		return fmt.Errorf("event %s: %w", ev.ID, err)
	}
	if ev.AllDay {
		start, err := parseWallDate(ev.Start)
		if err != nil {
			return fmt.Errorf("event %s (all-day): start %w", ev.ID, err)
		}
		end, err := parseWallDate(ev.End)
		if err != nil {
			return fmt.Errorf("event %s (all-day): end %w", ev.ID, err)
		}
		if !end.after(start) {
			return fmt.Errorf("event %s (all-day): end %s does not come after start %s (end is exclusive, so a one-day event ends the next day)", ev.ID, ev.End, ev.Start)
		}
	} else {
		start, err := parseWall(ev.Start)
		if err != nil {
			return fmt.Errorf("event %s: start %w", ev.ID, err)
		}
		end, err := parseWall(ev.End)
		if err != nil {
			return fmt.Errorf("event %s: end %w", ev.ID, err)
		}
		if !end.after(start) {
			return fmt.Errorf("event %s: end %s is not after start %s", ev.ID, ev.End, ev.Start)
		}
	}
	if ev.RRule != nil {
		start, err := parseWallAuto(ev.Start)
		if err != nil {
			return fmt.Errorf("event %s: start %w", ev.ID, err)
		}
		if err := validateRRule(*ev.RRule, start); err != nil {
			return fmt.Errorf("event %s: %w", ev.ID, err)
		}
	}
	return nil
}

// normalizeDoc validates and canonicalises a view-state doc from the wire.
// It rejects rather than repairs: an unknown mode or malformed date saved
// now is a frame that cannot navigate later.
func normalizeDoc(raw []byte) (Doc, error) {
	var d Doc
	if err := decodeStrict(raw, &d); err != nil {
		return Doc{}, err
	}
	if d.SchemaVersion != SchemaVersion {
		return Doc{}, fmt.Errorf("schemaVersion %q is not %q", d.SchemaVersion, SchemaVersion)
	}
	if d.View.Mode == "" {
		d.View.Mode = "week"
	}
	switch d.View.Mode {
	case "month", "week", "day":
	default:
		return Doc{}, fmt.Errorf("view mode %q is not one of month|week|day", d.View.Mode)
	}
	date := d.View.Date
	if date == "" {
		d.View.Date = "2026-03-09"
	}
	if _, err := parseWallDate(d.View.Date); err != nil {
		return Doc{}, fmt.Errorf("view date: %w", err)
	}
	return d, nil
}

// decodeStrict is declared in handlers.go (it needs encoding/json and the
// envelope cap); the declaration lives with the other wire helpers so the
// decoding rules stay in one file.
// (See decodeEnvelope in handlers.go.)

// weekdayCode maps a Go weekday to the RFC 5545 two-letter code.
var weekdayCode = [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

func weekdayFromCode(code string) (time.Weekday, bool) {
	switch strings.ToUpper(code) {
	case "MO":
		return time.Monday, true
	case "TU":
		return time.Tuesday, true
	case "WE":
		return time.Wednesday, true
	case "TH":
		return time.Thursday, true
	case "FR":
		return time.Friday, true
	case "SA":
		return time.Saturday, true
	case "SU":
		return time.Sunday, true
	}
	return 0, false
}
