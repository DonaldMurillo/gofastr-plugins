// GoFastr whiteboard — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage, or
// DOM, and never the network (the framed CSP sets connect-src 'none', which
// is the exfiltration guard the whole isolation design rests on).
//
// Boot order is load-bearing: the network probe wraps every outbound web API
// BEFORE any other module runs, so "the frame issued no network request" is
// an assertion with teeth for the entire session, not just after ready.
//
// Handshake: ready → init{tokens, capabilities}. With sync:room granted, the
// host adapter opens the room (SSE + publish) and drives this frame with
// syncApply / presenceApply / syncStatus; the frame answers syncSnapshot with
// its full CRDT state on (re)connect so offline edits reach everyone.

import { createRouter, sendEvent, rejectAllPending } from "./protocol";
import { applyScheme, applyTokens, sampleAppliedTokens } from "./theme";
import {
  SCHEMA_VERSION,
  applySyncStatus,
  computeProbes,
  debugApi,
  handlePresenceApply,
  handleSyncApply,
  mountBoard,
  setCanSync,
  snapshotState,
  teardownBoard,
} from "./board";

// --- the network probe: assert, do not assume ----------------------------------
//
// The CSP already forbids connections; this makes an ATTEMPT observable from
// the parent (Playwright can evaluate inside the opaque frame). Any call to
// fetch / XHR / EventSource / WebSocket / sendBeacon lands in __wbNetProbe
// and throws — the demo page and the e2e suite read the list and expect it
// empty after a full collaborative session.
const netAttempts: string[] = [];
(window as unknown as Record<string, unknown>).__wbNetProbe = { attempts: netAttempts };

function refuseNet(name: string): (...args: unknown[]) => never {
  return function refused(..._args: unknown[]): never {
    netAttempts.push(name);
    throw new Error(`whiteboard frame: ${name} blocked — connect-src 'none', the frame has no network`);
  };
}
const w = window as unknown as Record<string, unknown>;
w.fetch = refuseNet("fetch");
w.XMLHttpRequest = refuseNet("XMLHttpRequest");
w.EventSource = refuseNet("EventSource");
w.WebSocket = refuseNet("WebSocket");
if (typeof navigator !== "undefined" && "sendBeacon" in navigator) {
  (navigator as unknown as Record<string, unknown>).sendBeacon = refuseNet("sendBeacon");
}

// --- debug hooks for the demo mirrors and the e2e suite -------------------------

(window as unknown as Record<string, unknown>).__wbDebug = debugApi();

// --- protocol wiring ------------------------------------------------------------

const SAMPLE_TOKENS = ["--color-surface", "--color-text", "--color-border"];

const router = createRouter({
  init(params) {
    const p = params as { tokens?: unknown; scheme?: unknown; capabilities?: unknown } | null;
    applyTokens(p && p.tokens);
    applyScheme(p && p.scheme);
    const caps = Array.isArray(p && p.capabilities) ? (p!.capabilities as string[]) : [];
    setCanSync(caps.includes("sync:room"));
    mountBoard();
    sendEvent("themeApplied", { scheme: p && p.scheme, sample: sampleAppliedTokens(p && p.tokens, SAMPLE_TOKENS) });
  },
  themeChanged(params) {
    const p = params as { tokens?: unknown; scheme?: unknown } | null;
    applyTokens(p && p.tokens);
    applyScheme(p && p.scheme);
    sendEvent("themeApplied", { scheme: p && p.scheme, sample: sampleAppliedTokens(p && p.tokens, SAMPLE_TOKENS) });
  },
  syncApply: (params) => handleSyncApply(params),
  presenceApply: (params) => handlePresenceApply(params),
  syncStatus: (params) => applySyncStatus(params as Parameters<typeof applySyncStatus>[0]),
  syncSnapshot() {
    // Host→frame request: the reconnect handshake needs the frame's full CRDT
    // state so offline edits can be published to the room. Crosses as an
    // ArrayBuffer via structured clone.
    return { state: snapshotState() };
  },
  teardown() {
    teardownBoard();
    rejectAllPending({ code: "E_TEARDOWN", message: "frame torn down" });
    return {};
  },
});

window.addEventListener("message", router);

sendEvent("ready", {
  version: "0.1.0",
  schemaVersion: SCHEMA_VERSION,
  minHeight: 480,
  probes: computeProbes(),
});
