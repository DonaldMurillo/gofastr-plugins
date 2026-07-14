// Keystroke-latency rig (protocol-v1.md §8 — the Phase-0 go/no-go gate).
//
// Measures input→next-paint latency: startSample() records t0=performance.now()
// at text-input time; the next requestAnimationFrame records t1; sample = t1-t0.
// A ring buffer keeps the last N samples. window.__wysiwygMetrics exposes
// {samplesMs, count, p50(), p99(), reset()} for chromedp to read directly in
// the frame. editor.js posts a `metric` event every ~50 samples and on save.
//
// Target: p99 ≤ 16 ms. All editing happens in-frame (no per-keystroke
// postMessage), so the measurement reflects real input handling + DOM update +
// layout, up to just-before-paint.

const MAX_SAMPLES = 1000;
const samplesMs: number[] = [];
let totalCount = 0;
const sampleCallbacks: Array<() => void> = [];

function percentile(p: number): number {
  if (!samplesMs.length) return 0;
  // nearest-rank percentile over the in-buffer samples
  const sorted = samplesMs.slice().sort((a, b) => a - b);
  const rank = Math.ceil((p / 100) * sorted.length);
  const idx = Math.min(sorted.length - 1, Math.max(0, rank - 1));
  return sorted[idx] ?? 0;
}

function pushSample(dt: number): void {
  if (samplesMs.length >= MAX_SAMPLES) samplesMs.shift();
  samplesMs.push(dt);
  totalCount += 1;
  for (const cb of sampleCallbacks) cb();
}

/**
 * Begin one latency sample. Call at text-input time (beforeinput for typing);
 * the sample completes in the next animation frame, after the view updates.
 */
export function startSample() {
  const t0 = performance.now();
  requestAnimationFrame(() => {
    pushSample(performance.now() - t0);
  });
}

export const metrics = {
  get samplesMs() {
    return samplesMs;
  },
  get count() {
    return totalCount;
  },
  p50() {
    return percentile(50);
  },
  p99() {
    return percentile(99);
  },
  reset() {
    samplesMs.length = 0;
    totalCount = 0;
  },
  /** Subscribe to every completed sample (used to emit `metric` every ~50). */
  onSample(cb: () => void) {
    sampleCallbacks.push(cb);
  },
};

// Expose for chromedp / host-side tests reading inside the frame directly.
if (typeof window !== "undefined") {
  (window as any).__wysiwygMetrics = {
    get samplesMs() {
      return metrics.samplesMs;
    },
    get count() {
      return metrics.count;
    },
    p50: metrics.p50,
    p99: metrics.p99,
    reset: metrics.reset,
  };
}
