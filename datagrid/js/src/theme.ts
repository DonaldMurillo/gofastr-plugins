// Theming across the iframe boundary (protocol-v1.md §7), plus the AG Grid
// Theming API mapping.
//
// CSS custom properties do not inherit across an iframe boundary, so the host
// bridges resolved token VALUES. On init/themeChanged we write a single
// <style id="datagrid-tokens">:root{ … }</style> block into the frame's own
// <head> from params.tokens (a {"--name":"value"} map). Because this <style>
// is appended AFTER grid.css, it wins on equal :root specificity. The frame
// CSS is token-only with inline fallbacks, so it is legible before init.
//
// Light/dark is entirely token-driven here — NO prefers-color-scheme gating
// (the repo rule: theme is host-driven). The same token map also feeds AG
// Grid's Theming API (themeQuartz.withParams) so the grid's own chrome —
// header backgrounds, borders, selection, hover — matches the host palette by
// construction instead of by shipped hex.
//
// Styling approach note (docs/datagrid.md): AG Grid v33+ injects its theme
// CSS at runtime via constructed stylesheets / a <style> element. The
// platform's framedCSP carries style-src <origin> 'unsafe-inline', so the
// injection is permitted; this is verified in WebKit by the e2e suite rather
// than assumed.

import { themeQuartz } from "ag-grid-community";
import type { Theme } from "ag-grid-community";

const STYLE_ID = "datagrid-tokens";

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

/**
 * Map bridged host tokens onto an AG Grid Theming API theme. The param names
 * are the quartz theme's own; the values are the host's resolved tokens, so a
 * palette flip on the host re-resolves here with zero bespoke hex. Param set
 * is deliberately small — every added param is another quartz-version
 * coupling to re-verify in WebKit.
 */
export function gridTheme(tokens: unknown): Theme {
  const t = tokens && typeof tokens === "object" ? (tokens as Record<string, unknown>) : {};
  return themeQuartz.withParams({
    accentColor: token(t, "--color-primary", "#6366F1"),
    backgroundColor: token(t, "--color-surface", "#FFFFFF"),
    foregroundColor: token(t, "--color-text", "#1C2024"),
    borderColor: token(t, "--color-border", "#E3E6EA"),
    headerBackgroundColor: token(t, "--color-surface-soft", "#F4F4F5"),
    headerTextColor: token(t, "--color-text-muted", "#5B6470"),
    fontFamily: token(t, "--font-body", "system-ui, -apple-system, sans-serif"),
  });
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
