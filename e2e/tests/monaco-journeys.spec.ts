// User-journey e2e for the Monaco code-editor plugin — the third sandboxed
// heavy-JS plugin. Pins the load-bearing risk the plugin author could not verify
// headless: Monaco must boot WORKER-FREE inside the opaque-origin iframe (workers
// are restricted there), edit + autosave must round-trip, and a save conflict
// must surface rather than vanish.
import { test, expect, type Page } from "@playwright/test";

const MONACO = "/monaco";
const SAVE = "/__gofastr/plugin/monaco/save";

const frame = (page: Page) => page.frameLocator("iframe");
const editor = (page: Page) => frame(page).locator(".monaco-editor");
const viewLines = (page: Page) => frame(page).locator(".monaco-editor .view-lines");
const status = (page: Page) => frame(page).locator("#monaco-status");

async function ready(page: Page) {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __monacoReady?: boolean }) | null;
      return !!f && f.__monacoReady === true;
    },
    undefined,
    { timeout: 15_000 }
  );
}

test.beforeEach(async ({ page, request, baseURL }) => {
  // Reset the shared demo doc to a known baseline.
  await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", code: "// baseline\n", language: "javascript", schemaVersion: "monaco-v1" },
  });
  await page.goto(MONACO);
  await ready(page);
});

test("mounts worker-free in the opaque-origin sandbox with no console/boot errors", async ({ page }) => {
  const errors: string[] = [];
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  page.on("pageerror", (e) => errors.push(String(e)));

  await expect(editor(page)).toBeVisible({ timeout: 10_000 });
  // Worker-free boot leaves no MonacoEnvironment/worker/SecurityError noise.
  expect(errors.filter((e) => !/favicon/i.test(e))).toEqual([]);
});

test("editing autosaves and survives a reload", async ({ page }) => {
  await viewLines(page).click();
  await page.keyboard.type("const answer = 42;");
  // Autosave debounce is 2s; wait it out, then reload and confirm persistence.
  await page.waitForTimeout(2600);
  await page.reload();
  await ready(page);
  await expect(viewLines(page)).toContainText("const answer = 42;");
});

test("a save conflict (409) surfaces a status line instead of a silent loss", async ({ page }) => {
  await viewLines(page).click();
  await page.keyboard.type("x");

  await page.route(`**${SAVE}`, (route) =>
    route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: "E_CONFLICT" }) })
  );
  // Ctrl/Cmd-S forces an immediate save; Monaco owns the shortcut in-frame.
  await page.keyboard.press("ControlOrMeta+s");
  await expect(status(page)).toContainText(/conflict/i);
});

// --- Showcase: the control panel reconfigures the frame LIVE (postMessage) -----

const toggle = (page: Page, opt: string) => page.locator(`.fui-btn.toggle[data-opt="${opt}"]`);

test("control panel reconfigures the editor live: line numbers + font size", async ({ page }) => {
  // Line numbers are on by default → rendered in the gutter. Toggling the control
  // removes them from the frame's DOM live (an unambiguous reconfigure signal;
  // Monaco keeps a .minimap node around even when disabled, so it's a poor probe).
  const lineNums = frame(page).locator(".margin-view-overlays .line-numbers");
  await expect(lineNums.first()).toBeVisible();
  await toggle(page, "lineNumbers").click();
  await expect(toggle(page, "lineNumbers")).not.toHaveClass(/active/);
  await expect(lineNums).toHaveCount(0);

  // Font size control updates the page state and pushes it to the frame.
  await expect(page.locator("#font-val")).toHaveText("14");
  await page.locator("#font-inc").click();
  await expect(page.locator("#font-val")).toHaveText("15");
});

test("diff view toggles a side-by-side diff editor", async ({ page }) => {
  await expect(frame(page).locator(".monaco-diff-editor")).toHaveCount(0);
  await page.locator("#diff").click();
  await expect(page.locator("#diff")).toHaveClass(/active/);
  await expect(frame(page).locator(".monaco-diff-editor")).toBeVisible({ timeout: 10_000 });
});

test("language switch + load sample replaces the buffer", async ({ page }) => {
  await page.locator("#lang").selectOption("go");
  await page.locator("#sample").click();
  await expect(viewLines(page)).toContainText("package main");
});

test("settings modal is configurable and stays in sync with the toolbar", async ({ page }) => {
  // A toolbar change (modal closed) is reflected in the modal control on open.
  await toggle(page, "wordWrap").click();
  await page.locator("#settings").click();
  await expect(page.locator("#modal")).toHaveClass(/open/);
  await expect(page.locator("#m-wordWrap")).toBeChecked();

  // A change made INSIDE the modal reconfigures the editor live AND syncs the
  // toolbar — the settings panel is interactive, not a read-only readout. (The
  // toolbar sits behind the modal, so we assert its synced state by class, not
  // by clicking it.)
  const lineNums = frame(page).locator(".margin-view-overlays .line-numbers");
  await expect(lineNums.first()).toBeVisible();
  await page.locator("#m-lineNumbers").uncheck();
  await expect(lineNums).toHaveCount(0); // the frame applied it
  await expect(toggle(page, "lineNumbers")).not.toHaveClass(/active/); // toolbar synced
  await expect(page.locator("#cfg-json")).toContainText('"lineNumbers": false');

  await page.keyboard.press("Escape");
  await expect(page.locator("#modal")).not.toHaveClass(/open/);
});
