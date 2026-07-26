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

// --- redaction ------------------------------------------------------------
//
// The journey the whole plugin exists for. It asserts against the bytes the
// HOST actually stored, not against the frame's opinion of its own work: a
// verifier grading its own homework proves nothing, and "the UI said done" is
// exactly the failure mode a redaction tool must not have.

const SECRET = "SPIKE_SECRET_ALPHA";
// A rect over the secret line, in PDF user space (points, bottom-left origin).
const SECRET_RECT = [40, 700, 400, 40];

test("redaction removes the content from the exported file, and proves it", async ({ page, request }) => {
  const frame = page.frameLocator("iframe");
  const inner = page.frames().find((f) => f !== page.mainFrame());
  expect(inner, "pdf frame must be present").toBeTruthy();

  // Author the redaction through the frame's own hook, then drive the real
  // arm/confirm UI — the confirmation is a load-bearing part of the design, so
  // the test goes through it rather than around it.
  await inner!.evaluate(
    ([pg, rect, reason]) =>
      (window as unknown as { __pdfAddRedaction: (p: number, r: number[], s: string) => void })
        .__pdfAddRedaction(pg as number, rect as number[], reason as string),
    [1, SECRET_RECT, "PII"]
  );
  await expect(frame.locator('button[aria-label="Apply redaction"]')).toBeVisible();
  await frame.locator('button[aria-label="Apply redaction"]').click();
  await frame.locator(".pdf-redact-confirm-btn").click();

  // Wait for the pipeline to settle via the HOST-side mirror.
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __pdfRedactState?: string }) | null;
      return f?.__pdfRedactState === "done" || f?.__pdfRedactState === "error";
    },
    undefined,
    { timeout: 60_000 }
  );

  const result = await page.evaluate(() => {
    const f = document.querySelector("iframe") as HTMLIFrameElement & {
      __pdfRedactState?: string;
      __pdfLastVerifyReport?: { ok: boolean; checks: { name: string; ok: boolean }[] };
      __pdfLastExportUrl?: string;
      __pdfLastExportError?: string;
    };
    return {
      state: f.__pdfRedactState,
      report: f.__pdfLastVerifyReport,
      url: f.__pdfLastExportUrl,
      error: f.__pdfLastExportError,
    };
  });

  expect(result.error ?? null, "export must not error").toBeNull();
  expect(result.state).toBe("done");

  // All six checks must pass. Naming them makes a silently-dropped check visible
  // — an empty checks array would otherwise satisfy an `every()`.
  expect(result.report?.ok).toBe(true);
  expect(result.report?.checks.map((c) => c.name).sort()).toEqual(
    ["annotations", "byteSearch", "incremental", "metadata", "rectIntersect", "textExtract"]
  );
  for (const c of result.report!.checks) expect(c.ok, `check ${c.name} must pass`).toBe(true);

  // Now the independent part: fetch what the host actually stored and search it
  // ourselves.
  expect(result.url, "host must return an export URL").toBeTruthy();
  const stored = await request.get(result.url!);
  expect(stored.ok()).toBe(true);
  const body = await stored.body();
  expect(body.length).toBeGreaterThan(1000);

  // SELF-CHECK FIRST. A plain `body.includes("SPIKE_SECRET_ALPHA")` would be
  // VACUOUS here: pdf-lib writes text as hex strings inside FlateDecode
  // streams, so the literal never appears in the raw bytes even BEFORE
  // redaction — the assertion would pass against an untouched file and prove
  // nothing. So we run the same search against the original document and
  // require it to FIND the secret. If this self-check ever stops finding it,
  // the search is broken and the absence check below is worthless.
  const original = await request.get("/__gofastr/plugin/pdf/doc/demo");
  expect(original.ok()).toBe(true);
  expect(
    await containsDecoded(await original.body(), SECRET),
    "self-check: the search must FIND the secret in the un-redacted source, " +
      "otherwise the absence assertion below is vacuous"
  ).toBe(true);

  expect(
    await containsDecoded(body, SECRET),
    "secret must not survive anywhere in the exported bytes"
  ).toBe(false);
});

// containsDecoded searches PDF bytes the way the in-frame verifier does:
// raw, then every inflated stream, then hex-string tokens decoded to text
// (including UTF-16BE). Anything less misses hex-encoded text and compressed
// object streams entirely — the trap that makes a naive `grep` report a clean
// file that is still leaking.
async function containsDecoded(buf: Buffer, needle: string): Promise<boolean> {
  const zlib = await import("node:zlib");
  const latin = buf.toString("latin1");
  const scan = (s: string): boolean => {
    if (s.includes(needle)) return true;
    // Hex strings: <48656C6C6F> — also handle a UTF-16BE BOM.
    for (const m of s.matchAll(/<([0-9A-Fa-f]{4,})>/g)) {
      const bytes = Buffer.from(m[1], "hex");
      if (bytes.toString("latin1").includes(needle)) return true;
      if (bytes.length > 2 && bytes[0] === 0xfe && bytes[1] === 0xff) {
        if (bytes.subarray(2).swap16().toString("utf16le").includes(needle)) return true;
      }
    }
    return false;
  };
  if (scan(latin)) return true;
  for (const m of latin.matchAll(/stream\r?\n/g)) {
    const start = m.index! + m[0].length;
    const end = latin.indexOf("endstream", start);
    if (end < 0) continue;
    try {
      if (scan(zlib.inflateSync(buf.subarray(start, end)).toString("latin1"))) return true;
    } catch {
      /* not a flate stream (raw image data, etc.) — the raw pass covered it */
    }
  }
  return false;
}
