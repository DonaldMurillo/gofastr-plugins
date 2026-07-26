// Ambient declaration for the pdf.js worker entry. The published pdfjs-dist
// ships no .d.ts for build/pdf.worker.mjs (its `types` field points only at the
// root pdf.d.ts). This ambient module gives the single export we depend on a
// structural type. Kept as a SCRIPT file (no top-level import/export) so the
// ambient declaration is authoritative and not shadowed by the real .mjs on
// disk during type resolution.
declare module "pdfjs-dist/build/pdf.worker.mjs" {
  export const WorkerMessageHandler: {
    setup: (handler: unknown, port: unknown) => void;
  };
}
