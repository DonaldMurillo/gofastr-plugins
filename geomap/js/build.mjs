// Build script for the Geomap in-frame Leaflet-map bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - map.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                that bundles Leaflet + the controller
//   - map.css  — token-only map stylesheet (copied from frame/map.css); Leaflet's
//                own CSS is INLINED into map.js by the cssInjectPlugin below
//   - map.html — the opaque-origin frame document (copied from frame/map.html)
//
// Deterministic: `npm ci && npm run build` reproduces the three files byte-for-byte
// for a fixed dep set (the IIFE is minified and stable; html/css are copied as-is).
//
// Marker icons: Leaflet's default icon path 404s under a bundler and is blocked
// at runtime under the opaque origin (img-src <origin> data:). The controller
// uses an inline-SVG L.divIcon, so NO marker image assets are emitted or fetched.
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
const frameDir = path.join(here, "frame");
const assetsDir = path.join(here, "..", "assets");

const banner = {
  js: "/* GoFastr Leaflet map — protocol v1, schemaVersion map-v1. Built IIFE (bundles leaflet); do not edit by hand. */",
};

// esbuild plugin: Leaflet's ESM build imports leaflet.css. Bundling into a single
// IIFE needs a loader; we inline the CSS as a JS module that injects a <style>
// tag into the frame's <head> at load. Leaflet's controls/scale/attribution and
// tile/marker/panning surface need this styling to render correctly.
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

  // 1. Bundle map.ts + leaflet to a single minified IIFE.
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

  // 2. Copy the frame document + stylesheet.
  const html = await readFile(path.join(frameDir, "map.html"), "utf8");
  await writeFile(path.join(assetsDir, "map.html"), html, "utf8");

  const css = await readFile(path.join(frameDir, "map.css"), "utf8");
  await writeFile(path.join(assetsDir, "map.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(`\n[built] map.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`);
  console.log(`[built] map.css ${Buffer.byteLength(css, "utf8")} B`);
  console.log(`[built] map.html ${Buffer.byteLength(html, "utf8")} B`);
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
