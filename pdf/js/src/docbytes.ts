// documentBytes chunk assembler.
//
// pdf/host/adapter.js relays the PDF into the frame as chunked `documentBytes`
// events `{ reqId, seq, total, bytes }` (≈4 MiB each). A large file must not
// cross the boundary as one giant structured-clone, so the host slices it and
// the frame reassembles. The adapter ALSO emits a single-shot `loadBytes`
// fallback after the chunks for backward compatibility — the viewer's
// `loading || state.rendered` guard ensures only the FIRST delivery renders.
//
// Defensive: out-of-order chunks, duplicates, and a stream that never completes
// are all handled. An incomplete stream surfaces a clear error rather than
// hanging on a spinner forever (the brief's requirement).

export interface DocumentBytesChunk {
  reqId: string;
  seq: number;   // 0-based
  total: number; // total chunks expected
  bytes: Uint8Array;
}

interface Pending {
  total: number;
  seen: Map<number, Uint8Array>;
  received: number; // distinct seqs accepted
  createdAt: number;
}

// How long to wait for a chunk stream to complete before giving up. Generous:
// a 32 MiB doc crosses in 8 chunks and the host emits them in a tight loop, so
// completion is normally sub-second. 30 s absorbs a slow host without hanging
// a real user on a spinner.
const ASSEMBLE_TIMEOUT_MS = 30_000;

type Sink = (bytes: Uint8Array) => void;
type ErrorSink = (message: string) => void;

export class DocumentBytesAssembler {
  private readonly pending = new Map<string, Pending>();
  private timer: number | null = null;
  private readonly onReady: Sink;
  private readonly onError: ErrorSink;

  constructor(onReady: Sink, onError: ErrorSink) {
    this.onReady = onReady;
    this.onError = onError;
  }

  /** Ingest one `documentBytes` chunk. */
  push(chunk: DocumentBytesChunk): void {
    if (!chunk || typeof chunk.reqId !== "string") return;
    const { reqId, seq, total, bytes } = chunk;
    if (!Number.isFinite(seq) || !Number.isFinite(total) || total <= 0) return;
    if (!(bytes instanceof Uint8Array)) return;
    if (seq < 0 || seq >= total) return; // out of range — ignore

    let p = this.pending.get(reqId);
    if (!p) {
      p = { total, seen: new Map(), received: 0, createdAt: Date.now() };
      this.pending.set(reqId, p);
      this.ensureTimer();
    } else if (p.total !== total) {
      // The host should not change `total` mid-stream for the same reqId, but
      // if it does, reset on the latest declaration.
      p.total = total;
    }
    if (p.seen.has(seq)) return; // duplicate — defensive
    p.seen.set(seq, bytes);
    p.received++;

    if (p.received >= p.total) {
      this.pending.delete(reqId);
      this.onReady(this.assemble(p));
    }
  }

  private assemble(p: Pending): Uint8Array {
    let len = 0;
    for (let i = 0; i < p.total; i++) {
      const b = p.seen.get(i);
      if (!b) {
        // Should be unreachable (received >= total implies every seq seen),
        // but a defensive guard keeps the assembler total-corruption-proof.
        throw new Error("documentBytes stream completed with a missing chunk");
      }
      len += b.length;
    }
    const out = new Uint8Array(len);
    let off = 0;
    for (let i = 0; i < p.total; i++) {
      const b = p.seen.get(i)!;
      out.set(b, off);
      off += b.length;
    }
    return out;
  }

  private ensureTimer(): void {
    if (this.timer !== null) return;
    this.timer = window.setInterval(() => this.sweep(), 2000);
  }

  private sweep(): void {
    const now = Date.now();
    let alive = 0;
    for (const [reqId, p] of this.pending) {
      if (now - p.createdAt > ASSEMBLE_TIMEOUT_MS) {
        this.pending.delete(reqId);
        this.onError(
          "document stream did not complete in time (" + p.received + "/" + p.total +
          " chunks received)"
        );
      } else {
        alive++;
      }
    }
    if (alive === 0 && this.timer !== null) {
      window.clearInterval(this.timer);
      this.timer = null;
    }
  }

  /** Test seam + teardown: stop the sweep timer. */
  dispose(): void {
    if (this.timer !== null) {
      window.clearInterval(this.timer);
      this.timer = null;
    }
    this.pending.clear();
  }
}

/** Narrow type guard for a `documentBytes` params payload. */
export function isDocumentBytesChunk(p: unknown): p is DocumentBytesChunk {
  if (!p || typeof p !== "object") return false;
  const o = p as Record<string, unknown>;
  return (
    typeof o.reqId === "string" &&
    typeof o.seq === "number" &&
    typeof o.total === "number" &&
    o.bytes instanceof Uint8Array
  );
}
