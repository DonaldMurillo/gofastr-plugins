// User-journey e2e for the chart plugin. The point of this plugin is that
// its TWO renderers agree, so the agreement journey here is the core test:
// for the same spec, the server SVG (read from the host page BEFORE
// hydration) and the hydrated Plot SVG (read from the opaque frame after
// `ready`) must produce the same axis tick labels, the same series names,
// and the same data extents. The four datasets use deliberately awkward
// ranges — exactly where a hand-rolled tick algorithm diverges from d3's.
import { test, expect, type APIRequestContext, type Page } from "@playwright/test";

const CHART = "/chart";
const SAVE = "/__gofastr/plugin/chart/save";

// The baseline demo doc: two series, title, axis captions, legend —
// everything the no-JS journey needs to see in the SSR output.
const BASELINE = {
  schemaVersion: "chart-v1",
  type: "line",
  title: "Weekly signups",
  axes: { x: { label: "week" }, y: { label: "signups" } },
  series: [
    { name: "Product", points: [{ x: 0, y: 120 }, { x: 1, y: 180 }, { x: 2, y: 165 }, { x: 3, y: 240 }, { x: 4, y: 280 }, { x: 5, y: 340 }] },
    { name: "Referral", points: [{ x: 0, y: 40 }, { x: 1, y: 55 }, { x: 2, y: 90 }, { x: 3, y: 85 }, { x: 4, y: 140 }, { x: 5, y: 190 }] },
  ],
};

// The four awkward-range agreement datasets. Each exercises the range on
// BOTH axes, and the set covers all four chart types.
const AGREEMENT_DATASETS: Array<{ name: string; spec: unknown }> = [
  {
    name: "0 to 7 (line)",
    spec: {
      schemaVersion: "chart-v1",
      type: "line",
      series: [
        { name: "alpha", points: [{ x: 0, y: 0 }, { x: 1.5, y: 1.2 }, { x: 3, y: 2.8 }, { x: 4.5, y: 4.9 }, { x: 6, y: 6.1 }, { x: 7, y: 7 }] },
        { name: "beta", points: [{ x: 0, y: 7 }, { x: 2, y: 5.5 }, { x: 3.5, y: 3.5 }, { x: 5, y: 2 }, { x: 7, y: 0 }] },
      ],
    },
  },
  {
    name: "0 to 1 (bar)",
    spec: {
      schemaVersion: "chart-v1",
      type: "bar",
      series: [
        { name: "rate", points: [{ x: 0, y: 0.05 }, { x: 0.2, y: 0.3 }, { x: 0.4, y: 0.55 }, { x: 0.6, y: 0.42 }, { x: 0.8, y: 0.8 }, { x: 1, y: 1 }] },
        { name: "baseline", points: [{ x: 0, y: 0.5 }, { x: 0.25, y: 0.5 }, { x: 0.5, y: 0.5 }, { x: 0.75, y: 0.5 }, { x: 1, y: 0.5 }] },
      ],
    },
  },
  {
    name: "-3.5 to 3.5 (area)",
    spec: {
      schemaVersion: "chart-v1",
      type: "area",
      title: "Drift",
      series: [
        { name: "delta", points: [{ x: -3.5, y: -3.5 }, { x: -2, y: -1 }, { x: -1, y: 0.5 }, { x: 0, y: 2.2 }, { x: 1, y: -0.4 }, { x: 2, y: 3.5 }, { x: 3.5, y: 1 }] },
      ],
    },
  },
  {
    name: "0 to 1,000,000 (scatter)",
    spec: {
      schemaVersion: "chart-v1",
      type: "scatter",
      series: [
        { name: "rps", points: [{ x: 0, y: 0 }, { x: 250000, y: 120000 }, { x: 500000, y: 480000 }, { x: 750000, y: 760000 }, { x: 1000000, y: 1000000 }] },
        { name: "errors", points: [{ x: 0, y: 0 }, { x: 250000, y: 9000 }, { x: 500000, y: 41000 }, { x: 750000, y: 300000 }, { x: 1000000, y: 640000 }] },
      ],
    },
  },
];

async function ready(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __chartReady?: boolean }) | null;
      return !!f && f.__chartReady === true;
    },
    undefined,
    { timeout: 15_000 }
  );
}

async function resetBaseline(request: APIRequestContext, baseURL: string | undefined): Promise<void> {
  const resp = await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", doc: BASELINE, schemaVersion: "chart-v1" },
  });
  expect(resp.ok()).toBeTruthy();
}

// --- extraction: both renderers, identical shapes ------------------------

interface ChartFacts {
  xTicks: string[];
  yTicks: string[];
  series: string[];
  domainX: [number, number];
  domainY: [number, number];
}

async function ssrFacts(page: Page): Promise<ChartFacts> {
  return page.evaluate(() => {
    const svg = document.querySelector(".gofastr-chart-ssr svg");
    if (!svg) throw new Error("no SSR svg on the page");
    const ticks = (axis: string) =>
      Array.from(svg.querySelectorAll(`[data-axis="${axis}"] .gofastr-chart-tick-label`)).map((t) => t.textContent ?? "");
    const dom = (attr: string): [number, number] => {
      const [a, b] = (svg.getAttribute(attr) ?? "").split(",").map(Number);
      return [a, b];
    };
    return {
      xTicks: ticks("x"),
      yTicks: ticks("y"),
      series: Array.from(svg.querySelectorAll("[data-series]")).map((g) => g.getAttribute("data-series") ?? ""),
      domainX: dom("data-domain-x"),
      domainY: dom("data-domain-y"),
    } as ChartFacts;
  });
}

// Reads inside the opaque frame via Playwright's frameLocator (the parent
// page cannot reach in; Playwright drives the frame directly).
async function frameFacts(page: Page): Promise<ChartFacts> {
  const fl = page.frameLocator("iframe");
  await fl.locator("svg.gofastr-plot").waitFor({ timeout: 10_000 });
  return fl.locator("#chart-root").evaluate((root) => {
    const svg = root.querySelector("svg.gofastr-plot");
    if (!svg) throw new Error("no hydrated svg in frame");
    const tickTexts = (sel: string) =>
      Array.from(svg.querySelectorAll(sel)).map((t) => t.textContent ?? "");
    const dom = (attr: string): [number, number] => {
      const [a, b] = (svg.getAttribute(attr) ?? "").split(",").map(Number);
      return [a, b];
    };
    return {
      xTicks: tickTexts('g[aria-label="x-axis tick label"] text'),
      yTicks: tickTexts('g[aria-label="y-axis tick label"] text'),
      series: Array.from(root.querySelectorAll(".gofastr-plot-swatch")).map((s) => (s.textContent ?? "").trim()),
      domainX: dom("data-domain-x"),
      domainY: dom("data-domain-y"),
    } as ChartFacts;
  });
}

// --- journeys -------------------------------------------------------------

// The hydrated journeys share one reset + mount. The no-JS describe below
// deliberately does NOT use this hook: `ready` can never fire there.
test.describe("hydrated", () => {
  test.beforeEach(async ({ page, request, baseURL }) => {
    await resetBaseline(request, baseURL);
    await page.goto(CHART);
    await ready(page);
  });

  test("mounts sandboxed with no console errors and renders both renderers", async ({ page }) => {
    const errors: string[] = [];
    page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
    page.on("pageerror", (e) => errors.push(String(e)));

    // The frame is the opaque sandbox: allow-scripts ONLY.
    const sandbox = await page.locator("iframe").getAttribute("sandbox");
    expect(sandbox).toBe("allow-scripts");
    await expect(page.frameLocator("iframe").locator("svg.gofastr-plot")).toBeVisible({ timeout: 10_000 });

    // The SSR node handed off: hidden once the interactive chart is up.
    await expect(page.locator(".gofastr-chart-ssr")).toBeHidden();

    expect(errors.filter((e) => !/favicon/i.test(e))).toEqual([]);
  });

  test("server and hydrated renderers agree on ticks, series and extents (the point of the plugin)", async ({ page, request, baseURL }) => {
    test.setTimeout(120_000);
    const results: Array<{ name: string; firstTry: boolean; ssr?: ChartFacts; frame?: ChartFacts }> = [];

    for (const ds of AGREEMENT_DATASETS) {
      // Persist the dataset, then load the page with the FRAME'S document
      // request held at the network layer, so the SSR svg is provably read
      // BEFORE hydration: the iframe element is client-side created by the
      // broker, so its document request fires only after host JS boots.
      const saved = await request.post(`${baseURL}${SAVE}`, {
        data: { docId: "demo", doc: ds.spec, schemaVersion: "chart-v1" },
      });
      expect(saved.ok()).toBeTruthy();

      // releaseHeld is assigned synchronously by the Promise executor, but
      // TS cannot see through callbacks, so it is declared (not initialized)
      // and called optionally.
      let releaseHeld: (() => void) | undefined;
      let resolveIntercepted!: () => void;
      const intercepted = new Promise<void>((r) => (resolveIntercepted = r));
      const held = new Promise<void>((r) => (releaseHeld = r));
      await page.route(/__gofastr\/plugin\/chart\/chart\.html/, async (route) => {
        resolveIntercepted();
        await held; // hold the frame document until the SSR facts are read
        await route.continue();
      });
      try {
        // domcontentloaded: the `load` event waits for the HELD iframe
        // request forever — that deadlock is why this used to time out.
        await page.goto(CHART, { waitUntil: "domcontentloaded" });
        await intercepted; // the frame document request is now HELD
        const ssr = await ssrFacts(page); // pre-hydration by construction
        releaseHeld?.();
        await ready(page);
        const frame = await frameFacts(page);

        expect(frame.xTicks, `${ds.name}: x tick labels`).toEqual(ssr.xTicks);
        expect(frame.yTicks, `${ds.name}: y tick labels`).toEqual(ssr.yTicks);
        expect(frame.series, `${ds.name}: series names`).toEqual(ssr.series);
        expect(frame.domainX, `${ds.name}: x extents`).toEqual(ssr.domainX);
        expect(frame.domainY, `${ds.name}: y extents`).toEqual(ssr.domainY);
        results.push({ name: ds.name, firstTry: true, ssr, frame });
      } catch (err) {
        results.push({ name: ds.name, firstTry: false });
        throw err;
      } finally {
        releaseHeld?.(); // never leave the route blocked on failure
        await page.unroute(/__gofastr\/plugin\/chart\/chart\.html/);
      }
    }
    // Every dataset must have produced real labels on both sides.
    for (const r of results) {
      expect(r.ssr?.xTicks.length ?? 0).toBeGreaterThan(0);
      expect(r.frame?.yTicks.length ?? 0).toBeGreaterThan(0);
    }
  });

  test("hovering a point shows a tooltip (the hydration adds interaction)", async ({ page }) => {
    const fl = page.frameLocator("iframe");
    const svg = fl.locator("svg.gofastr-plot");
    await svg.waitFor({ timeout: 10_000 });

    // Hover the first series mark: real pointer events with real
    // coordinates (Plot's pointer interaction hit-tests by position).
    const dot = fl.locator("svg.gofastr-plot circle").first();
    await dot.hover({ force: true });
    const tip = fl.locator('svg.gofastr-plot g[aria-label="tip"] text');
    await expect(tip.first()).toBeVisible({ timeout: 5_000 });
  });

  test("editing the spec through the demo persists and re-renders both renderers", async ({ page }) => {
    const scatter = {
      schemaVersion: "chart-v1",
      type: "scatter",
      title: "Applied",
      series: [{ name: "solo", points: [{ x: 0, y: 0 }, { x: 1, y: 5 }, { x: 2, y: 3 }, { x: 3, y: 9 }] }],
    };
    // The editor lives in a collapsed <details>: open it first.
    await page.locator("details > summary").click();
    await page.locator("#chart-spec").fill(JSON.stringify(scatter));
    // Apply POSTs, then reloads. waitForURL(/\/chart$/) would match the
    // CURRENT url and synchronize nothing; instead poll for the reloaded
    // page's SSR content (waitForFunction re-injects across navigations).
    await page.locator("#apply-spec").click();
    await page.waitForFunction(
      () =>
        document.querySelector(".gofastr-chart-ssr [data-series]")?.getAttribute("data-series") ===
        "solo",
      undefined,
      { timeout: 15_000 }
    );
    await ready(page);

    const ssr = await ssrFacts(page);
    expect(ssr.series).toEqual(["solo"]);
    // Frame side agrees.
    const frame = await frameFacts(page);
    expect(frame.series).toEqual(["solo"]);
    expect(frame.xTicks).toEqual(ssr.xTicks);
  });
});

// The no-JS journey: JavaScript disabled at the context level. The broker
// never runs, the iframe is never created, and the SSR svg IS the page —
// with axes and a legend.
test.describe("no JavaScript", () => {
  test.use({ javaScriptEnabled: false });

  test("the server-rendered chart is the page: axes, ticks, legend", async ({ page, request, baseURL }) => {
    await resetBaseline(request, baseURL);
    await page.goto(CHART);

    const ssr = page.locator(".gofastr-chart-ssr");
    await expect(ssr).toBeVisible();
    await expect(ssr.locator("svg")).toBeVisible();

    // Axes with tick labels on both axes…
    const xTicks = ssr.locator('[data-axis="x"] .gofastr-chart-tick-label');
    const yTicks = ssr.locator('[data-axis="y"] .gofastr-chart-tick-label');
    await expect(xTicks.first()).toBeVisible();
    await expect(yTicks.first()).toBeVisible();
    expect(await xTicks.count()).toBeGreaterThan(3);
    expect(await yTicks.count()).toBeGreaterThan(3);

    // …axis captions…
    await expect(ssr.locator(".gofastr-chart-axis-label").first()).toBeVisible();

    // …and a legend with both series (DOM order = series order).
    const legend = ssr.locator("[data-legend-item]");
    await expect(legend).toHaveCount(2);
    await expect(legend.first()).toHaveAttribute("data-legend-name", "Product");

    // No iframe materialized (scripts never ran) and the wrapper was never
    // hidden — the handoff only happens client-side.
    await expect(page.locator("iframe")).toHaveCount(0);
    await expect(ssr).not.toHaveAttribute("hidden", "");
  });
});
