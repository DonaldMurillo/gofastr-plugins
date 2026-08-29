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
  const serious = results.violations
    .filter((v) => v.impact === "serious" || v.impact === "critical")
    // Drop color-contrast findings INSIDE a sandboxed frame, and only those.
    //
    // axe resolves an element's background by walking its ancestors. A plugin
    // frame is an opaque origin, so the walk cannot cross out of it, and axe
    // falls back to assuming a white canvas. The calendar toolbar was reported
    // at 1.12:1 — #f4f1ed on #ffffff — where #ffffff is that assumption, not a
    // colour anything actually painted.
    //
    // The frame's real palette is coherent and was captured from the failing CI
    // run itself: __pluginTheme reported --color-text oklch(0.96 0.006 80) on
    // --color-surface oklch(0.17 0.006 75), which is a strong contrast. Both
    // axe 4.12 and 4.13 handle oklch correctly on a same-document page, so the
    // colour space is not the problem either; the frame boundary is.
    //
    // Every other rule still applies inside the frame, and contrast on the HOST
    // page is still enforced. Tracked in #36.
    .filter((v) => !(v.id === "color-contrast" && v.nodes.every((n) => n.target[0] === "iframe")));
  const detail = serious
    .map((v) => `[${v.impact}] ${v.id}: ${v.help}\n  ${v.nodes.map((n) => n.target.join(" ")).join("\n  ")}`)
    .join("\n");

  // A contrast violation reports the two colours it compared and nothing about
  // WHY the element had them. That is the whole question when a frame is
  // themed over a postMessage bridge: which tokens actually landed, and did the
  // host and the frame agree on the scheme. Reproducing it took several CI
  // rounds precisely because the failure carried none of that. Dump it here so
  // the next failure explains itself on the first look.
  let themeDump = "";
  if (serious.some((v) => v.id === "color-contrast")) {
    themeDump = await page
      .evaluate(() => {
        const names = [
          "--color-text", "--color-surface", "--color-background",
          "--color-border", "--color-surface-soft",
        ];
        const read = (root: Document) => {
          const cs = getComputedStyle(root.documentElement);
          return Object.fromEntries(names.map((n) => [n, cs.getPropertyValue(n).trim()]));
        };
        const iframe = document.querySelector("iframe") as HTMLIFrameElement | null;
        const mirrors = iframe as unknown as Record<string, unknown> | null;
        let frame: unknown = "unreachable (opaque origin)";
        try {
          if (iframe?.contentDocument) frame = read(iframe.contentDocument);
        } catch {
          /* opaque origin — expected for sandboxed plugins */
        }
        return JSON.stringify(
          {
            hostScheme: document.documentElement.getAttribute("data-color-scheme"),
            prefersDark: matchMedia("(prefers-color-scheme: dark)").matches,
            hostTokens: read(document),
            frameTokens: frame,
            frameThemeMirror: mirrors
              ? Object.keys(mirrors).filter((k) => k.startsWith("__") && k.toLowerCase().includes("theme"))
                  .map((k) => [k, mirrors[k]])
              : [],
          },
          null,
          2
        );
      })
      .catch((e) => `theme dump failed: ${String(e)}`);
  }
  expect(
    serious,
    `${label}: serious/critical a11y violations:\n${detail}${themeDump ? `\n\ntheme state at audit time:\n${themeDump}` : ""}`
  ).toEqual([]);
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

test("a11y: scanner demo page (host chrome + mount)", async ({ page }) => {
  await page.goto("/scanner");
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __scannerReady?: boolean }) | null;
      return !!f && f.__scannerReady === true;
    },
    undefined,
    { timeout: 25_000 }
  );
  // The file input is deliberately visually hidden behind a styled <label>, so
  // this page is the one where a hidden-but-labelled control has to survive the
  // audit rather than be waved through.
  await audit(page, "scanner demo");
});

test("a11y: calendar demo page (host chrome + mount)", async ({ page }) => {
  await page.goto("/calendar");
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & {
        __calendarReady?: boolean;
        __calendarTheme?: { scheme?: string };
      }) | null;
      // Wait for the THEME to be applied, not merely for the frame to be
      // ready. The host bridges token values as a snapshot and re-sends them
      // when the color-scheme bootstrap resolves, so a page audited between
      // the two carries a torn palette — CI caught the frame with dark-theme
      // text on a light-theme surface, 1.12:1, which neither theme produces on
      // its own. __calendarTheme is set on themeApplied, after the frame has
      // written the bridged block.
      return !!f && f.__calendarReady === true && !!f.__calendarTheme?.scheme;
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

// ─── in-frame contrast, computed from the bridged tokens ───────────────────
//
// axe reports color-contrast findings inside a sandboxed frame, and they are
// not trustworthy: it resolves a background by walking ancestors, cannot walk
// out of an opaque origin, and falls back to assuming a white canvas. That is
// why those findings are filtered in `audit` above (#36).
//
// Filtering them left plugin frames with NO contrast coverage at all, which is
// worse than a noisy check. This restores it by measuring the thing that
// actually determines legibility: the token values the host bridged in.
//
// Colours arrive as oklch(), which neither engine serialises to rgb and which
// hand-rolled parsers get wrong. So the frame paints each one to a 1x1 canvas
// and reads the pixel back — the browser does the conversion, in whatever
// colour space it actually renders, and the answer is true sRGB bytes.

/** WCAG 2.1 relative luminance from sRGB bytes. */
function luminance([r, g, b]: [number, number, number]): number {
  const f = (v: number) => {
    const c = v / 255;
    return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}

function contrast(a: [number, number, number], b: [number, number, number]): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** Resolve the frame's :root tokens to sRGB bytes, via a 1x1 canvas. */
async function frameTokenRGB(page: Page, names: string[]): Promise<Record<string, [number, number, number] | null>> {
  return page
    .frameLocator("iframe")
    .locator("body")
    .evaluate((_el, tokenNames: string[]) => {
      const root = getComputedStyle(document.documentElement);
      const canvas = document.createElement("canvas");
      canvas.width = 1;
      canvas.height = 1;
      const ctx = canvas.getContext("2d", { willReadFrequently: true });
      const out: Record<string, [number, number, number] | null> = {};
      for (const name of tokenNames) {
        const value = root.getPropertyValue(name).trim();
        if (!value || !ctx) {
          out[name] = null;
          continue;
        }
        ctx.clearRect(0, 0, 1, 1);
        ctx.fillStyle = "#000";
        ctx.fillStyle = value; // ignored if unparseable, leaving the probe colour
        ctx.fillRect(0, 0, 1, 1);
        const d = ctx.getImageData(0, 0, 1, 1).data;
        out[name] = [d[0], d[1], d[2]];
      }
      return out;
    }, names);
}

const CONTRAST_PAIRS: [string, string, number][] = [
  // [foreground, background, minimum ratio]
  ["--color-text", "--color-surface", 4.5],
  ["--color-text", "--color-background", 4.5],
  // Muted text is still body text under WCAG; it gets the same floor.
  ["--color-text-muted", "--color-surface", 4.5],
];

/** Wait for the frame to have been handed a theme, not merely mounted. */
async function waitForBridgedTheme(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & Record<string, unknown>) | null;
      if (!f) return false;
      return Object.keys(f).some((k) => k.startsWith("__") && k.toLowerCase().includes("theme"));
    },
    undefined,
    { timeout: 25_000 }
  );
}

/**
 * Switch the host page's scheme and let the new tokens re-bridge.
 *
 * Returns the scheme actually in effect, which the caller must check: a test
 * that asks for dark, silently gets light, and then passes is measuring the
 * light theme twice.
 */
async function setScheme(page: Page, scheme: "light" | "dark"): Promise<string | undefined> {
  const current = await page.evaluate(() => document.documentElement.dataset.colorScheme);
  if (current !== scheme) {
    const toggle = page.locator("#fui-scheme-toggle");
    if (await toggle.count()) {
      await toggle.first().click({ timeout: 5_000 });
    } else {
      await page.evaluate((sc) => {
        document.documentElement.dataset.colorScheme = sc;
      }, scheme);
    }
    await page.waitForTimeout(500); // tokens re-bridge into the frame
  }
  return page.evaluate(() => document.documentElement.dataset.colorScheme);
}

for (const plugin of ["datagrid", "chart", "logstream", "imageedit", "formbuilder", "calendar", "scanner"]) {
  // BOTH schemes, because the bug this exists to catch was a DARK one: a
  // partial dark palette landing on the frame's light fallbacks rendered
  // near-white text on white at 1.12:1, and a light-only check cannot see it
  // (gofastr#271). calendar carried a hand-declared token block as the
  // workaround; deleting that block is only safe because this runs dark.
  for (const scheme of ["light", "dark"] as const) {
    test(`contrast: ${plugin} frame tokens meet WCAG AA (${scheme})`, async ({ page }) => {
      await page.goto(`/${plugin}`);
      await waitForBridgedTheme(page);

      const applied = await setScheme(page, scheme);
      expect(applied, `asked for ${scheme}, page is in ${applied}`).toBe(scheme);
      await waitForBridgedTheme(page);

      const names = [...new Set(CONTRAST_PAIRS.flatMap(([a, b]) => [a, b]))];
      const rgb = await frameTokenRGB(page, names);

      // A token that does not resolve used to `continue`, which meant a frame
      // receiving NO palette passed every pair by measuring nothing. The whole
      // failure mode here is a palette arriving incomplete, so an unresolved
      // token is the finding, not a reason to skip.
      const unresolved = names.filter((n) => !rgb[n]);
      expect(
        unresolved,
        `${plugin} (${scheme}): the frame resolved no value for ${unresolved.join(", ")}, ` +
          `so the palette did not arrive intact`
      ).toEqual([]);

      for (const [fg, bg, min] of CONTRAST_PAIRS) {
        const f = rgb[fg];
        const b = rgb[bg];
        if (!f || !b) continue; // unreachable: the assertion above already failed
        const ratio = contrast(f, b);
        expect(
          ratio,
          `${plugin} (${scheme}): ${fg} on ${bg} is ${ratio.toFixed(2)}:1 (rgb ${f} on ${b}), want >= ${min}:1`
        ).toBeGreaterThanOrEqual(min);
      }
    });
  }
}
