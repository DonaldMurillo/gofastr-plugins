// Theming across the iframe boundary (protocol-v1.md §7).
//
// CSS custom properties do not inherit across an iframe boundary, so the host
// bridges resolved token VALUES. On init/themeChanged we write a single
// <style id="richtext-tokens">:root{ … }</style> block into the frame's own
// <head> from params.tokens (a {"--name":"value"} map). Because this <style> is
// appended AFTER editor.css, it wins on equal :root specificity. The editor CSS
// is token-only with inline fallbacks, so the frame is legible before init.
//
// Light/dark is entirely token-driven here — there is NO prefers-color-scheme
// gating (the repo rule: theme is host-driven).

const STYLE_ID = "richtext-tokens";

function normalizeName(name: string): string {
  return String(name).startsWith("--") ? String(name) : `--${name}`;
}

export function applyTokens(tokens: unknown) {
  if (!tokens || typeof tokens !== "object") return;
  let rules = ":root{";
  let count = 0;
  for (const [name, value] of Object.entries(tokens)) {
    if (value == null) continue;
    rules += `${normalizeName(name)}:${String(value)};`;
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
export function applyScheme(scheme: unknown) {
  if (scheme) {
    document.documentElement.setAttribute("data-color-scheme", String(scheme));
  }
}

/**
 * sampleAppliedTokens resolves the given token NAMES from the frame's own :root
 * AFTER they were applied, returning a {"--name":"resolvedValue"} map. Used by
 * the Phase-0 test-observability hook (protocol-v1.md §8a): the frame reports
 * back what it actually resolved so a host-side test can assert the crossed
 * token equals the host's value.
 */
export function sampleAppliedTokens(tokens: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (!tokens || typeof tokens !== "object") return out;
  const cs = getComputedStyle(document.documentElement);
  for (const name of Object.keys(tokens)) {
    const n = normalizeName(name);
    const v = cs.getPropertyValue(n);
    if (v != null) out[n] = String(v).trim();
  }
  return out;
}
