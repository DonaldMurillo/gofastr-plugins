// User-journey e2e for the imageedit plugin — the bytes-over-the-bridge
// plugin for a second file format. The suite's job is to prove the claims
// the plugin exists to make, in the LIVE app:
//
//   1. the mount is sandboxed (allow-scripts, no allow-same-origin), boots
//      with no console errors, and the FRAME itself issues no network
//      requests — the image bytes arrive over postMessage, not fetch;
//   2. preview and server output AGREE: after fixed edits, the frame's own
//      1:1 render (the previewRender mirror) and the Go-rendered export
//      match on dimensions and at deterministic sample points;
//   3. redaction actually REMOVES: after redacting the sample's visible
//      secret token and exporting, the exported bytes carry no trace of it;
//   4. the operation list is the live document (rotate/annotation/crop
//      mirror as JSON; the demo page shows it);
//   5. the optional upload:images path moves bytes over the bridge from a
//      local file picked INSIDE the frame.
import { test, expect, type Page } from "@playwright/test";
import { deflateSync } from "node:zlib";

// The demo mounts a 960x640 sample; rotated it renders a ~962px-tall canvas,
// which is taller than Playwright's default 720px viewport. page.mouse works
// in viewport coordinates, so at the default size a drag across the image is
// partly off-screen no matter where the page is scrolled — the crop from the
// top-left corner lands outside the canvas and no crop is ever produced.
// These journeys need a viewport that fits their subject.
test.use({ viewport: { width: 1280, height: 1400 } });

const DEMO = "/imageedit";
const SAVE = "/__gofastr/plugin/imageedit/save";
const STATIC = "/__gofastr/plugin/imageedit/";

// The sample's secret-token rect, mirrored from imageedit/sample.go's
// SampleTokenRect() (source pixels; the sample is 960×640).
const TOKEN = { x: 100, y: 460, w: 510, h: 35 };

const DEFAULT_DOC = {
  schemaVersion: "imageedit-v1",
  src: { kind: "id", ref: "demo" },
  rotate: 0,
  annotations: [],
  redactions: [],
};

// ─── harness ────────────────────────────────────────────────────────────────

type Mirror = HTMLIFrameElement & {
  __imageeditReady?: boolean;
  __imageeditDoc?: string;
  __imageeditPreview?: { dataUrl: string; width: number; height: number };
  __imageeditLastExport?: {
    url: string;
    format: string;
    width: number;
    height: number;
    bytes: number;
    sha256: string;
    verify: boolean;
    report: { redactionsChecked: number; pass: boolean; failed?: string[] } | null;
  };
  __imageeditLastError?: string;
};

function fl(page: Page) {
  return page.frameLocator("iframe");
}

const consoleErrors = new WeakMap<Page, string[]>();
const frameRequests = new WeakMap<Page, string[]>();

test.beforeEach(async ({ page, request, baseURL }) => {
  const errors: string[] = [];
  consoleErrors.set(page, errors);
  const frameReqs: string[] = [];
  frameRequests.set(page, frameReqs);

  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(String(e)));

  // Any request whose issuing frame is NOT the main page came from inside a
  // frame document. The editor frame must only ever load its own three
  // static assets — never an image, never an RPC (those fetches run in the
  // privileged host adapter).
  page.on("request", (r) => {
    try {
      if (r.frame() && r.frame() !== page.mainFrame()) {
        frameReqs.push(new URL(r.url()).pathname);
      }
    } catch {
      /* a navigated-away frame; ignore */
    }
  });

  // Reset the persisted doc so journeys are independent.
  await request.post(`${baseURL}${SAVE}`, {
    data: { docId: "demo", doc: DEFAULT_DOC, schemaVersion: "imageedit-v1" },
  });
});

async function openDemo(page: Page): Promise<void> {
  await page.goto(DEMO);
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return !!f && f.__imageeditReady === true;
  });
}

/** The live operation list, parsed off the adapter's mirror. */
async function docOf(page: Page): Promise<Record<string, unknown>> {
  return page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f && f.__imageeditDoc ? JSON.parse(f.__imageeditDoc) : {};
  });
}

/** Canvas display box + backing size, in coordinates page.mouse can actually
 * use. Three traps make the naive reads wrong here, all measured by probing
 * the live frame's own pointer events:
 *
 *   1. The canvas lives in the OPAQUE-origin sandboxed iframe, so an
 *      element.scrollIntoView run INSIDE it cannot scroll the outer page
 *      (cross-origin) — the outer page stays wherever the last Playwright
 *      auto-scroll (e.g. a toolbar click) left it.
 *   2. locator.boundingBox() for an element inside the opaque frame combines
 *      a CACHED iframe offset that goes stale the moment anything scrolls:
 *      probe-measured it reported the canvas at (319, 0) while the frame's
 *      own pointer events showed (174, 46).
 *   3. mouse.down() into the iframe moves focus there, and the browser may
 *      scroll to reveal the focused element — AFTER the measurement, moving
 *      the canvas under an already-computed drag.
 *
 * So: scroll the IFRAME (a top-level element, so the scroll really happens)
 * to the viewport top and past any page chrome covering it (a sticky header
 * otherwise intercepts both the drag and every later toolbar click — webkit's
 * live failure mode); tap the canvas once to settle focus (a tap commits
 * nothing — pointerup with distance < 3 redraws only, inert for every drag
 * tool; skipped for the text tool, which places text on pointerdown);
 * re-settle; then add the iframe's own bounding box to the canvas rect read
 * INSIDE the frame — both live values, no cached offset. */
async function canvasGeom(page: Page) {
  const canvas = fl(page).locator("#ie-canvas");
  // Scroll the iframe to the viewport top, then keep walking down until the
  // iframe is genuinely the topmost element — the demo pages keep a sticky
  // <header> on top, and block:"start" parks the iframe's toolbar UNDER it,
  // where it intercepts every later click (the repo's example/smoke_test.go
  // documents the same trap). Pull the page up so the iframe's top edge sits
  // on the first row it actually owns.
  const settleIframe = () =>
    page.evaluate(() => {
      const f = document.querySelector("iframe");
      if (!f) return;
      f.scrollIntoView({ block: "start", inline: "nearest" });
      const r = f.getBoundingClientRect();
      const cx = r.x + r.width / 2;
      for (let y = Math.max(r.top, 0); y < Math.min(r.bottom, innerHeight) - 8; y += 4) {
        if (document.elementFromPoint(cx, y) === f) {
          if (y > r.top) window.scrollBy(0, -(y - r.top));
          return;
        }
      }
      throw new Error("page chrome covers the whole editor iframe");
    });
  const measure = async () => {
    const iframeBox = await page.locator("iframe").boundingBox();
    // Typed as HTMLCanvasElement: locator.evaluate hands back HTMLElement |
    // SVGElement, which has no width/height, and the CI typecheck rejects it.
    // bw/bh are the BACKING store (output space), distinct from the CSS box.
    const r = await canvas.evaluate((el: HTMLCanvasElement) => {
      const b = el.getBoundingClientRect();
      return { left: b.left, top: b.top, width: b.width, height: b.height, bw: el.width, bh: el.height };
    });
    if (!iframeBox || !r.bw || !r.bh) throw new Error("canvas geometry unavailable");
    const box = { x: iframeBox.x + r.left, y: iframeBox.y + r.top, width: r.width, height: r.height };
    return { box, width: r.bw, height: r.bh, sx: box.width / r.bw, sy: box.height / r.bh };
  };

  await settleIframe();
  const tool = await fl(page)
    .locator('.ie-tool[aria-pressed="true"]')
    .getAttribute("data-tool")
    .catch(() => null);
  if (tool !== "text" && tool !== null) {
    const { box } = await measure();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.up();
    await settleIframe();
    await page.waitForTimeout(50);
  }
  return measure();
}

/** Drag over a SOURCE-coordinate rect on the displayed canvas.
 *
 * The plugin's contract (js/src/editor.ts + js/src/render.ts): the user drags
 * in OUTPUT space — the displayed, cropped+rotated view — and the frame
 * inverts that through displayToSource() to author doc coordinates. A source
 * rect must therefore be mapped FORWARD through the same crop∘rotate (the
 * mirror of render.go's mapPoint) to find the on-screen points to drag
 * between; scaling source axes by the canvas scale only works unrotated,
 * because after a 90/270 rotate the backing store swaps axes and the naive
 * point is either the wrong region or off-canvas entirely (where
 * canvasPoint returns null, the drag never progresses past its origin, and
 * no edit is ever committed).
 *
 * Endpoints are inset 5 backing px from the canvas edge: the canvas carries
 * a 1px border and a 4px border-radius, whose corner pixels are a pointer
 * dead zone, and canvasPoint rejects outX === canvas.width. The ≤5px shrink
 * of the recovered source rect is far inside every journey's tolerance. */
async function dragSourceRect(
  page: Page,
  x: number,
  y: number,
  w: number,
  h: number
): Promise<void> {
  const doc = await docOf(page);
  const { box, width, height, sx, sy } = await canvasGeom(page);
  const rot = ((Number(doc.rotate) || 0) % 360 + 360) % 360;
  // mapPoint subtracts the effective crop; a null crop is the whole source,
  // whose dims are the output dims with axes swapped under 90/270.
  const swapped = rot === 90 || rot === 270;
  const crop = doc.crop as { x: number; y: number; w: number; h: number } | undefined;
  const c = crop ?? {
    x: 0,
    y: 0,
    w: swapped ? height : width,
    h: swapped ? width : height,
  };
  const map = (px: number, py: number): [number, number] => {
    const lx = px - c.x;
    const ly = py - c.y;
    switch (rot) {
      case 90:
        return [c.h - 1 - ly, lx];
      case 180:
        return [c.w - 1 - lx, c.h - 1 - ly];
      case 270:
        return [ly, c.w - 1 - lx];
      default:
        return [lx, ly];
    }
  };
  const INSET = 5;
  const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));
  const [ax, ay] = map(x, y);
  const [bx, by] = map(x + w, y + h);
  const axc = clamp(ax, INSET, width - 1 - INSET);
  const ayc = clamp(ay, INSET, height - 1 - INSET);
  const bxc = clamp(bx, INSET, width - 1 - INSET);
  const byc = clamp(by, INSET, height - 1 - INSET);
  const x0 = box.x + axc * sx;
  const y0 = box.y + ayc * sy;
  const x1 = box.x + bxc * sx;
  const y1 = box.y + byc * sy;
  // WebKit needs the pointer settled before the button goes down, and it drops
  // coarse moves: a 4-step sweep that works in chromium everywhere and in
  // webkit locally failed repeatedly on CI's webkit, where the machine is
  // slower and events coalesce differently. Settling first and moving in finer
  // steps makes the gesture survive a loaded runner. These are pointer
  // mechanics, not a relaxed assertion.
  await page.mouse.move(x0, y0);
  await page.waitForTimeout(30);
  await page.mouse.down();
  await page.waitForTimeout(30);
  await page.mouse.move((x0 + x1) / 2, (y0 + y1) / 2, { steps: 12 });
  await page.mouse.move(x1, y1, { steps: 12 });
  await page.waitForTimeout(30);
  await page.mouse.up();
  await page.waitForTimeout(30);
}

async function exportNow(page: Page): Promise<NonNullable<Mirror["__imageeditLastExport"]>> {
  await fl(page).locator("#ie-export").click();
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return !!f && !!f.__imageeditLastExport;
  });
  return page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f!.__imageeditLastExport!;
  });
}

/** Load an image URL (http or data:) into pixels, inside the page. */
async function pixelsOf(page: Page, src: string): Promise<{ w: number; h: number; data: number[] }> {
  return page.evaluate(async (url) => {
    const img = new Image();
    const loaded = new Promise<void>((res, rej) => {
      img.onload = () => res();
      img.onerror = () => rej(new Error("image failed to load: " + url.slice(0, 60)));
    });
    img.src = url;
    await loaded;
    const c = document.createElement("canvas");
    c.width = img.naturalWidth;
    c.height = img.naturalHeight;
    const cx = c.getContext("2d", { willReadFrequently: true })!;
    cx.drawImage(img, 0, 0);
    const d = cx.getImageData(0, 0, c.width, c.height);
    return { w: c.width, h: c.height, data: Array.from(d.data) };
  }, src);
}

/** Deterministic sample points across an image (golden-ratio spacing). */
function samplePoints(w: number, h: number, n: number): [number, number][] {
  const PHI = 0.6180339887498949;
  const pts: [number, number][] = [];
  for (let i = 0; i < n; i++) {
    pts.push([
      Math.floor((((i + 1) * PHI) % 1) * w),
      Math.floor((((i * 7 + 3) * PHI) % 1) * h),
    ]);
  }
  return pts;
}

// ─── 1. mount + sandbox + no console errors + no frame network ─────────────

test("mounts sandboxed (allow-scripts, no allow-same-origin), no console errors, and the frame itself issues no network requests", async ({ page }) => {
  await openDemo(page);

  const sandbox = await page.locator("iframe").getAttribute("sandbox");
  expect(sandbox).toBe("allow-scripts");

  // The image arrived over the bridge: the frame published its 1:1 render.
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return !!f && !!f.__imageeditPreview && f.__imageeditPreview.dataUrl.startsWith("data:image/png");
  });

  // Every request issued from INSIDE a frame must be one of the three static
  // assets. The /img fetch ran in the host adapter (main frame), invisible
  // to this filter — which is the point: the frame never fetched anything.
  const frameReqs = frameRequests.get(page)!;
  const offenders = frameReqs.filter(
    (p) => p !== `${STATIC}editor.html` && p !== `${STATIC}editor.js` && p !== `${STATIC}editor.css`
  );
  expect(offenders, `frame issued non-asset requests: ${offenders.join(", ")}`).toEqual([]);

  expect(consoleErrors.get(page)!).toEqual([]);
});

// ─── 2. THE agreement test: preview and server output agree ────────────────

test("after rotate + annotation, the frame's 1:1 render and the Go-rendered export agree on dimensions and sampled pixels", async ({ page }) => {
  await openDemo(page);

  // Fixed edits: rotate 90° CW, then one red rectangle annotation in the
  // sky area (source coords — under rotate it lands somewhere else, which
  // is exactly what the agreement is about).
  await fl(page).locator("#ie-rotate-r").click();
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return !!f && !!f.__imageeditDoc && JSON.parse(f.__imageeditDoc).rotate === 90;
  });
  await fl(page).locator('.ie-tool[data-tool="rect"]').click();
  await dragSourceRect(page, 620, 80, 180, 120);

  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    const d = f && f.__imageeditDoc ? JSON.parse(f.__imageeditDoc) : null;
    return !!d && Array.isArray(d.annotations) && d.annotations.length === 1;
  });

  const exp = await exportNow(page);
  expect(exp.verify).toBe(true);
  expect(exp.format).toBe("png");
  // 960×640 rotated 90° → 640×960.
  expect(exp.width).toBe(640);
  expect(exp.height).toBe(960);

  const preview = await page.evaluate(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return f!.__imageeditPreview!;
  });
  expect(preview.width).toBe(640);
  expect(preview.height).toBe(960);

  // The export URL is same-origin (the example app's store): read it back.
  const exportUrl = new URL(exp.url, page.url()).toString();
  const [a, b] = await Promise.all([pixelsOf(page, preview.dataUrl), pixelsOf(page, exportUrl)]);

  const pts = samplePoints(a.w, a.h, 120);
  let exact = 0;
  let within = 0;
  for (const [x, y] of pts) {
    const i = (y * a.w + x) * 4;
    const dr = Math.abs(a.data[i] - b.data[i]);
    const dg = Math.abs(a.data[i + 1] - b.data[i + 1]);
    const db = Math.abs(a.data[i + 2] - b.data[i + 2]);
    if (dr === 0 && dg === 0 && db === 0) exact++;
    if (dr <= 2 && dg <= 2 && db <= 2) within++;
  }
  // A PNG source composes through the identical integer pipeline: expect
  // bit-exact pixels, with the ≤2/channel window as the cross-browser floor.
  expect(within, `${within}/120 sampled points within tolerance`).toBe(120);
  expect(exact).toBeGreaterThanOrEqual(114); // ≥95% bit-exact

  // The annotation's ink is present in BOTH renders (the drag really drew,
  // and the server really re-rendered it): #D0342C strokes along the mapped
  // rect's top border. Source rect (620,80,180,120) under rotate90 maps to
  // output x∈[560-1-(80+120-1)…], computed generally instead:
  const sawRed = (img: { w: number; h: number; data: number[] }): boolean => {
    for (let y = 0; y < img.h; y += 2) {
      for (let x = 0; x < img.w; x += 2) {
        const i = (y * img.w + x) * 4;
        if (img.data[i] > 180 && img.data[i + 1] < 90 && img.data[i + 2] < 90) return true;
      }
    }
    return false;
  };
  expect(sawRed(a)).toBe(true);
  expect(sawRed(b)).toBe(true);

  expect(consoleErrors.get(page)!).toEqual([]);
});

// ─── 3. redaction actually removes ─────────────────────────────────────────

test("redacting the sample's visible secret token removes it from the exported bytes, and the server's verifier passes", async ({ page }) => {
  await openDemo(page);

  // The token must be visible BEFORE: sample the preview's token region for
  // non-black ink.
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return !!f && !!f.__imageeditPreview;
  });
  const before = await page.evaluate(async (tok) => {
    const f = document.querySelector("iframe") as Mirror | null;
    const img = new Image();
    img.src = f!.__imageeditPreview!.dataUrl;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.naturalWidth;
    c.height = img.naturalHeight;
    const cx = c.getContext("2d", { willReadFrequently: true })!;
    cx.drawImage(img, 0, 0);
    const d = cx.getImageData(0, 0, c.width, c.height).data;
    let ink = 0;
    for (let y = tok.y; y < tok.y + tok.h; y += 2) {
      for (let x = tok.x; x < tok.x + tok.w; x += 2) {
        const i = (y * c.width + x) * 4;
        if (d[i] > 40 || d[i + 1] > 40 || d[i + 2] > 40) ink++;
      }
    }
    return ink;
  }, TOKEN);
  expect(before).toBeGreaterThan(100); // a visible secret, not an empty region

  // Drag the redaction over the token and export.
  await fl(page).locator('.ie-tool[data-tool="redact"]').click();
  await dragSourceRect(page, TOKEN.x, TOKEN.y, TOKEN.w, TOKEN.h);
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    const d = f && f.__imageeditDoc ? JSON.parse(f.__imageeditDoc) : null;
    return !!d && Array.isArray(d.redactions) && d.redactions.length === 1;
  });

  const exp = await exportNow(page);
  expect(exp.verify).toBe(true);
  expect(exp.report?.pass).toBe(true);
  expect(exp.report?.redactionsChecked).toBe(1);
  expect(exp.report?.failed ?? []).toEqual([]);

  // The exported bytes: the token region is uniformly the fill (black).
  const exportUrl = new URL(exp.url, page.url()).toString();
  const out = await pixelsOf(page, exportUrl);
  const black = await page.evaluate(async ({ url, tok }) => {
    const img = new Image();
    img.src = url;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.naturalWidth;
    c.height = img.naturalHeight;
    const cx = c.getContext("2d", { willReadFrequently: true })!;
    cx.drawImage(img, 0, 0);
    const d = cx.getImageData(0, 0, c.width, c.height).data;
    let black = 0;
    let total = 0;
    for (let y = tok.y; y < tok.y + tok.h; y += 2) {
      for (let x = tok.x; x < tok.x + tok.w; x += 2) {
        const i = (y * c.width + x) * 4;
        total++;
        if (d[i] < 16 && d[i + 1] < 16 && d[i + 2] < 16) black++;
      }
    }
    return { black, total };
  }, { url: exportUrl, tok: TOKEN });
  expect(out.w).toBe(960); // no crop/rotate in this journey
  expect(black.total).toBeGreaterThan(0);
  expect(black.black).toBe(black.total); // EVERY sampled pixel is the fill

  expect(consoleErrors.get(page)!).toEqual([]);
});

// ─── 4. the operation list is the live document ────────────────────────────

test("the live operation list mirrors edits as JSON, on the iframe and on the demo page", async ({ page }) => {
  await openDemo(page);

  await fl(page).locator("#ie-rotate-l").click();
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    return !!f && !!f.__imageeditDoc && JSON.parse(f.__imageeditDoc).rotate === 270;
  });

  // The demo page renders the same doc as live JSON in the proof panel.
  await expect(page.locator("#ie-proof-doc")).toContainText('"rotate": 270');

  // Crop narrows the output: drag a crop over the left half, then the
  // preview's backing canvas shrinks accordingly.
  //
  // The rect starts INSIDE the image rather than at source (0,0). The canvas
  // corner is a pointer dead zone — a 1px border plus a 4px radius, and
  // canvasPoint rejects a coordinate equal to the backing width — so a
  // corner-anchored drag is a coin flip that came up tails on CI's webkit while
  // passing everywhere else. What this journey tests is that the operation list
  // mirrors the drag, not that the extreme corner is reachable; the crop
  // geometry itself is covered by the agreement journey and the Go tests.
  await fl(page).locator('.ie-tool[data-tool="crop"]').click();
  await dragSourceRect(page, 40, 40, 400, 560);
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    const d = f && f.__imageeditDoc ? JSON.parse(f.__imageeditDoc) : null;
    return !!d && !!d.crop && d.crop.w > 330 && d.crop.w < 440;
  });
  const geom = await canvasGeom(page);
  expect(geom.width).toBeLessThan(960); // cropped, not the full image

  // Clear resets the whole operation list.
  await fl(page).locator("#ie-reset").click();
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    const d = f && f.__imageeditDoc ? JSON.parse(f.__imageeditDoc) : null;
    return !!d && d.rotate === 0 && !d.crop;
  });

  expect(consoleErrors.get(page)!).toEqual([]);
});

// ─── 5. the optional upload path crosses the bridge ────────────────────────

test("loading a local image inside the frame crosses the bridge, not the network, and re-points the doc", async ({ page }) => {
  await openDemo(page);

  // A real (tiny, deterministic) PNG built here: 64×40, red top half, blue
  // bottom half. The frame's file input receives it; the bytes go
  // requestUpload → POST /upload → host store → id → doc.src.ref.
  const png = makePNG(64, 40);
  await fl(page).locator("#ie-file").setInputFiles({
    name: "chart-screenshot.png",
    mimeType: "image/png",
    buffer: png,
  });

  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    const d = f && f.__imageeditDoc ? JSON.parse(f.__imageeditDoc) : null;
    return !!d && d.src && d.src.ref !== "demo";
  });
  const doc = await docOf(page);
  expect((doc.src as { ref: string }).ref).toMatch(/^[0-9a-f]{16}$/);

  // The new image rendered: preview dims are the uploaded file's.
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe") as Mirror | null;
    const p = f && f.__imageeditPreview;
    return !!p && p.width === 64 && p.height === 40;
  });

  // And the frame STILL issued no non-asset request of its own.
  const offenders = frameRequests
    .get(page)!
    .filter(
      (p) =>
        p !== `${STATIC}editor.html` && p !== `${STATIC}editor.js` && p !== `${STATIC}editor.css`
    );
  expect(offenders).toEqual([]);
  expect(consoleErrors.get(page)!).toEqual([]);
});

// ─── helpers: a minimal deterministic PNG builder ──────────────────────────

function crc32(buf: Buffer): number {
  let c = ~0;
  for (let i = 0; i < buf.length; i++) {
    c ^= buf[i];
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
  }
  return ~c >>> 0;
}

function chunk(type: string, data: Buffer): Buffer {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

function makePNG(w: number, h: number): Buffer {
  const raw = Buffer.alloc(h * (1 + w * 3));
  for (let y = 0; y < h; y++) {
    const row = y * (1 + w * 3);
    raw[row] = 0; // filter: none
    for (let x = 0; x < w; x++) {
      const i = row + 1 + x * 3;
      // Red above the midline, blue below — both saturated.
      if (y < h / 2) {
        raw[i] = 0xd0; raw[i + 1] = 0x34; raw[i + 2] = 0x2c;
      } else {
        raw[i] = 0x1c; raw[i + 1] = 0x7e; raw[i + 2] = 0xd6;
      }
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // truecolor
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}
