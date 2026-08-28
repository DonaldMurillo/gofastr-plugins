// GoFastr form builder — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY — never host cookies, storage, or
// DOM, and never fetch (the framed CSP sets connect-src 'none'). On load it
// announces `ready`; the host replies `init` carrying the canonical doc
// (the form schema), theme tokens, and granted capabilities.
//
// The builder edits the schema DATA: a field list (drag to reorder, buttons
// as the keyboard/touch path) plus a property panel (name, label, help,
// required, per-type rules). Every change goes out as a debounced docChanged
// and an autosave `save` event; the host POSTs it to the plugin's Go route,
// which VALIDATES and either persists or refuses with a specific error code
// that comes back as saveResult and is shown in the status line. The server
// is the authority — the frame's own name/duplicate hints are decoration.
//
// STRUCTURAL RULE: the builder never emits HTML. Every node below is built
// with createElement/textContent; the only strings that ever carry doc data
// are text nodes and input values. The saved schema is data only — the whole
// point of this plugin — and the frame cannot violate that by construction.

import { createRouter, rejectAllPending, sendEvent } from "./protocol";
import { applyScheme, applyTokens, sampleAppliedTokens } from "./theme";

const SCHEMA_VERSION = "formbuilder-v1";
const DOC_CHANGED_DEBOUNCE_MS = 400;
const AUTOSAVE_DEBOUNCE_MS = 1200;
const SAMPLE_TOKENS = ["--color-surface", "--color-text", "--color-border"];

const FIELD_TYPES = ["text", "email", "number", "textarea", "select", "checkbox", "date"] as const;
type FieldType = (typeof FIELD_TYPES)[number];

/** What each type can carry; drives both the palette labels and the rules
 * rows the property panel offers. Mirrors schema.go's rule applicability. */
const TYPE_META: Record<string, { label: string; length: boolean; range: boolean; pattern: boolean; options: boolean }> = {
  text:     { label: "Text",      length: true,  range: false, pattern: true,  options: false },
  email:    { label: "Email",     length: true,  range: false, pattern: true,  options: false },
  number:   { label: "Number",    length: false, range: true,  pattern: false, options: false },
  textarea: { label: "Long text", length: true,  range: false, pattern: true,  options: false },
  select:   { label: "Select",    length: false, range: false, pattern: false, options: true  },
  checkbox: { label: "Checkbox",  length: false, range: false, pattern: false, options: false },
  date:     { label: "Date",      length: false, range: false, pattern: false, options: false },
};

const NAME_RE = /^[a-z][a-z0-9_]{0,39}$/;

interface Rules {
  minLength?: number;
  maxLength?: number;
  min?: number;
  max?: number;
  pattern?: string;
}

interface Field {
  type: FieldType | string;
  name: string;
  label: string;
  required: boolean;
  help: string;
  options: string[];
  rules: Rules;
}

interface LiveDoc {
  version: string;
  fields: Field[];
}

// --- runtime state (module-scoped; single instance per frame) ---------------

let root: HTMLElement | null = null;
let listEl: HTMLElement | null = null;
let propsEl: HTMLElement | null = null;
let countEl: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let warnEl: HTMLElement | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

let doc: LiveDoc = { version: SCHEMA_VERSION, fields: [] };
let selected = -1;
let initialized = false;
let scheme = "light";
let lastTokens: unknown = null;
let docChangedTimer: number | undefined;
let autosaveTimer: number | undefined;
let resizeTimer: number | undefined;
// Drag state: the list item being dragged (index read back from data-index).
let dragIndex = -1;

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

function hasCap(list: string[], name: string): boolean {
  return list.includes(name);
}

function meta(type: string): { label: string; length: boolean; range: boolean; pattern: boolean; options: boolean } {
  return TYPE_META[type] ?? { label: type, length: false, range: false, pattern: false, options: false };
}

// --- doc narrowing (init.doc is untrusted postMessage data) -----------------

function parseRules(raw: unknown): Rules {
  const r = asRecord(raw);
  const out: Rules = {};
  if (typeof r.minLength === "number" && Number.isFinite(r.minLength)) out.minLength = r.minLength;
  if (typeof r.maxLength === "number" && Number.isFinite(r.maxLength)) out.maxLength = r.maxLength;
  if (typeof r.min === "number" && Number.isFinite(r.min)) out.min = r.min;
  if (typeof r.max === "number" && Number.isFinite(r.max)) out.max = r.max;
  if (typeof r.pattern === "string") out.pattern = r.pattern;
  return out;
}

function parseField(raw: unknown): Field | null {
  const f = asRecord(raw);
  if (typeof f.type !== "string" || f.type === "") return null;
  const field: Field = {
    type: f.type,
    name: typeof f.name === "string" ? f.name : "",
    label: typeof f.label === "string" ? f.label : "",
    required: f.required === true,
    help: typeof f.help === "string" ? f.help : "",
    options: [],
    rules: parseRules(f.rules),
  };
  if (Array.isArray(f.options)) {
    field.options = f.options.filter((o): o is string => typeof o === "string" && o !== "");
  }
  return field;
}

function parseDoc(raw: unknown): LiveDoc {
  const d = asRecord(raw);
  const out: LiveDoc = {
    version: typeof d.version === "string" && d.version !== "" ? d.version : SCHEMA_VERSION,
    fields: [],
  };
  if (Array.isArray(d.fields)) {
    for (const f of d.fields) {
      const field = parseField(f);
      if (field) out.fields.push(field);
    }
  }
  return out;
}

// --- doc emission -----------------------------------------------------------

function currentDoc(): Record<string, unknown> {
  const fields = doc.fields.map((f) => {
    const out: Record<string, unknown> = {
      type: f.type,
      name: f.name,
      label: f.label,
      required: f.required,
    };
    if (f.help !== "") out.help = f.help;
    if (f.type === "select") out.options = f.options.slice();
    const rules: Record<string, unknown> = {};
    if (f.rules.minLength !== undefined) rules.minLength = f.rules.minLength;
    if (f.rules.maxLength !== undefined) rules.maxLength = f.rules.maxLength;
    if (f.rules.min !== undefined) rules.min = f.rules.min;
    if (f.rules.max !== undefined) rules.max = f.rules.max;
    if (f.rules.pattern !== undefined && f.rules.pattern !== "") rules.pattern = f.rules.pattern;
    if (Object.keys(rules).length > 0) out.rules = rules;
    return out;
  });
  return { version: doc.version, fields };
}

function ruleCount(): number {
  let n = 0;
  for (const f of doc.fields) {
    if (f.required) n += 1;
    for (const key of Object.keys(f.rules) as (keyof Rules)[]) {
      if (f.rules[key] !== undefined && f.rules[key] !== "") n += 1;
    }
  }
  return n;
}

function setStatus(text: string, tone: "idle" | "ok" | "bad"): void {
  if (!statusEl) return;
  statusEl.textContent = text;
  statusEl.className = "fb-status" + (tone === "ok" ? " fb-status-ok" : tone === "bad" ? " fb-status-bad" : "");
}

function markDirty(): void {
  setStatus("Unsaved changes…", "idle");
  window.clearTimeout(docChangedTimer);
  docChangedTimer = window.setTimeout(() => {
    sendEvent("docChanged", { doc: currentDoc(), dirty: true });
  }, DOC_CHANGED_DEBOUNCE_MS);
  window.clearTimeout(autosaveTimer);
  autosaveTimer = window.setTimeout(doSave, AUTOSAVE_DEBOUNCE_MS);
}

function doSave(): void {
  window.clearTimeout(autosaveTimer);
  setStatus("Saving…", "idle");
  sendEvent("save", { doc: currentDoc(), schemaVersion: SCHEMA_VERSION });
}

// --- frame-side hints (the server's verdict is the authority) ----------------

function frameWarnings(): string[] {
  const warns: string[] = [];
  const seen = new Set<string>();
  for (const f of doc.fields) {
    if (f.name === "") warns.push(`Field “${f.label || f.type}” has no name`);
    else if (!NAME_RE.test(f.name)) warns.push(`“${f.name}” is not a valid field name (lowercase, digits, _)`);
    if (seen.has(f.name) && f.name !== "") warns.push(`Duplicate name: ${f.name}`);
    seen.add(f.name);
    if (f.label.includes("<") || f.help.includes("<")) warns.push(`“${f.label || f.name}”: markup is not data — the server refuses it`);
  }
  return warns;
}

function renderWarnings(): void {
  if (!warnEl) return;
  while (warnEl.firstChild) warnEl.removeChild(warnEl.firstChild);
  for (const w of frameWarnings()) {
    const li = document.createElement("li");
    li.textContent = w;
    warnEl.appendChild(li);
  }
  warnEl.hidden = warnEl.firstChild === null;
}

// --- rendering ----------------------------------------------------------------

function el(tag: string, cls?: string): HTMLElement {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  return node;
}

function textInput(value: string, placeholder: string, onInput: (v: string) => void, prop: string): HTMLInputElement {
  const input = document.createElement("input");
  input.type = "text";
  input.className = "fb-input";
  input.value = value;
  input.placeholder = placeholder;
  input.setAttribute("data-fui-fb-prop", prop);
  input.addEventListener("input", () => onInput(input.value));
  return input;
}

function numInput(value: number | undefined, placeholder: string, onInput: (v: number | undefined) => void): HTMLInputElement {
  const input = document.createElement("input");
  input.type = "number";
  input.className = "fb-input";
  if (value !== undefined) input.value = String(value);
  input.placeholder = placeholder;
  input.addEventListener("input", () => {
    if (input.value === "") onInput(undefined);
    else if (Number.isFinite(Number(input.value))) onInput(Number(input.value));
  });
  return input;
}

function propRow(labelText: string, control: HTMLElement): HTMLElement {
  const row = el("div", "fb-prop-row");
  const label = el("label", "fb-prop-label");
  label.textContent = labelText;
  row.append(label, control);
  return row;
}

function renderList(): void {
  if (!listEl) return;
  const list = listEl;
  while (list.firstChild) list.removeChild(list.firstChild);

  if (doc.fields.length === 0) {
    const empty = el("li", "fb-empty");
    empty.textContent = "No fields yet — add one from the palette above.";
    list.appendChild(empty);
    return;
  }

  doc.fields.forEach((f, i) => {
    const item = el("li", "fb-item" + (i === selected ? " is-selected" : ""));
    item.dataset.index = String(i);
    item.draggable = true;

    const grip = el("span", "fb-grip");
    grip.textContent = "⠿";
    grip.title = "Drag to reorder";

    const type = el("span", "fb-type");
    type.textContent = meta(f.type).label.toLowerCase();

    const label = el("span", "fb-item-label");
    label.textContent = f.label || f.name || "(unnamed)";

    const name = el("span", "fb-item-name");
    name.textContent = f.name;

    const btns = el("span", "fb-item-btns");
    const mk = (glyph: string, title: string, fn: () => void) => {
      const b = document.createElement("button");
      b.className = "fb-iconbtn";
      b.type = "button";
      b.textContent = glyph;
      b.title = title;
      b.setAttribute("aria-label", title + " “" + (f.label || f.name || f.type) + "”");
      b.addEventListener("click", (ev) => {
        ev.stopPropagation();
        fn();
      });
      return b;
    };
    btns.append(
      mk("↑", "Move up", () => moveField(i, -1)),
      mk("↓", "Move down", () => moveField(i, 1)),
      mk("✕", "Remove", () => removeField(i))
    );

    item.append(grip, type, label, name, btns);
    item.addEventListener("click", () => {
      selected = i;
      renderList();
      renderProps();
    });

    item.addEventListener("dragstart", () => {
      dragIndex = i;
      item.classList.add("is-dragging");
    });
    item.addEventListener("dragend", () => {
      dragIndex = -1;
      item.classList.remove("is-dragging");
    });
    item.addEventListener("dragover", (ev) => {
      if (dragIndex === -1 || dragIndex === i) return;
      ev.preventDefault();
      item.classList.add("is-drop-target");
    });
    item.addEventListener("dragleave", () => item.classList.remove("is-drop-target"));
    item.addEventListener("drop", (ev) => {
      ev.preventDefault();
      item.classList.remove("is-drop-target");
      if (dragIndex === -1 || dragIndex === i) return;
      moveField(dragIndex, i - dragIndex);
      dragIndex = -1;
    });

    list.appendChild(item);
  });
}

function renderProps(): void {
  if (!propsEl) return;
  while (propsEl.firstChild) propsEl.removeChild(propsEl.firstChild);

  if (selected < 0 || selected >= doc.fields.length) {
    const p = el("p", "fb-props-empty");
    p.textContent = "Select a field to edit its properties.";
    propsEl.appendChild(p);
    return;
  }
  const f = doc.fields[selected];
  const m = meta(f.type);

  propsEl.appendChild(propRow("Label", textInput(f.label, "Shown above the field", (v) => {
    f.label = v;
    refreshAfterPropertyEdit();
  }, "label")));
  propsEl.appendChild(propRow("Name", textInput(f.name, "form field identifier", (v) => {
    f.name = v.trim();
    refreshAfterPropertyEdit();
  }, "name")));
  propsEl.appendChild(propRow("Help text", textInput(f.help, "Shown under the field", (v) => {
    f.help = v;
    refreshAfterPropertyEdit();
  }, "help")));

  const reqRow = el("div", "fb-prop-row fb-prop-check");
  const reqLabel = el("label", "fb-prop-label");
  const reqBox = document.createElement("input");
  reqBox.type = "checkbox";
  reqBox.className = "fb-check";
  reqBox.checked = f.required;
  reqBox.id = "fb-prop-required";
  reqBox.addEventListener("change", () => {
    f.required = reqBox.checked;
    refreshAfterPropertyEdit();
  });
  reqLabel.setAttribute("for", "fb-prop-required");
  reqLabel.textContent = "Required";
  reqRow.append(reqBox, reqLabel);
  propsEl.appendChild(reqRow);

  if (m.options) {
    const box = document.createElement("textarea");
    box.className = "fb-input fb-options";
    box.rows = 4;
    box.value = f.options.join("\n");
    box.placeholder = "one option per line";
    box.addEventListener("input", () => {
      f.options = box.value.split("\n").map((s) => s.trim()).filter((s) => s !== "");
      refreshAfterPropertyEdit();
    });
    const row = propRow("Options", box);
    row.classList.add("fb-prop-row-wide");
    propsEl.appendChild(row);
  }

  if (m.length) {
    const pair = el("div", "fb-prop-pair");
    pair.append(
      numInput(f.rules.minLength, "min length", (v) => { setRule(f, "minLength", v); }),
      numInput(f.rules.maxLength, "max length", (v) => { setRule(f, "maxLength", v); })
    );
    const row = propRow("Length", pair);
    row.classList.add("fb-prop-row-wide");
    propsEl.appendChild(row);
  }

  if (m.range) {
    const pair = el("div", "fb-prop-pair");
    pair.append(
      numInput(f.rules.min, "min", (v) => { setRule(f, "min", v); }),
      numInput(f.rules.max, "max", (v) => { setRule(f, "max", v); })
    );
    const row = propRow("Range", pair);
    row.classList.add("fb-prop-row-wide");
    propsEl.appendChild(row);
  }

  if (m.pattern) {
    const row = propRow("Pattern (regexp)", textInput(f.rules.pattern ?? "", "e.g. ^[a-z ]+$", (v) => {
      if (v === "") delete f.rules.pattern;
      else f.rules.pattern = v;
      refreshAfterPropertyEdit();
    }, "pattern"));
    row.classList.add("fb-prop-row-wide");
    propsEl.appendChild(row);
  }
}

function setRule(f: Field, key: "minLength" | "maxLength" | "min" | "max", v: number | undefined): void {
  if (v === undefined) delete f.rules[key];
  else f.rules[key] = v;
  refreshAfterPropertyEdit();
}

/** Property edits mutate state directly; only the list labels, warnings and
 * the debounced save pipeline need refreshing — rebuilding the panel would
 * steal focus from the input being typed in. */
function refreshAfterPropertyEdit(): void {
  renderList();
  renderWarnings();
  updateCount();
  markDirty();
}

function updateCount(): void {
  if (!countEl) return;
  const n = doc.fields.length;
  const r = ruleCount();
  countEl.textContent = `${n} field${n === 1 ? "" : "s"} · ${r} rule${r === 1 ? "" : "s"}`;
}

function uniqueName(): string {
  const taken = new Set(doc.fields.map((f) => f.name));
  for (let n = 1; n <= 200; n++) {
    const candidate = `field_${n}`;
    if (!taken.has(candidate)) return candidate;
  }
  return "field";
}

function addField(type: FieldType): void {
  const f: Field = {
    type,
    name: uniqueName(),
    label: meta(type).label,
    required: false,
    help: "",
    options: type === "select" ? ["Option 1", "Option 2"] : [],
    rules: {},
  };
  doc.fields.push(f);
  selected = doc.fields.length - 1;
  renderList();
  renderProps();
  renderWarnings();
  updateCount();
  markDirty();
  scheduleResize();
}

function moveField(index: number, delta: number): void {
  const to = index + delta;
  if (index < 0 || index >= doc.fields.length || to < 0 || to >= doc.fields.length) return;
  const [f] = doc.fields.splice(index, 1);
  doc.fields.splice(to, 0, f);
  if (selected === index) selected = to;
  else if (selected === to) selected = index;
  renderList();
  renderProps();
  markDirty();
  scheduleResize();
}

function removeField(index: number): void {
  if (index < 0 || index >= doc.fields.length) return;
  doc.fields.splice(index, 1);
  if (selected >= doc.fields.length) selected = doc.fields.length - 1;
  renderList();
  renderProps();
  renderWarnings();
  updateCount();
  markDirty();
  scheduleResize();
}

// --- resize bridge -----------------------------------------------------------

function scheduleResize(): void {
  window.clearTimeout(resizeTimer);
  resizeTimer = window.setTimeout(() => {
    if (!root) return;
    const h = Math.round(root.getBoundingClientRect().height);
    if (h > 0) sendEvent("resize", { height: Math.min(2000, Math.max(320, h)) });
  }, 60);
}

// ---------------------------------------------------------------------------
// host → plugin handlers

function handleInit(params: unknown): void {
  if (initialized) return;
  initialized = true;
  const p = asRecord(params);
  const caps = Array.isArray(p.capabilities) ? p.capabilities.filter((c): c is string => typeof c === "string") : [];
  if (!hasCap(caps, "document:write")) {
    setStatus("Read-only mount — saving is disabled", "bad");
  }
  scheme = typeof p.scheme === "string" ? p.scheme : "light";
  lastTokens = p.tokens;
  applyTokens(p.tokens);
  applyScheme(scheme);
  doc = parseDoc(p.doc);
  selected = doc.fields.length - 1;
  renderList();
  renderProps();
  renderWarnings();
  updateCount();
  if (doc.fields.length > 0) setStatus("Loaded — edits autosave over the bridge", "idle");
  sendEvent("themeApplied", { scheme, sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS) });
  scheduleResize();
}

function handleThemeChanged(params: unknown): void {
  const p = asRecord(params);
  scheme = typeof p.scheme === "string" ? p.scheme : scheme;
  lastTokens = p.tokens;
  applyTokens(p.tokens);
  applyScheme(scheme);
  sendEvent("themeApplied", { scheme, sample: sampleAppliedTokens(p.tokens, SAMPLE_TOKENS) });
}

function handleRequestSave(): Record<string, unknown> {
  return { doc: currentDoc(), schemaVersion: SCHEMA_VERSION };
}

function handleSaveResult(params: unknown): void {
  const p = asRecord(params);
  if (p.ok === true) {
    const fields = typeof p.fields === "number" ? p.fields : doc.fields.length;
    const rules = typeof p.rules === "number" ? p.rules : ruleCount();
    setStatus(`Saved ✓ — Go validated ${fields} fields, ${rules} rules`, "ok");
  } else {
    const code = typeof p.code === "string" ? p.code : "E_SAVE";
    setStatus(`Refused by the server: ${code}`, "bad");
  }
}

function handleTeardown(): Record<string, never> {
  window.clearTimeout(docChangedTimer);
  window.clearTimeout(autosaveTimer);
  window.clearTimeout(resizeTimer);
  rejectAllPending({ code: "E_TEARDOWN", message: "frame torn down" });
  if (messageListener) {
    window.removeEventListener("message", messageListener);
    messageListener = null;
  }
  return {};
}

// ---------------------------------------------------------------------------
// Lifecycle

// §8a: self-isolation probes, computed INSIDE the opaque frame at boot. Under
// sandbox="allow-scripts" (no allow-same-origin) each of these is blocked by
// the browser, so accessing them throws — which is exactly the third-party
// guarantee.
function isolationProbes(): { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } {
  let cookieEmpty = false;
  let parentBlocked = false;
  let storageBlocked = false;
  try {
    cookieEmpty = document.cookie === "";
  } catch {
    cookieEmpty = true;
  }
  try {
    void (window.parent as unknown as { document?: unknown }).document;
  } catch {
    parentBlocked = true;
  }
  try {
    void window.localStorage.length;
  } catch {
    storageBlocked = true;
  }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

function announceReady(): void {
  sendEvent("ready", {
    version: SCHEMA_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: 480,
    probes: isolationProbes(),
  });
}

function boot(): void {
  root = document.getElementById("fb-root");
  listEl = document.getElementById("fb-list");
  propsEl = document.getElementById("fb-props");
  countEl = document.getElementById("fb-count");
  statusEl = document.getElementById("fb-status");
  warnEl = document.getElementById("fb-warn");
  if (!root || !listEl || !propsEl) return;

  const palette = document.getElementById("fb-palette");
  if (palette) {
    for (const type of FIELD_TYPES) {
      const b = document.createElement("button");
      b.className = "fb-add-btn";
      b.type = "button";
      b.textContent = "+ " + meta(type).label;
      b.setAttribute("data-fui-fb-add", type);
      b.addEventListener("click", () => addField(type));
      palette.appendChild(b);
    }
  }

  const saveBtn = document.getElementById("fb-save");
  if (saveBtn) saveBtn.addEventListener("click", doSave);

  // The frame's width follows the host iframe; a reflow can change the
  // natural height, so observe and re-announce.
  if (typeof ResizeObserver === "function") {
    new ResizeObserver(() => scheduleResize()).observe(root);
  }

  messageListener = createRouter({
    init: handleInit,
    themeChanged: handleThemeChanged,
    requestSave: handleRequestSave,
    saveResult: handleSaveResult,
    teardown: handleTeardown,
  });
  window.addEventListener("message", messageListener);
  announceReady();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
