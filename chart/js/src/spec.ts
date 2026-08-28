// chart-v1 spec — the frame-side twin of chart/ssr/spec.go.
//
// The canonical doc arrives over init.doc (untrusted postMessage payload),
// so it is validated and normalized HERE before rendering. The rules are a
// deliberate mirror of the Go Normalize(): same type set, same caps
// (10,000 total points / 12 series), same defaults (tickCount 10, legend
// on), same domain rules in extents(). When the two sides drift, the
// server/client agreement test catches it — keep them in lockstep.

export const SCHEMA_VERSION = "chart-v1";
export const MAX_POINTS = 10000;
export const MAX_SERIES = 12;
export const MAX_TEXT = 200;

export type ChartType = "line" | "bar" | "area" | "scatter";

export interface Point {
  x: number;
  y: number;
}

export interface Series {
  name: string;
  points: Point[];
}

export interface AxisSpec {
  label: string;
  tickCount: number;
}

export interface Spec {
  schemaVersion: string;
  type: ChartType;
  title: string;
  series: Series[];
  axes: { x: AxisSpec; y: AxisSpec };
  options: { legend: boolean };
}

export class SpecError extends Error {}

const TYPES: Record<string, true> = { line: true, bar: true, area: true, scatter: true };

function finiteNumber(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

/** Normalize an untrusted doc into a renderable Spec, or throw SpecError. */
export function normalizeSpec(doc: unknown): Spec {
  if (!doc || typeof doc !== "object") throw new SpecError("doc is not an object");
  const raw = doc as Record<string, unknown>;

  const schemaVersion = raw.schemaVersion === undefined ? SCHEMA_VERSION : String(raw.schemaVersion);
  if (schemaVersion !== SCHEMA_VERSION) {
    throw new SpecError(`unknown schemaVersion ${schemaVersion}`);
  }
  const type = String(raw.type ?? "");
  if (!TYPES[type]) throw new SpecError(`unknown chart type ${type}`);

  const title = typeof raw.title === "string" ? raw.title : "";
  if (title.length > MAX_TEXT) throw new SpecError("title too long");

  const seriesRaw = Array.isArray(raw.series) ? raw.series : null;
  if (!seriesRaw || seriesRaw.length === 0) throw new SpecError("chart has no series");
  if (seriesRaw.length > MAX_SERIES) throw new SpecError("too many series");

  const series: Series[] = [];
  let total = 0;
  for (let i = 0; i < seriesRaw.length; i++) {
    const s = seriesRaw[i];
    if (!s || typeof s !== "object") throw new SpecError(`series ${i + 1} is not an object`);
    const name = typeof (s as Record<string, unknown>).name === "string" && (s as Record<string, unknown>).name !== ""
      ? ((s as Record<string, unknown>).name as string)
      : `series ${i + 1}`;
    if (name.length > MAX_TEXT) throw new SpecError("series name too long");
    const pointsRaw = Array.isArray((s as Record<string, unknown>).points) ? ((s as Record<string, unknown>).points as unknown[]) : [];
    const points: Point[] = [];
    for (const p of pointsRaw) {
      if (!p || typeof p !== "object") throw new SpecError("point is not an object");
      const rec = p as Record<string, unknown>;
      const x = finiteNumber(rec.x);
      const y = finiteNumber(rec.y);
      if (x === null || y === null) throw new SpecError("point x/y must be finite numbers");
      points.push({ x, y });
    }
    total += points.length;
    series.push({ name, points });
  }
  if (total > MAX_POINTS) throw new SpecError("too many points");

  const axesRaw = (raw.axes && typeof raw.axes === "object" ? raw.axes : {}) as Record<string, unknown>;
  const axis = (a: unknown): AxisSpec => {
    const rec = (a && typeof a === "object" ? a : {}) as Record<string, unknown>;
    const label = typeof rec.label === "string" ? rec.label : "";
    if (label.length > MAX_TEXT) throw new SpecError("axis label too long");
    let tickCount = 10;
    const n = finiteNumber(rec.tickCount);
    if (n !== null && Number.isInteger(n)) tickCount = Math.min(20, Math.max(2, Math.trunc(n)));
    return { label, tickCount };
  };

  const optionsRaw = (raw.options && typeof raw.options === "object" ? raw.options : {}) as Record<string, unknown>;

  return {
    schemaVersion,
    type: type as ChartType,
    title,
    series,
    axes: { x: axis(axesRaw.x), y: axis(axesRaw.y) },
    options: { legend: optionsRaw.legend === undefined ? true : optionsRaw.legend !== false },
  };
}

/**
 * The domain rules — a mirror of ssr.Extents. The frame passes the result
 * to Plot as explicit `domain`, which is what pins the hydrated chart to
 * the SSR chart's extents:
 *   - bar/area include the zero baseline (Plot's maybeZero does the same)
 *   - bar pads x by half a bar group each side, so edge bars fit
 *   - a degenerate extent widens to m-1..m+1
 */
export function extents(spec: Spec): { x: [number, number]; y: [number, number] } {
  let has = false;
  let x0 = 0, x1 = 0, y0 = 0, y1 = 0;
  for (const ser of spec.series) {
    for (const p of ser.points) {
      if (!has) {
        x0 = x1 = p.x;
        y0 = y1 = p.y;
        has = true;
        continue;
      }
      x0 = Math.min(x0, p.x);
      x1 = Math.max(x1, p.x);
      y0 = Math.min(y0, p.y);
      y1 = Math.max(y1, p.y);
    }
  }
  if (!has) return { x: [0, 1], y: [0, 1] };
  if (spec.type === "bar" || spec.type === "area") {
    y0 = Math.min(y0, 0);
    y1 = Math.max(y1, 0);
  }
  if (!(x1 > x0)) { const m = x0; x0 = m - 1; x1 = m + 1; }
  if (!(y1 > y0)) { const m = y0; y0 = m - 1; y1 = m + 1; }
  if (spec.type === "bar") {
    const g = minXGap(spec);
    if (g !== Infinity) {
      x0 -= 0.4 * g;
      x1 += 0.4 * g;
    }
  }
  return { x: [x0, x1], y: [y0, y1] };
}

/**
 * Smallest positive gap between adjacent x values across all series
 * (pooled, sorted; duplicates collapse to zero and are skipped). Infinity
 * when fewer than two distinct x values exist. Mirrors ssr.minXGap — the
 * agreement test compares the padded data-domain-x attributes, so the
 * arithmetic here must produce the identical double the Go side does.
 */
export function minXGap(spec: Spec): number {
  const xs: number[] = [];
  for (const ser of spec.series) for (const p of ser.points) xs.push(p.x);
  xs.sort((a, b) => a - b);
  let minGap = Infinity;
  for (let i = 1; i < xs.length; i++) {
    const g = xs[i] - xs[i - 1];
    if (g > 0 && g < minGap) minGap = g;
  }
  return minGap;
}
