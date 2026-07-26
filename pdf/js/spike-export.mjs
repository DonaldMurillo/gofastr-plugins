// Round-trip export proof — the one verification that matters for P2.
//
// Spawns pdf/cmd/spike in MODE=annotate, drives the
// demo via Playwright/WebKit: places a highlight (text selection) and a text
// box, types a distinctive token, triggers Export, then re-points the viewer
// at /pdf?doc=replay (the server serves the just-produced bytes for that id)
// and asserts the annotation survived into the exported PDF — by reading the
// re-rendered text layer for the token.
//
// Run from the repo root:  node pdf/js/spike-export.mjs
//
// This is a throwaway; it is NOT added to e2e/tests/.

import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "..", "..");
const require = createRequire(path.join(repoRoot, "e2e", "node_modules"));
const { webkit } = require("playwright");

const PORT = 8119;
const BASE = `http://localhost:${PORT}`;
const TOKEN = "ROUND_TRIP_TOKEN_42";

function log(line) { console.log("[export-spike] " + line); }

async function waitForServer(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url);
      if (r.ok || r.status === 404 || r.status === 500) return;
    } catch { /* not up */ }
    await new Promise((res) => setTimeout(res, 250));
  }
  throw new Error(`server at ${url} did not come up in ${timeoutMs}ms`);
}

async function main() {
  // 1. Spawn the annotate+export server.
  const srv = spawn("go", ["run", "./pdf/cmd/spike"], {
    cwd: repoRoot,
    env: { ...process.env, GOWORK: "off", PORT: String(PORT), MODE: "annotate" },
    stdio: ["ignore", "pipe", "pipe"],
    detached: true,
  });
  srv.stdout.on("data", (d) => process.stdout.write("[srv:out] " + d));
  srv.stderr.on("data", (d) => process.stderr.write("[srv:err] " + d));

  const browser = await webkit.launch({ headless: true });
  let verdict = "FAIL";
  let detail = "";
  try {
    await waitForServer(BASE + "/pdf", 60000);
    log("server up; loading demo");

    const context = await browser.newContext();
    const page = await context.newPage();
    const pageErrors = [];
    page.on("pageerror", (e) => pageErrors.push(String(e)));

    // --- Load the demo (annotate mode). ---
    await page.goto(BASE + "/pdf", { waitUntil: "domcontentloaded", timeout: 30000 });
    await page.waitForFunction(() => {
      const f = document.querySelector("iframe");
      return f && f.__pdfRendered === true;
    }, undefined, { timeout: 25000 });
    log("page 1 rendered");

    // Confirm the editor mounted (annotate mode).
    const hasEditor = await page.evaluate(() => {
      const f = document.querySelector("iframe");
      return !!(f && f.contentWindow);
    });
    if (!hasEditor) throw new Error("editor iframe missing");

    // --- Place a text box: click the Text tool, then click on the page. ---
    // The tool buttons live inside the opaque-origin frame; Playwright can
    // reach into it via the iframe's frame context.
    const frame = page.frames().find((fr) => fr !== page.mainFrame());
    if (!frame) throw new Error("could not find the pdf iframe frame");

    // --- Place a text box: Text tool, then click on the page. (A highlight is
    //     exercised in the node-level export test — pdf/js export.ts draws
    //     highlight quads via the same drawAnnotation path. Driving a real
    //     text-selection drag inside the opaque frame is flaky, so the browser
    //     proof uses the text box, which gives a deterministic text assertion.) ---
    await frame.click('button[aria-label="Text"]', { timeout: 5000 });
    log("text tool selected");
    await frame.click('.pdf-page[data-page="1"]', { position: { x: 150, y: 300 }, force: true, timeout: 5000 });
    await frame.fill('.pdf-text-editor', TOKEN);
    // Dispatch input so the editor's commit-on-input fires even if the change
    // event races the blur; then blur to be safe.
    await frame.evaluate(() => {
      const el = document.querySelector('.pdf-text-editor');
      if (el) { el.dispatchEvent(new Event("input", { bubbles: true })); el.blur(); }
    });
    log("text box content: " + TOKEN);
    // Guard: wait until the frame's state reflects the annotation before export.
    await frame.waitForFunction(() => {
      const st = window.__pdfState;
      return !!(st && st.annotationCount >= 1);
    }, undefined, { timeout: 5000 });
    log("text box content: " + TOKEN);
    // --- Trigger export. ---
    await frame.click('button[aria-label="Export PDF"]', { timeout: 5000 });
    // Give the export path a moment, then capture the frame's status + mirrors
    // so a failure surfaces a real message instead of a silent timeout.
    const probe = await frame.evaluate(() => {
      const st = window.__pdfState || {};
      const status = (document.querySelector(".pdf-status") || {}).textContent || "";
      return {
        status,
        lastExportError: st.lastExportError || null,
        lastExportBytes: st.lastExportBytes || 0,
        annotationCount: st.annotationCount || 0,
      };
    });
    log("after export click: " + JSON.stringify(probe));
    // Wait for the export to round-trip: the adapter relays to /export, the
    // server stores lastBytes, and we can fetch /last-export once non-empty.
    await page.waitForFunction(async () => {
      try {
        const r = await fetch(BASE + "/last-export");
        if (!r.ok) return false;
        const buf = await r.arrayBuffer();
        return buf.byteLength > 100; // a real PDF is KB; the 404 body is 14 B
      } catch { return false; }
    }, undefined, { timeout: 15000 });
    const exported = await (await fetch(BASE + "/last-export")).arrayBuffer();
    log("export produced " + exported.byteLength + " bytes");

    // --- Re-open the produced bytes with the viewer (doc=replay) and assert
    //     the annotation text survived into the exported PDF. ---
    // --- Re-open the produced bytes with the viewer (doc=replay) and assert
    //     the annotation text survived into the exported PDF. A rapid nav can
    //     occasionally trip pdf.js's "same canvas during multiple render()"
    //     guard (a P1 render-scheduling race); retry once on error.
    let replay = { rendered: false, text: "", error: null };
    for (let attempt = 0; attempt < 2; attempt++) {
      await page.goto(BASE + "/pdf?doc=replay", { waitUntil: "domcontentloaded", timeout: 30000 });
      await page.waitForFunction(() => {
        const f = document.querySelector("iframe");
        if (!f) return false;
        return !!(f.__pdfRendered || f.__pdfError);
      }, undefined, { timeout: 25000 });
      replay = await page.evaluate(() => {
        const f = document.querySelector("iframe");
        return {
          rendered: !!(f && f.__pdfRendered),
          text: (f && f.__pdfText) || "",
          error: (f && f.__pdfError) || null,
        };
      });
      if (replay.rendered && !replay.error) break;
      log("replay attempt " + (attempt + 1) + " failed: " + replay.error + "; retrying");
    }

    const replayText = replay.text || "";
    const hasToken = replayText.includes(TOKEN);
    verdict = hasToken && pageErrors.length === 0 ? "PASS" : "FAIL";
    detail = `token "${TOKEN}" in replayed text layer: ${hasToken}; ` +
      `replay text length ${replayText.length}; page errors ${pageErrors.length}`;

    console.log("\n========== ROUND-TRIP RESULT ==========");
    console.log("exported bytes:", exported.byteLength);
    console.log("replay rendered:", replay.rendered, "error:", replay.error);
    console.log("replay text (first 240):", JSON.stringify(replayText.slice(0, 240)));
    console.log("assertion:", detail);
    console.log("page errors:", pageErrors);
    console.log("\nVERDICT (round-trip): " + verdict);

    // Save a screenshot of the replayed (annotated) page next to the script
    // (welcome per the brief; not asserted on). The replay page shows the
    // exported PDF with the text-box annotation baked in.
    await page.screenshot({ path: path.join(here, "spike-export-shot.png"), fullPage: false }).catch(() => {});
  } finally {
    try { await browser.close(); } catch { /* ignore */ }
    try { process.kill(-srv.pid); } catch { /* detached group */ }
  }

  if (verdict !== "PASS") process.exit(1);
}

main().catch((err) => {
  console.error("[export-spike] fatal:", err);
  process.exit(1);
});
