package main

// The calendar demo's data layer: a fixed seed of events chosen to make the
// plugin's argument visible in one glance. Every hard case is in the seed:
//
//   - an event whose END falls inside the spring-forward gap (the server
//     resolves it and the demo readout shows the divergence),
//   - a weekly standup series straddling BOTH 2026 transitions (wall 09:00
//     constant, instants shifting 23h/25h),
//   - a conflict pair (the server's overlap verdict, styled by the frame),
//   - a two-day all-day event and a midnight-spanning deploy window.
//
// The seed is static Go — deterministic, offline, and the e2e journey
// asserts exact rendered times derived from the same IANA rules.

import (
	"context"

	"github.com/DonaldMurillo/gofastr-plugins/calendar"
)

// demoCalendarEvents is the plugin's WithEventsSource.
func demoCalendarEvents(context.Context) ([]calendar.Event, error) {
	ny := "America/New_York"
	return []calendar.Event{
		{
			ID: "gapend", Title: "Red-eye arrival",
			Start: "2026-03-08T01:30", End: "2026-03-08T02:00", Zone: ny,
		},
		{
			ID: "standup", Title: "Standup",
			Start: "2026-02-02T09:00", End: "2026-02-02T09:30", Zone: ny,
			RRule: &calendar.RRule{Freq: "WEEKLY", ByDay: []string{"MO", "TU", "WE", "TH", "FR"}, Until: "2026-12-31"},
		},
		{
			ID: "board", Title: "Board review",
			Start: "2026-03-11T13:00", End: "2026-03-11T15:00", Zone: ny,
		},
		{
			ID: "one2one", Title: "1:1 with Dana",
			Start: "2026-03-11T14:30", End: "2026-03-11T15:30", Zone: ny,
		},
		{
			ID: "retro", Title: "Release retro",
			Start: "2026-01-13T10:00", End: "2026-01-13T11:00", Zone: ny,
			RRule: &calendar.RRule{Freq: "MONTHLY", ByDay: []string{"2TU"}},
		},
		{
			ID: "offsite", Title: "GoFastr offsite",
			Start: "2026-03-12", End: "2026-03-14", AllDay: true, Zone: ny,
		},
		{
			ID: "deploy", Title: "Night deploy window",
			Start: "2026-03-07T23:30", End: "2026-03-08T00:30", Zone: ny,
		},
		{
			ID: "fold", Title: "Fold-hour coffee",
			Start: "2026-11-01T01:30", End: "2026-11-01T02:30", Zone: ny,
		},
	}, nil
}

// demoCalendarDoc is the view state mounted on the demo page before any
// save: the week whose Sunday contains the spring-forward transition.
func demoCalendarDoc() calendar.Doc {
	return calendar.Doc{
		SchemaVersion: calendar.SchemaVersion,
		View:          calendar.View{Date: "2026-03-08", Mode: "week"},
	}
}
