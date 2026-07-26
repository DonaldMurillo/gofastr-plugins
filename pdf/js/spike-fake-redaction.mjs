// The footgun regression test — a first-class deliverable.
//
// Proves that a COSMETIC redaction (an opaque black rectangle drawn over text
// with pdf-lib, NOT the real pipeline) STILL LEAKS the covered text three ways,
// and that the PRODUCTION verifier FAILS that file — then that the verifier
// PASSES a properly rasterized one produced by the real Apply flow.
//
// This exists so nobody can quietly regress into shipping cosmetic redaction.
// Run from the repo root:  node pdf/js/spike-fake-redaction.mjs
//
// Browser-driven via the Playwright in e2e/node_modules (like spike-export.mjs).
// The production verifier is reached through the in-frame __pdfVerifyRedaction
// test hook (pure function, no bridge) so the test asserts the SAME code that
// gates release — not a re-derived copy.

import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { readFile } from "node:fs/promises";
import zlib from "node:zlib";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "..", "..");
// playwright from e2e/ (this script deliberately has no deps of its own).
const pwRequire = createRequire(path.join(repoRoot, "e2e", "node_modules"));
const { webkit } = pwRequire("playwright");
// pdf-lib + pdfjs from pdf/js (the bundle's own dep set).
const jsRequire = createRequire(path.join(here, "package.json"));
const { PDFDocument, rgb } = jsRequire("pdf-lib");

const PORT = 8211;
const BASE = `http://localhost:${PORT}`;
const SECRET = "SPIKE_SECRET_ALPHA";
// SPIKE_SECRET_ALPHA sits at PDF (56, 702), size 28 (see scripts/gen-sample-pdf.mjs).
// Cover it generously — the rect only needs to overhang the glyphs.
const REDACT_RECT = [50, 696, 300, 36]; // [x, y, w, h] PDF user space

function log(line) { console.log("[fake-redact] " + line); }
// `go run` spawns a compiled binary that survives `process.kill(-srv.pid)`
// (a grandchild named "spike"). Kill the port holder directly so a stale
// server cannot false-pass a later run reusing the same port.
function killPort(port) {
  try { spawn("sh", ["-c", `lsof -ti :${port} | xargs kill -9 2>/dev/null`]); } catch { /* best-effort */ }
}

async function waitForServer(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const { ok } = await fetch(url);
      if (ok) return;
    } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`server at ${url} did not come up in ${timeoutMs}ms`);
}

// --- node-side leak probes -------------------------------------------------

async function nodeTextExtract(file) {
  const pdfjs = jsRequire("pdfjs-dist/legacy/build/pdf.mjs");
  const bytes = await readFile(file);
  const task = pdfjs.getDocument({ data: new Uint8Array(bytes), useSystemFonts: true, verbosity: 0 });
  const doc = await task.promise;
  const out = [];
  for (let i = 1; i <= doc.numPages; i++) {
    const page = await doc.getPage(i);
    const tc = await page.getTextContent();
    out.push({ page: i, text: tc.items.map((x) => x.str).join("") });
  }
  try { await task.destroy(); } catch { /* legacy build: best-effort */ }
  return out;
}

function byteGrep(buf, needle) {
  const b = Buffer.isBuffer(buf) ? buf : Buffer.from(buf);
  const latin = b.toString("latin1");
  const hits = [];
  const raw = latin.indexOf(needle);
  if (raw >= 0) hits.push({ source: "file:raw", at: raw });
  const streamRe = /stream\r?\n([\s\S]*?)\r?\nendstream/g;
  let m; let idx = 0;
  while ((m = streamRe.exec(latin))) {
    idx++;
    const bytes = Buffer.from(m[1], "latin1");
    let inflated = null;
    try { inflated = zlib.inflateSync(bytes); } catch {
      try { inflated = zlib.inflateRawSync(bytes); } catch { inflated = null; }
    }
    if (!inflated) continue;
    const dec = inflated.toString("latin1");
    // Also decode hex strings <...> + strip NULs (UTF-16BE hex), like the port.
    const dehex = dec.replace(/<([0-9A-Fa-f]{2,})>/g, (_, h) => {
      let s = "";
      for (let i = 0; i + 1 < h.length; i += 2) s += String.fromCharCode(parseInt(h.slice(i, i + 2), 16));
      return s;
    }).replace(/\u0000/g, "");
    const i1 = dec.indexOf(needle);
    if (i1 >= 0) hits.push({ source: `stream#${idx}:raw`, at: i1 });
    const i2 = dehex.indexOf(needle);
    if (i2 >= 0) hits.push({ source: `stream#${idx}:decoded`, at: i2 });
  }
  return hits;
}

async function buildFakeRedacted(samplePath, outPath) {
  const bytes = await readFile(samplePath);
  const doc = await PDFDocument.load(bytes, { ignoreEncryption: true });
  const page = doc.getPage(0);
  const [x, y, w, h] = REDACT_RECT;
  // THE footgun: an opaque black rectangle PAINTED OVER the text. The text
  // underneath is byte-intact and still selectable.
  page.drawRectangle({ x, y, width: w, height: h, color: rgb(0, 0, 0) });
  const out = await doc.save();
  const { writeFile } = jsRequire("node:fs/promises");
  await writeFile(outPath, out);
  return out;
}

async function main() {
  const samplePath = path.join(repoRoot, "pdf", "assets", "sample.pdf");
  const fakePath = path.join(here, "spike-fake-redacted.pdf");

  // 0. spawn the redact-mode spike server.
  const srv = spawn("go", ["run", "./pdf/cmd/spike"], {
    cwd: repoRoot,
    env: { ...process.env, GOWORK: "off", PORT: String(PORT), MODE: "redact" },
    stdio: ["ignore", "pipe", "pipe"],
    detached: true,
  });
  srv.stdout.on("data", (d) => process.stdout.write("[srv:out] " + d));
  srv.stderr.on("data", (d) => process.stderr.write("[srv:err] " + d));

  const browser = await webkit.launch({ headless: true });
  const results = [];
  let exit = 0;
  try {
    await waitForServer(BASE + "/pdf", 60000);
    log("server up (redact mode)");

    // 1. build the fake-redacted file (pdf-lib black rect over the secret).
    const fakeBytes = await buildFakeRedacted(samplePath, fakePath);
    log(`built fake-redacted.pdf (${fakeBytes.length} B)`);

    // 2. LEAK #1: pdf.js getTextContent still reads the secret.
    const pages = await nodeTextExtract(fakePath);
    const leak1Pages = pages.filter((p) => p.text.includes(SECRET)).map((p) => p.page);
    results.push({ name: "leak1:pdfjs-extract", pass: leak1Pages.length > 0, detail: `secret on pages ${JSON.stringify(leak1Pages)}` });

    // 3. LEAK #2: decompressed-byte grep finds the secret in a stream.
    const ghits = byteGrep(fakeBytes, SECRET);
    results.push({ name: "leak2:byte-grep", pass: ghits.length > 0, detail: `${ghits.length} hit(s): ${JSON.stringify(ghits.slice(0, 3))}` });

    // 4. Browser: PASS case — real Apply flow on a redaction over the secret.
    const context = await browser.newContext();
    const page = await context.newPage();
    const pageErrors = [];
    page.on("pageerror", (e) => pageErrors.push(String(e)));
    await page.goto(BASE + "/pdf", { waitUntil: "domcontentloaded", timeout: 30000 });
    await page.waitForFunction(() => {
      const f = document.querySelector("iframe");
      return !!(f && f.__pdfRendered === true);
    }, undefined, { timeout: 25000 });
    log("demo rendered");

    const frame = page.frames().find((fr) => fr !== page.mainFrame());
    if (!frame) throw new Error("pdf iframe frame not found");

    // Author the redaction rect over the secret through the real command path.
    await frame.evaluate(([pg, rect, reason]) => window.__pdfAddRedaction(pg, rect, reason), [1, REDACT_RECT, "PII"]);
    await frame.waitForFunction(() => {
      const st = window.__pdfState;
      return !!(st && st.redactionCount >= 1);
    }, undefined, { timeout: 5000 });
    log("redaction authored via __pdfAddRedaction");

    // Open the confirm modal (Apply), then click the destructive confirm.
    await frame.click('button[aria-label="Apply redaction"]', { timeout: 5000 });
    await frame.waitForSelector('.pdf-redact-confirm-btn', { timeout: 5000 });
    await frame.click('.pdf-redact-confirm-btn', { timeout: 5000 });
    // Poll the frame's redactState — "done" = emitted, "error" = pipeline failed.
    // (No node closure inside the page function: it reads window.__pdfState.)
    const settled = await frame.waitForFunction(() => {
      const st = window.__pdfState;
      if (!st) return false;
      return (st.redactState === "done" || st.redactState === "error") ? st.redactState : false;
    }, undefined, { timeout: 60000 });
    const stAfter = await frame.evaluate(() => {
      const s = window.__pdfState || {};
      return {
        redactState: s.redactState, lastExportBytes: s.lastExportBytes,
        lastExportError: s.lastExportError,
        hasReport: !!s.lastVerifyReport, reportOk: !!(s.lastVerifyReport && s.lastVerifyReport.ok),
        reportChecks: s.lastVerifyReport ? s.lastVerifyReport.checks.map((c) => c.name + ":" + (c.ok ? "✓" : "✗")) : null,
      };
    });
    log("redact settled: " + settled + " :: " + JSON.stringify(stAfter));
    // Fetch the produced bytes from node (the host stores them at /last-export).
    const realBytes = new Uint8Array(await (await fetch(BASE + "/last-export")).arrayBuffer());
    log(`real-redacted bytes: ${realBytes.length} B`);

    const allChecksPass = !!stAfter.hasReport && stAfter.reportOk && stAfter.redactState === "done" && realBytes.length > 100;
    results.push({ name: "pass:rasterized-verifier", pass: allChecksPass, detail: `redactState=${stAfter.redactState} reportOk=${stAfter.reportOk} bytes=${realBytes.length} checks=${JSON.stringify(stAfter.reportChecks)} err=${stAfter.lastExportError}` });

    // Re-run the production verifier on the real bytes through the hook — must PASS.
    const realVerify = await frame.evaluate(async ([bytes, rect]) => {
      const r = await window.__pdfVerifyRedaction(bytes, {
        needles: ["SPIKE_SECRET_ALPHA"],
        redactions: [{ page: 1, rect }],
      });
      return { ok: r.ok, checks: r.checks.map((c) => ({ name: c.name, ok: c.ok })) };
    }, [realBytes, REDACT_RECT]);
    results.push({ name: "pass:verifier-on-real-bytes", pass: realVerify.ok, detail: JSON.stringify(realVerify.checks) });

    // 5. FAIL case — the production verifier on the FAKE bytes must FAIL.
    const fakeVerify = await frame.evaluate(async ([bytes, rect]) => {
      const r = await window.__pdfVerifyRedaction(bytes, {
        needles: ["SPIKE_SECRET_ALPHA"],
        redactions: [{ page: 1, rect }],
      });
      return { ok: r.ok, fails: r.checks.filter((c) => !c.ok).map((c) => c.name), checks: r.checks.map((c) => ({ name: c.name, ok: c.ok, detail: c.detail })) };
    }, [fakeBytes, REDACT_RECT]);
    const failsFake = !fakeVerify.ok && fakeVerify.fails.includes("textExtract") && fakeVerify.fails.includes("rectIntersect");
    results.push({ name: "fail:verifier-rejects-fake", pass: failsFake, detail: `ok=${fakeVerify.ok} failing=${JSON.stringify(fakeVerify.fails)}` });

    // 6. LEAK #3: real browser selectability — load the fake bytes via replay
    //    (POST them to /export so the spike serves them as doc=replay) and read
    //    the live text layer + a programmatic selection.
    await fetch(BASE + "/__gofastr/plugin/pdf/export", {
      method: "POST",
      headers: { "X-Export-Kind": "redact", "Content-Type": "application/pdf" },
      body: fakeBytes,
    });
    await page.goto(BASE + "/pdf?doc=replay", { waitUntil: "domcontentloaded", timeout: 30000 });
    await page.waitForFunction(() => {
      const f = document.querySelector("iframe");
      return !!(f && (f.__pdfRendered === true || f.__pdfError));
    }, undefined, { timeout: 25000 });
    // The text layer lives INSIDE the opaque frame; the host page cannot cross
    // the sandbox (allow-scripts, no allow-same-origin) to read it. So drive a
    // programmatic selection FROM the frame context, where it is allowed — this
    // is the "selectable in a real browser" property (clipboard-write is the
    // host's separate capability, intentionally blocked here). The replay
    // navigation recreated the iframe, so re-acquire its frame handle.
    const replayFrame = page.frames().find((fr) => fr !== page.mainFrame());
    if (!replayFrame) throw new Error("replay iframe frame not found");
    const selProbe = await replayFrame.evaluate(() => {
      let selected = "";
      let textLen = 0;
      let textHasSecret = false;
      try {
        const layer = document.querySelector(".text-layer");
        if (layer) {
          textLen = layer.textContent.length;
          textHasSecret = layer.textContent.includes("SPIKE_SECRET_ALPHA");
          const range = document.createRange();
          range.selectNodeContents(layer);
          const sel = window.getSelection();
          sel.removeAllRanges();
          sel.addRange(range);
          selected = sel.toString();
        }
      } catch { selected = "<selection-error>"; }
      return {
        textHasSecret,
        selectedHasSecret: selected.includes("SPIKE_SECRET_ALPHA"),
        textLen, selectedLen: selected.length,
      };
    });
    results.push({ name: "leak3:browser-selectable", pass: selProbe.textHasSecret && selProbe.selectedHasSecret, detail: `textLayerHasSecret=${selProbe.textHasSecret} selectionHasSecret=${selProbe.selectedHasSecret} textLen=${selProbe.textLen}` });

    results.push({ name: "clean:zero-page-errors", pass: pageErrors.length === 0, detail: pageErrors.length ? JSON.stringify(pageErrors) : "0 page errors" });

    // Report card.
    console.log("\n========== FAKE-REDACTION REGRESSION ==========");
    for (const r of results) {
      const tag = r.pass ? "PASS" : "FAIL";
      if (!r.pass) exit = 1;
      console.log(`  [${tag}] ${r.name}`);
      console.log(`        ${r.detail}`);
    }
    const allPass = results.every((r) => r.pass);
    console.log("\nVERDICT (fake-redaction): " + (allPass ? "PASS" : "FAIL"));
    console.log("  leak proofs (must ALL be leak=STILL-PRESENT): " + ["leak1:pdfjs-extract", "leak2:byte-grep", "leak3:browser-selectable"]
      .map((n) => n + "=" + (results.find((r) => r.name === n)?.pass ? "LEAK-PROVEN" : "MISSING")).join(", "));
    console.log("  verifier (must PASS real, FAIL fake): " + ["pass:verifier-on-real-bytes", "fail:verifier-rejects-fake"]
      .map((n) => n + "=" + (results.find((r) => r.name === n)?.pass ? "CORRECT" : "WRONG")).join(", "));
  } finally {
    try { await browser.close(); } catch { /* ignore */ }
    try { process.kill(-srv.pid); } catch { /* detached group */ }
    killPort(PORT);
  }
  process.exit(exit);
}

main().catch((err) => { console.error(err); process.exit(1); });
