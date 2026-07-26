// DOM construction + host-token theming.
//
// el(): a tiny hyperscript used to build the toolbar/sidebar/text-layer DOM
// imperatively. Holding element references instead of re-querying by id keeps
// the lookup surface to a single root grab in boot().
//
// applyTokens(): writes a :root{} block from init.tokens / themeChanged.tokens.
// This is the ONLY way host palette reaches the frame — CSS custom properties
// do not inherit across the iframe boundary, so the host bridges resolved
// values and we mirror them into a single <style>. Plugin CSS is token-only
// (var(--color-…) with inline fallbacks), so it matches the host palette by
// construction in both light and dark schemes.

export type ElAttrs = {
  id?: string;
  cls?: string;
  text?: string;
  title?: string;
  role?: string;
  type?: string;
  ariaLabel?: string;
  ariaLive?: string;
  ariaExpanded?: boolean | null;
  ariaHidden?: boolean | null;
  ariaControls?: string | null;
  ariaSelected?: boolean | string | null;
  ariaPressed?: boolean | string | null;
  tabIndex?: number;
  disabled?: boolean;
  checked?: boolean;
  ariaModal?: boolean | null;
  data?: Record<string, string>;
  style?: Record<string, string>;
  attrs?: Record<string, string>;
  on?: Record<string, (e: Event) => void>;
};

export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs?: ElAttrs,
  children?: Node[]
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (attrs) {
    if (attrs.id) node.id = attrs.id;
    if (attrs.cls) node.className = attrs.cls;
    if (attrs.text !== undefined) node.textContent = attrs.text;
    if (attrs.title) node.title = attrs.title;
    if (attrs.role) node.setAttribute("role", attrs.role);
    if (attrs.type !== undefined) node.setAttribute("type", attrs.type);
    if (attrs.ariaLabel) node.setAttribute("aria-label", attrs.ariaLabel);
    if (attrs.ariaLive) node.setAttribute("aria-live", attrs.ariaLive);
    if (attrs.ariaControls !== undefined && attrs.ariaControls !== null) node.setAttribute("aria-controls", attrs.ariaControls);
    if (attrs.ariaExpanded !== undefined && attrs.ariaExpanded !== null) node.setAttribute("aria-expanded", String(attrs.ariaExpanded));
    if (attrs.ariaHidden !== undefined && attrs.ariaHidden !== null) node.setAttribute("aria-hidden", String(attrs.ariaHidden));
    if (attrs.ariaSelected !== undefined && attrs.ariaSelected !== null) node.setAttribute("aria-selected", String(attrs.ariaSelected));
    if (attrs.ariaPressed !== undefined && attrs.ariaPressed !== null) node.setAttribute("aria-pressed", String(attrs.ariaPressed));
    if (attrs.tabIndex !== undefined) node.tabIndex = attrs.tabIndex;
    if (attrs.disabled !== undefined) {
      const btn = node as HTMLElement & { disabled?: boolean };
      if (typeof btn.disabled === "boolean") btn.disabled = attrs.disabled;
      else node.setAttribute("aria-disabled", String(attrs.disabled));
    }
    if (attrs.checked !== undefined) {
      const chk = node as HTMLElement & { checked?: boolean };
      if (typeof chk.checked === "boolean") chk.checked = attrs.checked;
    }
    if (attrs.ariaModal !== undefined && attrs.ariaModal !== null) node.setAttribute("aria-modal", String(attrs.ariaModal));
    if (attrs.data) for (const [k, v] of Object.entries(attrs.data)) node.setAttribute("data-" + k, v);
    if (attrs.style) for (const [k, v] of Object.entries(attrs.style)) node.style.setProperty(k, v);
    if (attrs.attrs) for (const [k, v] of Object.entries(attrs.attrs)) node.setAttribute(k, v);
    if (attrs.on) for (const [k, v] of Object.entries(attrs.on)) node.addEventListener(k, v);
  }
  if (children) for (const c of children) node.appendChild(c);
  return node;
}

// Type guard for the tokens-bearing params shape shared by init/themeChanged.
export function hasTokens(p: unknown): p is { tokens: unknown } {
  return !!p && typeof p === "object" && "tokens" in p;
}

export function applyTokens(tokens: unknown): void {
  if (!tokens || typeof tokens !== "object") return;
  const entries = Object.entries(tokens).filter(
    ([k, v]) => typeof k === "string" && k.startsWith("--") && typeof v === "string"
  ) as Array<[string, string]>;
  if (entries.length === 0) return; // no tokens — keep fallback :root in viewer.css
  let css = ":root {\n";
  for (const [k, v] of entries) css += "  " + k + ": " + v + ";\n";
  css += "}";
  let style = document.getElementById("pdf-tokens");
  if (!(style instanceof HTMLStyleElement)) {
    style = document.createElement("style");
    style.id = "pdf-tokens";
    document.head.appendChild(style);
  }
  style.textContent = css;
}
