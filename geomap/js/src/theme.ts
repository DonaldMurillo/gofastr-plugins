// Theming across the iframe boundary (protocol-v1.md §7) — Leaflet edition.
//
// CSS custom properties do not inherit across an iframe boundary, so the host
// bridges resolved token VALUES. On init/themeChanged we write a single
// <style id="map-tokens">:root{ ... }</style> block into the frame's own
// <head> from params.tokens (a {"--name":"value"} map). The map CSS is
// token-only with inline fallbacks, so the frame is legible before init.
//
// Leaflet itself is themed via these same CSS variables (the .leaflet-* CSS is
// bundled alongside map.css and reads the vars through the overrides in
// map.css). Light/dark is entirely token-driven — there is NO
// prefers-color-scheme gating (the repo rule: theme is host-driven).

const STYLE_ID = "map-tokens";

function normalizeName(name: string): string {
  return name.startsWith("--") ? name : `--${name}`;
}

/** A bridged token bag is a string->string map (values arrive as strings). */
type TokenMap = Record<string, string>;

function toTokenMap(tokens: unknown): TokenMap | null {
  if (!tokens || typeof tokens !== "object") return null;
  const out: TokenMap = {};
  for (const [name, value] of Object.entries(tokens as Record<string, unknown>)) {
    if (value == null) continue;
    out[normalizeName(name)] = String(value);
  }
  return Object.keys(out).length > 0 ? out : null;
}

export function applyTokens(tokens: unknown): void {
  const map = toTokenMap(tokens);
  if (!map) return;
  let rules = ":root{";
  for (const [name, value] of Object.entries(map)) {
    rules += `${name}:${value};`;
  }
  rules += "}";

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
  if (typeof scheme === "string") {
    document.documentElement.setAttribute("data-scheme", scheme);
  }
}

/**
 * Resolve the given token NAMES from the frame's own :root AFTER they were
 * applied, returning a {"--name":"resolvedValue"} map. Used by the test-
 * observability hook: the frame reports what it actually resolved so a host-
 * side test can assert the crossed token equals the host's value.
 */
export function sampleAppliedTokens(tokens: unknown): TokenMap {
  const out: TokenMap = {};
  if (!tokens || typeof tokens !== "object") return out;
  const cs = getComputedStyle(document.documentElement);
  for (const name of Object.keys(tokens as Record<string, unknown>)) {
    const n = normalizeName(name);
    const v = cs.getPropertyValue(n);
    if (v != null) out[n] = String(v).trim();
  }
  return out;
}
