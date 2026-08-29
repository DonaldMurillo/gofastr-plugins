// GoFastr genui — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage, or
// DOM, and never fetch (the framed CSP sets connect-src 'none').
//
// The model runs in Go, never here. A composition is produced host-side,
// validated host-side, and arrives over the bridge already checked — and this
// frame validates it AGAIN against its own compiled-in registry before
// rendering anything (validate.ts): the registry is the entire containment
// story, and the frame's copy of the rules is what enforces it at the last
// inch. The frame holds no key, opens no socket, and cannot exfiltrate the
// document it is composing — an API key in a browser is not a key, and a frame
// that could call a model could send it anything.
//
// Every registry component is statically imported and bundled (React included)
// — no dynamic import, no React.lazy, nothing loaded at runtime. What was
// compiled in is all that can ever render.
//
// Flow: the host starts a generation (POST /compose) and this frame is told
// `composePending`; the host polls, and when the composition is ready it
// arrives whole as one `composition` event — no streaming tokens into the DOM.
// A generated Button does nothing itself: the click emits
// `uiAction {action, nodeId}` and the host, which supplied the allow-list the
// action was validated against, decides what it means.
//
// States (window.__genuiDebug.state()): idle (nothing composed yet), pending
// (placeholder skeleton while the host generates), rendered, refused (this
// frame's validator rejected the tree — displayed loudly, with the reason: a
// refused composition is a feature demonstration, not an error to hide), and
// failed (the host's generation itself failed). Refusals never touch the
// console: the e2e suite asserts zero console errors INCLUDING on the refused
// path.

import { createElement } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { createRouter, rejectAllPending, sendEvent } from "./protocol";
import { REGISTRY, REGISTRY_IDS } from "./registry";
import { applyScheme, applyTokens } from "./theme";
import { SCHEMA_VERSION, validateComposition, type CompositionNode } from "./validate";

// The adapter registers 460px in its manifest; the frame announces the same
// number so the two never disagree about the cage's minimum size.
const MIN_HEIGHT = 460;

// --- runtime state (module-scoped; single instance per frame) -----------------
type FrameState = "idle" | "pending" | "rendered" | "refused" | "failed";

let root: Root | null = null;
let container: HTMLElement | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;
let initialized = false;

let state: FrameState = "idle";
/** The composition currently rendered (validated). */
let rendered: { root: CompositionNode; nodeCount: number } | null = null;
/** The raw tree of the last ACCEPTED composition — what composition() hands
 *  back. Refused trees are already fully described by lastError. */
let lastAcceptedTree: unknown = null;
/** Last refusal or failure reason, null while everything has succeeded. */
let lastError: string | null = null;
/** The host's action allow-list (init.config.actions). Empty until init: a
 *  composition carrying an action the host never named is refused, so before
 *  init NO action is nameable. */
let allowedActions = new Set<string>();
/** The host's registry ids from init.config.registry, for the debug mismatch
 *  check — the authoritative agreement assertion is the host's, on `ready`. */
let hostRegistryIds: readonly string[] | null = null;

/** Narrow an untrusted postMessage params object to a string-keyed record. */
function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

function stringArray(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

// --- rendering ------------------------------------------------------------------

/** The validated tree → React elements. The registry lookup cannot miss post-
 *  validation (REGISTRY is const and validateComposition checked every
 *  component name); the null is for the compiler, not the runtime. Children
 *  ride as React children — createElement's variadic argument, never a prop. */
function renderNode(node: CompositionNode): ReactNode {
  const entry = REGISTRY[node.component];
  if (!entry) return null;
  const props: Record<string, unknown> = { ...node.props, nodeId: node.nodeId };
  if (node.action !== undefined) {
    // Button only (the validator guarantees it): the click crosses the bridge
    // as an id pair; the component itself does nothing.
    props.onActivate = () => sendEvent("uiAction", { action: node.action, nodeId: node.nodeId });
  }
  const children = node.children?.map((child) => renderNode(child)) ?? [];
  return createElement(entry.component, props, ...children);
}

function IdleView(): ReactNode {
  return <p className="gu-idle">Nothing composed yet — the host will send a composition.</p>;
}

function PendingView(): ReactNode {
  return (
    <div className="gu-skeleton" role="status" aria-label="Composing a view">
      <div className="gu-sk gu-sk-title"></div>
      <div className="gu-sk"></div>
      <div className="gu-sk gu-sk-short"></div>
      <div className="gu-sk gu-sk-block"></div>
    </div>
  );
}

/** Refusal and failure share one notice shape: a loud panel, a plain-language
 *  lead, and the machine reason verbatim — the refusal IS the demo. */
function Notice({
  tone,
  title,
  lead,
  reason,
  refused,
}: { tone: "bad" | "warn"; title: string; lead: string; reason: string; refused?: boolean }): ReactNode {
  return (
    <div className="gu-notice" data-tone={tone} data-genui-refused={refused ? "" : undefined} role="alert">
      <div className="gu-notice-title">{title}</div>
      <p className="gu-notice-lead">{lead}</p>
      <code className="gu-notice-reason">{reason}</code>
    </div>
  );
}

function currentBody(): ReactNode {
  switch (state) {
    case "idle":
      return <IdleView />;
    case "pending":
      return <PendingView />;
    case "rendered":
      return rendered ? renderNode(rendered.root) : <IdleView />;
    case "refused":
      return (
        <Notice
          tone="bad"
          refused
          title="Composition refused"
          lead="The frame's validator rejected this composition, so none of it was rendered."
          reason={lastError ?? "unknown reason"}
        />
      );
    case "failed":
      return (
        <Notice
          tone="warn"
          title="Generation failed"
          lead="The host could not produce a composition."
          reason={lastError ?? "unknown error"}
        />
      );
  }
}

/** One render per state change. Compositions are bounded (≤200 nodes) and
 *  events are rare, so re-rendering the whole frame per event is the boring,
 *  honest option — React reconciles the DOM transition. */
function render(): void {
  root?.render(currentBody());
}

// --- host → plugin handlers -----------------------------------------------------

function handleInit(params: unknown): void {
  if (initialized) return;
  initialized = true;
  const p = asRecord(params);
  applyTokens(p.tokens);
  applyScheme(typeof p.scheme === "string" ? p.scheme : "light");
  const config = asRecord(p.config);
  allowedActions = new Set(stringArray(config.actions));
  hostRegistryIds = stringArray(config.registry);
  render();
}

function handleThemeChanged(params: unknown): void {
  const p = asRecord(params);
  applyTokens(p.tokens);
  applyScheme(p.scheme);
}

/** One whole composition from the host. Validate FIRST, render only on ok,
 *  and answer every composition with a renderResult — the host's e2e seam. */
function handleComposition(params: unknown): void {
  const p = asRecord(params);
  const id = typeof p.id === "string" ? p.id : "";
  const result = validateComposition(p.tree, allowedActions);
  if (result.ok) {
    state = "rendered";
    rendered = result;
    lastError = null;
    lastAcceptedTree = p.tree;
    sendEvent("renderResult", { id, ok: true, nodeCount: result.nodeCount });
  } else {
    state = "refused";
    rendered = null;
    lastError = result.error;
    sendEvent("renderResult", { id, ok: false, error: result.error });
  }
  render();
}

function handleComposePending(params: unknown): void {
  const p = asRecord(params);
  state = "pending";
  if (typeof p.id === "string" && p.id !== "") lastError = null;
  render();
}

function handleComposeFailed(params: unknown): void {
  const p = asRecord(params);
  const id = typeof p.id === "string" ? p.id : "";
  const why = typeof p.error === "string" ? p.error : JSON.stringify(p.error) ?? String(p.error);
  state = "failed";
  rendered = null;
  // The id is optional on the wire (the adapter's pushes carry none); the
  // reason must not read "compose : …" when it is absent.
  lastError = `${id ? `compose ${id}` : "compose"}: ${why}`;
  render();
}

// teardown is a REQUEST → return {} after a clean teardown (listener off,
// React tree unmounted, nothing left pending).
function handleTeardown(): Record<string, never> {
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
  root?.unmount();
  root = null;
  rejectAllPending({ code: "E_TEARDOWN", message: "frame torn down" });
  return {};
}

// ---------------------------------------------------------------------------
// Lifecycle

// Self-isolation probes, computed INSIDE the opaque frame at boot (the
// scanner/logstream pattern). Under sandbox="allow-scripts" (no
// allow-same-origin) each of these is blocked by the browser, so accessing
// them throws — which is exactly the third-party guarantee.
function isolationProbes(): { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } {
  let cookieEmpty = false;
  let parentBlocked = false;
  let storageBlocked = false;
  try {
    cookieEmpty = document.cookie === "";
  } catch {
    cookieEmpty = true;
  }
  try {
    void (window.parent as unknown as { document?: unknown }).document;
  } catch {
    parentBlocked = true;
  }
  try {
    void window.localStorage.length;
  } catch {
    storageBlocked = true;
  }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

function announceReady(): void {
  sendEvent("ready", {
    version: SCHEMA_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: MIN_HEIGHT,
    probes: isolationProbes(),
    // The compiled-in registry: the host asserts its copy agrees with this
    // one, because every composition is validated against the frame's.
    registry: REGISTRY_IDS.slice(),
  });
}

/**
 * In-frame debug hooks (the logstream/scanner pattern): the e2e suite reads
 * these through the frame's own window. The contract's four first — state,
 * nodeCount, lastError, composition — then the registry pair a test needs to
 * explain a refusal caused by registry skew.
 */
function publishDebug(): void {
  (window as unknown as Record<string, unknown>).__genuiDebug = {
    state: (): FrameState => state,
    nodeCount: (): number => (state === "rendered" ? (rendered?.nodeCount ?? 0) : 0),
    lastError: (): string | null => lastError,
    composition: (): unknown => lastAcceptedTree,
    // The compiled-in registry ids, and whether init's host registry agreed
    // with them. Null before init.
    registry: (): string[] => REGISTRY_IDS.slice(),
    registryAgrees: (): boolean | null => {
      if (hostRegistryIds === null) return null;
      const host = new Set(hostRegistryIds);
      return host.size === REGISTRY_IDS.length && REGISTRY_IDS.every((id) => host.has(id));
    },
  };
}

function boot(): void {
  container = document.getElementById("genui-root");
  if (!container) return;
  root = createRoot(container);

  publishDebug();
  messageListener = createRouter({
    init: handleInit,
    themeChanged: handleThemeChanged,
    composition: handleComposition,
    composePending: handleComposePending,
    composeFailed: handleComposeFailed,
    teardown: handleTeardown,
  });
  window.addEventListener("message", messageListener);
  render();
  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
