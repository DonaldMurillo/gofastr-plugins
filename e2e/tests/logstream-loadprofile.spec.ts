// A measurement, not a test. Opt-in: LOAD_PROFILE=1.
//
// #66 asks whether a 6,000 lines/s flood actually saturates the host page. I
// could not reproduce it locally: at ~5,900 lines/s with the CPU throttled 10x,
// the host's event loop never stalled more than 30ms and the Go server answered
// in 2-3ms. But the failure that raised the question happened on a two-core CI
// runner hosting the producer, a webkit browser and the Playwright driver at
// once, and throttling one process is not the same as oversubscribing a box.
//
// So this runs the measurement THERE. It asserts almost nothing on purpose: a
// number that fails a threshold invites arguing about the threshold, and what
// is wanted is the number. CI prints it; #66 gets a data point from the machine
// that actually has the problem.
//
// It runs at the demo's real rate, not the e2e's lowered one, because 6,000 is
// the number the question is about.
import { test, expect, type Page } from "@playwright/test";

const PORT = Number(process.env.E2E_PORT ?? 8123);

/**
 * Sleep in the DRIVER, never through the page.
 *
 * `page.waitForTimeout` is implemented via the page, so it cannot resolve while
 * the page's main thread is pinned — which is precisely the condition this file
 * exists to measure. Using it here made the instrument depend on the thing it
 * was measuring: on webkit at full rate the profile hit the 180s test timeout
 * twice and produced no number at all, while chromium finished in 8s. A
 * measurement that only works when there is nothing to measure is not a
 * measurement.
 */
const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/**
 * Run a page-dependent call with a ceiling, returning null when the page could
 * not answer in time. A pinned page is DATA here, not an error: "webkit could
 * not evaluate 1+1 within 10s under flood" is the finding, and it belongs in
 * the report rather than in a stack trace.
 */
async function bounded<T>(what: Promise<T>, ms: number): Promise<T | null> {
  return Promise.race([what.catch(() => null), sleep(ms).then(() => null)]);
}
type Mirror = HTMLIFrameElement & {
  __logstreamReady?: boolean;
  __logstreamDelivered?: number;
  __logstreamDropped?: number;
  __logstreamNotices?: number;
};

test.skip(process.env.LOAD_PROFILE !== "1", "load profile is opt-in: LOAD_PROFILE=1");

async function setRate(page: Page, rate: "calm" | "fast"): Promise<void> {
  // Out of band, through the request context: the whole point is that the page
  // may not be able to run anything, so control must not depend on it.
  const res = await page.request.post(`http://localhost:${PORT}/demo/logstream/rate`, {
    headers: { "Content-Type": "application/json" },
    data: { rate },
  });
  expect(res.ok()).toBe(true);
}

test("profile: host event-loop lag under a full-rate flood", async ({ page }, testInfo) => {
  test.setTimeout(180_000);
  await page.goto(`http://localhost:${PORT}/logstream`);
  await page.waitForFunction(
    () => (document.querySelector(".editor-card iframe") as Mirror | null)?.__logstreamReady === true,
    undefined,
    { timeout: 30_000 }
  );

  // A 16ms heartbeat on the HOST page. However fast the machine, the gap
  // between ticks is how long something else held the event loop.
  await page.evaluate(() => {
    (window as unknown as { __lag: number[] }).__lag = [];
    let last = performance.now();
    window.setInterval(() => {
      const now = performance.now();
      (window as unknown as { __lag: number[] }).__lag.push(now - last - 16);
      last = now;
    }, 16);
  });

  await setRate(page, "fast");
  await sleep(1500); // let the flood reach steady state
  await page.evaluate(() => {
    (window as unknown as { __lag: number[] }).__lag = [];
  });

  // Server responsiveness measured OUT of the page, interleaved with the
  // flood: if this is slow too, the bottleneck is the producer's process
  // rather than the browser's event loop, and #66 is looking in the wrong
  // place entirely.
  const serverMs: number[] = [];
  for (let i = 0; i < 6; i++) {
    const t = Date.now();
    await page.request.get(`http://localhost:${PORT}/`);
    serverMs.push(Date.now() - t);
    await sleep(1000);
  }

  const sample = (await bounded(page.evaluate(() => {
    const f = document.querySelector(".editor-card iframe") as Mirror | null;
    const lag = (window as unknown as { __lag: number[] }).__lag.slice().sort((a, b) => a - b);
    const at = (q: number) => (lag.length ? Math.round(lag[Math.floor(lag.length * q)]) : -1);
    return {
      ticks: lag.length,
      lagP50: at(0.5),
      lagP95: at(0.95),
      lagMax: lag.length ? Math.round(lag[lag.length - 1]) : -1,
      over50ms: lag.filter((d) => d > 50).length,
      over500ms: lag.filter((d) => d > 500).length,
      delivered: f?.__logstreamDelivered ?? 0,
      dropped: f?.__logstreamDropped ?? 0,
      notices: f?.__logstreamNotices ?? 0,
    };
  }), 15_000)) ?? {
    // The page never answered. Record that, with sentinels, instead of dying.
    ticks: -1, lagP50: -1, lagP95: -1, lagMax: -1, over50ms: -1, over500ms: -1,
    delivered: 0, dropped: 0, notices: 0,
  };

  // Can the page still run something trivial? This is the operation that
  // timed out at 90 seconds on CI, so it is the one worth timing.
  const t0 = Date.now();
  const answered = await bounded(page.evaluate(() => 1 + 1), 10_000);
  const evaluateMs = answered === null ? -1 : Date.now() - t0;

  await setRate(page, "calm");

  const report = {
    engine: testInfo.project.name,
    linesPerSec: Math.round((sample.delivered + sample.dropped) / 6),
    ...sample,
    serverMs,
    evaluateMs,
  };
  console.log("LOADPROFILE " + JSON.stringify(report));
  await testInfo.attach("load-profile.json", {
    body: JSON.stringify(report, null, 2),
    contentType: "application/json",
  });

  // The only assertion: the flood actually happened. Without it a green run
  // could mean "the producer never started", and a measurement of nothing is
  // worse than no measurement — it looks like an answer.
  expect(report.linesPerSec, "the producer never reached flood rate").toBeGreaterThan(1_000);
});
