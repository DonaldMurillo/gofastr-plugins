// Overlay document model (schema pdf-v1) + command-pattern undo/redo.
//
// The canonical doc is a small JSON overlay; the PDF bytes are external. All
// geometry is PDF user space (see coords.ts). This module owns the in-memory
// shape, mutates it ONLY through Commands (each carrying its own inverse), and
// serializes to the exact JSON the Go `Overlay` struct in pdf/overlay.go reads.
//
// Why commands, not deep-clone snapshots: the brief forbids snapshot-based
// undo. Each command captures the minimal before-state of the entity it
// touched (one annotation, one form field, one page op) and restores exactly
// that on undo — so undo depth is bounded by history length, not by N×docsize.
//
// The model is DOM-free and bridge-free: it emits a change signal, and the
// viewer decides what to do with it (debounced docChanged, re-render, etc.).

import type { PdfRect, PdfQuad } from "./coords";

// --- annotation types ------------------------------------------------------

export type AnnId = string;

export interface AnnotationBase {
  readonly id: AnnId;
  page: number;     // 1-indexed page the annotation lives on
  type: AnnotationType;
  rect: PdfRect;    // [x, y, w, h] bounding box, PDF user space (matches Go Rect)
}

export type AnnotationType =
  | "highlight"
  | "ink"
  | "rect"
  | "ellipse"
  | "arrow"
  | "text"
  | "note"
  | "stamp";

export interface HighlightAnnotation extends AnnotationBase {
  type: "highlight";
  color: string;        // CSS hex, e.g. "#FFEB3B"
  opacity: number;      // 0..1
  quads: PdfQuad[];     // one per text-selection line (multi-line safe)
}

export interface InkAnnotation extends AnnotationBase {
  type: "ink";
  color: string;
  width: number;        // stroke width, PDF points
  points: [number, number][]; // PDF user space, in stroke order
}

export interface ShapeAnnotation extends AnnotationBase {
  type: "rect" | "ellipse" | "arrow";
  color: string;        // stroke
  fill: string | null;  // fill colour or null for hollow
  width: number;        // stroke width, PDF points
  opacity: number;      // 0..1 (fill+stroke)
}

export interface TextAnnotation extends AnnotationBase {
  type: "text";
  color: string;
  text: string;
  fontSize: number;     // PDF points (rendered with a standard-14 font at export)
}

export interface NoteAnnotation extends AnnotationBase {
  type: "note";
  color: string;        // anchor colour
  text: string;         // popup body
}

export interface StampAnnotation extends AnnotationBase {
  type: "stamp";
  mime: string;         // e.g. "image/png"
  data: string;         // data: URL (capped at ingest time, never here)
  kind: "image" | "drawn" | "text"; // provenance for the export path
  label: string;        // alt text / text-signature content
}

export type Annotation =
  | HighlightAnnotation
  | InkAnnotation
  | ShapeAnnotation
  | TextAnnotation
  | NoteAnnotation
  | StampAnnotation;

// --- page operations -------------------------------------------------------

export type PageOpKind = "rotate" | "delete" | "move" | "insert" | "append";

export interface PageOp {
  op: PageOpKind;
  page: number;   // 1-indexed target/anchor
  value: number;  // rotation deg / source page / count, per op
}

// --- the overlay -----------------------------------------------------------

export interface FormValue {
  // json.RawMessage on the Go side — opaque JSON. We keep the parsed value and
  // re-serialize verbatim, so a bool stays a bool and a string stays a string.
  v: unknown;
}

export interface OverlayState {
  schemaVersion: string;
  src: OverlaySource;
  annotations: Annotation[];
  formFields: Map<string, FormValue>;
  pageOps: PageOp[];
  redactions: Redaction[];   // P3 — left empty by this build, modelled for parity
  rev: number;
}

export interface OverlaySource {
  kind: "id" | "url";
  ref: string;
  sha256: string;
  pages: number;
}

export interface Redaction {
  id: string;
  page: number;
  rect: PdfRect;
  reason: string;
}

// --- commands --------------------------------------------------------------

// A Command mutates the overlay forward and knows how to reverse itself. Each
// implementation captures the minimal before-state at apply() time (the one
// entity it touched), so undo is O(1) in doc size.
export interface Command {
  readonly kind: string;
  apply(ov: OverlayState): void;
  undo(ov: OverlayState): void;
  describe(): string;   // human label, read into the aria-live announcer
}

export class AddAnnotationCommand implements Command {
  readonly kind = "addAnnotation";
  private insertAt = -1;
  constructor(private readonly ann: Annotation) {}
  apply(ov: OverlayState): void {
    ov.annotations.push(this.ann);
    this.insertAt = ov.annotations.length - 1;
  }
  undo(ov: OverlayState): void {
    const i = ov.annotations.lastIndexOf(this.ann);
    if (i >= 0) ov.annotations.splice(i, 1);
    else if (this.insertAt >= 0) ov.annotations.splice(this.insertAt, 1);
  }
  describe(): string { return "Add " + this.ann.type; }
}

export class RemoveAnnotationCommand implements Command {
  readonly kind = "removeAnnotation";
  private removed: Annotation | null = null;
  private index = -1;
  constructor(private readonly id: AnnId) {}
  apply(ov: OverlayState): void {
    const i = ov.annotations.findIndex((a) => a.id === this.id);
    if (i < 0) return;
    this.removed = ov.annotations[i];
    this.index = i;
    ov.annotations.splice(i, 1);
  }
  undo(ov: OverlayState): void {
    if (this.removed) ov.annotations.splice(this.index, 0, this.removed);
  }
  describe(): string { return "Delete annotation"; }
}

// UpdateAnnotation mutates a single annotation via a caller-supplied mutator.
// The mutator receives a shallow clone of the annotation's pre-mutation state
// for capture; it then applies the desired change in place on the live object.
export class UpdateAnnotationCommand implements Command {
  readonly kind = "updateAnnotation";
  private before: Annotation | null = null;
  private idx = -1;
  constructor(
    private readonly id: AnnId,
    private readonly applyFn: (a: Annotation) => void,
  ) {}
  apply(ov: OverlayState): void {
    const i = ov.annotations.findIndex((a) => a.id === this.id);
    if (i < 0) return;
    this.idx = i;
    this.before = cloneAnnotation(ov.annotations[i]);
    this.applyFn(ov.annotations[i]);
  }
  undo(ov: OverlayState): void {
    if (this.before && this.idx >= 0) ov.annotations[this.idx] = this.before;
  }
  describe(): string { return "Edit annotation"; }
}

export class SetFormFieldCommand implements Command {
  readonly kind = "setFormField";
  private hadBefore = false;
  private before: FormValue | null = null;
  constructor(
    private readonly name: string,
    private readonly value: unknown,
  ) {}
  apply(ov: OverlayState): void {
    const prev = ov.formFields.get(this.name);
    if (prev) { this.hadBefore = true; this.before = { v: prev.v }; }
    else { this.hadBefore = false; this.before = null; }
    ov.formFields.set(this.name, { v: this.value });
  }
  undo(ov: OverlayState): void {
    if (this.hadBefore && this.before) ov.formFields.set(this.name, this.before);
    else ov.formFields.delete(this.name);
  }
  describe(): string { return "Fill form field"; }
}

export class AddPageOpCommand implements Command {
  readonly kind = "addPageOp";
  constructor(private readonly op: PageOp) {}
  apply(ov: OverlayState): void { ov.pageOps.push({ ...this.op }); }
  undo(ov: OverlayState): void { ov.pageOps.pop(); }
  describe(): string { return this.op.op + " page"; }
}
// --- redaction commands (P3) ----------------------------------------------
// Redactions live in ov.redactions (PDF user space, same convention as
// annotations). They are authored here and consumed by the rasterize+verify
// pipeline at export; they never become PDF annotation objects.

export class AddRedactionCommand implements Command {
  readonly kind = "addRedaction";
  private insertAt = -1;
  constructor(private readonly red: Redaction) {}
  apply(ov: OverlayState): void {
    ov.redactions.push(this.red);
    this.insertAt = ov.redactions.length - 1;
  }
  undo(ov: OverlayState): void {
    const i = ov.redactions.lastIndexOf(this.red);
    if (i >= 0) ov.redactions.splice(i, 1);
    else if (this.insertAt >= 0) ov.redactions.splice(this.insertAt, 1);
  }
  describe(): string { return "Add redaction"; }
}

export class RemoveRedactionCommand implements Command {
  readonly kind = "removeRedaction";
  private removed: Redaction | null = null;
  private index = -1;
  constructor(private readonly id: string) {}
  apply(ov: OverlayState): void {
    const i = ov.redactions.findIndex((r) => r.id === this.id);
    if (i < 0) return;
    this.removed = ov.redactions[i];
    this.index = i;
    ov.redactions.splice(i, 1);
  }
  undo(ov: OverlayState): void {
    if (this.removed) ov.redactions.splice(this.index, 0, this.removed);
  }
  describe(): string { return "Delete redaction"; }
}

// UpdateRedaction mutates one redaction (rect / reason) via a mutator; the
// mutator receives a shallow clone of the pre-mutation state for capture.
export class UpdateRedactionCommand implements Command {
  readonly kind = "updateRedaction";
  private before: Redaction | null = null;
  private idx = -1;
  constructor(
    private readonly id: string,
    private readonly applyFn: (r: Redaction) => void,
  ) {}
  apply(ov: OverlayState): void {
    const i = ov.redactions.findIndex((r) => r.id === this.id);
    if (i < 0) return;
    this.idx = i;
    this.before = { ...ov.redactions[i], rect: ov.redactions[i].rect.slice() as PdfRect };
    this.applyFn(ov.redactions[i]);
  }
  undo(ov: OverlayState): void {
    if (this.before && this.idx >= 0) ov.redactions[this.idx] = this.before;
  }
  describe(): string { return "Edit redaction"; }
}

export class ClearRedactionsCommand implements Command {
  readonly kind = "clearRedactions";
  private prev: Redaction[] = [];
  apply(ov: OverlayState): void { this.prev = ov.redactions.slice(); ov.redactions.length = 0; }
  undo(ov: OverlayState): void { ov.redactions = this.prev.slice(); }
  describe(): string { return "Clear redactions"; }
}

// A batch groups dependent edits (e.g. "split annotation" or "paste N stamps")
// into one undo unit. Sub-commands are applied in order and undone in reverse.
export class BatchCommand implements Command {
  readonly kind = "batch";
  constructor(
    private readonly subs: Command[],
    private readonly label: string,
  ) {}
  apply(ov: OverlayState): void { for (const c of this.subs) c.apply(ov); }
  undo(ov: OverlayState): void {
    for (let i = this.subs.length - 1; i >= 0; i--) this.subs[i].undo(ov);
  }
  describe(): string { return this.label; }
}

function cloneAnnotation(a: Annotation): Annotation {
  // Shallow-clone the base + slice the array-valued extras so a captured
  // before-state cannot be aliased by a later in-place mutation. Primitives
  // (strings/numbers) are immutable; only the arrays (rect, quads, points)
  // need copying here. `rect` is a tuple of numbers — slice it.
  const out = { ...a } as Annotation & Record<string, unknown>;
  out.rect = a.rect.slice() as PdfRect;
  if (a.type === "highlight") out.quads = a.quads.slice();
  else if (a.type === "ink") out.points = a.points.map((p) => [p[0], p[1]] as [number, number]);
  return out as Annotation;
}

// --- the document ----------------------------------------------------------

export type DocListener = (snapshot: { dirty: boolean; rev: number; annotationCount: number; redactionCount: number; undoDepth: number }) => void;

export class OverlayDoc {
  readonly state: OverlayState;
  private undoStack: Command[] = [];
  private redoStack: Command[] = [];
  private listeners = new Set<DocListener>();
  private dirty = false;

  constructor(initial?: Partial<OverlayState>) {
    this.state = {
      schemaVersion: "pdf-v1",
      src: initial?.src ?? { kind: "id", ref: "", sha256: "", pages: 0 },
      annotations: initial?.annotations ?? [],
      formFields: initial?.formFields ?? new Map(),
      pageOps: initial?.pageOps ?? [],
      redactions: initial?.redactions ?? [],
      rev: initial?.rev ?? 0,
    };
  }

  /** Apply a command forward, clear the redo stack, and notify. */
  apply(cmd: Command): void {
    cmd.apply(this.state);
    this.undoStack.push(cmd);
    this.redoStack.length = 0;
    this.dirty = true;
    this.emit();
  }

  canUndo(): boolean { return this.undoStack.length > 0; }
  canRedo(): boolean { return this.redoStack.length > 0; }
  undoDepth(): number { return this.undoStack.length; }

  undo(): boolean {
    const cmd = this.undoStack.pop();
    if (!cmd) return false;
    cmd.undo(this.state);
    this.redoStack.push(cmd);
    this.emit();
    return true;
  }

  redo(): boolean {
    const cmd = this.redoStack.pop();
    if (!cmd) return false;
    cmd.apply(this.state);
    this.undoStack.push(cmd);
    this.emit();
    return true;
  }

  isDirty(): boolean { return this.dirty; }

  /** Mark clean (after a successful save). */
  markClean(): void {
    if (!this.dirty) return;
    this.dirty = false;
    this.emit();
  }

  getAnnotation(id: AnnId): Annotation | null {
    return this.state.annotations.find((a) => a.id === id) ?? null;
  }

  annotationsForPage(page1: number): Annotation[] {
    return this.state.annotations.filter((a) => a.page === page1);
  }
  redactionsForPage(page1: number): Redaction[] {
    return this.state.redactions.filter((r) => r.page === page1);
  }

  getRedaction(id: string): Redaction | null {
    return this.state.redactions.find((r) => r.id === id) ?? null;
  }

  /** Bind the overlay to its source PDF (called once after the bytes load). */
  setStateSrc(src: { sha256: string; pageCount: number; ref?: string; kind?: "id" | "url" }): void {
    this.state.src = {
      kind: src.kind ?? "id",
      ref: src.ref ?? this.state.src.ref,
      sha256: src.sha256,
      pages: src.pageCount,
    };
  }

  subscribe(cb: DocListener): () => void {
    this.listeners.add(cb);
    cb(this.snapshot());
    return () => { this.listeners.delete(cb); };
  }

  private snapshot() {
    return {
      dirty: this.dirty,
      rev: this.state.rev,
      annotationCount: this.state.annotations.length,
      redactionCount: this.state.redactions.length,
      undoDepth: this.undoStack.length,
    };
  }

  private emit(): void {
    const snap = this.snapshot();
    for (const cb of this.listeners) cb(snap);
  }

  /** Bump rev (called by the viewer after a successful save ack). */
  bumpRev(): void { this.state.rev++; }
}

// --- serialization (the wire shape Go reads) -------------------------------
//
// Output matches pdf/overlay.go exactly:
//   { schemaVersion, src, annotations[], formFields{}, redactions[],
//     pageOps[], rev }
// Each annotation carries id/page/type/rect (typed on the Go side) PLUS its
// type-specific extras inline (which survive verbatim in DocJSON). `rect` is
// [x,y,w,h]. `formFields` values are opaque JSON (string/bool/number).

export interface SerializedOverlay {
  schemaVersion: string;
  src: OverlaySource;
  annotations: Array<Record<string, unknown>>;
  formFields: Record<string, unknown>;
  redactions: Redaction[];
  pageOps: PageOp[];
  rev: number;
}

export function serializeAnnotation(a: Annotation): Record<string, unknown> {
  const base: Record<string, unknown> = {
    id: a.id,
    page: a.page,
    type: a.type,
    rect: [a.rect[0], a.rect[1], a.rect[2], a.rect[3]],
  };
  switch (a.type) {
    case "highlight":
      base.color = a.color;
      base.opacity = a.opacity;
      base.quads = a.quads.map(quadToObject);
      break;
    case "ink":
      base.color = a.color;
      base.width = a.width;
      base.points = a.points.map((p) => [p[0], p[1]]);
      break;
    case "rect":
    case "ellipse":
    case "arrow":
      base.color = a.color;
      base.fill = a.fill;
      base.width = a.width;
      base.opacity = a.opacity;
      break;
    case "text":
      base.color = a.color;
      base.text = a.text;
      base.fontSize = a.fontSize;
      break;
    case "note":
      base.color = a.color;
      base.text = a.text;
      break;
    case "stamp":
      base.mime = a.mime;
      base.data = a.data;
      base.kind = a.kind;
      base.label = a.label;
      break;
  }
  return base;
}

export function serializeOverlay(ov: OverlayState): SerializedOverlay {
  const formFields: Record<string, unknown> = {};
  for (const [k, v] of ov.formFields) formFields[k] = v.v;
  return {
    schemaVersion: ov.schemaVersion,
    src: { ...ov.src },
    annotations: ov.annotations.map(serializeAnnotation),
    formFields,
    redactions: ov.redactions.map((r) => ({ ...r, rect: r.rect.slice() as PdfRect })),
    pageOps: ov.pageOps.map((op) => ({ ...op })),
    rev: ov.rev,
  };
}

function quadToObject(q: PdfQuad): Record<string, number> {
  return { x1: q.x1, y1: q.y1, x2: q.x2, y2: q.y2, x3: q.x3, y3: q.y3, x4: q.x4, y4: q.y4 };
}

// --- deserialization (tolerant: init.doc round-trip) ----------------------
//
// The host may round-trip a previously-saved overlay through init.doc. We parse
// it defensively: unknown annotation types are kept (their typed fields are
// zero-valued, extras survive), and malformed entries are skipped rather than
// failing the whole load — matching the Go side's "DocJSON is authoritative"
// posture.

export function deserializeOverlay(raw: unknown): OverlayState {
  const ov = new OverlayDoc().state;
  if (!raw || typeof raw !== "object") return ov;
  const o = raw as Record<string, unknown>;
  if (typeof o.schemaVersion === "string") ov.schemaVersion = o.schemaVersion;
  if (o.src && typeof o.src === "object") {
    const s = o.src as Record<string, unknown>;
    ov.src = {
      kind: s.kind === "url" ? "url" : "id",
      ref: typeof s.ref === "string" ? s.ref : "",
      sha256: typeof s.sha256 === "string" ? s.sha256 : "",
      pages: typeof s.pages === "number" ? s.pages : 0,
    };
  }
  if (Array.isArray(o.annotations)) {
    for (const ar of o.annotations) {
      const a = parseAnnotation(ar);
      if (a) ov.annotations.push(a);
    }
  }
  if (o.formFields && typeof o.formFields === "object") {
    const ff = o.formFields as Record<string, unknown>;
    for (const k of Object.keys(ff)) ov.formFields.set(k, { v: ff[k] });
  }
  if (Array.isArray(o.pageOps)) {
    for (const op of o.pageOps) {
      const p = parsePageOp(op);
      if (p) ov.pageOps.push(p);
    }
  }
  if (Array.isArray(o.redactions)) {
    for (const r of o.redactions) {
      const rr = parseRedaction(r);
      if (rr) ov.redactions.push(rr);
    }
  }
  if (typeof o.rev === "number") ov.rev = o.rev;
  return ov;
}

function asNum(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

function asRect(v: unknown): PdfRect | null {
  if (!Array.isArray(v) || v.length < 4) return null;
  const r: number[] = [];
  for (let i = 0; i < 4; i++) {
    const n = asNum(v[i]);
    if (n === undefined) return null;
    r.push(n);
  }
  return [r[0], r[1], r[2], r[3]];
}

function asStr(v: unknown, def = ""): string {
  return typeof v === "string" ? v : def;
}

function parseAnnotation(raw: unknown): Annotation | null {
  if (!raw || typeof raw !== "object") return null;
  const o = raw as Record<string, unknown>;
  const id = asStr(o.id, "");
  const page = Math.max(1, Math.floor(asNum(o.page) ?? 1));
  const type = asStr(o.type, "") as AnnotationType;
  const rect = asRect(o.rect) ?? ([0, 0, 0, 0] as PdfRect);
  const color = asStr(o.color, "#000000");
  switch (type) {
    case "highlight": {
      const quads = Array.isArray(o.quads) ? o.quads.map(parseQuad).filter(Boolean) as PdfQuad[] : [];
      return { id: id || cryptoId(), page, type, rect, color, opacity: asNum(o.opacity) ?? 0.35, quads };
    }
    case "ink": {
      const points = Array.isArray(o.points) ? (o.points as unknown[]).map(parsePoint).filter(Boolean) as [number, number][] : [];
      return { id: id || cryptoId(), page, type, rect, color, width: asNum(o.width) ?? 2, points };
    }
    case "rect":
    case "ellipse":
    case "arrow":
      return {
        id: id || cryptoId(), page, type, rect, color,
        fill: o.fill == null ? null : asStr(o.fill),
        width: asNum(o.width) ?? 2,
        opacity: asNum(o.opacity) ?? 1,
      };
    case "text":
      return { id: id || cryptoId(), page, type, rect, color, text: asStr(o.text), fontSize: asNum(o.fontSize) ?? 14 };
    case "note":
      return { id: id || cryptoId(), page, type, rect, color, text: asStr(o.text) };
    case "stamp":
      return {
        id: id || cryptoId(), page, type, rect,
        mime: asStr(o.mime, "image/png"),
        data: asStr(o.data),
        kind: (o.kind === "image" || o.kind === "drawn" || o.kind === "text") ? o.kind : "image",
        label: asStr(o.label),
      };
    default:
      // Unknown type — keep the typed base fields so it round-trips; the
      // overlay layer renders it as a placeholder box. The Go side already
      // tolerates unknown types via DocJSON.
      return null;
  }
}

function parseQuad(v: unknown): PdfQuad | null {
  if (!v || typeof v !== "object") return null;
  const q = v as Record<string, unknown>;
  const x1 = asNum(q.x1), y1 = asNum(q.y1), x2 = asNum(q.x2), y2 = asNum(q.y2);
  const x3 = asNum(q.x3), y3 = asNum(q.y3), x4 = asNum(q.x4), y4 = asNum(q.y4);
  if (x1 === undefined || y1 === undefined || x2 === undefined || y2 === undefined ||
      x3 === undefined || y3 === undefined || x4 === undefined || y4 === undefined) return null;
  return { x1, y1, x2, y2, x3, y3, x4, y4 };
}

function parsePoint(v: unknown): [number, number] | null {
  if (!Array.isArray(v) || v.length < 2) return null;
  const x = asNum(v[0]), y = asNum(v[1]);
  if (x === undefined || y === undefined) return null;
  return [x, y];
}

function parsePageOp(v: unknown): PageOp | null {
  if (!v || typeof v !== "object") return null;
  const o = v as Record<string, unknown>;
  const op = asStr(o.op, "") as PageOpKind;
  if (op !== "rotate" && op !== "delete" && op !== "move" && op !== "insert" && op !== "append") return null;
  return { op, page: Math.max(1, Math.floor(asNum(o.page) ?? 1)), value: Math.floor(asNum(o.value) ?? 0) };
}

function parseRedaction(v: unknown): Redaction | null {
  if (!v || typeof v !== "object") return null;
  const o = v as Record<string, unknown>;
  const rect = asRect(o.rect);
  if (!rect) return null;
  return {
    id: asStr(o.id, cryptoId()),
    page: Math.max(1, Math.floor(asNum(o.page) ?? 1)),
    rect,
    reason: asStr(o.reason),
  };
}

// --- id generation ---------------------------------------------------------
//
// crypto.randomUUID is available in the sandboxed frame (secure-context not
// required for sandboxed opaque frames in modern engines; the fallback below
// covers the rare engine where it is missing). IDs are client-assigned and
// stable for an annotation's lifetime.
let idCounter = 0;
export function cryptoId(): string {
  const g = globalThis as { crypto?: { randomUUID?: () => string } };
  if (g.crypto && typeof g.crypto.randomUUID === "function") {
    try { return "a-" + g.crypto.randomUUID(); } catch { /* fall through */ }
  }
  idCounter++;
  return "a-" + Date.now().toString(36) + "-" + idCounter.toString(36);
}
