// Build script for the sqlnotebook in-frame bundle.
//
// Emits TWO committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - notebook.js — single self-contained IIFE (esbuild --bundle --format=iife
//     --minify) of src/notebook.ts. It bundles NO sql.js: the emscripten glue
//     (sql-wasm.js) is vendored in assets/ and loaded by the frame document as
//     its own <script src>, and the .wasm bytes arrive over the postMessage
//     bridge (sqlnb/init). Nothing here fetches anything at runtime.
//   - frame.html  — the opaque-origin frame document, copied from
//     frame/frame.html with frame/notebook.css INLINED as a <style> block
//     (the source keeps a relative <link> so it renders standalone under
//     js/frame/). Inlining keeps the framed asset set at exactly three files
//     — frame.html, notebook.js, sql-wasm.js — each needing its own
//     AssetSpec.ContentType on the Go side, where an empty content type is a
//     silent 200-with-unparseable-bytes (gofastr#303).
//
// Deterministic: `npm ci && npm run build` reproduces both files byte-for-byte
// for a fixed dep set (the IIFE is minified and stable; html/css are copied).
//
// Run from this directory: `npm run build`.

import { build } from "esbuild";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { createHash } from "node:crypto";

const here = path.dirname(fileURLToPath(import.meta.url));
const srcDir = path.join(here, "src");
const frameDir = path.join(here, "frame");
const assetsDir = path.join(here, "..", "assets");

const banner = {
  js: "/* GoFastr SQL notebook — sqlnotebook protocol v1, schemaVersion sqlnotebook.v1. Built IIFE (no sql.js inside: the glue loads via <script src>, the wasm arrives over the bridge); do not edit by hand. */",
};

// The link line the CSS inliner replaces. Kept as the exact literal emitted
// by frame/frame.html so a refactor of that document fails the build LOUDLY
// instead of silently shipping a frame with an unfilled <link>.
const CSS_LINK = '  <link rel="stylesheet" href="./notebook.css" />';

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the controller to a single minified IIFE. No code splitting
  //    and no dynamic imports: a module fetch is a CORS-mode request an
  //    opaque origin can never satisfy, and connect-src 'none' forbids it
  //    besides (same constraint as every bundle here).
  const result = await build({
    entryPoints: [path.join(srcDir, "notebook.ts")],
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
  await writeFile(path.join(assetsDir, "notebook.js"), jsOut.text, "utf8");

  // 2. Copy the frame document, inlining the stylesheet. The committed
  //    artifact carries its CSS in a <style> block where the source has the
  //    <link>, so the framed document needs no separate .css asset route.
  const html = await readFile(path.join(frameDir, "frame.html"), "utf8");
  const css = await readFile(path.join(frameDir, "notebook.css"), "utf8");
  if (!html.includes(CSS_LINK)) {
    throw new Error(
      `frame/frame.html no longer contains the stylesheet link line (${CSS_LINK.trim()}); update CSS_LINK in build.mjs`
    );
  }
  const framed = html.replace(CSS_LINK, "  <style>\n" + css + "  </style>");
  await writeFile(path.join(assetsDir, "frame.html"), framed, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] notebook.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(
    `[built] frame.html   ${Buffer.byteLength(framed, "utf8")} B (css inlined: ${Buffer.byteLength(css, "utf8")} B)`
  );
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
