// Build script for the Monaco in-frame code-editor bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - editor.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                   that bundles the monaco-editor API + the default language
//                   tokenizers + the controller
//   - editor.css  — token-only editor stylesheet (copied from frame/editor.css)
//   - editor.html — the opaque-origin frame document (copied from frame/editor.html)
//
// Deterministic: `npm ci && npm run build` reproduces the three files byte-for-byte
// for a fixed dep set (the IIFE is minified and stable; html/css are copied as-is).
//
// Worker handling: Monaco spins up web workers for language services, which an
// opaque-origin sandbox restricts. The bundle inlines editor.worker.js as a
// STRING (via the monacoWorkerSourcePlugin below) so that, WHEN the host opts
// into workers (config.workers === true), the frame can build a blob: worker
// from the inlined source. The default (worker-free) path never uses it.
//
// Run from this directory: `npm run build`.

import { build } from "esbuild";
import { copyFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { createHash } from "node:crypto";

const here = path.dirname(fileURLToPath(import.meta.url));
const srcDir = path.join(here, "src");
const frameDir = path.join(here, "frame");
const assetsDir = path.join(here, "..", "assets");
const nodeRequire = createRequire(import.meta.url);

const banner = {
  js: "/* GoFastr Monaco code editor — protocol v1, schemaVersion monaco-v1. Built IIFE (bundles monaco-editor); do not edit by hand. */",
};

// esbuild plugin: inline the Monaco editor.worker.js source as a default-exported
// STRING. Without this, importing the worker entry would bundle its top-level
// code into the main thread (wrong) instead of giving us the source to build a
// blob: worker from.
function monacoWorkerSourcePlugin() {
  return {
    name: "monaco-worker-source",
    setup(b) {
      const filter = /monaco-editor\/esm\/vs\/editor\/editor\.worker\.js$/;
      b.onResolve({ filter }, (args) => ({
        path: args.path,
        namespace: "monaco-worker-src",
      }));
      b.onLoad({ filter: /.*/, namespace: "monaco-worker-src" }, (args) => {
        // Resolve the real file within node_modules from this script's location.
        const resolved = nodeRequire.resolve(args.path);
        const source = readFileSync(resolved, "utf8");
        return {
          contents: `export default ${JSON.stringify(source)};`,
          loader: "js",
        };
      });
    },
  };
}
// esbuild plugin: Monaco's ESM modules import their component `.css` files
// (actionbar.css, button.css, …). Bundling them into a single IIFE needs a
// loader; we inline each CSS file as a JS module that injects a <style> tag
// into the frame's <head> at load. Monaco's editor surface (gutter, cursor,
// scrollbars, widgets) needs this styling to render correctly.
function monacoCssInjectPlugin() {
  return {
    name: "monaco-css-inject",
    setup(b) {
      b.onLoad({ filter: /\.css$/ }, (args) => {
        const css = readFileSync(args.path, "utf8");
        const body = `var __css=${JSON.stringify(css)};var __s=document.createElement("style");__s.textContent=__css;document.head.appendChild(__s);`;
        return { contents: body, loader: "js" };
      });
    },
  };
}

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the editor + monaco-editor to a single minified IIFE.
  const result = await build({
    entryPoints: [path.join(srcDir, "editor.ts")],
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
    plugins: [monacoWorkerSourcePlugin(), monacoCssInjectPlugin()],
  });

  const jsOut = result.outputFiles[0];
  if (!jsOut) throw new Error("esbuild produced no output");
  await writeFile(path.join(assetsDir, "editor.js"), jsOut.text, "utf8");

  // 2. Copy the frame document.
  const html = await readFile(path.join(frameDir, "editor.html"), "utf8");
  await writeFile(path.join(assetsDir, "editor.html"), html, "utf8");

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
