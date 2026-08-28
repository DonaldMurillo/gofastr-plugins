// Pure wall-clock date helpers, string-in/string-out.
//
// THE RULE THIS FILE EXISTS TO ENFORCE: the frame never constructs a zoned
// Date and never parses a datetime string with the Date constructor — either
// would consult the browser's local timezone, and the frame is forbidden
// from guessing zones. Calendar math runs through Date.UTC (pure field
// arithmetic), and every wall string is sliced by position.

export interface Day {
  y: number;
  m: number; // 1..12
  d: number;
}

export interface WallTime {
  day: string; // "2026-03-08"
  minute: number; // minutes since local midnight
}

const DAY_RE = /^(\d{4})-(\d{2})-(\d{2})$/;
const WALL_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/;

export function parseDay(s: string): Day | null {
  const m = DAY_RE.exec(s);
  if (!m) return null;
  return { y: Number(m[1]), m: Number(m[2]), d: Number(m[3]) };
}

export function dayToStr(day: Day): string {
  const p2 = (n: number) => String(n).padStart(2, "0");
  return `${day.y}-${p2(day.m)}-${p2(day.d)}`;
}

/** UTC-anchored Date for pure calendar field math (never a zoned read). */
function utc(day: Day): Date {
  return new Date(Date.UTC(day.y, day.m - 1, day.d));
}

export function weekday(s: string): number {
  const day = parseDay(s);
  if (!day) return 0;
  return utc(day).getUTCDay(); // 0=Sun..6=Sat
}

export function addDays(s: string, n: number): string {
  const day = parseDay(s);
  if (!day) return s;
  const d = utc(day);
  d.setUTCDate(d.getUTCDate() + n);
  return dayToStr({ y: d.getUTCFullYear(), m: d.getUTCMonth() + 1, d: d.getUTCDate() });
}

export function diffDays(a: string, b: string): number {
  const da = parseDay(a);
  const db = parseDay(b);
  if (!da || !db) return 0;
  return Math.round((utc(da).getTime() - utc(db).getTime()) / 86_400_000);
}

/** Monday of `s`'s week. */
export function startOfWeek(s: string): string {
  const wd = weekday(s); // 0=Sun..6=Sat → Monday-based offset
  const off = (wd + 6) % 7;
  return off === 0 ? s : addDays(s, -off);
}

export function startOfMonth(s: string): string {
  return s.slice(0, 8) + "01";
}

export function daysInMonth(s: string): number {
  const day = parseDay(s);
  if (!day) return 30;
  return new Date(Date.UTC(day.y, day.m, 0)).getUTCDate();
}

/** Parse "2026-03-08T01:30" → {day, minute}. Date-only strings are midnight. */
export function parseWall(s: string): WallTime | null {
  const w = WALL_RE.exec(s);
  if (w) {
    return { day: s.slice(0, 10), minute: Number(w[4]) * 60 + Number(w[5]) };
  }
  const d = DAY_RE.exec(s);
  if (d) return { day: s, minute: 0 };
  return null;
}

/** "01:30" → 90; "03:00" → 180. */
export function parseHM(s: string): number | null {
  const m = /^(\d{2}):(\d{2})$/.exec(s);
  if (!m) return null;
  return Number(m[1]) * 60 + Number(m[2]);
}

/** "2026-03-08T01:30" → "1:30". */
export function wallTimeOf(s: string): string {
  const w = parseWall(s);
  return w ? fmtMinute(w.minute) : s;
}

/** 90 → "1:30"; 180 → "3:00"; 1320 → "22:00". 24h, no leading zero on the
 *  hour — chip-friendly, and explicit about o'clock times so screen-reader
 *  labels say "9:00 to 9:30", never "9 to 9:30". */
export function fmtMinute(min: number): string {
  const clamped = Math.max(0, Math.min(24 * 60, min));
  const h = Math.floor(clamped / 60);
  const m = clamped % 60;
  return `${h}:${String(m).padStart(2, "0")}`;
}
const MONTHS = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];
const MONTHS_SHORT = [
  "Jan", "Feb", "Mar", "Apr", "May", "Jun",
  "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
];
const WEEKDAYS_LONG = [
  "Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday",
];
const WEEKDAYS_MIN = ["S", "M", "T", "W", "T", "F", "S"];

export function monthTitle(s: string): string {
  const day = parseDay(s);
  if (!day) return s;
  return `${MONTHS[day.m - 1]} ${day.y}`;
}

export function weekTitle(from: string, to: string): string {
  const a = parseDay(from);
  const b = parseDay(to);
  if (!a || !b) return from;
  if (a.m === b.m) return `${MONTHS[a.m - 1]} ${a.d} – ${b.d}, ${b.y}`;
  return `${MONTHS_SHORT[a.m - 1]} ${a.d} – ${MONTHS_SHORT[b.m - 1]} ${b.d}, ${b.y}`;
}

export function dayTitle(s: string): string {
  const day = parseDay(s);
  if (!day) return s;
  return `${WEEKDAYS_LONG[weekday(s)]}, ${MONTHS_SHORT[day.m - 1]} ${day.d}, ${day.y}`;
}

export const WEEKDAY_HEAD_MIN = WEEKDAYS_MIN;

/** The wall dates a timed occurrence intersects (start date .. end date; an
 *  end exactly at midnight belongs to no further day). */
export function wallDatesOf(startWall: string, endWall: string): string[] {
  const s = parseWall(startWall);
  const e = parseWall(endWall);
  if (!s || !e) return [];
  let last = e.day;
  if (e.minute === 0) last = addDays(last, -1); // exclusive-midnight end
  if (last < s.day) last = s.day;
  const out: string[] = [];
  for (let cur = s.day; cur <= last && out.length < 366; cur = addDays(cur, 1)) {
    out.push(cur);
  }
  return out;
}
