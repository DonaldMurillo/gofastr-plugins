// User-journey e2e for the formbuilder plugin — the plugin whose output the
// framework itself consumes. The suite's job is to prove the whole loop:
//
//   1. the builder boots inside the opaque sandbox (probes pass, no console
//      errors, sandbox="allow-scripts" and NOT allow-same-origin);
//   2. a field designed in the frame autosaves over the bridge, the SERVER
//      validates it, and it survives a reload;
//   3. the server REFUSES a bad schema designed in the frame (duplicate
//      name) with the specific code, shown live on the demo page;
//   4. the live route renders the saved schema as a real ui.Form with all
//      seven field types;
//   5. the SERVER rejects a submit that violates the schema — from a real
//      browser, with no client-side validation to catch it first — and
//      accepts a clean one.
import { test, expect, type APIRequestContext, type Page } from "@playwright/test";

const DESIGN = "/formbuilder";
const LIVE = "/formbuilder/live";
const SAVE = "/__gofastr/plugin/formbuilder/save";

// The 7-type schema the live-form journeys mount. Mirrors the Go test's
// sevenTypeDoc: every field type, most rules.
const SEVEN_TYPE = {
  version: "formbuilder-v1",
  fields: [
    { type: "text", name: "full_name", label: "Full name", required: true,
      rules: { minLength: 2, maxLength: 80, pattern: "^[A-Z][a-z]+ [A-Z][a-z]+$" } },
    { type: "email", name: "email", label: "Email", required: true },
    { type: "number", name: "seats", label: "Seats", rules: { min: 1, max: 20 } },
    { type: "textarea", name: "notes", label: "Notes", rules: { maxLength: 200 } },
    { type: "select", name: "plan", label: "Plan", options: ["starter", "scale"] },
    { type: "checkbox", name: "terms", label: "Accept terms", required: true },
    { type: "date", name: "start", label: "Start date" },
  ],
};

// The default demo canvas (formbuilder/demo.go defaultDemoDoc) — what a
// fresh mount shows before any save.
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

type Mirror = HTMLIFrameElement & {
  __formbuilderReady?: boolean;
  __formbuilderProbes?: { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } | null;
  __formbuilderSave?: { ok: boolean; code?: string; fields?: number; rules?: number };
  __formbuilderSaves?: number;
};

const consoleErrors = new WeakMap<Page, string[]>();

test.beforeEach(async ({ page }) => {
  consoleErrors.set(page, []);
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.get(page)!.push(msg.text());
  });
  page.on("pageerror", (err) => consoleErrors.get(page)!.push(String(err)));
});

// The journeys deliberately trigger HTTP 400 (a refused save) and 422 (a
// refused submit); the browser logs those responses as console errors on its
// own. They are the product working, not JS faults — filter them out of the
// "no console errors" assertion. Anything else (a 404 script load, a CSP
// violation, an exception) still fails.
function unexpectedConsoleErrors(page: Page): string[] {
  return consoleErrors.get(page)!.filter(
    (m) => !/Failed to load resource: the server responded with a status of (400|422)/.test(m)
  );
}

async function resetBaseline(request: APIRequestContext, baseURL: string | undefined): Promise<void> {
  const resp = await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", doc: DEMO_DOC, schemaVersion: "formbuilder-v1" },
  });
  expect(resp.ok()).toBeTruthy();
}

function fl(page: Page) {
  return page.frameLocator("iframe");
}

async function ready(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as Mirror | null;
      return !!f && f.__formbuilderReady === true;
    },
    undefined,
    { timeout: 15_000 }
  );
}

async function waitForSaveOK(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as Mirror | null;
      return !!f && !!f.__formbuilderSave && f.__formbuilderSave.ok === true;
    },
    undefined,
    { timeout: 15_000 }
  );
}

async function waitForSaveRefusal(page: Page, code: string): Promise<void> {
  await page.waitForFunction(
    (want) => {
      const f = document.querySelector("iframe") as Mirror | null;
      return !!f && !!f.__formbuilderSave && f.__formbuilderSave.ok === false && f.__formbuilderSave.code === want;
    },
    code,
    { timeout: 15_000 }
  );
}

// ─── 1. the builder boots in the cage ────────────────────────────────────────

test("a schema the server never receives is refused out loud, not silently lost", async ({ page }) => {
  // The schema IS the document here: Go validates and stores every save, and
  // the frame keeps nothing durable. So a save the server never receives must
  // not read like one it accepted.
  let attempts = 0;
  await page.route("**/__gofastr/plugin/formbuilder/save", (r) => {
    attempts += 1;
    return r.abort();
  });
  await page.goto("/formbuilder");

  const frame = fl(page);
  const status = frame.locator("#fb-status");
  await expect(status).not.toHaveText(/Loading/i, { timeout: 25_000 });

  await frame.locator('[data-fui-fb-add="text"]').click();

  // Assert the save was ATTEMPTED before judging the outcome: a journey that
  // never fires the request passes every assertion below by doing nothing,
  // which is how two earlier probes of other plugins fooled me.
  await expect.poll(() => attempts, { timeout: 10_000 }).toBeGreaterThan(0);

  await expect(status).toContainText(/refused|failed|error/i, { timeout: 10_000 });
  await expect(status, "a failed save must not read as a saved one").not.toContainText(/Saved/i);
});

test("the builder boots inside the opaque sandbox, clean", async ({ page, request, baseURL }) => {
  await resetBaseline(request, baseURL);
  await page.goto(DESIGN);
  await ready(page);

  const iframe = page.locator("iframe");
  const sandbox = await iframe.getAttribute("sandbox");
  expect(sandbox).toContain("allow-scripts");
  expect(sandbox).not.toContain("allow-same-origin");

  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    const p = f?.__formbuilderProbes;
    return !!p && p.cookieEmpty === true && p.parentBlocked === true && p.storageBlocked === true;
  }, undefined, { timeout: 10_000 });

  // The demo canvas round-trips: the frame shows the persisted fields.
  await expect(fl(page).locator(".fb-item")).toHaveCount(5);

  expect(unexpectedConsoleErrors(page)).toEqual([]);
});

// ─── 2. design in the frame → server validates → reload round-trips ─────────

test("a field designed in the frame is validated by Go and survives a reload", async ({ page, request, baseURL }) => {
  await resetBaseline(request, baseURL);
  await page.goto(DESIGN);
  await ready(page);

  // Add a number field through the frame UI.
  await fl(page).locator('[data-fui-fb-add="number"]').click();
  await expect(fl(page).locator(".fb-item")).toHaveCount(6);

  // Give it a name and a range rule through the property panel.
  await fl(page).locator('[data-fui-fb-prop="name"]').fill("headcount");
  await fl(page).locator('.fb-prop-pair input[type="number"]').first().fill("1");
  await fl(page).locator('.fb-prop-pair input[type="number"]').nth(1).fill("50");

  // Autosave crosses the bridge; the SERVER's verdict lands on the mirror.
  await waitForSaveOK(page);
  const saved = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f?.__formbuilderSave ?? null;
  });
  expect(saved?.fields).toBe(6);
  // demo doc rules: full_name(3) + email(1) + role(1) + pitch(3) = 8, plus
  // headcount's min+max = 10.
  expect(saved?.rules).toBe(10);

  // Reload: the schema comes back intact — 6 fields, headcount among them.
  await page.reload();
  await ready(page);
  await expect(fl(page).locator(".fb-item")).toHaveCount(6);
  await expect(fl(page).locator(".fb-item-name", { hasText: "headcount" })).toBeVisible();

  expect(unexpectedConsoleErrors(page)).toEqual([]);
});

// ─── 3. the server refuses a bad schema designed in the frame ───────────────

test("a duplicate name designed in the frame is refused by Go, live on the page", async ({ page, request, baseURL }) => {
  await resetBaseline(request, baseURL);
  await page.goto(DESIGN);
  await ready(page);

  // Add a text field and collide its name with full_name.
  await fl(page).locator('[data-fui-fb-add="text"]').click();
  await expect(fl(page).locator(".fb-item")).toHaveCount(6);
  await fl(page).locator('[data-fui-fb-prop="name"]').fill("full_name");

  // The frame does not block the edit — the SERVER refuses the save, and the
  // refusal code is mirrored back onto the iframe element and the proof strip.
  await waitForSaveRefusal(page, "E_DUPLICATE_NAME");
  const verdict = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f?.__formbuilderSave ?? null;
  });
  expect(verdict?.code).toBe("E_DUPLICATE_NAME");

  // The demo page's proof strip shows the refusal, not a success.
  await expect(page.locator("#fb-live-verdict")).toHaveText(/Refused: E_DUPLICATE_NAME/);
  await expect(page.locator("#fb-live-verdict")).toHaveClass(/is-bad/);

  // And nothing was persisted: a reload shows the five baseline fields.
  await page.reload();
  await ready(page);
  await expect(fl(page).locator(".fb-item")).toHaveCount(5);

  expect(unexpectedConsoleErrors(page)).toEqual([]);
});

// ─── 4. the live form renders the saved schema ──────────────────────────────

test("the live form renders the saved schema: all seven field types", async ({ page, request, baseURL }) => {
  const resp = await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", doc: SEVEN_TYPE, schemaVersion: "formbuilder-v1" },
  });
  expect(resp.ok()).toBeTruthy();

  await page.goto(LIVE);
  await expect(page.locator("#fb-verdict")).toHaveAttribute("data-verdict", "fresh");
  await expect(page.locator("#fb-verdict")).toContainText("7 fields · 9 rules");

  const form = page.locator("form");
  await expect(form.locator('input[type="text"][name="full_name"]')).toHaveCount(1);
  await expect(form.locator('input[type="email"][name="email"]')).toHaveCount(1);
  await expect(form.locator('input[type="number"][name="seats"]')).toHaveCount(1);
  await expect(form.locator('textarea[name="notes"]')).toHaveCount(1);
  await expect(form.locator('select[name="plan"]')).toHaveCount(1);
  await expect(form.locator('select[name="plan"] option')).toHaveCount(3); // placeholder + 2
  await expect(form.locator('input[type="checkbox"][name="terms"]')).toHaveCount(1);
  await expect(form.locator('input[type="date"][name="start"]')).toHaveCount(1);

  // No native constraint attributes: enforcement is the server's. If a
  // `required` or `pattern` attribute appeared here, the browser would start
  // answering before Go ever sees the values.
  for (const sel of ['input[name="full_name"]', 'input[name="seats"]', 'input[name="terms"]']) {
    await expect(form.locator(sel)).not.toHaveAttribute("required");
  }
  await expect(form.locator('input[name="full_name"]')).not.toHaveAttribute("pattern");

  expect(unexpectedConsoleErrors(page)).toEqual([]);
});

// ─── 5. the server rejects what the frame would have blocked ────────────────

test("submitting garbage: the SERVER rejects it, then accepts a clean submit", async ({ page, request, baseURL }) => {
  const resp = await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", doc: SEVEN_TYPE, schemaVersion: "formbuilder-v1" },
  });
  expect(resp.ok()).toBeTruthy();

  await page.goto(LIVE);

  // Fill values that break the schema's rules. Nothing stops us in the
  // browser — no required attr, no pattern attr — so the click goes straight
  // to the server.
  await page.locator('input[name="full_name"]').fill("ada lovelace"); // breaks the pattern
  await page.locator('input[name="email"]').fill("not-an-email");
  await page.locator('input[name="seats"]').fill("99");
  await page.locator('textarea[name="notes"]').fill("x".repeat(201));
  await page.locator('select[name="plan"]').selectOption("starter");
  // start left empty (optional); a malformed date STRING cannot even cross a
  // real date input — that path is covered by the Go test's direct HTTP POST.
  // terms left unchecked.

  const [rejectResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(LIVE) && r.request().method() === "POST"),
    page.locator('button[type="submit"]').click(),
  ]);
  expect(rejectResp.status()).toBe(422);
  await expect(page.locator("#fb-verdict")).toHaveAttribute("data-verdict", "rejected");
  await expect(page.locator("#fb-verdict")).toContainText("Server rejected — HTTP 422");
  // The form-level error summary and the per-field errors are the server's.
  await expect(page.locator(".ui-form")).toContainText("does not match the required pattern");
  await expect(page.locator(".ui-form")).toContainText("Enter a valid email address.");
  await expect(page.locator(".ui-form")).toContainText("Must be at most 20.");
  await expect(page.locator(".ui-form")).toContainText("This box must be checked.");

  // A crafted value cannot even sneak through select membership: the option
  // exists only client-side.
  await page.locator('select[name="plan"]').selectOption({ label: "Choose…" });
  await page.evaluate(() => {
    const sel = document.querySelector('select[name="plan"]') as HTMLSelectElement | null;
    if (sel) {
      const foreign = document.createElement("option");
      foreign.value = "enterprise";
      foreign.text = "enterprise";
      sel.appendChild(foreign);
      sel.value = "enterprise";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    }
  });
  const [rejectResp2] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(LIVE) && r.request().method() === "POST"),
    page.locator('button[type="submit"]').click(),
  ]);
  expect(rejectResp2.status()).toBe(422);
  await expect(page.locator(".ui-form")).toContainText("Choose one of the listed options.");

  // Clean values: accepted, with the submitted data echoed back.
  await page.locator('input[name="full_name"]').fill("Ada Lovelace");
  await page.locator('input[name="email"]').fill("ada@example.com");
  await page.locator('input[name="seats"]').fill("5");
  await page.locator('textarea[name="notes"]').fill("");
  await page.locator('select[name="plan"]').selectOption("starter");
  await page.locator('input[name="terms"]').check();
  await page.locator('input[name="start"]').fill("2026-09-01");

  const [acceptResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(LIVE) && r.request().method() === "POST"),
    page.locator('button[type="submit"]').click(),
  ]);
  expect(acceptResp.status()).toBe(200);
  await expect(page.locator("#fb-verdict")).toHaveAttribute("data-verdict", "accepted");
  await expect(page.locator(".live-accepted")).toContainText("Ada Lovelace");
  await expect(page.locator(".live-accepted")).toContainText("ada@example.com");

  expect(unexpectedConsoleErrors(page)).toEqual([]);
});
