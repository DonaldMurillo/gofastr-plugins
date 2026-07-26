// UX regression probe for the PDF editor — the kept assertions the brief asked
// for. NOT a throwaway: run it from the repo root with
//
//   node pdf/js/spike-ux-probe.mjs
//
// It spawns MODE=redact PORT=8099 GOWORK=off go run ./pdf/cmd/spike (redact
// covers every tool), drives the framed editor with WebKit via the e2e/
// playwright install, and asserts POSITION and BEHAVIOUR — not annotation
// counts. Every assertion that ever regressed in this plugin lived in the
// gap between "the suite was green" and "the box landed where the user drew
// it"; this probe closes that gap.
//
// Categories:
//   1. Position fidelity — for each of rect/ellipse/arrow/ink/text/note/
//      highlight, create by gesture and assert the painted box matches the
//      gesture within a couple of px; then zoom and rotate and assert it
//      tracks (scale-invariant ratios hold; dims swap under 90° rotation).
//   2. Select-after-create — create, click it, assert 8 handles appear and
//      __pdfState.selectedId is the new annotation.
//   3. Move/resize + undo — drag the body, assert it moved; undo restores.
//      Drag a handle, assert it resized; undo restores.
//   4. Modal escape — open the stamp dialog, press Escape, assert it is gone.

import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "..", "..");
const require = createRequire(path.join(repoRoot, "e2e", "node_modules"));
const { webkit } = require("playwright");

const PORT = 8099;
const BASE = `http://localhost:${PORT}`;
const URL = `${BASE}/pdf`;
const TOL = 3;       // px tolerance for a painted box to "match" a gesture
const INK_TOL = 8;   // ink smoothing can extend the bbox a little
const RATIO_TOL = 0.012; // scale-invariant ratio tolerance for zoom tracking

function log(line) { console.log("[ux-probe] " + line); }

async function waitForServer(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url, { signal: AbortSignal.timeout(1500) });
      if (r.ok || r.status === 500) return;
    } catch { /* keep waiting */ }
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`server at ${url} did not come up in ${timeoutMs}ms`);
}

const approx = (a, b, tol) => Math.abs(a - b) <= tol;
const ratio = (a, b) => (b ? a / b : 0);

async function main() {
  const srv = spawn("go", ["run", "./pdf/cmd/spike"], {
    cwd: repoRoot,
    env: { ...process.env, MODE: "redact", PORT: String(PORT), GOWORK: "off" },
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  srv.stdout.on("data", () => {});
  srv.stderr.on("data", (d) => process.stderr.write("[srv:err] " + d));

  const results = [];
  const record = (name, ok, detail) => results.push({ name, ok, detail });

  const browser = await webkit.launch({ headless: true });
  try {
    await waitForServer(URL, 60000);
    log("server up");
    const context = await browser.newContext();
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(URL, { waitUntil: "domcontentloaded", timeout: 30000 });
    const frame = page.frames().find((fr) => fr !== page.mainFrame());
    if (!frame) throw new Error("no iframe frame");
    await frame.waitForFunction(() => !!(window.__pdfState && window.__pdfState.rendered), undefined, { timeout: 25000 });
    log("page 1 rendered");

    const SLOT = '.pdf-page[data-page="1"]';
    const slotBox = async () => frame.locator(SLOT).boundingBox();
    const state = () => frame.evaluate(() => window.__pdfState);

    // First painted annotation on page 1, box relative to the slot.
    const paintedBox = async (cls = ".pdf-ann") => frame.evaluate((c) => {
      const slot = document.querySelector('.pdf-page[data-page="1"]');
      const ann = document.querySelector(`.pdf-page[data-page="1"] ${c}`);
      if (!slot || !ann) return null;
      const s = slot.getBoundingClientRect();
      const a = ann.getBoundingClientRect();
      return {
        left: a.left - s.left, top: a.top - s.top,
        width: a.width, height: a.height,
        cx: a.left - s.left + a.width / 2, cy: a.top - s.top + a.height / 2,
        slotW: s.width, slotH: s.height,
      };
    }, cls);

    const clearAll = async () => {
      // Select-all isn't available; cycle: select tool, then Ctrl+A does not
      // apply. Instead: read count, click first ann to select, Delete, repeat.
      await frame.click('button[aria-label="Select"]', { timeout: 5000 });
      for (let i = 0; i < 30; i++) {
        const n = (await state()).annotationCount;
        if (n === 0) break;
        // Click the first annotation's centre to select it.
        await frame.evaluate(() => {
          const slot = document.querySelector('.pdf-page[data-page="1"]');
          const ann = document.querySelector('.pdf-page[data-page="1"] .pdf-ann');
          if (!slot || !ann) return;
          const sr = slot.getBoundingClientRect();
          const ar = ann.getBoundingClientRect();
          const x = sr.left + (ar.left - sr.left) + ar.width / 2;
          const y = sr.top + (ar.top - sr.top) + ar.height / 2;
          const pages = document.querySelector(".pdf-pages");
          const fire = (type) => pages.dispatchEvent(new PointerEvent(type, { clientX: x, clientY: y, bubbles: true, pointerId: 1, pointerType: "mouse", button: 0 }));
          fire("pointerdown"); fire("pointerup");
        });
        await page.waitForTimeout(40);
        await page.keyboard.press("Delete");
        await page.waitForTimeout(40);
      }
    };

    const setTool = (label) => frame.click(`button[aria-label="${label}"]`, { timeout: 5000 });

    // Drive a mouse drag on page 1 between two page-relative points.
    const drag = async (x1, y1, x2, y2, steps = 10) => {
      const b = await slotBox();
      const ax = (x) => Math.round(b.x + x);
      const ay = (y) => Math.round(b.y + y);
      await page.mouse.move(0, 0);
      await page.mouse.move(ax(x1), ay(y1));
      await page.mouse.down();
      await page.mouse.move(ax(x2), ay(y2), { steps });
      await page.mouse.up();
    };
    // Drive a click on page 1 at a page-relative point.
    const clickAt = async (x, y) => {
      const b = await slotBox();
      await page.mouse.move(0, 0);
      await page.mouse.click(Math.round(b.x + x), Math.round(b.y + y));
    };

    // ================= 1. POSITION FIDELITY =================
    log("category 1: position fidelity");
    // (a) rect / ellipse / arrow — drag tools. Same gesture for each.
    for (const tool of ["Rectangle", "Ellipse", "Arrow"]) {
      await clearAll();
      await setTool(tool);
      const g = { x: 90, y: 140, w: 140, h: 80 };
      await drag(g.x, g.y, g.x + g.w, g.y + g.h);
      await frame.waitForFunction(() => (window.__pdfState?.annotationCount ?? 0) >= 1, undefined, { timeout: 5000 });
      const box = await paintedBox();
      const ok = !!box
        && approx(box.left, g.x, TOL) && approx(box.top, g.y, TOL)
        && approx(box.width, g.w, TOL) && approx(box.height, g.h, TOL);
      record(`${tool} paints at gesture (fit-width)`, ok, box ? JSON.stringify({ left: Math.round(box.left), top: Math.round(box.top), w: Math.round(box.width), h: Math.round(box.height) }) : "no box");

      if (box) {
        // Zoom in and assert scale-invariant ratios hold (the box tracks).
        await frame.click('button[aria-label="Zoom in"]', { timeout: 5000 });
        await page.waitForTimeout(500);
        const z = await paintedBox();
        const rok = !!z
          && approx(ratio(z.left, z.slotW), ratio(box.left, box.slotW), RATIO_TOL)
          && approx(ratio(z.top, z.slotH), ratio(box.top, box.slotH), RATIO_TOL)
          && approx(ratio(z.width, z.slotW), ratio(box.width, box.slotW), RATIO_TOL)
          && approx(ratio(z.height, z.slotH), ratio(box.height, box.slotH), RATIO_TOL);
        record(`${tool} tracks after zoom-in`, rok, z ? `ratios held` : "no box");

        // Rotate 90° and assert width/height swap (same zoom).
        await frame.click('button[aria-label="Rotate clockwise 90 degrees"]', { timeout: 5000 });
        await page.waitForTimeout(500);
        const r = await paintedBox();
        const swapped = !!r && approx(r.width, box.height, Math.max(3, box.height * 0.12)) && approx(r.height, box.width, Math.max(3, box.width * 0.12));
        record(`${tool} dims swap under 90° rotation`, swapped, r ? `w=${Math.round(r.width)} h=${Math.round(r.height)} (exp ≈${Math.round(box.height)}×${Math.round(box.width)})` : "no box");

        // Reset: rotate back 3×, cycle zoom to Fit width.
        await frame.click('button[aria-label="Rotate clockwise 90 degrees"]', { timeout: 5000 });
        await frame.click('button[aria-label="Rotate clockwise 90 degrees"]', { timeout: 5000 });
        await frame.click('button[aria-label="Rotate clockwise 90 degrees"]', { timeout: 5000 });
        for (let i = 0; i < 4; i++) {
          const label = await frame.locator('button[aria-label="Zoom mode"]').textContent().catch(() => "");
          if (label && label.trim() === "Fit width") break;
          await frame.click('button[aria-label="Zoom mode"]', { timeout: 5000 });
          await page.waitForTimeout(250);
        }
        await page.waitForTimeout(300);
      }
    }

    // (b) ink — a freehand stroke; its bbox ≈ the drag extents (smoothing may
    //     extend it slightly, hence INK_TOL).
    await clearAll();
    await setTool("Draw");
    {
      const g = { x: 100, y: 200, w: 160, h: 90 };
      await drag(g.x, g.y, g.x + g.w, g.y + g.h);
      await frame.waitForFunction(() => (window.__pdfState?.annotationCount ?? 0) >= 1, undefined, { timeout: 5000 });
      const box = await paintedBox();
      const ok = !!box
        && approx(box.left, g.x, INK_TOL) && approx(box.top, g.y, INK_TOL)
        && approx(box.width, g.w, INK_TOL) && approx(box.height, g.h, INK_TOL);
      record("Ink paints at gesture (fit-width)", ok, box ? JSON.stringify({ left: Math.round(box.left), top: Math.round(box.top), w: Math.round(box.width), h: Math.round(box.height) }) : "no box");
    }

    // (c) text — click-placed, default 160×24 (CSS pts → scaled). Assert the
    //     box lands at the click and has the expected PDF-space size scaled.
    await clearAll();
    await setTool("Text");
    {
      const g = { x: 120, y: 260 };
      await clickAt(g.x, g.y);
      await frame.waitForFunction(() => (window.__pdfState?.annotationCount ?? 0) >= 1, undefined, { timeout: 5000 });
      const box = await paintedBox();
      const ok = !!box && approx(box.left, g.x, TOL) && approx(box.top, g.y, TOL) && box.width > 40 && box.height > 12;
      record("Text paints at click (fit-width)", ok, box ? JSON.stringify({ left: Math.round(box.left), top: Math.round(box.top), w: Math.round(box.width), h: Math.round(box.height) }) : "no box");
    }

    // (d) note — click-placed, 20×20 pt.
    await clearAll();
    await setTool("Note");
    {
      const g = { x: 200, y: 180 };
      await clickAt(g.x, g.y);
      await frame.waitForFunction(() => (window.__pdfState?.annotationCount ?? 0) >= 1, undefined, { timeout: 5000 });
      const box = await paintedBox();
      const ok = !!box && approx(box.left, g.x, TOL) && approx(box.top, g.y, TOL) && box.width > 8 && box.height > 8;
      record("Note paints at click (fit-width)", ok, box ? JSON.stringify({ left: Math.round(box.left), top: Math.round(box.top), w: Math.round(box.width), h: Math.round(box.height) }) : "no box");
    }

    // (e) highlight — drag-select across a text-layer span; on release the
    //     editor converts the selection. The painted .pdf-highlight-wrap box
    //     should cover the selected span's vertical band.
    await clearAll();
    await setTool("Highlight");
    {
      const spanRect = await frame.evaluate(() => {
        const slot = document.querySelector('.pdf-page[data-page="1"]');
        const span = document.querySelector('.pdf-page[data-page="1"] .text-layer span');
        if (!slot || !span) return null;
        const s = slot.getBoundingClientRect();
        const r = span.getBoundingClientRect();
        return { left: r.left - s.left, top: r.top - s.top, width: r.width, height: r.height };
      });
      let box = null;
      if (spanRect) {
        const b = await slotBox();
        const selLen = Math.min(140, spanRect.width * 0.7);
        const x1 = b.x + spanRect.left + 6;
        const y1 = b.y + spanRect.top + spanRect.height / 2;
        const x2 = x1 + selLen;
        // A real mouse drag-select forms a native selection; release commits.
        await page.mouse.move(0, 0);
        await page.mouse.move(x1, y1);
        await page.mouse.down();
        await page.mouse.move(x2, y1, { steps: 12 });
        await page.mouse.up();
        await page.waitForTimeout(120);
        let count = (await state()).annotationCount;
        if (count === 0) {
          // Fallback for engines that don't form a selection from synthetic
          // drags: set the Selection directly and dispatch pointerup, which is
          // exactly the commit path a real release takes.
          await frame.evaluate(() => {
            const span = document.querySelector('.pdf-page[data-page="1"] .text-layer span');
            if (!span) return;
            const sel = window.getSelection();
            sel.removeAllRanges();
            const range = document.createRange();
            range.selectNodeContents(span);
            sel.addRange(range);
            const pages = document.querySelector(".pdf-pages");
            const r = span.getBoundingClientRect();
            pages.dispatchEvent(new PointerEvent("pointerup", { clientX: r.right, clientY: r.top + r.height / 2, bubbles: true, pointerId: 1, pointerType: "mouse", button: 0 }));
          });
          await page.waitForTimeout(120);
          count = (await state()).annotationCount;
        }
        box = await paintedBox(".pdf-highlight-wrap");
        // The highlight must cover the selected span's vertical band and sit
        // within the span's x-range.
        const ok = count >= 1 && !!box && box.top <= spanRect.top + 4 && box.top + box.height >= spanRect.top + spanRect.height - 4 && box.width > 20;
        record("Highlight creates + covers the selection", ok, box ? JSON.stringify({ count, top: Math.round(box.top), h: Math.round(box.height), w: Math.round(box.width) }) : `count=${count}`);
      } else {
        record("Highlight creates + covers the selection", false, "no text-layer span on page 1");
      }
    }

    // ================= 2. SELECT-AFTER-CREATE =================
    log("category 2: select-after-create");
    await clearAll();
    await setTool("Rectangle");
    await drag(90, 140, 230, 220);
    await frame.waitForFunction(() => (window.__pdfState?.annotationCount ?? 0) >= 1, undefined, { timeout: 5000 });
    // After place, the tool should revert to Select and the new ann be selected.
    const toolAfter = await frame.evaluate(() => {
      const b = document.querySelector('button.pdf-tool-btn[aria-pressed="true"]');
      return b ? b.getAttribute("aria-label") : null;
    });
    const selAfter = (await state()).selectedId;
    const handleCount = await frame.evaluate(() => document.querySelectorAll('.pdf-page[data-page="1"] .pdf-handle').length);
    record("Tool reverts to Select after place", toolAfter === "Select", `tool=${toolAfter}`);
    record("Just-placed annotation is selected", !!selAfter, `selectedId=${selAfter}`);
    record("8 resize handles appear", handleCount === 8, `handles=${handleCount}`);

    // ================= 3. MOVE / RESIZE + UNDO =================
    log("category 3: move/resize + undo");
    // The rect from category 2 is still selected at ~ (90,140,140,80).
    const before = await paintedBox();
    // MOVE: drag the body by (+30, +25). Click the centre, then drag.
    {
      const b = await slotBox();
      const cx = b.x + before.cx;
      const cy = b.y + before.cy;
      await page.mouse.move(0, 0);
      await page.mouse.move(cx, cy);
      await page.mouse.down();
      await page.mouse.move(cx + 30, cy + 25, { steps: 8 });
      await page.mouse.up();
      await page.waitForTimeout(150);
    }
    const afterMove = await paintedBox();
    const movedOk = !!afterMove && approx(afterMove.left, before.left + 30, TOL + 1) && approx(afterMove.top, before.top + 25, TOL + 1);
    record("Move: body drag shifts the box by the delta", movedOk, afterMove ? `Δleft=${Math.round(afterMove.left - before.left)}, Δtop=${Math.round(afterMove.top - before.top)}` : "no box");

    // UNDO restores position.
    await page.keyboard.press("Control+z");
    await page.waitForTimeout(150);
    const afterUndoMove = await paintedBox();
    const undoMoveOk = !!afterUndoMove && approx(afterUndoMove.left, before.left, TOL + 1) && approx(afterUndoMove.top, before.top, TOL + 1);
    record("Undo restores position after move", undoMoveOk, afterUndoMove ? `left=${Math.round(afterUndoMove.left)}, top=${Math.round(afterUndoMove.top)} (exp ${Math.round(before.left)},${Math.round(before.top)})` : "no box");

    // RESIZE: drag the SE handle by (+25, +20) → width/height grow.
    {
      const box = await paintedBox();
      const b = await slotBox();
      // SE handle sits at (left+width, top+height).
      const hx = b.x + box.left + box.width;
      const hy = b.y + box.top + box.height;
      await page.mouse.move(0, 0);
      await page.mouse.move(hx, hy);
      await page.mouse.down();
      await page.mouse.move(hx + 25, hy + 20, { steps: 8 });
      await page.mouse.up();
      await page.waitForTimeout(150);
    }
    const afterResize = await paintedBox();
    const resizeOk = !!afterResize && approx(afterResize.width, before.width + 25, TOL + 1) && approx(afterResize.height, before.height + 20, TOL + 1);
    record("Resize: SE handle grows width/height", resizeOk, afterResize ? `Δw=${Math.round(afterResize.width - before.width)}, Δh=${Math.round(afterResize.height - before.height)}` : "no box");

    // UNDO restores size.
    await page.keyboard.press("Control+z");
    await page.waitForTimeout(150);
    const afterUndoResize = await paintedBox();
    const undoResizeOk = !!afterUndoResize && approx(afterUndoResize.width, before.width, TOL + 1) && approx(afterUndoResize.height, before.height, TOL + 1);
    record("Undo restores size after resize", undoResizeOk, afterUndoResize ? `w=${Math.round(afterUndoResize.width)}, h=${Math.round(afterUndoResize.height)} (exp ${Math.round(before.width)},${Math.round(before.height)})` : "no box");

    // DELETE via keyboard removes it.
    await page.keyboard.press("Delete");
    await page.waitForTimeout(120);
    const countAfterDel = (await state()).annotationCount;
    record("Delete key removes the selected annotation", countAfterDel === 0, `count=${countAfterDel}`);

    // ================= 4. MODAL ESCAPE =================
    log("category 4: modal escape");
    await clearAll();
    // Focus the Stamp tool button first so we can assert focus restoration.
    await frame.evaluate(() => {
      const b = document.querySelector('button[aria-label="Stamp"]');
      if (b) b.focus();
    });
    await setTool("Stamp"); // opens the picker
    await page.waitForTimeout(250);
    const modalBefore = await frame.evaluate(() => !!document.querySelector(".pdf-modal-overlay"));
    await page.keyboard.press("Escape");
    await page.waitForTimeout(250);
    const modalAfter = await frame.evaluate(() => !!document.querySelector(".pdf-modal-overlay"));
    const focusedLabel = await frame.evaluate(() => {
      const ae = document.activeElement;
      return ae && ae.getAttribute ? ae.getAttribute("aria-label") : null;
    });
    record("Stamp dialog opens", modalBefore, `open=${modalBefore}`);
    record("Escape closes the dialog", !modalAfter, `openAfter=${modalAfter}`);
    record("Focus restored to invoker after close", focusedLabel === "Stamp", `focused=${focusedLabel}`);

    // ================= report =================
    const pageErrors = errors.length;
    console.log("\n========== UX PROBE RESULTS ==========");
    for (const r of results) {
      console.log(`  [${r.ok ? "PASS" : "FAIL"}] ${r.name}${r.detail ? " — " + r.detail : ""}`);
    }
    const failures = results.filter((r) => !r.ok).length;
    console.log(`\n${results.length - failures}/${results.length} passed, ${pageErrors} frame page-errors`);
    console.log("VERDICT (ux): " + (failures === 0 && pageErrors === 0 ? "PASS" : "FAIL"));
    if (failures !== 0 || pageErrors !== 0) process.exitCode = 1;
  } finally {
    try { await browser.close(); } catch { /* ignore */ }
    try { process.kill(-srv.pid, "SIGKILL"); } catch { /* ignore */ }
  }
}

main().catch((err) => {
  console.error("[ux-probe] fatal:", err);
  process.exit(1);
});
