// Frame state — the single object host-side tests read (mirrored onto the
// iframe element by the adapter, so the parent can read it without crossing the
// opaque boundary). Mutated in place by the viewer as it progresses.
//
// CONTRACTS PRESERVED (a Go test + the webkit probe read these):
//   ready, rendered, error, text (FIRST rendered page), pageCount, nonBlank,
//   nonWhitePixels, pdfjsVersion, probes, caps.
// ADDED (welcome — extra e2e handles): currentPage, zoom, rotation,
//   matchCount, matchIndex, mode, annotationCount, dirty, undoDepth,
//   lastExportBytes (length only), lastExportError.

import { version as pdfjsVersion } from "pdfjs-dist";

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
  dirty: boolean;
  undoDepth: number;
  lastExportBytes: number;   // LENGTH ONLY — never the bytes (they are confidential)
  lastExportError: string | null;
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
  dirty: false,
  undoDepth: 0,
  lastExportBytes: 0,
  lastExportError: null,
};

window.__pdfState = state;

