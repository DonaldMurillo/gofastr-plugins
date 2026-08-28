// Build script for the GoFastr chart in-frame bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's
// them):
//   - chart.js   — single self-contained IIFE (esbuild --bundle --format=iife
//                  --minify) bundling @observablehq/plot + d3-array + the
//                  controller
//   - chart.css  — token-only frame stylesheet (copied from frame/chart.css)
//   - chart.html — the opaque-origin frame document (copied from frame/chart.html)
//
// Deterministic: `npm ci && npm run build` reproduces the three files
// byte-for-byte for a fixed dep set. Run from this directory: `npm run build`.
//
// Adapted from mermaid/js/build.mjs (same platform, same shape).

import { build } from "esbuild";
import { copyFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { createHash } from "node:crypto";

const here = path.dirname(fileURLToPath(import.meta.url));
const srcDir = path.join(here, "src");
const frameDir = path.join(here, "frame");
const assetsDir = path.join(here, "..", "assets");

const banner = {
  js: "/* GoFastr chart frame — protocol v1, schemaVersion chart-v1. Built IIFE (bundles Observable Plot); do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the controller + Observable Plot to a single minified IIFE.
  const result = await build({
    entryPoints: [path.join(srcDir, "chart.ts")],
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
  await writeFile(path.join(assetsDir, "chart.js"), jsOut.text, "utf8");

  // 2. Copy the frame document.
  const html = await readFile(path.join(frameDir, "chart.html"), "utf8");
  await writeFile(path.join(assetsDir, "chart.html"), html, "utf8");

  // 3. Copy the token-only stylesheet.
  const css = await readFile(path.join(frameDir, "chart.css"), "utf8");
  await writeFile(path.join(assetsDir, "chart.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(`\n[built] chart.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`);
  console.log(`[built] chart.css ${Buffer.byteLength(css, "utf8")} B`);
  console.log(`[built] chart.html ${Buffer.byteLength(html, "utf8")} B`);
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
