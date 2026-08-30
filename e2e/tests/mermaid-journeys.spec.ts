// User-journey e2e for the mermaid plugin — the second heavy-JS plugin, which
// the review flagged as having NO Playwright coverage (only a chromedp mount
// canary). This specifically pins the autosave path that was shipped dead:
// editing the diagram source must persist and survive a reload.
import { test, expect } from "@playwright/test";

const MERMAID = "/mermaid";
const SAVE = "/__gofastr/plugin/mermaid/save";

// The frame's textarea lives inside the sandboxed iframe.
function source(page: import("@playwright/test").Page) {
  return page.frameLocator("iframe").locator("#mermaid-source");
}

async function ready(page: import("@playwright/test").Page) {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __mermaidReady?: boolean }) | null;
      return !!f && f.__mermaidReady === true;
    },
    undefined,
    { timeout: 15_000 }
  );
}

test.beforeEach(async ({ page, request, baseURL }) => {
  // Reset the shared demo diagram to a known baseline.
  await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", source: "graph TD; A-->B", schemaVersion: "mermaid-v1" },
  });
  await page.goto(MERMAID);
  await ready(page);
});

test("invalid syntax reports a parse error instead of leaving the last good diagram up", async ({ page }) => {
  await page.goto("/mermaid");
  const frame = page.frameLocator("iframe");
  const editor = frame.locator("textarea").first();
  await expect(frame.locator("svg")).toHaveCount(1, { timeout: 25_000 });

  await editor.click();
  await editor.fill("graph TD\n  A --> ((((( unclosed");

  // Two properties, and the second is the one that matters. Reporting the
  // error is good; leaving the PREVIOUS diagram on screen while reporting it
  // would be worse than either, because the picture would no longer describe
  // the source and nothing would say which one to believe.
  await expect(frame.locator("body")).toContainText(/parse error/i, { timeout: 15_000 });
  await expect(frame.locator("svg"), "a stale diagram must not outlive its source").toHaveCount(0);
});

test("mounts sandboxed with no console errors and renders a diagram", async ({ page }) => {
  const errors: string[] = [];
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  page.on("pageerror", (e) => errors.push(String(e)));

  // The preview must contain an SVG rendered from the source.
  await expect(page.frameLocator("iframe").locator("svg")).toBeVisible({ timeout: 10_000 });
  expect(errors.filter((e) => !/favicon/i.test(e))).toEqual([]);
});

test("editing the source autosaves and survives a reload (the previously-dead save path)", async ({ page }) => {
  const ta = source(page);
  await ta.click();
  await ta.fill("sequenceDiagram; Alice->>Bob: Hello");

  // Autosave debounce is 2s; wait it out, then reload and confirm persistence.
  await page.waitForTimeout(2600);
  await page.reload();
  await ready(page);
  await expect(source(page)).toHaveValue(/Alice->>Bob: Hello/);
});
