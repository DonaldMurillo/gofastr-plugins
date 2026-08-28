# Calendar plugin (`calendar`)

A month/week/day calendar written **from scratch** — no FullCalendar, no
upstream JavaScript library of any kind (the frame bundle is ~24 KB raw /
~8.8 KB gzip with **zero npm dependencies**). The point of the plugin is
where the hard parts live: **recurrence, timezones and conflict detection
run in Go**, and the frame renders what the server already resolved.

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/calendar`
- **Route prefix:** `/__gofastr/plugin/calendar`
- **Isolation:** `sandbox-iframe-opaque` (`allow-scripts`, no `allow-same-origin`)
- **Canonical doc (schema `calendar-v1`):** view state only —
  `{view:{date, mode}}`. **Events are never part of the doc.**
- **Capabilities:** `document:read` (window reads), `document:write`
  (moves + view-state saves), `theme:read`
- **Demo:** `/calendar` — jump buttons land on both 2026 DST weekends, and a
  live readout shows the requested delta next to the wall-clock result and
  the elapsed time of every move.

## The problem this plugin is built around

Wrapping a library proves the wrapper works, not the platform. A calendar is
the sharpest available test because the parts everyone gets wrong —
recurrence, timezones, conflicts — are correctness questions, and the frame
is an untrusted rendering surface. So the split is structural:

- **The events source owns the definitions.** Events are naive wall-clock
  strings plus an IANA zone name — never instants, because an event
  definition is not an instant: "daily 09:00 in New York" survives a DST
  change precisely because it is wall anchored. The frame never receives an
  RRULE at all; it cannot mis-expand a rule it is never told.
- **Go answers the zone questions.** Every occurrence is resolved to
  explicit instants AND explicit wall clocks before it crosses the bridge.
- **The frame sends intents, not results.** "Move occurrence X by N wall
  minutes" goes to `POST /move`; the host re-resolves through the zone and
  returns what actually happened — including a wall-clock delta different
  from the one dragged when the target lands in a spring-forward gap or a
  fall-back fold. The frame renders the answer.

## The bridge protocol (no protocol change)

Frame-to-host requests are fire-and-forget events answered by a correlated
event — the richtext `requestUpload` → `uploadResult` pattern:

| frame emits | host adapter POSTs | frame receives |
|---|---|---|
| `requestOccurrences` `{reqId, from, to}` | `/occurrences` | `occurrencesResult` `{reqId, occurrences[], conflicts[], transitions[], zone}` |
| `requestMove` `{reqId, eventId, date, dayDelta, minuteDelta}` | `/move` | `moveResult` `{reqId, occurrence, requestedWallMinutes, actualWallMinutes, elapsedMinutes, note}` |

Each occurrence carries **both** time systems, explicitly: `startUtc`/
`endUtc` (RFC3339 instants), `startWall`/`endWall` (naive strings in the
event's zone — and for gap-carried times, the wall time where it *landed*,
not the nonexistent one that was asked for), the zone's abbreviation and
offset, a `dstNote` whenever a resolution was not exact, and the
server-computed `conflictIds`. The frame's layout math slices those strings;
it never constructs a zoned `Date` and never consults a timezone database —
it cannot guess wrong because it is never asked to guess.

The adapter mirrors the proof payloads onto the iframe element:
`iframe.__calendarOccCount` (what one window delivered) and
`iframe.__calendarLastMove` (the server's answer to the latest move —
requested vs wall vs elapsed, plus the note). The demo page's live
"Server resolution" strip polls exactly these mirrors.

## The recurrence subset (exactly this, nothing more)

```
FREQ     = DAILY | WEEKLY | MONTHLY
INTERVAL = positive integer (default 1)
COUNT    = positive integer, or
UNTIL    = inclusive wall date "2006-01-02"          — exactly one of COUNT/UNTIL
BYDAY    = MO TU WE TH FR SA SU                      — DAILY: filter; WEEKLY: the week's days
BYDAY    = 1..5 / -1 prefixed codes (2TU, -1FR)      — MONTHLY only
```

Semantics, all wall-clock:

- **DAILY** steps `INTERVAL` days from DTSTART; BYDAY filters to the listed
  weekdays.
- **WEEKLY** uses Monday-start weeks (RFC 5545 WKST default). Without BYDAY
  it expands only DTSTART's weekday; with BYDAY exactly the listed weekdays,
  from DTSTART onward.
- **MONTHLY** without BYDAY expands DTSTART's day-of-month — months that
  lack it (Jan 31 → February) skip it. Plain BYDAY codes expand to *every*
  such weekday of the month; ordinals select the nth (`2TU` = second
  Tuesday, `-1FR` = last Friday; `0` and `>5` are invalid).
- **COUNT counts emitted instances** (DTSTART is the first). **UNTIL is
  inclusive.**

Everything else — YEARLY, sub-daily frequencies, BYMONTH, BYMONTHDAY,
BYSETPOS, BYHOUR, WKST, ordinals outside MONTHLY, COUNT together with UNTIL
(RFC 5545 declares the pair invalid) — is **rejected loudly** with a
specific error (`E_RRULE_UNSUPPORTED`-family messages naming the offender),
never silently approximated. A hostile `COUNT` is capped (100,000) and
series enumeration is step-bounded so a bad rule cannot spin the CPU.

## The timezone policy (pinned by table tests)

Expansion is wall-anchored: a daily 09:00 stays 09:00 across a transition,
and the instants between straddling occurrences shift by 23h (spring
forward) or 25h (fall back). Each occurrence's wall times are then resolved
to instants by `calendar/zones.go` with an explicit, deterministic policy —
the implementation probes the zone's offsets around the nominal instant and
checks each candidate for self-consistency, so it never relies on
`time.Date`'s documented-as-unguaranteed behaviour for ambiguous inputs:

| case | what happens | example (America/New_York) |
|---|---|---|
| **exact** | the wall time maps to one instant | `2026-03-09T09:00` → `13:00Z` (EDT) |
| **gap** (spring forward) | the wall time never occurs; it is **carried by the pre-transition offset**, so it lands *after* the gap in display terms | `2026-03-08T02:30` → `07:30Z`, displays `03:30 EDT` |
| **fold** (fall back) | the wall time occurs twice; the **first (earlier)** occurrence wins | `2026-11-01T01:30` → `05:30Z` (EDT), not the 06:30Z EST repeat |

Consequences worth knowing:

- An event whose **end** falls inside the gap resolves to the derived wall
  time where it landed: `01:30–02:00` on 2026-03-08 becomes
  `01:30–03:30` on the wall clock but **30 real minutes** on the instant
  clock. The occurrence carries a `dstNote` saying so in words.
- A **3-hour meeting spanning the gap** (`01:30–04:30`) keeps both exact
  resolutions: 3 wall hours, **2 real hours**.
- Zones are table-tested beyond US hour steps: Lord Howe's **30-minute**
  transition and Kolkata's fixed half-hour offset both appear in
  `zones_test.go`. The package imports `time/tzdata` (~450 KB) so the
  answers survive on hosts with no system tzdata (scratch images, Windows).
- Zone names are validated (`UTC` and IANA paths only): `Local` and friends
  are refused — a calendar anchored to "whatever zone the server process
  happens to run in" is exactly the silent guess this plugin exists to
  eliminate.

## Moves are intents, and the server's answer is the truth

A drag (pointer events — never HTML5 drag-and-drop, which does not behave in
a sandboxed frame) or an arrow key computes a **wall-clock delta**: whole
days plus minutes, snapped to 15 minutes for drags and 30 for keys. That
intent crosses the bridge; Go:

1. finds the occurrence by **stable identity** (event ID + the ORIGINAL
   series date — an override moves an instance, never the series);
2. applies the delta to the wall fields and re-resolves start and end
   through the zone;
3. records a per-instance **override** (RECURRENCE-ID semantics, shrunk to
   the subset) and re-runs conflict detection in the new neighbourhood;
4. answers with the re-resolved occurrence plus the three numbers the demo
   readout shows: `requestedWallMinutes` (what the grid asked for),
   `actualWallMinutes` (how far the wall clock really moved), and
   `elapsedMinutes` (real time between the old and new starts), plus a
   plain-language `note` whenever they diverge.

The canonical demo moment: an event at 01:30 EST — 30 minutes before the
spring-forward — dragged one hour lands on "02:30", which does not exist.
The server carries it to **03:30 EDT**: requested **+60 min**, wall result
**+120 min**, elapsed **+60 min**. A frame that computed the move itself
would have silently produced a wall time that never happens.

All-day events move by whole days only (`minuteDelta` ≠ 0 is refused);
their `End` is an **exclusive** date (RFC 5545 DATE semantics), so a
two-day offsite is `03-12 → 03-14`.

## Conflicts

Overlap detection runs in Go over **resolved instants** — all-day events
participate through their whole-day instants, end-touching is not overlap,
and instances of the *same* series never conflict with each other (that is
a series-definition problem, not two commitments colliding). It runs on
every window fetch and again after every move; the frame styles what the
server sends (`is-conflict`), it never decides what a conflict is.

## Capabilities, and the authentication warning

| capability | always on? | gates |
|---|---|---|
| `document:read` | yes | `POST /occurrences` |
| `document:write` | yes | `POST /move`, `POST /save` |
| `theme:read` | yes | token bridging |

There are no handler-gated optional capabilities: the move and save hooks
have in-memory defaults (the demo's persistence story — overrides survive a
page reload because the process keeps them). Production hosts wire
`WithMoveHandler` / `WithSaveHandler` to real storage.

> **`pluginhost.Allow` is a capability gate, NOT authentication.** It passes
> for anonymous callers. Both write routes must be treated as unauthenticated
> until the HOST's own handlers check the session. `WithMoveHandler` is
> where that check belongs; the demo's `WithDevGrantAll()` skips the gate
> entirely and MUST NOT survive into a production mount.

## Mounting

```go
import "github.com/DonaldMurillo/gofastr-plugins/calendar"

app.RegisterPlugin(calendar.New(
    // REQUIRED — a calendar with no server-side events has nothing for Go
    // to be right about; construction panics without a source.
    calendar.WithEventsSource(func(ctx context.Context) ([]calendar.Event, error) {
        return store.Events(ctx) // wall-clock strings + IANA zone, never instants
    }),
    // Persistence hooks (defaults: in-memory). Check the session HERE.
    calendar.WithMoveHandler(func(ctx context.Context, ov calendar.Override) error {
        return store.SaveOverride(ctx, ov) // ← authorize against the real session
    }),
    calendar.WithDemoPage(), // themed demo at /calendar
))
```

Drop the mount marker into a form:

```go
calendar.Mount(calendar.MountConfig{
    DocID:      "team",           // persistence key for the view-state doc
    Doc:        initialDocJSON,   // optional {view:{date, mode}}
    Field:      "calendar_doc",   // hidden input the adapter mirrors the doc into
    MinHeight:  "620px",
})
```

Apps rendering through a `UIHost` inject the host scripts with
`calendar.UIHostOption()` — platform broker, then the adapter (there is no
config.js: no optional capabilities to publish).

## Accessibility

There is no library to inherit semantics from, so the grid builds them:
the month view is a real `role=grid` (rows/`gridcell`s, roving tabindex on
day buttons, `aria-selected`), event chips are buttons whose `aria-label`
carries the full sentence ("Standup, 9:00 to 9:30, recurring, conflicts
with 1 other event"), and every pointer action has a keyboard path —
arrows move focus across month days, `Enter` opens, and with a chip focused
in week/day view the arrows **move the event** through the same server
round trip a drag takes. `M`/`W`/`D`/`T` switch views and jump to today.
The popover is a labelled `role=dialog` with `Escape` close.

## Guards worth knowing

- **Envelope strictness.** Request bodies are capped at 64 KiB and must be
  exactly ONE JSON value — anything after the first value is
  `400 E_BAD_JSON`.
- **Window cap.** `/occurrences` refuses ranges over 400 days
  (`E_RANGE_TOO_LARGE`); move deltas are bounded (±366 days, ±1440 minutes,
  `E_DELTA_OUT_OF_RANGE`).
- **Save-path normalisation.** `/save` persists the doc it validated: mode
  whitelisted, date format-checked, unknown fields refused — a garbage save
  cannot come back on the next load as a frame that cannot navigate.
- **Fail-closed routes.** Source errors surface as `500 E_SOURCE` / `422
  E_BAD_EVENT`, never a panic; a bad event definition is rejected wherever
  it entered (host source or wire) with the offender named.
- **No rules on the wire.** The occurrences payload is asserted (Go test)
  to contain no `rrule`/`freq` keys — the structural version of "the frame
  never computes a recurrence".
