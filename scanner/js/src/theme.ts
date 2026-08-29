// Theming across the iframe boundary (protocol-v1.md §7).
//
// CSS custom properties do not inherit across an iframe boundary, so the host
// bridges resolved token VALUES. On init/themeChanged we write a single
// <style id="scan-tokens">:root{ … }</style> block into the frame's own
// <head> from params.tokens (a {"--name":"value"} map). Because this <style>
// is appended AFTER scan.css, it wins on equal :root specificity — and
// scan.css declares its whole default palette on :root up front
// (docs/demo-page-design.md), so the frame is legible before init and there
// is exactly one reviewable place to check the fallbacks.
const STYLE_ID = "scan-tokens";

export function applyTokens(tokens: unknown): void {
  if (!tokens || typeof tokens !== "object") return;
  let rules = ":root{";
  let count = 0;
  for (const [name, value] of Object.entries(tokens)) {
    if (!name.startsWith("--") || typeof value !== "string" || value === "") continue;
    rules += `${name}:${value};`;
    count += 1;
  }
  rules += "}";
  if (count === 0) return;

  let style = document.getElementById(STYLE_ID);
  if (!style) {
    style = document.createElement("style");
    style.id = STYLE_ID;
    document.head.appendChild(style);
  }
  style.textContent = rules;
}

/** Set a data-scheme marker (debug/observability only — CSS stays token-only). */
export function applyScheme(scheme: unknown): void {
  if (scheme) {
    document.documentElement.setAttribute("data-color-scheme", String(scheme));
  }
}

/**
 * sampleAppliedTokens resolves the given token NAMES from the frame's own :root
 * AFTER applying init/themeChanged tokens — the round-trip check that the
 * crossed values actually landed.
 */
export function sampleAppliedTokens(tokens: unknown, names: string[]): Record<string, string> {
  const out: Record<string, string> = {};
  const cs = getComputedStyle(document.documentElement);
  for (const name of names) {
    const v = cs.getPropertyValue(name);
    if (v) out[name] = v.trim();
  }
  return out;
}
