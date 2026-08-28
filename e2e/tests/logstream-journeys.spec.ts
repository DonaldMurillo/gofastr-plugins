// User-journey e2e for the logstream plugin — the push plugin. Every other
// plugin here moves data in answer to a frame-initiated request; this one is
// PUSHED at, open-ended, and produced faster than it can be rendered. The
// suite's job is to prove the three claims the plugin exists to make:
//
//   1. host-pushed lines render in order, ANSI colour intact, on a sandboxed
//      (opaque-origin, no allow-same-origin) mount with no console errors;
//   2. the BACKPRESSURE path actually runs: at the flood rate the host drops
//      from the oldest end of its buffer (dropped counter climbs), the frame
//      renders a visible drop marker, and the frame's retained scrollback
//      stays at or below its published bound;
//   3. pause stops rendering and resume catches up without a reload, and
//      search reaches a line that has scrolled out of the viewport but is
//      still in the scrollback buffer.
//
// The demo generator is a pure function of the line's sequence number
// (example/logstream.go); the helpers below recompute the same formulas in
// TypeScript so assertions pin EXACT line content, not shapes.
import { test, expect, type Page } from "@playwright/test";

const DEMO = "/logstream";
const RATE = "/demo/logstream/rate";

// ─── deterministic generator mirror (MUST match example/logstream.go) ──────
const LEVELS = ["INFO", "WARN", "ERROR", "DEBUG", "TRACE"];
const SERVICES = ["api-gateway", "auth-svc", "billing", "search", "worker"];
const MESSAGES = [
  "request served",
  "cache hit for profile",
  "upstream latency high, retrying",
  "connection pool at 82% capacity",
  "token refreshed for session",
  "queue depth draining",
  "background job completed",
];

type Mirror = HTMLIFrameElement & {
  __logstreamReady?: boolean;
  __logstreamProbes?: unknown;
  __logstreamDelivered?: number;
  __logstreamDropped?: number;
  __logstreamInFlight?: number;
  __logstreamStats?: { lastSeq: number; rendered: number; scrollback: number; cap: number; rows: number };
};
const pad2 = (n: number) => String(n).padStart(2, "0");

function lineText(n: number): string {
  const ts = `${pad2(Math.floor(n / 3600) % 24)}:${pad2(Math.floor(n / 60) % 60)}:${pad2(n % 60)}`;
  return `${ts} ${LEVELS[n % 5]} ${SERVICES[(n * 3) % 5]} ${MESSAGES[(n * 5) % 7]} seq=${String(n).padStart(6, "0")}`;
}

// The frame's published bounds, mirrored from logstream/js/src/term.ts and
// host/adapter.js.
const SCROLLBACK_CAP = 10_000;
const MAX_IN_FLIGHT = 4;

// ─── harness ────────────────────────────────────────────────────────────────

function fl(page: Page) {
  return page.frameLocator("iframe");
}

// The iframe element carries the adapter's mirrors (the parent cannot read
// into the opaque frame, so the mirror IS the observability channel):
// __logstreamReady/__logstreamProbes, __logstreamDelivered (lines pushed),
// __logstreamDropped (host-side drops), __logstreamInFlight,
// __logstreamStats (the frame's own ack accounting).

// Console/page errors captured from BEFORE navigation, so boot-time errors
// (script load failures, CSP violations, uncaught exceptions during the
// ready→init handshake) are visible to the assertions instead of arriving
// after the listeners were installed.
const consoleErrors = new WeakMap<Page, string[]>();

async function setRate(page: Page, rate: "calm" | "fast"): Promise<void> {
  await page.evaluate(async (r) => {
    await fetch("/demo/logstream/rate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ rate: r }),
    });
  }, rate);
}

async function ready(page: Page): Promise<void> {
  await page.goto(DEMO);
  await page.waitForFunction(() => {
    const f = document.querySelector(".editor-card iframe") as Mirror | null;
    return !!f && f.__logstreamReady === true;
  });
}

// Snapshot of every adapter mirror, as a PLAIN object (element handles do
// not serialize their expando properties across the CDP boundary — this is
// the trap the original stats() helper fell into).
interface FrameState {
  ready: boolean;
  delivered: number;
  dropped: number;
  inFlight: number;
  stats: { lastSeq: number; rendered: number; scrollback: number; cap: number; rows: number } | null;
}

async function frameState(page: Page): Promise<FrameState> {
  const handle = await page.waitForFunction(() => {
    const f = document.querySelector(".editor-card iframe") as Mirror | null;
    if (!f || !f.__logstreamStats) return null;
    return {
      ready: f.__logstreamReady === true,
      delivered: f.__logstreamDelivered ?? 0,
      dropped: f.__logstreamDropped ?? 0,
      inFlight: f.__logstreamInFlight ?? 0,
      stats: f.__logstreamStats,
    };
  });
  return handle.jsonValue() as Promise<FrameState>;
}

// The flood journey deliberately drives the producer faster than the frame can
// render and then polls frame state while ~6,000 lines/s cross the bridge. That
// is real work, and on CI's webkit — the slowest engine here, running last in a
// suite that now covers eleven plugins — it does not fit the suite's 30s
// default. It fails as a page.evaluate timeout while the frame is busy, not as
// a missing drop.
//
// The budget is raised for the work. Every assertion still runs: the counter
// moves, the marker renders, scrollback stays under its bound, and in-flight
// batches stay within the ack window.
test.describe.configure({ timeout: 90_000 });

// The producer keeps a 1,024-line replay ring and re-serves it on connect
// (?after=0), so every fresh page first absorbs a ~1k-line burst at the
// frame's full render rate before settling into the live calm rate. Tests
// that snapshot "now" must first wait for that burst to finish: two samples
// 600 ms apart whose delivered counts differ by < 8 (the calm rate is 5/s).
async function awaitCalmRegime(page: Page): Promise<void> {
  const delivered = () =>
    page.evaluate(() => {
      const f = document.querySelector(".editor-card iframe") as Mirror | null;
      return f?.__logstreamDelivered ?? 0;
    });
  const deadline = Date.now() + 20_000;
  for (;;) {
    const d0 = await delivered();
    await page.waitForTimeout(600);
    const d1 = await delivered();
    if (d1 - d0 < 8) return;
    if (Date.now() > deadline) {
      throw new Error(`stream never settled into the calm regime (delivered ${d0} → ${d1} in 600 ms)`);
    }
  }
}
test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  consoleErrors.set(page, errors);
  page.on("console", (msg) => {
    if (msg.type() === "error") errors.push(msg.text());
  });
  page.on("pageerror", (err) => errors.push(String(err)));
  // Journeys share one server (and one producer); every test starts calm so
  // the deterministic-content assertions are not racing a previous flood.
  await page.goto(DEMO);
  await setRate(page, "calm");
});

// ─── 1. mount + sandbox + order + ANSI + no console errors ─────────────────

test("mounts sandboxed (allow-scripts, no allow-same-origin) and renders host-pushed lines in order with ANSI colour intact", async ({ page }) => {
  await ready(page);

  const frameEl = page.locator(".editor-card iframe");
  await expect(frameEl).toHaveAttribute("sandbox", "allow-scripts");
  await expect(frameEl).toHaveAttribute("referrerpolicy", "no-referrer");

  // The frame's own isolation probes (computed inside the opaque origin at
  // boot and mirrored by the adapter): all three blocked = the guarantee.
  const probes = await page.evaluate(() => {
    const f = document.querySelector(".editor-card iframe") as Mirror;
    return f.__logstreamProbes ?? {};
  });
  expect(probes).toEqual({ cookieEmpty: true, parentBlocked: true, storageBlocked: true });

  // Let a pageful of calm lines land, then read the frame's viewport.
  await page.waitForFunction(
    (min) => {
      const f = document.querySelector(".editor-card iframe") as Mirror;
      return !!f && !!f.__logstreamStats && f.__logstreamStats.rendered >= min;
    },
    20
  );

  const rows = fl(page).locator(".xterm-rows > div");
  const texts = await rows.allInnerTexts();
  const seqs: number[] = [];
  for (const t of texts) {
    const m = t.match(/seq=(\d{6})/);
    if (m) seqs.push(Number(m[1]));
  }
  // Order: every rendered seq in the viewport ascends strictly (the bridge
  // is seq-ordered; a shuffle or a duplicate breaks this immediately).
  expect(seqs.length).toBeGreaterThan(5);
  for (let i = 1; i < seqs.length; i++) {
    expect(seqs[i]).toBeGreaterThan(seqs[i - 1]);
  }

  // Content: a known seq renders EXACTLY the generator's formula for it.
  // Asserted against the SNAPSHOT above — re-querying the row would race
  // the live stream (the row can scroll out of the DOM between read and
  // re-query, which is exactly how this test first failed).
  const needle = `seq=${String(seqs[0]).padStart(6, "0")}`;
  const row = texts.find((t) => t.includes(needle));
  expect(row, `viewport snapshot missing ${needle}`).toBeDefined();
  expect(row).toContain(lineText(seqs[0]).slice(0, 30)); // ts + level

  // ANSI: the level column renders as a COLOURED span (xterm maps SGR
  // colours to .xterm-fg-N classes in the DOM renderer). A bridge that
  // escaped or stripped the SGR codes renders plain spans only.
  const coloured = await fl(page).locator(".xterm-rows span[class^='xterm-fg-']").count();
  expect(coloured).toBeGreaterThan(0);

  const errors = consoleErrors.get(page) ?? [];
  expect(errors, errors.join("\n")).toEqual([]);
});

// ─── 2. the backpressure path actually runs ────────────────────────────────

test("flood rate overruns the render loop: drops are counted, marked visibly, and scrollback stays at or below its bound", async ({ page }) => {
  await ready(page);
  await setRate(page, "fast");

  // Dropped counter climbs (host dropped from the oldest end of its buffer),
  // and a visible "N lines dropped" marker row passes through the viewport —
  // at 6,000 lines/s against a ~1,480 lines/s render rate both are continuous.
  // On CI's webkit this wait has never been satisfied: it consumed 40s of a 30s
  // budget and 1.7m of a 90s one, which is a hang rather than slowness. Report
  // the bridge state on the way out so the next failure says WHY nothing was
  // dropped instead of only that nothing was.
  try {
    await page.waitForFunction(
      () => {
        const f = document.querySelector(".editor-card iframe") as Mirror;
        return !!f && (f.__logstreamDropped ?? 0) > 200;
      },
      { timeout: 20_000 }
    );
  } catch (err) {
    const state = await page.evaluate(() => {
      const f = document.querySelector(".editor-card iframe") as Mirror | null;
      return {
        haveFrame: !!f,
        ready: f?.__logstreamReady ?? null,
        delivered: f?.__logstreamDelivered ?? null,
        dropped: f?.__logstreamDropped ?? null,
        inFlight: f?.__logstreamInFlight ?? null,
        stats: f?.__logstreamStats ?? null,
      };
    });
    const rate = await page
      .evaluate(async () => (await (await fetch("/demo/logstream/rate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ rate: "fast" }) })).text()))
      .catch((e) => `rate probe failed: ${String(e)}`);
    throw new Error(
      `flood never dropped a line.\nbridge state: ${JSON.stringify(state, null, 2)}\nrate route said: ${rate}\noriginal: ${String(err)}`
    );
  }

  // The marker lives in SCROLLBACK, so search for it rather than hoping it is
  // on screen.
  //
  // `.xterm-rows > div` holds only the rows currently in the viewport, and at
  // flood rate every row scrolls out within milliseconds. Pausing first does
  // not help either: pause freezes the drain, so a marker still queued in the
  // frame never reaches the terminal at all. The search addon reads the whole
  // buffer, which is also how a person would find it — the same mechanism the
  // scrollback journey below exercises.
  //
  // A real page.click on Pause still happens further down, so responsiveness
  // under flood stays covered (#40).
  const search = fl(page).locator("#ls-search");
  await search.fill("lines dropped");
  await search.press("Enter");
  const marker = fl(page).locator(".xterm-rows > div", { hasText: "lines dropped" });
  await expect(marker.first()).toBeVisible({ timeout: 15_000 });
  const markerText = await marker.first().innerText();
  expect(markerText).toMatch(/⋯ [\d,]+ lines dropped/);
  await search.fill("");
  await search.press("Escape");

  // Responsiveness under flood: a REAL click, which waits for actionability.
  // This timed out at 90s before rendering was capped per tick (#40).
  await page.click("#ls-btn-pause");
  await page.click("#ls-btn-pause"); // resume

  // The scrollback bound: the frame's own ack accounting (retained history =
  // buffer minus viewport rows) never exceeds the published cap, and the
  // in-flight window never exceeds its size.
  const st = await frameState(page);
  expect(st.stats!.scrollback).toBeLessThanOrEqual(SCROLLBACK_CAP);
  expect(st.stats!.cap).toBe(SCROLLBACK_CAP);
  expect(st.inFlight).toBeLessThanOrEqual(MAX_IN_FLIGHT);

  await setRate(page, "calm");
  const errors = consoleErrors.get(page) ?? [];
  expect(errors, errors.join("\n")).toEqual([]);
});

// ─── 3. pause stops rendering; resume catches up without a reload ──────────

test("pause freezes rendering and delivery; resume catches up on the same mount, no reload", async ({ page }) => {
  await ready(page);
  // Past the connect-time replay burst: the freeze assertions compare exact
  // counts, so the stream must already be in its calm regime.
  await awaitCalmRegime(page);
  const st0 = await frameState(page);
  expect(st0.stats!.rendered).toBeGreaterThan(0);

  await page.click("#ls-btn-pause");
  // Let the batch already in flight settle (~1 render tick), then snapshot:
  // from here rendered and delivered must be EXACTLY frozen — pause stops
  // sending while the adapter keeps draining into its bounded buffer.
  await page.waitForTimeout(300);
  const frozen = await frameState(page);
  const frozenRendered = frozen.stats!.rendered;
  const frozenDelivered = frozen.delivered;
  await page.waitForTimeout(1500);
  const stFrozen = await frameState(page);
  expect(stFrozen.stats!.rendered).toBe(frozenRendered);
  expect(stFrozen.delivered).toBe(frozenDelivered);

  // Resume on the SAME page: delivery resumes and the frame renders forward
  // from the frozen point — no reload, no remount (ready flag stays set).
  await page.click("#ls-btn-pause"); // the button toggles to Resume
  await page.waitForFunction(
    ([r, d]) => {
      const f = document.querySelector(".editor-card iframe") as Mirror;
      return (
        !!f &&
        f.__logstreamReady === true &&
        !!f.__logstreamStats &&
        f.__logstreamStats.rendered > (r as number) &&
        (f.__logstreamDelivered ?? 0) > (d as number)
      );
    },
    [frozenRendered, frozenDelivered],
    { timeout: 10_000 }
  );

  const errors = consoleErrors.get(page) ?? [];
  expect(errors, errors.join("\n")).toEqual([]);
});

// ─── 4. search reaches scrollback that left the viewport ───────────────────

test("search finds a line that scrolled out of the viewport but is still in scrollback", async ({ page }) => {
  await ready(page);

  // The producer is a process-wide singleton (seqs keep climbing across
  // tests) and re-serves its replay ring on connect, so the target must be
  // a seq THIS page actually received early: grab the first acked seq, let
  // the replay burst plus the live stream push it well past the ~37-row
  // viewport, then go looking for it.
  const st0 = await frameState(page);
  const target = st0.stats!.lastSeq;
  const needle = `seq=${String(target).padStart(6, "0")}`;
  await awaitCalmRegime(page);
  await page.waitForFunction(
    () => {
      const f = document.querySelector(".editor-card iframe") as Mirror | null;
      // rendered counts THIS page's lines; the target was among the first,
      // so 65 rendered rows puts it ~28 rows above the 37-row viewport —
      // out of the DOM, in the scrollback, whatever the replay size.
      return !!f && !!f.__logstreamStats && f.__logstreamStats.rendered >= 65;
    },
    undefined,
    { timeout: 20_000 }
  );
  // in the DOM — the DOM renderer keeps only viewport rows).
  const viewport = fl(page).locator(".xterm-rows");
  await expect(viewport).not.toContainText(needle);

  const search = fl(page).locator("#ls-search");
  await search.fill(needle);
  await search.press("Enter");

  await expect(fl(page).locator("#ls-search-status")).toHaveText(/match shown/);
  // findNext scrolls the match into the viewport — the line that was absent
  // from the DOM is back in it. That is "still in scrollback", proven.
  await expect(viewport).toContainText(lineText(target), { timeout: 5_000 });

  const errors = consoleErrors.get(page) ?? [];
  expect(errors, errors.join("\n")).toEqual([]);
});
