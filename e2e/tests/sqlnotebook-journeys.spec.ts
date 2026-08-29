// User journeys for sqlnotebook: a real SQLite engine, compiled to
// WebAssembly, running inside an opaque-origin frame that cannot open a
// socket.
//
// This plugin exists to answer a platform question, so these journeys assert
// the answer rather than the widget. The interesting properties are all
// negative — the frame cannot fetch, cannot eval, cannot reach the host — and
// a test that only drove the UI would pass on a plugin that had quietly given
// all of that away.
import { test, expect, type Page, type FrameLocator } from "@playwright/test";

type Debug = {
  initSent: number;
  results: number;
  errors: number;
  queries: number;
  ready: { sqliteVersion: string; ms: number } | null;
  wasmBytes: number;
};

const readDebug = (page: Page) =>
  page.evaluate(() => {
    const g = (window as unknown as { __sqlnbDebug?: Debug }).__sqlnbDebug;
    if (!g) return null;
    return {
      initSent: g.initSent, results: g.results, errors: g.errors,
      queries: g.queries, ready: g.ready, wasmBytes: g.wasmBytes,
    };
  }) as Promise<Debug | null>;

function frame(page: Page): FrameLocator {
  return page.frameLocator("iframe");
}

/** Wait for the engine, not merely the mount: the wasm arrives after boot. */
async function ready(page: Page): Promise<Debug> {
  await expect
    .poll(async () => (await readDebug(page))?.ready?.sqliteVersion ?? null, { timeout: 30_000 })
    .not.toBeNull();
  const d = await readDebug(page);
  if (!d) throw new Error("__sqlnbDebug disappeared after reporting ready");
  return d;
}

async function run(page: Page, sql: string): Promise<void> {
  const f = frame(page);
  const editor = f.locator("textarea, [contenteditable], input[type=text]").first();
  await editor.click();
  await editor.fill(sql);
  await f.getByText("Run", { exact: false }).first().click();
}

test.beforeEach(async ({ page }) => {
  await page.goto("/sqlnotebook");
});

test("a real SQLite engine boots in the cage, from bytes the host handed in", async ({ page }) => {
  const d = await ready(page);

  // A version string is the proof that this is SQLite and not a shim: it comes
  // out of the compiled engine, not out of our code.
  expect(d.ready?.sqliteVersion).toMatch(/^3\.\d+\.\d+$/);

  // The engine crossed the BRIDGE. connect-src 'none' means the frame cannot
  // fetch its own .wasm, so a non-zero byte count here is the whole delivery
  // story: the host read the file and posted it in.
  expect(d.wasmBytes, "the wasm must arrive as bytes over the bridge").toBeGreaterThan(100_000);
  expect(d.initSent).toBe(1);
});

test("a query runs inside the frame and its rows come back", async ({ page }) => {
  await ready(page);
  await run(page, "SELECT isolation, COUNT(*) AS n FROM plugins GROUP BY isolation ORDER BY n DESC");

  await expect.poll(async () => (await readDebug(page))?.results ?? 0, { timeout: 15_000 }).toBeGreaterThan(0);
  const d = await readDebug(page);
  expect(d?.errors, "a valid query must not report an error").toBe(0);

  // Assert the ANSWER, not just that a table appeared: an empty table would
  // satisfy a selector check while proving the engine did nothing.
  const table = frame(page).locator("table").first();
  await expect(table).toContainText("sandbox-iframe-opaque");
  await expect(table.locator("tr")).not.toHaveCount(0);
});

test("a broken query reports an error and keeps the last good result on screen", async ({ page }) => {
  await ready(page);
  await run(page, "SELECT name FROM plugins");
  await expect.poll(async () => (await readDebug(page))?.results ?? 0, { timeout: 15_000 }).toBeGreaterThan(0);

  await run(page, "SELECT * FROM no_such_table");
  await expect.poll(async () => (await readDebug(page))?.errors ?? 0, { timeout: 15_000 }).toBeGreaterThan(0);

  // The previous result must survive. A notebook that blanks its output on a
  // typo makes you re-run the query you already answered.
  await expect(frame(page).locator("table").first()).toContainText(/\w/);
});

test("the schema sidebar tracks DDL, not just the seed", async ({ page }) => {
  await ready(page);
  const tables = frame(page).locator(".sqlnb-tables");
  await expect(tables).toContainText("plugins");
  await expect(tables).not.toContainText("notes");

  await run(page, "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)");
  await expect.poll(async () => (await readDebug(page))?.results ?? 0, { timeout: 15_000 }).toBeGreaterThan(0);

  // The sidebar is this notebook's only map of the database, and DDL is
  // ordinary in one. refreshSchema used to run once at boot and never again,
  // so a created table stayed invisible for the rest of the session.
  await expect(tables).toContainText("notes");
  await expect(tables).toContainText("body");

  await run(page, "DROP TABLE notes");
  await expect(tables).not.toContainText("notes");
  await expect(tables, "dropping one table must not blank the rest").toContainText("plugins");
});

test("the frame is sealed: opaque origin, and the tier grants only wasm", async ({ page, request, baseURL }) => {
  await ready(page);

  // ── the origin half, probed inside the frame ──────────────────────────────
  //
  // These are ORIGIN restrictions, not CSP ones, so the browser enforces them
  // whatever context the code runs in. They are faithful from here.
  const sealed = await frame(page).locator("body").evaluate(() => {
    const probe = (fn: () => unknown) => {
      try { fn(); return "ALLOWED"; } catch (e) { return (e as Error).name; }
    };
    return {
      storage: probe(() => localStorage.length),
      cookie: probe(() => document.cookie),
      parentDom: probe(() => (window.parent as unknown as { document: unknown }).document),
      wasm: typeof WebAssembly.Module,
    };
  });

  expect(sealed.storage, "an opaque origin has no storage").not.toBe("ALLOWED");
  expect(sealed.cookie, "an opaque origin has no cookies").not.toBe("ALLOWED");
  expect(sealed.parentDom, "the frame must not reach the host DOM").not.toBe("ALLOWED");
  expect(sealed.wasm, "the tier must actually be in effect").toBe("function");

  // ── the CSP half, asserted on the SERVED HEADER ───────────────────────────
  //
  // Do NOT probe eval() or new Function() from inside the frame here. Playwright's
  // evaluate runs in a context the page's CSP does not apply to, so BOTH come back
  // "ALLOWED" — including in the pdf frame, which has no CSP tier at all. That
  // measures the harness, not the plugin, and an earlier version of this test
  // failed for exactly that reason. The response header is the real contract and
  // is faithful from out here.
  const res = await request.get(new URL("/__gofastr/plugin/sqlnotebook/frame.html", baseURL).toString());
  expect(res.status()).toBe(200);
  const csp = res.headers()["content-security-policy"] ?? "";
  expect(csp, "the frame must be served a CSP at all").not.toBe("");

  const scriptSrc = csp.split(";").map((d) => d.trim()).find((d) => d.startsWith("script-src ")) ?? "";
  const tokens = scriptSrc.replace("script-src ", "").split(/\s+/);

  expect(tokens, "the wasm tier must reach the frame (gofastr#300)").toContain("'wasm-unsafe-eval'");
  expect(tokens, "string eval must never be granted").not.toContain("'unsafe-eval'");
  expect(csp, "the frame must keep its network sealed").toContain("connect-src 'none'");
});

test("the frame issues zero network requests while running a query", async ({ page }) => {
  const framed: string[] = [];
  page.on("request", (r) => {
    const f = r.frame();
    if (f && f !== page.mainFrame() && f.url().includes("frame.html")) framed.push(r.url());
  });

  await ready(page);
  const afterBoot = framed.length;
  await run(page, "SELECT COUNT(*) FROM plugins");
  await expect.poll(async () => (await readDebug(page))?.results ?? 0, { timeout: 15_000 }).toBeGreaterThan(0);

  // Running SQL must cost nothing on the wire. The engine and the data are
  // already inside; results leave over postMessage, not over HTTP.
  expect(framed.length, `frame requested: ${framed.slice(afterBoot).join(", ")}`).toBe(afterBoot);
});
