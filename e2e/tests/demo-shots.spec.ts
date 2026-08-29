// Every demo page, both themes, captured for human review.
//
// docs/demo-page-design.md opens with "look at all four. Not the DOM, the
// pixels", and every visual defect this repo has shipped was found that way and
// by nothing else: a pdf mount rendered inside its own fact-chip row, a scanner
// whose phone viewport showed toolbars and not one line of document, a result
// line duplicated between frame and page. Every one of those passed the whole
// test suite.
//
// So this captures the pixels on every run and hands them to CI as an artifact.
// It cannot judge them — nothing here asserts a design — but it removes the
// excuse that looking was inconvenient.
//
// Opt-in like the editor shots: SHOTS=1 npm run shots. Output is gitignored.
import { test, expect, type Page } from "@playwright/test";
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const SHOTS = process.env.SHOTS === "1";
const DIR = "shots";

test.skip(!SHOTS, "screenshot capture is opt-in: SHOTS=1 npm run shots");
test.describe.configure({ mode: "serial" });

test.beforeAll(() => mkdirSync(DIR, { recursive: true }));

/**
 * One test per page, from plugins.json plus the two recipes.
 *
 * Generated statically rather than discovered inside one big test, because a
 * single test capturing seventeen pages is a single timeout and a single
 * retry: on CI the batch blew a five-minute budget and Playwright then redid
 * all seventeen. Per page, a failure names the page and the retry costs one
 * page.
 *
 * plugins.json is the source of truth for what ships, and Go's
 * TestGalleryListsEveryShippedPlugin already ties the gallery to it, so a
 * plugin cannot ship uncaptured. The coverage test below closes the other
 * direction: a page in the gallery that is not in this list fails.
 */
const RECIPES = ["blogsite", "blogapp"];
const PLUGIN_SLUGS: string[] = (
  JSON.parse(readFileSync(join(__dirname, "..", "..", "plugins.json"), "utf8")) as {
    plugins: { name: string }[];
  }
).plugins.map((p) => p.name);
const PAGES = [...PLUGIN_SLUGS, ...RECIPES];

/**
 * Wait for the plugin to be up, generically.
 *
 * The broker sets __pluginReady on every sandboxed frame it mounts, so this
 * needs no per-plugin knowledge and cannot fall behind a new plugin's private
 * readiness mirror. Trusted host-page plugins have no frame at all, so a page
 * that never grows one is given a fixed settle instead of being failed — this
 * is a capture, and a blank shot of a broken page is itself the finding.
 */
async function settle(page: Page): Promise<void> {
  await page
    .waitForFunction(
      () => {
        const f = document.querySelector("iframe") as (HTMLIFrameElement & { __pluginReady?: boolean }) | null;
        return !f || f.__pluginReady === true;
      },
      undefined,
      { timeout: 20_000 }
    )
    .catch(() => {
      /* captured as-is; see above */
    });
  await page.waitForTimeout(900);
}

async function shoot(page: Page, slug: string, scheme: "light" | "dark"): Promise<void> {
  await page.goto(`/${slug}`);
  const current = await page.evaluate(() => document.documentElement.dataset.colorScheme);
  if (current !== scheme) {
    // Best effort, on purpose. The tour plugin lays a spotlight overlay across
    // the page, so its toggle is not clickable while the tour runs — and a
    // capture job that fails because one page's control is obstructed captures
    // nothing, which is worse than capturing that page in one scheme. Fall
    // back to setting the attribute directly; if even that does not take, the
    // shot is still taken and the reader sees what is there.
    const toggle = page.locator("#fui-scheme-toggle");
    if (await toggle.count()) {
      await toggle
        .first()
        .click({ timeout: 5_000 })
        .catch(async () => {
          await page.evaluate((s) => {
            document.documentElement.dataset.colorScheme = s;
          }, scheme);
        });
      await page.waitForTimeout(500); // token re-bridge into the frame
    }
  }
  await settle(page);
  await page.screenshot({ path: `${DIR}/demo-${slug}-${scheme}.png`, fullPage: true });
}

for (const slug of PAGES) {
  test(`shots: ${slug}`, async ({ page }) => {
    test.setTimeout(120_000);
    await page.setViewportSize({ width: 1280, height: 900 });
    await shoot(page, slug, "light");
    await shoot(page, slug, "dark");

    // 390px, where the real defects have been: a frame height that fits a
    // toolbar and no content, controls that wrap into a column, cards that
    // stop stacking. The design standard names this width specifically.
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`/${slug}`);
    await settle(page);
    await page.screenshot({ path: `${DIR}/demo-${slug}-mobile.png`, fullPage: true });
  });
}

// The other direction: a page reachable from the gallery that nothing above
// captures. plugins.json covers the plugins; this catches anything else the
// gallery grows.
test("every gallery entry is captured", async ({ page }) => {
  await page.goto("/");
  const slugs = await page.locator(".nav-item").evaluateAll((els) =>
    els.map((e) => (e as HTMLElement).dataset.slug ?? "").filter(Boolean)
  );
  expect(slugs.length, "the gallery listed no plugins at all").toBeGreaterThan(5);
  const missing = slugs.filter((s) => !PAGES.includes(s));
  expect(missing, `the gallery links pages that no shot covers: ${missing.join(", ")}`).toEqual([]);
});
