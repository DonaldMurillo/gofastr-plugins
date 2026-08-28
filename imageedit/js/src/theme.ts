// Theming across the iframe boundary (protocol-v1.md §7).
//
// CSS custom properties do not inherit across an iframe boundary, so the host
// bridges resolved token VALUES. On init/themeChanged we write a single
// <style id="imageedit-tokens">:root{ … }</style> block into the frame's own
// <head> from params.tokens (a {"--name":"value"} map). Because this <style>
// is appended AFTER editor.css, it wins on equal :root specificity. The frame
// CSS is token-only with inline fallbacks, so it is legible before init.
//
// Light/dark is entirely token-driven here — NO prefers-color-scheme gating
// (the repo rule: theme is host-driven).

const STYLE_ID = "imageedit-tokens";

export function applyTokens(tokens: unknown): void {
  if (!tokens || typeof tokens !== "object") return;
  let rules = ":root{";
  let count = 0;
  for (const [name, value] of Object.entries(tokens as Record<string, unknown>)) {
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
 * AFTER applying init/themeChanged tokens — the §8a round-trip check that the
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
