// User-journey e2e for the genui plugin — generative UI in the cage.
//
// The claim under test is containment, not cleverness: a model composes a view
// out of a FIXED registry of components, and anything outside that registry is
// refused rather than rendered. So the interesting journeys here are the ones
// where a composition is REJECTED, and they are written to fail loudly if the
// validator is ever loosened.
import { test, expect, type Page } from "@playwright/test";

const PORT = Number(process.env.E2E_PORT ?? 8123);
const DEMO = `http://localhost:${PORT}/genui`;

type Mirror = HTMLIFrameElement & {
  __genuiReady?: boolean;
  __genuiState?: "idle" | "pending" | "rendered" | "refused" | "failed";
  __genuiLastId?: string;
  __genuiRenderResult?: { id: string; ok: boolean; nodeCount: number; error?: string };
  __genuiLastAction?: { action: string; nodeId: string };
  __genuiProbes?: { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean };
};

declare global {
  interface Window {
    __gofastrGenuiDemo?: {
      compose(prompt: string): Promise<string>;
      // Test seam: post a composition straight to the frame, skipping the Go
      // validator. It exists to prove the frame's OWN validator works, which
      // is the whole point of validating twice — "the host already checked it"
      // is exactly the assumption that makes a second bug fatal. It grants
      // nothing a host page could not already do to its own frame.
      pushRawComposition(tree: unknown): void;
      lastComposition(): unknown;
      state(): string;
    };
  }
}

const consoleErrors = new Map<Page, string[]>();

test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  consoleErrors.set(page, errors);
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(DEMO);
  await page.waitForFunction(
    () => (document.querySelector(".editor-card iframe") as Mirror | null)?.__genuiReady === true,
    undefined,
    { timeout: 20_000 }
  );
  await page.locator(".editor-card").scrollIntoViewIfNeeded();
});

function expectNoConsoleErrors(page: Page): void {
  const errors = (consoleErrors.get(page) ?? []).filter((e) => !/favicon/i.test(e));
  expect(errors, errors.join("\n")).toEqual([]);
}

const mirror = (page: Page) =>
  page.evaluate(() => {
    const f = document.querySelector(".editor-card iframe") as Mirror | null;
    return {
      state: f?.__genuiState ?? null,
      result: f?.__genuiRenderResult ?? null,
      action: f?.__genuiLastAction ?? null,
    };
  });

// ─── 1. mount + sandbox + probes ────────────────────────────────────────────

test("a compose the server never answers reports failure, not 'nothing composed yet'", async ({ page }) => {
  // The composition is produced HOST-side, so the POST is the whole pipeline.
  // Break only it.
  let intercepted = 0;
  await page.route(
    (u) => u.pathname.endsWith("/genui/compose"),
    (r) => {
      intercepted += 1;
      return r.abort();
    }
  );
  await page.goto("/genui");
  await page.locator("#genui-compose").click();

  await expect.poll(() => intercepted, { timeout: 15_000 }).toBeGreaterThan(0);

  // failGeneration was wired only to the polling paths, so the likeliest
  // failure of all — the compose request not landing — reached nothing. The
  // state stayed idle and the verdict kept reading "nothing composed yet",
  // which says you never asked rather than that it failed.
  await expect
    .poll(
      async () =>
        page.evaluate(() => {
          const f = document.querySelector(".editor-card iframe") as (HTMLIFrameElement & { __genuiState?: string }) | null;
          return f?.__genuiState ?? "";
        }),
      { timeout: 15_000 }
    )
    .toBe("failed");

  await expect(page.locator(".verdict").first()).toContainText(/generation failed/i);
  await expect(page.locator(".verdict").first()).not.toContainText(/nothing composed yet/i);
});

test("mounts sandboxed (allow-scripts, no allow-same-origin) with the frame's own isolation probes passing", async ({ page }) => {
  const frame = page.locator(".editor-card iframe");
  await expect(frame).toHaveAttribute("sandbox", "allow-scripts");
  await expect(frame).toHaveAttribute("referrerpolicy", "no-referrer");

  const probes = await page.evaluate(() => {
    const f = document.querySelector(".editor-card iframe") as Mirror;
    return f.__genuiProbes ?? {};
  });
  expect(probes).toEqual({ cookieEmpty: true, parentBlocked: true, storageBlocked: true });

  expectNoConsoleErrors(page);
});

// ─── 2. a composition renders ───────────────────────────────────────────────

test("a prompt produces a rendered view, composed from registry components only", async ({ page }) => {
  await page.locator("#genui-prompt").fill("show me q3 revenue");
  await page.locator("#genui-compose").click();

  await expect
    .poll(async () => (await mirror(page)).state, { timeout: 20_000, message: "never finished composing" })
    .toBe("rendered");

  const m = await mirror(page);
  expect(m.result?.ok).toBe(true);
  expect(m.result?.nodeCount).toBeGreaterThan(1);

  // The generation is async by design: a placeholder first, the view after.
  // Whatever it composed, it is REAL DOM inside the frame, not a description.
  const frame = page.frameLocator(".editor-card iframe");
  await expect(frame.locator("[data-genui-node]").first()).toBeVisible();

  expectNoConsoleErrors(page);
});

// ─── 3. THE ONE THAT MATTERS: an unknown component is refused ───────────────

test("a composition naming a component outside the registry is refused, not rendered", async ({ page }) => {
  // Straight to the frame, past the Go validator, because the frame's own
  // validator is what this asserts. If the frame ever trusts the bridge, this
  // is the test that goes red.
  await page.evaluate(() => {
    window.__gofastrGenuiDemo!.pushRawComposition({
      schemaVersion: "genui-v1",
      root: { component: "ScriptTag", props: { src: "https://example.com/x.js" } },
    });
  });

  await expect
    .poll(async () => (await mirror(page)).state, { timeout: 15_000, message: "the frame never refused it" })
    .toBe("refused");

  const m = await mirror(page);
  expect(m.result?.ok).toBe(false);
  expect(m.result?.error ?? "").toMatch(/ScriptTag/);

  // Refused means nothing rendered, and the page SAYS so rather than sitting
  // silent — a refusal a user cannot see is indistinguishable from a hang.
  const frame = page.frameLocator(".editor-card iframe");
  await expect(frame.locator("[data-genui-refused]")).toBeVisible();
  // Nothing from the composition reached the DOM. Scoped to the render root:
  // the frame document has its own <script src="genui.js"> like every plugin
  // here, so an unscoped count is 1 and asserts nothing. Caught in review —
  // as first written this would have passed for the wrong reason.
  expect(await frame.locator("#genui-root script").count()).toBe(0);
  expect(await frame.locator("[data-genui-node]").count()).toBe(0);

  expectNoConsoleErrors(page);
});

// ─── 4. props are a closed set too ──────────────────────────────────────────

test("a known component carrying an undeclared prop is refused", async ({ page }) => {
  // The registry is only containment if the PROPS are bounded too: a Text that
  // accepts arbitrary props is a hole big enough to walk through.
  await page.evaluate(() => {
    window.__gofastrGenuiDemo!.pushRawComposition({
      schemaVersion: "genui-v1",
      root: { component: "Text", props: { text: "hi", dangerouslySetInnerHTML: { __html: "<img onerror=1>" } } },
    });
  });

  await expect
    .poll(async () => (await mirror(page)).state, { timeout: 15_000 })
    .toBe("refused");

  const m = await mirror(page);
  expect(m.result?.error ?? "").toMatch(/dangerouslySetInnerHTML|unknown prop/i);

  expectNoConsoleErrors(page);
});

// ─── 5. generated actions are allow-listed ──────────────────────────────────

test("a generated control can only fire an action the host named", async ({ page }) => {
  await page.evaluate(() => {
    window.__gofastrGenuiDemo!.pushRawComposition({
      schemaVersion: "genui-v1",
      root: { component: "Button", props: { label: "Delete everything" }, action: "wipe-database" },
    });
  });

  await expect
    .poll(async () => (await mirror(page)).state, { timeout: 15_000 })
    .toBe("refused");
  expect((await mirror(page)).result?.error ?? "").toMatch(/wipe-database|action/i);

  expectNoConsoleErrors(page);
});

// ─── 6. the frame opens nothing of its own ──────────────────────────────────

test("across a full compose session the frame issues zero network requests", async ({ page }) => {
  const framed: string[] = [];
  page.on("request", (req) => {
    const f = req.frame();
    if (f && f !== page.mainFrame()) framed.push(`${req.method()} ${req.url()}`);
  });

  await page.locator("#genui-prompt").fill("show me q3 revenue");
  await page.locator("#genui-compose").click();
  await expect.poll(async () => (await mirror(page)).state, { timeout: 20_000 }).toBe("rendered");

  // The frame's own document and bundle are fetched by the HOST navigation, so
  // only requests the frame ITSELF initiates count — and there must be none.
  // A generative-UI plugin that could fetch would be the one plugin here where
  // that mattered most: the model's output would be reaching the network.
  const initiated = framed.filter((r) => !/\/__gofastr\/plugin\/genui\/genui\.(html|js|css)/.test(r));
  expect(initiated, initiated.join("\n")).toEqual([]);

  expectNoConsoleErrors(page);
});
