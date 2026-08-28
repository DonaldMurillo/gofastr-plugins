// User-journey e2e for the calendar plugin — the correctness plugin. The
// suite's job is to prove the claim the plugin exists to make: recurrence,
// timezones and conflicts are answered by the HOST's Go process, the frame
// only renders server-resolved occurrences, and a move is an INTENT whose
// answer can differ from the drag — most visibly when a drag lands on a
// spring-forward boundary the frame cannot even see coming.
//
// Deterministic fixtures: the seed lives in example/calendar.go and every
// expected time below was derived from the same IANA rules (America/New_York
// 2026: spring forward Sun Mar 8 02:00→03:00, fall back Sun Nov 1 02:00→01:00).
//
// Journeys mutate server state (overrides live in the plugin's in-memory
// store and survive page reloads BY DESIGN — that is the persistence story).
// Every move journey therefore normalizes its target event back to the seed
// position first via the public /move route, so tests are order-independent
// and retry-safe.
import { test, expect, type Page, type APIRequestContext } from "@playwright/test";

const GRID = "/calendar";
const OCC = "/__gofastr/plugin/calendar/occurrences";
const MOVE = "/__gofastr/plugin/calendar/move";
const SAVE = "/__gofastr/plugin/calendar/save";

const DEFAULT_DOC = {
  schemaVersion: "calendar-v1",
  view: { date: "2026-03-08", mode: "week" },
};

// ─── harness ────────────────────────────────────────────────────────────────

function fl(page: Page) {
  return page.frameLocator("iframe");
}

type Mirror = HTMLIFrameElement & {
  __calendarReady?: boolean;
  __calendarOccCount?: { count: number; conflicts: number; zone: string; from: string; to: string } | null;
  __calendarLastMove?: {
    title: string;
    from: string;
    to: string;
    requestedWallMinutes: number | null;
    actualWallMinutes: number | null;
    elapsedMinutes: number | null;
    note: string;
  } | null;
  __calendarLastError?: string;
};

// Console/page errors captured from BEFORE navigation, so boot-time errors
// (script load failures, CSP violations, uncaught exceptions during the
// ready→init handshake) are visible to the assertions.
const consoleErrors = new WeakMap<Page, string[]>();

async function ready(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe");
      return !!(f && (f as Mirror).__calendarReady);
    },
    undefined,
    { timeout: 20_000 }
  );
  await fl(page).locator(".cal-evt").first().waitFor({ timeout: 10_000 });
}

async function resetViewState(request: APIRequestContext, baseURL: string): Promise<void> {
  await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", doc: DEFAULT_DOC, schemaVersion: "calendar-v1" },
  });
}

interface WireOccurrence {
  id: string;
  eventId: string;
  startWall: string;
  endWall: string;
}

async function occurrences(
  request: APIRequestContext,
  baseURL: string,
  from: string,
  to: string
): Promise<WireOccurrence[]> {
  const resp = await request.post(`${baseURL}${OCC}`, { data: { docId: "demo", from, to } });
  expect(resp.ok(), `GET window ${from}..${to}: ${resp.status()}`).toBeTruthy();
  const body = (await resp.json()) as { occurrences: WireOccurrence[] };
  return body.occurrences;
}

/** Minutes-of-day of a wall string ("2026-03-08T01:30" → 90). */
function wallMinutes(wall: string): number {
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2})$/.exec(wall);
  if (!m) throw new Error(`bad wall string ${wall}`);
  return Number(m[2]) * 60 + Number(m[3]);
}

/**
 * Normalize one occurrence to the seed position using the PUBLIC move route:
 * read where it is now, move by the difference. Idempotent, so journeys stay
 * independent no matter what an earlier (or retried) journey left behind.
 */
async function moveTo(
  request: APIRequestContext,
  baseURL: string,
  eventId: string,
  date: string,
  targetWall: string
): Promise<void> {
  const occs = await occurrences(request, baseURL!, date, date);
  const occ = occs.find((o) => o.id === `${eventId}/${date}`);
  expect(occ, `occurrence ${eventId}/${date} exists`).toBeDefined();
  if (!occ) throw new Error(`occurrence ${eventId}/${date} missing`);
  const delta = wallMinutes(targetWall) - wallMinutes(occ.startWall);
  const resp = await request.post(`${baseURL}${MOVE}`, {
    data: { docId: "demo", eventId, date, dayDelta: 0, minuteDelta: delta },
  });
  expect(resp.ok(), `normalize ${eventId} by ${delta}min: ${resp.status()} ${await resp.text()}`).toBeTruthy();
}

test.beforeEach(async ({ page, request, baseURL }) => {
  const errors: string[] = [];
  consoleErrors.set(page, errors);
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(String(e)));
  await resetViewState(request, baseURL!);
});

// ─── 1. mount + sandbox + no console errors ────────────────────────────────

test("mounts sandboxed (allow-scripts, no allow-same-origin) with no console errors and renders the DST week", async ({ page, request, baseURL }) => {
  // Another journey (or another browser project) may have moved the gap
  // event — normalize the seed before asserting it.
  await moveTo(request, baseURL!, "gapend", "2026-03-08", "2026-03-08T01:30");
  await page.goto(GRID);
  await ready(page);

  const frame = page.locator("iframe");
  await expect(frame).toHaveAttribute("sandbox", "allow-scripts");
  await expect(frame).toHaveAttribute("title", "Calendar");

  // The week whose Sunday carries the spring-forward transition.
  await expect(fl(page).locator("#cal-title")).toHaveText(/March 2 – 8, 2026/);
  // The server-resolved gap event: 01:30 start, end derived past the gap.
  await expect(fl(page).locator(".cal-evt[aria-label*='Red-eye arrival']")).toHaveAttribute(
    "aria-label",
    /1:30 to 3:00/
  );
  // The frame never receives an RRULE and computes nothing: the DST badge and
  // the dashed marker both come from the server's transitions list.
  await expect(fl(page).locator("#cal-dst")).toHaveText(/2026-03-08: 02:00→03:00 \(\+1h\)/);
  await expect(fl(page).locator(".cal-dstline")).toBeVisible();
  await expect(fl(page).locator("#cal-zone")).toContainText("America/New_York");

  // Occurrences crossed the bridge (mirror), rules never did.
  const count = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f?.__calendarOccCount;
  });
  expect(count?.count ?? 0).toBeGreaterThan(0);
  expect(count?.zone).toBe("America/New_York");

  expect(consoleErrors.get(page) ?? [], "console errors").toEqual([]);
});

// ─── 2. recurrence: wall clock constant across both transitions ────────────

test("a weekly series keeps its 9:00 wall clock on both sides of the fall-back, and never appears on weekends", async ({ page, request, baseURL }) => {
  await page.goto(GRID);
  await ready(page);

  // Jump to the fall-back weekend (host → adapter → frame event).
  await page.click('[data-jump="2026-11-01"]');
  await expect(fl(page).locator("#cal-title")).toHaveText(/Oct 26 – Nov 1, 2026/);
  // Mon–Fri chips, all at 9:00 — the instants shift across Nov 1, the wall
  // clock does not. That is Go's answer, rendered.
  const week = fl(page).locator(".cal-evtbox .cal-evt[aria-label*='Standup']");
  await expect(week).toHaveCount(5);
  for (let i = 0; i < 5; i++) {
    await expect(week.nth(i)).toHaveAttribute("aria-label", /9:00 to 9:30/);
    await expect(week.nth(i)).toHaveAttribute("aria-label", /recurring/);
  }
  // Saturday Oct 31 and Sunday Nov 1 carry no standup: BYDAY is server-side.
  const sun = fl(page).locator(".cal-daycol[data-day='2026-11-01'] .cal-evt[aria-label*='Standup']");
  await expect(sun).toHaveCount(0);
  const sat = fl(page).locator(".cal-daycol[data-day='2026-10-31'] .cal-evt[aria-label*='Standup']");
  await expect(sat).toHaveCount(0);
  await fl(page).locator("#cal-next").click(); // Mon Nov 2 week
  await expect(fl(page).locator("#cal-title")).toHaveText(/November 2 – 8, 2026/);
  const nextWeek = fl(page).locator(".cal-evtbox .cal-evt[aria-label*='Standup']");
  await expect(nextWeek).toHaveCount(5);
  await expect(nextWeek.first()).toHaveAttribute("aria-label", /9:00 to 9:30/);

  // And the wire never carried a rule: the /occurrences payload has no rrule.
  const body = await occurrences(request, baseURL!, "2026-11-02", "2026-11-06");
  expect(JSON.stringify(body)).not.toContain("rrule");
  expect(consoleErrors.get(page) ?? []).toEqual([]);
});

// ─── 3. the money shot: drag across the spring-forward gap ─────────────────

test("dragging the 1:30 event one hour across the spring-forward gap lands at 3:30 — the server answers, not the frame", async ({ page, request, baseURL }) => {
  await moveTo(request, baseURL!, "gapend", "2026-03-08", "2026-03-08T01:30");
  await page.goto(GRID);
  await ready(page);

  const chip = fl(page).locator(".cal-evtbox .cal-evt[aria-label*='Red-eye arrival']");
  await chip.scrollIntoViewIfNeeded();
  // Seeded state: 01:30 start; the 02:00 end falls inside the gap and the
  // server already resolved it to a derived 03:00 wall end.
  await expect(chip).toHaveAttribute("aria-label", /1:30 to 3:00/);

  // Pointer drag (never HTML5 DnD): one hour down the wall grid = 44px.
  const box = await chip.boundingBox();
  expect(box, "chip geometry").not.toBeNull();
  const cx = box!.x + box!.width / 2;
  const cy = box!.y + 8;
  await page.mouse.move(cx, cy);
  await page.mouse.down();
  await page.mouse.move(cx, cy + 22, { steps: 4 });
  await page.mouse.move(cx, cy + 44, { steps: 4 });
  await page.mouse.up();

  // The mirror carries the server's answer: requested +60, wall +120,
  // elapsed +60 — 02:30 does not exist, so the wall clock jumped the gap.
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as Mirror | null;
      const mv = f?.__calendarLastMove;
      return !!mv && mv.requestedWallMinutes === 60 && mv.actualWallMinutes === 120 && mv.elapsedMinutes === 60;
    },
    undefined,
    { timeout: 10_000 }
  );
  const mv = await page.evaluate(() => (document.querySelector("iframe") as Mirror).__calendarLastMove);
  if (!mv) throw new Error("no move mirror after the drag");
  expect(mv.note).toContain("does not exist");
  expect(mv.to).toBe("2026-03-08T03:30");

  // The frame renders what came back: 3:30–4:00 — the wall duration is
  // preserved from the NORMALIZED start, never from the nonexistent 02:30.
  await expect(chip).toHaveAttribute("aria-label", /3:30 to 4:00/);
  await expect(page.locator("#cal-req")).toHaveText("+1h");
  await expect(page.locator("#cal-wall")).toHaveText("+2h");
  await expect(page.locator("#cal-elapsed")).toHaveText("+1h");
  await expect(page.locator("#cal-note")).toContainText("does not exist");
  expect(consoleErrors.get(page) ?? []).toEqual([]);
});

// ─── 4. a normal drag is boring (the control case) ─────────────────────────

test("dragging on a transition-free day applies exactly what was dragged", async ({ page, request, baseURL }) => {
  await moveTo(request, baseURL!, "board", "2026-03-11", "2026-03-11T13:00");
  await page.goto(GRID);
  await ready(page);
  await fl(page).locator("#cal-next").click(); // into the Mar 9–15 week

  const chip = fl(page).locator(".cal-evtbox .cal-evt[aria-label*='Board review']");
  await chip.scrollIntoViewIfNeeded();
  const box = await chip.boundingBox();
  const cx = box!.x + box!.width / 2;
  const cy = box!.y + 8;
  await page.mouse.move(cx, cy);
  await page.mouse.down();
  await page.mouse.move(cx, cy + 44, { steps: 6 });
  await page.mouse.up();

  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as Mirror | null;
      const mv = f?.__calendarLastMove;
      return !!mv && mv.requestedWallMinutes === 60 && mv.actualWallMinutes === 60 && mv.elapsedMinutes === 60;
    },
    undefined,
    { timeout: 10_000 }
  );
  await expect(chip).toHaveAttribute("aria-label", /14:00 to 16:00/);
  expect(consoleErrors.get(page) ?? []).toEqual([]);
});

// ─── 5. conflicts are the server's verdict ─────────────────────────────────

test("overlapping events render with conflict styling; end-touching does not; all-day spans the day", async ({ page, request, baseURL }) => {
  await moveTo(request, baseURL!, "board", "2026-03-11", "2026-03-11T13:00");
  await page.goto(GRID);
  await ready(page);

  // The midnight-spanning deploy window appears on both of its wall dates in
  // THIS week (Mar 2–8), the second one flagged as a continuation.
  const sat = fl(page).locator(".cal-daycol[data-day='2026-03-07'] .cal-evt[aria-label*='deploy']");
  const sun = fl(page).locator(".cal-daycol[data-day='2026-03-08'] .cal-evt[aria-label*='deploy']");
  await expect(sat).toHaveCount(1);
  await expect(sun).toHaveCount(1);
  await expect(sun).toHaveClass(/is-continues/);

  await fl(page).locator("#cal-next").click(); // into the Mar 9–15 week

  const board = fl(page).locator(".cal-evt[aria-label*='Board review']");
  const one2one = fl(page).locator(".cal-evt[aria-label*='1:1 with Dana']");
  await expect(board).toHaveClass(/is-conflict/);
  await expect(one2one).toHaveClass(/is-conflict/);
  await expect(board).toHaveAttribute("aria-label", /conflicts with/);

  // The all-day offsite renders once, in the all-day row, with its day count.
  const offsite = fl(page).locator(".cal-allday .cal-evt[aria-label*='offsite']");
  await expect(offsite).toHaveCount(1);
  await expect(offsite).toHaveAttribute("aria-label", /all-day, 2 days/);

  expect(consoleErrors.get(page) ?? []).toEqual([]);
});

// ─── 6. keyboard: focus across days, open, and move — no mouse ─────────────

test("keyboard moves focus across month days, opens a day view, and moves an event through the server", async ({ page, request, baseURL }) => {
  await moveTo(request, baseURL!, "gapend", "2026-03-08", "2026-03-08T01:30");
  await page.goto(GRID);
  await ready(page);

  // Month view: a real grid with roving focus on day buttons.
  await fl(page).locator("#cal-v-month").click();
  await expect(fl(page).locator("#cal-title")).toHaveText(/March 2026/);
  const day = fl(page).locator(".cal-mdate[aria-label='2026-03-11: open day view']");
  await day.focus();
  await expect(day).toBeFocused();
  await page.keyboard.press("ArrowRight");
  const next = fl(page).locator(".cal-mdate[aria-label='2026-03-12: open day view']");
  await expect(next).toBeFocused();
  await page.keyboard.press("ArrowDown");
  const below = fl(page).locator(".cal-mdate[aria-label='2026-03-19: open day view']");
  await expect(below).toBeFocused();
  await page.keyboard.press("ArrowUp");
  await expect(next).toBeFocused();

  // Enter opens the focused day.
  await page.keyboard.press("Enter");
  await expect(fl(page).locator("#cal-title")).toHaveText(/Thursday, Mar 12, 2026/);
  // The offsite is there, all-day.
  await expect(fl(page).locator(".cal-allday .cal-evt[aria-label*='offsite']")).toBeVisible();

  // Keyboard move: focus the 1:30 event on its own day view and nudge it.
  await page.click('[data-jump="2026-03-08"]');
  await fl(page).locator("#cal-v-day").click();
  const chip = fl(page).locator(".cal-evtbox .cal-evt[aria-label*='Red-eye arrival']");
  await chip.scrollIntoViewIfNeeded();
  // Seeded state: 01:30 start; the 02:00 end falls inside the gap and the
  // server already resolved it to a derived 03:00 wall end.
  await expect(chip).toHaveAttribute("aria-label", /1:30 to 3:00/);
  await chip.scrollIntoViewIfNeeded();
  await chip.focus();
  await page.keyboard.press("ArrowDown"); // +30 min intent
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as Mirror | null;
      const mv = f?.__calendarLastMove;
      return !!mv && mv.requestedWallMinutes === 30;
    },
    undefined,
    { timeout: 10_000 }
  );
  // 01:30 + 30 wall minutes = 02:00, which does not exist on Mar 8 either:
  // the server carries it to 03:00 and the end builds on the normalized
  // wall. Another answer the frame could not have given.
  await expect(chip).toHaveAttribute("aria-label", /3:00 to 3:30/);
  expect(consoleErrors.get(page) ?? []).toEqual([]);
});

// ─── 7. editing one occurrence leaves the series alone ─────────────────────

test("moving Wednesday's standup does not rewrite the series, and survives a reload", async ({ page, request, baseURL }) => {
  await moveTo(request, baseURL!, "standup", "2026-03-11", "2026-03-11T09:00");
  await page.goto(GRID);
  await ready(page);
  const saveDone = page.waitForResponse((r) => r.url().includes("/calendar/save") && r.ok(), { timeout: 10_000 });
  await fl(page).locator("#cal-next").click(); // into the Mar 9–15 week
  await saveDone; // autosave is debounced — let the view state land

  const wed = fl(page).locator(".cal-daycol[data-day='2026-03-11'] .cal-evt[aria-label*='Standup']");
  const thu = fl(page).locator(".cal-daycol[data-day='2026-03-12'] .cal-evt[aria-label*='Standup']");
  await expect(wed).toHaveAttribute("aria-label", /9:00 to 9:30/);
  await wed.scrollIntoViewIfNeeded();

  // Drag Wednesday's standup down one hour (Wednesday has no transition).
  const box = await wed.boundingBox();
  const cx = box!.x + box!.width / 2;
  const cy = box!.y + 8;
  await page.mouse.move(cx, cy);
  await page.mouse.down();
  await page.mouse.move(cx, cy + 44, { steps: 6 });
  await page.mouse.up();
  await expect(wed).toHaveAttribute("aria-label", /10:00 to 10:30/);

  // Thursday (and the rest of the series) is untouched.
  await expect(thu).toHaveAttribute("aria-label", /9:00 to 9:30/);

  // Reload: the override lives in the plugin's store on the SERVER, so the
  // edit survives while the series still renders from the rule.
  await page.reload();
  await ready(page);
  await expect(
    fl(page).locator(".cal-daycol[data-day='2026-03-11'] .cal-evt[aria-label*='Standup']")
  ).toHaveAttribute("aria-label", /10:00 to 10:30/);
  await expect(
    fl(page).locator(".cal-daycol[data-day='2026-03-12'] .cal-evt[aria-label*='Standup']")
  ).toHaveAttribute("aria-label", /9:00 to 9:30/);
  expect(consoleErrors.get(page) ?? []).toEqual([]);
});

// ─── 8. view state round-trips through the save route ──────────────────────

test("navigating autosaves the view state and the reloaded page reopens there", async ({ page }) => {
  await page.goto(GRID);
  await ready(page);

  await fl(page).locator("#cal-v-day").click();
  await expect(fl(page).locator("#cal-title")).toHaveText(/Sunday, Mar 8, 2026/);
  const saveDone = page.waitForResponse((r) => r.url().includes("/calendar/save") && r.ok(), { timeout: 10_000 });
  await fl(page).locator("#cal-next").click();
  await expect(fl(page).locator("#cal-title")).toHaveText(/Monday, Mar 9, 2026/);
  // The autosave is debounced (1.2 s) — let the persist round trip land
  // before reloading, or the reload races it.
  await saveDone;

  await page.reload();
  await ready(page);
  await expect(fl(page).locator("#cal-title")).toHaveText(/Monday, Mar 9, 2026/);
  expect(consoleErrors.get(page) ?? []).toEqual([]);
});
