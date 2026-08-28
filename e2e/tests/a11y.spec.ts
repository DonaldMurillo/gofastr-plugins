// Phase-1 a11y gate: axe-core audits of every user-facing surface.
//
// The trusted (in-page) mount is what makes the EDITOR itself auditable: axe
// cannot see into an opaque-origin sandboxed iframe, so the framed demo audit
// covers the host page + mount chrome, while the trusted page audit covers the
// full editor DOM — toolbar buttons, slash-menu listbox, task checkboxes.
import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

const EMPTY_DOC = {
  docId: "demo",
  doc: { type: "doc", content: [{ type: "paragraph" }] },
  markdown: "",
  schemaVersion: "richtext-v1",
};

async function audit(page: Page, label: string) {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa"])
    .analyze();
  const serious = results.violations.filter(
    (v) => v.impact === "serious" || v.impact === "critical"
  );
  const detail = serious
    .map((v) => `[${v.impact}] ${v.id}: ${v.help}\n  ${v.nodes.map((n) => n.target.join(" ")).join("\n  ")}`)
    .join("\n");
  expect(serious, `${label}: serious/critical a11y violations:\n${detail}`).toEqual([]);
}

test.beforeEach(async ({ request, baseURL }) => {
  await request.post(`${baseURL}/__gofastr/plugin/richtext/save`, { data: EMPTY_DOC });
});

test("a11y: pdf demo page (host chrome + mount)", async ({ page }) => {
  await page.goto("/pdf");
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __pdfRendered?: boolean }) | null;
      return !!f && f.__pdfRendered === true;
    },
    undefined,
    { timeout: 25_000 }
  );
  // As with the other sandboxed plugins, axe cannot see into the opaque frame,
  // so this audits the host page and the mount chrome around it.
  await audit(page, "pdf demo");
});

test("a11y: calendar demo page (host chrome + mount)", async ({ page }) => {
  await page.goto("/calendar");
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __calendarReady?: boolean }) | null;
      return !!f && f.__calendarReady === true;
    },
    undefined,
    { timeout: 25_000 }
  );
  // As with the other sandboxed plugins, axe cannot see into the opaque
  // frame, so this audits the host page and the mount chrome around it.
  await audit(page, "calendar demo");
});

test("a11y: framed demo page (host chrome)", async ({ page }) => {
  await page.goto("/richtext");
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as (HTMLIFrameElement & { __richtextReady?: boolean }) | null;
    return !!f && f.__richtextReady === true;
  });
  await audit(page, "framed demo");
});

test("a11y: trusted page with the full editor DOM, slash menu open", async ({ page }) => {
  await page.goto("/__gofastr/plugin/richtext/trusted");
  await page.waitForFunction(
    () => (window as unknown as { __richtextTrustedReady?: boolean }).__richtextTrustedReady === true
  );
  await audit(page, "trusted editor (idle)");

  // Open the slash menu and audit the open-menu state (listbox/option roles).
  await page.locator(".ProseMirror").click();
  await page.keyboard.type("/");
  await expect(page.locator(".richtext-slash-menu")).toBeVisible();
  await audit(page, "trusted editor (slash menu open)");
});

test("a11y: trusted page with a selection + bubble toolbar open", async ({ page }) => {
  await page.goto("/__gofastr/plugin/richtext/trusted");
  await page.waitForFunction(
    () => (window as unknown as { __richtextTrustedReady?: boolean }).__richtextTrustedReady === true
  );
  await page.locator(".ProseMirror").click();
  await page.keyboard.type("select this text");
  for (let i = 0; i < 4; i++) await page.keyboard.press("Shift+ArrowLeft");
  await expect(page.locator(".richtext-bubble")).toBeVisible();
  await audit(page, "trusted editor (bubble toolbar open)");
});

test("a11y: SSR read view", async ({ page, request, baseURL }) => {
  // Give the read view real content first.
  await request.post(`${baseURL}/__gofastr/plugin/richtext/save`, {
    data: {
      docId: "demo",
      doc: {
        type: "doc",
        content: [
          { type: "heading", attrs: { level: 1 }, content: [{ type: "text", text: "Read view" }] },
          { type: "paragraph", content: [{ type: "text", text: "Rendered without JS." }] },
        ],
      },
      markdown: "",
      schemaVersion: "richtext-v1",
    },
  });
  await page.goto("/__gofastr/plugin/richtext/read?doc=demo");
  await audit(page, "SSR read view");
});
