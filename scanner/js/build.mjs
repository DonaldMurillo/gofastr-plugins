// Build script for the scanner in-frame decode bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - scan.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                 bundling @zxing/library + the controller. Pure JavaScript decode:
//                 no wasm, no workers, no network — @zxing/browser is deliberately
//                 NOT pulled in (it drags DOM helpers the cage cannot use; the
//                 frame never touches getUserMedia, the host owns the camera).
//   - scan.css  — the token-only frame stylesheet (copied from frame/scan.css;
//                 zxing ships no CSS, so there is nothing to prepend)
//   - scan.html — the opaque-origin frame document (copied from frame/scan.html)
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
  js: "/* GoFastr barcode scanner — protocol v1, schemaVersion scanner-v1. Built IIFE (bundles @zxing/library); do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the controller + zxing to a single minified IIFE.
  //    No code splitting: a dynamic import() is a CORS-mode module fetch an
  //    opaque origin can never satisfy, so the bundle is monolithic by
  //    construction (same constraint as the datagrid/pdf/logstream bundles).
  //    No workers either — the framed CSP has no worker-src escape, same as
  //    pdf's fake-worker path. zxing decodes ~300x300 in ~10-30 ms on the
  //    main thread, which the one-frame-in-flight ack makes a non-problem.
  const result = await build({
    entryPoints: [path.join(srcDir, "scan.ts")],
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
  await writeFile(path.join(assetsDir, "scan.js"), jsOut.text, "utf8");

  // 2. Copy the frame document and stylesheet.
  const html = await readFile(path.join(frameDir, "scan.html"), "utf8");
  await writeFile(path.join(assetsDir, "scan.html"), html, "utf8");
  const css = await readFile(path.join(frameDir, "scan.css"), "utf8");
  await writeFile(path.join(assetsDir, "scan.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] scan.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] scan.css ${Buffer.byteLength(css, "utf8")} B (frame rules only)`);
  console.log(`[built] scan.html ${Buffer.byteLength(html, "utf8")} B`);
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
