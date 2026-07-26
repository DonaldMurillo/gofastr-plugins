// Isolation probes (§8a) + frame capability probe.
//
// Both empirically prove the third-party guarantee and feed the `ready`/`caps`
// events the host asserts on. NONE of this logs to the console: the webkit and
// chromedp gates demand ZERO console messages, so every probe swallows its own
// throw and the clipboard promise's rejection is captured (never an unhandled
// rejection, which would trip the zero-console-error gate).

export interface IsolationProbes {
  cookieEmpty: boolean;
  parentBlocked: boolean;
  storageBlocked: boolean;
}

// Under sandbox="allow-scripts" (no allow-same-origin) each cross-boundary
// access throws — the third-party guarantee. Reported in `ready`.
export function isolationProbes(): IsolationProbes {
  let cookieEmpty = false;
  try { cookieEmpty = document.cookie === ""; } catch { cookieEmpty = true; }
  let parentBlocked = false;
  try { void window.parent.document; } catch { parentBlocked = true; }
  let storageBlocked = false;
  try { void window.localStorage; } catch { storageBlocked = true; }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

export interface FrameCaps {
  hasPrint: boolean;          // typeof window.print === "function" (calling it is blocked)
  clipboardWrite: string;     // "ok" or the rejection/error message
  allowedFeatures: string[];  // document.featurePolicy.allowedFeatures() if present
  origin: string;             // window.location.origin — "null" under opaque sandbox
}

// Probe what the sandbox + opaque origin block: clipboard write, print
// availability, featurePolicy, and the frame's own origin string. The clipboard
// promise is awaited and its rejection captured so it never surfaces as an
// unhandled rejection.
export async function probeCaps(): Promise<FrameCaps> {
  let clipboardWrite = "not-tested";
  try {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
      await navigator.clipboard.writeText("pdf-viewer-probe");
      clipboardWrite = "ok";
    } else {
      clipboardWrite = "no navigator.clipboard.writeText";
    }
  } catch (e: unknown) {
    clipboardWrite = e instanceof Error ? e.message : String(e);
  }
  let allowedFeatures: string[] = [];
  const docFP = document as unknown as { featurePolicy?: { allowedFeatures: () => string[] } };
  if (docFP.featurePolicy && typeof docFP.featurePolicy.allowedFeatures === "function") {
    try { allowedFeatures = docFP.featurePolicy.allowedFeatures(); } catch { allowedFeatures = []; }
  }
  return {
    hasPrint: typeof window.print === "function",
    clipboardWrite,
    allowedFeatures,
    origin: window.location.origin,
  };
}
