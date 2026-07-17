// User-journey e2e tests for the Rich Text editor. These drive the editor the
// way a person does — real mouse clicks, real typing, real menu selection —
// and assert what is VISUALLY rendered, in both WebKit (Safari's engine) and
// Chromium, against BOTH mounts:
//
//   • sandboxed — the default: opaque-origin iframe + postMessage broker
//   • trusted   — the explicit opt-out: in-page mount, no iframe
//     (DECISIONS.md "secure by default, opt out")
//
// Every test also fails on any console error or uncaught page error, so CSP
// refusals / crashed init (the class of bug Safari kept exposing) fail loudly
// even when the journey's own assertions would have been skipped.
import { test, expect, type Page, type FrameLocator } from "@playwright/test";

const EMPTY_DOC = {
  docId: "demo",
  doc: { type: "doc", content: [{ type: "paragraph" }] },
  markdown: "",
  schemaVersion: "richtext-v1",
};

interface Surface {
  name: string;
  path: string;
  /** The DOM scope the editor + its overlays live in. */
  ui: (page: Page) => Page | FrameLocator;
  ready: (page: Page) => Promise<unknown>;
}

const SURFACES: Surface[] = [
  {
    name: "sandboxed iframe",
    path: "/",
    ui: (page) => page.frameLocator("iframe"),
    ready: (page) =>
      page.waitForFunction(
        () => {
          const f = document.querySelector("iframe") as (HTMLIFrameElement & { __richtextReady?: boolean }) | null;
          return !!f && f.__richtextReady === true;
        },
        undefined,
        { timeout: 15_000 }
      ),
  },
  {
    name: "trusted in-page mount",
    path: "/__gofastr/plugin/richtext/trusted",
    ui: (page) => page,
    ready: (page) =>
      page.waitForFunction(
        () => (window as unknown as { __richtextTrustedReady?: boolean }).__richtextTrustedReady === true,
        undefined,
        { timeout: 15_000 }
      ),
  },
];

let consoleErrors: string[];

for (const surface of SURFACES) {
  test.describe(surface.name, () => {
    const ui = (page: Page) => surface.ui(page);
    const editor = (page: Page) => ui(page).locator(".ProseMirror");

    test.beforeEach(async ({ page, request, baseURL }) => {
      // Reset the persisted demo doc so every journey starts from a blank page.
      const res = await request.post(`${baseURL}/__gofastr/plugin/richtext/save`, {
        data: EMPTY_DOC,
      });
      expect(res.ok()).toBeTruthy();

      consoleErrors = [];
      page.on("console", (msg) => {
        if (msg.type() === "error") consoleErrors.push(msg.text());
      });
      page.on("pageerror", (err) => consoleErrors.push(`pageerror: ${err.message}`));

      await page.goto(surface.path);
      await surface.ready(page);
    });

    test.afterEach(() => {
      const real = consoleErrors.filter((e: string) => !/favicon/i.test(e));
      expect(real, `console/page errors during journey:\n${real.join("\n")}`).toEqual([]);
    });

    test("typing renders text in the editor", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("Hello from a real browser");
      await expect(editor(page)).toContainText("Hello from a real browser");
    });

    test("slash menu: open with '/', pick 'Heading 1' with the MOUSE, heading renders styled", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/");

      const menu = ui(page).locator(".richtext-slash-menu");
      await expect(menu).toBeVisible();

      // The user's exact gesture: click the menu item with the mouse.
      await ui(page).locator(".richtext-slash-item", { hasText: "Heading 1" }).click();

      await expect(menu).toBeHidden();

      // The paragraph must now BE an h1 — and the "/" must be gone.
      const h1 = editor(page).locator("h1");
      await expect(h1).toHaveCount(1);
      await expect(editor(page)).not.toContainText("/");

      // Type into it and confirm the text lands inside the heading, visibly
      // larger than surrounding text (i.e. the stylesheet actually applied —
      // the Safari CSP bug made everything render as same-size plain text).
      await page.keyboard.type("Big Title");
      await expect(h1).toContainText("Big Title");
      const sizes = await h1.evaluate((el: HTMLElement) => {
        const h = parseFloat(getComputedStyle(el).fontSize);
        const body = parseFloat(getComputedStyle(el.ownerDocument.body).fontSize);
        return { h, body };
      });
      expect(sizes.h, `h1 font-size ${sizes.h}px should exceed body ${sizes.body}px — is the stylesheet applied?`).toBeGreaterThan(sizes.body * 1.3);
    });

    test("slash menu: keyboard-only (arrows + Enter) inserts a bulleted list", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/bullet");
      await expect(ui(page).locator(".richtext-slash-menu")).toBeVisible();
      await page.keyboard.press("Enter");

      await expect(editor(page).locator("ul li")).toHaveCount(1);
      await page.keyboard.type("first item");
      await expect(editor(page).locator("ul li")).toContainText("first item");

      // Enter continues the list like a user expects.
      await page.keyboard.press("Enter");
      await page.keyboard.type("second item");
      await expect(editor(page).locator("ul li")).toHaveCount(2);
    });

    test("slash menu: filtering ('/quote') narrows and mouse-click inserts a blockquote", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/quote");

      const items = ui(page).locator(".richtext-slash-item");
      await expect(items).toHaveCount(1);
      await items.first().click();

      await expect(editor(page).locator("blockquote")).toHaveCount(1);
      await page.keyboard.type("wise words");
      await expect(editor(page).locator("blockquote")).toContainText("wise words");
    });

    test("bubble toolbar: select a word, click Bold with the mouse, text turns bold", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("make me bold please");

      await expect(editor(page)).toContainText("make me bold please");

      // Select the word "please" with Shift+ArrowLeft — a real user gesture
      // that behaves identically in every engine. (Headless WebKit's synthetic
      // dblclick does not perform word-selection, so dblclick can't be used
      // cross-engine.) Like a user, watch the highlight: under load the frame
      // can drop a synthetic keypress, so extend until the selection IS the
      // word rather than firing a blind fixed count.
      for (let i = 0; i < "please".length + 6; i++) {
        await page.keyboard.press("Shift+ArrowLeft");
        const sel = await editor(page).evaluate(
          (el: HTMLElement) => el.ownerDocument.getSelection()?.toString() || ""
        );
        if (sel === "please") break;
      }

      const bubble = ui(page).locator(".richtext-bubble");
      await expect(bubble).toBeVisible();

      // The user's gesture under test: a real mouse click on the Bold button.
      await bubble.locator('[title="Bold"]').click();
      await expect(editor(page).locator("strong")).toContainText("please");
    });

    test("markdown shortcut: '# ' becomes a heading while typing", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("# Instant heading");
      await expect(editor(page).locator("h1")).toContainText("Instant heading");
    });

    test("to-do list: insert via slash, click the checkbox like a user", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/to-do");
      await ui(page).locator(".richtext-slash-item", { hasText: "To-do list" }).click();
      await page.keyboard.type("buy milk");
      await page.keyboard.press("Enter");
      await page.keyboard.type("walk dog");

      await expect(editor(page)).toContainText("buy milk");
      const items = editor(page).locator("li.richtext-task-item");
      await expect(items).toHaveCount(2);

      // The checkbox is a non-editable span decoration (role="checkbox"), not
      // an <input>. Clicking the SECOND item's checkbox — while the caret sits
      // in it — must flip THAT item. (Regression: the handler used to act on
      // the selection, so clicking a decoration that doesn't move the caret
      // toggled the wrong item / lagged a click behind.)
      await expect(items.nth(0)).toHaveAttribute("data-checked", "false");
      await expect(items.nth(1)).toHaveAttribute("data-checked", "false");
      await items.nth(1).locator(".richtext-task-checkbox").click();
      await expect(items.nth(1)).toHaveAttribute("data-checked", "true");
      await expect(items.nth(0)).toHaveAttribute("data-checked", "false");

      // And the FIRST item's checkbox toggles the first, from a cold click
      // (no caret in it) — the exact fresh-load case the UX review caught.
      await items.nth(0).locator(".richtext-task-checkbox").click();
      await expect(items.nth(0)).toHaveAttribute("data-checked", "true");
      await expect(items.nth(1)).toHaveAttribute("data-checked", "true");
    });

    test("toggle block: insert via slash menu and it renders open with summary + body", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/toggle");
      await ui(page).locator(".richtext-slash-item", { hasText: "Toggle" }).click();
      await page.keyboard.type("My summary");
      await expect(editor(page)).toContainText("My summary");
    });

    test("divider: slash-insert renders a visible horizontal rule", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("some intro");
      await page.keyboard.press("Enter");
      await page.keyboard.type("/div");
      await ui(page).locator(".richtext-slash-item", { hasText: "Divider" }).click();
      await expect(editor(page).locator("hr")).toBeVisible();
    });

    test("code block: syntax highlighting colors keywords, numbers and strings once a language is set", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/code");
      await ui(page).locator(".richtext-slash-item", { hasText: "Code" }).click();

      // A code block, no language yet → no highlight spans.
      await page.keyboard.type('const answer = 42');
      const code = editor(page).locator("pre code");
      await expect(code).toContainText("const answer = 42");
      await expect(code.locator(".hl-keyword")).toHaveCount(0);

      // Pick a language from the floating block controls, the way a user would.
      await ui(page).locator(".richtext-blockctl-select").selectOption("javascript");

      // `const` is now a keyword span and `42` a number span — and their
      // computed color differs from the surrounding code text (the stylesheet
      // actually applied, not just the class).
      const kw = code.locator(".hl-keyword", { hasText: "const" });
      const num = code.locator(".hl-number", { hasText: "42" });
      await expect(kw).toHaveCount(1);
      await expect(num).toHaveCount(1);

      const colors = await code.evaluate((el: HTMLElement) => {
        const k = el.querySelector(".hl-keyword") as HTMLElement;
        const plain = getComputedStyle(el).color;
        return { keyword: getComputedStyle(k).color, plain };
      });
      expect(colors.keyword, "keyword color should differ from default code text").not.toBe(colors.plain);
    });

    test("slash menu: arrowing past the fold scrolls the active item into view, Enter applies it", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/");
      const menu = ui(page).locator(".richtext-slash-menu");
      await expect(menu).toBeVisible();

      // The unfiltered menu is taller than its 320px max-height. Arrow deep
      // into it — the highlight must stay VISIBLE inside the scrolled menu,
      // not vanish below the fold.
      for (let i = 0; i < 12; i++) await page.keyboard.press("ArrowDown");

      const active = menu.locator(".richtext-slash-item.is-active");
      await expect(active).toHaveCount(1);
      const inView = await menu.evaluate((m: HTMLElement) => {
        const a = m.querySelector(".richtext-slash-item.is-active")!;
        const mr = m.getBoundingClientRect();
        const ar = a.getBoundingClientRect();
        return ar.top >= mr.top - 1 && ar.bottom <= mr.bottom + 1;
      });
      expect(inView, "active item scrolled out of the menu's visible area").toBe(true);

      // Enter must apply the item the highlight is on (index 12 = Toggle).
      await expect(active).toContainText("Toggle");
      await page.keyboard.press("Enter");
      await expect(menu).toBeHidden();
      await expect(editor(page).locator('[data-type="richtext-toggle"]')).toHaveCount(1);
    });

    test("drag handle: stays visible while reaching for it, and drag reorders blocks", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("alpha block");
      await page.keyboard.press("Enter");
      await page.keyboard.type("beta block");

      // Hover the first paragraph like a user — the ⋮⋮ handle appears to its left.
      const first = editor(page).locator("p", { hasText: "alpha block" });
      const fb = (await first.boundingBox())!;
      await page.mouse.move(fb.x + 60, fb.y + fb.height / 2);
      const handle = ui(page).locator(".richtext-draghandle");
      await expect(handle).toBeVisible();

      // Travel from the text to the handle (crossing the editor's edge). The
      // handle must STAY visible and hoverable — this was impossible before:
      // it hid the instant the pointer left the editor.
      const hb = (await handle.boundingBox())!;
      await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2, { steps: 8 });
      await page.waitForTimeout(400); // longer than the hide grace period
      await expect(handle).toBeVisible();

      // Now actually use it: drag the first block onto the second.
      const second = editor(page).locator("p", { hasText: "beta block" });
      const sb = (await second.boundingBox())!;
      await page.mouse.down();
      await page.mouse.move(sb.x + 60, sb.y + sb.height - 2, { steps: 10 });
      await page.mouse.up();

      await expect(editor(page).locator("p").first()).toContainText("beta block");
      await expect(editor(page).locator("p").nth(1)).toContainText("alpha block");
    });

    test("clicking outside the menu dismisses it (no Escape key needed)", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/");
      const menu = ui(page).locator(".richtext-slash-menu");
      await expect(menu).toBeVisible();

      // The user's gesture: click somewhere else on the page — the host
      // header, outside the editor (and outside the iframe in framed mode).
      await page.locator("header h1").click();
      await expect(menu).toBeHidden();
    });

    test("persistent toolbar: block-type dropdown transforms the current block", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("toolbar heading");
      // The dropdown is the toolbar's signature control (Bold etc. are covered
      // by the bubble test). selectOption avoids fragile coordinate clicks.
      await ui(page).locator(".richtext-tb-select").selectOption("h1");
      await expect(editor(page).locator("h1")).toContainText("toolbar heading");
      // The dropdown reflects the current block back.
      await expect(ui(page).locator(".richtext-tb-select")).toHaveValue("h1");
      // Undo button (mobile's only undo path) reverts it.
      await ui(page).locator(".richtext-toolbar .richtext-tbtn[title='Undo']").click();
      await expect(editor(page).locator("h1")).toHaveCount(0);
    });

    test("word count status bar reflects the document", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("one two three");
      await expect(ui(page).locator(".richtext-statusbar")).toContainText("3 words");
    });

    test("alignment: toolbar centers the current block", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("center me");
      await ui(page).locator(".richtext-toolbar .richtext-tbtn[title='Align center']").click();
      const align = await editor(page).locator("p").first().evaluate(
        (el: HTMLElement) => getComputedStyle(el).textAlign
      );
      expect(align).toBe("center");
    });

    // Exhaustive toolbar coverage: every button in the persistent toolbar,
    // exercised the way a user clicks it. (The suite previously covered only the
    // block dropdown, one alignment, and Bold.)
    //
    // KEY: a toolbar click preserves the editor selection (activate() calls
    // preventDefault on mousedown), so after applying a mark we can toggle it
    // straight off WITHOUT re-selecting. Re-clicking the editable to re-select
    // is what to avoid — inside the sandboxed OOPIF that click times out.
    const tbtn = (page: Page, title: string) =>
      ui(page).locator(`.richtext-toolbar .richtext-tbtn[title='${title}']`);
    const selectAll = (page: Page) => page.keyboard.press("ControlOrMeta+a");

    for (const m of [
      { title: "Bold", tag: "strong" },
      { title: "Italic", tag: "em" },
      { title: "Underline", tag: "u" },
      { title: "Strikethrough", tag: "s" },
      { title: "Inline code", tag: "code" },
    ]) {
      test(`toolbar mark: ${m.title} applies and toggles off`, async ({ page }) => {
        await editor(page).click();
        await page.keyboard.type("format this text");
        await selectAll(page);
        await tbtn(page, m.title).click();
        await expect(editor(page).locator(m.tag)).toContainText("format this text");
        // Selection is still the whole line — toggle straight back off.
        await tbtn(page, m.title).click();
        await expect(editor(page).locator(m.tag)).toHaveCount(0);
      });
    }

    test("toolbar: Undo and Redo buttons", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("keep this");
      await page.keyboard.type(" and this");
      await tbtn(page, "Undo").click();
      await expect(editor(page)).not.toContainText("and this");
      await tbtn(page, "Redo").click();
      await expect(editor(page)).toContainText("and this");
    });

    test("toolbar: alignment right / center / left", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("align me");
      const alignOf = () =>
        editor(page).locator("p").first().evaluate((el: HTMLElement) => getComputedStyle(el).textAlign);
      await tbtn(page, "Align right").click();
      expect(await alignOf()).toBe("right");
      await tbtn(page, "Align center").click();
      expect(await alignOf()).toBe("center");
      // "left" is the schema default → emits no inline style → computes as "start".
      await tbtn(page, "Align left").click();
      expect(["start", "left"]).toContain(await alignOf());
    });

    test("toolbar: Link button opens the popover and applies a link", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("linkable words");
      await selectAll(page);
      await tbtn(page, "Link").click();
      const pop = ui(page).locator(".richtext-linkpop");
      await expect(pop).toBeVisible();
      await pop.locator(".richtext-link-input").fill("example.com");
      await pop.locator(".richtext-link-apply").click();
      // A bare domain is normalized to https://.
      await expect(editor(page).locator("a[href='https://example.com']")).toHaveCount(1);
    });

    for (const c of [
      { title: "Text color", slot: "red" },
      { title: "Highlight", slot: "yellow" },
    ]) {
      test(`toolbar: ${c.title} opens the swatch grid and applies a color`, async ({ page }) => {
        await editor(page).click();
        await page.keyboard.type("color these words");
        await selectAll(page);
        await tbtn(page, c.title).click();
        const grid = ui(page).locator(".richtext-colorgrid");
        await expect(grid).toBeVisible();
        // Positioning guard: the grid must float as a compact popover, not get
        // clipped above the toolbar nor stretch full-width (the two bugs that
        // shipped). Its box sits within the viewport and stays swatch-sized.
        const box = await grid.boundingBox();
        expect(box, "color grid has a layout box").not.toBeNull();
        expect(box!.y, "grid not clipped above the frame").toBeGreaterThanOrEqual(0);
        expect(box!.width, "grid is compact, not full-width-stretched").toBeLessThan(400);
        await grid.locator(`.richtext-swatch[data-slot='${c.slot}']`).click();
        // A styled span now carries the color/background var.
        await expect(editor(page).locator('span[style*="var(--richtext-"]')).toHaveCount(1);
      });
    }

    test("toolbar: Clear formatting strips marks", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("bold then plain");
      await selectAll(page);
      await tbtn(page, "Bold").click();
      await expect(editor(page).locator("strong")).toHaveCount(1);
      // Selection is still the whole line — clear straight away.
      await tbtn(page, "Clear formatting").click();
      await expect(editor(page).locator("strong")).toHaveCount(0);
    });

    test("toolbar block-type: Code and Quote via dropdown", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("becomes code");
      await ui(page).locator(".richtext-tb-select").selectOption("code_block");
      await expect(editor(page).locator("pre code")).toContainText("becomes code");
      await ui(page).locator(".richtext-tb-select").selectOption("blockquote");
      await expect(editor(page).locator("blockquote")).toHaveCount(1);
    });

    test("find & replace: Mod-F finds and Replace all rewrites", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("cat dog cat dog cat");
      await page.keyboard.press("ControlOrMeta+f");
      const panel = ui(page).locator(".richtext-find-panel");
      await expect(panel).toBeVisible();
      await panel.locator(".richtext-find-input").first().fill("cat");
      // Match highlights appear.
      await expect(ui(page).locator(".richtext-find-match")).toHaveCount(3);
      // Replace all cat → tiger.
      await panel.locator(".richtext-find-input").nth(1).fill("tiger");
      await panel.getByText("Replace all", { exact: false }).click();
      await expect(editor(page)).toContainText("tiger dog tiger dog tiger");
    });

    test("keyboard: Bold button works via Enter (no mouse), Alt-Arrow reorders blocks", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("first line");
      await page.keyboard.press("Enter");
      await page.keyboard.type("second line");

      // Alt-ArrowUp moves the current (second) block above the first — a
      // keyboard equivalent of the drag handle.
      await page.keyboard.press("Alt+ArrowUp");
      await expect(editor(page).locator("p").first()).toContainText("second line");

      // The reorder leaves the caret at the moved block's start; select the
      // word forward, extending until the highlight IS the word (a synthetic
      // keypress can drop under load). Then activate Bold from the KEYBOARD.
      for (let i = 0; i < "second".length + 6; i++) {
        const sel = await editor(page).evaluate(
          (el: HTMLElement) => el.ownerDocument.getSelection()?.toString() || ""
        );
        if (sel === "second") break;
        await page.keyboard.press("Shift+ArrowRight");
      }
      const bold = ui(page).locator('.richtext-bubble [title="Bold"]');
      await expect(bold).toBeVisible();
      await bold.focus();
      await page.keyboard.press("Enter");
      await expect(editor(page).locator("p").first().locator("strong")).toContainText("second");
    });

    test("table: Tab at the last cell appends a row", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/table");
      await ui(page).locator(".richtext-slash-item", { hasText: "Table" }).click();
      const rows = editor(page).locator("table tr");
      const before = await rows.count();
      // Tab from the first cell all the way past the last → appends a row.
      for (let i = 0; i < before * 3 + 1; i++) await page.keyboard.press("Tab");
      await expect(rows).toHaveCount(before + 1);
    });

    test("link: a bare domain becomes an https:// link, a bad scheme shows an error", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("visit here");
      for (let i = 0; i < "here".length; i++) await page.keyboard.press("Shift+ArrowLeft");
      await ui(page).locator('.richtext-bubble [title="Link"]').click();

      const input = ui(page).locator(".richtext-link-input");
      await expect(input).toBeVisible();
      await input.fill("example.com/path");
      await input.press("Enter");
      const link = editor(page).locator('a[href="https://example.com/path"]');
      await expect(link).toHaveCount(1);

      // A dangerous scheme is rejected with an inline error, not silently.
      await editor(page).locator("a").click();
      for (let i = 0; i < "here".length; i++) await page.keyboard.press("Shift+ArrowLeft");
      await ui(page).locator('.richtext-bubble [title="Link"]').click();
      await ui(page).locator(".richtext-link-input").fill("javascript:alert(1)");
      await ui(page).locator(".richtext-link-input").press("Enter");
      await expect(ui(page).locator(".richtext-link-error")).toBeVisible();
    });

    test("Escape closes the slash menu and the '/' stays as typed text", async ({ page }) => {
      await editor(page).click();
      await page.keyboard.type("/");
      await expect(ui(page).locator(".richtext-slash-menu")).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(ui(page).locator(".richtext-slash-menu")).toBeHidden();
      await expect(editor(page)).toContainText("/");
    });
  });
}

// ---------------------------------------------------------------------------
// Sandboxed-mount-only journeys (iframe geometry)

test.describe("sandboxed iframe (frame-specific)", () => {
  const frame = (page: Page) => page.frameLocator("iframe");
  const editor = (page: Page) => frame(page).locator(".ProseMirror");

  test.beforeEach(async ({ page, request, baseURL }) => {
    await request.post(`${baseURL}/__gofastr/plugin/richtext/save`, { data: EMPTY_DOC });
    await page.goto("/");
    await page.waitForFunction(() => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __richtextReady?: boolean }) | null;
      return !!f && f.__richtextReady === true;
    });
  });

  test("dismissing the menu by clicking the host page shrinks the frame back", async ({ page }) => {
    // The exact user report: open "/", then interact elsewhere — the menu must
    // close (frame blur produces no editor transaction, so this needs its own
    // listener) and the frame must return to its pre-menu height instead of
    // leaving the page permanently pushed down.
    await editor(page).click();
    await page.keyboard.type("hello");
    const h0 = (await page.locator("iframe").boundingBox())!.height;

    await page.keyboard.press("Enter");
    await page.keyboard.type("/");
    const menu = frame(page).locator(".richtext-slash-menu");
    await expect(menu).toBeVisible();
    await expect
      .poll(async () => (await page.locator("iframe").boundingBox())!.height, {
        message: "frame should grow to fit the open menu",
      })
      .toBeGreaterThan(h0 + 50);

    await page.locator("header h1").click();
    await expect(menu).toBeHidden();
    await expect
      .poll(async () => (await page.locator("iframe").boundingBox())!.height, {
        message: `frame should shrink back to ~${h0}px after dismissal`,
      })
      .toBeLessThanOrEqual(h0 + 40); // one extra empty line remains
  });

  test("slash menu is never clipped by the iframe edge (short doc, caret at bottom)", async ({ page }) => {
    // Replicates the user-reported bug: a few blocks, then "/" on the last
    // line of a short doc — the menu opened below the caret and was CUT OFF at
    // the iframe boundary, because the frame is autosized to content only.
    await editor(page).click();
    await page.keyboard.type("# sdfsd");
    await page.keyboard.press("Enter");
    await page.keyboard.type("dmfnwkre");
    await page.keyboard.press("Enter");

    // Height AFTER the content but BEFORE the menu — the shrink-back baseline
    // (the frame is content-hugging, so the baseline must include the doc).
    await page.waitForTimeout(400); // let the resize RPC settle
    const h0 = (await page.locator("iframe").boundingBox())!.height;
    await page.keyboard.type("/");

    const menu = frame(page).locator(".richtext-slash-menu");
    await expect(menu).toBeVisible();
    await page.waitForTimeout(400); // let the overlay-aware resize RPC round-trip

    const menuBox = (await menu.boundingBox())!;
    const frameBox = (await page.locator("iframe").boundingBox())!;
    const grownHeight = frameBox.height;
    expect(
      menuBox.y + menuBox.height,
      `menu bottom ${menuBox.y + menuBox.height} pokes past iframe bottom ${frameBox.y + frameBox.height} — clipped`
    ).toBeLessThanOrEqual(frameBox.y + frameBox.height + 2);
    // The menu genuinely grew the frame (the whole point of the fix).
    expect(grownHeight).toBeGreaterThan(h0 + 100);

    // Close the menu: the frame must shrink back toward the content height — NOT
    // stay menu-sized (the ratchet bug kept it grown forever). Assert it drops
    // well below the grown height (a persisting "/" line + the toolbar add a
    // little, so allow generous slack over the exact pre-menu baseline).
    await page.keyboard.press("Escape");
    await expect
      .poll(async () => (await page.locator("iframe").boundingBox())!.height, {
        message: `frame should shrink back from the grown ${grownHeight}px toward ~${h0}px`,
      })
      .toBeLessThan(h0 + 80);

    // The proof of usability: reopen and click an item deep in the menu.
    await page.keyboard.press("Backspace"); // remove the "/" Escape left behind
    await page.keyboard.type("/");
    await expect(menu).toBeVisible();
    // The grown frame can extend below the page fold (the demo page has a
    // hero above the editor) and Chromium does not auto-scroll the OUTER page
    // for an element inside the frame — bring the frame's bottom on-screen.
    await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
    await frame(page).locator(".richtext-slash-item", { hasText: "Table" }).click();
    await expect(editor(page).locator("table")).toHaveCount(1);
  });
});

// ---------------------------------------------------------------------------
// Trusted-mount-only journeys (the opt-out's specific promises)

test.describe("trusted in-page mount (mode-specific)", () => {
  const TRUSTED = "/__gofastr/plugin/richtext/trusted";
  const editor = (page: Page) => page.locator(".ProseMirror");

  test.beforeEach(async ({ page, request, baseURL }) => {
    await request.post(`${baseURL}/__gofastr/plugin/richtext/save`, { data: EMPTY_DOC });
    await page.goto(TRUSTED);
    await page.waitForFunction(
      () => (window as unknown as { __richtextTrustedReady?: boolean }).__richtextTrustedReady === true
    );
  });

  test("mounts frameless: no iframe anywhere, editor lives in the page DOM", async ({ page }) => {
    await expect(page.locator("iframe")).toHaveCount(0);
    await expect(editor(page)).toBeVisible();
    // The overlays live inside the scoped wrapper, not on document.body.
    const scoped = await page
      .locator(".gofastr-richtext-trusted .richtext-slash-menu")
      .count();
    expect(scoped).toBe(1);
  });

  test("save round-trips: Mod+S persists, reload restores the doc", async ({ page }) => {
    await editor(page).click();
    await page.keyboard.type("persist me across reloads");
    await page.keyboard.press("ControlOrMeta+s");
    await page.waitForTimeout(500); // save POST is fire-and-forget

    await page.reload();
    await page.waitForFunction(
      () => (window as unknown as { __richtextTrustedReady?: boolean }).__richtextTrustedReady === true
    );
    await expect(editor(page)).toContainText("persist me across reloads");
  });

  test("overlays stay anchored while the page scrolls", async ({ page }) => {
    // The page (not a frame viewport) scrolls in trusted mode, so a fixed-
    // position overlay must follow its anchor. The slash menu re-places
    // against the caret on scroll; the drag handle hides (it re-anchors on
    // the next mouse move — a stale handle must never float mid-page).
    await editor(page).click();
    for (let i = 0; i < 14; i++) {
      await page.keyboard.type(`line ${i}`);
      await page.keyboard.press("Enter");
    }

    // Drag handle: visible on hover, hidden once the page scrolls.
    const firstP = editor(page).locator("p").first();
    const fb = (await firstP.boundingBox())!;
    await page.mouse.move(fb.x + 40, fb.y + fb.height / 2);
    const handle = page.locator(".richtext-draghandle");
    await expect(handle).toBeVisible();
    await page.mouse.wheel(0, 120);
    await expect(handle).toBeHidden();

    // Slash menu: opens at the caret and FOLLOWS it when the page scrolls.
    await page.keyboard.type("/");
    const menu = page.locator(".richtext-slash-menu");
    await expect(menu).toBeVisible();
    const before = (await menu.boundingBox())!;
    await page.mouse.wheel(0, -100); // scroll back up 100px
    await page.waitForTimeout(250);
    const after = (await menu.boundingBox())!;
    const drift = Math.abs(after.y - (before.y + 100));
    expect(drift, `menu drifted ${drift}px from its caret while scrolling`).toBeLessThanOrEqual(8);
  });

  test("theme tokens inherit natively: toggling the page theme restyles the editor with no plugin traffic", async ({ page }) => {
    // The background lives on the scoped wrapper (the rescoped body rule);
    // .ProseMirror itself is transparent.
    const bgOf = () =>
      page
        .locator(".gofastr-richtext-trusted")
        .evaluate((el: HTMLElement) => getComputedStyle(el).backgroundColor);
    await editor(page).click();
    await page.keyboard.type("theme me");
    const before = await bgOf();
    await page.locator("#fui-scheme-toggle").click();
    await expect
      .poll(bgOf, { message: "editor background should follow the page theme instantly" })
      .not.toBe(before);
  });
});
