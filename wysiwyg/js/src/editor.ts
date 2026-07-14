// GoFastr WYSIWYG editor — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY. Never touches host cookies,
// localStorage, or the host DOM. On load it announces `ready`; the host replies
// `init`; we mount ProseMirror from init.doc/markdown, apply bridged tokens,
// respect granted capabilities, and emit the full plugin→host event set.
//
// Full block set (schema wysiwyg-v1): every node/mark in docs/design/schema-v1.md.
// All editing stays IN-FRAME; the only host channel is window.parent.postMessage.
// The Phase-0 protocol plumbing, §8a observability hooks, and the keystroke-
// latency rig (p99 ≤ 16 ms) are preserved — see below.

import { EditorState } from "prosemirror-state";
import type { Transaction } from "prosemirror-state";
import { EditorView } from "prosemirror-view";
import { Node as PMNode } from "prosemirror-model";
import type { ResolvedPos } from "prosemirror-model";
import { schema, SCHEMA_VERSION, PROTOCOL_VERSION } from "./schema.ts";
import { serializeMarkdown, parseMarkdown } from "./markdown.ts";
import { buildPlugins } from "./plugins.ts";
import {
  sendEvent,
  createRouter,
  routeEnvelope,
  setTransport,
  defaultTransport,
} from "./protocol.ts";
import type { Envelope } from "./protocol.ts";
import { applyTokens, applyScheme, sampleAppliedTokens } from "./theme.ts";
import { metrics, startSample } from "./metrics.ts";
import { uiPlugins, setSlashImageHook, setSlashItemFilter, setOverlayChangedHook, setOverlayParent, dismissAllOverlays } from "./ui.ts";
import type { SlashItem } from "./ui.ts";
import * as cmd from "./commands.ts";

const ROOT_SELECTOR = "#editor";
const DOC_CHANGED_DEBOUNCE_MS = 300;
const AUTOSAVE_DEBOUNCE_MS = 2000;
const METRIC_BATCH = 50;
const READY_MIN_HEIGHT = 120;

// inputTypes that represent genuine text entry / deletion (not formatting).
const INPUT_TYPE_RE =
  /^(insert(Text|Composition|FromPaste|FromDrop|Replace|ReplacementText|Link)|delete(Content|ContentBackward|ContentForward|ByCut|ByDrag))/;

// --- runtime state (module-scoped; single instance per frame) ---
let root: Element | null = null;
let view: EditorView | null = null;
let resizeObserver: ResizeObserver | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

let capabilities = new Set<string>();
let initialized = false;
let rev = 0;
let dirty = false;
let docChangedTimer: ReturnType<typeof setTimeout> | null = null;
let autosaveTimer: ReturnType<typeof setTimeout> | null = null;
let uploadSeq = 0;
let trustedMounted = false; // set by mountTrusted, cleared by its destroy()
const pendingUploads = new Map<string, { name: string; type: string; pos: number }>(); // reqId -> {name, type}

function hasCap(name: string) {
  return capabilities.has(name);
}

function serialize() {
  return {
    doc: view ? view.state.doc.toJSON() : null,
    markdown: view ? serializeMarkdown(view.state.doc) : "",
  };
}

// ---------------------------------------------------------------------------
// ProseMirror view

function stateFromInit(doc: unknown, markdown: unknown): EditorState {
  let pmDoc: PMNode | null = null;
  if (doc && typeof doc === "object") {
    try {
      pmDoc = PMNode.fromJSON(schema, doc);
      pmDoc.check(); // validate against the schema; throw → fall through
    } catch (err) {
      console.warn("[wysiwyg] init.doc rejected by schema, trying markdown:", err);
      pmDoc = null;
    }
  }
  if (!pmDoc && typeof markdown === "string" && markdown.trim()) {
    pmDoc = parseMarkdown(markdown);
  }
  return EditorState.create({
    // `schema` is REQUIRED when there's no initial doc (empty editor), otherwise
    // ProseMirror can't determine the top node type. When pmDoc is present the
    // schema is inferred from it, but passing it explicitly is harmless + correct.
    schema,
    doc: pmDoc || undefined, // undefined → schema default empty doc
    plugins: buildPlugins({ onSave: onSaveShortcut, uiPlugins: uiPlugins({ onPickImage: pickImage }) }),
  });
}

function dispatchTransaction(tr: Transaction) {
  if (!view) return;
  const newState = view.state.apply(tr);
  view.updateState(newState);
  if (tr.docChanged) {
    rev += 1;
    dirty = true;
    scheduleDocChanged();
    scheduleAutosave();
  }
}

function createView(state: EditorState): EditorView {
  const v = new EditorView(root, {
    state,
    dispatchTransaction,
    editable: () => hasCap("document:write"),
    attributes: {
      role: "textbox",
      "aria-multiline": "true",
      "aria-label": "Rich text editor",
      spellcheck: "true",
    },
  });
  v.dom.addEventListener("beforeinput", onBeforeInput);
  v.dom.addEventListener("focus", () => sendEvent("focusChanged", { focused: true }));
  v.dom.addEventListener("blur", () => sendEvent("focusChanged", { focused: false }));
  v.dom.addEventListener("paste", onPaste);
  v.dom.addEventListener("drop", onDrop);
  v.dom.addEventListener("click", onContentClick);
  v.dom.addEventListener("keydown", onDecorationKeydown);
  return v;
}

// Keyboard activation for the task checkbox / toggle summary decorations. They
// carry role + tabindex so they are in the Tab order (SR announces their
// state); Space/Enter must therefore actually toggle them, or the ARIA lies.
function onDecorationKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" && event.key !== " ") return;
  const target = event.target;
  if (!(target instanceof Element) || !view) return;
  const checkbox = target.closest(".wysiwyg-task-checkbox");
  const summary = target.closest(".wysiwyg-toggle-summary");
  const hit = checkbox || summary;
  if (!hit) return;
  const pos = view.posAtDOM(hit, 0);
  if (pos == null || pos < 0) return;
  runCmd(checkbox ? cmd.toggleTaskItemAt(pos) : cmd.toggleOpenAt(pos));
  event.preventDefault();
}

// --- metrics: measure input→next-paint on genuine text input ---
function onBeforeInput(event: Event) {
  if (INPUT_TYPE_RE.test((event as InputEvent).inputType || "")) startSample();
}

// --- image upload (capability-gated; no network in frame) ---
// Records the selection position at paste/drop/pick time so the resulting image

function currentInsertPos() {
  if (!view) return 0;
  const { $from } = view.state.selection;
  return $from.pos;
}

function requestImageUpload(file: File, pos?: number | null) {
  if (!hasCap("upload:images")) return;
  const reqId = `p-upload-${(uploadSeq += 1)}`;
  pendingUploads.set(reqId, { name: file.name, type: file.type, pos: pos == null ? currentInsertPos() : pos });
  file
    .arrayBuffer()
    .then((bytes) => {
      sendEvent("requestUpload", {
        reqId,
        name: file.name,
        type: file.type,
        bytes, // structured-cloned ArrayBuffer (transferable)
      });
    })
    .catch((err) => console.warn("[wysiwyg] upload read failed:", err));
}

function eachImageFile(dt: DataTransfer | null, fn: (file: File) => void) {
  if (!dt) return;
  const items = dt.items;
  if (items && items.length) {
    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      if (it.kind === "file" && /^image\//.test(it.type)) {
        const f = it.getAsFile();
        if (f) fn(f);
      }
    }
    return;
  }
  const files = dt.files;
  if (files && files.length) {
    for (let i = 0; i < files.length; i++) {
      if (/^image\//.test(files[i].type)) fn(files[i]);
    }
  }
}

// A single URL (used to wrap a selection in a link on paste).
const SINGLE_URL_RE = /^(https?:\/\/|mailto:)\S+$/i;
// Heuristic: does this plain text look like markdown worth parsing to blocks?
const MD_HINT_RE = /(^|\n)\s{0,3}(#{1,6}\s|[-*+]\s|\d+\.\s|>\s|```|\|.*\|)/;

function onPaste(event: ClipboardEvent) {
  const dt = event.clipboardData;
  if (!view || !dt) return;

  // 1. Image paste (capability-gated) — unchanged, highest priority.
  if (hasCap("upload:images")) {
    const seen: File[] = [];
    eachImageFile(dt, (file) => seen.push(file));
    if (seen.length) {
      event.preventDefault();
      seen.forEach((file) => requestImageUpload(file, currentInsertPos()));
      return;
    }
  }

  // If the clipboard carries real HTML, let ProseMirror's own parser handle it
  // (richer than our markdown heuristic). Only act on PLAIN-text clipboards.
  const html = dt.getData("text/html");
  const text = dt.getData("text/plain");
  if (html || !text) return;

  const { from, to, empty } = view.state.selection;

  // 2. Paste a single URL over a non-empty selection → wrap it in a link.
  if (!empty && SINGLE_URL_RE.test(text.trim())) {
    const href = cmd.sanitizeHref(text.trim());
    if (href) {
      event.preventDefault();
      cmd.setLink({ href })(view.state, view.dispatch.bind(view));
      return;
    }
  }

  // 3. Paste markdown-looking text → parse to blocks and insert.
  if (empty && MD_HINT_RE.test(text)) {
    const parsed = parseMarkdown(text);
    if (parsed && parsed.childCount) {
      event.preventDefault();
      const tr = view.state.tr.replaceWith(from, to, parsed.content);
      view.dispatch(tr.scrollIntoView());
      return;
    }
  }
  // else: fall through to ProseMirror's default plain-text paste.
}

function onDrop(event: DragEvent) {
  if (!hasCap("upload:images")) return;
  const seen: File[] = [];
  eachImageFile(event.dataTransfer, (file) => seen.push(file));
  if (!seen.length) return;
  event.preventDefault();
  // Resolve the drop position from the pointer so the image lands there.
  const coords =
    event.clientX != null ? view!.posAtCoords({ left: event.clientX, top: event.clientY }) : null;
  const pos = coords ? coords.pos : currentInsertPos();
  seen.forEach((file) => requestImageUpload(file, pos));
}

// File-picker entry for the slash menu's "Image" item (no host DOM needed: a
// hidden <input type=file> works inside the frame).
function pickImage() {
  if (!hasCap("upload:images")) return;
  const input = document.createElement("input");
  input.type = "file";
  input.accept = "image/*";
  input.style.display = "none";
  document.body.appendChild(input);
  input.addEventListener("change", () => {
    const file = input.files && input.files[0];
    if (file) requestImageUpload(file, currentInsertPos());
    if (input.parentNode) input.parentNode.removeChild(input);
  });
  input.click();
}

// Click delegation: toggle task checkboxes and toggle disclosure state. The
// checkbox/arrow are contenteditable=false decorations, so clicks land on them.
function onContentClick(event: MouseEvent) {
  const target = event.target;
  if (!(target instanceof Element) || !view) return;
  const checkbox = target.closest(".wysiwyg-task-checkbox");
  const summary = target.closest(".wysiwyg-toggle-summary");
  const hit = checkbox || summary;
  if (!hit) return;
  // Resolve the CLICKED decoration to a document position and act on the node
  // there — NOT on the current selection (the decoration is
  // contenteditable=false, so clicking it never moves the caret into the item;
  // a selection-based command would toggle whatever was previously selected).
  const pos = view.posAtDOM(hit, 0);
  if (pos == null || pos < 0) return;
  runCmd(checkbox ? cmd.toggleTaskItemAt(pos) : cmd.toggleOpenAt(pos));
  event.preventDefault();
}

function runCmd(
  c: (state: EditorState, dispatch: (tr: Transaction) => void, view: EditorView) => unknown
) {
  if (!view) return;
  c(view.state, view.dispatch.bind(view), view);
}

// ---------------------------------------------------------------------------
// Debounced plugin→host emitters

function scheduleDocChanged() {
  clearTimeout(docChangedTimer!);
  docChangedTimer = setTimeout(emitDocChanged, DOC_CHANGED_DEBOUNCE_MS);
}

function emitDocChanged() {
  if (!hasCap("document:write") || !view) return;
  const { doc, markdown } = serialize();
  sendEvent("docChanged", { doc, markdown, dirty, rev });
  // The root is viewport-clamped (min-height 100%), so DELETING content never
  // fires the ResizeObserver — re-report here (already 300 ms debounced, off
  // the keystroke path) so the frame can shrink with the doc.
  postResize();
}

function scheduleAutosave() {
  clearTimeout(autosaveTimer!);
  autosaveTimer = setTimeout(emitSave, AUTOSAVE_DEBOUNCE_MS);
}

function emitSave() {
  if (!hasCap("document:write") || !view) return;
  const { doc, markdown } = serialize();
  sendEvent("save", { doc, markdown, schemaVersion: SCHEMA_VERSION });
  dirty = false;
}

// Mod-S: explicit save. Flush metrics + the pending docChanged (so the host's
// hidden-field mirror gets the final keystrokes too, not just the save RPC).
function onSaveShortcut() {
  flushMetrics();
  clearTimeout(docChangedTimer!);
  emitDocChanged(); // flush the mirror BEFORE clearing its timer
  emitSave();
  clearTimeout(autosaveTimer!);
}

// ---------------------------------------------------------------------------
// Metrics emission (every METRIC_BATCH samples + on host requestSave)

function flushMetrics() {
  sendEvent("metric", {
    name: "keystroke",
    p50: metrics.p50(),
    p99: metrics.p99(),
    count: metrics.count,
    samplesMs: metrics.samplesMs.slice(),
  });
}

metrics.onSample(() => {
  if (metrics.count > 0 && metrics.count % METRIC_BATCH === 0) flushMetrics();
});

// ---------------------------------------------------------------------------
// host → plugin handlers

function handleInit(params: Record<string, unknown>) {
  const p = params || {};
  capabilities = new Set(Array.isArray(p.capabilities) ? p.capabilities : []);
  if (hasCap("theme:read")) {
    applyTokens(p.tokens);
    applyScheme(p.scheme);
    emitThemeApplied(p.tokens, p.scheme);
  }

  const state = stateFromInit(p.doc, p.markdown);
  if (view) {
    view.destroy();
    view = null;
  }
  view = createView(state);
  root!.setAttribute("data-readonly", String(!hasCap("document:write")));

  if (p.schemaVersion && p.schemaVersion !== SCHEMA_VERSION) {
    console.warn(
      `[wysiwyg] schemaVersion mismatch: host=${p.schemaVersion} frame=${SCHEMA_VERSION}`
    );
  }

  initialized = true;
  // report content height so the host sizes the iframe immediately.
  postResize();
}

function handleThemeChanged(params: Record<string, unknown>) {
  const p = params || {};
  applyTokens(p.tokens);
  applyScheme(p.scheme);
  emitThemeApplied(p.tokens, p.scheme);
}

// §8a: report what the frame actually resolved after applying the crossed tokens,
// so a host-side test can assert the value matches the host's.
function emitThemeApplied(tokens: unknown, scheme: unknown) {
  sendEvent("themeApplied", { scheme, sample: sampleAppliedTokens(tokens) });
}

// §8a: self-isolation probes, computed INSIDE the opaque frame at boot. Under
// sandbox="allow-scripts" (no allow-same-origin) each of these is blocked by the
// browser, so accessing them throws — which is exactly the third-party guarantee.
function isolationProbes() {
  let cookieEmpty = false;
  let parentBlocked = false;
  let storageBlocked = false;
  try {
    cookieEmpty = document.cookie === "";
  } catch (e) {
    cookieEmpty = true; // access itself blocked → no cookie reach
  }
  try {
    void window.parent.document;
    parentBlocked = false;
  } catch (e) {
    parentBlocked = true;
  }
  try {
    void window.localStorage;
    storageBlocked = false;
  } catch (e) {
    storageBlocked = true;
  }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

// requestSave is a REQUEST → must return {doc, markdown, schemaVersion}.
function handleRequestSave() {
  if (!view) {
    // The editor was torn down; refuse rather than resolve an empty doc that
    // a host would then persist over real content (trusted-mount race).
    throw { code: "E_TORN_DOWN", message: "editor is not mounted" };
  }
  flushMetrics(); // §8: post metric immediately when the host requests a save
  const { doc, markdown } = serialize();
  dirty = false;
  return { doc, markdown, schemaVersion: SCHEMA_VERSION };
}

// uploadResult is an EVENT answering a prior requestUpload. The image block is
// inserted at the position recorded at paste/drop/pick time (or the current
// selection as a fallback). If the target block is an empty paragraph it is
// replaced in place; otherwise the image is placed after the enclosing block.
function handleUploadResult(params: Record<string, unknown>) {
  const p = params || {};
  const pending = pendingUploads.get(p.reqId as string);
  pendingUploads.delete(p.reqId as string);
  if (p.error) {
    console.warn("[wysiwyg] upload failed:", p.reqId, p.error);
    return;
  }
  if (!p.url || !view) return;
  insertImageBlock(p.url as string, pending ? pending.pos : null);
}

function insertImageBlock(src: string, pos: number | null) {
  if (!view) return;
  const image = schema.nodes.image.create({ src });
  const tr = view.state.tr;
  let $pos: ResolvedPos | null = null;
  if (pos != null && pos >= 0 && pos <= view.state.doc.content.size) {
    try {
      $pos = view.state.doc.resolve(pos);
    } catch (e) {
      $pos = null;
    }
  }
  if ($pos && $pos.depth >= 1) {
    const start = $pos.before(1);
    const block = $pos.node(1);
    if (block.type.name === "paragraph" && block.childCount === 0) {
      tr.replaceWith(start, start + block.nodeSize, image);
    } else {
      tr.insert($pos.after(1), image);
    }
  } else {
    tr.insert(view.state.doc.content.size, image);
  }
  view.dispatch(tr.scrollIntoView());
}

// teardown is a REQUEST → return {} after a clean teardown (no leaked listeners).
function handleTeardown() {
  teardown();
  return {};
}

// ---------------------------------------------------------------------------
// Lifecycle + resize

// Extent of any visible floating overlay (slash menu / bubble / link editor).
// Overlays are position:fixed and out of flow, so content height alone would
// let an overlay near the bottom be CLIPPED at the iframe edge (the frame is
// exactly content-sized). The reported height is content ∪ overlay extent, so
// the frame grows while an overlay is open and shrinks back when it closes.
function overlayExtent(): number {
  let max = 0;
  const sels = [".wysiwyg-slash-menu", ".wysiwyg-bubble", ".wysiwyg-linkpop"];
  for (const sel of sels) {
    document.querySelectorAll<HTMLElement>(sel).forEach((node) => {
      if (node.style.display === "none") return;
      const r = node.getBoundingClientRect();
      if (r.height > 0) max = Math.max(max, r.bottom + 12);
    });
  }
  return max;
}

// Intrinsic height of the CONTENT, not of the mount root: the root is
// flex-stretched to fill the frame viewport, so root.getBoundingClientRect()
// always answers "current frame height" — measuring it made the frame a
// ratchet (grew for an overlay, never shrank back, permanently pushing the
// host page). Measure the bottom of the last block inside the editable plus
// its bottom padding instead.
function contentExtent(): number {
  if (!root) return 0;
  const pm = root.querySelector(".ProseMirror") || root;
  let bottom = 0;
  for (let i = 0; i < pm.children.length; i++) {
    const r = pm.children[i].getBoundingClientRect();
    if (r.bottom > bottom) bottom = r.bottom;
  }
  const pad = parseFloat(getComputedStyle(pm).paddingBottom) || 0;
  return Math.ceil(bottom + pad);
}

function postResize() {
  if (!root) return;
  const h = Math.ceil(Math.max(READY_MIN_HEIGHT, contentExtent(), overlayExtent()));
  sendEvent("resize", { height: h });
}

function setupResizeObserver() {
  if (typeof ResizeObserver === "undefined" || !root) return;
  resizeObserver = new ResizeObserver(() => postResize());
  resizeObserver.observe(root);
}

// Mobile virtual-keyboard handling: when the visual viewport shrinks (keyboard
// up), scroll the caret into view so it isn't covered. visualViewport is the
// modern API for this; it fires on iOS/Android keyboard show/hide.
function setupMobileViewport() {
  const vv = window.visualViewport;
  if (!vv) return;
  let timer: ReturnType<typeof setTimeout> | null = null;
    const onResize = () => {
      clearTimeout(timer!);
      timer = setTimeout(() => {
      if (!view || !view.hasFocus()) return;
      const { from } = view.state.selection;
      try {
        const coords = view.coordsAtPos(from);
        // If the caret is below the visible viewport top region, scroll to it.
        if (coords.bottom > vv.height + vv.offsetTop - 8) {
          const target = coords.top - vv.height / 2;
          window.scrollTo(0, Math.max(0, target));
        }
      } catch (e) {
        /* selection pos out of range — ignore */
      }
    }, 60);
  };
  vv.addEventListener("resize", onResize);
  vv.addEventListener("scroll", onResize);
}

function announceReady() {
  sendEvent("ready", {
    version: PROTOCOL_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: READY_MIN_HEIGHT,
    probes: isolationProbes(), // §8a — host stashes these on the iframe element
  });
}

function teardown() {
  // Flush any unsaved edits before tearing down — an SPA navigation can fire
  // inside the autosave debounce window, and clearing the timers without a
  // flush would silently lose the last <2s of typing (protocol requestSave
  // exists for exactly this; flushing here covers the framed AND trusted paths).
  if (dirty) {
    emitDocChanged();
    emitSave();
  }
  clearTimeout(docChangedTimer!);
  clearTimeout(autosaveTimer!);
  docChangedTimer = null;
  autosaveTimer = null;
  if (resizeObserver) {
    resizeObserver.disconnect();
    resizeObserver = null;
  }
  if (view) {
    view.destroy();
    view = null;
  }
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
  pendingUploads.clear();
  initialized = false;
}

// ---------------------------------------------------------------------------
// Boot

export function bootFrame() {
  try {
    root = document.querySelector(ROOT_SELECTOR);
    if (!root) {
      console.error("[wysiwyg] mount root", ROOT_SELECTOR, "not found");
      return;
    }

    const handlers = {
      init: handleInit,
      themeChanged: handleThemeChanged,
      requestSave: handleRequestSave,
      uploadResult: handleUploadResult,
      teardown: handleTeardown,
      // Host page saw a pointerdown outside this frame — dismiss open overlays
      // (iOS WebKit gives the frame no blur for that; see the broker relay).
      hostPointerdown: () => {
        dismissAllOverlays();
      },
    };
    messageListener = createRouter(handlers);
    window.addEventListener("message", messageListener);

    // Slash-menu "Image" → in-frame file picker → requestUpload.
    setSlashImageHook(pickImage);
  setSlashItemFilter((it: SlashItem) => (it.needsUpload ? hasCap("upload:images") : true));
    setSlashItemFilter((it: SlashItem) => (it.needsUpload ? hasCap("upload:images") : true));
    // Overlays report open/close/reposition so the frame can grow to fit them.
    setOverlayChangedHook(postResize);
    // Mobile: keep the caret above the virtual keyboard (visualViewport-aware).
    setupMobileViewport();

    setupResizeObserver();
    // The frame speaks first (the host cannot know when JS finished loading).
    announceReady();
  } catch (err) {
    // Surface any boot-time throw to the host instead of failing silently — a
    // frame that can't boot would otherwise just never send `ready`.
    try {
      window.parent.postMessage(
        { v: 1, id: "p-booterr", type: "event", src: "plugin", method: "bootError", params: { error: String((err && (err as { stack?: string }).stack) || err) } },
        "*"
      );
    } catch (e2) {
      /* postMessage itself failed — nothing more we can do */
    }
  }
}

// ---------------------------------------------------------------------------
// Trusted in-page mount (DECISIONS.md "secure by default, opt out").
//
// Same editor, same protocol envelopes, no iframe and no wire: the host page
// calls the returned handle directly and receives plugin events via onEvent.
// This mode runs the plugin bundle WITH FULL PAGE ACCESS — it exists only for
// plugins the app owner compiles in and vouches for, and only behind an
// explicit host-side opt-in (wysiwyg.WithTrustedMount on the Go side).
// One editor instance per page (module-scoped state).

export interface TrustedMountOptions {
  /** Receives every plugin→host event: ready, docChanged, save, requestUpload,
   * metric, focusChanged, resize (ignorable in-page), themeApplied. */
  onEvent?: (method: string, params: unknown) => void;
}

export interface TrustedMountHandle {
  /** Host→plugin init (doc/markdown/capabilities/schemaVersion). */
  init(params: Record<string, unknown>): void;
  /** Fire a host→plugin event (themeChanged, uploadResult, …). */
  event(method: string, params?: Record<string, unknown>): void;
  /** Host→plugin request (requestSave, teardown) → the handler's result. */
  request(method: string, params?: Record<string, unknown>): Promise<unknown>;
  /** Tear the editor down and release the mount. */
  destroy(): void;
}

export function mountTrusted(el: Element, opts: TrustedMountOptions = {}): TrustedMountHandle {
  // Guard on a dedicated flag, NOT on `view`: the view is created lazily by
  // init(), so a `view`-based guard would let a second mountTrusted() before
  // init() silently steal the transport/root/overlay parent.
  if (trustedMounted) throw new Error("wysiwyg: an editor is already mounted on this page");
  trustedMounted = true;
  root = el;

  // plugin→host: deliver envelopes straight to the host callback; host→plugin
  // requests are answered through the same channel, resolved locally by id.
  const awaiting = new Map<string, { resolve: (v: unknown) => void; reject: (e: unknown) => void }>();
  setTransport({
    post(msg: Envelope) {
      if (msg.type === "response") {
        const entry = awaiting.get(msg.id);
        if (!entry) return;
        awaiting.delete(msg.id);
        if (msg.error) entry.reject(msg.error);
        else entry.resolve(msg.result);
        return;
      }
      if (opts.onEvent && msg.method) opts.onEvent(msg.method, msg.params);
    },
  });

  const handlers = {
    init: handleInit,
    themeChanged: handleThemeChanged,
    requestSave: handleRequestSave,
    uploadResult: handleUploadResult,
    teardown: handleTeardown,
  };
  const deliver = (msg: Envelope) => routeEnvelope(handlers, msg);

  setSlashImageHook(pickImage);
  // Overlays attach inside the mount element so the scoped stylesheet reaches
  // them; they position against the real page viewport, so neither the frame
  // autosize hook nor the resize observer is needed in this mode.
  setOverlayParent(el as HTMLElement);
  setOverlayChangedHook(null);

  let hostSeq = 0;
  const envelope = (
    type: "event" | "request",
    method: string,
    params: Record<string, unknown>
  ): Envelope => ({ v: PROTOCOL_VERSION, id: `h-${(hostSeq += 1)}`, type, src: "host", method, params });

  announceReady(); // synchronous: opts.onEvent sees "ready" before mount returns

  return {
    init(params) {
      deliver(envelope("event", "init", params));
    },
    event(method, params = {}) {
      deliver(envelope("event", method, params));
    },
    request(method, params = {}) {
      return new Promise((resolve, reject) => {
        const msg = envelope("request", method, params);
        awaiting.set(msg.id, { resolve, reject });
        deliver(msg);
      });
    },
    destroy() {
      teardown();
      // Reject any in-flight requests — teardown removes the only settlement
      // path, so without this they hang forever.
      awaiting.forEach((entry) => entry.reject({ code: "E_TEARDOWN", message: "editor torn down" }));
      awaiting.clear();
      setOverlayParent(null);
      setTransport(defaultTransport); // restore the frame transport for a clean re-mount
      root = null;
      trustedMounted = false;
    },
  };
}
