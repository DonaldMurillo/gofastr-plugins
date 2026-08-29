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
import { mkdirSync } from "node:fs";

const SHOTS = process.env.SHOTS === "1";
const DIR = "shots";

test.skip(!SHOTS, "screenshot capture is opt-in: SHOTS=1 npm run shots");
test.describe.configure({ mode: "serial" });

test.beforeAll(() => mkdirSync(DIR, { recursive: true }));

/**
 * The page list comes from the running gallery, not a table here.
 *
 * A hand-kept list is the thing that already went stale once: the scanner
 * shipped fully wired and missing from the gallery, and nothing noticed.
 * Go's TestGalleryListsEveryShippedPlugin makes the gallery cover
 * plugins.json, so scraping the gallery inherits that coverage — a plugin
 * cannot ship without appearing here.
 */
async function demoRoutes(page: Page): Promise<{ slug: string; path: string }[]> {
  await page.goto("/");
  const slugs = await page.locator(".nav-item").evaluateAll((els) =>
    els.map((e) => (e as HTMLElement).dataset.slug ?? "").filter(Boolean)
  );
  expect(slugs.length, "the gallery listed no plugins at all").toBeGreaterThan(5);
  return slugs.map((slug) => ({ slug, path: `/${slug}` }));
}

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

test("capture every demo page, light and dark", async ({ page }) => {
  test.setTimeout(300_000);
  await page.setViewportSize({ width: 1280, height: 900 });
  const routes = await demoRoutes(page);

  for (const { slug, path } of routes) {
    for (const scheme of ["light", "dark"] as const) {
      await page.goto(path);
      const current = await page.evaluate(() => document.documentElement.dataset.colorScheme);
      if (current !== scheme) {
        const toggle = page.locator("#fui-scheme-toggle");
        if (await toggle.count()) {
          await toggle.first().click();
          await page.waitForTimeout(500); // token re-bridge into the frame
        }
      }
      await settle(page);
      await page.screenshot({ path: `${DIR}/demo-${slug}-${scheme}.png`, fullPage: true });
    }
  }
});

test("capture every demo page at 390px, where the layout actually breaks", async ({ page }) => {
  test.setTimeout(300_000);
  // The design standard says check 390px, and it is where the real defects
  // have been: a frame height that fits a toolbar and no content, controls
  // that wrap into a column, cards that stop stacking.
  await page.setViewportSize({ width: 390, height: 844 });
  const routes = await demoRoutes(page);

  for (const { slug, path } of routes) {
    await page.goto(path);
    await settle(page);
    await page.screenshot({ path: `${DIR}/demo-${slug}-mobile.png`, fullPage: true });
  }
});
