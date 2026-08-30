// User-journey e2e for the whiteboard plugin — the collaboration plugin.
// The claim under test is structural: two browsers converge on one board
// while the sandboxed frame never opens a connection of its own. That
// cannot be tested in a single context, so the core journeys here drive
// TWO real browser contexts against the same room and assert both sides
// agree — same stroke set, same rendered pixels, no last-writer-wins after
// a disconnect, no name-carrying presence, and zero frame-originated
// network requests across a full session.
import { test, expect, type Page, type BrowserContext, type Frame } from "@playwright/test";

const PORT = Number(process.env.E2E_PORT ?? 8123);
const BASE = `http://localhost:${PORT}`;

// Both sides of every pixel-comparison journey render at the SAME viewport
// and DPR, or canvas dataURLs could differ without a document difference.
// 900px tall so the 560px board clears the hero with room to spare once
// scrolled into view (mouse events cannot reach outside the viewport).
const CTX_OPTS = { viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 };

// ─── harness ────────────────────────────────────────────────────────────────

type Mirror = HTMLIFrameElement & {
  __wbReady?: boolean;
  __wbConnected?: boolean;
  __wbPid?: string;
  __wbColor?: string;
  __wbParticipants?: number;
  __wbStrokes?: number;
  __wbSent?: { updates: number; bytes: number };
  __wbRecv?: { updates: number; bytes: number };
  __wbProbes?: { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } | null;
};

// The adapter installs this on the host page (whiteboard/host/adapter.js);
// declaring it on Window types the demo/e2e control surface without casts.
declare global {
  interface Window {
    __gofastrWhiteboardDemo?: {
      disconnect(): void;
      reconnect(): void;
      state(): { connected: boolean; pid: string; color: string; participants: number } | null;
    };
  }
}

const consoleErrors = new WeakMap<Page, string[]>();

function trackConsole(page: Page): void {
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(String(e)));
  consoleErrors.set(page, errors);
}

async function newRoomPage(context: BrowserContext, room: string): Promise<Page> {
  const page = await context.newPage();
  trackConsole(page);
  await page.goto(`${BASE}/whiteboard?room=${room}`);
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as Mirror | null;
      return !!f && f.__wbReady === true;
    },
    undefined,
    { timeout: 20_000 }
  );
  return page;
}

async function boardFrame(page: Page): Promise<Frame> {
  const frame = page.frames().find((f) => f.url().includes("/__gofastr/plugin/whiteboard/board.html"));
  if (!frame) throw new Error("whiteboard frame not found");
  return frame;
}

/** Draw one straight stroke on a page's board from (fx0,fy0) to (fx1,fy1),
 *  all in FRACTIONS of the canvas. Pointer events land on whatever is at the
 *  page coordinates — the canvas inside the opaque frame included. */
async function drawStroke(page: Page, fx0: number, fy0: number, fx1: number, fy1: number): Promise<void> {
  // The board sits under the hero; scroll it fully into view FIRST, or the
  // lower canvas fractions fall outside the viewport where synthetic mouse
  // events never arrive and strokes silently fail to commit.
  await page.locator(".editor-card").scrollIntoViewIfNeeded();
  const box = await (await boardFrame(page)).locator("#wb-canvas").boundingBox();
  if (!box) throw new Error("canvas not visible");
  const x0 = box.x + box.width * fx0;
  const y0 = box.y + box.height * fy0;
  const x1 = box.x + box.width * fx1;
  const y1 = box.y + box.height * fy1;
  await page.mouse.move(x0, y0);
  await page.mouse.down();
  await page.mouse.move((x0 + x1) / 2, (y0 + y1 * 1.4) / 2, { steps: 12 });
  await page.mouse.move(x1, y1, { steps: 12 });
  await page.mouse.up();
}

async function strokeDump(page: Page): Promise<string> {
  return (await boardFrame(page)).evaluate("window.__wbDebug.strokeDump()");
}

/** Ids in the order the frame paints them (board.ts debugApi.paintOrder). */
async function paintOrder(p: Page): Promise<string[]> {
  return p
    .frameLocator("iframe")
    .locator("body")
    .evaluate(() => {
      const api = (window as unknown as { __wbDebug?: { paintOrder?: () => string[] } }).__wbDebug;
      return api?.paintOrder?.() ?? [];
    });
}

async function canvasDataUrl(page: Page): Promise<string> {
  return (await boardFrame(page)).evaluate(
    "document.getElementById('wb-canvas').toDataURL()"
  );
}

export interface PixelDiff {
  sameSize: boolean;
  width: number;
  height: number;
  otherWidth: number;
  otherHeight: number;
  blocks: number;
  blocksDiffering: number;
  worstBlock: { x: number; y: number; a: number; b: number } | null;
  pixelsDiffering: number;
  maxChannelDelta: number;
}

/** A channel this far from the canvas background counts the pixel as ink. */
const INK_DELTA = 48;
/** Ink coverage is compared over blocks of this many pixels a side. Whole-block
 *  coverage is what survives the disagreement in #37: two engines can shade the
 *  edge pixels of a stroke differently, but they cannot disagree about how much
 *  of a 16x16 block a stroke covers. */
const BLOCK = 16;
/** Two blocks differ when their ink coverage is this far apart. A stroke that
 *  failed to replay empties its blocks — roughly 25 points of coverage for a
 *  4px stroke crossing a 16px block. Edge shading moves a block by one or two. */
const COVERAGE_TOLERANCE = 0.1;
/** And this many blocks may differ before two pictures are called unequal. */
const BLOCK_BUDGET = 8;

/** Compare two canvas dataURLs by ink coverage per block.
 *
 *  The replicas live in separate browser contexts, so the bitmaps only meet if
 *  one is carried across as a dataURL — small, because the board is mostly
 *  empty. A scratch page in the host context decodes both and walks them
 *  together; only the tallies come back.
 *
 *  Byte equality is what these journeys used to assert, and CI's webkit broke
 *  it with every property the plugin promises equal (#37). A flat per-pixel
 *  budget cannot replace it: three strokes ink 0.85% of this canvas, so one
 *  lost stroke and a rasteriser disagreeing along every stroke edge move a
 *  comparable number of pixels. Coverage per block separates the two by an
 *  order of magnitude, and journey 4 proves on every run that it still can. */
async function pixelDiff(host: BrowserContext, urlA: string, urlB: string): Promise<PixelDiff> {
  const page = await host.newPage();
  try {
    return await page.evaluate(
      async ([a, b, inkDelta, block, tolerance]) => {
        const load = async (src: string): Promise<ImageData> => {
          const img = new Image();
          img.src = src;
          await img.decode();
          const c = document.createElement("canvas");
          c.width = img.naturalWidth;
          c.height = img.naturalHeight;
          const ctx = c.getContext("2d");
          if (!ctx) throw new Error("no 2d context in the scratch page");
          ctx.drawImage(img, 0, 0);
          return ctx.getImageData(0, 0, c.width, c.height);
        };
        const ia = await load(a as string);
        const ib = await load(b as string);
        const base = {
          sameSize: false,
          width: ia.width,
          height: ia.height,
          otherWidth: ib.width,
          otherHeight: ib.height,
          blocks: 0,
          blocksDiffering: 0,
          worstBlock: null as { x: number; y: number; a: number; b: number } | null,
          pixelsDiffering: 0,
          maxChannelDelta: 0,
        };
        if (ia.width !== ib.width || ia.height !== ib.height) return base;

        const ink = inkDelta as number;
        const size = block as number;
        const tol = tolerance as number;
        // The top-left pixel is board background on any non-degenerate render.
        const bgA = [ia.data[0], ia.data[1], ia.data[2], ia.data[3]];
        const bgB = [ib.data[0], ib.data[1], ib.data[2], ib.data[3]];
        const isInk = (d: Uint8ClampedArray, i: number, bg: number[]): boolean => {
          for (let ch = 0; ch < 4; ch++) if (Math.abs(d[i + ch] - bg[ch]) > ink) return true;
          return false;
        };

        let pixelsDiffering = 0;
        let maxChannelDelta = 0;
        let blocks = 0;
        let blocksDiffering = 0;
        let worstBlock: { x: number; y: number; a: number; b: number } | null = null;
        let worstGap = 0;

        for (let by = 0; by < ia.height; by += size) {
          for (let bx = 0; bx < ia.width; bx += size) {
            const x1 = Math.min(bx + size, ia.width);
            const y1 = Math.min(by + size, ia.height);
            let inkA = 0;
            let inkB = 0;
            let count = 0;
            for (let y = by; y < y1; y++) {
              for (let x = bx; x < x1; x++) {
                const i = (y * ia.width + x) * 4;
                count++;
                if (isInk(ia.data, i, bgA)) inkA++;
                if (isInk(ib.data, i, bgB)) inkB++;
                let delta = 0;
                for (let ch = 0; ch < 4; ch++) {
                  const d = Math.abs(ia.data[i + ch] - ib.data[i + ch]);
                  if (d > delta) delta = d;
                }
                if (delta > maxChannelDelta) maxChannelDelta = delta;
                if (delta > ink) pixelsDiffering++;
              }
            }
            blocks++;
            const ca = inkA / count;
            const cb = inkB / count;
            const gap = Math.abs(ca - cb);
            if (gap > tol) blocksDiffering++;
            if (gap > worstGap) {
              worstGap = gap;
              worstBlock = { x: bx, y: by, a: Number(ca.toFixed(3)), b: Number(cb.toFixed(3)) };
            }
          }
        }
        return { ...base, sameSize: true, blocks, blocksDiffering, worstBlock, pixelsDiffering, maxChannelDelta };
      },
      [urlA, urlB, INK_DELTA, BLOCK, COVERAGE_TOLERANCE] as [string, string, number, number, number]
    );
  } finally {
    await page.close();
  }
}

/** Assert two replicas rendered the same picture, allowing rasterisation
 *  noise but not a difference in what was drawn. Returns the diff so a caller
 *  can make further claims about it. */
async function expectSamePicture(host: BrowserContext, a: string, b: string, what: string): Promise<PixelDiff> {
  const diff = await pixelDiff(host, a, b);
  expect(
    diff.sameSize,
    `${what}: canvases differ in size, ${diff.width}x${diff.height} vs ${diff.otherWidth}x${diff.otherHeight}`
  ).toBe(true);
  expect(
    diff.blocksDiffering,
    `${what}: ${diff.blocksDiffering}/${diff.blocks} blocks differ in ink coverage by more than ` +
      `${COVERAGE_TOLERANCE * 100} points (worst ${JSON.stringify(diff.worstBlock)}; ` +
      `${diff.pixelsDiffering} px over the channel threshold, worst channel delta ${diff.maxChannelDelta})`
  ).toBeLessThanOrEqual(BLOCK_BUDGET);
  return diff;
}

/** Poll until fn's promise resolves truthy (expect.poll over async page state). */
async function until(page: Page, fn: (p: Page) => Promise<unknown>, what: string, timeout = 15_000): Promise<void> {
  await expect
    .poll(async () => fn(page), { timeout, message: what })
    .toBeTruthy();
}

function expectNoConsoleErrors(pages: Page[]): void {
  for (const page of pages) {
    const errors = consoleErrors.get(page) ?? [];
    expect(errors.filter((e) => !/favicon/i.test(e)), `console errors on ${page.url()}`).toEqual([]);
  }
}

let roomSeq = 0;
function freshRoom(prefix: string): string {
  roomSeq += 1;
  return `${prefix}-${Date.now().toString(36)}-${roomSeq}`;
}

// ─── 1. mount + sandbox + no console errors ────────────────────────────────

test("a board that cannot reach the room says so instead of looking synced", async ({ page }) => {
  // The dangerous failure for a collaborative tool is not an error, it is
  // silence: you keep drawing, the strokes never leave, and the board looks
  // exactly as it does when they do. Break only the room stream.
  let intercepted = 0;
  await page.route(
    (u) => u.pathname.includes("/whiteboard/room/stream"),
    (r) => {
      intercepted += 1;
      return r.abort();
    }
  );
  await page.goto(`${BASE}/whiteboard`);

  const status = page.frameLocator("iframe").locator("#wb-status");
  await expect(status).toBeVisible({ timeout: 25_000 });

  // Assert the stream was actually attempted: a route that never matches
  // makes every assertion below pass by measuring an unbroken board. An
  // earlier version of this probe used a glob that missed the query string
  // and reported "no failure shown" while blocking nothing.
  await expect.poll(() => intercepted, { timeout: 15_000 }).toBeGreaterThan(0);

  await expect(status).toContainText(/offline/i, { timeout: 15_000 });
  await expect(status, "an unreachable room must not read as synced").not.toContainText(/synced/i);
});

test("mounts sandboxed (allow-scripts, no allow-same-origin) with no console errors", async ({ page }) => {
  trackConsole(page);
  const room = freshRoom("mount");
  await page.goto(`${BASE}/whiteboard?room=${room}`);

  await expect(page.locator("iframe")).toHaveCount(1);
  const sandbox = await page.locator("iframe").getAttribute("sandbox");
  expect(sandbox).toBe("allow-scripts");

  await until(page, (p) =>
    p.waitForFunction(() => {
      const f = document.querySelector("iframe") as Mirror | null;
      return !!f && f.__wbReady === true;
    }, undefined, { timeout: 20_000 }).then(() => true),
    "frame ready");

  // Self-isolation probes (protocol §8a): the frame checked its own cage.
  const probes = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f?.__wbProbes ?? null;
  });
  expect(probes).toEqual({ cookieEmpty: true, parentBlocked: true, storageBlocked: true });

  // The room connects and assigns identity without a name anywhere.
  await until(page, (p) =>
    p.waitForFunction(() => {
      const f = document.querySelector("iframe") as Mirror | null;
      return !!f && f.__wbConnected === true && !!f.__wbPid && !!f.__wbColor;
    }, undefined, { timeout: 15_000 }).then(() => true),
    "room connected with pid+color");

  expectNoConsoleErrors([page]);
});

// ─── 2. THE claim: a stroke drawn in one window appears in the other ───────

test("a stroke drawn in one browser context appears in the other, rendered to the same picture", async ({ browser }) => {
  const room = freshRoom("two");
  const ctxA = await browser.newContext(CTX_OPTS);
  const ctxB = await browser.newContext(CTX_OPTS);
  try {
    const pageA = await newRoomPage(ctxA, room);
    const pageB = await newRoomPage(ctxB, room);

    // Both windows see 2 participants before anyone draws.
    await until(pageA, (p) => p.evaluate(() => {
      const f = document.querySelector("iframe") as Mirror | null;
      return f?.__wbParticipants === 2;
    }), "A sees 2 participants");
    await until(pageB, (p) => p.evaluate(() => {
      const f = document.querySelector("iframe") as Mirror | null;
      return f?.__wbParticipants === 2;
    }), "B sees 2 participants");

    await drawStroke(pageA, 0.3, 0.3, 0.7, 0.55);

    // The stroke crosses: B's frame holds the same stroke ids and data.
    await until(pageB, async (p) => (await strokeDump(p)).length > 4, "B received the stroke");
    await until(pageA, async (p) => (await strokeDump(p)) === (await strokeDump(pageB)), "both sides agree on the document");

    const dumpA = await strokeDump(pageA);
    const dumpB = await strokeDump(pageB);
    expect(dumpA).toBe(dumpB);
    expect(JSON.parse(dumpA)).toHaveLength(1);

    // Identical document, identical picture. Compared by ink coverage rather
    // than byte for byte: engines disagree about edge shading (#37), never
    // about what was drawn.
    const imgA = await canvasDataUrl(pageA);
    const imgB = await canvasDataUrl(pageB);
    expect(imgA.length).toBeGreaterThan(1000); // not a blank canvas
    await expectSamePicture(ctxA, imgA, imgB, "the relayed stroke");

    // The host relayed real bytes both ways is NOT required (B only
    // receives here), but B must have received the update via the host.
    const recvB = await pageB.evaluate(() => {
      const f = document.querySelector("iframe") as Mirror | null;
      return f?.__wbRecv;
    });
    expect(recvB?.updates ?? 0).toBeGreaterThanOrEqual(1);
    expect(recvB?.bytes ?? 0).toBeGreaterThan(0);

    expectNoConsoleErrors([pageA, pageB]);
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

// ─── 3. convergence after a gap ─────────────────────────────────────────────

test("drawing offline on both sides converges after reconnect instead of last-writer winning", async ({ browser }) => {
  const room = freshRoom("conv");
  const ctxA = await browser.newContext(CTX_OPTS);
  const ctxB = await browser.newContext(CTX_OPTS);
  try {
    const pageA = await newRoomPage(ctxA, room);
    const pageB = await newRoomPage(ctxB, room);

    // Baseline stroke while both are connected.
    await drawStroke(pageA, 0.25, 0.25, 0.4, 0.4);
    await until(pageB, async (p) => JSON.parse(await strokeDump(p)).length === 1, "baseline stroke reached B");

    // Drop A's connection; wait until the room says A is gone.
    await pageA.evaluate(() => window.__gofastrWhiteboardDemo?.disconnect());
    await until(pageB, (p) => p.evaluate(() => {
      const f = document.querySelector("iframe") as Mirror | null;
      return f?.__wbParticipants === 1;
    }), "room sees A leave");

    // Draw offline in BOTH windows: 2 strokes on A, 1 on B.
    await drawStroke(pageA, 0.55, 0.25, 0.75, 0.35); // offline in A
    await drawStroke(pageA, 0.55, 0.55, 0.75, 0.7); // offline in A
    await drawStroke(pageB, 0.2, 0.6, 0.35, 0.75); // published by B

    expect(JSON.parse(await strokeDump(pageA))).toHaveLength(3); // baseline + 2 offline
    expect(JSON.parse(await strokeDump(pageB))).toHaveLength(2); // baseline + its own

    // Reconnect A: the snapshot publishes A's offline strokes; the replay
    // delivers B's. The CRDT merges — the union (4 strokes) wins on BOTH.
    await pageA.evaluate(() => window.__gofastrWhiteboardDemo?.reconnect());
    await until(pageA, async (p) => JSON.parse(await strokeDump(p)).length === 4, "A converged to the union");
    await until(pageB, async (p) => JSON.parse(await strokeDump(p)).length === 4, "B converged to the union");

    const dumpA = await strokeDump(pageA);
    const dumpB = await strokeDump(pageB);
    expect(dumpA).toBe(dumpB); // same ids, same colors, same points: converged documents

    const imgA = await canvasDataUrl(pageA);
    const imgB = await canvasDataUrl(pageB);
    // Converged picture, not just converged bookkeeping.
    await expectSamePicture(ctxA, imgA, imgB, "the merged board");

    expectNoConsoleErrors([pageA, pageB]);
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

// ─── 4. a late joiner gets the existing board ───────────────────────────────

test("a joiner arriving after the drawing gets the existing board, not an empty one", async ({ browser }) => {
  const room = freshRoom("late");
  const ctxA = await browser.newContext(CTX_OPTS);
  const ctxB = await browser.newContext(CTX_OPTS);
  try {
    const pageA = await newRoomPage(ctxA, room);
    await drawStroke(pageA, 0.3, 0.3, 0.6, 0.6);
    await drawStroke(pageA, 0.6, 0.3, 0.3, 0.6);
    await until(pageA, async (p) => JSON.parse(await strokeDump(p)).length === 2, "A has 2 strokes");
    // Kept as the control for the picture comparison below: a board that is
    // one stroke short of the one the joiner must replay.
    const oneStrokeShort = await canvasDataUrl(pageA);

    await drawStroke(pageA, 0.45, 0.2, 0.5, 0.8);
    await until(pageA, async (p) => JSON.parse(await strokeDump(p)).length === 3, "A has 3 strokes");

    // B joins AFTER the fact: the hub replays the room's persisted state.
    const pageB = await newRoomPage(ctxB, room);
    await until(pageB, async (p) => JSON.parse(await strokeDump(p)).length === 3, "late joiner replayed 3 strokes");

    const dumpA = await strokeDump(pageA);
    const dumpB = await strokeDump(pageB);
    expect(dumpB).toBe(dumpA);

    // The replay must RENDER, and both replicas must paint in the same order.
    //
    // Paint order is asserted from the frame rather than inferred from pixels:
    // it is deterministic across engines, and it catches an ordering regression
    // even between same-coloured strokes, where a bitmap compare sees nothing.
    const orderA = await paintOrder(pageA);
    const orderB = await paintOrder(pageB);
    expect(orderB, "replicas must paint strokes in the same order").toEqual(orderA);

    const imgA = await canvasDataUrl(pageA);
    const imgB = await canvasDataUrl(pageB);
    expect(imgB.length, "the joiner's canvas rendered something").toBeGreaterThan(1000);

    // Before trusting the comparison, prove it still has teeth ON THIS ENGINE:
    // the same call must reject A's own board from one stroke earlier. Without
    // this the tolerance could drift wide enough to pass anything, which is how
    // a picture check quietly stops being one.
    const control = await pixelDiff(ctxA, imgA, oneStrokeShort);
    expect(
      control.blocksDiffering,
      `the picture comparison can no longer see a missing stroke: only ` +
        `${control.blocksDiffering}/${control.blocks} blocks differ (budget ${BLOCK_BUDGET})`
    ).toBeGreaterThan(BLOCK_BUDGET);

    await expectSamePicture(ctxA, imgA, imgB, "the replayed board");

    expectNoConsoleErrors([pageA, pageB]);
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

// ─── 5. the frame issues no network requests at all ─────────────────────────

test("during a full collaborative session the frame issues zero network requests", async ({ browser }) => {
  const room = freshRoom("nonet");
  const ctxA = await browser.newContext(CTX_OPTS);
  const ctxB = await browser.newContext(CTX_OPTS);
  try {
    const pageA = await newRoomPage(ctxA, room);
    const pageB = await newRoomPage(ctxB, room);

    // Host-side collection from AFTER both frames are ready: any request
    // whose INITIATING frame is the plugin iframe is a violation. (The SSE
    // stream and publishes are host-page requests — main frame — and are
    // exactly the point: the host owns the only network leg.)
    const frameRequests: string[] = [];
    for (const page of [pageA, pageB]) {
      page.on("request", (req) => {
        const url = req.frame()?.url() ?? "";
        if (url.includes("/__gofastr/plugin/whiteboard/board.html")) {
          frameRequests.push(`${req.method()} ${req.url()}`);
        }
      });
    }

    // A full session: draw both ways, receive both ways, presence moves.
    await drawStroke(pageA, 0.3, 0.3, 0.7, 0.6);
    await drawStroke(pageB, 0.6, 0.25, 0.35, 0.7);
    const boxA = await (await boardFrame(pageA)).locator("#wb-canvas").boundingBox();
    if (boxA) {
      await pageA.mouse.move(boxA.x + boxA.width * 0.5, boxA.y + boxA.height * 0.5, { steps: 8 });
    }
    await until(pageA, async (p) => JSON.parse(await strokeDump(p)).length === 2, "A holds both strokes");
    await until(pageB, async (p) => JSON.parse(await strokeDump(p)).length === 2, "B holds both strokes");

    // In-frame probe: every outbound web API is wrapped at boot; any attempt
    // (even a CSP-blocked one) would be recorded. The list must be empty.
    for (const page of [pageA, pageB]) {
      const attempts = await (await boardFrame(page)).evaluate("window.__wbNetProbe.attempts");
      expect(attempts, `net attempts inside ${page.url()}'s frame`).toEqual([]);
    }
    expect(frameRequests, "requests initiated by the plugin frame").toEqual([]);

    expectNoConsoleErrors([pageA, pageB]);
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

// ─── 6. presence: the other participant's cursor appears, carrying no name ──

test("a cursor from the other participant appears, coloured, with no name", async ({ browser }) => {
  const room = freshRoom("presence");
  const ctxA = await browser.newContext(CTX_OPTS);
  const ctxB = await browser.newContext(CTX_OPTS);
  try {
    const pageA = await newRoomPage(ctxA, room);
    const pageB = await newRoomPage(ctxB, room);

    const pidA = await pageA.evaluate(() => {
      const f = document.querySelector("iframe") as Mirror | null;
      return f?.__wbPid ?? "";
    });
    const colorA = await pageA.evaluate(() => {
      const f = document.querySelector("iframe") as Mirror | null;
      return f?.__wbColor ?? "";
    });
    expect(pidA).toMatch(/^p-\d+$/);
    expect(colorA).not.toBe("");

    // Move A's pointer over ITS board; B must show A's cursor.
    const box = await (await boardFrame(pageA)).locator("#wb-canvas").boundingBox();
    if (!box) throw new Error("canvas not visible");
    await pageA.mouse.move(box.x + box.width * 0.6, box.y + box.height * 0.4, { steps: 10 });
    await pageA.mouse.move(box.x + box.width * 0.62, box.y + box.height * 0.45, { steps: 5 });

    const cursor = (await boardFrame(pageB)).locator(`[data-wb-cursor="${pidA}"]`);
    await expect(cursor).toBeVisible({ timeout: 10_000 });
    // Presence carries an opaque pid and a colour — never a name. The cursor
    // is a coloured ring with no text content and no name-ish attributes.
    await expect(cursor).toHaveText("");
    const cursorColor = await cursor.evaluate((el) => el.style.getPropertyValue("--cursor-color"));
    expect(cursorColor).toBe(colorA);

    // The frame's own presence roster knows the pid, not a person.
    const pidsB = await (await boardFrame(pageB)).evaluate<string[]>("window.__wbDebug.presencePids()");
    expect(pidsB).toContain(pidA);

    // Moving out of the board hides the cursor again.
    await pageA.mouse.move(box.x - 40, box.y - 40);
    await expect(cursor).toHaveCount(0, { timeout: 10_000 });

    expectNoConsoleErrors([pageA, pageB]);
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});
