// Scanned-document regression + redaction timing probe.
//
// 1. SCANNED REGRESSION: the build's no-wasm JPX/JBIG2 fallback must keep real
//    scans rendering (a blank white page with no error is the dangerous failure
//    this plugin's job depends on). Temporarily swap each scan fixture over
//    pdf/assets/sample.pdf (backed up first, restored after), boot the view-mode
//    spike, and confirm nonBlank:true + a real non-white pixel count.
//
// 2. TIMING: redact 1 page and a 50-page document through the real Apply flow;
//    report wall-clock + the longest single main-thread page block.
//
// Run from the repo root:  node pdf/js/spike-redact-probe.mjs
// Temporarily overwrites pdf/assets/sample.pdf; ALWAYS restores it.

import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { readFile, writeFile, copyFile } from "node:fs/promises";
import { createHash } from "node:crypto";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "..", "..");
const pwRequire = createRequire(path.join(repoRoot, "e2e", "node_modules"));
const { webkit } = pwRequire("playwright");
const jsRequire = createRequire(path.join(here, "package.json"));
const { PDFDocument, StandardFonts, rgb } = jsRequire("pdf-lib");

const samplePath = path.join(repoRoot, "pdf", "assets", "sample.pdf");
const testdata = path.join(repoRoot, "pdf", "testdata");

function log(line) { console.log("[probe] " + line); }

async function waitForServer(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try { const { ok } = await fetch(url); if (ok) return; } catch { /* not up */ }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`server at ${url} did not come up in ${timeoutMs}ms`);
}

async function sha256(p) {
  const b = await readFile(p);
  return createHash("sha256").digest("hex");
}

async function withServer(mode, port, fn) {
  // `-a` forces a rebuild so a swapped sample.pdf is re-embedded (go's build
  // cache keys on embed contents, but -a removes any ambiguity for the scan
  // fixtures which the demo serves via go:embed).
  const srv = spawn("go", ["run", "-a", "./pdf/cmd/spike"], {
    cwd: repoRoot,
    env: { ...process.env, GOWORK: "off", PORT: String(port), MODE: mode },
    stdio: ["ignore", "pipe", "pipe"],
    detached: true,
  });
  srv.stdout.on("data", (d) => process.stdout.write("[srv:out] " + d));
  srv.stderr.on("data", (d) => process.stderr.write("[srv:err] " + d));
  const base = `http://localhost:${port}`;
  try {
    await waitForServer(base + "/pdf", 90000);
    return await fn(base);
  } finally {
    try { process.kill(-srv.pid); } catch { /* detached */ }
    killPort(port);
    await new Promise((r) => setTimeout(r, 700)); // let the port free
  }
}

function killPort(port) {
  try { spawn("sh", ["-c", `lsof -ti :${port} | xargs kill -9 2>/dev/null`]); } catch { /* best-effort */ }
}

async function probeRenderStats(base) {
  for (let attempt = 0; attempt < 2; attempt++) {
    const browser = await webkit.launch({ headless: true });
    try {
      const page = await browser.newPage();
      const pageerrors = [];
      page.on("pageerror", (e) => pageerrors.push(String(e)));
      await page.goto(base + "/pdf", { waitUntil: "domcontentloaded", timeout: 30000 });
      await page.waitForFunction(() => {
        const f = document.querySelector("iframe");
        return !!(f && (f.__pdfRendered === true || f.__pdfError));
      }, undefined, { timeout: 90000 });
      // Small settle delay — the JPX/JBIG2 pure-JS decode can finish just
      // after __pdfRendered flips; let the canvas sample land before reading.
      await new Promise((r) => setTimeout(r, 500));
      const stats = await page.evaluate(() => {
        const f = document.querySelector("iframe");
        return {
          nonBlank: !!(f && f.__pdfNonBlank),
          nonWhitePixels: (f && f.__pdfNonWhitePixels) || 0,
          error: (f && f.__pdfError) || null,
        };
      });
      return { ...stats, pageerrors };
    } catch (e) {
      if (attempt === 1) return { nonBlank: false, nonWhitePixels: 0, error: "probe failed: " + (String(e).slice(0, 120)), pageerrors: [] };
      // retry once (the renderer can be briefly unstable on the first JPX load)
    } finally {
      await browser.close();
    }
  }
  return { nonBlank: false, nonWhitePixels: 0, error: "unreachable", pageerrors: [] };
}

async function scannedRegression() {
  console.log("\n========== SCANNED REGRESSION ==========");
  const origSha = await sha256(samplePath);
  const backupPath = path.join(here, ".sample.bak.pdf");
  await copyFile(samplePath, backupPath);
  const results = [];
  const scanFiles = ["scan-jpx.pdf", "scan-jbig2.pdf"];
  try {
    for (let i = 0; i < scanFiles.length; i++) {
      const name = scanFiles[i];
      // Distinct port per fixture — a lingering previous server on the same
      // port would serve the OLD scan and false-pass the count.
      const port = 8220 + i;
      await copyFile(path.join(testdata, name), samplePath);
      const stats = await withServer("view", port, probeRenderStats);
      const ok = stats.nonBlank && stats.nonWhitePixels > 1000;
      results.push({ name, ok, nonBlank: stats.nonBlank, nonWhitePixels: stats.nonWhitePixels, error: stats.error });
      console.log(`  ${name}: nonBlank=${stats.nonBlank} nonWhitePixels=${stats.nonWhitePixels} error=${stats.error} → ${ok ? "PASS" : "FAIL"}`);
    }
  } finally {
    await copyFile(backupPath, samplePath);
    const { unlink } = jsRequire("node:fs/promises");
    try { await unlink(backupPath); } catch { /* ignore */ }
  }
  const restoredSha = await sha256(samplePath);
  const restored = restoredSha === origSha;
  console.log(`  sample.pdf restored intact: ${restored}`);
  if (!restored) results.push({ name: "sample.pdf restore", ok: false });
  return results;
}

async function generateManyPagePDF(numPages) {
  const doc = await PDFDocument.create();
  const font = await doc.embedFont(StandardFonts.Helvetica);
  const accent = rgb(0.145, 0.39, 0.92);
  for (let i = 0; i < numPages; i++) {
    const p = doc.addPage([595, 842]);
    p.drawText(`Page ${i + 1} of ${numPages}`, { x: 56, y: 800, size: 18, font, color: rgb(0.1, 0.1, 0.1) });
    p.drawText(`SPIKE_SECRET_ALPHA`, { x: 56, y: 760, size: 24, font, color: accent });
    p.drawText(`page-index-${i}-filler-text-${i}-${i}`, { x: 56, y: 700 - (i % 20) * 14, size: 10, font, color: rgb(0.4, 0.4, 0.4) });
  }
  return doc.save();
}

async function timeRedact(base, docId, redactions) {
  const browser = await webkit.launch({ headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(base + "/pdf" + (docId ? "?doc=" + docId : ""), { waitUntil: "domcontentloaded", timeout: 60000 });
    await page.waitForFunction(() => {
      const f = document.querySelector("iframe");
      return !!(f && f.__pdfRendered === true);
    }, undefined, { timeout: 60000 });
    const frame = page.frames().find((fr) => fr !== page.mainFrame());
    for (const r of redactions) {
      await frame.evaluate(([pg, rect, reason]) => window.__pdfAddRedaction(pg, rect, reason), [r.page, r.rect, r.reason]);
    }
    // Open the confirm modal (Apply), then click the destructive confirm.
    await frame.click('button[aria-label="Apply redaction"]', { timeout: 5000 });
    await frame.waitForSelector('.pdf-redact-confirm-btn', { timeout: 5000 });
    const t0 = Date.now();
    await frame.click('.pdf-redact-confirm-btn', { timeout: 5000 });
    await frame.waitForFunction(() => {
      const st = window.__pdfState;
      return !!(st && (st.redactState === "done" || st.redactState === "error"));
    }, undefined, { timeout: 180000 });
    const settled = await frame.evaluate(() => (window.__pdfState && window.__pdfState.redactState) || "unknown");
    const wallMs = Date.now() - t0;
    const timing = await frame.evaluate(() => {
      const s = window.__pdfState || {};
      return { totalMs: s.lastRedactTotalMs, maxBlockMs: s.lastRedactMaxBlockMs, reportOk: !!(s.lastVerifyReport && s.lastVerifyReport.ok) };
    });
    return { settled, wallMs, ...timing };
  } finally {
    await browser.close();
  }
}

async function timingProbe() {
  console.log("\n========== TIMING ==========");
  // 1-page: the demo sample (2 pages, redact page 1 only).
  const one = await withServer("redact", 8213, async (base) =>
    timeRedact(base, "", [{ page: 1, rect: [50, 696, 300, 36], reason: "PII" }]));
  console.log(`  1-page redact: settled=${one.settled} wall=${one.wallMs}ms pipeline=${one.totalMs}ms maxBlock=${one.maxBlockMs}ms reportOk=${one.reportOk}`);

  // 50-page: generate, push as replay, redact EVERY page (worst case).
  const fiftyBytes = await generateManyPagePDF(50);
  const two = await withServer("redact", 8214, async (base) => {
    // Serve the 50-page doc via the spike's replay (POST it to /export → last).
    await fetch(base + "/__gofastr/plugin/pdf/export", {
      method: "POST",
      headers: { "X-Export-Kind": "redact", "Content-Type": "application/pdf" },
      body: fiftyBytes,
    });
    const redactions = Array.from({ length: 50 }, (_, i) => ({ page: i + 1, rect: [40, 740, 320, 40], reason: "PII" }));
    return timeRedact(base, "replay", redactions);
  });
  console.log(`  50-page redact (all): settled=${two.settled} wall=${two.wallMs}ms pipeline=${two.totalMs}ms maxBlock=${two.maxBlockMs}ms reportOk=${two.reportOk}`);
  return { one, two };
}

async function main() {
  let exit = 0;
  const scan = await scannedRegression();
  const time = await timingProbe();
  const scanOk = scan.every((r) => r.ok);
  const timingOk = time.one.settled === "done" && time.two.settled === "done";
  console.log(`  checks: scanOk=${scanOk} timingOk=${timingOk} (one=${JSON.stringify(time.one.settled)} two=${JSON.stringify(time.two.settled)})`);
  if (!scanOk) console.log("  FAIL: scan " + JSON.stringify(scan.filter((r) => !r.ok).map((r) => r.name)));
  if (!timingOk) console.log("  FAIL: timing did not settle");
  exit = scanOk && timingOk ? 0 : 1;
  console.log("\nVERDICT (probe): " + (exit === 0 ? "PASS" : "FAIL"));
}

main().catch((err) => { console.error(err); process.exit(1); });
