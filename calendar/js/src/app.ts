// GoFastr calendar — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage, or
// DOM, and never fetch (the framed CSP sets connect-src 'none'). On load it
// announces `ready`; the host replies `init` carrying the view-state doc
// (view state ONLY — the frame never receives events or rules), the theme
// tokens, and the granted capabilities.
//
// The frame's contract with the host, in one paragraph: it asks for
// resolved occurrences per visible window, renders the strings it is given
// (wall clock for layout, UTC instants for display), and when the user
// moves an event it sends an INTENT — a wall-clock delta measured on the
// grid. The server re-resolves the intent through the event's zone and
// answers with what actually happened; the frame renders the answer and
// publishes the numbers for the host page's readout. It never computes a
// recurrence, never detects a conflict, never guesses a zone.

import { createRouter, rejectAllPending, sendEvent } from "./protocol";
import { applyScheme, applyTokens, sampleAppliedTokens } from "./theme";
import {
  handleBridgeResult,
  rejectAllPending as rejectAllBridge,
  requestOccurrences,
  requestMove,
  type Occurrence,
  type Transition,
} from "./bridge";
import {
  KEYBOARD_MOVE_MINUTES,
  dayWindow,
  monthDateButton,
  monthWindow,
  moveMonthFocus,
  renderMonth,
  renderTimeGrid,
  visibleDays,
  weekWindow,
  type MonthState,
} from "./views";
import {
  addDays,
  dayTitle,
  fmtMinute,
  monthTitle,
  parseWall,
  startOfMonth,
  weekTitle,
} from "./dates";

const SCHEMA_VERSION = "calendar-v1";
const DOC_CHANGED_DEBOUNCE_MS = 500;
const AUTOSAVE_DEBOUNCE_MS = 1200;
const SAMPLE_TOKENS = ["--color-surface", "--color-text", "--color-border"];

type Mode = "month" | "week" | "day";

// --- runtime state (module-scoped; single instance per frame) ---------------

let viewEl: HTMLElement | null = null;
let titleEl: HTMLElement | null = null;
let zoneEl: HTMLElement | null = null;
let dstEl: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

let mode: Mode = "week";
let anchor = "2026-03-08";
const monthState: MonthState = { focusDay: anchor };
let today = localToday();

let occs: Occurrence[] = [];
let transitions: Transition[] = [];
let zone = "";
let loadedFrom = "";
let loadedTo = "";
let loadToken = 0;

let initialized = false;
let canWrite = false;
let docChangedTimer: number | undefined;
let autosaveTimer: number | undefined;
let popover: HTMLElement | null = null;

/** The frame's own clock, for the cosmetic is-today highlight only. */
function localToday(): string {
  const d = new Date();
  const p2 = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`;
}

function hasCap(list: string[], name: string): boolean {
  return list.includes(name);
}

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

function setStatus(text: string): void {
  if (statusEl) statusEl.textContent = text;
}

// --- view state plumbing ------------------------------------------------------

function currentDoc(): Record<string, unknown> {
  return {
    schemaVersion: SCHEMA_VERSION,
    view: { date: anchor, mode },
  };
}

function scheduleDocSync(): void {
  window.clearTimeout(docChangedTimer);
  docChangedTimer = window.setTimeout(() => {
    sendEvent("docChanged", { doc: currentDoc(), dirty: true, rev: Date.now() });
  }, DOC_CHANGED_DEBOUNCE_MS);
  window.clearTimeout(autosaveTimer);
  autosaveTimer = window.setTimeout(() => {
    sendEvent("save", { doc: currentDoc(), schemaVersion: SCHEMA_VERSION });
  }, AUTOSAVE_DEBOUNCE_MS);
}

function windowFor(m: Mode, a: string): { from: string; to: string } {
  if (m === "month") return monthWindow(a);
  if (m === "week") return weekWindow(a);
  return dayWindow(a);
}

/** Shift the anchor by one period in the current mode. */
function shiftAnchor(delta: number): void {
  if (mode === "day") {
    anchor = addDays(anchor, delta);
  } else if (mode === "week") {
    anchor = addDays(anchor, delta * 7);
  } else {
    anchor = shiftMonth(anchor, delta);
  }
  monthState.focusDay = anchor;
}

/** Month arithmetic on the anchor: keep the day, clamp into the target month. */
function shiftMonth(day: string, delta: number): string {
  const y = Number(day.slice(0, 4));
  const m = Number(day.slice(5, 7));
  const d = Number(day.slice(8, 10));
  const idx = y * 12 + (m - 1) + delta;
  const ny = Math.floor(idx / 12);
  const nm = (idx % 12) + 1;
  const dim = new Date(Date.UTC(ny, nm, 0)).getUTCDate();
  const p2 = (n: number) => String(n).padStart(2, "0");
  return `${ny}-${p2(nm)}-${p2(Math.min(d, dim))}`;
}

// --- data loading --------------------------------------------------------------

async function ensureData(force = false): Promise<void> {
  const w = windowFor(mode, anchor);
  const covered = !force && loadedFrom !== "" && w.from >= loadedFrom && w.to <= loadedTo;
  if (covered) return;
  const token = ++loadToken;
  setStatus("asking the server for this window…");
  try {
    const res = await requestOccurrences(w.from, w.to);
    if (token !== loadToken) return; // a newer window superseded this answer
    occs = res.occurrences;
    transitions = res.transitions;
    zone = res.zone;
    loadedFrom = res.from;
    loadedTo = res.to;
    const conflictCount = res.conflicts.length;
    sendEvent("occCount", {
      count: occs.length,
      conflicts: conflictCount,
      zone,
      from: res.from,
      to: res.to,
    });
    setStatus(`${occs.length} occurrences resolved in Go${conflictCount ? ` · ${conflictCount} conflict${conflictCount === 1 ? "" : "s"} flagged` : ""}`);
    render();
  } catch (err) {
    const e = err as { code?: string };
    setStatus(`window load failed: ${e.code ?? "error"}`);
  }
}

// --- rendering -------------------------------------------------------------------

function viewCallbacks() {
  return {
    onOpen: openPopover,
    onDayActivate(day: string) {
      mode = "day";
      anchor = day;
      monthState.focusDay = day;
      void navigate();
    },
    onMoveIntent: (occ: Occurrence, dayDelta: number, minuteDelta: number) => {
      void moveOccurrence(occ, dayDelta, minuteDelta);
    },
    onFocusDay(day: string) {
      monthState.focusDay = day;
    },
  };
}

function render(): void {
  if (!viewEl) return;
  closePopover();

  // Title + zone + DST badge.
  const days = visibleDays(mode, anchor);
  if (titleEl) {
    titleEl.textContent =
      mode === "month" ? monthTitle(anchor) : mode === "week" ? weekTitle(days[0], days[6]) : dayTitle(anchor);
  }
  if (zoneEl) zoneEl.textContent = zone ? `zone: ${zone} — resolved server-side` : "";
  const visible = new Set(days);
  const tr = transitions.find((t) => visible.has(t.date));
  if (dstEl) {
    if (tr) {
      dstEl.textContent = `${tr.date}: ${tr.wallFrom}→${tr.wallTo} (${tr.deltaMinutes > 0 ? "+" : "−"}${Math.abs(tr.deltaMinutes / 60)}h)`;
      dstEl.hidden = false;
    } else {
      dstEl.hidden = true;
    }
  }

  // View-switch buttons reflect mode.
  for (const [m, id] of [
    ["month", "cal-v-month"],
    ["week", "cal-v-week"],
    ["day", "cal-v-day"],
  ] as const) {
    const btn = document.getElementById(id);
    if (btn) btn.setAttribute("aria-pressed", String(mode === m));
  }

  const cb = viewCallbacks();
  if (mode === "month") {
    renderMonth(viewEl, startOfMonth(anchor), occs, today, monthState, cb);
  } else {
    renderTimeGrid(viewEl, days, occs, transitions, today, cb);
  }
}

async function navigate(): Promise<void> {
  scheduleDocSync();
  await ensureData();
  render();
}

// --- the move intent: ask Go, render the answer -----------------------------------

async function moveOccurrence(occ: Occurrence, dayDelta: number, minuteDelta: number): Promise<void> {
  if (!canWrite) {
    setStatus("read-only mount — document:write not granted");
    return;
  }
  closePopover();
  const identityDate = occ.id.includes("/") ? occ.id.slice(occ.id.indexOf("/") + 1) : occ.startWall.slice(0, 10);
  setStatus(`sending intent: move by ${dayDelta}d ${minuteDelta >= 0 ? "+" : "−"}${Math.abs(minuteDelta)}m — the server decides…`);
  try {
    const res = await requestMove(occ.eventId, identityDate, dayDelta, minuteDelta);
    // The demo readout's payload: requested vs wall vs elapsed, verbatim.
    sendEvent("moveResolved", {
      title: occ.title,
      from: occ.startWall,
      to: res.occurrence.startWall,
      requestedWallMinutes: res.requestedWallMinutes,
      actualWallMinutes: res.actualWallMinutes,
      elapsedMinutes: res.elapsedMinutes,
      zone: res.zone,
      zoneAbbr: res.zoneAbbr,
      offsetMinutes: res.offsetMinutes,
      note: res.note,
    });
    const diverged = res.requestedWallMinutes !== res.actualWallMinutes;
    setStatus(
      `${occ.title}: requested ${fmtDelta(res.requestedWallMinutes)} · wall ${fmtDelta(res.actualWallMinutes)} · elapsed ${fmtDelta(res.elapsedMinutes)}${diverged ? " — the server adjusted it" : ""}`
    );
    await ensureData(true); // conflicts changed: re-ask the source of truth
  } catch (err) {
    const e = err as { code?: string };
    setStatus(`move refused: ${e.code ?? "error"}`);
  }
}

function fmtDelta(min: number): string {
  const sign = min < 0 ? "−" : "+";
  const m = Math.abs(min);
  return m % 60 === 0 ? `${sign}${m / 60}h` : `${sign}${Math.floor(m / 60)}h${String(m % 60).padStart(2, "0")}`;
}

// --- the event popover -------------------------------------------------------------

function openPopover(occ: Occurrence, anchorEl: HTMLElement): void {
  closePopover();
  if (!viewEl) return;
  const panel = document.createElement("div");
  panel.className = "cal-pop";
  panel.setAttribute("role", "dialog");
  panel.setAttribute("aria-label", `Event details: ${occ.title}`);

  const h3 = document.createElement("h3");
  h3.textContent = occ.title || "Untitled event";
  panel.appendChild(h3);

  const close = document.createElement("button");
  close.type = "button";
  close.className = "cal-btn cal-pop-close";
  close.textContent = "✕";
  close.setAttribute("aria-label", "Close event details");
  close.addEventListener("click", () => closePopover());
  panel.appendChild(close);

  const dl = document.createElement("dl");
  const rows: [string, string][] = [];
  if (occ.allDay) {
    rows.push(["When", `all-day ${occ.startWall}${occ.days > 1 ? ` → ${occ.endWall} (${occ.days} days)` : ""}`]);
  } else {
    const sw = parseWall(occ.startWall);
    const ew = parseWall(occ.endWall);
    const when = `${fmtMinute(sw?.minute ?? 0)} – ${fmtMinute(ew?.minute ?? 0)}${occ.spansMidnight ? ` (+${Math.max(1, Math.round(((ew?.minute ?? 0) - (sw?.minute ?? 0) + 1440) / 1440) - (occ.spansMidnight ? 0 : 0)) || 1}d, spans midnight)` : ""}`;
    rows.push(["When", `${occ.startWall.slice(0, 10)} ${when}`]);
  }
  const off = occ.startOffsetMinutes;
  const offStr = off === 0 ? "UTC" : `UTC${off < 0 ? "−" : "+"}${String(Math.abs(off) / 60).padStart(2, "0")}:${String(Math.abs(off) % 60).padStart(2, "0")}`;
  rows.push(["Zone", `${occ.zone} · ${occ.zoneAbbr} (${offStr})`]);
  rows.push(["Server instant", `${occ.startUtc} → ${occ.endUtc}`]);
  if (occ.recurring) rows.push(["Series", occ.exception ? "part of a series — this instance was moved" : "part of a series"]);
  for (const [dt, dd] of rows) {
    const dtEl = document.createElement("dt");
    dtEl.textContent = dt;
    const ddEl = document.createElement("dd");
    ddEl.textContent = dd;
    dl.appendChild(dtEl);
    dl.appendChild(ddEl);
  }
  panel.appendChild(dl);

  if (occ.dstNote) {
    const note = document.createElement("p");
    note.className = "cal-pop-note";
    note.textContent = occ.dstNote;
    panel.appendChild(note);
  }
  const conflicts = occ.conflictIds?.length ?? 0;
  if (conflicts > 0) {
    const c = document.createElement("p");
    c.className = "cal-pop-conflict";
    c.textContent = `Conflicts with ${conflicts} other event${conflicts === 1 ? "" : "s"} — flagged by the server`;
    panel.appendChild(c);
  }

  if (canWrite) {
    const actions = document.createElement("div");
    actions.className = "cal-pop-actions";
    for (const [label, dd, dm] of [
      ["−1 day", -1, 0],
      ["−30 min", 0, -KEYBOARD_MOVE_MINUTES],
      ["+30 min", 0, KEYBOARD_MOVE_MINUTES],
      ["+1 day", 1, 0],
    ] as const) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "cal-btn";
      b.textContent = label;
      b.addEventListener("click", () => void moveOccurrence(occ, dd, dm));
      actions.appendChild(b);
    }
    panel.appendChild(actions);
  }

  viewEl.appendChild(panel);
  popover = panel;

  // Position near the activating chip, clamped inside the scrollable view.
  const viewRect = viewEl.getBoundingClientRect();
  const r = anchorEl.getBoundingClientRect();
  panel.style.visibility = "hidden";
  panel.style.left = "0px";
  panel.style.top = "0px";
  const pw = panel.offsetWidth || 280;
  const ph = panel.offsetHeight || 200;
  let left = r.left - viewRect.left + viewEl.scrollLeft + 12;
  let top = r.top - viewRect.top + viewEl.scrollTop + 12;
  if (left + pw > viewEl.scrollLeft + viewEl.clientWidth) left = Math.max(4, viewEl.scrollLeft + viewEl.clientWidth - pw - 8);
  if (top + ph > viewEl.scrollTop + viewEl.clientHeight) top = Math.max(4, viewEl.scrollTop + viewEl.clientHeight - ph - 8);
  panel.style.left = `${left}px`;
  panel.style.top = `${top}px`;
  panel.style.visibility = "";
  close.focus();
}

function closePopover(): void {
  popover?.remove();
  popover = null;
}

// --- keyboard ---------------------------------------------------------------------

function onKeyDown(ev: KeyboardEvent): void {
  if (popover && ev.key === "Escape") {
    closePopover();
    ev.preventDefault();
    return;
  }
  const target = ev.target as HTMLElement | null;

  // Month grid: arrows move the roving day focus; Enter opens the day.
  // The CURRENT day is read from the DOM (the focused cell), not from
  // stored state — a programmatic .focus() (tests, screen readers) must
  // not desync the roving tabindex from what is actually focused.
  if (mode === "month" && target?.classList.contains("cal-mdate")) {
    const step =
      ev.key === "ArrowLeft" ? -1 :
      ev.key === "ArrowRight" ? 1 :
      ev.key === "ArrowUp" ? -7 :
      ev.key === "ArrowDown" ? 7 : 0;
    if (step !== 0 && viewEl) {
      const cell = target.closest<HTMLElement>(".cal-mcell");
      const current = cell?.dataset.day ?? monthState.focusDay;
      const next = moveMonthFocus(viewEl, current, step);
      if (next) monthState.focusDay = next;
      ev.preventDefault();
      return;
    }
  }

  // Time grids: arrows on a focused chip MOVE the event (an intent — the
  // server answers). Tab roams focus between chips.
  if (mode !== "month" && target?.classList.contains("cal-evt")) {
    const occ = occs.find((o) => o.id === target.dataset.occId);
    if (occ) {
      const dd = ev.key === "ArrowLeft" ? -1 : ev.key === "ArrowRight" ? 1 : 0;
      const dm =
        ev.key === "ArrowUp" ? -KEYBOARD_MOVE_MINUTES :
        ev.key === "ArrowDown" ? KEYBOARD_MOVE_MINUTES : 0;
      if (dd !== 0 || dm !== 0) {
        ev.preventDefault();
        void moveOccurrence(occ, dd, dm);
        return;
      }
    }
  }

  // View shortcuts, only when no popover and nothing focused.
  if (!popover && (target === document.body || target === viewEl)) {
    const keyMap: Record<string, () => void> = {
      m: () => setMode("month"),
      w: () => setMode("week"),
      d: () => setMode("day"),
      t: () => goToday(),
    };
    const fn = keyMap[ev.key.toLowerCase()];
    if (fn) {
      fn();
      ev.preventDefault();
    }
  }
}

function goToday(): void {
  anchor = today;
  monthState.focusDay = anchor;
  void navigate();
}

function setMode(m: Mode): void {
  mode = m;
  monthState.focusDay = anchor;
  void navigate();
}

function handleInit(params: unknown): void {
  const p = asRecord(params);
  const doc = asRecord(p.doc);
  const view = asRecord(doc.view);
  const date = typeof view.date === "string" ? view.date : "";
  if (/^\d{4}-\d{2}-\d{2}$/.test(date)) anchor = date;
  const m = typeof view.mode === "string" ? view.mode : "";
  if (m === "month" || m === "week" || m === "day") mode = m;
  monthState.focusDay = anchor;
  today = localToday();

  applyTokens(p.tokens);
  applyScheme(p.scheme);
  sendEvent("themeApplied", {
    scheme: p.scheme ?? null,
    sample: sampleAppliedTokens(SAMPLE_TOKENS),
  });

  const caps = Array.isArray(p.capabilities) ? p.capabilities.filter((c) => typeof c === "string") : [];
  canWrite = hasCap(caps, "document:write");
  initialized = true;

  void navigate();
}

function handleThemeChanged(params: unknown): void {
  const p = asRecord(params);
  applyTokens(p.tokens);
  applyScheme(p.scheme);
  sendEvent("themeApplied", {
    scheme: p.scheme ?? null,
    sample: sampleAppliedTokens(SAMPLE_TOKENS),
  });
}

// requestSave is a REQUEST → must answer with the current view-state doc.
function handleRequestSave(): Record<string, unknown> {
  return { doc: currentDoc(), schemaVersion: SCHEMA_VERSION };
}

// teardown is a REQUEST → return {} after a clean teardown (no leaked
// listeners, nothing left pending).
function handleTeardown(): Record<string, never> {
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
  rejectAllBridge({ code: "E_TEARDOWN", message: "frame torn down" });
  rejectAllPending({ code: "E_TEARDOWN", message: "frame torn down" });
  return {};
}

function handleJumpToDate(params: unknown): void {
  const p = asRecord(params);
  const date = typeof p.date === "string" ? p.date : "";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) return;
  anchor = date;
  monthState.focusDay = date;
  setStatus(`jumped to ${date}`);
  void navigate();
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
    void window.parent.document;
  } catch {
    parentBlocked = true;
  }
  try {
    window.localStorage.getItem("__probe");
  } catch {
    storageBlocked = true;
  }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

function announceReady(): void {
  sendEvent("ready", {
    version: "0.1.0",
    schemaVersion: SCHEMA_VERSION,
    minHeight: 620,
    probes: isolationProbes(),
  });
}

function boot(): void {
  viewEl = document.getElementById("cal-view");
  titleEl = document.getElementById("cal-title");
  zoneEl = document.getElementById("cal-zone");
  dstEl = document.getElementById("cal-dst");
  statusEl = document.getElementById("cal-status");
  if (!viewEl) return;

  document.getElementById("cal-prev")?.addEventListener("click", () => {
    shiftAnchor(-1);
    void navigate();
  });
  document.getElementById("cal-next")?.addEventListener("click", () => {
    shiftAnchor(1);
    void navigate();
  });
  document.getElementById("cal-today")?.addEventListener("click", goToday);
  document.getElementById("cal-v-month")?.addEventListener("click", () => setMode("month"));
  document.getElementById("cal-v-week")?.addEventListener("click", () => setMode("week"));
  document.getElementById("cal-v-day")?.addEventListener("click", () => setMode("day"));

  viewEl.addEventListener("keydown", onKeyDown);

  messageListener = createRouter({
    init: handleInit,
    themeChanged: handleThemeChanged,
    requestSave: handleRequestSave,
    teardown: handleTeardown,
    hostPointerdown: () => closePopover(),
    jumpToDate: handleJumpToDate,
    occurrencesResult: (params) => {
      handleBridgeResult("occurrencesResult", params);
    },
    moveResult: (params) => {
      handleBridgeResult("moveResult", params);
    },
  });
  window.addEventListener("message", messageListener);

  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
