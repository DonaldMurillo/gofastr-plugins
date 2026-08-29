// The frame's own copy of the genui validation rules (schemaVersion genui-v1).
//
// Go is authoritative — it validates before persisting and before serving —
// and this runs anyway, on every composition, BEFORE rendering. "The host
// already checked it" is exactly the assumption that makes a second bug fatal:
// the registry, the allow-list and the tree all cross an untyped postMessage
// boundary, and the frame trusts none of them. Same rules, same registry,
// no trust in the bridge.
//
// The rules (the contract, restated as the code enforces it):
//   - `schemaVersion` must be "genui-v1"; the envelope carries exactly
//     {schemaVersion, root} and a node exactly {component, props, action?,
//     children?} — anything else is an unexpected field and refused.
//   - `component` must name a registry entry. Unknown → the whole composition
//     is rejected, never silently dropped.
//   - `props` must match the entry's declared schema exactly: unknown key →
//     refused; wrong type → refused; missing required key → refused.
//   - `action` is optional, only on entries that carry one, and must name an
//     entry in the host-supplied allow-list.
//   - `children` only where the entry accepts them.
//   - Depth ≤ MAX_DEPTH and node count ≤ MAX_NODES: a model that emits a
//     runaway tree fails validation here, not the renderer.
//
// The FIRST violation is reported (one clear reason, not a pile); a refused
// composition is surfaced loudly in the UI, not hidden.

import { REGISTRY, type PropType } from "./registry";

export const SCHEMA_VERSION = "genui-v1";
/** Deepest allowed node (root is depth 1). */
export const MAX_DEPTH = 16;
/** Most allowed nodes in one composition. */
export const MAX_NODES = 200;

/** A validated node. `nodeId` is the tree path minted during validation
 *  ("root", "root/0", "root/0/1") — the id a Button's uiAction carries, so a
 *  host can name exactly which generated control fired. */
export interface CompositionNode {
  component: string;
  nodeId: string;
  props: Record<string, unknown>;
  action?: string;
  children?: CompositionNode[];
}

export type ValidationResult =
  | { ok: true; root: CompositionNode; nodeCount: number }
  | { ok: false; error: string };

/**
 * Internal control flow, not an error protocol: thrown at the violation site
 * and caught by validateComposition, which is the only caller-facing surface.
 * Nothing here ever reaches the console — a refused composition is an expected
 * outcome the UI displays, and the frame must stay at zero console errors.
 */
class Refusal extends Error {}

/** The exact fields a composition envelope may carry. */
const ENVELOPE_FIELDS = new Set(["schemaVersion", "root"]);
/** The exact fields a node may carry. */
const NODE_FIELDS = new Set(["component", "props", "action", "children"]);

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Human-readable, length-capped description of an offending value. */
function describe(v: unknown): string {
  if (v === undefined) return "undefined";
  if (v === null) return "null";
  const json = JSON.stringify(v);
  if (json === undefined) return typeof v;
  return json.length <= 40 ? `${json} (${typeof v})` : typeof v;
}

function refuse(at: string, why: string): never {
  throw new Refusal(`${at}: ${why}`);
}

function checkType(value: unknown, type: PropType, at: string): void {
  switch (type.kind) {
    case "string":
      if (typeof value !== "string") refuse(at, `expected string, got ${describe(value)}`);
      return;
    case "number":
      if (typeof value !== "number" || !Number.isFinite(value)) {
        refuse(at, `expected a finite number, got ${describe(value)}`);
      }
      return;
    case "enum":
      if (!type.values.includes(value as string | number)) {
        refuse(at, `expected one of ${JSON.stringify(type.values)}, got ${describe(value)}`);
      }
      return;
    case "stringArray":
      if (!Array.isArray(value) || !value.every((c) => typeof c === "string")) {
        refuse(at, `expected string[], got ${describe(value)}`);
      }
      return;
    case "stringTable":
      if (
        !Array.isArray(value) ||
        !value.every((row) => Array.isArray(row) && row.every((c) => typeof c === "string"))
      ) {
        refuse(at, `expected string[][], got ${describe(value)}`);
      }
      return;
  }
}

/** Mutable walk state: the running node count against MAX_NODES. */
interface WalkCtx {
  actions: ReadonlySet<string>;
  count: number;
}

function validateNode(raw: unknown, path: string, depth: number, ctx: WalkCtx): CompositionNode {
  ctx.count += 1;
  if (ctx.count > MAX_NODES) refuse(path, `node count exceeds ${MAX_NODES}`);
  if (depth > MAX_DEPTH) refuse(path, `depth exceeds ${MAX_DEPTH}`);

  if (!isPlainObject(raw)) refuse(path, `expected an object, got ${describe(raw)}`);
  for (const key of Object.keys(raw)) {
    if (!NODE_FIELDS.has(key)) {
      refuse(path, `unexpected field "${key}" (allowed: component, props, action, children)`);
    }
  }

  const component = raw.component;
  if (typeof component !== "string") {
    refuse(`${path}.component`, `expected a string, got ${describe(component)}`);
  }
  const entry = REGISTRY[component];
  if (!entry) {
    refuse(
      `${path}.component`,
      `"${component}" is not in the registry (known: ${Object.keys(REGISTRY).join(", ")})`
    );
  }

  let props: Record<string, unknown> = {};
  if (raw.props !== undefined) {
    if (!isPlainObject(raw.props)) refuse(`${path}.props`, `expected an object, got ${describe(raw.props)}`);
    props = raw.props;
  }
  for (const key of Object.keys(props)) {
    if (!(key in entry.props)) {
      refuse(`${path}.props.${key}`, `not declared by ${component}`);
    }
  }
  for (const [key, spec] of Object.entries(entry.props)) {
    const value = props[key];
    if (value === undefined) {
      if (!spec.optional) refuse(`${path}.props.${key}`, `required by ${component}, missing`);
      continue;
    }
    checkType(value, spec.type, `${path}.props.${key}`);
  }

  let action: string | undefined;
  if (raw.action !== undefined) {
    if (!entry.carriesAction) refuse(`${path}.action`, `${component} does not accept an action`);
    if (typeof raw.action !== "string") {
      refuse(`${path}.action`, `expected a string, got ${describe(raw.action)}`);
    }
    if (!ctx.actions.has(raw.action)) {
      refuse(`${path}.action`, `"${raw.action}" is not in the host allow-list`);
    }
    action = raw.action;
  }

  let children: CompositionNode[] | undefined;
  if (raw.children !== undefined) {
    if (!entry.acceptsChildren) refuse(`${path}.children`, `${component} does not accept children`);
    if (!Array.isArray(raw.children)) {
      refuse(`${path}.children`, `expected an array, got ${describe(raw.children)}`);
    }
    children = raw.children.map((child, i) => validateNode(child, `${path}/${i}`, depth + 1, ctx));
  }

  return { component, nodeId: path, props, action, children };
}

/**
 * Validate one composition envelope (`{schemaVersion, root}` — the `tree` of a
 * host `composition` event) against the compiled-in registry and the
 * host-supplied action allow-list. Returns the first refusal reason, or the
 * validated tree with its node count. Pure: no DOM, no bridge, no console.
 */
export function validateComposition(tree: unknown, actions: ReadonlySet<string>): ValidationResult {
  try {
    if (!isPlainObject(tree)) {
      return { ok: false, error: `composition: expected an object, got ${describe(tree)}` };
    }
    for (const key of Object.keys(tree)) {
      if (!ENVELOPE_FIELDS.has(key)) {
        return { ok: false, error: `composition: unexpected field "${key}" (allowed: schemaVersion, root)` };
      }
    }
    if (tree.schemaVersion !== SCHEMA_VERSION) {
      return {
        ok: false,
        error: `composition.schemaVersion: expected "${SCHEMA_VERSION}", got ${describe(tree.schemaVersion)}`,
      };
    }
    if (tree.root === undefined) {
      return { ok: false, error: "composition.root: missing" };
    }
    const ctx: WalkCtx = { actions, count: 0 };
    const root = validateNode(tree.root, "root", 1, ctx);
    return { ok: true, root, nodeCount: ctx.count };
  } catch (err) {
    if (err instanceof Refusal) return { ok: false, error: err.message };
    // Pure code above only throws Refusal; this is the belt for the unexpected.
    return { ok: false, error: `composition: unexpected validation failure (${String(err)})` };
  }
}
