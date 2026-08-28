// Tick label formatting — the frame-side twin of chart/ssr/ticks.go's
// FormatFloatFixed + tickStepLabelPrecision.
//
// The frame does NOT run its own tick algorithm: Observable Plot's axes
// sample ticks through d3-scale's linear.ticks, which calls d3-array's
// ticks(domain[0], domain[1], count) — the exact function the Go side
// ports (and proves faithful against a committed d3 ground-truth sweep).
// This module only supplies the axis tickFormat so the LABEL STRINGS match
// the server SVG: v.toFixed(p) where p is the smallest precision that
// round-trips the step. The round-trip derivation is engine-independent —
// a Math.log10-based one can differ between JS engines and Go at exact
// power-of-ten boundaries.

import { tickStep } from "d3-array";

/** Smallest p such that step.toFixed(p) parses back to step. */
export function stepPrecision(step: number): number {
  if (!(step > 0) || !Number.isFinite(step)) return 0;
  for (let p = 0; p <= 17; p++) {
    if (Number(step.toFixed(p)) === step) return p;
  }
  return 17;
}

export type TickFormatter = (v: number) => string;

/**
 * The tickFormat handed to both Plot axes. Uses the REAL d3 tickStep on
 * the same (domain, count) the axis samples, so server and client format
 * identical tick values at identical precision.
 */
export function makeTickFormat(domain: readonly [number, number], count: number): TickFormatter {
  const step = tickStep(domain[0], domain[1], count);
  const p = stepPrecision(step);
  // JS toFixed never prints "-0.0" for negative zero (per spec the sign is
  // only prepended when x < 0, which is false for -0); the Go twin
  // normalizes -0 explicitly, so no branch is needed here.
  return (v: number) => v.toFixed(p);
}
