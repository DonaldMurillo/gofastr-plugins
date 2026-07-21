// Theming across the iframe boundary (protocol-v1.md §7) — Monaco edition.
//
// CSS custom properties do not inherit across an iframe boundary, so the host
// bridges resolved token VALUES. On init/themeChanged we write a single
// <style id="monaco-tokens">:root{ ... }</style> block into the frame's own
// <head> from params.tokens (a {"--name":"value"} map). The editor CSS is
// token-only with inline fallbacks, so the frame is legible before init.
//
// The same token map also drives a Monaco theme registered via
// monaco.editor.defineTheme, so the editor surface (gutter, selection, cursor)
// matches the host palette. Light/dark is entirely token-driven — there is NO
// prefers-color-scheme gating (the repo rule: theme is host-driven).

const STYLE_ID = "monaco-tokens";

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
  if (scheme) {
    document.documentElement.setAttribute("data-color-scheme", String(scheme));
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

/** Read a single token value from a bridged bag, "" if absent. */
function token(map: TokenMap | null, name: string): string {
  if (!map) return "";
  const v = map[name] ?? map[normalizeName(name)];
  return v != null ? v.trim() : "";
}

/** Monaco theme rule set returned to editor.defineTheme. */
export interface MonacoThemeData {
  /** Monaco base theme to inherit: "vs" (light) or "vs-dark". */
  base: "vs" | "vs-dark";
  inherit: boolean;
  rules: Array<{ token: string; foreground?: string }>;
  colors: Record<string, string>;
}

/**
 * Map bridged host tokens onto a Monaco theme. Monaco's theming uses specific
 * color keys (editor.background, editor.foreground, …); we feed the host
 * palette into the common ones so the editor matches the surrounding UI. Any
 * token the host did not bridge is omitted and Monaco falls back to the base.
 */
export function monacoThemeData(tokens: unknown, scheme: string): MonacoThemeData {
  const map = toTokenMap(tokens);
  const background = token(map, "--color-background");
  const surface = token(map, "--color-surface");
  const text = token(map, "--color-text");
  const textMuted = token(map, "--color-text-muted");
  const border = token(map, "--color-border");
  const primary = token(map, "--color-primary");

  const base: "vs" | "vs-dark" = scheme === "dark" ? "vs-dark" : "vs";

  const colors: Record<string, string> = {};
  // editor.background MUST be opaque for Monaco; fall back to the base if the
  // host bridged a transparent/empty token.
  const bg = background || surface;
  if (bg) colors["editor.background"] = bg;
  if (text) colors["editor.foreground"] = text;
  if (textMuted) colors["editorLineNumber.foreground"] = textMuted;
  if (border) {
    colors["editor.lineHighlightBackground"] = border;
    colors["editorGutter.background"] = bg || surface;
  }
  if (primary) {
    colors["editorCursor.foreground"] = primary;
    colors["editor.selectionBackground"] = primary;
  }

  const rules: Array<{ token: string; foreground?: string }> = [];
  if (text) rules.push({ token: "", foreground: text.replace("#", "") });
  if (textMuted) rules.push({ token: "comment", foreground: textMuted.replace("#", "") });
  if (primary) rules.push({ token: "keyword", foreground: primary.replace("#", "") });

  return { base, inherit: true, rules, colors };
}
