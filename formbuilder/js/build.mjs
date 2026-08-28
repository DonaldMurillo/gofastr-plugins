// Build script for the formbuilder in-frame bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - builder.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                    that bundles the builder controller (NO third-party code:
//                    a drag-to-reorder list and a property panel are ordinary DOM
//                    work, and a vendor form-builder would bring a palette that
//                    fights the design tokens)
//   - builder.css  — token-only frame stylesheet (copied from frame/builder.css)
//   - builder.html — the opaque-origin frame document (copied from frame/builder.html)
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
  js: "/* GoFastr form builder — protocol v1, schemaVersion formbuilder-v1. Built IIFE (no third-party code); do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the controller to a single minified IIFE. No code splitting:
  //    a dynamic import() is a CORS-mode module fetch an opaque origin can
  //    never satisfy, so the bundle is monolithic by construction (same
  //    constraint as every other plugin here).
  const result = await build({
    entryPoints: [path.join(srcDir, "builder.ts")],
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
  await writeFile(path.join(assetsDir, "builder.js"), jsOut.text, "utf8");

  // 2. Copy the frame document.
  const html = await readFile(path.join(frameDir, "builder.html"), "utf8");
  await writeFile(path.join(assetsDir, "builder.html"), html, "utf8");

  // 3. Copy the token-only stylesheet.
  const css = await readFile(path.join(frameDir, "builder.css"), "utf8");
  await writeFile(path.join(assetsDir, "builder.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] builder.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] builder.css ${Buffer.byteLength(css, "utf8")} B`);
  console.log(`[built] builder.html ${Buffer.byteLength(html, "utf8")} B`);
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
