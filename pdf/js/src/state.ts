// Frame state — the single object host-side tests read (mirrored onto the
// iframe element by the adapter, so the parent can read it without crossing the
// opaque boundary). Mutated in place by the viewer as it progresses.
//
// CONTRACTS PRESERVED (a Go test + the webkit probe read these):
//   ready, rendered, error, text (FIRST rendered page), pageCount, nonBlank,
//   nonWhitePixels, pdfjsVersion, probes, caps.
// ADDED (welcome — extra e2e handles): currentPage, zoom, rotation,
//   matchCount, matchIndex, mode, annotationCount, redactionCount, dirty,
//   undoDepth, lastExportBytes (length only), lastExportError,
//   lastVerifyReport (structured redaction verdicts), redactState.

import { version as pdfjsVersion } from "pdfjs-dist";
import type { VerifyReportSummary } from "./redact/verify";

export const SCHEMA_VERSION = "pdf-v1";
export const VIEWER_VERSION = "1.0.0";

export interface PdfFrameState {
  ready: boolean;
  rendered: boolean;
  error: string | null;
  probes: unknown;
  caps: unknown;
  // Page-1 render stats (the regression contract).
  text: string;
  pageCount: number;
  nonBlank: boolean;
  nonWhitePixels: number;
  widthPx: number;
  heightPx: number;
  pdfjsVersion: string;
  // Live viewer state (added).
  currentPage: number;
  zoom: number | string; // pct (50–400) when custom, else "fit-width" | "fit-page"
  rotation: number;      // user view rotation, 0/90/180/270
  matchCount: number;
  matchIndex: number;    // 1-based; 0 = no active match
  // P2 annotate surface — mirrored for the e2e suite.
  mode: "view" | "annotate" | "redact";
  annotationCount: number;
  redactionCount: number;          // P3 — pending redactions authored
  dirty: boolean;
  undoDepth: number;
  lastExportBytes: number;   // LENGTH ONLY — never the bytes (they are confidential)
  lastExportError: string | null;
  // P3 redaction — the structured verification report from the last redact
  // export (null until a redaction has been applied + verified). The host
  // reads this as the audit record; it is bounded (verdicts + capped sample).
  lastVerifyReport: VerifyReportSummary | null;
  // P3 redaction — coarse surface state for the host: "idle" | "armed" |
  redactState: "idle" | "armed" | "working" | "done" | "error";
  // P3 redact timing — wall-clock and longest single main-thread page block
  // (ms), for the perf report. The pipeline yields between pages so the UI
  // stays responsive; maxBlockMs is bounded by one page's render+encode.
  lastRedactTotalMs: number;
  lastRedactMaxBlockMs: number;
}

declare global {
  interface Window {
    __pdfState?: PdfFrameState;
    // Set at module load so pdf.js takes the main-thread fake-worker path
    // (wired in viewer.ts entry; declared here so all modules share the type).
    pdfjsWorker?: { WorkerMessageHandler: unknown } | undefined;
  }
}

export const state: PdfFrameState = {
  ready: false,
  rendered: false,
  error: null,
  probes: null,
  caps: null,
  text: "",
  pageCount: 0,
  nonBlank: false,
  nonWhitePixels: 0,
  widthPx: 0,
  heightPx: 0,
  pdfjsVersion,
  currentPage: 1,
  zoom: "fit-width",
  rotation: 0,
  matchCount: 0,
  matchIndex: 0,
  mode: "view",
  annotationCount: 0,
  redactionCount: 0,
  dirty: false,
  undoDepth: 0,
  lastExportBytes: 0,
  lastExportError: null,
  lastVerifyReport: null,
  redactState: "idle",
  lastRedactTotalMs: 0,
  lastRedactMaxBlockMs: 0,
};

window.__pdfState = state;

