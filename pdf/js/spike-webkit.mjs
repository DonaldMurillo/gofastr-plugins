// Throwaway WebKit (Safari engine) probe for the PDF spike.
//
// Launches WebKit via the playwright install already present under e2e/, points
// it at a running pdf spike server (it spawns `go run ./pdf/cmd/spike` itself),
// and dumps EVERY console message, page error, and failed request it sees — plus
// the iframe's mirrored render stats. WebKit matters more than Chromium for this
// spike (several shipped bugs in this repo were Safari-only; WebKit follows the
// CSP spec strictly where Chrome is lenient). Do NOT add this to e2e/tests/.
//
// Run from the repo root:   node pdf/js/spike-webkit.mjs
// (or from pdf/js:          node spike-webkit.mjs)

import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";

// Resolve playwright from the e2e/ install (NOT pdf/js — this script deliberately
// has no deps of its own so `npm ci` in pdf/js stays focused on the bundle).
const here = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(path.join(here, "..", "..", "e2e", "node_modules"));
const { webkit } = require("playwright");

const PORT = 8099;
const URL = `http://localhost:${PORT}/pdf`;
const repoRoot = path.resolve(here, "..", "..");

function log(line) {
  console.log("[webkit-spike] " + line);
}

async function waitForServer(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url);
      if (r.ok || r.status === 404) return; // server is up (404 ok: we hit /pdf)
      if (r.status === 500) return;
    } catch {
      // not up yet
    }
    await new Promise((res) => setTimeout(res, 250));
  }
  throw new Error(`server at ${url} did not come up in ${timeoutMs}ms`);
}

async function main() {
  // If a URL is passed on argv, use it (caller runs the server). Otherwise spawn
  // the spike server ourselves. GOWORK=off so the sibling gofastr checkout's
  // in-flight edits (another worker) cannot break the build here.
  const argUrl = process.argv[2];
  let srv = null;
  const url = argUrl || URL;
  if (!argUrl) {
    srv = spawn("go", ["run", "./pdf/cmd/spike"], {
      cwd: repoRoot,
      env: { ...process.env, GOWORK: "off", PORT: String(PORT) },
      stdio: ["ignore", "pipe", "pipe"],
      detached: true, // new process group so we can reap the go-run CHILD binary
    });
    srv.stdout.on("data", (d) => process.stdout.write("[srv:out] " + d));
    srv.stderr.on("data", (d) => process.stderr.write("[srv:err] " + d));
  }

  const browser = await webkit.launch({ headless: true });
  const messages = [];
  const pageerrors = [];
  const reqfails = [];
  const cspViolations = [];

  try {
    await waitForServer(url, 45000);
    const context = await browser.newContext();
    const page = await context.newPage();

    page.on("console", (msg) => {
      const type = msg.type();
      const text = msg.text();
      messages.push(`${type}: ${text}`);
      // CSP violations in WebKit surface as console messages mentioning the
      // Content Security Policy directive that blocked something.
      if (
        type === "error" &&
        (text.toLowerCase().includes("content security policy") ||
          text.toLowerCase().includes("refused to") ||
          text.includes("CSP"))
      ) {
        cspViolations.push(text);
      }
    });
    page.on("pageerror", (err) => pageerrors.push(String(err)));
    page.on("requestfailed", (req) => {
      const f = req.failure();
      reqfails.push(`${req.url()} — ${f ? f.errorText : "(unknown)"}`);
    });

    log("navigating to " + url);
    await page.goto(url, { waitUntil: "domcontentloaded", timeout: 30000 });
    // Wait for the iframe to mirror ready/rendered (or an error). 25s budget.
    const result = await page.evaluate(async () => {
      const deadline = Date.now() + 25000;
      while (Date.now() < deadline) {
        const f = document.querySelector("iframe");
        if (f) {
          if (f.__pdfError) return { error: f.__pdfError };
          if (f.__pdfRendered) {
            return {
              ready: true,
              text: f.__pdfText,
              pageCount: f.__pdfPageCount,
              nonBlank: f.__pdfNonBlank,
              nonWhitePixels: f.__pdfNonWhitePixels,
              pdfjsVersion: f.__pdfPdfjsVersion,
              probes: f.__pdfProbes,
            };
          }
        }
        await new Promise((r) => setTimeout(r, 150));
      }
      return { timeout: true };
    });

    // Give WebKit a moment to flush any trailing console/security notice.
    await page.waitForTimeout(800);
    const caps = await page.evaluate(() => {
      const f = document.querySelector("iframe");
      return f ? f.__pdfCaps || null : null;
    });

    console.log("\n========== WEBKIT SPIKE RESULT ==========");
    console.log("render:", JSON.stringify(result, null, 2));
    console.log("caps:", JSON.stringify(caps, null, 2));
    console.log("\n--- console messages (" + messages.length + ") ---");
    for (const m of messages) console.log("  " + m);
    console.log("\n--- page errors (" + pageerrors.length + ") ---");
    for (const e of pageerrors) console.log("  " + e);
    console.log("\n--- failed requests (" + reqfails.length + ") ---");
    for (const r of reqfails) console.log("  " + r);
    console.log("\n--- CSP violations (" + cspViolations.length + ") ---");
    for (const c of cspViolations) console.log("  " + c);

    const ok =
      result &&
      result.ready &&
      !result.error &&
      result.nonBlank &&
      typeof result.text === "string" &&
      result.text.includes("SPIKE_SECRET_ALPHA") &&
      pageerrors.length === 0 &&
      cspViolations.length === 0;
    console.log("\nVERDICT (webkit): " + (ok ? "PASS" : "FAIL"));
  } finally {
    try { await browser.close(); } catch { /* ignore */ }
    if (srv) {
      try {
        // detached:true spawned a new process group; -pid kills the whole group,
        // including the compiled binary `go run` would otherwise orphan.
        process.kill(-srv.pid, "SIGKILL");
      } catch {
        try { srv.kill("SIGTERM"); } catch { /* ignore */ }
      }
    }
  }
}

main().catch((err) => {
  console.error("[webkit-spike] fatal:", err);
  process.exit(1);
});
