// Theming across the iframe boundary (protocol-v1.md §7), plus the xterm.js
// theme mapping.
//
// CSS custom properties do not inherit across an iframe boundary, so the host
// bridges resolved token VALUES. On init/themeChanged we write a single
// <style id="logstream-tokens">:root{ … }</style> block into the frame's own
// <head> from params.tokens (a {"--name":"value"} map). Because this <style>
// is appended AFTER term.css, it wins on equal :root specificity. The frame
// CSS is token-only with inline fallbacks, so it is legible before init.
//
// Light/dark is entirely token-driven here — NO prefers-color-scheme gating
// (the repo rule: theme is host-driven). The same token map also feeds
// xterm's theme (background/foreground/cursor/selection), so the terminal
// chrome matches the host palette by construction instead of by shipped hex.
// The ANSI-16 palette stays xterm's own: the design system has no
// per-ANSI-slot tokens, and the demo's generator picks ANSI colours that
// stay legible on both ladders.

const STYLE_ID = "logstream-tokens";

/** Resolve one token from the bridged map, with a fallback. */
function token(tokens: Record<string, unknown>, name: string, fallback: string): string {
  const v = tokens[name];
  return typeof v === "string" && v !== "" ? v : fallback;
}

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

/** xterm's ITheme subset, rebuilt from the bridged token map. */
export interface TermTheme {
  background: string;
  foreground: string;
  cursor: string;
  cursorAccent: string;
  selectionBackground: string;
  selectionForeground: string | undefined;
}

export function termTheme(tokens: unknown): TermTheme {
  const t = tokens && typeof tokens === "object" ? (tokens as Record<string, unknown>) : {};
  return {
    background: token(t, "--color-surface", "#ffffff"),
    foreground: token(t, "--color-text", "#1c2024"),
    cursor: token(t, "--color-primary", "#e0a040"),
    cursorAccent: token(t, "--color-primary-fg", "#ffffff"),
    // Selection rides the framework's translucent selection token when the
    // host bridges one; otherwise a translucent primary tint.
    selectionBackground: token(t, "--color-selection", "rgba(224,160,64,0.30)"),
    selectionForeground: undefined,
  };
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
