// User journeys for recipes/blogapp — the blog whose posts are written with
// the rich text plugin.
//
// The Go suite already covers status codes, the admin gate, and the
// anonymous-save refusal. What only a browser can prove is the authoring loop
// itself: that the editor mounts inside its opaque-origin frame, that typing in
// it reaches the database over the bridge, and that a reader then gets that
// text as plain server-rendered HTML with no editor anywhere near it.
//
// WebKit and Chromium both, because the isolation boundary is exactly where
// this project's Safari-only bugs have lived.
import { test, expect, type Page } from "@playwright/test";
import { ADMIN_PASSWORD, BLOGAPP } from "./recipes";

let consoleErrors: string[];

/**
 * Extra console/page errors a single test expects. A test that deliberately
 * provokes one pushes its pattern here; everything else stays fatal.
 */
let expectedConsoleErrors: RegExp[];

test.beforeEach(async ({ page }) => {
  consoleErrors = [];
  expectedConsoleErrors = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", (err) => consoleErrors.push(`pageerror: ${err.message}`));
});

test.afterEach(() => {
  // Drop favicon noise and "Failed to load resource … <status>" lines: those
  // are HTTP-status notices rather than JS errors, and a real boot failure is
  // already caught functionally by the editorReady gate.
  const real = consoleErrors.filter(
    (e) =>
      !/favicon/i.test(e) &&
      !/Failed to load resource/i.test(e) &&
      // WebKit reports an EventSource that navigation cancelled as a page
      // error ("…/__gofastr/sse?session=… due to access control checks"). The
      // framework runtime opens that stream on every page, so a journey with
      // several navigations trips it. Verified benign: the endpoint holds a
      // connection open indefinitely when the page stays put — this is the
      // abort, not a failure to connect.
      !/__gofastr\/sse.*access control checks/.test(e) &&
      !expectedConsoleErrors.some((re) => re.test(e))
  );
  expect(real, `console/page errors during journey:\n${real.join("\n")}`).toEqual([]);
});

const editor = (page: Page) => page.frameLocator("iframe").locator(".ProseMirror");

/**
 * Waits until the plugin frame reports it has booted and loaded its document.
 *
 * Skipping this is the trap: the iframe element exists — and even paints the
 * document — well before the editor is wired to receive input, so a click and
 * keystrokes land on nothing and the test fails claiming autosave is broken.
 */
async function editorReady(page: Page) {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __richtextReady?: boolean }) | null;
      return !!f && f.__richtextReady === true;
    },
    undefined,
    { timeout: 20_000 }
  );
}

async function login(page: Page) {
  await page.goto(`${BLOGAPP}/admin/login`);
  await page.locator("#admin-password").fill(ADMIN_PASSWORD);
  await page.locator("#sign-in").click();
  await expect(page).toHaveURL(`${BLOGAPP}/admin`);
}

/** Creates a fresh draft and returns the id from the URL it lands on. */
async function newPost(page: Page): Promise<string> {
  await page.getByRole("button", { name: "New post" }).click();
  await expect(page).toHaveURL(/\/admin\/posts\/[0-9a-f]+$/);
  return page.url().split("/").pop()!;
}

/**
 * Removes a post created by a test.
 *
 * Every test that creates one must call this. The tests share a server, and a
 * leftover "Untitled post" makes the NEXT new post come out as
 * "untitled-post-2" — which used to be enough to change what slug a later
 * assertion should expect. Cleaning up keeps each journey independent of the
 * order the others ran in.
 */
async function deletePost(page: Page, id: string) {
  await page.goto(`${BLOGAPP}/admin`);
  await page.locator(`form[action="/admin/posts/${id}/delete"] button`).click();
  await expect(page.locator(`a[href="/admin/posts/${id}"]`)).toHaveCount(0);
}

// ─── Reading ─────────────────────────────────────────────────────────

test("the homepage lists seeded posts and a reader gets no editor", async ({ page }) => {
  await page.goto(`${BLOGAPP}/`);
  await expect(page.getByRole("heading", { level: 1, name: "Written in the browser" })).toBeVisible();

  await page.getByRole("link", { name: /Why the reader never loads the editor/ }).first().click();
  await expect(page).toHaveURL(`${BLOGAPP}/posts/why-the-reader-never-loads-the-editor`);

  // The stored ProseMirror document rendered server-side: a heading, a list,
  // a code block, and a quote all survive the trip through richtext/ssr.
  await expect(page.getByRole("heading", { name: "Two representations, one source of truth" })).toBeVisible();
  await expect(page.locator("pre")).toContainText("ssr.RenderJSON");
  await expect(page.locator("blockquote")).toBeVisible();

  // And none of the editor came with it.
  await expect(page.locator("iframe")).toHaveCount(0);
  await expect(page.locator("[data-fui-plugin]")).toHaveCount(0);
});

test("a draft is not readable and returns a real 404", async ({ page }) => {
  const response = await page.goto(`${BLOGAPP}/posts/a-draft-nobody-can-read`);
  expect(response?.status()).toBe(404);
  await expect(page.getByRole("heading", { name: /404 . not found/ })).toBeVisible();
  await expect(page.getByRole("link", { name: "Back to posts" })).toBeVisible();
});

test("an unknown slug 404s rather than answering 200 with a not-found body", async ({ page }) => {
  // The soft-404 guard. The route pattern matches anything slug-shaped, so
  // without the resolve-or-404 middleware this would be a 200.
  const response = await page.goto(`${BLOGAPP}/posts/no-such-post-at-all`);
  expect(response?.status()).toBe(404);
});

test("tags and search reach the same posts", async ({ page }) => {
  await page.goto(`${BLOGAPP}/tags`);
  await page.getByRole("link", { name: /^richtext \(\d+\)$/ }).click();
  await expect(page).toHaveURL(`${BLOGAPP}/tags/richtext`);
  await expect(page.getByRole("link", { name: /Why the reader never loads the editor/ })).toBeVisible();

  await page.goto(`${BLOGAPP}/search`);
  await page.locator("#search-page-q").fill("capability");
  await page.locator("#search-page-q").press("Enter");
  await expect(page.getByRole("link", { name: /capability gate is not an authentication gate/ })).toBeVisible();
});

// ─── The admin gate ──────────────────────────────────────────────────

test("the admin is unreachable until you sign in", async ({ page }) => {
  await page.goto(`${BLOGAPP}/admin`);
  await expect(page).toHaveURL(/\/admin\/login/);
  await expect(page.locator("#admin-password")).toBeVisible();

  // Wrong password keeps you out.
  await page.locator("#admin-password").fill("not-the-password");
  await page.locator("#sign-in").click();
  await expect(page.getByText("Wrong password")).toBeVisible();

  await login(page);
  await expect(page.getByRole("heading", { level: 1, name: "Posts" })).toBeVisible();
  // Drafts are visible here and only here.
  await expect(page.getByText("Draft").first()).toBeVisible();
});

// ─── The authoring loop ──────────────────────────────────────────────

test("the editor mounts in an opaque-origin sandboxed frame", async ({ page }) => {
  await login(page);
  const id = await newPost(page);
  await editorReady(page);

  const frame = page.locator("iframe");
  // allow-scripts WITHOUT allow-same-origin is what makes the origin opaque.
  // If allow-same-origin ever appears here, the isolation is gone.
  const sandbox = await frame.getAttribute("sandbox");
  expect(sandbox).toContain("allow-scripts");
  expect(sandbox).not.toContain("allow-same-origin");

  // The host page cannot reach into the frame — that is the boundary working.
  //
  // Reading contentDocument on an opaque frame is a security violation, and
  // WebKit reports it as an uncaught page error even though the read below is
  // wrapped in a try. That error is the assertion succeeding, so it is declared
  // expected rather than filtered globally: any OTHER cross-frame violation in
  // any other journey still fails the suite.
  expectedConsoleErrors.push(/sandboxed and lacks the "allow-same-origin" flag/);
  const reachable = await page.evaluate(() => {
    const f = document.querySelector("iframe") as HTMLIFrameElement;
    try {
      return !!f.contentDocument;
    } catch {
      return false;
    }
  });
  expect(reachable).toBe(false);

  // The frame auto-sized itself to its content rather than staying at the
  // configured minimum.
  await expect.poll(async () => (await frame.boundingBox())?.height ?? 0).toBeGreaterThan(100);

  await deletePost(page, id);
});

test("writing in the editor autosaves over the bridge and reaches the reader", async ({ page }) => {
  await login(page);
  const id = await newPost(page);
  await editorReady(page);

  await editor(page).click();
  await page.keyboard.type("Written through the sandboxed editor.");
  await expect(editor(page)).toContainText("Written through the sandboxed editor.");

  // The bridge mirrors the document into the host form's hidden inputs on every
  // change, and POSTs it on a debounce. Wait for the POST rather than sleeping.
  await page.waitForResponse(
    (r) => r.url().includes("/__gofastr/plugin/richtext/save") && r.request().method() === "POST" && r.ok(),
    { timeout: 15_000 }
  );

  // Give the post a title and publish it, so a reader can reach it.
  await page.locator("#post-title").fill("Autosaved in a browser");
  await page.locator("#post-status").selectOption("published");
  await page.locator("#save-post").click();
  await expect(page).toHaveURL(`${BLOGAPP}/admin/posts/${id}`);

  // The reader's view: server-rendered, and carrying what was typed.
  await page.goto(`${BLOGAPP}/posts/autosaved-in-a-browser`);
  await expect(page.getByRole("heading", { level: 1, name: "Autosaved in a browser" })).toBeVisible();
  await expect(page.getByText("Written through the sandboxed editor.")).toBeVisible();
  await expect(page.locator("iframe")).toHaveCount(0);

  await deletePost(page, id);
});

test("the document survives a reload — the body is server-rendered back into the frame", async ({ page }) => {
  await login(page);
  const id = await newPost(page);
  await editorReady(page);

  await editor(page).click();
  await page.keyboard.type("Persisted across a reload.");
  await page.waitForResponse(
    (r) => r.url().includes("/__gofastr/plugin/richtext/save") && r.request().method() === "POST" && r.ok(),
    { timeout: 15_000 }
  );

  await page.reload();
  await editorReady(page);
  await expect(editor(page)).toContainText("Persisted across a reload.");

  await deletePost(page, id);
});

/**
 * Flip a post's published status and WAIT for the server to have done it.
 *
 * The button submits a form, so clicking it starts a POST and a navigation.
 * Navigating on to the homepage without waiting races that round trip: on a
 * loaded runner the homepage renders before the status change is committed,
 * and the assertion then retries against a DOM snapshot that will never
 * change, because a locator re-query does not reload the page. It failed
 * twice that way on CI while passing every time locally.
 */
async function toggleStatus(page: Page, id: string): Promise<void> {
  await page.goto(`${BLOGAPP}/admin`);
  await Promise.all([
    // Wait for the REDIRECT TO LAND, not merely for the POST to answer.
    //
    // Waiting on the response alone was the first attempt at this, and it
    // fixed the original stale-homepage race while creating a second one: the
    // POST answers 303, the browser then navigates to /admin, and the caller's
    // next goto collided with that in-flight navigation. CI said so exactly:
    // "Navigation to /  is interrupted by another navigation to /admin".
    // Landing on /admin implies the POST both completed and succeeded.
    page.waitForURL((u) => u.pathname.startsWith("/admin"), { timeout: 15_000 }),
    page.locator(`form[action="/admin/posts/${id}/status"] button`).click(),
  ]);
}

test("publish and unpublish move a post on and off the public site", async ({ page }) => {
  await login(page);
  const id = await newPost(page);
  await editorReady(page);

  await page.locator("#post-title").fill("Toggled visibility");
  await page.locator("#save-post").click();

  // Draft: not on the homepage.
  await page.goto(`${BLOGAPP}/`);
  await expect(page.getByRole("link", { name: "Toggled visibility" })).toHaveCount(0);

  await toggleStatus(page, id);
  await page.goto(`${BLOGAPP}/`);
  await expect(page.getByRole("link", { name: /Toggled visibility/ }).first()).toBeVisible();

  // And back off again.
  await toggleStatus(page, id);
  await page.goto(`${BLOGAPP}/`);
  await expect(page.getByRole("link", { name: "Toggled visibility" })).toHaveCount(0);

  await deletePost(page, id);
});

// ─── The security boundary, driven from a real browser ───────────────

test("an anonymous save cannot overwrite a post", async ({ page, request }) => {
  // The plugin's capability gate passes for anonymous callers, so the app's own
  // check inside the save handler is the only thing stopping this. The Go suite
  // asserts the same thing; doing it here too means the browser-facing endpoint
  // is covered by the suite people actually run before shipping UI changes.
  const slug = "why-the-reader-never-loads-the-editor";
  await page.goto(`${BLOGAPP}/posts/${slug}`);
  const before = await page.locator("main").textContent();

  const res = await request.post(`${BLOGAPP}/__gofastr/plugin/richtext/save`, {
    data: {
      docId: "any-id",
      doc: { type: "doc", content: [{ type: "paragraph", content: [{ type: "text", text: "OWNED" }] }] },
      markdown: "OWNED",
      schemaVersion: "richtext-v1",
    },
  });
  expect(res.ok()).toBeFalsy();

  await page.reload();
  expect(await page.locator("main").textContent()).toBe(before);
  await expect(page.getByText("OWNED")).toHaveCount(0);
});

// ─── Mobile ──────────────────────────────────────────────────────────

test("mobile: reading works at 390px without sideways scroll", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 780 });
  await page.goto(`${BLOGAPP}/posts/slugs-drafts-and-the-soft-404-problem`);

  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth
  );
  expect(overflow).toBeLessThanOrEqual(1);
});
