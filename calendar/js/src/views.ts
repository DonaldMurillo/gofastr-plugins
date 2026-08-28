// View rendering: month grid and week/day time grid, hand-written.
//
// Everything the frame draws comes from server-resolved strings — startWall/
// endWall for layout (wall clock IS the grid), startUtc/endUtc only for
// display. No Date parsing of datetime strings, no zone arithmetic: the
// frame cannot guess a zone because nothing here ever asks for one.
//
// Pointer events (never HTML5 drag-and-drop, which does not behave inside a
// sandboxed frame): pointerdown on a chip captures the pointer, a ghost
// follows, and pointerup converts the displacement into a WALL-CLOCK intent
// — dayDelta days + minuteDelta minutes, 15-minute snap — handed to the
// host. What lands is whatever the server says landed.

import type { Occurrence, Transition } from "./bridge";
import {
  WEEKDAY_HEAD_MIN,
  addDays,
  fmtMinute,
  parseHM,
  parseWall,
  wallDatesOf,
  wallTimeOf,
  weekday,
} from "./dates";

const HOUR_H = 44; // px per wall-clock hour — the grid's unit of geometry
const DAY_H = 24 * HOUR_H;
const MONTH_CHIP_MAX = 3;
const DRAG_SNAP_MIN = 15;
const KEY_MOVE_MIN = 30;

export interface ViewCallbacks {
  /** Chip activated (click/Enter): open the detail popover. */
  onOpen(occ: Occurrence, anchor: HTMLElement): void;
  /** Month date activated (click/Enter): switch to the day view. */
  onDayActivate(day: string): void;
  /** A drag or keyboard move resolved to an intent; the host decides. */
  onMoveIntent(occ: Occurrence, dayDelta: number, minuteDelta: number): void;
  /** Roving focus moved to a new day (month view) — keeps state for re-render. */
  onFocusDay(day: string): void;
}

// --- shared chip construction -----------------------------------------------

function chipClasses(occ: Occurrence): string {
  const cls = ["cal-evt"];
  if (occ.allDay) cls.push("is-allday");
  if (occ.recurring) cls.push("is-recurring");
  if ((occ.conflictIds?.length ?? 0) > 0) cls.push("is-conflict");
  return cls.join(" ");
}

function occAria(occ: Occurrence): string {
  const parts: string[] = [occ.title || "Untitled event"];
  if (occ.allDay) {
    parts.push(occ.days > 1 ? `all-day, ${occ.days} days` : "all-day");
  } else {
    parts.push(`${wallTimeOf(occ.startWall)} to ${wallTimeOf(occ.endWall)}`);
  }
  if (occ.recurring) parts.push("recurring");
  const conflicts = occ.conflictIds?.length ?? 0;
  if (conflicts > 0) {
    parts.push(`conflicts with ${conflicts} other event${conflicts === 1 ? "" : "s"}`);
  }
  if (occ.dstNote) parts.push(occ.dstNote);
  return parts.join(", ");
}

function makeChip(occ: Occurrence, opts: { continues?: boolean; timePrefix?: boolean; multiDay?: boolean }): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = chipClasses(occ) + (opts.continues ? " is-continues" : "");
  btn.dataset.occId = occ.id;
  btn.setAttribute("aria-label", occAria(occ));
  btn.draggable = false;

  if (opts.timePrefix && !occ.allDay) {
    const time = document.createElement("span");
    time.className = "cal-evt-time";
    time.textContent = (opts.continues ? "→" : "") + wallTimeOf(occ.startWall);
    btn.appendChild(time);
  }
  const title = document.createElement("span");
  title.className = "cal-evt-title";
  title.textContent = occ.title + (opts.multiDay && occ.days > 1 ? ` · ${occ.days}d` : "");
  btn.appendChild(title);
  return btn;
}

/** Occurrences intersecting one wall date, display order. */
export function occurrencesOnDay(occs: Occurrence[], day: string): Occurrence[] {
  const out: { occ: Occurrence; rank: number }[] = [];
  for (const occ of occs) {
    const dates = occ.allDay ? allDayDates(occ) : wallDatesOf(occ.startWall, occ.endWall);
    const idx = dates.indexOf(day);
    if (idx === -1) continue;
    const start = parseWall(occ.startWall);
    out.push({ occ, rank: occ.allDay ? 0 : start ? start.minute : 0 });
  }
  out.sort((a, b) => a.rank - b.rank || a.occ.startWall.localeCompare(b.occ.startWall));
  return out.map((o) => o.occ);
}

function allDayDates(occ: Occurrence): string[] {
  const out: string[] = [];
  // startWall..endWall exclusive; days already server-computed, but derive
  // from the strings so the frame trusts nothing it cannot see.
  let cur = occ.startWall.slice(0, 10);
  const endExclusive = occ.endWall.slice(0, 10);
  for (let i = 0; i < 366 && cur < endExclusive; i++) {
    out.push(cur);
    cur = addDays(cur, 1);
  }
  return out;
}

// --- month view --------------------------------------------------------------

export interface MonthState {
  focusDay: string; // roving-focus day inside the grid
}

export function renderMonth(
  container: HTMLElement,
  anchorMonth: string,
  occurrences: Occurrence[],
  today: string,
  state: MonthState,
  cb: ViewCallbacks
): void {
  container.replaceChildren();

  // Monday-start weeks, 6 rows — the classic month grid.
  const first = anchorMonth.slice(0, 8) + "01";
  const gridStart = weekStartOf(first);
  const days: string[] = [];
  for (let i = 0; i < 42; i++) days.push(addDays(gridStart, i));

  const head = document.createElement("div");
  head.className = "cal-month-head";
  head.setAttribute("aria-hidden", "true");
  // Monday-first column order.
  for (let i = 1; i <= 7; i++) {
    const h = document.createElement("div");
    h.textContent = WEEKDAY_HEAD_MIN[i % 7];
    head.appendChild(h);
  }
  container.appendChild(head);

  const grid = document.createElement("div");
  grid.className = "cal-month-grid";
  grid.setAttribute("role", "grid");
  grid.setAttribute("aria-label", "Month view");

  let focusPlaced = false;
  for (let row = 0; row < 6; row++) {
    const rowEl = document.createElement("div");
    rowEl.className = "cal-mrow";
    rowEl.setAttribute("role", "row");
    for (let col = 0; col < 7; col++) {
      const day = days[row * 7 + col];
      const cell = document.createElement("div");
      cell.className = "cal-mcell";
      cell.setAttribute("role", "gridcell");
      cell.dataset.day = day;
      if (!day.startsWith(anchorMonth.slice(0, 7))) cell.classList.add("is-out");
      if (day === today) cell.classList.add("is-today");
      if (day === state.focusDay) cell.setAttribute("aria-selected", "true");

      const dateBtn = document.createElement("button");
      dateBtn.type = "button";
      dateBtn.className = "cal-mdate";
      dateBtn.textContent = String(Number(day.slice(8, 10)));
      // Roving tabindex: the focused day (or else the first in-month cell)
      // is the single tab stop; every other date is arrow-reachable.
      dateBtn.tabIndex = -1;
      if (day === state.focusDay) {
        dateBtn.tabIndex = 0;
        focusPlaced = true;
      } else if (!focusPlaced && day.startsWith(anchorMonth.slice(0, 7))) {
        dateBtn.tabIndex = 0;
        focusPlaced = true;
      }
      dateBtn.setAttribute("aria-label", `${day}: open day view`);
      dateBtn.addEventListener("click", () => cb.onDayActivate(day));
      cell.appendChild(dateBtn);

      const dayOccs = occurrencesOnDay(occurrences, day);
      const shown = dayOccs.slice(0, MONTH_CHIP_MAX);
      for (const occ of shown) {
        const firstDay = (occ.allDay ? allDayDates(occ) : wallDatesOf(occ.startWall, occ.endWall))[0] === day;
        cell.appendChild(
          makeChip(occ, { timePrefix: true, continues: !firstDay, multiDay: occ.allDay })
        );
      }
      if (dayOccs.length > shown.length) {
        const more = document.createElement("span");
        more.className = "cal-mmore";
        more.textContent = `+${dayOccs.length - shown.length} more`;
        cell.appendChild(more);
      }
      rowEl.appendChild(cell);
    }
    grid.appendChild(rowEl);
  }

  // Chip clicks inside the grid (delegated; chips are inside gridcells).
  grid.addEventListener("click", (ev) => {
    const target = ev.target as HTMLElement;
    const chip = target.closest<HTMLElement>(".cal-evt");
    const occ = chip && occurrences.find((o) => o.id === chip.dataset.occId);
    if (occ) {
      ev.stopPropagation();
      cb.onOpen(occ, chip);
    }
  });
  container.appendChild(grid);
}

function weekStartOf(day: string): string {
  const off = (weekday(day) + 6) % 7;
  return off === 0 ? day : addDays(day, -off);
}

/** Month-grid keyboard roving: move the focused date button. */
export function moveMonthFocus(container: HTMLElement, current: string, deltaDays: number): string | null {
  const next = addDays(current, deltaDays);
  const btn = monthDateButton(container, next);
  if (!btn) return null;
  const prev = monthDateButton(container, current);
  if (prev) prev.tabIndex = -1;
  btn.tabIndex = 0;
  btn.focus();
  return next;
}

export function monthDateButton(container: HTMLElement, day: string): HTMLButtonElement | null {
  const cell = container.querySelector<HTMLElement>(`.cal-mcell[data-day="${day}"]`);
  const btn = cell?.querySelector<HTMLButtonElement>(".cal-mdate") ?? null;
  return btn;
}

// --- week / day time grid ------------------------------------------------------

interface Box {
  occ: Occurrence;
  startMin: number;
  endMin: number;
  continues: boolean;
  col: number;
  cols: number;
}

export function renderTimeGrid(
  container: HTMLElement,
  days: string[],
  occurrences: Occurrence[],
  transitions: Transition[],
  today: string,
  cb: ViewCallbacks
): void {
  container.replaceChildren();

  const wrap = document.createElement("div");
  wrap.className = "cal-wk";
  const gutterWrap = document.createElement("div");
  gutterWrap.setAttribute("aria-hidden", "true");
  const main = document.createElement("div");
  main.className = "cal-wk-main";
  main.style.setProperty("--cal-cols", String(days.length));

  // Header row + all-day row (shares column geometry with the grid).
  const heads = document.createElement("div");
  heads.className = "cal-allday";
  heads.setAttribute("aria-hidden", "false");
  const headInner = document.createElement("div");
  headInner.className = "cal-allday-inner";
  const allday = document.createElement("div");
  allday.className = "cal-allday";
  const alldayInner = document.createElement("div");
  alldayInner.className = "cal-allday-inner";
  alldayInner.style.setProperty("grid-template-columns", `repeat(${days.length}, 1fr)`);
  headInner.style.setProperty("grid-template-columns", `repeat(${days.length}, 1fr)`);
  for (let i = 0; i < days.length; i++) {
    const day = days[i];
    const head = document.createElement("div");
    head.className = "cal-dayhead" + (day === today ? " is-today" : "");
    // No role="columnheader": these heads sit outside any row/grid
    // structure (axe: aria-required-parent), and every chip already carries
    // a full sentence of its own in aria-label.
    const label = document.createElement("span");
    label.textContent = `${weekdayShort(day)} ${Number(day.slice(8, 10))}${transitions.find((t) => t.date === day) ? " ⏱" : ""}`;
    head.appendChild(label);
    const trHere = transitions.find((t) => t.date === day);
    if (trHere) {
      head.title = `DST: ${trHere.wallFrom}→${trHere.wallTo} (${trHere.deltaMinutes > 0 ? "+" : ""}${trHere.deltaMinutes} min)`;
    }
    headInner.appendChild(head);

    const cell = document.createElement("div");
    cell.className = "cal-allday-day";
    for (const occ of occurrencesOnDay(occurrences, day)) {
      if (!occ.allDay) continue;
      const firstDay = allDayDates(occ)[0] === day;
      if (!firstDay) continue; // one chip on the first day; the row reads left-to-right
      const chip = makeChip(occ, { multiDay: true });
      chip.tabIndex = 0;
      cell.appendChild(chip);
    }
    alldayInner.appendChild(cell);
  }
  heads.appendChild(headInner);
  allday.appendChild(alldayInner);

  // Scrollable time grid.
  const scroll = document.createElement("div");
  scroll.className = "cal-wk-scroll";
  const gridEl = document.createElement("div");
  gridEl.className = "cal-wk-grid";
  gridEl.style.setProperty("--cal-cols", String(days.length));

  // Hour gutter.
  const gutter = document.createElement("div");
  gutter.className = "cal-gutter";
  for (let h = 0; h < 24; h++) {
    const lab = document.createElement("span");
    lab.className = "cal-gutter-hour";
    lab.style.top = `${h * HOUR_H}px`;
    lab.textContent = h === 0 ? "" : fmtMinute(h * 60);
    gutter.appendChild(lab);
  }
  gridEl.appendChild(gutter);

  // Day columns with positioned occurrence boxes.
  const byDay = new Map<string, HTMLElement>();
  for (const day of days) {
    const col = document.createElement("div");
    col.className = "cal-daycol" + ([0, 6].includes(weekday(day)) ? " is-weekend" : "");
    col.dataset.day = day;
    byDay.set(day, col);
    gridEl.appendChild(col);
  }

  // DST transition markers, where the server says they land.
  for (const tr of transitions) {
    const from = parseHM(tr.wallFrom);
    const to = parseHM(tr.wallTo);
    if (from === null || to === null) continue;
    const lo = Math.min(from, to);
    const hi = Math.max(from, to);
    const line = document.createElement("div");
    line.className = "cal-dstline";
    line.style.top = `${(lo / 60) * HOUR_H}px`;
    line.style.height = `${((hi - lo) / 60) * HOUR_H}px`;
    line.dataset.label =
      tr.deltaMinutes > 0
        ? `${tr.wallFrom}→${tr.wallTo} does not exist (+${tr.deltaMinutes / 60}h)`
        : `${tr.wallFrom} repeats (−${Math.abs(tr.deltaMinutes) / 60}h)`;
    gridEl.appendChild(line);
  }

  // Timed boxes per day.
  for (const day of days) {
    const col = byDay.get(day);
    if (!col) continue;
    const boxes: Box[] = [];
    for (const occ of occurrencesOnDay(occurrences, day)) {
      if (occ.allDay) continue;
      const start = parseWall(occ.startWall);
      const end = parseWall(occ.endWall);
      if (!start || !end) continue;
      let startMin = start.minute;
      let endMin = end.minute;
      const continues = start.day < day;
      if (start.day < day) startMin = 0;
      if (end.day > day || (end.day === day && end.minute === 0 && start.day < day)) endMin = 24 * 60;
      if (endMin <= startMin) endMin = Math.min(startMin + 30, 24 * 60);
      boxes.push({ occ, startMin, endMin, continues, col: 0, cols: 1 });
    }
    layoutColumns(boxes);
    for (const box of boxes) {
      const el = document.createElement("div");
      el.className = "cal-evtbox" + ((box.endMin - box.startMin) < 45 ? " is-tiny" : "");
      el.style.top = `${(box.startMin / 60) * HOUR_H}px`;
      el.style.height = `${Math.max((box.endMin - box.startMin) / 60 * HOUR_H, 16)}px`;
      el.style.left = `calc(${(box.col / box.cols) * 100}% + 2px)`;
      el.style.width = `calc(${100 / box.cols}% - 4px)`;
      const chip = makeChip(box.occ, { timePrefix: true, continues: box.continues });
      chip.tabIndex = 0;
      chip.style.touchAction = "none"; // pointer-drag surface, not a scroll handle
      wireDrag(chip, box.occ, gridEl, days.length, cb);
      el.appendChild(chip);
      col.appendChild(el);
    }
  }


  scroll.appendChild(gridEl);
  main.appendChild(heads);
  main.appendChild(allday);
  main.appendChild(scroll);
  wrap.appendChild(gutterWrap);
  wrap.appendChild(main);
  container.appendChild(wrap);

  // Delegated chip activation (click on non-drag, all-day chips).
  container.addEventListener("click", (ev) => {
    const chip = (ev.target as HTMLElement).closest<HTMLElement>(".cal-evt");
    const occ = chip && occurrences.find((o) => o.id === chip.dataset.occId);
    if (occ) {
      ev.stopPropagation();
      cb.onOpen(occ, chip);
    }
  });
}

const WEEKDAY_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
function weekdayShort(day: string): string {
  return WEEKDAY_SHORT[weekday(day)];
}

/** Greedy column assignment for overlapping boxes within one day. */
function layoutColumns(boxes: Box[]): void {
  boxes.sort((a, b) => a.startMin - b.startMin || b.endMin - a.endMin);
  let cluster: Box[] = [];
  let clusterEnd = -1;
  const flush = () => {
    if (cluster.length === 0) return;
    const colEnds: number[] = [];
    for (const box of cluster) {
      let col = 0;
      while (col < colEnds.length && colEnds[col] > box.startMin) col++;
      box.col = col;
      if (col === colEnds.length) colEnds.push(box.endMin);
      else colEnds[col] = Math.max(colEnds[col], box.endMin);
    }
    for (const box of cluster) box.cols = colEnds.length;
    cluster = [];
  };
  for (const box of boxes) {
    if (cluster.length > 0 && box.startMin >= clusterEnd) flush();
    cluster.push(box);
    clusterEnd = Math.max(clusterEnd, box.endMin);
  }
  flush();
}

// --- pointer drag (never HTML5 DnD) -------------------------------------------

interface DragState {
  occ: Occurrence;
  chip: HTMLElement;
  ghost: HTMLElement;
  startX: number;
  startY: number;
  dayWidth: number;
  moved: boolean;
}

let drag: DragState | null = null;

function wireDrag(
  chip: HTMLElement,
  occ: Occurrence,
  gridEl: HTMLElement,
  colCount: number,
  cb: ViewCallbacks
): void {
  chip.addEventListener("pointerdown", (ev: PointerEvent) => {
    if (ev.button !== 0) return;
    const gridRect = gridEl.getBoundingClientRect();
    const chipRect = chip.getBoundingClientRect();
    const ghost = document.createElement("div");
    ghost.className = "cal-ghost";
    ghost.style.left = `${chipRect.left - gridRect.left}px`;
    ghost.style.top = `${chipRect.top - gridRect.top}px`;
    ghost.style.width = `${chipRect.width}px`;
    ghost.style.height = `${chipRect.height}px`;
    ghost.textContent = occ.title;
    gridEl.appendChild(ghost);
    drag = {
      occ,
      chip,
      ghost,
      startX: ev.clientX,
      startY: ev.clientY,
      dayWidth: gridRect.width / Math.max(1, colCount),
      moved: false,
    };
    chip.setPointerCapture(ev.pointerId);
    ev.preventDefault();
  });

  chip.addEventListener("pointermove", (ev: PointerEvent) => {
    if (!drag || drag.chip !== chip) return;
    const dx = ev.clientX - drag.startX;
    const dy = ev.clientY - drag.startY;
    if (Math.abs(dx) > 3 || Math.abs(dy) > 3) drag.moved = true;
    drag.ghost.style.transform = `translate(${dx}px, ${dy}px)`;
  });

  const finish = (ev: PointerEvent) => {
    if (!drag || drag.chip !== chip) return;
    const state = drag;
    drag = null;
    state.ghost.remove();
    const dx = ev.clientX - state.startX;
    const dy = ev.clientY - state.startY;
    if (!state.moved) return; // plain click → activation via click handler
    const dayDelta = Math.round(dx / state.dayWidth);
    const minuteDelta = Math.round(dy / HOUR_H * 60 / DRAG_SNAP_MIN) * DRAG_SNAP_MIN;
    if (dayDelta === 0 && minuteDelta === 0) return;
    cb.onMoveIntent(state.occ, dayDelta, minuteDelta);
  };
  chip.addEventListener("pointerup", finish);
  chip.addEventListener("pointercancel", () => {
    if (drag && drag.chip === chip) {
      drag.ghost.remove();
      drag = null;
    }
  });
}

/** Keyboard move step for arrow keys on a focused chip. */
export const KEYBOARD_MOVE_MINUTES = KEY_MOVE_MIN;

/** Grid geometry export for tests/metrics. */
export const GRID = { HOUR_H, DAY_H };

/** The wall dates a month view covers (Monday-start, 6 weeks). */
export function monthWindow(anchorMonth: string): { from: string; to: string } {
  const first = anchorMonth.slice(0, 8) + "01";
  const from = weekStartOf(first);
  return { from, to: addDays(from, 48) }; // 42 grid days + span pad
}

/** The wall dates a week view covers (padded a day for spanning events). */
export function weekWindow(anchor: string): { from: string; to: string } {
  const from = addDays(weekStartOf(anchor), -1);
  return { from, to: addDays(from, 9) };
}

/** The wall dates a day view covers. */
export function dayWindow(anchor: string): { from: string; to: string } {
  return { from: addDays(anchor, -1), to: addDays(anchor, 2) };
}

export function visibleDays(mode: "month" | "week" | "day", anchor: string): string[] {
  if (mode === "day") return [anchor];
  if (mode === "week") {
    const start = weekStartOf(anchor);
    return Array.from({ length: 7 }, (_, i) => addDays(start, i));
  }
  const first = anchor.slice(0, 8) + "01";
  const start = weekStartOf(first);
  return Array.from({ length: 42 }, (_, i) => addDays(start, i));
}

