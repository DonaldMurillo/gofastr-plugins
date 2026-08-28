// Build script for the imageedit in-frame editor bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - editor.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                   that bundles the editor controller + the render/font twins
//   - editor.css  — token-only frame stylesheet (copied from frame/editor.css)
//   - editor.html — the opaque-origin frame document (copied from frame/editor.html)
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
  js: "/* GoFastr image editor — protocol v1, schemaVersion imageedit-v1. Built IIFE; do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the controller to a single minified IIFE. No dependencies —
  //    the whole point of this plugin is that crop/rotate/annotate/redact are
  //    a few hundred lines of canvas work, not a vendor editor library — so
  //    the bundle is pure first-party code. No code splitting regardless: a
  //    dynamic import() is a CORS-mode module fetch an opaque origin can
  //    never satisfy (same constraint as the pdf viewer).
  const result = await build({
    entryPoints: [path.join(srcDir, "editor.ts")],
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
  await writeFile(path.join(assetsDir, "editor.js"), jsOut.text, "utf8");

  // 2. Copy the frame document.
  const html = await readFile(path.join(frameDir, "editor.html"), "utf8");
  await writeFile(path.join(assetsDir, "editor.html"), html, "utf8");

  // 3. Copy the token-only stylesheet.
  const css = await readFile(path.join(frameDir, "editor.css"), "utf8");
  await writeFile(path.join(assetsDir, "editor.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] editor.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] editor.css ${Buffer.byteLength(css, "utf8")} B`);
  console.log(`[built] editor.html ${Buffer.byteLength(html, "utf8")} B`);
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
