// The genui component registry — the entire containment story.
//
// A model composes views out of THIS fixed map and nothing else: eight
// entries, each a React component statically imported into the bundle with a
// declared props schema. There is no second way in — no dynamic import, no
// React.lazy, no runtime loading, no markup/CSS/code emission the model could
// reach around. What is compiled in here is what can ever render.
//
// The schema and each component's TypeScript props are the same facts stated
// twice (one for the runtime validator, one for the compiler). They are
// compiled in together and reviewed together; validate.ts enforces the
// SCHEMA, never the type. No entry declares `style`, `className`, `children`
// as a prop, or anything else that reaches the DOM directly: React children
// arrive via the element tree (createElement's children argument), never as a
// composition prop, and styling is token-only CSS keyed off data-* attributes
// the components themselves mint from their declared props.
//
// RENDERER-INTERNAL props (never in any schema, so a composition carrying
// them is refused as an undeclared key before a component ever sees them):
//   nodeId — the validated tree path; every component stamps it as
//     data-genui-node on its root element, so "which DOM did the model
//     compose" is queryable and a test can tell composed DOM from the
//     frame's own chrome.
//   onActivate — Button only; see Button.

import type { ElementType, PropsWithChildren, ReactNode } from "react";

// --- the prop-type vocabulary a schema may declare ----------------------------
//
// Deliberately tiny: strings, finite numbers, closed enums (string OR number
// literals — Heading's level is 1|2|3), and the two array shapes Table needs.
// Nothing recursive, nothing open-ended, nothing a schema could use to smuggle
// an arbitrary value into the DOM.

/** One declared prop type. The validator is the only consumer of `kind`. */
export type PropType =
  | { kind: "string" }
  | { kind: "number" }
  | { kind: "enum"; values: readonly (string | number)[] }
  | { kind: "stringArray" }
  | { kind: "stringTable" };

/** A declared prop: its type, and whether a composition may omit it. */
export interface PropSpec {
  type: PropType;
  optional?: boolean;
}

export type PropSchema = Record<string, PropSpec>;

/**
 * One registry entry. `component` is ElementType (not a narrow ComponentType)
 * because the renderer hands it a Record<string, unknown> that the RUNTIME
 * validator — not the type system — has proven to match `props`: the
 * composition crossed an untyped postMessage boundary, and this is the one
 * documented seam where dynamic proof hands off to static types.
 */
export interface RegistryEntry {
  component: ElementType;
  /** The composition may nest children under this entry. */
  acceptsChildren: boolean;
  /** The composition may attach the `action` field (the host allow-list still
   *  gates it). True for Button only: a generated control is the one thing in
   *  a composition with behaviour. */
  carriesAction: boolean;
  props: PropSchema;
}

export type Registry = Readonly<Record<string, RegistryEntry>>;

// --- the eight components ------------------------------------------------------

/** Stack — the layout primitive. Both props required: a composition states
 *  its geometry, it does not inherit a guess. */
function Stack({
  gap,
  direction,
  nodeId,
  children,
}: PropsWithChildren<{ gap: "sm" | "md" | "lg"; direction: "row" | "column"; nodeId?: string }>): ReactNode {
  return (
    <div className="gu-stack" data-gap={gap} data-direction={direction} data-genui-node={nodeId}>
      {children}
    </div>
  );
}

/** Card — a titled surface. The only container besides Stack. */
function Card({ title, nodeId, children }: PropsWithChildren<{ title?: string; nodeId?: string }>): ReactNode {
  return (
    <section className="gu-card" data-genui-node={nodeId}>
      {title !== undefined && <div className="gu-card-title">{title}</div>}
      {children}
    </section>
  );
}

/** Heading — text with document weight. The tag follows the declared level,
 *  so a composition's outline is real HTML, not styled-up paragraphs. */
function Heading({ text, level, nodeId }: { text: string; level: 1 | 2 | 3; nodeId?: string }): ReactNode {
  const Tag = (["h1", "h2", "h3"] as const)[level - 1];
  return (
    <Tag className="gu-heading" data-level={level} data-genui-node={nodeId}>
      {text}
    </Tag>
  );
}

/** Text — body copy. `muted` is the only voice besides default. */
function Text({ text, tone, nodeId }: { text: string; tone?: "default" | "muted"; nodeId?: string }): ReactNode {
  return (
    <p className="gu-text" data-tone={tone ?? "default"} data-genui-node={nodeId}>
      {text}
    </p>
  );
}

/** Stat — one labelled number with an optional delta. The delta's sign picks
 *  its colour; the composition supplies meaning, not styling. */
function Stat({ label, value, delta, nodeId }: { label: string; value: string; delta?: number; nodeId?: string }): ReactNode {
  return (
    <div className="gu-stat" data-genui-node={nodeId}>
      <div className="gu-stat-label">{label}</div>
      <div className="gu-stat-value">{value}</div>
      {delta !== undefined && (
        <div className="gu-stat-delta" data-dir={delta > 0 ? "up" : delta < 0 ? "down" : "flat"}>
          {delta > 0 ? "▲ +" : delta < 0 ? "▼ " : ""}
          {delta}
        </div>
      )}
    </div>
  );
}

/** Badge — a status pill. Four tones, no other channel. */
function Badge({ label, tone, nodeId }: { label: string; tone?: "neutral" | "good" | "warn" | "bad"; nodeId?: string }): ReactNode {
  return (
    <span className="gu-badge" data-tone={tone ?? "neutral"} data-genui-node={nodeId}>
      {label}
    </span>
  );
}

/** Table — the one wide component. Columns and rows are plain strings; cells
 *  are text nodes, so a composition can never plant markup in a grid. Ragged
 *  rows render ragged (HTML tolerates it) — the schema promises string[][],
 *  nothing more, and the frame enforces exactly what the schema says. */
function Table({ columns, rows, nodeId }: { columns: string[]; rows: string[][]; nodeId?: string }): ReactNode {
  return (
    <table className="gu-table" data-genui-node={nodeId}>
      <thead>
        <tr>
          {columns.map((c, i) => (
            <th key={i} scope="col">
              {c}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((row, r) => (
          <tr key={r}>
            {row.map((cell, c) => (
              <td key={c}>{cell}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/**
 * Button — the only entry that carries an action. `onActivate` is
 * RENDERER-INTERNAL: the tree renderer mints it from the node's validated
 * `action` field, and it is not in the schema below — a composition that tries
 * to pass `onActivate` (or any handler) in props is refused as an undeclared
 * key before this component ever sees it. The button itself does nothing: the
 * click emits `uiAction {action, nodeId}` over the bridge and the HOST decides
 * what the id means. A generated control cannot point anywhere the host did
 * not name.
 */
function Button({
  label,
  variant,
  nodeId,
  onActivate,
}: {
  label: string;
  variant?: "primary" | "default";
  nodeId?: string;
  onActivate?: () => void;
}): ReactNode {
  return (
    <button
      type="button"
      className="gu-btn"
      data-variant={variant ?? "default"}
      data-genui-node={nodeId}
      onClick={onActivate}
    >
      {label}
    </button>
  );
}

// --- the registry ---------------------------------------------------------------
//
// The fixed table. A static Record literal — never mutated, only looked up by
// the validator and the renderer. String keys keep insertion order, so
// REGISTRY_IDS preserves the brief's declaration order and reads like the doc.

export const REGISTRY: Registry = {
  Stack: {
    component: Stack,
    acceptsChildren: true,
    carriesAction: false,
    props: {
      gap: { type: { kind: "enum", values: ["sm", "md", "lg"] } },
      direction: { type: { kind: "enum", values: ["row", "column"] } },
    },
  },
  Card: {
    component: Card,
    acceptsChildren: true,
    carriesAction: false,
    props: { title: { type: { kind: "string" }, optional: true } },
  },
  Heading: {
    component: Heading,
    acceptsChildren: false,
    carriesAction: false,
    props: {
      text: { type: { kind: "string" } },
      level: { type: { kind: "enum", values: [1, 2, 3] } },
    },
  },
  Text: {
    component: Text,
    acceptsChildren: false,
    carriesAction: false,
    props: {
      text: { type: { kind: "string" } },
      tone: { type: { kind: "enum", values: ["default", "muted"] }, optional: true },
    },
  },
  Stat: {
    component: Stat,
    acceptsChildren: false,
    carriesAction: false,
    props: {
      label: { type: { kind: "string" } },
      value: { type: { kind: "string" } },
      delta: { type: { kind: "number" }, optional: true },
    },
  },
  Badge: {
    component: Badge,
    acceptsChildren: false,
    carriesAction: false,
    props: {
      label: { type: { kind: "string" } },
      tone: { type: { kind: "enum", values: ["neutral", "good", "warn", "bad"] }, optional: true },
    },
  },
  Table: {
    component: Table,
    acceptsChildren: false,
    carriesAction: false,
    props: {
      columns: { type: { kind: "stringArray" } },
      rows: { type: { kind: "stringTable" } },
    },
  },
  Button: {
    component: Button,
    acceptsChildren: false,
    carriesAction: true,
    props: {
      label: { type: { kind: "string" } },
      variant: { type: { kind: "enum", values: ["primary", "default"] }, optional: true },
    },
  },
};

/** Registry ids in declaration order — announced on `ready` so the host can
 *  assert its registry and the frame's agree. */
export const REGISTRY_IDS: readonly string[] = Object.keys(REGISTRY);
