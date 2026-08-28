// Visual-review shots for the formbuilder demo pages (design + live form).
// NOT a pass/fail gate: writes PNGs for humans, asserts nothing beyond boot.
// Runs ONLY under the dedicated "shots" project (SHOTS=1 npm run shots), like
// shots.spec.ts — the default projects ignore files matching /shots\.spec\.ts/.
import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";

const SHOTS = process.env.SHOTS === "1";
const DIR = "shots";

// The demo canvas, so the shots always show the same designed form.
const DEMO_DOC = {
  version: "formbuilder-v1",
  fields: [
    { type: "text", name: "full_name", label: "Full name", required: true,
      rules: { minLength: 2, maxLength: 80 } },
    { type: "email", name: "email", label: "Email", required: true },
    { type: "select", name: "role", label: "I am a…", required: true,
      options: ["Founder", "Operator", "Engineer", "Investor", "Other"] },
    { type: "textarea", name: "pitch", label: "What are you building?", required: true,
      rules: { minLength: 20, maxLength: 500 } },
    { type: "date", name: "launch", label: "Target launch" },
  ],
};

test.describe("formbuilder shots", () => {
  test.skip(!SHOTS, "run via: SHOTS=1 npx playwright test --project=shots");
  test.beforeAll(() => mkdirSync(DIR, { recursive: true }));

  test.beforeEach(async ({ request, baseURL }) => {
    const resp = await request.post(`${baseURL}/__gofastr/plugin/formbuilder/save`, {
      data: { docId: "demo", doc: DEMO_DOC, schemaVersion: "formbuilder-v1" },
    });
    expect(resp.ok()).toBeTruthy();
  });

  async function boot(page: Page, path: string): Promise<void> {
    await page.goto(path);
    const frame = page.locator("iframe");
    if (await frame.count()) {
      await page.waitForFunction(
        () => {
          const f = document.querySelector("iframe") as (HTMLIFrameElement & { __formbuilderReady?: boolean }) | null;
          return !!f && f.__formbuilderReady === true;
        },
        undefined,
        { timeout: 15_000 }
      );
      // Let the first save verdict land so the proof strip is populated.
      await page.waitForTimeout(1800);
    }
  }

  for (const vp of [
    { name: "desktop", width: 1280, height: 800 },
    { name: "390", width: 390, height: 844 },
  ]) {
    for (const scheme of ["light", "dark"]) {
      test(`${scheme} ${vp.name}: design page`, async ({ browser }) => {
        const ctx = await browser.newContext({
          viewport: { width: vp.width, height: vp.height },
          extraHTTPHeaders: {},
        });
        await ctx.addCookies([{ name: "fui-color-scheme", value: scheme, url: "http://localhost:8123" }]);
        const page = await ctx.newPage();
        await boot(page, "/formbuilder");
        await page.screenshot({ path: `${DIR}/formbuilder-${scheme}-${vp.name}.png`, fullPage: true });
        await ctx.close();
      });
      test(`${scheme} ${vp.name}: live form`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: { width: vp.width, height: vp.height } });
        await ctx.addCookies([{ name: "fui-color-scheme", value: scheme, url: "http://localhost:8123" }]);
        const page = await ctx.newPage();
        await boot(page, "/formbuilder/live");
        await page.screenshot({ path: `${DIR}/formbuilder-live-${scheme}-${vp.name}.png`, fullPage: true });
        await ctx.close();
      });
    }
  }
});
