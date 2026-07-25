// User-journey e2e for the guided-tour ("app cues") plugin — the first TRUSTED
// host-page plugin. It runs in the page (no iframe), so the overlay lives in the
// top document. Drives the tour explicitly via window.gofastrTour rather than the
// demo page's auto-run, since auto-run is gated on server "seen" state shared
// across tests.
import { test, expect, type Page } from "@playwright/test";

const TOUR = "/tour";
const SEEN = "/__gofastr/plugin/tour/seen";

// Minimal typed view of the runtime the demo page installs on window.
interface TourApi {
  run(id: string): Promise<void>;
  restart(id: string): Promise<void>;
  stop(): void;
  markSeen(id: string): Promise<void>;
  isSeenLocally(id: string): boolean;
}
const runtime = (page: Page) =>
  page.evaluate(() => (window as unknown as { gofastrTour: TourApi }).gofastrTour !== undefined);

const bubble = (page: Page) => page.locator(".gofastr-tour-bubble");

async function ready(page: Page) {
  await page.waitForFunction(
    () => (window as unknown as { gofastrTour?: unknown }).gofastrTour !== undefined,
    undefined,
    { timeout: 15_000 }
  );
}

async function run(page: Page, id: string) {
  // run()'s promise resolves only when the tour FINISHES, so fire-and-forget:
  // awaiting it here would hang until the tour closes.
  await page.evaluate((tid) => {
    void (window as unknown as { gofastrTour: TourApi }).gofastrTour.run(tid);
  }, id);
}

test.beforeEach(async ({ page }) => {
  // Suppress the demo page's async auto-run BEFORE the page loads (mark it seen):
  // autoRun('demo') bails synchronously on a seen tour, so it can't clobber the
  // tour each test drives explicitly. Explicit run('demo') ignores seen state.
  await page.addInitScript(() => {
    try {
      localStorage.setItem("gofastrTour:seen:demo", "1");
    } catch {
      /* private mode — the stop() below is the fallback */
    }
  });
  await page.goto(TOUR);
  await ready(page);
  expect(await runtime(page)).toBe(true);
  await page.evaluate(() => (window as unknown as { gofastrTour: TourApi }).gofastrTour.stop());
});

const next = (page: Page) => bubble(page).locator(".gofastr-tour-next");

test("runs a tour: spotlight scrim + stepped dialog, mouse and keyboard nav, accent, Done closes it", async ({ page }) => {
  await run(page, "demo");

  await expect(bubble(page)).toBeVisible();
  await expect(bubble(page)).toHaveAttribute("role", "dialog");
  await expect(page.locator(".gofastr-tour-scrim")).toBeVisible(); // the dimmer/spotlight
  // The demo tour sets a tour-level accent option (styling is configurable).
  await expect(page.locator(".gofastr-tour-root")).toHaveAttribute("style", /--gofastr-tour-accent/);
  await expect(bubble(page).locator(".gofastr-tour-progress")).toContainText("Step 1 of 4");
  await expect(bubble(page).locator(".gofastr-tour-title")).toContainText("Welcome");

  // Advance with the Next button.
  await next(page).click();
  await expect(bubble(page).locator(".gofastr-tour-progress")).toContainText("Step 2 of 4");

  // Advance with the keyboard.
  await page.keyboard.press("ArrowRight");
  await expect(bubble(page).locator(".gofastr-tour-progress")).toContainText("Step 3 of 4");

  await next(page).click();
  await expect(bubble(page).locator(".gofastr-tour-progress")).toContainText("Step 4 of 4");
  await expect(next(page)).toHaveText("Done");

  // Done closes the tour.
  await next(page).click();
  await expect(bubble(page)).toHaveCount(0);
});

test("a step's before-action opens a collapsed panel to reveal a buried target", async ({ page }) => {
  await expect(page.locator("#demo-buried")).toBeHidden(); // panel starts collapsed
  await run(page, "demo");

  // Advance to step 3 (the buried target): its before-actions click "Advanced"
  // to open the panel, then wait for the element before spotlighting it.
  await next(page).click(); // step 2
  await next(page).click(); // step 3 → before-action reveals the target
  await expect(page.locator("#demo-panel")).toHaveClass(/open/);
  await expect(page.locator("#demo-buried")).toBeVisible();
  await expect(bubble(page)).toContainText(/buried/i);
});

test("a step renders server-composed component content, styled by host CSS", async ({ page }) => {
  await run(page, "demo");
  await next(page).click(); // 2
  await next(page).click(); // 3
  await next(page).click(); // 4 → server-rendered custom step
  await expect(bubble(page).locator("h3.tour-custom-title")).toContainText("Rendered by a Go component");
  // The host page's CSS reaches the bubble content (trusted-plugin perk): the
  // list gets its left padding from the .tour-custom-list rule on this page.
  const pad = await bubble(page).locator("ul.tour-custom-list").evaluate(
    (el) => getComputedStyle(el).paddingLeft
  );
  expect(parseFloat(pad)).toBeGreaterThan(0);
});

test("a step can target a live element reference instead of a selector", async ({ page }) => {
  // JS-defined tour: pass the element itself (a framework would pass ref.current)
  // — no selector, no id needed.
  await page.evaluate(() => {
    const el = document.getElementById("demo-card");
    (window as unknown as { gofastrTour: { run(t: unknown): Promise<void> } }).gofastrTour.run({
      steps: [{ target: el, title: "By reference", body: "targeted via a live element ref" }],
    });
  });
  await expect(bubble(page)).toBeVisible();
  await expect(bubble(page)).toContainText("targeted via a live element ref");

  // The spotlight cutout hugs the referenced element (its width), not the 200px
  // centered placeholder used when a target can't be resolved.
  const w = await page.evaluate(() => ({
    el: document.getElementById("demo-card")!.getBoundingClientRect().width,
    cut: document.querySelector(".gofastr-tour-cutout")!.getBoundingClientRect().width,
  }));
  expect(w.el).toBeGreaterThan(220); // sanity: the card is much wider than the placeholder
  expect(Math.abs(w.el - w.cut)).toBeLessThan(2);
});

// Regression guard for an ordering bug that only showed up under load: showStep
// made the overlay visible, then deferred the FIRST position() until after
// scrollIntoView + two animation frames. Until it ran, the scrim and cutout had
// no geometry at all. On a fast machine that is a sub-frame flash; on a loaded
// CI runner it was a visibly misplaced spotlight — and it made the test above
// fail there while passing locally.
//
// This asserts the invariant directly instead of racing it: drain microtasks
// only, never yielding an animation frame, and require the cutout to already
// hug its target. Before the fix the cutout spans the whole viewport here.
test("the cutout is positioned before any animation frame", async ({ page }) => {
  const res = await page.evaluate(async () => {
    const el = document.getElementById("demo-card")!;
    void (window as unknown as { gofastrTour: { run(t: unknown): Promise<void> } }).gofastrTour.run({
      steps: [{ target: el, title: "By reference", body: "positioned synchronously" }],
    });
    for (let i = 0; i < 50; i++) await Promise.resolve();
    const cut = document.querySelector(".gofastr-tour-cutout");
    return {
      el: el.getBoundingClientRect().width,
      cut: cut ? cut.getBoundingClientRect().width : -1,
    };
  });
  expect(res.cut, "the cutout must exist as soon as the tour starts").toBeGreaterThan(0);
  expect(Math.abs(res.el - res.cut), "the cutout must hug its target from the first frame")
    .toBeLessThan(2);
});

test("Esc skips the tour", async ({ page }) => {
  await run(page, "demo");
  await expect(bubble(page)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(bubble(page)).toHaveCount(0);
});

test("completion persists client-side (localStorage) and to the server", async ({ page, baseURL }) => {
  await page.evaluate(
    (tid) => (window as unknown as { gofastrTour: TourApi }).gofastrTour.markSeen(tid),
    "e2e-persist"
  );

  const local = await page.evaluate(
    (tid) => (window as unknown as { gofastrTour: TourApi }).gofastrTour.isSeenLocally(tid),
    "e2e-persist"
  );
  expect(local).toBe(true);

  const resp = await page.request.get(`${baseURL}${SEEN}?tourId=e2e-persist`);
  expect((await resp.json()).seen).toBe(true);
});
