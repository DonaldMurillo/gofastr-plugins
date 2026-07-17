// Build script for the Mermaid in-frame diagram editor bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - diagram.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                    that bundles the `mermaid` npm package + the controller
//   - diagram.css  — token-only editor stylesheet (copied from frame/diagram.css)
//   - diagram.html — the opaque-origin frame document (copied from frame/diagram.html)
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
  js: "/* GoFastr Mermaid diagram editor — protocol v1, schemaVersion mermaid-v1. Built IIFE (bundles mermaid); do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the editor + mermaid to a single minified IIFE.
  const result = await build({
    entryPoints: [path.join(srcDir, "diagram.ts")],
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
    // Mermaid pulls in some optional Node-flavored code paths; keep the browser
    // conditions. esbuild defaults to browser for platform:"browser".
  });

  const jsOut = result.outputFiles[0];
  if (!jsOut) throw new Error("esbuild produced no output");
  await writeFile(path.join(assetsDir, "diagram.js"), jsOut.text, "utf8");

  // 2. Copy the frame document.
  const html = await readFile(path.join(frameDir, "diagram.html"), "utf8");
  await writeFile(path.join(assetsDir, "diagram.html"), html, "utf8");

  // 3. Copy the token-only stylesheet.
  const css = await readFile(path.join(frameDir, "diagram.css"), "utf8");
  await writeFile(path.join(assetsDir, "diagram.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] diagram.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] diagram.css ${Buffer.byteLength(css, "utf8")} B`);
  console.log(`[built] diagram.html ${Buffer.byteLength(html, "utf8")} B`);
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
