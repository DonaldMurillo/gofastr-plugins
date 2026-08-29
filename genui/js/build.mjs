// Build script for the genui in-frame renderer bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - genui.js   — single self-contained IIFE (esbuild --bundle --format=iife
//                  --minify) bundling react + react-dom + the registry. Statically
//                  imported by construction: there is no dynamic import, no
//                  React.lazy, no runtime loading — the fixed registry compiled
//                  into this file is the entire component vocabulary.
//   - genui.css  — the token-only frame stylesheet (copied from frame/genui.css;
//                  react ships no CSS, so there is nothing to prepend)
//   - genui.html — the opaque-origin frame document (copied from frame/genui.html)
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
  js: "/* GoFastr genui — protocol v1, schemaVersion genui-v1. Built IIFE (bundles react + react-dom); do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the renderer + react + react-dom to a single minified IIFE.
  //    No code splitting: a dynamic import() is a CORS-mode module fetch an
  //    opaque origin can never satisfy, so the bundle is monolithic by
  //    construction (same constraint as the datagrid/pdf/logstream/scanner
  //    bundles) — and for genui that monolith IS the containment story: the
  //    registry cannot grow at runtime because nothing can be loaded.
  //    NODE_ENV is DEFINED to "production": react's entry points branch on
  //    process.env.NODE_ENV, `process` does not exist in a browser, and an
  //    undefined read would crash the cage at boot. Production also keeps
  //    React's development-only console logging out of a frame whose e2e
  //    asserts zero console errors.
  const result = await build({
    entryPoints: [path.join(srcDir, "genui.tsx")],
    bundle: true,
    format: "iife",
    target: ["es2022"],
    platform: "browser",
    jsx: "automatic",
    minify: true,
    write: false,
    legalComments: "none",
    banner,
    sourcemap: false,
    logLevel: "info",
    define: { "process.env.NODE_ENV": '"production"' },
  });

  const jsOut = result.outputFiles[0];
  if (!jsOut) throw new Error("esbuild produced no output");
  await writeFile(path.join(assetsDir, "genui.js"), jsOut.text, "utf8");

  // 2. Copy the frame document and stylesheet.
  const html = await readFile(path.join(frameDir, "genui.html"), "utf8");
  await writeFile(path.join(assetsDir, "genui.html"), html, "utf8");
  const css = await readFile(path.join(frameDir, "genui.css"), "utf8");
  await writeFile(path.join(assetsDir, "genui.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] genui.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] genui.css ${Buffer.byteLength(css, "utf8")} B (frame rules only)`);
  console.log(`[built] genui.html ${Buffer.byteLength(html, "utf8")} B`);
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
