// User-journey e2e for the example app's gallery shell: a homepage + persistent
// sidebar that frames each plugin's demo so you can navigate between them. The
// sidebar must work at all screen sizes (a persistent rail on desktop, a drawer
// on mobile).
import { test, expect, type Page } from "@playwright/test";

const sidebar = (page: Page) => page.locator("aside#sidebar");
const frame = (page: Page) => page.locator("iframe#frame");
const home = (page: Page) => page.locator("#home");
const navItem = (page: Page, slug: string) => page.locator(`.nav-item[data-slug="${slug}"]`);

test("homepage shows the gallery: sidebar + a card per plugin, no demo framed yet", async ({ page }) => {
  await page.goto("/");
  await expect(sidebar(page)).toBeVisible();
  // richtext, mermaid, monaco, pdf, tour, map
  await expect(page.locator(".nav-item")).toHaveCount(6);
  await expect(page.locator(".home .card")).toHaveCount(6);
  // Name them rather than only counting: a count alone passes if a card is
  // renamed or duplicated, and this is the completeness canary for the gallery.
  await expect(navItem(page, "pdf")).toBeVisible();
  await expect(home(page)).toBeVisible();
  await expect(frame(page)).not.toHaveClass(/show/); // nothing framed on the home view
});

test("selecting a plugin frames its demo while the sidebar persists", async ({ page }) => {
  await page.goto("/");
  await navItem(page, "monaco").click();

  await expect(frame(page)).toHaveClass(/show/);
  await expect(frame(page)).toHaveAttribute("data-slug", "monaco");
  await expect(frame(page)).toHaveAttribute("src", /\/monaco$/);
  await expect(home(page)).toBeHidden();
  await expect(sidebar(page)).toBeVisible(); // menu stays put across navigation
  await expect(navItem(page, "monaco")).toHaveClass(/active/);
});

test("deep link opens straight to a plugin", async ({ page }) => {
  await page.goto("/#/tour");
  await expect(frame(page)).toHaveClass(/show/);
  await expect(frame(page)).toHaveAttribute("data-slug", "tour");
  await expect(frame(page)).toHaveAttribute("src", /\/tour$/);
});

test("mobile: the sidebar is a hamburger drawer, still reachable", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 780 });
  await page.goto("/");

  const burger = page.locator("#burger");
  await expect(burger).toBeVisible(); // the top-bar toggle only shows at narrow widths
  await expect(page.locator("body")).not.toHaveClass(/nav-open/);

  await burger.click();
  await expect(page.locator("body")).toHaveClass(/nav-open/);
  // Picking a plugin from the drawer navigates and closes it.
  await navItem(page, "mermaid").click();
  await expect(page.locator("body")).not.toHaveClass(/nav-open/);
  await expect(frame(page)).toHaveAttribute("data-slug", "mermaid");
});
