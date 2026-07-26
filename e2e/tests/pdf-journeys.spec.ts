// User-journey e2e for the PDF viewer / editor / redactor plugin — the fourth
// sandboxed heavy-JS plugin.
//
// This file pins the properties that are load-bearing for a REDACTOR and that a
// unit test cannot see, in both engines:
//
//   1. the frame is genuinely OPAQUE. Everything else in this plugin's security
//      story is downstream of that, so it is asserted directly (cookies,
//      window.parent and storage all unreachable) rather than inferred from the
//      sandbox attribute.
//   2. pdf.js boots WORKER-FREE and fetches nothing. Under the framed CSP a real
//      Worker is impossible (worker-src falls back to default-src, no blob:) and
//      connect-src is 'none', so the document bytes must arrive over the bridge.
//   3. a page actually renders PIXELS. "No error" is not the same as "rendered"
//      here: the JPX/JBIG2 wasm trap produced a perfectly silent BLANK page, and
//      a blank page is the one failure a redaction user would never notice.
//
// Feature journeys (annotate, forms, page ops, redact + export) live alongside
// these as those modes land; they build on the same mirrors.
import { test, expect, type Page } from "@playwright/test";

const PDF = "/pdf";

type PdfFrame = HTMLIFrameElement & {
  __pdfRendered?: boolean;
  __pdfError?: string;
  __pdfText?: string;
  __pdfPageCount?: number;
  __pdfNonBlank?: boolean;
  __pdfNonWhitePixels?: number;
  __pdfPdfjsVersion?: string;
  __pdfProbes?: { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean };
};

const mirror = <K extends keyof PdfFrame>(page: Page, key: K) =>
  page.evaluate((k) => {
    const f = document.querySelector("iframe") as PdfFrame | null;
    return f ? f[k as keyof PdfFrame] : undefined;
  }, key as string);

async function ready(page: Page) {
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as PdfFrame | null;
      if (!f) return false;
      if (f.__pdfError) throw new Error("frame reported render error: " + f.__pdfError);
      return f.__pdfRendered === true;
    },
    undefined,
    { timeout: 25_000 }
  );
}

test.beforeEach(async ({ page }) => {
  await page.goto(PDF);
  await ready(page);
});

test("mounts in a genuinely opaque sandbox with no console/boot errors", async ({ page }) => {
  const errors: string[] = [];
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  page.on("pageerror", (e) => errors.push(String(e)));

  await page.reload();
  await ready(page);

  // The opacity assertion. A sandbox attribute can be edited; these three
  // probes are the observable consequence of allow-same-origin being absent,
  // and they are what actually guarantees a confidential document cannot reach
  // host cookies, storage or the host DOM.
  const probes = await mirror(page, "__pdfProbes");
  expect(probes, "frame must expose isolation probes").toBeTruthy();
  expect(probes).toEqual({ cookieEmpty: true, parentBlocked: true, storageBlocked: true });

  expect(errors.filter((e) => !/favicon/i.test(e))).toEqual([]);
});

test("renders real pixels, not just an error-free blank page", async ({ page }) => {
  expect(await mirror(page, "__pdfNonBlank")).toBe(true);

  // A floor, not an exact count: engines antialias differently and the sample
  // is deliberately simple. Zero is the failure this guards — a silently blank
  // canvas is exactly what the JPEG2000/JBIG2 wasm trap produced.
  const px = (await mirror(page, "__pdfNonWhitePixels")) as number;
  expect(px).toBeGreaterThan(1000);
});

test("exposes a text layer for selection, copy and screen readers", async ({ page }) => {
  const text = (await mirror(page, "__pdfText")) as string;
  expect(text).toContain("SPIKE_SECRET_ALPHA");

  const pages = (await mirror(page, "__pdfPageCount")) as number;
  expect(pages).toBeGreaterThanOrEqual(1);
});

test("the frame issues no programmatic network requests of its own", async ({ page }) => {
  // connect-src 'none' plus an opaque origin means the frame cannot fetch —
  // not its fonts, not its cMaps, and above all not the document. If this ever
  // regresses, the plugin has quietly acquired an exfiltration channel.
  //
  // The document IS fetched — /__gofastr/plugin/pdf/doc/{id} — but by the HOST
  // page adapter, which is privileged, same-origin, and carries the session and
  // CSRF token. That is the design, so the URL alone cannot distinguish a
  // legitimate host fetch from a frame escape. The originating frame can, so
  // that is what this asserts: no xhr/fetch may originate anywhere but the main
  // frame. (The frame's own <script>/<link> sub-resources are loaded by the
  // browser, not by frame code, and are a different resourceType.)
  const offenders: string[] = [];
  page.on("request", (r) => {
    const type = r.resourceType();
    if (type !== "xhr" && type !== "fetch") return;
    const f = r.frame();
    if (f && f !== page.mainFrame()) offenders.push(`${type} ${new URL(r.url()).pathname}`);
  });

  await page.reload();
  await ready(page);

  expect(offenders, `frame made programmatic requests: ${offenders.join(", ")}`).toEqual([]);
});
