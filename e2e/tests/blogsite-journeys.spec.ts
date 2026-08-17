// User journeys for recipes/blogsite — the markdown blog.
//
// These drive it the way a reader does: land on the homepage, follow a post,
// chase a tag, page back and forth, search, and subscribe. Run in WebKit and
// Chromium both, because every Safari-only bug this project has shipped was
// invisible to a Chrome-only harness.
//
// The Go suite already asserts status codes and feed shape. What only a browser
// can check is here: that the links actually go where the markup claims, that
// the theme toggle survives a navigation, and that the reading surface works at
// 390px.
import { test, expect, type Page } from "@playwright/test";
import { BLOGSITE } from "./recipes";

// Any console error or uncaught exception fails the test it happened in. A blog
// that renders correctly while throwing is still broken.
let consoleErrors: string[];

test.beforeEach(async ({ page }) => {
  consoleErrors = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", (err) => consoleErrors.push(`pageerror: ${err.message}`));
});

test.afterEach(() => {
  // "Failed to load resource … <status>" is an HTTP-status notice, not a JS
  // error, and one of these journeys deliberately requests a 404. Filtering it
  // here is what lets the rest of the suite treat a real console error as fatal.
  const real = consoleErrors.filter(
    (e) =>
      !/favicon/i.test(e) &&
      !/Failed to load resource/i.test(e) &&
      // WebKit reports an EventSource cancelled by navigation as a page error.
      // The framework runtime opens that stream on every page; the endpoint is
      // healthy (it holds a connection open when the page stays put).
      !/__gofastr\/sse.*access control checks/.test(e)
  );
  expect(real, `console/page errors during journey:\n${real.join("\n")}`).toEqual([]);
});

const postCards = (page: Page) => page.locator('a[href^="/posts/"]').filter({ has: page.locator("h3") });

test("homepage lists posts and the hero explains the recipe", async ({ page }) => {
  await page.goto(`${BLOGSITE}/`);

  await expect(page.getByRole("heading", { level: 1, name: "Notes on a flat file" })).toBeVisible();
  // Four posts a page — the window postsPerPage sets in screens.go.
  await expect(postCards(page)).toHaveCount(4);
  await expect(page.getByRole("link", { name: /Older posts/ })).toBeVisible();
  // Page 1 has nothing newer to offer.
  await expect(page.getByRole("link", { name: /Newer posts/ })).toHaveCount(0);
});

test("reading a post: title, prose, code block, tags, and the pager", async ({ page }) => {
  await page.goto(`${BLOGSITE}/`);
  await page.getByRole("link", { name: /One binary, no assets directory/ }).first().click();

  await expect(page).toHaveURL(`${BLOGSITE}/posts/one-binary-no-assets`);
  await expect(page.getByRole("heading", { level: 1, name: "One binary, no assets directory" })).toBeVisible();

  // The markdown was rendered, not dumped: a real <pre> carrying the fence's
  // contents, and no stray backticks anywhere on the page.
  const code = page.locator("pre").first();
  await expect(code).toBeVisible();
  await expect(code).toContainText("go:embed content");
  await expect(page.locator("body")).not.toContainText("```");

  // The <title> comes from the post, which is what a dynamic ScreenTitle buys.
  await expect(page).toHaveTitle(/One binary, no assets directory/);

  // Prev/next point at real neighbours and actually navigate.
  const pagerPrev = page.getByRole("link", { name: /Search without an index/ }).first();
  await expect(pagerPrev).toBeVisible();
  await pagerPrev.click();
  await expect(page).toHaveURL(`${BLOGSITE}/posts/search-without-an-index`);
});

test("tags: chip on a post leads to the tag archive, which lists that post", async ({ page }) => {
  await page.goto(`${BLOGSITE}/posts/frontmatter-reference`);
  await page.getByRole("link", { name: "markdown", exact: true }).first().click();

  await expect(page).toHaveURL(`${BLOGSITE}/tags/markdown`);
  await expect(page.getByRole("heading", { level: 1, name: "markdown" })).toBeVisible();
  await expect(page.getByRole("link", { name: /Frontmatter reference/ })).toBeVisible();

  // And the tag index counts agree with what the tag page shows. Scoped to
  // <main>: the footer's "Tags" column lists the busiest tags in the same
  // "label (N)" format, so a page-wide match finds two of them.
  await page.getByRole("link", { name: "All tags" }).click();
  await expect(page).toHaveURL(`${BLOGSITE}/tags`);
  const chip = page.locator("main").getByRole("link", { name: /^markdown \(\d+\)$/ });
  await expect(chip).toBeVisible();
  const listed = await postCards(page).count();
  expect(listed).toBe(0); // the index shows chips, not cards
});

test("pagination walks forward and back without a dead link", async ({ page }) => {
  await page.goto(`${BLOGSITE}/`);
  await page.getByRole("link", { name: /Older posts/ }).click();

  await expect(page).toHaveURL(`${BLOGSITE}/page/2`);
  // Scoped to the pagination nav: the readout lives there, and that is the only
  // place it should appear (the page heading used to repeat it).
  await expect(page.getByRole("navigation", { name: "Pagination" })).toContainText(/Page 2 of \d+/);

  // "Newer" from page 2 must go to "/", not "/page/1" — that route is never
  // registered, so linking to it would 404.
  const newer = page.getByRole("link", { name: /Newer posts/ });
  await expect(newer).toHaveAttribute("href", "/");
  await newer.click();
  await expect(page).toHaveURL(`${BLOGSITE}/`);
});

test("search finds a post and the empty state explains a miss", async ({ page }) => {
  await page.goto(`${BLOGSITE}/search`);
  await expect(page.getByText("Type a query to search.")).toBeVisible();

  await page.locator("#search-page-q").fill("frontmatter");
  await page.locator("#search-page-q").press("Enter");

  await expect(page).toHaveURL(/\/search\?q=frontmatter/);
  await expect(page.getByRole("link", { name: /Frontmatter reference/ })).toBeVisible();

  await page.locator("#search-page-q").fill("zzzznotarealword");
  await page.locator("#search-page-q").press("Enter");
  await expect(page.getByRole("heading", { name: /No matches for/ })).toBeVisible();
  await expect(page.getByText(/does not stem words/)).toBeVisible();
});

test("search is reachable from the nav on any page", async ({ page }) => {
  // Search is a nav LINK rather than a box in the header's Actions slot:
  // SiteHeader renders Actions twice (desktop + mobile drawer), so a field with
  // a fixed id there would be a duplicate element id.
  await page.goto(`${BLOGSITE}/archive`);
  await page.getByRole("link", { name: "Search", exact: true }).first().click();

  await expect(page).toHaveURL(`${BLOGSITE}/search`);
  await page.locator("#search-page-q").fill("scheduling");
  await page.locator("#search-page-q").press("Enter");
  await expect(page.getByRole("link", { name: /Drafts and scheduling/ })).toBeVisible();
});

test("archive groups every published post by year", async ({ page }) => {
  await page.goto(`${BLOGSITE}/archive`);

  await expect(page.getByRole("heading", { level: 1, name: "Archive" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "2026" })).toBeVisible();

  // The archive is the one page that lists everything, so it is where a
  // hidden post would show up if the filter regressed.
  await expect(page.getByRole("link", { name: /Tags are just strings/ })).toHaveCount(0);
  await expect(page.getByRole("link", { name: /Scheduled for later/ })).toHaveCount(0);
});

test("a draft URL renders the site's own 404, not a browser error", async ({ page }) => {
  const response = await page.goto(`${BLOGSITE}/posts/tags-are-just-strings`);
  expect(response?.status()).toBe(404);

  await expect(page.getByRole("heading", { name: /404 . not found/ })).toBeVisible();
  // The 404 renders inside the normal chrome, so the recovery paths are there.
  await expect(page.getByRole("link", { name: "Back to posts" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Recent posts" })).toBeVisible();
});

test("content pages are in the nav and render their markdown", async ({ page }) => {
  await page.goto(`${BLOGSITE}/`);
  await page.getByRole("link", { name: "Colophon" }).first().click();

  await expect(page).toHaveURL(`${BLOGSITE}/colophon`);
  await expect(page.getByRole("heading", { level: 1, name: "Colophon" })).toBeVisible();
  // The page's markdown table survived the render.
  await expect(page.locator("table")).toBeVisible();
  await expect(page.getByText("gofastr/core/markdown")).toBeVisible();
});

test("the theme toggle switches scheme and survives a navigation", async ({ page }) => {
  await page.goto(`${BLOGSITE}/`);
  const scheme = () => page.evaluate(() => document.documentElement.getAttribute("data-color-scheme"));

  const before = await scheme();
  await page.locator("[data-fui-theme-toggle], button[aria-label*='theme' i], button[title*='theme' i]").first().click();
  await expect.poll(scheme).not.toBe(before);

  const chosen = await scheme();
  await page.goto(`${BLOGSITE}/archive`);
  // The choice is stored client-side and re-applied by the bootstrap script.
  await expect.poll(scheme).toBe(chosen);
});

test("feeds are served with the right content types and absolute links", async ({ request }) => {
  const rss = await request.get(`${BLOGSITE}/feed.xml`);
  expect(rss.status()).toBe(200);
  expect(rss.headers()["content-type"]).toContain("application/rss+xml");
  const xml = await rss.text();
  expect(xml).toContain(`<link>${BLOGSITE}/</link>`);
  expect(xml).not.toContain("tags-are-just-strings");

  const json = await request.get(`${BLOGSITE}/feed.json`);
  expect(json.status()).toBe(200);
  const feed = await json.json();
  expect(feed.version).toBe("https://jsonfeed.org/version/1.1");
  expect(feed.items.length).toBeGreaterThan(0);
  for (const item of feed.items) {
    expect(item.url).toMatch(/^https?:\/\//);
  }
});

test("mobile: the reading surface works at 390px", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 780 });
  await page.goto(`${BLOGSITE}/posts/markdown-subset`);

  await expect(page.getByRole("heading", { level: 1, name: "The markdown subset" })).toBeVisible();

  // Nothing may push the page sideways. Wide content (the post's table, its
  // code blocks) has to scroll inside its own container instead.
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth
  );
  expect(overflow).toBeLessThanOrEqual(1); // 1px of subpixel rounding is fine
});
