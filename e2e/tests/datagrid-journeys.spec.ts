// User-journey e2e for the datagrid plugin — the volume plugin. Every other
// plugin here moves ONE small document across the bridge; this one moves
// 100,000 rows, so the suite's job is to prove the claim the plugin exists
// to make: sort/filter/paging run in the HOST's Go rows source, the frame
// only ever holds pages, no journey ever ships the whole table across the
// postMessage bridge, and the frame does not RETAIN what it scrolled past
// (the block cache is bounded).
//
// The dataset is a pure function of the row index (example/datagrid.go); the
// helpers below recompute the same formulas in TypeScript so the assertions
// pin EXACT cell values, not shapes.
import { test, expect, type Page, type APIRequestContext } from "@playwright/test";

const GRID = "/datagrid";
const SAVE = "/__gofastr/plugin/datagrid/save";

// ─── deterministic dataset mirror (MUST match example/datagrid.go) ─────────
const ROWS = 100_000;
const FIRST = [
  "Alice", "Bruno", "Carmen", "Dara", "Eli", "Farah", "Gustav", "Hana",
  "Ivan", "Jia", "Kofi", "Lena", "Milo", "Nadia", "Omar", "Priya",
];
const LAST = [
  "Alvarez", "Bishop", "Chen", "Duarte", "Eriksen", "Fontaine", "Gruber", "Haddad",
  "Iversen", "Jimenez", "Kowalski", "Lindqvist", "Moreau", "Nakamura", "Okafor", "Petrov",
];

function rowId(n: number): string {
  return "ROW-" + String(n).padStart(6, "0");
}
function rowAmount(n: number): string {
  const t = (n * 7919) % 9973;
  return `${Math.floor(t / 10)}.${t % 10}`;
}
function rowName(n: number): string {
  return `${FIRST[n % 16]} ${LAST[Math.floor(n / 16) % 16]}`;
}
// The server's stable sort tie-breaks equal amounts by ascending row number,
// so the first row of a descending sort is the FIRST n achieving the max.
function maxAmountRow(): { n: number; amount: string } {
  let bestT = -1;
  let bestN = -1;
  for (let n = 0; n < ROWS; n++) {
    const t = (n * 7919) % 9973;
    if (t > bestT) {
      bestT = t;
      bestN = n;
    }
  }
  return { n: bestN, amount: `${Math.floor(bestT / 10)}.${bestT % 10}` };
}

// The default demo view state, as mounted by example/datagrid.go's demoGridDoc.
// beforeEach resets the persisted doc to this so journeys are independent.
const DEFAULT_DOC = {
  schemaVersion: "datagrid-v1",
  columns: [
    { field: "id", header: "ID", width: 128, sortable: true },
    { field: "name", header: "Name", width: 160, sortable: true, editable: true },
    { field: "email", header: "Email", width: 210 },
    { field: "company", header: "Company", width: 150, sortable: true },
    { field: "city", header: "City", width: 112, sortable: true },
    { field: "amount", header: "Amount", width: 96, type: "number", sortable: true },
    { field: "status", header: "Status", width: 104, editable: true },
  ],
  pageSize: 100,
};

// The frame's block-cache bound, mirrored from datagrid/js/src/grid.ts:
// ceil(MAX_RESIDENT_ROWS / blockSize) blocks, MAX_RESIDENT_ROWS = 2500.
const CACHE_CAP = 25;

// ─── harness ────────────────────────────────────────────────────────────────

function fl(page: Page) {
  return page.frameLocator("iframe");
}

// The iframe element carries the adapter's mirrors (the parent cannot read
// into the opaque frame, so the mirror IS the observability channel):
// __datagridReady, __datagridRowsDelivered (rows that crossed the bridge),
// __datagridMaxRowsDelivered (largest single rowsResult — the per-round-trip
// bridge limit), __datagridLastRowsRequest (the sort/filter the server just
// served), __datagridLastExportUrl. Every evaluate/waitForFunction predicate
// below is stringified into the page, so each one re-queries the iframe
// itself.
type Mirror = HTMLIFrameElement & {
  __datagridReady?: boolean;
  __datagridRowsDelivered?: number;
  __datagridMaxRowsDelivered?: number;
  __datagridRowsRequests?: number;
  __datagridLastRowsRequest?: {
    startRow: number;
    endRow: number;
    sort: { field: string; dir: string }[];
    filter: string;
  };
  __datagridLastExportUrl?: string;
  __datagridLastError?: string;
};

// Console/page errors captured from BEFORE navigation, so boot-time errors
// (script load failures, CSP violations, uncaught exceptions during the
// ready→init handshake) are visible to the assertions instead of arriving
// after the listeners were installed.
const consoleErrors = new WeakMap<Page, string[]>();

async function ready(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as Mirror | null;
      return !!f && f.__datagridReady === true && (f.__datagridRowsDelivered ?? 0) > 0;
    },
    undefined,
    { timeout: 20_000 }
  );
}

async function resetViewState(request: APIRequestContext, baseURL: string): Promise<void> {
  const resp = await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", doc: DEFAULT_DOC, schemaVersion: "datagrid-v1" },
  });
  expect(resp.status(), "resetting the demo view state via POST /save").toBe(200);
}

test.beforeEach(async ({ page, request, baseURL }) => {
  // Listeners FIRST — then navigate. Installed after goto+ready, they can
  // never see a boot error, which is exactly how one ships unnoticed.
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(String(e)));
  consoleErrors.set(page, errors);

  await resetViewState(request, baseURL!);
  await page.goto(GRID);
  await ready(page);
});

// ─── 1. mount + sandbox + no console errors ────────────────────────────────

test("mounts sandboxed (allow-scripts, no allow-same-origin) with no console errors and renders the deterministic first page", async ({ page }) => {
  const frame = fl(page);

  await expect(frame.locator('.ag-row[row-id="ROW-000000"]')).toBeVisible();
  await expect(frame.locator('.ag-row[row-id="ROW-000000"] .ag-cell[col-id="name"]')).toHaveText(
    rowName(0)
  );
  await expect(frame.locator('.ag-row[row-id="ROW-000000"] .ag-cell[col-id="email"]')).toHaveText(
    "user0@example.com"
  );
  await expect(frame.locator('.ag-row[row-id="ROW-000000"] .ag-cell[col-id="amount"]')).toHaveText(
    rowAmount(0)
  );
  // The count line reports the full table size from the server's lastRow.
  await expect(frame.locator("#dg-count")).toHaveText("100,000 rows");

  // The isolation contract: sandbox attr is allow-scripts ONLY.
  const sandbox = await page.locator("iframe").getAttribute("sandbox");
  expect(sandbox).toBe("allow-scripts");

  // The hidden input exists and is empty until a doc change mirrors into it
  // (the adapter resolves the field name from the marker's
  // data-fui-plugin-field — the default is "datagrid_doc").
  const hidden = page.locator('form#fui-demo-form input[name="datagrid_doc"]');
  await expect(hidden).toHaveCount(1);

  const errors = consoleErrors.get(page) ?? [];
  expect(errors.filter((e) => !/favicon/i.test(e))).toEqual([]);
});

// ─── 2. the volume claim: deep scroll, few rows delivered ──────────────────

test("scrolling to row 50,000 renders the correct cells while the bridge delivers only a tiny fraction of the table", async ({ page }) => {
  const frame = fl(page);

  // Jump-scroll the grid's real scroll container to ~row 50,000 — a normal
  // "drag the scrollbar to the middle" session, at the DOM level.
  await frame.locator(".ag-grid-viewport").evaluate((el) => {
    el.scrollTop = Math.floor(0.5 * (el.scrollHeight - el.clientHeight));
  });
  await expect(frame.locator('[row-id="ROW-050000"]')).toBeVisible({ timeout: 10_000 });

  const cell = (col: string) =>
    frame.locator(`[row-id="ROW-050000"] .ag-cell[col-id="${col}"]`);
  await expect(cell("name")).toHaveText(rowName(50_000));
  await expect(cell("email")).toHaveText("user50000@example.com");
  await expect(cell("amount")).toHaveText(rowAmount(50_000));

  // A normal scroll session continues: nudge down ~8k rows, back up ~3k.
  for (const frac of [0.58, 0.55]) {
    await frame.locator(".ag-grid-viewport").evaluate((el, f) => {
      el.scrollTop = Math.floor(f * (el.scrollHeight - el.clientHeight));
    }, frac);
    await page.waitForTimeout(400);
  }

  // THE assertion, both halves: the rows that crossed the bridge this
  // session stay far below the 100,000-row table (measured locally:
  // ~300–900 rows), AND no single response exceeded the per-round-trip
  // bridge limit (500 rows) — the session total alone cannot prove the cap.
  const delivered = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return { total: f?.__datagridRowsDelivered ?? -1, max: f?.__datagridMaxRowsDelivered ?? -1 };
  });
  test.info().annotations.push({
    type: "rows-delivered",
    description: `${delivered.total} of ${ROWS} rows crossed the bridge during the scroll session (max single response: ${delivered.max})`,
  });
  expect(delivered.total).toBeGreaterThan(0);
  expect(delivered.total).toBeLessThanOrEqual(5_000);
  expect(delivered.max, "no single bridge round trip may exceed the 500-row page cap").toBeGreaterThan(0);
  expect(delivered.max).toBeLessThanOrEqual(500);
});

// ─── 3. the retention claim: the block cache is bounded ────────────────────

test("deep-scrolling past the cache bound leaves the frame holding at most the block cap, per AG Grid's own cache state", async ({ page }) => {
  const frame = fl(page);

  // Jump across MANY more distinct blocks than the cap (25): 36 jumps
  // spread over the table each land in a different 100-row block, so the
  // session requests well over 25 blocks in total.
  for (let i = 1; i <= 36; i++) {
    const before = await page.evaluate(
      () => (document.querySelector("iframe") as Mirror | null)?.__datagridRowsRequests ?? 0
    );
    await frame.locator(".ag-grid-viewport").evaluate((el, f) => {
      el.scrollTop = Math.floor(f * (el.scrollHeight - el.clientHeight));
    }, i / 37);
    // Wait for this jump's request before the next, so the session really
    // fetches 30+ distinct blocks rather than coalescing jumps.
    await page.waitForFunction(
      (n) => (document.querySelector("iframe") as Mirror | null)?.__datagridRowsRequests ?? 0 > n,
      before,
      { timeout: 10_000 }
    );
  }

  // Read AG Grid's OWN cache state from inside the frame. Rows delivered
  // over the bridge is NOT evidence of what the frame retains — this is.
  const gridFrame = page.frames().find((f) => f.url().includes("grid.html"));
  expect(gridFrame, "the grid frame is attached").toBeTruthy();
  const state = await gridFrame!.evaluate(() => {
    const hook = (
      window as unknown as { __datagridCacheState?: () => { count: number; cap: number } }
    ).__datagridCacheState;
    return hook ? hook() : null;
  });
  expect(state, "the frame exposes its cache state").not.toBeNull();
  expect(state!.cap).toBe(CACHE_CAP);
  expect(state!.count).toBeGreaterThan(0);
  expect(
    state!.count,
    `resident blocks (${state!.count}) must stay at or below the cap (${state!.cap}) after requesting 36+`
  ).toBeLessThanOrEqual(state!.cap);
});

// ─── 4. server-side sort: the first page changes ───────────────────────────

test("sorting by amount reorders the whole table server-side — the first page comes back sorted", async ({ page }) => {
  const frame = fl(page);

  await frame.locator('.ag-header-cell[col-id="amount"]').click();
  await expect(frame.locator('[row-id="ROW-000000"]')).toBeVisible({ timeout: 10_000 });
  await expect(frame.locator('[row-id="ROW-000000"] .ag-cell[col-id="amount"]')).toHaveText("0.0");
  await expect(frame.locator('.ag-header-cell[col-id="amount"]')).toHaveAttribute(
    "aria-sort",
    "ascending"
  );

  // Proof the sort crossed to the server: the host-side mirror of the LAST
  // rows request carries the sort model, and the first page refetched from
  // row 0 (AG Grid purged its cache — the frame never saw the ordered table).
  const lastAsc = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f!.__datagridLastRowsRequest!;
  });
  expect(lastAsc.startRow).toBe(0);
  expect(lastAsc.sort).toEqual([{ field: "amount", dir: "asc" }]);

  // A frame-side sort could only reorder rows the frame HELD — pages around
  // the top of the table. The numeric max lives at maxAmountRow().n; if the
  // sort had run client-side, that row could never appear first on desc.
  await frame.locator('.ag-header-cell[col-id="amount"]').click();
  const max = maxAmountRow();
  await expect(frame.locator(`[row-id="${rowId(max.n)}"]`)).toBeVisible({ timeout: 10_000 });
  await expect(frame.locator(`[row-id="${rowId(max.n)}"] .ag-cell[col-id="amount"]`)).toHaveText(
    max.amount
  );
  await expect(frame.locator('.ag-header-cell[col-id="amount"]')).toHaveAttribute(
    "aria-sort",
    "descending"
  );

  // Strengthened: the FIRST row of the descending view is the max row
  // (visible-somewhere is not first), and the mirrored request carries the
  // mirrored sort model refetched from row 0.
  await expect(frame.locator('.ag-row[row-index="0"]')).toHaveAttribute("row-id", rowId(max.n));
  const lastDesc = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f!.__datagridLastRowsRequest!;
  });
  expect(lastDesc.startRow).toBe(0);
  expect(lastDesc.sort).toEqual([{ field: "amount", dir: "desc" }]);

  // The hidden field mirrors the view state (docChanged → the adapter writes
  // the input named by the marker's data-fui-plugin-field) — the form-submit
  // round-trip the mount exists for.
  await expect(page.locator('form#fui-demo-form input[name="datagrid_doc"]')).toHaveValue(
    /"sort":\[\{"field":"amount","dir":"desc"\}\]/,
    { timeout: 5_000 }
  );
});

// ─── 5. CSV export runs host-side for the sorted view ──────────────────────

test("exporting CSV produces a host-side file of the whole table and starts the download in the host page", async ({ page }) => {
  const frame = fl(page);

  const downloadPromise = page.waitForEvent("download", { timeout: 20_000 }).catch(() => null);
  await frame.locator("#dg-export").click();
  await expect(frame.locator("#dg-status")).toContainText("Export ready (100,000 rows)", {
    timeout: 20_000,
  });
  const download = await downloadPromise;
  expect(download, "the host page started the download the frame cannot").not.toBeNull();

  // Read the produced file back through the mirrored URL and pin it: full
  // table, deterministic content, default (id) order.
  const url = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f?.__datagridLastExportUrl;
  });
  expect(url).toBeTruthy();
  const csv = await page.evaluate(async (u) => {
    const r = await fetch(u!);
    return { status: r.status, text: await r.text() };
  }, url);
  expect(csv.status).toBe(200);
  const lines = csv.text.trim().split("\n");
  expect(lines.length, "header + every row").toBe(ROWS + 1);
  expect(lines[0]).toBe("ID,Name,Email,Company,City,Amount,Status");
  expect(lines[1]).toBe(
    `${rowId(0)},${rowName(0)},user0@example.com,Northwind,Lisbon,${rowAmount(0)},active`
  );
  expect(lines[ROWS]).toContain(rowId(ROWS - 1));
});

// ─── 6. cell edit round-trips through the host and survives a reload ──────

test("editing a cell round-trips through the host and survives a reload", async ({ page }) => {
  const frame = fl(page);

  await expect(frame.locator('[row-id="ROW-000001"]')).toBeVisible();
  const nameCell = frame.locator('.ag-row[row-id="ROW-000001"] .ag-cell[col-id="name"]');
  await nameCell.dblclick();
  const editor = frame.locator(".ag-cell-editor input, .ag-cell-inline-editing input");
  await expect(editor).toBeVisible({ timeout: 5_000 });
  await editor.fill("Edited Name");
  await editor.press("Enter");

  // The write crossed the bridge and back: AG Grid does not persist an edit
  // itself on a valueGetter column (its valueSetter is a no-op by design),
  // so the cell can only show the new value because the requestCellWrite
  // round trip RESOLVED and the frame mutated its page copy. The transient
  // "Saved" status line is deliberately not asserted — it is UI chrome that
  // a re-render can reset; the durable proof is the reload below.
  await expect(nameCell).toHaveText("Edited Name", { timeout: 10_000 });
  const writeError = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f?.__datagridLastError ?? null;
  });
  expect(writeError).toBeNull();

  // Reload: the edit must come back from the HOST (the demo dataset's edit
  // overlay), proving the write persisted server-side, not in the frame.
  await page.reload();
  await ready(page);
  const nameAfter = frame.locator('.ag-row[row-id="ROW-000001"] .ag-cell[col-id="name"]');
  await expect(nameAfter).toHaveText("Edited Name", { timeout: 10_000 });
  // And a neighbour is untouched.
  await expect(
    frame.locator('.ag-row[row-id="ROW-000002"] .ag-cell[col-id="name"]')
  ).toHaveText(rowName(2));

  const errors = consoleErrors.get(page) ?? [];
  expect(errors.filter((e) => !/favicon/i.test(e))).toEqual([]);
});
