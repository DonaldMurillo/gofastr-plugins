// Generates the SPIKE sample PDF committed at ../assets/sample.pdf.
//
// Requirements (from the spike brief): real SELECTABLE text including the exact
// string SPIKE_SECRET_ALPHA, at least 2 pages, and one embedded RASTER image.
// The raster is synthesized locally (no network): a small RGB PNG built from a
// hand-rolled minimal encoder (signature + IHDR + zlib IDAT + IEND), so the
// whole artifact is reproducible from this script with zero runtime deps beyond
// pdf-lib + Node's zlib.
//
// Run: `npm run gen-sample` (writes ../assets/sample.pdf).

import { PDFDocument, StandardFonts, rgb } from "pdf-lib";
import { writeFile, mkdir } from "node:fs/promises";
import { deflateSync } from "node:zlib";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const assetsDir = path.join(here, "..", "..", "assets");

// --- minimal RGB PNG encoder (8-bit, color type 2) -------------------------

function crc32(buf) {
  let c = ~0;
  for (let i = 0; i < buf.length; i++) {
    c ^= buf[i];
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
  }
  return (~c) >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const typeBuf = Buffer.from(type, "ascii");
  const crcBuf = Buffer.alloc(4);
  crcBuf.writeUInt32BE(crc32(Buffer.concat([typeBuf, data])), 0);
  return Buffer.concat([len, typeBuf, data, crcBuf]);
}

// Build a W×H RGB PNG with a two-color diagonal gradient + a hard border, so the
// rendered page has plenty of non-white inked pixels for the canvas sample gate.
function makeGradientPng(w, h) {
  const sig = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8;   // bit depth
  ihdr[9] = 2;   // color type RGB
  ihdr[10] = 0;  // compression
  ihdr[11] = 0;  // filter
  ihdr[12] = 0;  // interlace

  const raw = Buffer.alloc((w * 3 + 1) * h);
  let o = 0;
  for (let y = 0; y < h; y++) {
    raw[o++] = 0; // filter byte (None) for this scanline
    for (let x = 0; x < w; x++) {
      const t = (x + y) / (w + h);
      const border = x < 4 || y < 4 || x > w - 5 || y > h - 5;
      raw[o++] = border ? 20 : Math.round(40 + 180 * t);          // R
      raw[o++] = border ? 30 : Math.round(80 * (1 - t) + 40);     // G
      raw[o++] = border ? 90 : Math.round(200 * (1 - t) + 30);    // B
    }
  }
  const idat = deflateSync(raw);
  return Buffer.concat([sig, chunk("IHDR", ihdr), chunk("IDAT", idat), chunk("IEND", Buffer.alloc(0))]);
}

async function main() {
  await mkdir(assetsDir, { recursive: true });

  const pdf = await PDFDocument.create();
  pdf.setProducer("gofastr-pdf-spike");
  pdf.setCreator("pdf/js/scripts/gen-sample-pdf.mjs (pdf-lib)");
  pdf.setTitle("GoFastr PDF spike sample");

  const bold = await pdf.embedFont(StandardFonts.HelveticaBold);
  const regular = await pdf.embedFont(StandardFonts.Helvetica);

  // 96×96 RGB gradient raster, embedded as PNG (image XObject).
  const pngBytes = makeGradientPng(96, 96);
  const png = await pdf.embedPng(pngBytes);

  const W = 595, H = 842; // A4 portrait in PDF points
  const BLACK = rgb(0.05, 0.05, 0.05);
  const ACCENT = rgb(0.145, 0.39, 0.92);

  // --- Page 1 -------------------------------------------------------------
  const p1 = pdf.addPage([W, H]);
  p1.drawRectangle({ x: 0, y: H - 90, width: W, height: 90, color: rgb(0.96, 0.97, 1) });
  // The selectable secret the e2e asserts is present in the text layer.
  p1.drawText("SPIKE_SECRET_ALPHA", { x: 56, y: H - 140, size: 28, font: bold, color: ACCENT });
  p1.drawText("GoFastr PDF viewer — opaque-origin sandboxed iframe spike", {
    x: 56, y: H - 60, size: 16, font: bold, color: BLACK,
  });
  p1.drawText(
    "This page is rendered by pdf.js running worker-free on the main thread,\n" +
    "inside a sandbox=\"allow-scripts\" frame with connect-src 'none'. The PDF\n" +
    "bytes arrive over the postMessage bridge; the frame fetches nothing.",
    { x: 56, y: H - 210, size: 12, font: regular, color: BLACK, lineHeight: 16 },
  );
  // Embedded raster image (one of the brief's requirements).
  p1.drawImage(png, { x: 56, y: H - 360, width: 192, height: 192 });
  p1.drawText("embedded raster (synthesized RGB PNG)", {
    x: 264, y: H - 270, size: 11, font: regular, color: rgb(0.4, 0.4, 0.45),
  });

  // --- Page 2 -------------------------------------------------------------
  const p2 = pdf.addPage([W, H]);
  p2.drawText("Page 2 — more selectable text", { x: 56, y: H - 90, size: 18, font: bold, color: BLACK });
  p2.drawText(
    "The viewer renders page 1 for the spike, but the document has multiple\n" +
    "pages so pageCount > 1 is exercised. Standard Helvetica is used throughout\n" +
    "so useSystemFonts:true renders without fetching standardFontDataUrl.",
    { x: 56, y: H - 140, size: 12, font: regular, color: BLACK, lineHeight: 16 },
  );
  p2.drawImage(png, { x: 200, y: 320, width: 160, height: 160 });

  const bytes = await pdf.save();
  await writeFile(path.join(assetsDir, "sample.pdf"), bytes);

  const sha = createHash("sha256").update(bytes).digest("hex").slice(0, 12);
  console.log(`[gen] sample.pdf  ${bytes.length} B  (pdf-lib, ${pdf.getPageCount()} pages, sha256:${sha})`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
