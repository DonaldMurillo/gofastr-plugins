// Build script for the PDF spike in-frame viewer bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - viewer.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//                   bundling pdfjs-dist's main thread API + the WORKER module, and
//                   wiring the worker's WorkerMessageHandler onto globalThis.pdfjsWorker
//                   so pdf.js takes its FAKE-WORKER path with ZERO Worker spawn / fetch.
//                   This is the load-bearing trick: under the framed CSP
//                   (connect-src 'none'; no blob: worker-src) a real Worker is
//                   impossible, so the worker code MUST run on the main thread.
//   - viewer.css  — token-only viewer stylesheet (copied from frame/viewer.css)
//   - viewer.html — the opaque-origin frame document (copied from frame/viewer.html)
//
// Deterministic: `npm ci && npm run build` reproduces the three files byte-for-byte
// for a fixed dep set (the IIFE is minified and stable; html/css are copied as-is).
//
// Why NOT the monaco blob-worker trick: the framed CSP has no blob: source in any
// directive (img-src/style-src/font-src default to <origin>), and worker-src falls
// back to default-src (<origin>), so a blob: worker is CSP-blocked. The only way to
// run the worker code is to import it as a normal module on the main thread and hand
// its exports to pdf.js via globalThis.pdfjsWorker.WorkerMessageHandler.
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

// Absolute paths to pdf.js's PURE-JS (no-WebAssembly) image codec fallbacks.
const wasmDir = path.join(
  path.dirname(nodeRequire.resolve("pdfjs-dist/package.json")),
  "wasm"
);

// esbuild plugin: make pdf.js's no-WebAssembly image codecs reachable.
//
// WHY THIS EXISTS — the scanned-document bug. pdf.js decodes JPEG 2000 and
// JBIG2 (the codecs real-world SCANS use, which are exactly the documents
// people redact) through WebAssembly. Under the framed CSP, wasm cannot
// instantiate at all: `script-src <origin>` without 'wasm-unsafe-eval' makes
// WebAssembly.instantiate throw. pdf.js does ship pure-JS fallbacks for both
// codecs, but it reaches them with a DYNAMIC import():
//
//     const mod = await import(`${WasmImage.#wasmUrl}${this._noWasmFilename}`);
//
// A dynamic import is a CORS-mode module fetch, which an opaque origin can
// never satisfy — and `connect-src 'none'` forbids it besides. So the fallback
// is as unreachable as the wasm it replaces, and a JPX page renders as a
// BLANK WHITE PAGE with no error, no console message, and no CSP violation.
// That silent failure is the dangerous part: a user would "redact" a blank
// page and believe it worked.
//
// The fix: rewrite that one dynamic import into a STATIC dispatcher over the
// two fallback modules, which esbuild then inlines into the same IIFE. Nothing
// is fetched and no wasm is instantiated. Combined with `useWasm: false` at the
// getDocument() call site, JPX/JBIG2 decode entirely on the main thread.
//
// The replacement is ASSERTED below: if a pdfjs-dist upgrade reshapes that
// expression the build FAILS loudly, rather than silently reverting to blank
// scanned pages.
function pdfjsNoWasmFallbackPlugin() {
  const dynamicImportRe =
    /await import\(\s*(?:\/\*[^*]*\*\/\s*)*`\$\{WasmImage\.#wasmUrl\}\$\{this\._noWasmFilename\}`\s*\)/;

  return {
    name: "pdfjs-no-wasm-fallback",
    setup(b) {
      b.onLoad({ filter: /pdfjs-dist[/\\]build[/\\]pdf\.worker\.mjs$/ }, (args) => {
        const source = readFileSync(args.path, "utf8");
        if (!dynamicImportRe.test(source)) {
          throw new Error(
            "pdfjs-no-wasm-fallback: could not find the no-wasm dynamic import in " +
              "pdf.worker.mjs. pdfjs-dist changed shape — re-check WasmImage#getJsModule " +
              "before shipping, or scanned (JPX/JBIG2) pages will silently render blank."
          );
        }
        const prelude =
          `import __gofastrOpenJPEG from ${JSON.stringify(path.join(wasmDir, "openjpeg_nowasm_fallback.js"))};\n` +
          `import __gofastrJBIG2 from ${JSON.stringify(path.join(wasmDir, "jbig2_nowasm_fallback.js"))};\n` +
          `const __gofastrNoWasmModule = (name) => ({\n` +
          `  default: String(name).includes("openjpeg") ? __gofastrOpenJPEG : __gofastrJBIG2,\n` +
          `});\n`;
        const patched = source.replace(
          dynamicImportRe,
          "__gofastrNoWasmModule(this._noWasmFilename)"
        );
        return { contents: prelude + patched, loader: "js", resolveDir: path.dirname(args.path) };
      });
    },
  };
}

const banner = {
  js: "/* GoFastr PDF viewer (SPIKE) — protocol v1, schemaVersion pdf-v1. Built IIFE (bundles pdfjs-dist main + worker; worker-free via globalThis.pdfjsWorker); do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the viewer + pdfjs-dist main + pdfjs-dist worker to a single minified
  //    IIFE. The entry (src/viewer.ts) statically imports WorkerMessageHandler from
  //    the worker module and assigns globalThis.pdfjsWorker; esbuild inlines the
  //    worker code into the same IIFE, so at runtime pdf.js's
  //    PDFWorker.#mainThreadWorkerMessageHandler returns it and the fake-worker path
  //    runs entirely on the main thread — no Worker() constructor, no fetch, no blob.
  const result = await build({
    entryPoints: [path.join(srcDir, "viewer.ts")],
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
    plugins: [pdfjsNoWasmFallbackPlugin()],
    // The emscripten-generated no-wasm fallbacks read `import.meta.url` to locate
    // themselves. There is nothing to locate — they are inlined and fetch nothing —
    // so pin it to an inert value; an IIFE has no import.meta otherwise.
    define: { "import.meta.url": JSON.stringify("about:blank") },
    // pdfjs-dist references a few Node-only branches via `isNodeJS` guards; platform
    // browser keeps those dead and strips the Node fetch path.
  });

  const jsOut = result.outputFiles[0];
  if (!jsOut) throw new Error("esbuild produced no output");
  await writeFile(path.join(assetsDir, "viewer.js"), jsOut.text, "utf8");

  // 2. Copy the frame document + token-only stylesheet.
  const html = await readFile(path.join(frameDir, "viewer.html"), "utf8");
  await writeFile(path.join(assetsDir, "viewer.html"), html, "utf8");
  const css = await readFile(path.join(frameDir, "viewer.css"), "utf8");
  await writeFile(path.join(assetsDir, "viewer.css"), css, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] viewer.js  ${(raw / 1024).toFixed(1)} KB raw  ${(gzip / 1024).toFixed(1)} KB gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] viewer.css ${Buffer.byteLength(css, "utf8")} B`);
  console.log(`[built] viewer.html ${Buffer.byteLength(html, "utf8")} B`);
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
