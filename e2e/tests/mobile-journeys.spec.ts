// Phase-1 mobile gate: the editor driven like a PHONE user — narrow viewport,
// real touch taps — on both mounts, in mobile WebKit (iPhone) and mobile
// Chromium (Pixel). This is exactly the surface where the desktop suite went
// blind: the user found the frame-clipped slash menu in a narrow window, and
// touch layouts have no Escape key to dismiss anything.
import { test, expect, type Page, type FrameLocator } from "@playwright/test";

const EMPTY_DOC = {
  docId: "demo",
  doc: { type: "doc", content: [{ type: "paragraph" }] },
  markdown: "",
  schemaVersion: "wysiwyg-v1",
};

interface Surface {
  name: string;
  path: string;
  ui: (page: Page) => Page | FrameLocator;
  ready: (page: Page) => Promise<unknown>;
}

const SURFACES: Surface[] = [
  {
    name: "sandboxed iframe",
    path: "/",
    ui: (page) => page.frameLocator("iframe"),
    ready: (page) =>
      page.waitForFunction(() => {
        const f = document.querySelector("iframe") as (HTMLIFrameElement & { __wysiwygReady?: boolean }) | null;
        return !!f && f.__wysiwygReady === true;
      }),
  },
  {
    name: "trusted in-page mount",
    path: "/__gofastr/plugin/wysiwyg/trusted",
    ui: (page) => page,
    ready: (page) =>
      page.waitForFunction(
        () => (window as unknown as { __wysiwygTrustedReady?: boolean }).__wysiwygTrustedReady === true
      ),
  },
];

let consoleErrors: string[];

for (const surface of SURFACES) {
  test.describe(`mobile · ${surface.name}`, () => {
    const ui = (page: Page) => surface.ui(page);
    const editor = (page: Page) => ui(page).locator(".ProseMirror");

    test.beforeEach(async ({ page, request, baseURL }) => {
      await request.post(`${baseURL}/__gofastr/plugin/wysiwyg/save`, { data: EMPTY_DOC });
      consoleErrors = [];
      page.on("console", (msg) => {
        if (msg.type() === "error") consoleErrors.push(msg.text());
      });
      page.on("pageerror", (err) => consoleErrors.push(`pageerror: ${err.message}`));
      await page.goto(surface.path);
      await surface.ready(page);
    });

    test.afterEach(() => {
      const real = consoleErrors.filter((e) => !/favicon/i.test(e));
      expect(real, `console/page errors:\n${real.join("\n")}`).toEqual([]);
    });

    test("tap to focus, type, text renders", async ({ page }) => {
      await editor(page).tap();
      await page.keyboard.type("typed on a phone");
      await expect(editor(page)).toContainText("typed on a phone");
    });

    test("slash menu opens on-screen and a TAP on an item applies it", async ({ page }) => {
      await editor(page).tap();
      await page.keyboard.type("/");

      const menu = ui(page).locator(".wysiwyg-slash-menu");
      await expect(menu).toBeVisible();

      // The menu must be fully INSIDE the phone viewport horizontally, and (in
      // the framed mount) not clipped by the iframe edge vertically.
      await page.waitForTimeout(400); // overlay-aware frame resize round-trip
      const menuBox = (await menu.boundingBox())!;
      const viewport = page.viewportSize()!;
      expect(menuBox.x, "menu clipped off the left edge").toBeGreaterThanOrEqual(0);
      expect(menuBox.x + menuBox.width, "menu clipped off the right edge").toBeLessThanOrEqual(viewport.width + 1);
      if (surface.path === "/") {
        const frameBox = (await page.locator("iframe").boundingBox())!;
        expect(
          menuBox.y + menuBox.height,
          "menu clipped by the iframe edge on a phone"
        ).toBeLessThanOrEqual(frameBox.y + frameBox.height + 2);
      }

      // Touch selection: tap "Heading 1" (no mouse hover exists on a phone).
      await ui(page).locator(".wysiwyg-slash-item", { hasText: "Heading 1" }).tap();
      await expect(menu).toBeHidden();
      await expect(editor(page).locator("h1")).toHaveCount(1);
    });

    test("tapping outside dismisses the slash menu (phones have no Escape key)", async ({ page }) => {
      await editor(page).tap();
      await page.keyboard.type("/");
      const menu = ui(page).locator(".wysiwyg-slash-menu");
      await expect(menu).toBeVisible();

      await page.locator("header h1").tap();
      await expect(menu).toBeHidden();
    });

    test("to-do checkbox toggles with a tap", async ({ page }) => {
      await editor(page).tap();
      await page.keyboard.type("/to-do");
      await ui(page).locator(".wysiwyg-slash-item", { hasText: "To-do list" }).tap();
      await page.keyboard.type("phone task");

      const item = editor(page).locator("li.wysiwyg-task-item").first();
      await expect(item).toHaveAttribute("data-checked", "false");
      await item.locator(".wysiwyg-task-checkbox").tap();
      await expect(item).toHaveAttribute("data-checked", "true");
    });

    test("no horizontal page overflow at phone width", async ({ page }) => {
      await editor(page).tap();
      await page.keyboard.type("# A heading that is fairly long for a phone");
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - document.documentElement.clientWidth
      );
      expect(overflow, "page scrolls sideways on a phone").toBeLessThanOrEqual(1);
    });
  });
}
