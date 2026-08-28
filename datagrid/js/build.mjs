// Build script for the datagrid in-frame grid bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - grid.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                 that bundles ag-grid-community + the controller
//   - grid.css  — token-only frame stylesheet (copied from frame/grid.css)
//   - grid.html — the opaque-origin frame document (copied from frame/grid.html)
//
// Deterministic: `npm ci && npm run build` reproduces the three files byte-for-byte
// for a fixed dep set (the IIFE is minified and stable; html/css are copied as-is).
//
// Run from this directory: `npm run build`.

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
  js: "/* GoFastr data grid — protocol v1, schemaVersion datagrid-v1. Built IIFE (bundles AG Grid Community); do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the controller + ag-grid-community to a single minified IIFE.
  //    No code splitting: a dynamic import() is a CORS-mode module fetch an
  //    opaque origin can never satisfy, so the bundle is monolithic by
  //    construction (same constraint as the pdf viewer).
  const result = await build({
    entryPoints: [path.join(srcDir, "grid.ts")],
    bundle: true,
    format: "iife",
    target: ["es2022"],
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
  await writeFile(path.join(assetsDir, "grid.js"), jsOut.text, "utf8");

  // 2. Copy the frame document.
  const html = await readFile(path.join(frameDir, "grid.html"), "utf8");
  await writeFile(path.join(assetsDir, "grid.html"), html, "utf8");

  // 3. Copy the token-only stylesheet.
  const css = await readFile(path.join(frameDir, "grid.css"), "utf8");
  await writeFile(path.join(assetsDir, "grid.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] grid.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] grid.css ${Buffer.byteLength(css, "utf8")} B`);
  console.log(`[built] grid.html ${Buffer.byteLength(html, "utf8")} B`);
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
