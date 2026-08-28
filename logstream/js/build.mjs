// Build script for the logstream in-frame terminal bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - term.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                 that bundles xterm.js + the search/fit addons + the controller
//   - term.css  — xterm's own stylesheet, then the token-only frame stylesheet
//                 (concatenated: one <link> in the frame document)
//   - term.html — the opaque-origin frame document (copied from frame/term.html)
//
// Deterministic: `npm ci && npm run build` reproduces the three files byte-for-byte
// for a fixed dep set (the IIFE is minified and stable; html is copied as-is).
//
// Run from this directory: `npm run build`.

import { build } from "esbuild";
import { copyFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";
import path from "node:path";
import { createHash } from "node:crypto";

const here = path.dirname(fileURLToPath(import.meta.url));
const srcDir = path.join(here, "src");
const frameDir = path.join(here, "frame");
const assetsDir = path.join(here, "..", "assets");

const banner = {
  js: "/* GoFastr log stream — protocol v1, schemaVersion logstream-v1. Built IIFE (bundles xterm.js); do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the controller + xterm.js to a single minified IIFE.
  //    No code splitting: a dynamic import() is a CORS-mode module fetch an
  //    opaque origin can never satisfy, so the bundle is monolithic by
  //    construction (same constraint as the datagrid/pdf bundles).
  const result = await build({
    entryPoints: [path.join(srcDir, "term.ts")],
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
  await writeFile(path.join(assetsDir, "term.js"), jsOut.text, "utf8");

  // 2. Copy the frame document.
  const html = await readFile(path.join(frameDir, "term.html"), "utf8");
  await writeFile(path.join(assetsDir, "term.html"), html, "utf8");

  // 3. Concatenate xterm's stylesheet with the token-only frame stylesheet.
  //    xterm's DOM renderer needs its css/xterm.css (measures char cells,
  //    draws the viewport); our own rules follow so they win on equal
  //    specificity. One <link>, one framed asset.
  const require = createRequire(import.meta.url);
  const xtermCssPath = require.resolve("@xterm/xterm/css/xterm.css");
  const xtermCss = await readFile(xtermCssPath, "utf8");
  const css = await readFile(path.join(frameDir, "term.css"), "utf8");
  await writeFile(path.join(assetsDir, "term.css"), xtermCss + "\n" + css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] term.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] term.css ${Buffer.byteLength(xtermCss + css, "utf8")} B (xterm.css + frame rules)`);
  console.log(`[built] term.html ${Buffer.byteLength(html, "utf8")} B`);
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
