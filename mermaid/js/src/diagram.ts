// GoFastr Mermaid diagram editor — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY. Never touches host cookies,
// localStorage, or the host DOM. On load it announces `ready`; the host replies
// `init`; we populate the <textarea> from init.doc.source, apply bridged tokens,
// respect granted capabilities, render the diagram via the bundled Mermaid lib,
// and emit the full plugin→host event set.
//
// Canonical doc (schema mermaid-v1): { source: "<mermaid graph text>" }.
// All rendering stays IN-FRAME; the only host channel is window.parent.postMessage.

import mermaid from "mermaid";
import { PROTOCOL_VERSION, sendEvent, createRouter, type HandlerMap } from "./protocol";
import { applyTokens, applyScheme, sampleAppliedTokens, mermaidThemeVariables } from "./theme";
import { metrics, startSample, finishSample } from "./metrics";

const ROOT_SELECTOR = "#mermaid-root";
const TEXTAREA_SELECTOR = "#mermaid-source";
const PREVIEW_SELECTOR = "#mermaid-preview";

const SCHEMA_VERSION = "mermaid-v1";
const DOC_CHANGED_DEBOUNCE_MS = 300;
const AUTOSAVE_DEBOUNCE_MS = 2000;
const RENDER_DEBOUNCE_MS = 300;
const METRIC_BATCH = 10;
const READY_MIN_HEIGHT = 200;

const DEFAULT_SOURCE = "graph TD\n    A[Start] --> B{Decision}\n    B -->|Yes| C[Do it]\n    B -->|No| D[Skip]";

// Untrusted host→plugin payloads: every field is validated before use, so the
// shapes below are all-optional/unknown on purpose.
interface InitParams {
  capabilities?: unknown;
  scheme?: unknown;
  tokens?: unknown;
  doc?: unknown;
  schemaVersion?: unknown;
}

interface ThemeChangedParams {
  scheme?: unknown;
  tokens?: unknown;
}

// --- runtime state (module-scoped; single instance per frame) ---
let root: HTMLElement | null = null;
let textarea: HTMLTextAreaElement | null = null;
let preview: HTMLElement | null = null;
let resizeObserver: ResizeObserver | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

let source = "";
let initialized = false;
let capabilities = new Set<string>();
let scheme = "light";
let mermaidConfigured = false;
let renderSeq = 0;
let renderTimer: ReturnType<typeof setTimeout> | undefined;
let docChangedTimer: ReturnType<typeof setTimeout> | undefined;
let autosaveTimer: ReturnType<typeof setTimeout> | undefined;
let dirty = false;

function hasCap(name: string): boolean {
  return capabilities.has(name);
}

function escapeHtml(s: unknown): string {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

// ---------------------------------------------------------------------------
// Mermaid rendering

function configureMermaid(tokens: unknown): void {
  const themeVars = hasCap("theme:read") ? mermaidThemeVariables(tokens) : {};
  // "base" theme honors themeVariables, so the host palette drives the SVG.
  // On a dark scheme, dark node/edge defaults read better even with vars set.
  const mermaidTheme = scheme === "dark" ? "dark" : "base";
  try {
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: "strict", // no inline event handlers / no html labels escape
      theme: mermaidTheme,
      themeVariables: themeVars,
      flowchart: { htmlLabels: false, curve: "basis" },
      fontFamily: "inherit",
    });
    mermaidConfigured = true;
  } catch (err) {
    console.error("[mermaid] initialize failed:", err);
  }
}

async function renderDiagram(): Promise<void> {
  const code = source.trim();
  if (!code) {
    preview!.innerHTML = "";
    postResize();
    return;
  }
  if (!mermaidConfigured) configureMermaid(null);
  renderSeq += 1;
  const seq = renderSeq;
  // Unique id per render so Mermaid never collides with a leftover temp node.
  const id = `mermaid-svg-${renderSeq}`;
  startSample();
  try {
    const out = await mermaid.render(id, code);
    // A newer render started while this one awaited — drop this stale result so
    // an older SVG can't overwrite the newer one (out-of-order completion).
    if (seq !== renderSeq) return;
    const svg = out && out.svg ? out.svg : "";
    preview!.innerHTML = svg;
    // Mermaid's rendered SVG defaults to a fixed width; let it scale to container.
    const svgEl = preview!.querySelector("svg");
    if (svgEl) {
      svgEl.style.maxWidth = "100%";
      svgEl.style.height = "auto";
    }
    if (typeof out.bindFunctions === "function") {
      try { out.bindFunctions(preview!); } catch (e) { /* non-fatal */ }
    }
  } catch (err) {
    if (seq !== renderSeq) return; // stale error from a superseded render
    // Render errors (syntax) are expected user input; show them in-frame.
    const e = err as { str?: unknown; message?: unknown } | null;
    preview!.innerHTML =
      `<pre class="mermaid-error">${escapeHtml(String((e && e.str) || (e && e.message) || e))}</pre>`;
    cleanupMermaidTemps();
  }
  finishSample();
  postResize();
}

// mermaid.render inserts a temporary <div id="d{id}"> into <body> on error and
// occasionally leaves it behind; sweep them so they don't skew the resize height.
function cleanupMermaidTemps(): void {
  const temps = document.querySelectorAll('body > div[id^="dmermaid-svg-"]');
  for (let i = 0; i < temps.length; i++) temps[i].remove();
}

function scheduleRender(): void {
  clearTimeout(renderTimer);
  renderTimer = setTimeout(renderDiagram, RENDER_DEBOUNCE_MS);
}

// ---------------------------------------------------------------------------
// Plugin → host events

function currentDoc(): { source: string } {
  return { source };
}

function scheduleDocChanged(): void {
  clearTimeout(docChangedTimer);
  docChangedTimer = setTimeout(emitDocChanged, DOC_CHANGED_DEBOUNCE_MS);
}

function scheduleAutosave(): void {
  clearTimeout(autosaveTimer);
  autosaveTimer = setTimeout(emitSave, AUTOSAVE_DEBOUNCE_MS);
}

function emitDocChanged(): void {
  if (!hasCap("document:write")) return;
  sendEvent("docChanged", { source });
}

function emitSave(): void {
  if (!hasCap("document:write")) return;
  sendEvent("save", { source, schemaVersion: SCHEMA_VERSION });
  dirty = false;
}

// Metrics emission (every METRIC_BATCH renders + on host requestSave).
function flushMetrics(): void {
  sendEvent("metric", {
    name: "render",
    p50: metrics.p50(),
    p99: metrics.p99(),
    count: metrics.count,
    samplesMs: metrics.samplesMs.slice(),
  });
}

metrics.onSample(() => {
  if (metrics.count > 0 && metrics.count % METRIC_BATCH === 0) flushMetrics();
});

// §8a: report what the frame actually resolved after applying the crossed tokens.
function emitThemeApplied(tokens: unknown): void {
  sendEvent("themeApplied", { scheme, sample: sampleAppliedTokens(tokens) });
}

// ---------------------------------------------------------------------------
// host → plugin handlers

function deriveSource(doc: unknown): string {
  if (doc == null) return "";
  if (typeof doc === "string") return doc;
  if (typeof doc === "object" && typeof (doc as { source?: unknown }).source === "string") {
    return (doc as { source: string }).source;
  }
  return "";
}

function handleInit(params: InitParams | undefined): void {
  const p = params || {};
  capabilities = new Set(Array.isArray(p.capabilities) ? p.capabilities : []);
  if (typeof p.scheme === "string") scheme = p.scheme;

  if (hasCap("theme:read")) {
    applyTokens(p.tokens);
    applyScheme(p.scheme);
  }
  configureMermaid(hasCap("theme:read") ? p.tokens : null);

  if (hasCap("document:read")) {
    source = deriveSource(p.doc);
  } else {
    source = "";
  }
  textarea!.value = source;
  textarea!.readOnly = !hasCap("document:write");

  if (p.schemaVersion && p.schemaVersion !== SCHEMA_VERSION) {
    console.warn(
      `[mermaid] schemaVersion mismatch: host=${p.schemaVersion} frame=${SCHEMA_VERSION}`
    );
  }

  initialized = true;
  // Initial render + theme report + height. Report theme AFTER applyTokens.
  if (hasCap("theme:read")) emitThemeApplied(p.tokens);
  renderDiagram();
}

function handleThemeChanged(params: ThemeChangedParams | undefined): void {
  const p = params || {};
  if (typeof p.scheme === "string") scheme = p.scheme;
  applyTokens(p.tokens);
  applyScheme(p.scheme);
  configureMermaid(p.tokens);
  emitThemeApplied(p.tokens);
  renderDiagram();
}

// requestSave is a REQUEST → must return {source, schemaVersion}.
function handleRequestSave(): { source: string; schemaVersion: string } {
  flushMetrics();
  return { source, schemaVersion: SCHEMA_VERSION };
}

// teardown is a REQUEST → return {} after a clean teardown (no leaked listeners).
function handleTeardown(): Record<string, never> {
  teardown();
  return {};
}

// ---------------------------------------------------------------------------
// Lifecycle + resize

function postResize(): void {
  if (!root) return;
  const h = Math.ceil(root.getBoundingClientRect().height);
  sendEvent("resize", { height: h });
}

function setupResizeObserver(): void {
  if (typeof ResizeObserver === "undefined" || !root) return;
  resizeObserver = new ResizeObserver(() => postResize());
  resizeObserver.observe(root);
}

// §8a: self-isolation probes, computed INSIDE the opaque frame at boot. Under
// sandbox="allow-scripts" (no allow-same-origin) each of these is blocked by the
// browser, so accessing them throws — which is exactly the third-party guarantee.
function isolationProbes(): { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } {
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

function announceReady(): void {
  sendEvent("ready", {
    version: PROTOCOL_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: READY_MIN_HEIGHT,
    probes: isolationProbes(), // §8a — host stashes these on the iframe element
  });
}

function teardown(): void {
  if (dirty) emitSave(); // flush before teardown (SPA nav within the debounce)
  clearTimeout(renderTimer);
  clearTimeout(docChangedTimer);
  clearTimeout(autosaveTimer);
  renderTimer = undefined;
  docChangedTimer = undefined;
  autosaveTimer = undefined;
  if (resizeObserver) {
    resizeObserver.disconnect();
    resizeObserver = null;
  }
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
  initialized = false;
}

// ---------------------------------------------------------------------------
// Boot

function onInput(): void {
  source = textarea!.value;
  dirty = true;
  scheduleDocChanged();
  scheduleAutosave(); // the save event was previously never emitted (dead path)
  scheduleRender();
}

function boot(): void {
  try {
    root = document.querySelector<HTMLElement>(ROOT_SELECTOR);
    textarea = document.querySelector<HTMLTextAreaElement>(TEXTAREA_SELECTOR);
    preview = document.querySelector<HTMLElement>(PREVIEW_SELECTOR);
    if (!root || !textarea || !preview) {
      console.error("[mermaid] mount roots not found");
      return;
    }

    const handlers: HandlerMap = {
      init: handleInit,
      themeChanged: handleThemeChanged,
      requestSave: handleRequestSave,
      teardown: handleTeardown,
    };
    messageListener = createRouter(handlers);
    window.addEventListener("message", messageListener);

    textarea.addEventListener("input", onInput);
    textarea.addEventListener("keydown", (e) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === "s" || e.key === "S")) {
        e.preventDefault();
        clearTimeout(autosaveTimer);
        emitSave();
      }
    });
    textarea.addEventListener("focus", () => sendEvent("focusChanged", { focused: true }));
    textarea.addEventListener("blur", () => sendEvent("focusChanged", { focused: false }));

    setupResizeObserver();
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
    } catch (e2) {
      /* postMessage itself failed — nothing more we can do */
    }
  }
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
