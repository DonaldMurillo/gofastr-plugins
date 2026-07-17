// Frame entry: the sandboxed-iframe bundle (assets/editor.js). Auto-boots in
// the frame document and speaks protocol v1 over window.parent.postMessage.
// This entry NEVER calls setTransport/mountTrusted — the opaque-origin frame
// path stays exactly as protocol-v1.md froze it.
import { bootFrame } from "./editor.ts";

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", bootFrame, { once: true });
} else {
  bootFrame();
}
