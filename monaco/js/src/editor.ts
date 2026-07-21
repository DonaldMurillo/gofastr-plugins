// GoFastr Monaco code editor — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY. Never touches host cookies,
// localStorage, or the host DOM. On load it announces `ready`; the host replies
// `init`; we mount Monaco (normal or diff editor) from init.{doc, config}, apply
// bridged tokens, respect granted capabilities, and emit the full plugin->host
// event set.
//
// Canonical doc (schema monaco-v1): { code, language }.
// All editing stays IN-FRAME; the only host channel is postMessage.

import * as monaco from "monaco-editor/esm/vs/editor/editor.api";

// Basic-language contributions register the monarch tokenizer for each language
// (main-thread syntax highlighting — works worker-free). Richer language
// services (completions/diagnostics) need workers and are OFF by default.
import "monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution";
import "monaco-editor/esm/vs/basic-languages/typescript/typescript.contribution";
import "monaco-editor/esm/vs/basic-languages/go/go.contribution";
import "monaco-editor/esm/vs/basic-languages/python/python.contribution";
import "monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution";
import "monaco-editor/esm/vs/basic-languages/html/html.contribution";
import "monaco-editor/esm/vs/basic-languages/css/css.contribution";
import "monaco-editor/esm/vs/basic-languages/sql/sql.contribution";
import "monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution";
import "monaco-editor/esm/vs/basic-languages/shell/shell.contribution";

// The editor.worker.js source, inlined as a string by build.mjs (onLoad plugin).
// Used ONLY when the host opts into workers (config.workers === true); the
// default worker-free path never constructs a real Worker.
import editorWorkerSource from "monaco-editor/esm/vs/editor/editor.worker.js";

import { PROTOCOL_VERSION, sendEvent, createRouter, type HandlerMap } from "./protocol";
import { applyTokens, applyScheme, sampleAppliedTokens, monacoThemeData } from "./theme";

const CONTAINER_SELECTOR = "#monaco-container";
const STATUS_SELECTOR = "#monaco-status";

const SCHEMA_VERSION = "monaco-v1";
const DOC_CHANGED_DEBOUNCE_MS = 300;
const AUTOSAVE_DEBOUNCE_MS = 2000;
const READY_MIN_HEIGHT = 240;
const MAX_EDITOR_HEIGHT = 2000;

const LIGHT_THEME = "gofastr-light";
const DARK_THEME = "gofastr-dark";

interface DiffConfig {
  original: string;
  modified: string;
  language?: string;
}

interface EditorConfig {
  language: string;
  theme: string; // "light" | "dark" | "auto"
  readOnly: boolean;
  minimap: boolean;
  wordWrap: boolean;
  lineNumbers: boolean;
  fontSize: number;
  workers: boolean;
  diff: DiffConfig | null;
}

const DEFAULT_CONFIG: EditorConfig = {
  language: "plaintext",
  theme: "auto",
  readOnly: false,
  minimap: false,
  wordWrap: false,
  lineNumbers: true,
  fontSize: 14,
  workers: false,
  diff: null,
};

// --- MonacoEnvironment / workers -------------------------------------------
//
// Monaco spawns web workers for language services. Under sandbox="allow-scripts"
// WITHOUT allow-same-origin the frame is on an OPAQUE origin, so a worker loaded
// from a same-origin URL or a blob:/data: URL is restricted and would throw.
// DEFAULT = worker-free: a NoopWorker swallows every postMessage. The monarch
// tokenizer (syntax highlighting) runs on the MAIN thread and degrades
// gracefully — the editor still boots and highlights. `config.workers === true`
// OPTS IN: we try to spin up a real worker from a blob URL built from the bundled
// worker source; if the sandbox refuses it we fall back to NoopWorker (never throw).

class NoopWorker {
  postMessage(): void {}
  terminate(): void {}
  addEventListener(): void {}
  removeEventListener(): void {}
  dispatchEvent(): boolean { return true; }
  // Monaco assigns .onmessage; accept any handler, never invoke it.
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  set onmessage(_: ((ev: MessageEvent) => void) | null) {}
  get onmessage(): ((ev: MessageEvent) => void) | null { return null; }
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  set onerror(_: ((ev: ErrorEvent) => void) | null) {}
  get onerror(): ((ev: ErrorEvent) => void) | null { return null; }
}

function realWorkerFromSource(source: string): Worker | null {
  try {
    const blob = new Blob([source], { type: "text/javascript" });
    const url = URL.createObjectURL(blob);
    const w = new Worker(url);
    // Revoke shortly after; the worker is already running.
    setTimeout(() => URL.revokeObjectURL(url), 0);
    return w;
  } catch (err) {
    console.warn("[monaco] worker opt-in failed, falling back to worker-free:", err);
    return null;
  }
}

function configureWorkers(enabled: boolean): void {
  self.MonacoEnvironment = {
    getWorker: () => {
      if (enabled) {
        const w = realWorkerFromSource(editorWorkerSource);
        if (w) return w;
      }
      // Worker-free fallback. NoopWorker quacks like a Worker for Monaco's API.
      return new NoopWorker() as unknown as Worker;
    },
  };
}

// --- runtime state (module-scoped; single instance per frame) ---
let container: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let editor: monaco.editor.IStandaloneCodeEditor | null = null;
let diffEditor: monaco.editor.IStandaloneDiffEditor | null = null;
let resizeObserver: ResizeObserver | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

let code = "";
let language = "plaintext";
let initialized = false;
let capabilities = new Set<string>();
let scheme = "light";
let config: EditorConfig = DEFAULT_CONFIG;
let dirty = false;

let docChangedTimer: number | undefined;
let autosaveTimer: number | undefined;

function hasCap(name: string): boolean {
  return capabilities.has(name);
}

// --- boundary narrowing (no `any`: validate untrusted init params) ---------

function readStringRecord(raw: unknown): Record<string, unknown> {
  return raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
}

function readString(raw: unknown, fallback: string): string {
  return typeof raw === "string" ? raw : fallback;
}

function readBool(raw: unknown, fallback: boolean): boolean {
  return typeof raw === "boolean" ? raw : fallback;
}

function readNumber(raw: unknown, fallback: number): number {
  return typeof raw === "number" && Number.isFinite(raw) ? raw : fallback;
}

function readConfig(raw: unknown): EditorConfig {
  const o = readStringRecord(raw);
  const diffRaw = o.diff;
  let diff: DiffConfig | null = null;
  if (diffRaw && typeof diffRaw === "object") {
    const d = diffRaw as Record<string, unknown>;
    if (typeof d.original === "string" && typeof d.modified === "string") {
      diff = {
        original: d.original,
        modified: d.modified,
        language: typeof d.language === "string" ? d.language : undefined,
      };
    }
  }
  return {
    language: readString(o.language, DEFAULT_CONFIG.language),
    theme: readString(o.theme, DEFAULT_CONFIG.theme),
    readOnly: readBool(o.readOnly, DEFAULT_CONFIG.readOnly),
    minimap: readBool(o.minimap, DEFAULT_CONFIG.minimap),
    wordWrap: readBool(o.wordWrap, DEFAULT_CONFIG.wordWrap),
    lineNumbers: readBool(o.lineNumbers, DEFAULT_CONFIG.lineNumbers),
    fontSize: readNumber(o.fontSize, DEFAULT_CONFIG.fontSize),
    workers: readBool(o.workers, DEFAULT_CONFIG.workers),
    diff,
  };
}

function deriveDoc(raw: unknown): { code: string; language: string } {
  const o = readStringRecord(raw);
  return {
    code: typeof o.code === "string" ? o.code : "",
    language: typeof o.language === "string" ? o.language : "",
  };
}

// --- theme + scheme --------------------------------------------------------

function resolvedScheme(): string {
  return config.theme === "dark" || config.theme === "light" ? config.theme : scheme;
}

function applyMonacoTheme(tokens: unknown): void {
  const data = monacoThemeData(tokens, resolvedScheme());
  monaco.editor.defineTheme(resolvedScheme() === "dark" ? DARK_THEME : LIGHT_THEME, {
    base: data.base,
    inherit: data.inherit,
    rules: data.rules,
    colors: data.colors,
  });
  monaco.editor.setTheme(resolvedScheme() === "dark" ? DARK_THEME : LIGHT_THEME);
}

// --- editor mounting -------------------------------------------------------

function editorOptions(): monaco.editor.IStandaloneEditorConstructionOptions {
  return {
    readOnly: config.readOnly || !hasCap("document:write"),
    minimap: { enabled: config.minimap },
    wordWrap: config.wordWrap ? "on" : "off",
    lineNumbers: config.lineNumbers ? "on" : "off",
    fontSize: config.fontSize,
    scrollBeyondLastLine: false,
    automaticLayout: true,
    theme: resolvedScheme() === "dark" ? DARK_THEME : LIGHT_THEME,
  };
}

function mountNormalEditor(initialCode: string, lang: string): void {
  configureWorkers(config.workers);
  const model = monaco.editor.createModel(initialCode, lang);
  editor = monaco.editor.create(container!, editorOptions());
  editor.setModel(model);
  wireEditor(editor);
}

function mountDiffEditor(diff: DiffConfig, lang: string): void {
  configureWorkers(config.workers);
  const original = monaco.editor.createModel(diff.original, lang);
  const modified = monaco.editor.createModel(diff.modified, lang);
  diffEditor = monaco.editor.createDiffEditor(container!, {
    ...editorOptions(),
    readOnly: config.readOnly || !hasCap("document:write"),
  });
  diffEditor.setModel({ original, modified });
  // Track the editable side for save/resize.
  editor = diffEditor.getModifiedEditor();
  wireEditor(editor);
}

function wireEditor(ed: monaco.editor.IStandaloneCodeEditor): void {
  ed.onDidChangeModelContent(() => {
    dirty = true;
    code = ed.getValue();
    scheduleDocChanged();
    scheduleAutosave();
  });
  ed.onDidContentSizeChange(() => updateHeight());
  ed.onDidFocusEditorText(() => sendEvent("focusChanged", { focused: true }));
  ed.onDidBlurEditorText(() => sendEvent("focusChanged", { focused: false }));
  // Mod-S / Ctrl-S saves immediately (in addition to the debounced autosave).
  ed.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
    window.clearTimeout(autosaveTimer);
    emitSave();
  });
}

function mount(): void {
  if (!container) return;
  if (config.diff) {
    const lang = config.diff.language || config.language;
    mountDiffEditor(config.diff, lang);
    language = lang;
  } else {
    mountNormalEditor(code, language);
  }
  updateHeight();
}

function remount(): void {
  // Tear down the current editor and re-mount (called on a config/scheme change
  // that the live editor cannot apply in place, e.g. switching diff<->normal).
  disposeEditor();
  mount();
}

function disposeEditor(): void {
  if (editor) { editor.getModel()?.dispose(); editor.dispose(); editor = null; }
  if (diffEditor) {
    const o = diffEditor.getModel();
    o?.original.dispose();
    o?.modified.dispose();
    diffEditor.dispose();
    diffEditor = null;
  }
}

// --- resize / auto-height --------------------------------------------------

function updateHeight(): void {
  if (!editor || !container) return;
  const contentHeight = Math.min(editor.getContentHeight(), MAX_EDITOR_HEIGHT);
  container.style.height = `${contentHeight}px`;
  editor.layout();
  sendEvent("resize", { height: contentHeight });
}

// --- plugin -> host events -------------------------------------------------

function scheduleDocChanged(): void {
  window.clearTimeout(docChangedTimer);
  docChangedTimer = window.setTimeout(emitDocChanged, DOC_CHANGED_DEBOUNCE_MS);
}

function scheduleAutosave(): void {
  window.clearTimeout(autosaveTimer);
  autosaveTimer = window.setTimeout(emitSave, AUTOSAVE_DEBOUNCE_MS);
}

function emitDocChanged(): void {
  if (!hasCap("document:write")) return;
  sendEvent("docChanged", { code, language });
}

function emitSave(): void {
  if (!hasCap("document:write")) return;
  sendEvent("save", { code, language, schemaVersion: SCHEMA_VERSION });
  dirty = false;
}

function emitThemeApplied(tokens: unknown): void {
  sendEvent("themeApplied", { scheme: resolvedScheme(), sample: sampleAppliedTokens(tokens) });
}

// --- host -> plugin handlers ----------------------------------------------

function handleInit(params: unknown): void {
  const p = readStringRecord(params);
  capabilities = new Set(Array.isArray(p.capabilities) ? (p.capabilities as string[]) : []);
  if (typeof p.scheme === "string") scheme = p.scheme;
  config = readConfig(p.config);

  const langFromDoc = deriveDoc(p.doc).language;
  language = config.language !== "plaintext" ? config.language : (langFromDoc || config.language);
  code = hasCap("document:read") ? deriveDoc(p.doc).code : "";

  configureWorkers(config.workers);
  if (hasCap("theme:read")) {
    applyTokens(p.tokens);
    applyScheme(scheme);
    applyMonacoTheme(p.tokens);
  } else {
    applyMonacoTheme(null);
  }

  if (typeof p.schemaVersion === "string" && p.schemaVersion !== SCHEMA_VERSION) {
    console.warn(`[monaco] schemaVersion mismatch: host=${p.schemaVersion} frame=${SCHEMA_VERSION}`);
  }

  mount();
  initialized = true;
  if (hasCap("theme:read")) emitThemeApplied(p.tokens);
  updateHeight();
}

function handleThemeChanged(params: unknown): void {
  const p = readStringRecord(params);
  if (typeof p.scheme === "string") scheme = p.scheme;
  applyTokens(p.tokens);
  applyScheme(scheme);
  applyMonacoTheme(p.tokens);
  emitThemeApplied(p.tokens);
}

function handleRequestSave(): { code: string; language: string; schemaVersion: string } {
  return { code, language, schemaVersion: SCHEMA_VERSION };
}

// reconfigure is a host->plugin EVENT that changes editor options LIVE, without a
// remount — the showcase demo drives this from its control panel. Only the keys
// present in params change; the rest are left as-is. Toggling diff<->normal is
// the one change Monaco cannot do in place, so it remounts. An optional `code`
// replaces the buffer (used by the "load sample" control).
function handleReconfigure(params: unknown): void {
  const p = readStringRecord(params);
  const prevDiff = !!config.diff;
  const prevLang = config.language;

  if (typeof p.language === "string") config.language = p.language;
  if (typeof p.theme === "string") config.theme = p.theme;
  if (typeof p.readOnly === "boolean") config.readOnly = p.readOnly;
  if (typeof p.minimap === "boolean") config.minimap = p.minimap;
  if (typeof p.wordWrap === "boolean") config.wordWrap = p.wordWrap;
  if (typeof p.lineNumbers === "boolean") config.lineNumbers = p.lineNumbers;
  if (typeof p.fontSize === "number" && Number.isFinite(p.fontSize)) config.fontSize = p.fontSize;
  if ("diff" in p) {
    const d = p.diff;
    if (d && typeof d === "object") {
      const dd = d as Record<string, unknown>;
      if (typeof dd.original === "string" && typeof dd.modified === "string") {
        config.diff = { original: dd.original, modified: dd.modified, language: typeof dd.language === "string" ? dd.language : undefined };
      }
    } else if (d === null) {
      config.diff = null;
    }
  }

  // Diff<->normal is a different editor type: rebuild it.
  if (prevDiff !== !!config.diff) {
    remount();
    return;
  }
  if (!editor) {
    mount();
    return;
  }
  editor.updateOptions({
    readOnly: config.readOnly || !hasCap("document:write"),
    minimap: { enabled: config.minimap },
    wordWrap: config.wordWrap ? "on" : "off",
    lineNumbers: config.lineNumbers ? "on" : "off",
    fontSize: config.fontSize,
  });
  if (config.language !== prevLang) {
    const m = editor.getModel();
    if (m) monaco.editor.setModelLanguage(m, config.language);
  }
  if (typeof p.code === "string") {
    editor.setValue(p.code);
    code = p.code;
  }
  updateHeight();
}

function handleSaveResult(params: unknown): void {
  const p = readStringRecord(params);
  if (!statusEl) return;
  if (readBool(p.ok, true)) {
    statusEl.textContent = "";
    statusEl.removeAttribute("role");
    return;
  }
  // Non-blocking status line on a failed save (esp. 409 conflict). richtext
  // shows a banner; we mirror that idea minimally — a single polite line.
  const status = readNumber(p.status, 0);
  const codeStr = readString(p.code, "E_SAVE");
  const msg = status === 409
    ? "Save conflict — the document changed elsewhere. Your edits were not saved."
    : `Save failed (${codeStr}). Your edits are still here.`;
  statusEl.textContent = msg;
  statusEl.setAttribute("role", "status");
}

function handleTeardown(): Record<string, never> {
  teardown();
  return {};
}

// --- lifecycle -------------------------------------------------------------

function teardown(): void {
  if (dirty) emitSave(); // flush before teardown (SPA nav within the debounce)
  window.clearTimeout(docChangedTimer);
  window.clearTimeout(autosaveTimer);
  docChangedTimer = undefined;
  autosaveTimer = undefined;
  disposeEditor();
  if (resizeObserver) { resizeObserver.disconnect(); resizeObserver = null; }
  if (messageListener) { window.removeEventListener("message", messageListener); messageListener = null; }
  initialized = false;
}

function isolationProbes(): { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } {
  let cookieEmpty = false;
  let parentBlocked = false;
  let storageBlocked = false;
  try { cookieEmpty = document.cookie === ""; } catch { cookieEmpty = true; }
  try { void window.parent.document; parentBlocked = false; } catch { parentBlocked = true; }
  try { void window.localStorage; storageBlocked = false; } catch { storageBlocked = true; }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

function announceReady(): void {
  sendEvent("ready", {
    version: PROTOCOL_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: READY_MIN_HEIGHT,
    probes: isolationProbes(),
  });
}

function boot(): void {
  try {
    container = document.querySelector<HTMLElement>(CONTAINER_SELECTOR);
    statusEl = document.querySelector<HTMLElement>(STATUS_SELECTOR);
    if (!container) {
      console.error("[monaco] mount container not found");
      return;
    }

    const handlers: HandlerMap = {
      init: handleInit,
      themeChanged: handleThemeChanged,
      reconfigure: handleReconfigure,
      requestSave: handleRequestSave,
      saveResult: handleSaveResult,
      teardown: handleTeardown,
    };
    messageListener = createRouter(handlers);
    window.addEventListener("message", messageListener);

    if (typeof ResizeObserver !== "undefined") {
      resizeObserver = new ResizeObserver(() => updateHeight());
      resizeObserver.observe(container);
    }

    // The frame speaks first (the host cannot know when JS finished loading).
    announceReady();
  } catch (err) {
    // Surface any boot-time throw to the host instead of failing silently — a
    // frame that can't boot would otherwise just never send `ready`.
    try {
      const e = err as { stack?: unknown } | null;
      window.parent.postMessage(
        { v: 1, id: "p-booterr", type: "event", src: "plugin", method: "bootError", params: { error: String((e && e.stack) || e) } },
        "*"
      );
    } catch {
      /* postMessage itself failed — nothing more we can do */
    }
  }
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
