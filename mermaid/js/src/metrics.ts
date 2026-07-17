// Render-latency rig (soft gate — protocol-v1.md §8 adapted for Mermaid).
//
// Measures full render latency: startSample() records t0=performance.now() just
// before mermaid.render is invoked; finishSample() records t1 right after the
// SVG is swapped into the DOM; sample = t1-t0. A ring buffer keeps the last N
// samples. window.__mermaidMetrics exposes {samplesMs, count, p50(), p99(),
// reset()} for tests reading directly inside the frame. diagram.ts posts a
// `metric` event every ~10 renders and on requestSave.
//
// Unlike the Rich Text keystroke rig this is NOT a hard go/no-go gate (diagram
// rendering is inherently bursty and async); it is reported for observability.

const MAX_SAMPLES = 1000;
const samplesMs: number[] = [];
let totalCount = 0;
const sampleCallbacks: Array<() => void> = [];
let currentT0 = 0;

declare global {
  interface Window {
    /** Test-observability mirror of the in-frame render metrics. */
    __mermaidMetrics?: {
      readonly samplesMs: number[];
      readonly count: number;
      p50(): number;
      p99(): number;
      reset(): void;
    };
  }
}

function percentile(p: number): number {
  if (!samplesMs.length) return 0;
  // nearest-rank percentile over the in-buffer samples
  const sorted = samplesMs.slice().sort((a, b) => a - b);
  const rank = Math.ceil((p / 100) * sorted.length);
  const idx = Math.min(sorted.length - 1, Math.max(0, rank - 1));
  return sorted[idx];
}

function pushSample(dt: number): void {
  if (samplesMs.length >= MAX_SAMPLES) samplesMs.shift();
  samplesMs.push(dt);
  totalCount += 1;
  for (let i = 0; i < sampleCallbacks.length; i++) sampleCallbacks[i]();
}

/** Begin one render-latency sample (call immediately before mermaid.render). */
export function startSample(): void {
  currentT0 = performance.now();
}

/** Complete the sample started by startSample() (call right after the SVG swap). */
export function finishSample(): number {
  if (currentT0 === 0) return 0;
  const dt = performance.now() - currentT0;
  currentT0 = 0;
  pushSample(dt);
  return dt;
}

export const metrics = {
  get samplesMs(): number[] {
    return samplesMs;
  },
  get count(): number {
    return totalCount;
  },
  p50(): number {
    return percentile(50);
  },
  p99(): number {
    return percentile(99);
  },
  reset(): void {
    samplesMs.length = 0;
    totalCount = 0;
    currentT0 = 0;
  },
  /** Subscribe to every completed sample (used to emit `metric` every ~10). */
  onSample(cb: () => void): void {
    sampleCallbacks.push(cb);
  },
};

// Expose for host-side tests reading inside the frame directly.
if (typeof window !== "undefined") {
  window.__mermaidMetrics = {
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
