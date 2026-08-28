// GoFastr chart frame — in-frame entry point (protocol v1, chart-v1).
//
// Boots inside the opaque-origin sandboxed iframe and speaks protocol v1 to
// the host over window.parent.postMessage ONLY. On load it announces
// `ready`; the host replies `init` carrying the canonical chart spec; the
// spec is normalized (src/spec.ts — the twin of chart/ssr/spec.go) and
// rendered with Observable Plot using EXPLICIT domains, tick counts, and
// tick formats, so the hydrated chart's axis labels are the same strings
// the server SVG rendered (that agreement is the plugin's whole point).
//
// The frame never edits the doc: requestSave echoes the current spec.
// Interactivity in v1 is hover tooltips (Plot `tip`) on the data marks.

import * as Plot from "@observablehq/plot";

import { createRouter, sendEvent, type HandlerMap } from "./protocol";
import { extents, minXGap, normalizeSpec, SCHEMA_VERSION, SpecError, type Spec } from "./spec";
import { makeTickFormat } from "./ticks";
import { palette, tokenSet, type TokenSet } from "./palette";

const ROOT_SELECTOR = "#chart-root";
const MIN_HEIGHT = 360;

interface FlatPoint {
  x: number;
  y: number;
  name: string;
}

/** The figure Plot.plot returns: an element plus the scale accessor. */
type Figure = ReturnType<typeof Plot.plot>;

// --- runtime state (single instance per frame) ---
let root: HTMLElement | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;
let spec: Spec | null = null;
let tokens: TokenSet = tokenSet(null);
let initialized = false;

function hasCap(caps: unknown, name: string): boolean {
  return Array.isArray(caps) && caps.includes(name);
}

function showError(message: string): void {
  if (!root) return;
  root.replaceChildren();
  const note = document.createElement("div");
  note.className = "chart-note chart-note-error";
  note.setAttribute("role", "alert");
  note.textContent = message; // textContent: never markup
  root.append(note);
}

// ---------------------------------------------------------------------------
// Rendering

function renderChart(): void {
  if (!root || !spec) return;
  const dom = extents(spec);
  const xFmt = makeTickFormat(dom.x, spec.axes.x.tickCount);
  const yFmt = makeTickFormat(dom.y, spec.axes.y.tickCount);

  const data: FlatPoint[] = [];
  for (const ser of spec.series) {
    for (const p of ser.points) data.push({ x: p.x, y: p.y, name: ser.name });
  }
  const names = spec.series.map((s) => s.name);
  const colors = palette(tokens, names.length);

  // Explicit domain + ticks + tickFormat on BOTH scales is the agreement
  // surface: the values Plot labels are exactly d3's ticks(domain, count),
  // and the strings are the same v.toFixed(p) the server SVG emits.
  // Grid, stroke weight, and the r=3.5 value dots mirror the SSR's
  // chartStyleCSS/writeDots treatment so both renderers carry the same
  // visual weight — only interaction (tip) is frame-only.
  const marks: Plot.Markish[] = [];
  const valueDots = (): Plot.Markish =>
    Plot.dot(data, {
      x: "x",
      y: "y",
      fill: "name",
      r: 3.5,
      stroke: tokens.background,
      strokeWidth: 1.5,
      tip: true,
    });
  switch (spec.type) {
    case "line":
      marks.push(
        Plot.line(data, { x: "x", y: "y", z: "name", stroke: "name", strokeWidth: 2.5, sort: "x" }),
        // Value dots double as hover targets — a filled dot hit-tests, so
        // no separate transparent tip layer is needed.
        valueDots()
      );
      break;
    case "area":
      marks.push(
        // Plot.areaY STACKS series by default (maybeStackY) — wrong here:
        // the SSR fills each series independently down to y=0, so the
        // frame must too or the two renderers disagree on geometry. Raw
        // Plot.area with an explicit zero baseline matches writeArea.
        Plot.area(data, {
          x1: "x",
          x2: "x",
          y1: 0,
          y2: "y",
          z: "name",
          fill: "name",
          fillOpacity: 0.18,
          sort: "x",
        }),
        Plot.lineY(data, { x: "x", y: "y", z: "name", stroke: "name", strokeWidth: 2.5, sort: "x" }),
        valueDots()
      );
      break;
    case "bar": {
      // barY would force a BAND x scale and stack — both wrong for
      // chart-v1's quantitative-x bar semantics (and fatal for tick
      // agreement: a band axis has no d3 ticks). rectY over the LINEAR x
      // scale with explicit x1/x2 AND explicit y1/y2 (zero baseline, not
      // Plot's maybeStackY stacking) keeps the geometry identical to the
      // SSR's writeBars: the group at each x spans 0.8× the min adjacent-x
      // gap, one slot per series, each bar 90% of its slot.
      const n = spec.series.length;
      const g = minXGap(spec);
      // No usable gap (single distinct x): unit fallback, undodged.
      const slot = g === Infinity ? 1 : (0.8 * g) / n;
      const barW = g === Infinity ? 1 : 0.9 * slot;
      // Per-series dodge offset in data units (series i centers on its
      // slot, offset from the group center — mirrors ssr writeBars).
      const dodge: Record<string, number> = {};
      spec.series.forEach((s, i) => { dodge[s.name] = (i - (n - 1) / 2) * slot; });
      marks.push(
        Plot.rectY(data, {
          x1: (d: FlatPoint) => d.x + dodge[d.name] - barW / 2,
          x2: (d: FlatPoint) => d.x + dodge[d.name] + barW / 2,
          y1: 0,
          y2: "y",
          fill: "name",
          tip: true,
        })
      );
      break;
    }
    case "scatter":
      marks.push(
        Plot.dot(data, {
          x: "x",
          y: "y",
          fill: "name",
          r: 4,
          stroke: tokens.background,
          strokeWidth: 1.5,
          tip: true,
        })
      );
      break;
  }

  const width = Math.max(320, Math.floor(root.clientWidth || 680));
  const height = 420;
  let figure: Figure | null = null;
  try {
    figure = Plot.plot({
      className: "gofastr-plot",
      width,
      height,
      title: spec.title || undefined,
      style: { color: "var(--color-text, #1c2024)" },
      x: {
        domain: dom.x,
        nice: false,
        label: spec.axes.x.label || null,
        labelArrow: false,
        grid: true,
        ticks: spec.axes.x.tickCount,
        tickFormat: xFmt as unknown as string,
      },
      y: {
        domain: dom.y,
        nice: false,
        label: spec.axes.y.label || null,
        labelArrow: false,
        grid: true,
        ticks: spec.axes.y.tickCount,
        tickFormat: yFmt as unknown as string,
      },
      color: {
        domain: names,
        range: colors,
        ...(spec.options.legend ? { legend: true } : {}),
      },
      marks,
    });
  } catch (err) {
    showError(`Chart failed to render: ${err instanceof Error ? err.message : String(err)}`);
    sendEvent("resize", { height: MIN_HEIGHT });
    return;
  }

  const xd = domainOf(figure, "x");
  const yd = domainOf(figure, "y");

  root.replaceChildren(figure);

  // Machine-readable agreement hooks, mirroring the SSR svg's data-domain
  // attributes: the frame's ACTUAL scale domains after Plot applied them.
  // NOTE: select the chart svg explicitly — when a legend renders, the
  // figure's FIRST svg descendant is a 15×15 legend swatch.
  const svg =
    figure.tagName === "svg" ? figure : figure.querySelector(":scope > svg");
  if (svg && xd && yd) {
    svg.setAttribute("data-domain-x", `${xd[0]},${xd[1]}`);
    svg.setAttribute("data-domain-y", `${yd[0]},${yd[1]}`);
  }
  postHeight();
}


/** Read a scale's applied numeric domain off a rendered figure, if linear.
 *  Plot exposes each scale as a descriptor whose `domain` is the applied
 *  ARRAY (see exposeScale in Plot's scales.js). */
function domainOf(figure: Figure | null, name: "x" | "y"): [number, number] | null {
  const scale = figure?.scale(name);
  const dom = scale && typeof scale === "object" && "domain" in scale ? scale.domain : undefined;
  if (
    Array.isArray(dom) &&
    dom.length === 2 &&
    typeof dom[0] === "number" &&
    typeof dom[1] === "number"
  ) {
    return [dom[0], dom[1]];
  }
  return null;
}

function postHeight(): void {
  const h = root ? Math.ceil(root.getBoundingClientRect().height) : MIN_HEIGHT;
  sendEvent("resize", { height: Math.max(h, MIN_HEIGHT) });
}

// ---------------------------------------------------------------------------
// Theming (protocol-v1.md §7): tokens arrive as resolved values; write a
// single :root block so the token-only CSS picks them up.

function applyTokens(raw: unknown): void {
  if (!raw || typeof raw !== "object") return;
  const entries = Object.entries(raw as Record<string, unknown>).filter(
    ([k, v]) => k.startsWith("--") && typeof v === "string" && v !== ""
  ) as [string, string][];
  if (entries.length === 0) return;
  let style = document.getElementById("chart-tokens");
  if (!style) {
    style = document.createElement("style");
    style.id = "chart-tokens";
    document.head.append(style);
  }
  style.textContent = `:root{${entries.map(([k, v]) => `${k}:${v};`).join("")}}`;
  tokens = tokenSet(raw);
}

function applyScheme(scheme: unknown): void {
  if (scheme) document.documentElement.setAttribute("data-color-scheme", String(scheme));
}

// ---------------------------------------------------------------------------
// host → plugin handlers

interface InitParams {
  doc?: unknown;
  tokens?: unknown;
  scheme?: unknown;
  capabilities?: unknown;
}

function handleInit(params: InitParams | undefined): void {
  initialized = true;
  applyScheme(params?.scheme);
  applyTokens(params?.tokens);
  if (!hasCap(params?.capabilities, "document:read")) {
    showError("document:read was not granted — no chart to show.");
    return;
  }
  try {
    spec = normalizeSpec(params?.doc);
  } catch (err) {
    showError(err instanceof SpecError ? `Chart spec invalid: ${err.message}` : "Chart spec invalid.");
    return;
  }
  renderChart();
  // Observability (§8a-style): report what the frame resolved after
  // applying the crossed tokens.
  const cs = getComputedStyle(document.documentElement);
  sendEvent("themeApplied", {
    scheme: params?.scheme ?? "light",
    sample: {
      "--color-text": cs.getPropertyValue("--color-text").trim(),
      "--color-primary": cs.getPropertyValue("--color-primary").trim(),
    },
  });
}

function handleThemeChanged(params: { scheme?: unknown; tokens?: unknown } | undefined): void {
  if (!initialized) return;
  applyScheme(params?.scheme);
  applyTokens(params?.tokens);
  if (spec) renderChart();
}

/** requestSave is a REQUEST → respond with the current doc. The frame is a
 *  renderer; it echoes the normalized spec it was given. */
function handleRequestSave(): { doc: Spec | null; schemaVersion: string } {
  return { doc: spec, schemaVersion: SCHEMA_VERSION };
}

/** teardown is a REQUEST → clean listeners, then ack. */
function handleTeardown(): Record<string, never> {
  if (messageListener) window.removeEventListener("message", messageListener);
  messageListener = null;
  return {};
}

// ---------------------------------------------------------------------------
// Lifecycle

// §8a self-isolation probes, computed INSIDE the opaque frame at boot: under
// sandbox="allow-scripts" (no allow-same-origin) each blocked access throws.
function isolationProbes(): { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } {
  const probes = { cookieEmpty: false, parentBlocked: false, storageBlocked: false };
  try {
    probes.cookieEmpty = document.cookie === "";
  } catch {
    probes.cookieEmpty = true;
  }
  try {
    void (window.parent as unknown as { document?: unknown }).document;
    probes.parentBlocked = false;
  } catch {
    probes.parentBlocked = true;
  }
  try {
    window.localStorage.getItem("x");
    probes.storageBlocked = false;
  } catch {
    probes.storageBlocked = true;
  }
  return probes;
}

function announceReady(): void {
  sendEvent("ready", {
    version: "0.1.0",
    schemaVersion: SCHEMA_VERSION,
    minHeight: MIN_HEIGHT,
    probes: isolationProbes(),
  });
}

const handlers: HandlerMap = {
  init: (params) => handleInit(params as InitParams | undefined),
  themeChanged: (params) => handleThemeChanged(params as { scheme?: unknown; tokens?: unknown } | undefined),
  requestSave: () => handleRequestSave(),
  teardown: () => handleTeardown(),
  // hostPointerdown is protocol-standard; the chart has no overlays to dismiss.
};

function boot(): void {
  root = document.querySelector<HTMLElement>(ROOT_SELECTOR);
  if (!root) {
    console.error("[chart] missing #chart-root");
    sendEvent("bootError", { message: "missing #chart-root" });
    return;
  }
  messageListener = createRouter(handlers);
  window.addEventListener("message", messageListener);
  // Report height after fonts settle so the first resize is close to final.
  if (document.fonts?.ready) document.fonts.ready.then(postHeight).catch(() => undefined);
  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
