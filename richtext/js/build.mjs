// Build script for the Rich Text in-frame editor bundle.
//
// Emits THREE committed artifacts into ../assets/ (the Go plugin go:embed's them):
//   - editor.js   — single self-contained IIFE (esbuild --bundle --format=iife --minify)
//   - editor.css  — token-only editor stylesheet (copied from frame/editor.css)
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
  js: "/* GoFastr Rich Text editor — protocol v1, schemaVersion richtext-v1. Built IIFE; do not edit by hand. */",
};

async function main() {
  await mkdir(assetsDir, { recursive: true });

  // 1. Bundle the editor to a single minified IIFE.
  const result = await build({
    entryPoints: [path.join(srcDir, "frame.ts")],
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
  await writeFile(path.join(assetsDir, "editor.js"), jsOut.text, "utf8");

  // 2/3. INLINE the stylesheet into the frame document. Serving editor.css as a
  // separate <link> subresource made it subject to the frame's CSP style-src —
  // which strict browsers (Safari) can refuse for the opaque-origin frame,
  // leaving the editor UNSTYLED. Inlining it as a <style> block (permitted by the
  // frame CSP's style-src 'unsafe-inline') removes that subresource entirely, so
  // the editor is always styled. editor.css is still written for reference.
  const css = await readFile(path.join(frameDir, "editor.css"), "utf8");
  let html = await readFile(path.join(frameDir, "editor.html"), "utf8");
  const styleBlock = `<style>\n${css}\n</style>`;
  const linkRe = /<link\b[^>]*href=["']\.\/editor\.css[^>]*>/i;
  if (linkRe.test(html)) {
    html = html.replace(linkRe, styleBlock);
  } else {
    // No <link> found — inject the <style> just before </head> as a fallback.
    html = html.replace(/<\/head>/i, styleBlock + "\n</head>");
  }
  await writeFile(path.join(assetsDir, "editor.html"), html, "utf8");
  await writeFile(path.join(assetsDir, "editor.css"), css, "utf8");

  // 4. Trusted in-page bundle (assets/editor-inline.js): same editor, exposes
  // window.__gofastrRichText.mountTrusted instead of auto-booting in a frame.
  const inlineResult = await build({
    entryPoints: [path.join(srcDir, "inline.ts")],
    bundle: true,
    format: "iife",
    target: ["es2020"],
    platform: "browser",
    minify: true,
    write: false,
    legalComments: "none",
    banner: {
      js: "/* GoFastr Rich Text editor — TRUSTED in-page mount (opt-out of the sandbox; DECISIONS.md). Built IIFE; do not edit by hand. */",
    },
    sourcemap: false,
    logLevel: "info",
  });
  const inlineOut = inlineResult.outputFiles[0];
  if (!inlineOut) throw new Error("esbuild produced no inline output");
  await writeFile(path.join(assetsDir, "editor-inline.js"), inlineOut.text, "utf8");

  // 5. Scoped stylesheet (assets/editor-scoped.css) for the trusted mount:
  // the frame stylesheet styles a whole document (html/body/:root); in-page it
  // must not bleed into the host, so every selector is rescoped under
  // .gofastr-richtext-trusted (html/body/:root map to the wrapper itself).
  const scopedCSS = scopeCSS(css, ".gofastr-richtext-trusted");
  await writeFile(path.join(assetsDir, "editor-scoped.css"), scopedCSS, "utf8");

  // Report sizes for the build log.
  const raw = Buffer.byteLength(jsOut.text, "utf8");
  const gzip = await gzipSize(jsOut.text);
  const hash = createHash("sha256").update(jsOut.text).digest("hex").slice(0, 12);
  console.log(
    `\n[built] editor.js  ${raw} B raw  ${gzip} B gzip  (iife, sha256:${hash})`
  );
  console.log(`[built] editor.css ${Buffer.byteLength(css, "utf8")} B`);
  console.log(`[built] editor.html ${Buffer.byteLength(html, "utf8")} B`);
  console.log(`[built] editor-inline.js ${Buffer.byteLength(inlineOut.text, "utf8")} B`);
  console.log(`[built] editor-scoped.css ${Buffer.byteLength(scopedCSS, "utf8")} B`);
}

// Rescope a document-level stylesheet under `scope`:
//   html / body / :root  →  the scope selector itself
//   anything else        →  descendant of the scope selector
// Handles one level of @media nesting (all editor.css uses). Comments pass
// through untouched inside rule bodies; top-level comments are preserved.
const KEEP_RICHTEXT_TOKENS = "__KEEP_RICHTEXT_TOKENS__";

function scopeCSS(css, scope) {
  const out = [];
  let i = 0;
  const n = css.length;
  let buf = "";
  let depth = 0;
  while (i < n) {
    const ch = css[i];
    if (ch === "/" && css[i + 1] === "*") {
      const end = css.indexOf("*/", i + 2);
      const comment = css.slice(i, end === -1 ? n : end + 2);
      if (depth === 0 && buf.trim() === "") out.push(comment);
      else buf += comment;
      i += comment.length;
      continue;
    }
    if (ch === "{") {
      if (depth === 0) {
        const sel = buf.trim();
        buf = "";
        if (sel.startsWith("@media")) {
          out.push(sel + " {");
          depth = 1; // selectors inside get rescoped as they close at depth 1
        } else {
          out.push(scopeSelector(sel, scope) + " {");
          depth = 1;
        }
        // For @media we need to keep transforming inner selectors — handled by
        // tracking depth: inner rule selectors accumulate in buf until their "{".
        if (sel.startsWith("@media")) depth = 0.5; // marker: inside @media, at selector level
      } else {
        // inner rule inside @media: buf holds its selector
        out.push("  " + scopeSelector(buf.trim(), scope) + " {");
        buf = "";
        depth = 1.5;
      }
      i += 1;
      continue;
    }
    if (ch === "}") {
      if (depth === 1) {
        out.push(buf.replace(/\s+$/, ""));
        out.push("}");
        buf = "";
        depth = 0;
      } else if (depth === 1.5) {
        out.push(buf.replace(/\s+$/, ""));
        out.push("  }");
        buf = "";
        depth = 0.5;
      } else if (depth === 0.5) {
        out.push("}");
        depth = 0;
      }
      i += 1;
      continue;
    }
    if (depth === 0 || depth === 0.5) {
      buf += ch; // accumulating a selector (or whitespace between rules)
    } else {
      buf += ch; // accumulating a declaration body
    }
    i += 1;
  }
  // Filter the fallback-token block: keep only plugin-local --richtext-*
  // declarations, rescoped under `scope`; drop framework-token fallbacks.
  const lines = out.join("\n").split("\n");
  const kept = [];
  let filtering = false;
  for (const line of lines) {
    if (!filtering && line.includes(KEEP_RICHTEXT_TOKENS)) {
      filtering = true;
      kept.push(scope + " {");
      continue;
    }
    if (filtering) {
      if (line.trim() === "}") {
        filtering = false;
        kept.push("}");
      } else if (line.trim().startsWith("--richtext-")) {
        kept.push(line);
      }
      continue;
    }
    kept.push(line);
  }
  return kept.join("\n") + "\n";
}

function scopeSelector(selectorList, scope) {
  return selectorList
    .split(",")
    .map((raw) => {
      const sel = raw.trim();
      if (!sel) return sel;
      // html/body/:root (with optional attribute/pseudo suffix) become the scope
      // wrapper itself; e.g. `body` → scope, `:root[data-x]` → scope[data-x].
      // Bare :root is the frame's fallback-token block. In-page the FRAMEWORK
      // tokens (--color-*, --spacing-*, …) must NOT be rescoped: they would
      // become direct custom properties on the wrapper and beat the host's
      // inherited :root tokens (element-level custom props win over inherited
      // ones) — freezing the trusted editor in the default light palette. The
      // host page brings those, and every rule has an inline var(…, fallback).
      // The PLUGIN-LOCAL --richtext-* slot tokens are different: no host
      // defines them, so they must survive rescoping or color marks silently
      // lose their color (caught by the dogfood shots). scopeCSS filters the
      // block down to --richtext-* declarations instead of dropping it.
      if (sel === ":root") return KEEP_RICHTEXT_TOKENS;
      const m = sel.match(/^(html|body|:root)(.*)$/);
      if (m) return scope + (m[2] || "");
      return scope + " " + sel;
    })
    .join(",\n");
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
