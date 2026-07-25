// Build script for the GoFastr geomap runtime — a TRUSTED host-page MapLibre
// GL + OpenFreeMap vector-tile bundle.
//
// Emits TWO committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - map.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                that scans the host page for [data-fui-geomap] mount elements
//                and renders a MapLibre map into each. MapLibre's own CSS is
//                imported by the bundle and injected as a <style> at load by the
//                cssInjectPlugin below (controls/canvas need it to render).
//   - map.css  — token-only overlay stylesheet (copied verbatim from src/map.css)
//                for the demo page's container sizing + style-switcher control.
//                MapLibre's core CSS is NOT here — it lives inside map.js.
//
// There is NO map.html: this is a trusted host-page plugin, not a sandboxed
// iframe document (see tour/js/build.mjs for the same shape).
//
// Deterministic: `npm ci && npm run build` reproduces both files byte-for-byte
// for a fixed dep set (the IIFE is minified and stable; css is copied as-is).
//
// Run from this directory: `npm run build`.

import { build } from "esbuild";
import { copyFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const srcDir = path.join(here, "src");
const assetsDir = path.join(here, "..", "assets");

const banner = {
  js: "/* GoFastr geomap runtime — trusted host-page MapLibre GL + OpenFreeMap. Built IIFE (bundles maplibre-gl); do not edit by hand. */",
};

// esbuild plugin: maplibre-gl's ESM build imports its CSS. Bundling into a
// single IIFE needs a loader; we inline the CSS as a JS module that injects a
// <style> tag into the document <head> at load. MapLibre's controls, scale,
// attribution, and canvas need this styling to render correctly.
function cssInjectPlugin() {
  return {
    name: "geomap-css-inject",
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

  // 1. Bundle map.ts + maplibre-gl to a single minified IIFE.
  const result = await build({
    entryPoints: [path.join(srcDir, "map.ts")],
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
    plugins: [cssInjectPlugin()],
  });

  const jsOut = result.outputFiles[0];
  if (!jsOut) throw new Error("esbuild produced no output");
  await writeFile(path.join(assetsDir, "map.js"), jsOut.text, "utf8");

  // 2. Copy the overlay stylesheet verbatim (host pages / demo link it; it is
  //    NOT bundled into map.js — MapLibre's core CSS is, this one isn't).
  const css = await readFile(path.join(srcDir, "map.css"), "utf8");
  await writeFile(path.join(assetsDir, "map.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(`\n[built] map.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`);
  console.log(`[built] map.css ${Buffer.byteLength(css, "utf8")} B`);
}

async function gzipSize(text) {
  const { gzip } = await import("node:zlib");
  return new Promise((resolve) => {
    gzip(text, { level: 9 }, (err, buf) => {
      if (err) resolve(0);
      else resolve(Buffer.byteLength(buf));
    });
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
