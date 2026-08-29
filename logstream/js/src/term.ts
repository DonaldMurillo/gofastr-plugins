// GoFastr log stream — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage, or
// DOM, and never fetch (the framed CSP sets connect-src 'none'). The
// direction is the point: every other plugin's frame ASKS for data; this one
// is PUSHED at. The host sends unsolicited `streamBatch` events
// {first, last, lines[], dropped}; this frame renders them into xterm.js and
// answers each rendered batch with a `streamAck` {lastSeq, rendered,
// scrollback, cap}. The host holds a bounded window of unacknowledged
// batches and drops from the oldest end of its buffer when the producer
// outruns this frame — so the ack cadence IS the bridge's backpressure.
//
// Consumption policy (the declared, documented rate): at most ONE batch per
// 16 ms scheduler tick (~60 batches/s), so a burst cannot monopolise the
// frame's main thread. A producer faster than that overflows the host's
// window, and the overflow becomes a visible "N lines dropped" marker here
// — never a silent gap.
//
// Scrollback is BOUNDED at SCROLLBACK_LINES (10,000): a frame that never
// forgets would eventually hold everything the host ever sent, and the
// streaming claim would die with it. The bound is published in every ack
// next to the live depth, so the demo page and the e2e suite can watch it.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { createRouter, rejectAllPending, sendEvent } from "./protocol";
import { applyScheme, applyTokens, sampleAppliedTokens, termTheme } from "./theme";

const SCHEMA_VERSION = "logstream-v1";
/** The scrollback bound — docs/logstream.md publishes this number. */
export const SCROLLBACK_LINES = 10_000;
/** One batch per tick: the frame's declared consumption rate (~60/s). */
const DRAIN_TICK_MS = 16;
/**
 * Ceiling on LINES written per tick, independent of how many arrived.
 *
 * One batch per tick bounds delivery, not rendering cost. At 6,000 lines/s a
 * batch is ~100 lines, and parsing plus painting that every 16ms starves the
 * main thread on a small machine: on CI's webkit the frame's own Pause button
 * stopped responding entirely — Playwright could not complete an actionability
 * check on it in 90 seconds (#40). The one control that stops a flood was the
 * one the flood made unreachable.
 *
 * Roughly a screenful. Lines beyond it stay queued and are written next tick,
 * so nothing is silently discarded here and the ack keeps meaning "written to
 * the terminal". If the producer stays ahead, the host's own bounded buffer
 * fills and IT drops and counts, which is the designed backpressure and is
 * already reported honestly in the drop marker and the telemetry counter.
 */
const MAX_LINES_PER_TICK = 24;
const SAMPLE_TOKENS = ["--color-surface", "--color-text", "--color-border"];

/** One host-pushed batch, narrowed from the untrusted postMessage payload. */
interface StreamBatch {
  first: number;
  last: number;
  lines: { seq: number; text: string }[];
  dropped: number;
  droppedTotal: number;
}

// --- runtime state (module-scoped; single instance per frame) ---------------
let root: HTMLElement | null = null;
let termEl: HTMLElement | null = null;
let searchInput: HTMLInputElement | null = null;
let searchStatus: HTMLElement | null = null;
let countEl: HTMLElement | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;
let drainTimer: number | undefined;

let term: Terminal | null = null;
let fit: FitAddon | null = null;
let search: SearchAddon | null = null;
let resizeObserver: ResizeObserver | null = null;

let initialized = false;
let lastTokens: unknown = null;
/** Batches accepted from the host, not yet rendered. */
const pending: StreamBatch[] = [];
/** Drop markers actually WRITTEN to the terminal, and the most recent one.
 *  These ride on the ack so a test can assert the gap was recorded without
 *  driving the UI: at flood rate CI's webkit cannot reliably service a click
 *  or a fill, so any assertion routed through the search box measures the
 *  runner rather than the plugin. */
let markersWritten = 0;
/** Out-of-band gap notices RECEIVED. Reported on every ack so a failure can
 *  tell "the notice never arrived" from "it arrived and wrote no marker" —
 *  the two have completely different causes and the CI symptom is identical. */
let dropEventsSeen = 0;
let lastMarkerText = "";
/** Highest cumulative drop total already written as a marker. The host reports
 *  a gap TWICE on purpose — once out of band the moment it happens, once on
 *  the next batch for a frame that missed the event — so the marker is written
 *  for whichever arrives first and skipped for the other. */
let droppedTotalMarked = 0;
/** The pending queue's bound: a stalled frame must not hoard host memory. */
const MAX_PENDING_BATCHES = 240;
/** Highest sequence number actually written to the terminal. */
let lastRendered = 0;
/** Total lines written (markers excluded). */
let rendered = 0;
let drainScheduled = false;

/** Narrow an untrusted postMessage params object to a string-keyed record. */
function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

/** Out-of-band gap notice: write the marker now, ahead of the batch that will
 *  also carry the count. Whichever arrives first wins; the other is skipped. */
function handleStreamDropped(params: unknown): void {
  const p = params as { dropped?: unknown; total?: unknown } | null;
  if (!p || typeof p.dropped !== "number" || typeof p.total !== "number") return;
  dropEventsSeen += 1;
  if (!term || p.dropped <= 0 || p.total <= droppedTotalMarked) return;
  const marker = dropMarker(p.dropped);
  markersWritten += 1;
  lastMarkerText = marker.replace(/[\r\n]+$/, "");
  droppedTotalMarked = p.total;
  term.write(marker, () => {
    updateCount();
    sendEvent("streamAck", ackParams());
  });
}

/** Parse + bound one host-pushed streamBatch; null means "drop it silently"
 * (malformed payloads must not corrupt the render loop). */
function parseBatch(raw: unknown): StreamBatch | null {
  const p = asRecord(raw);
  const rawLines = Array.isArray(p.lines) ? p.lines : [];
  const lines: { seq: number; text: string }[] = [];
  for (const item of rawLines) {
    const l = asRecord(item);
    if (typeof l.seq !== "number" || typeof l.text !== "string") continue;
    lines.push({ seq: l.seq, text: l.text });
  }
  if (lines.length === 0) return null;
  return {
    first: typeof p.first === "number" ? p.first : lines[0].seq,
    last: typeof p.last === "number" ? p.last : lines[lines.length - 1].seq,
    lines,
    dropped: typeof p.dropped === "number" && p.dropped > 0 ? Math.floor(p.dropped) : 0,
    droppedTotal:
      typeof p.droppedTotal === "number" && p.droppedTotal > 0 ? Math.floor(p.droppedTotal) : 0,
  };
}

function updateCount(): void {
  if (!countEl || !term) return;
  // Retained history (buffer minus viewport rows) against the published
  // bound — the same number the acks carry, so the toolbar, the demo page's
  // telemetry and the e2e all read one accounting.
  const retained = Math.max(0, term.buffer.active.length - term.rows);
  countEl.textContent = `${rendered.toLocaleString("en-US")} · sb ${retained.toLocaleString("en-US")}/${SCROLLBACK_LINES.toLocaleString("en-US")}`;
}

/** The drop marker: a gap the user can SEE. ANSI bright-yellow on default. */
function dropMarker(dropped: number): string {
  return `\u001b[1;33m⋯ ${dropped.toLocaleString("en-US")} lines dropped — producer outran the render loop ⋯\u001b[0m\r\n`;
}

/**
 * Drain exactly ONE pending batch: write marker + lines, then ack. Writing in
 * a single string keeps a marker atomic with the lines that follow it, and
 * acking from the write callback means "written to the terminal", not merely
 * "parsed by us" — the ack stays truthful.
 */
function drainOne(): void {
  const batch = pending.shift();
  if (!batch || !term) return;
  let out = "";
  if (batch.dropped > 0 && batch.droppedTotal > droppedTotalMarked) {
    const marker = dropMarker(batch.dropped);
    out += marker;
    markersWritten += 1;
    lastMarkerText = marker.replace(/[\r\n]+$/, "");
    droppedTotalMarked = batch.droppedTotal;
  }
  let written = 0;
  let i = 0;
  for (; i < batch.lines.length; i += 1) {
    const line = batch.lines[i];
    if (line.seq <= lastRendered) continue; // reconnect dedup: already written
    if (written >= MAX_LINES_PER_TICK) break;
    out += `${line.text}\r\n`;
    lastRendered = line.seq;
    rendered += 1;
    written += 1;
  }
  if (i < batch.lines.length) {
    // Over the per-tick ceiling: the rest goes back at the FRONT, in order, and
    // is written next tick. dropped is zeroed because its marker is already in
    // `out` — re-emitting it would double-count a gap that happened once.
    pending.unshift({ ...batch, lines: batch.lines.slice(i), dropped: 0 });
  }
  if (out !== "") {
    term.write(out, () => {
      updateCount();
      sendEvent("streamAck", ackParams());
    });
  } else {
    // Entirely deduped (reconnect overlap): still ack so the host's window
    // drains — silence here would wedge the flight window forever.
    sendEvent("streamAck", ackParams());
  }
}

function ackParams(): Record<string, unknown> {
  // Retained history = buffer length minus the viewport rows xterm keeps
  // live. xterm trims the buffer to scrollback + rows, so the retained
  // count is the honest "how much old log does the frame still hold"
  // against the cap — and the e2e asserts exactly that inequality.
  const rows = term ? term.rows : 0;
  const scrollback = term ? Math.max(0, term.buffer.active.length - rows) : 0;
  return {
    lastSeq: lastRendered,
    rendered,
    markers: markersWritten,
    lastMarker: lastMarkerText,
    dropEvents: dropEventsSeen,
    scrollback,
    rows,
    cap: SCROLLBACK_LINES,
  };
}

/** The drain loop: one batch per tick, only while work is pending. The tick
 * cadence is the declared consumption rate — see the file header. */
function scheduleDrain(): void {
  if (drainScheduled || pending.length === 0) return;
  drainScheduled = true;
  drainTimer = window.setTimeout(() => {
    drainScheduled = false;
    drainTimer = undefined;
    drainOne();
    scheduleDrain();
  }, DRAIN_TICK_MS);
}

function handleStreamBatch(params: unknown): void {
  const batch = parseBatch(params);
  if (!batch) return;
  if (pending.length >= MAX_PENDING_BATCHES) {
    // The frame-side bound: the host's window already throttles us to ~60
    // batches/s, so hitting this means acks stopped (throttled tab). Drop
    // the OLDEST pending batch and fold its lines into a marker so the gap
    // stays visible when rendering resumes.
    const oldest = pending.shift();
    if (oldest) {
      const next = pending[0];
      if (next) next.dropped += oldest.lines.length;
      sendEvent("streamAck", ackParams()); // free the host's slot immediately
    }
  }
  pending.push(batch);
  scheduleDrain();
}

// --- search -----------------------------------------------------------------

function runSearch(): void {
  if (!search || !searchInput) return;
  const query = searchInput.value;
  if (query === "") {
    if (searchStatus) searchStatus.textContent = "";
    search.clearDecorations();
    return;
  }
  const cs = getComputedStyle(document.documentElement);
  // Highlight colours come from the bridged tokens (applied to :root by
  // theme.ts), with the same values the pre-init fallbacks carry — no
  // bespoke hex in the frame.
  const primary = cs.getPropertyValue("--color-primary").trim() || "#e0a040";
  const primaryStrong = cs.getPropertyValue("--color-accent").trim() || primary;
  const found = search.findNext(query, {
    decorations: {
      matchBackground: primary,
      matchOverviewRuler: primary,
      activeMatchBackground: primaryStrong,
      activeMatchColorOverviewRuler: primaryStrong,
    },
  });
  // Report honestly: the addon only says whether a match exists, not how
  // many — a count would require walking the whole buffer ourselves.
  if (searchStatus) searchStatus.textContent = found ? "match shown ↑" : "no matches";
}

function onSearchKey(ev: KeyboardEvent): void {
  if (ev.key === "Enter") {
    ev.preventDefault();
    runSearch();
  } else if (ev.key === "Escape") {
    if (searchInput) searchInput.value = "";
    if (searchStatus) searchStatus.textContent = "";
    search?.clearDecorations();
  }
}

// --- host → plugin handlers ---------------------------------------------------

function handleInit(params: unknown): void {
  if (initialized) return;
  initialized = true;
  const p = asRecord(params);
  lastTokens = p.tokens;
  applyTokens(p.tokens);
  applyScheme(typeof p.scheme === "string" ? p.scheme : "light");
  if (term) {
    term.options.theme = termTheme(lastTokens);
    const mono = sampleAppliedTokens(p.tokens, ["--font-mono"])["--font-mono"];
    if (mono) term.options.fontFamily = mono;
    fit?.fit();
  }
  sendEvent("themeApplied", { scheme: p.scheme, sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS) });
  updateCount();
}

function handleThemeChanged(params: unknown): void {
  const p = asRecord(params);
  lastTokens = p.tokens;
  applyTokens(p.tokens);
  applyScheme(p.scheme);
  if (term) term.options.theme = termTheme(lastTokens);
  sendEvent("themeApplied", { scheme: p.scheme, sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS) });
}

// requestSave is a REQUEST → must answer. A log tail has no document; the
// honest answer is an empty doc.
function handleRequestSave(): Record<string, unknown> {
  return { doc: null, schemaVersion: SCHEMA_VERSION };
}

// teardown is a REQUEST → return {} after a clean teardown (no leaked
// listeners/timers, nothing left pending).
function handleTeardown(): Record<string, never> {
  teardown();
  return {};
}

// ---------------------------------------------------------------------------
// Lifecycle

// §8a: self-isolation probes, computed INSIDE the opaque frame at boot. Under
// sandbox="allow-scripts" (no allow-same-origin) each of these is blocked by
// the browser, so accessing them throws — which is exactly the third-party
// guarantee.
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
    minHeight: 520,
    probes: isolationProbes(),
  });
}

function teardown(): void {
  window.clearTimeout(drainTimer);
  if (resizeObserver) {
    resizeObserver.disconnect();
    resizeObserver = null;
  }
  if (term) {
    term.dispose();
    term = null;
  }
  rejectAllPending({ code: "E_TEARDOWN", message: "frame torn down" });
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
}

function boot(): void {
  root = document.getElementById("logstream-root");
  termEl = document.getElementById("logstream-term");
  searchInput = document.getElementById("ls-search") as HTMLInputElement | null;
  searchStatus = document.getElementById("ls-search-status");
  countEl = document.getElementById("ls-count");
  if (!root || !termEl) return;

  fit = new FitAddon();
  search = new SearchAddon();
  term = new Terminal({
    scrollback: SCROLLBACK_LINES,
    // The search/fit addons use xterm's proposed (addon-facing) APIs; the
    // option is the explicit opt-in xterm requires so proposed surface can
    // never leak in by accident. stdin stays disabled — read-only terminal.
    allowProposedApi: true,
    // A concrete stack xterm can measure char cells against. The var()
    // form of the bridged --font-mono token cannot resolve inside xterm's
    // own measurement context, so the boot default mirrors the token's
    // fallback and handleInit re-points it at the bridged value.
    fontFamily:
      "ui-monospace, SFMono-Regular, 'JetBrains Mono', Menlo, Consolas, monospace",
    fontSize: 12,
    cursorBlink: false,
    disableStdin: true,
    convertEol: false,
    theme: termTheme(null),
  });
  term.loadAddon(fit);
  term.loadAddon(search);
  term.open(termEl);
  fit.fit();
  // Keep the grid honest when the host resizes the iframe.
  resizeObserver = new ResizeObserver(() => fit?.fit());
  resizeObserver.observe(termEl);

  if (searchInput) searchInput.addEventListener("keydown", onSearchKey);

  messageListener = createRouter({
    init: handleInit,
    themeChanged: handleThemeChanged,
    requestSave: handleRequestSave,
    teardown: handleTeardown,
    // The push channel: unsolicited host→frame batches carrying the lines.
    // Everything else (resize / focusChanged / hostPointerdown / bootError)
    // needs no action.
    streamBatch: handleStreamBatch,
    // The gap notice, sent the moment the host drops rather than queued
    // behind data. Under backpressure the carrying batch can be many seconds
    // behind the drop it reports, which made "never a silent gap" untrue in
    // exactly the moment it mattered.
    streamDropped: handleStreamDropped,
  });
  window.addEventListener("message", messageListener);
  updateCount();
  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
