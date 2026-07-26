// Redaction verifier — proves content under a redaction rect is actually gone
// before any bytes leave the frame. Ported from the independently-reproduced
// reference at scratchpad/pdf-redact-spike/lib/{verify-redaction.mjs,pdfgrep.js}.
//
// The one substitution that matters: node's `zlib.inflateSync` becomes the
// browser's `DecompressionStream("deflate")` (the frame is forbidden workers,
// wasm, fetch, blob: and connect-src 'none'; DecompressionStream is a main-
// thread runtime API, not gated by CSP). That makes searchPdf async.
//
// Six checks (the spec in docs/pdf.md §"Verification"):
//   1. byteSearch    — needles in DECOMPRESSED streams (inflate + decode
//                      hex/literal/UTF-16BE tokens first; a raw `indexOf`
//                      misses UTF-16BE hex in the Info dict entirely).
//   2. textExtract   — pdf.js getTextContent per page; needle must not be
//                      extractable on a redacted page.
//   3. rectIntersect — no text-item bbox may hit a redaction rect (catches
//                      "covered but present" — a drawn rectangle over text).
//   4. metadata      — metaNeedles absent from any stream + no XMP packet.
//   5. annotations   — no annotation on a redacted page carries a needle.
//   6. incremental   — a single %%EOF and no /Prev (no prior revision hiding).
//
// Absence is asserted PER RECT, not globally: the same string may legitimately
// appear elsewhere un-redacted. A byteSearch hit on a content stream that
// textExtract confirms lives on a NON-redacted page is a WARNING, not a
// failure; a hit in a structural stream (ObjStm/XRef/image/raw) always fails.
// See docs/DECISIONS.md (2026-07-26) for the two traps this catches.

import { getDocument } from "pdfjs-dist";
import type { PDFDocumentProxy, PDFDocumentLoadingTask } from "pdfjs-dist";
import type { PdfRect } from "../coords";
import { castTextContent, isTextItem, type PdfTextItem } from "../textlayer";

// --- public types ---------------------------------------------------------

/** One check's verdict + a bounded evidence sample (report rides a header). */
export interface VerifyCheck {
  name: "byteSearch" | "textExtract" | "rectIntersect" | "metadata" | "annotations" | "incremental";
  ok: boolean;
  /** Short human detail. Occurrence lists are CAPPED so the header stays small. */
  detail: string;
}

/** A redaction rect the verifier asserts against, in PDF user space. */
export interface VerifyRect {
  page: number;     // 1-based
  rect: PdfRect;    // [x, y, w, h], bottom-left origin
}

export interface VerifyInput {
  /** Strings captured under each redaction rect (ground truth). Asserted absent. */
  needles: string[];
  /** Strings that must never appear anywhere (e.g. a token planted in metadata). */
  metaNeedles?: string[];
  /** The redaction rects; rectIntersect + annotations scope to these pages. */
  redactions: VerifyRect[];
}

/** The structured report the frame ships with every redact export. Bounded. */
export interface VerifyReport {
  ok: boolean;
  size: number;
  checks: VerifyCheck[];
  /** Pages rasterized (content replaced by an image). */
  rasterizedPages: number[];
  /** Other occurrences of a needle that survive on non-redacted pages. */
  warnings: string[];
}

/** Host-readable summary mirrored onto __pdfLastVerifyReport. */
export type VerifyReportSummary = Pick<VerifyReport, "ok" | "size" | "checks" | "rasterizedPages"> & {
  warnings: number;
};

export interface VerifyOptions {
  /** Cap on evidence items per check (keeps the header-borne report small). */
  sampleCap?: number;
}

// --- latin1 helper (Uint8Array → JS string, byte-wise) -------------------
// PDF streams are byte-oriented; a latin1 (ISO-8859-1) decode is the lossless
// 1-byte-per-char string we can run regexes and indexOf against. Built once.

function toLatin1(bytes: Uint8Array): string {
  // String.fromCharCode on a large array can blow the argument limit; chunk it.
  const CHUNK = 0x8000;
  let out = "";
  for (let i = 0; i < bytes.length; i += CHUNK) {
    out += String.fromCharCode.apply(null, bytes.subarray(i, i + CHUNK) as unknown as number[]);
  }
  return out;
}

// --- string-token decode (the load-bearing part) -------------------------
// pdf-lib writes text as hex strings (<546F6B…>) and packs the Info dict into
// a compressed object stream as UTF-16BE hex. A naive substring scan over the
// inflated bytes misses UTF-16BE needles entirely (there is a 0x00 before every
// ASCII byte). We decode every PDF string token and strip 0x0000 (norm) before
// searching, which is what makes the Info-dict leak visible.

interface DecodedToken { kind: string; text: string; }

const HEX_RE = /<([0-9A-Fa-f]{2,})>/g;
// Bounded + newline-excluding: PDF literal strings do not span lines, and an
// unbounded regex catastrophically backtracks on embedded JPEG binary data
// (measured in the spike). Cap 2000 chars, no raw \n/\r.
const LIT_RE = /\(((?:\\.|[^\\()\n\r]){0,2000})\)/g;

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length >> 1);
  for (let i = 0, j = 0; i + 1 < hex.length; i += 2, j++) {
    out[j] = parseInt(hex.slice(i, i + 2), 16);
  }
  return out;
}

// UTF-16BE decode (no TextDecoder('utf-16be') in some engines; do it by hand).
function utf16be(bytes: Uint8Array): string {
  let out = "";
  // Chunk via codePointAt-style build to avoid huge apply limits.
  const len = bytes.length - (bytes.length & 1);
  for (let i = 0; i < len; i += 2) {
    out += String.fromCharCode((bytes[i] << 8) | bytes[i + 1]);
  }
  return out;
}

function decodeStrings(latin1: string): DecodedToken[] {
  const out: DecodedToken[] = [];
  let m: RegExpExecArray | null;
  HEX_RE.lastIndex = 0;
  while ((m = HEX_RE.exec(latin1))) {
    const bytes = hexToBytes(m[1]);
    if (bytes.length >= 2 && bytes[0] === 0xfe && bytes[1] === 0xff) {
      out.push({ kind: "hex-utf16be", text: utf16be(bytes.subarray(2)) });
    }
    out.push({ kind: "hex-latin1", text: toLatin1(bytes) });
  }
  LIT_RE.lastIndex = 0;
  while ((m = LIT_RE.exec(latin1))) {
    const raw = m[1]
      .replace(/\\(\d{1,3})/g, (_$, o) => String.fromCharCode(parseInt(o, 8) & 0xff))
      .replace(/\\n/g, "\n").replace(/\\r/g, "\r").replace(/\\t/g, "\t")
      .replace(/\\\(/g, "(").replace(/\\\)/g, ")").replace(/\\\\/g, "\\");
    out.push({ kind: "literal", text: raw });
  }
  return out;
}

/** Strip NULs — UTF-16BE of ASCII is `\0R\0E\0D`; after norm it is `RED`. */
function norm(s: string): string { return s.replace(/\u0000/g, ""); }

function ctxOf(s: string, i: number, needle: string): string {
  const a = Math.max(0, i - 24), b = Math.min(s.length, i + needle.length + 24);
  return JSON.stringify(s.slice(a, b));
}

interface Hit { needle: string; source: string; ctx: string; }

function searchChunk(latin1: string, source: string, needles: string[], decode: boolean): Hit[] {
  const hits: Hit[] = [];
  for (const needle of needles) {
    const i = latin1.indexOf(needle);
    if (i >= 0) hits.push({ needle, source: source + ":raw", ctx: ctxOf(latin1, i, needle) });
  }
  if (decode) {
    for (const { kind, text } of decodeStrings(latin1)) {
      const t = norm(text);
      for (const needle of needles) {
        const i = t.indexOf(needle);
        if (i >= 0) hits.push({ needle, source: source + ":" + kind, ctx: ctxOf(t, i, needle) });
      }
    }
  }
  return hits;
}

// --- stream inflation (node zlib → DecompressionStream) ------------------
// `inflateSync`/`inflateRawSync` become an async inflate over a
// DecompressionStream("deflate") / ("deflate-raw") pipe. Returns null when the
// stream isn't valid Flate (e.g. an image's DCTDecode stream) — same as the
// reference's try/catch fallback.

async function inflate(bytes: Uint8Array, format: "deflate" | "deflate-raw"): Promise<Uint8Array | null> {
  // Copy into a fresh ArrayBuffer so the Blob-backed stream is detached cleanly.
  const buf = new Uint8Array(bytes.length);
  buf.set(bytes);
  try {
    const ds = new DecompressionStream(format);
    const writer = ds.writable.getWriter();
    writer.write(buf);
    writer.close();
    const reader = ds.readable.getReader();
    const chunks: Uint8Array[] = [];
    let total = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value);
      total += value.length;
    }
    const out = new Uint8Array(total);
    let off = 0;
    for (const c of chunks) { out.set(c, off); off += c.length; }
    return out;
  } catch {
    return null;
  }
}

interface StreamDesc { idx: number; kind: string; }

interface PdfSearchResult {
  hits: Hit[];
  /** Per-stream attribution: a hit's source `kind#idx` maps to streams[idx-1]. */
  streams: StreamDesc[];
}

const STREAM_RE = /stream\r?\n([\s\S]*?)\r?\nendstream/g;

/** Search the whole file: raw scan + inflate every stream + decode tokens. */
async function searchPdf(buf: Uint8Array, needles: string[]): Promise<PdfSearchResult> {
  const latin = toLatin1(buf);
  const allHits: Hit[] = searchChunk(latin, "file", needles, false);
  const streams: StreamDesc[] = [];

  const jobs: Array<{ idx: number; kind: string; bytes: Uint8Array }> = [];
  let m: RegExpExecArray | null;
  let idx = 0;
  STREAM_RE.lastIndex = 0;
  while ((m = STREAM_RE.exec(latin))) {
    idx++;
    const bytes = new Uint8Array(m[1].length);
    for (let i = 0; i < m[1].length; i++) bytes[i] = m[1].charCodeAt(i) & 0xff;
    const hdrStart = Math.max(0, m.index - 120);
    const hdr = latin.slice(hdrStart, m.index);
    let kind = "content";
    if (/\/Type\s*\/ObjStm/.test(hdr)) kind = "ObjStm";
    else if (/\/Type\s*\/XRef/.test(hdr)) kind = "XRef";
    else if (/\/Image[BIX]/.test(hdr) || /\/Subtype\s*\/Image/.test(hdr)) kind = "image";
    streams.push({ idx, kind });
    jobs.push({ idx, kind, bytes });
  }

  // Inflate in parallel; each is independent.
  const inflated = await Promise.all(jobs.map(async (j) => {
    let out = await inflate(j.bytes, "deflate");
    if (!out) out = await inflate(j.bytes, "deflate-raw");
    return { j, out };
  }));
  for (const { j, out } of inflated) {
    if (!out) continue;
    // Only decode string tokens on text-like streams (skip raw image bytes —
    // an image stream's bytes can spuriously contain a short needle, and the
    // string-token regex is wasted on them).
    const decode = j.kind !== "image";
    const hits = searchChunk(toLatin1(out), j.kind + "#" + j.idx, needles, decode);
    if (hits.length) allHits.push(...hits);
  }
  return { hits: allHits, streams };
}

// --- rect / text helpers --------------------------------------------------

/** Axis-aligned intersection of a text item's loose bbox with a PDF rect. */
function hitsRect(item: PdfTextItem, rect: PdfRect): boolean {
  const t = item.transform;
  const e = t[4] ?? 0, f = t[5] ?? 0;
  const w = item.width || 0, h = item.height || 0;
  const ix1 = e, iy1 = f, ix2 = e + w, iy2 = f + h;
  const [rx, ry, rw, rh] = rect;
  return ix1 < rx + rw && ix2 > rx && iy1 < ry + rh && iy2 > ry;
}

// --- the six checks -------------------------------------------------------

/**
 * Verify `buf` is a properly redacted PDF for `input`. Returns a bounded
 * report; never throws on a malformed file (a garbage file is a hard FAIL with
 * a reason, not a crash that drops the audit record).
 */
export async function verifyRedaction(
  buf: Uint8Array,
  input: VerifyInput,
  opts: VerifyOptions = {},
): Promise<VerifyReport> {
  const sampleCap = opts.sampleCap ?? 8;
  const needles = input.needles;
  const metaNeedles = input.metaNeedles ?? [];
  const redactions = input.redactions;
  const warnings: string[] = [];
  const redactPages = new Set(redactions.map((r) => r.page));

  // latin1 view computed ONCE, before any pdf.js call. getDocument TRANSFERS
  // (detaches) the ArrayBuffer it is handed, so a later toLatin1(buf) on the
  // same buffer would read detached memory and silently miss every check that
  // follows. We also hand pdf.js a COPY (buf.slice()) for the same reason.
  const latin = toLatin1(buf);

  // --- 1. byte search (raw scan + inflate every stream + decode tokens) ---
  const allNeedles = [...needles, ...metaNeedles];
  const pg = await searchPdf(buf, allNeedles);
  const structuralFail: string[] = [];
  const contentHits = new Set<string>();
  for (const h of pg.hits) {
    const streamKind = h.source.split(":")[0].split("#")[0];
    if (streamKind === "ObjStm" || streamKind === "XRef" || streamKind === "image" || streamKind === "file") {
      structuralFail.push(`${h.needle} @ ${h.source}`);
    } else {
      // content stream hit — may be a legitimate other occurrence; resolved
      // against textExtract below.
      contentHits.add(h.needle);
    }
  }

  // --- 2 + 3 + 5. one pdf.js pass: text extract, rect intersect, annotations -
  const textFail: string[] = [];
  const intersectFail: string[] = [];
  const annotFail: string[] = [];
  const needleOnPage = new Map<string, Set<number>>();
  let parseError: string | null = null;
  let doc: PDFDocumentProxy | null = null;
  const loadTask: PDFDocumentLoadingTask = getDocument({ data: buf.slice(), useWasm: false, useSystemFonts: true, verbosity: 0 });
  try {
    doc = await loadTask.promise;
    const redactByPage = new Map<number, PdfRect[]>();
    for (const r of redactions) {
      const arr = redactByPage.get(r.page);
      if (arr) arr.push(r.rect);
      else redactByPage.set(r.page, [r.rect]);
    }
    for (let i = 0; i < doc.numPages; i++) {
      const pageNum = i + 1;
      const page = await doc.getPage(pageNum);
      const tc = castTextContent(await page.getTextContent());
      const items = tc.items.filter(isTextItem);
      const pageText = items.map((x) => x.str).join("");
      for (const n of needles) {
        if (pageText.includes(n)) {
          let s = needleOnPage.get(n);
          if (!s) { s = new Set(); needleOnPage.set(n, s); }
          s.add(pageNum);
          // A needle on a REDACTED page is a real leak. On a non-redacted
          // page it is a legitimate other occurrence (warning, below).
          if (redactPages.has(pageNum)) textFail.push(`p${pageNum}:${n}`);
        }
      }
      const rectsHere = redactByPage.get(pageNum);
      if (rectsHere) {
        for (const rect of rectsHere) {
          const offenders = items.filter((it) => it.str && it.str.trim() && hitsRect(it, rect));
          if (offenders.length) {
            intersectFail.push(`p${pageNum} (${offenders.slice(0, 2).map((o) => JSON.stringify(o.str.slice(0, 24))).join(",")})`);
          }
        }
        // Annotations on a redacted page: a /Contents here is exactly where a
        // second copy of a sensitive string hides (measured leak). Any needle
        // in any annotation fails; the presence of annotations at all is
        // reported for review.
        const annots = await page.getAnnotations();
        for (const a of annots) {
          const blob = JSON.stringify(a);
          for (const n of allNeedles) {
            if (blob.includes(n)) annotFail.push(`p${pageNum}:${n}(${a.subtype ?? "?"})`);
          }
        }
        if (annots.length) annotFail.push(`p${pageNum}:${annots.length} annotation(s) on a redacted page`);
      }
    }
  } catch (e) {
    parseError = e instanceof Error ? e.message : String(e);
  } finally {
    if (doc) { try { await loadTask.destroy(); } catch { /* ignore */ } }
  }

  // Resolve content-stream byteSearch hits against per-page text: a hit whose
  // needle lives ONLY on non-redacted pages is a warning (legitimate other
  // occurrence); anything else is a leak or orphan and fails.
  for (const needle of contentHits) {
    const pages = needleOnPage.get(needle);
    const onlyNonRedacted = pages && pages.size > 0 && [...pages].every((p) => !redactPages.has(p));
    if (onlyNonRedacted) {
      warnings.push(`"${needle}" still appears on ${[...pages].map((p) => "p" + p).join(",")} (not redacted there)`);
    } else {
      structuralFail.push(`${needle} @ contentStream (leak or orphan)`);
    }
  }

  // --- 4. metadata -------------------------------------------------------
  const xmpPresent = /<\?xpacket|<x:xmpmeta|adobe:ns:meta/.test(latin);
  const metaFail = metaNeedles.filter((n) => pg.hits.some((h) => h.needle === n));

  // --- 6. incremental update leftovers -----------------------------------
  const eofCount = (latin.match(/%%EOF/g) || []).length;
  const hasPrev = /\/Prev\s+\d+/.test(latin);

  // Assemble checks in spec order (byteSearch, textExtract, rectIntersect,
  // metadata, annotations, incremental).
  const checks: VerifyCheck[] = [
    {
      name: "byteSearch",
      ok: structuralFail.length === 0,
      detail: structuralFail.length
        ? `needles still in bytes: ${cap(structuralFail, sampleCap)}`
        : `0/${needles.length} needles in any structural/metadata stream`,
    },
    {
      name: "textExtract",
      ok: parseError === null && textFail.length === 0,
      detail: parseError !== null
        ? "pdf.js parse failed: " + parseError.slice(0, 200)
        : textFail.length ? `needles extractable on redacted pages: ${cap(textFail, sampleCap)}` : `no needle extractable on any redacted page`,
    },
    {
      name: "rectIntersect",
      ok: parseError === null && intersectFail.length === 0,
      detail: parseError !== null
        ? "skipped (parse failed)"
        : intersectFail.length ? `text items intersect rects: ${cap(intersectFail, sampleCap)}` : `no text bbox intersects any of ${redactions.length} rect(s)`,
    },
    {
      name: "metadata",
      ok: metaFail.length === 0 && !xmpPresent,
      detail: `metaNeedles-in-bytes=${metaFail.length} xmpPresent=${xmpPresent}`,
    },
    {
      name: "annotations",
      ok: annotFail.length === 0,
      detail: annotFail.length ? cap(annotFail, sampleCap) : "no annotation carries a needle",
    },
    {
      name: "incremental",
      ok: eofCount <= 1 && !hasPrev,
      detail: `%%EOF count=${eofCount} /Prev present=${hasPrev}`,
    },
  ];

  const ok = checks.every((c) => c.ok);
  return {
    ok,
    size: buf.length,
    checks,
    rasterizedPages: [...redactPages].sort((a, b) => a - b),
    warnings,
  };
}

/** Reduce a full report to the host-mirrored summary (bounded, no occurrence lists). */
export function summarizeReport(r: VerifyReport): VerifyReportSummary {
  return {
    ok: r.ok,
    size: r.size,
    checks: r.checks.map((c) => ({ name: c.name, ok: c.ok, detail: c.detail.slice(0, 200) })),
    rasterizedPages: r.rasterizedPages,
    warnings: r.warnings.length,
  };
}

function cap(arr: string[], n: number): string {
  if (arr.length <= n) return JSON.stringify(arr);
  return JSON.stringify(arr.slice(0, n)) + ` …(+${arr.length - n} more)`;
}
