// Build script for the GoFastr tour runtime.
//
// Emits TWO committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - tour.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                 that attaches window.gofastrTour on the host page
//   - tour.css  — token-only overlay stylesheet (copied from src/tour.css)
//
// Deterministic: `npm ci && npm run build` reproduces the two files byte-for-byte
// for a fixed dep set (the IIFE is minified and stable; css is copied as-is).
//
// Run from this directory: `npm run build`.

import { build } from "esbuild";
import { copyFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { createHash } from "node:crypto";

const here = path.dirname(fileURLToPath(import.meta.url));
const srcDir = path.join(here, "src");
const assetsDir = path.join(here, "..", "assets");

const banner = {
  js: "/* GoFastr tour runtime — trusted host-page plugin. Built IIFE; do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the runtime to a single minified IIFE.
  const result = await build({
    entryPoints: [path.join(srcDir, "tour.ts")],
    bundle: true,
    format: "iife",
    target: ["es2020"],
    platform: "browser",
    minify: true,
    write: false,
    legalComments: "none",
    banner,
    sourcemap: false,
    logLevel: "info",
  });

  const jsOut = result.outputFiles[0];
  if (!jsOut) throw new Error("esbuild produced no output");
  await writeFile(path.join(assetsDir, "tour.js"), jsOut.text, "utf8");

  // 2. Copy the token-only stylesheet verbatim.
  const css = await readFile(path.join(srcDir, "tour.css"), "utf8");
  await writeFile(path.join(assetsDir, "tour.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(`\n[built] tour.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`);
  console.log(`[built] tour.css ${Buffer.byteLength(css, "utf8")} B`);
}

async function gzipSize(text) {
  const { gzip } = await import("node:zlib");
  return new Promise((resolve, reject) => {
    gzip(Buffer.from(text, "utf8"), (err, buf) => {
      if (err) reject(err);
      else resolve(buf.length);
    });
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
