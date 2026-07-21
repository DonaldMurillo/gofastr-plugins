// Phase-3 dogfood gate: PIXELS, NOT PROBES (gofastr Hard rule 9).
// Captures the editor with every block type, light + dark, desktop + mobile,
// framed + trusted + SSR, for human review. Opt-in: SHOTS=1 npm run shots.
// Output: e2e/shots/*.png (gitignored).
import { test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";

const SHOTS = process.env.SHOTS === "1";
const DIR = "shots";

// Exercises every node + mark in schema-v1 (mirrors example/everyelement_test.go).
const EVERY_DOC = {
  type: "doc",
  content: [
    { type: "heading", attrs: { level: 1 }, content: [{ type: "text", text: "Every block" }] },
    {
      type: "paragraph",
      content: [
        { type: "text", marks: [{ type: "strong" }], text: "bold " },
        { type: "text", marks: [{ type: "em" }], text: "italic " },
        { type: "text", marks: [{ type: "underline" }], text: "underline " },
        { type: "text", marks: [{ type: "strike" }], text: "strike " },
        { type: "text", marks: [{ type: "code" }], text: "code " },
        { type: "text", marks: [{ type: "link", attrs: { href: "https://example.com", title: null } }], text: "link " },
        { type: "text", marks: [{ type: "textColor", attrs: { color: "red" } }], text: "red " },
        { type: "text", marks: [{ type: "bgColor", attrs: { color: "yellow" } }], text: "highlight" },
      ],
    },
    { type: "blockquote", content: [{ type: "paragraph", content: [{ type: "text", text: "A quote." }] }] },
    { type: "code_block", attrs: { language: "go" }, content: [{ type: "text", text: 'fmt.Println("hi")' }] },
    { type: "divider" },
    {
      type: "bullet_list",
      content: [
        { type: "list_item", content: [{ type: "paragraph", content: [{ type: "text", text: "Bullet" }] }] },
      ],
    },
    {
      type: "task_list",
      content: [
        { type: "task_item", attrs: { checked: true }, content: [{ type: "paragraph", content: [{ type: "text", text: "Done" }] }] },
        { type: "task_item", attrs: { checked: false }, content: [{ type: "paragraph", content: [{ type: "text", text: "Todo" }] }] },
      ],
    },
    { type: "callout", attrs: { variant: "info", icon: null }, content: [{ type: "paragraph", content: [{ type: "text", text: "Info callout" }] }] },
    {
      type: "toggle",
      attrs: { open: true },
      content: [
        { type: "toggle_summary", content: [{ type: "text", text: "Toggle" }] },
        { type: "content", content: [{ type: "paragraph", content: [{ type: "text", text: "Body" }] }] },
      ],
    },
    {
      type: "columns",
      attrs: { count: 2 },
      content: [
        { type: "column", content: [{ type: "paragraph", content: [{ type: "text", text: "Left" }] }] },
        { type: "column", content: [{ type: "paragraph", content: [{ type: "text", text: "Right" }] }] },
      ],
    },
    {
      type: "table",
      content: [
        {
          type: "table_row",
          content: [
            { type: "table_header", attrs: { colspan: 1, rowspan: 1, colwidth: null }, content: [{ type: "paragraph", content: [{ type: "text", text: "Name" }] }] },
            { type: "table_header", attrs: { colspan: 1, rowspan: 1, colwidth: null }, content: [{ type: "paragraph", content: [{ type: "text", text: "Role" }] }] },
          ],
        },
        {
          type: "table_row",
          content: [
            { type: "table_cell", attrs: { colspan: 1, rowspan: 1, colwidth: null }, content: [{ type: "paragraph", content: [{ type: "text", text: "Ada" }] }] },
            { type: "table_cell", attrs: { colspan: 1, rowspan: 1, colwidth: null }, content: [{ type: "paragraph", content: [{ type: "text", text: "Engineer" }] }] },
          ],
        },
      ],
    },
  ],
};

test.describe("dogfood shots", () => {
  // Collection is gated by the dedicated "shots" Playwright project, which only
  // exists under SHOTS=1 (see playwright.config.ts). The default run never
  // collects this file, so there is nothing to skip. This assert is a guardrail
  // in case the file is ever run directly without the flag.
  test.skip(!SHOTS, "run via: npm run shots (SHOTS=1, --project=shots)");
  test.beforeAll(() => mkdirSync(DIR, { recursive: true }));

  test.beforeEach(async ({ request, baseURL }) => {
    await request.post(`${baseURL}/__gofastr/plugin/richtext/save`, {
      data: { docId: "demo", doc: EVERY_DOC, markdown: "", schemaVersion: "richtext-v1" },
    });
  });

  async function shoot(page: Page, path: string, ready: () => Promise<unknown>, name: string) {
    for (const scheme of ["light", "dark"] as const) {
      await page.goto(path);
      await ready();
      if (scheme === "dark") {
        const toggle = page.locator("#fui-scheme-toggle");
        if (await toggle.count()) await toggle.click();
        else await page.evaluate(() => document.documentElement.setAttribute("data-color-scheme", "dark"));
        await page.waitForTimeout(600); // token re-bridge (framed) settles
      }
      await page.waitForTimeout(400);
      await page.screenshot({ path: `${DIR}/${name}-${scheme}.png`, fullPage: true });
    }
  }

  for (const vp of [
    { label: "desktop", width: 1280, height: 900 },
    { label: "mobile", width: 390, height: 844 },
  ]) {
    test(`framed · ${vp.label}`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await shoot(
        page,
        "/richtext",
        () =>
          page.waitForFunction(() => {
            const f = document.querySelector("iframe") as (HTMLIFrameElement & { __richtextReady?: boolean }) | null;
            return !!f && f.__richtextReady === true;
          }),
        `framed-${vp.label}`
      );
    });

    test(`trusted · ${vp.label}`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await shoot(
        page,
        "/__gofastr/plugin/richtext/trusted",
        () =>
          page.waitForFunction(
            () => (window as unknown as { __richtextTrustedReady?: boolean }).__richtextTrustedReady === true
          ),
        `trusted-${vp.label}`
      );
    });

    test(`ssr read view · ${vp.label}`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await shoot(page, "/__gofastr/plugin/richtext/read?doc=demo", () => Promise.resolve(), `ssr-${vp.label}`);
    });
  }
});
