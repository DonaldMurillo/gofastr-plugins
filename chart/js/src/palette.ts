// Series palette derived from bridged theme tokens.
//
// The SSR SVG (chart/ssr/render.go) colors its series through the semantic
// color tokens plus CSS color-mix rules; this module computes the SAME
// colors in JS from the bridged token values so both renderers read the
// same tokens (light AND dark). Slots 0–4 pass the token value through
// verbatim (any resolved CSS color string works as a Plot attribute);
// slots 5–11 are channel-wise sRGB mixes mirroring the .gofastr-chart-sN
// rules exactly: [base token, other-weight] per slot, mixed with text.

export interface TokenSet {
  primary: string;
  info: string;
  success: string;
  accent: string;
  danger: string;
  text: string;
  background: string;
}

const FALLBACK: TokenSet = {
  primary: "#4f46e5",
  info: "#1d4ed8",
  success: "#166534",
  accent: "#7c3aed",
  danger: "#b91c1c",
  text: "#1c2024",
  background: "#ffffff",
};

// A mix slot: base token at weight (1 - w), text at weight w.
interface Mix {
  base: keyof TokenSet;
  w: number;
}

// Mirrors the .gofastr-chart-s0..s11 rules in chart/ssr/render.go.
const SLOTS: ReadonlyArray<keyof TokenSet | Mix> = [
  "primary", // s0: the amber accent on GoFastr pages
  "info", // s1: maximally distinct from s0 at 1px
  "success", // s2
  "accent", // s3
  "danger", // s4
  { base: "info", w: 0.45 }, // s5: info 55% + text
  { base: "primary", w: 0.55 }, // s6: primary 45% + text
  { base: "success", w: 0.55 }, // s7: success 45% + text
  { base: "danger", w: 0.5 }, // s8: danger 50% + text
  { base: "accent", w: 0.5 }, // s9: accent 50% + text
  { base: "info", w: 0.65 }, // s10: info 35% + text
  { base: "primary", w: 0.75 }, // s11: primary 25% + text
];

function parseHex(v: unknown, fallback: string): [number, number, number] {
  if (typeof v === "string") {
    const m = /^#([0-9a-f]{6})$/i.exec(v.trim());
    if (m) {
      const n = parseInt(m[1], 16);
      return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
    }
  }
  const m = /^#([0-9a-f]{6})$/i.exec(fallback)!;
  const n = parseInt(m[1], 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

/** Extract the tokens the palette needs from a bridged token map. */
export function tokenSet(tokens: unknown): TokenSet {
  const map = tokens && typeof tokens === "object" ? (tokens as Record<string, string>) : {};
  const pick = (name: string, fallback: string) => map[`--color-${name}`] ?? fallback;
  return {
    primary: pick("primary", FALLBACK.primary),
    info: pick("info", FALLBACK.info),
    success: pick("success", FALLBACK.success),
    accent: pick("accent", FALLBACK.accent),
    danger: pick("danger", FALLBACK.danger),
    text: pick("text", FALLBACK.text),
    background: pick("background", FALLBACK.background),
  };
}

/**
 * The color for series index i, identical in spirit to the SSR CSS rules:
 * direct slots pass the resolved token value through (the same string the
 * host page's var(--color-*) computes to); mix slots are a channel-wise
 * sRGB mix of the base token and text. (color-mix rounds at paint time;
 * JS rounds at construction — the difference is sub-visual and untested.)
 */
export function seriesColor(set: TokenSet, i: number): string {
  const slot = SLOTS[i % SLOTS.length];
  if (typeof slot === "string") return set[slot];
  const base = parseHex(set[slot.base], FALLBACK[slot.base]);
  const text = parseHex(set.text, FALLBACK.text);
  const w = slot.w;
  return rgbHex([
    Math.round(base[0] * (1 - w) + text[0] * w),
    Math.round(base[1] * (1 - w) + text[1] * w),
    Math.round(base[2] * (1 - w) + text[2] * w),
  ]);
}

function rgbHex([r, g, b]: readonly [number, number, number]): string {
  return `#${((r << 16) | (g << 8) | b).toString(16).padStart(6, "0")}`;
}

/** The full palette for n series. */
export function palette(set: TokenSet, n: number): string[] {
  return Array.from({ length: n }, (_, i) => seriesColor(set, i));
}
