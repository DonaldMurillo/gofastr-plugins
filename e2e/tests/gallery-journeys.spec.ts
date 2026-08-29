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
  // Fourteen plugins (richtext, mermaid, monaco, datagrid, chart, logstream,
  // imageedit, formbuilder, calendar, whiteboard, pdf, tour, map, scanner)
  // plus two recipes (blogsite, blogapp), which share the nav-item/card markup.
  //
  // The count is deliberately exact rather than a floor: the sidebar and the
  // home grid are built from ONE list, so a mismatch between them means the
  // grid dropped an entry the sidebar kept, which is invisible by eye. Go's
  // TestGalleryListsEveryShippedPlugin is what stops the list itself falling
  // behind plugins.json; this is what stops the two renderings diverging.
  await expect(page.locator(".nav-item")).toHaveCount(16);
  await expect(page.locator(".home .card")).toHaveCount(16);
  // Name them rather than only counting: a count alone passes if a card is
  // renamed or duplicated, and this is the completeness canary for the gallery.
  await expect(navItem(page, "pdf")).toBeVisible();
  await expect(navItem(page, "scanner")).toBeVisible();
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

// ─── Recipes ─────────────────────────────────────────────────────────
//
// A recipe is a whole GoFastr app on its own port, so the gallery cannot frame
// it the way it frames a plugin demo: two UIHost apps cannot share a router,
// and uihost ships frame-ancestors 'none' by default. What the gallery carries
// instead is a landing page per recipe — same-origin, so it loads in the same
// content iframe and the sidebar persists.

test("recipes have their own sidebar section, listed after the plugins", async ({ page }) => {
  await page.goto("/");

  const sections = page.locator(".nav-sec");
  await expect(sections).toHaveCount(2);
  await expect(sections.nth(0)).toHaveText("Plugins");
  await expect(sections.nth(1)).toHaveText("Recipes");

  await expect(navItem(page, "blogsite")).toBeVisible();
  await expect(navItem(page, "blogapp")).toBeVisible();
});

test("a recipe card opens its landing page in the shell, sidebar intact", async ({ page }) => {
  await page.goto("/");
  await navItem(page, "blogapp").click();

  await expect(frame(page)).toHaveClass(/show/);
  await expect(frame(page)).toHaveAttribute("data-slug", "blogapp");
  await expect(frame(page)).toHaveAttribute("src", "/recipes/blogapp");
  await expect(sidebar(page)).toBeVisible();
  await expect(navItem(page, "blogapp")).toHaveClass(/active/);

  // The landing page explains the recipe rather than running it.
  const doc = page.frameLocator("#frame");
  await expect(doc.getByRole("heading", { level: 1, name: "Authored blog" })).toBeVisible();
  await expect(doc.getByText("go run ./recipes/blogapp")).toBeVisible();
  // The page must carry the warning that the plugin's permission check does
  // not authenticate the caller. Asserted on the body text rather than the
  // heading: the headline wording has changed twice, but a page that still
  // makes this point will always say "anonymous" somewhere in it.
  await expect(doc.getByText(/anonymous request passes it/i)).toBeVisible();
});

test("each landing page links to its implementation on GitHub", async ({ page }) => {
  for (const [slug, title] of [
    ["blogsite", "Markdown blog"],
    ["blogapp", "Authored blog"],
  ] as const) {
    await page.goto(`/#/${slug}`);
    const doc = page.frameLocator("#frame");
    await expect(doc.getByRole("heading", { level: 1, name: title })).toBeVisible();

    const source = doc.getByRole("link", { name: /Source on GitHub/ });
    await expect(source).toHaveAttribute(
      "href",
      `https://github.com/DonaldMurillo/gofastr-plugins/tree/main/recipes/${slug}`
    );
    // Off-site links must not hand the opener over.
    await expect(source).toHaveAttribute("rel", /noopener/);
    await expect(source).toHaveAttribute("target", "_blank");

    // The "where to start reading" list points into that recipe's own
    // directory. Which files it names is the page's business — asserting a
    // specific one only pins the editorial choice, not the behaviour.
    const files = doc.locator("ul a");
    await expect(files.first()).toBeVisible();
    for (const href of await files.evaluateAll((els) => els.map((e) => e.getAttribute("href")))) {
      expect(href).toContain(`/recipes/${slug}/`);
    }
  }
});

test("deep link opens a recipe landing page directly", async ({ page }) => {
  await page.goto("/#/blogsite");
  await expect(frame(page)).toHaveAttribute("data-slug", "blogsite");
  await expect(page.frameLocator("#frame").getByRole("heading", { level: 1, name: "Markdown blog" })).toBeVisible();
});
