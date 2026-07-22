// GoFastr Leaflet map — in-frame entry point (protocol v1).
//
// Boots inside the opaque-origin sandboxed iframe. Speaks protocol v1 to the
// host over window.parent.postMessage ONLY. Never touches host cookies,
// localStorage, or the host DOM. On load it announces `ready`; the host replies
// `init`; we mount Leaflet from init.{doc, config}, apply bridged tokens, respect
// granted capabilities, and emit the full plugin->host event set.
//
// Canonical doc (schema map-v1): {lat, lng, zoom, markers:[{id,lat,lng,label}]}.
//
// THE CRITICAL SANDBOX CONSTRAINT: the framed CSP is
//   img-src <origin> data:; connect-src 'none'
// so (a) external tile hosts are blocked → we point Leaflet at the SAME-ORIGIN
// proxy /__gofastr/plugin/map/tiles/{provider}/{z}/{x}/{y}, and (b) raster
// tiles are <img>, NOT fetch, so connect-src 'none' does not affect them.
// Leaflet's default marker icon PNGs 404 under a bundler AND are blocked at
// runtime under img-src <origin> (the icon path resolves to a file://-style
// bundled asset URL the frame cannot fetch). We therefore use an inline-SVG
// L.divIcon — no image asset to resolve, no fetch, no path to fix up.

import "leaflet/dist/leaflet.css";
import * as L from "leaflet";

import { PROTOCOL_VERSION, sendEvent, createRouter, type HandlerMap } from "./protocol";
import { applyTokens, applyScheme, sampleAppliedTokens } from "./theme";

const CONTAINER_SELECTOR = "#map-container";
const STATUS_SELECTOR = "#map-status";

const SCHEMA_VERSION = "map-v1";
const DOC_CHANGED_DEBOUNCE_MS = 400;
const AUTOSAVE_DEBOUNCE_MS = 2000;
const READY_MIN_HEIGHT = 360;
const MAX_MAP_HEIGHT = 2000;

// Tile proxy: served same-origin at the plugin route prefix. The provider key
// selects an entry in the Go allowlist (osm / carto-light / carto-dark); the
// proxy validates z/x/y and fetches upstream server-side. We use the ABSOLUTE
// path (not relative) so it is identical to the Go route constant and survives
// any future cache-busting on the iframe src.
const TILE_PROXY_PREFIX = "/__gofastr/plugin/map/tiles";

// Frame-known providers. Each maps to a tile layer pointing at the same-origin
// proxy. The set mirrors the Go defaultTileProviders (osm, carto-light,
// carto-dark); the demo's "auto" theme swaps light<->dark by switching the
// active base layer. A host can register more providers server-side; only the
// three here are surfaced in the in-frame layers control (the control's job is
// a convenient switch, not an exhaustive directory).
type LayerKind = "osm" | "carto-light" | "carto-dark";
const KNOWN_LAYERS: Record<LayerKind, { label: string }> = {
  osm: { label: "OpenStreetMap" },
  "carto-light": { label: "Carto · Light" },
  "carto-dark": { label: "Carto · Dark" },
};

const DEFAULT_DOC: MapDoc = { lat: 20, lng: 0, zoom: 2, markers: [] };

interface MapMarker {
  id: string;
  lat: number;
  lng: number;
  label?: string;
}

interface MapDoc {
  lat: number;
  lng: number;
  zoom: number;
  markers: MapMarker[];
}

interface MapConfig {
  center: { lat: number; lng: number };
  zoom: number;
  minZoom: number;
  maxZoom: number;
  provider: string;
  readOnly: boolean;
  markers: MapMarker[];
  theme: string; // "light" | "dark" | "auto"
}

const DEFAULT_CONFIG: MapConfig = {
  center: { lat: 20, lng: 0 },
  zoom: 2,
  minZoom: 0,
  maxZoom: 19,
  provider: "osm",
  readOnly: false,
  markers: [],
  theme: "auto",
};

// --- runtime state (module-scoped; single instance per frame) ---
let container: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let map: L.Map | null = null;
let baseLayers: Record<string, L.TileLayer> = {};
let layersControl: L.Control.Layers | null = null;
let resizeObserver: ResizeObserver | null = null;
let messageListener: ((event: MessageEvent) => void) | null = null;

// markersById holds the live Leaflet marker per persisted id. keyed by id so
// highlightMarker / setProvider / save round-trips can address a marker without
// a linear scan. Built fresh on every renderMarkersFromDoc.
let markersById: Map<string, L.Marker> = new Map();
let highlightedId: string | null = null;

let doc: MapDoc = { ...DEFAULT_DOC };
let initialized = false;
let capabilities = new Set<string>();
let scheme = "light";
let config: MapConfig = DEFAULT_CONFIG;
let dirty = false;
let idSeq = 0;

let docChangedTimer: number | undefined;
let autosaveTimer: number | undefined;

function hasCap(name: string): boolean {
  return capabilities.has(name);
}

function nextMarkerId(): string {
  idSeq += 1;
  // Stable, human-readable, collision-free within a single frame session. The
  // host persists by id; a fresh session that rehydrates from a saved doc keeps
  // the persisted ids verbatim (this generator only mints NEW pin ids).
  return `m-${Date.now().toString(36)}-${idSeq}`;
}

// --- boundary narrowing (no `any`: validate untrusted init params) ----------

function readStringRecord(raw: unknown): Record<string, unknown> {
  return raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
}

function readString(raw: unknown, fallback: string): string {
  return typeof raw === "string" ? raw : fallback;
}

function readBool(raw: unknown, fallback: boolean): boolean {
  return typeof raw === "boolean" ? raw : fallback;
}

function readNumber(raw: unknown, fallback: number): number {
  return typeof raw === "number" && Number.isFinite(raw) ? raw : fallback;
}

function readPoint(raw: unknown, fallback: { lat: number; lng: number }): { lat: number; lng: number } {
  if (raw && typeof raw === "object") {
    const o = raw as { lat?: unknown; lng?: unknown };
    const lat = readNumber(o.lat, fallback.lat);
    const lng = readNumber(o.lng, fallback.lng);
    return { lat, lng };
  }
  return fallback;
}

function readMarkers(raw: unknown): MapMarker[] {
  if (!Array.isArray(raw)) return [];
  const out: MapMarker[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const m = item as Record<string, unknown>;
    const id = typeof m.id === "string" ? m.id : "";
    const lat = readNumber(m.lat, NaN);
    const lng = readNumber(m.lng, NaN);
    if (!id || !Number.isFinite(lat) || !Number.isFinite(lng)) continue;
    out.push({ id, lat, lng, label: typeof m.label === "string" ? m.label : undefined });
  }
  return out;
}

function readConfig(raw: unknown): MapConfig {
  const o = readStringRecord(raw);
  return {
    center: readPoint(o.center, DEFAULT_CONFIG.center),
    zoom: readNumber(o.zoom, DEFAULT_CONFIG.zoom),
    minZoom: readNumber(o.minZoom, DEFAULT_CONFIG.minZoom),
    maxZoom: readNumber(o.maxZoom, DEFAULT_CONFIG.maxZoom),
    provider: readString(o.provider, DEFAULT_CONFIG.provider),
    readOnly: readBool(o.readOnly, DEFAULT_CONFIG.readOnly),
    markers: readMarkers(o.markers),
    theme: readString(o.theme, DEFAULT_CONFIG.theme),
  };
}

function readDoc(raw: unknown): MapDoc {
  const o = readStringRecord(raw);
  const docMarkers = readMarkers(o.markers);
  return {
    lat: readNumber(o.lat, DEFAULT_DOC.lat),
    lng: readNumber(o.lng, DEFAULT_DOC.lng),
    zoom: readNumber(o.zoom, DEFAULT_DOC.zoom),
    markers: docMarkers,
  };
}

function hasDoc(raw: unknown): boolean {
  if (!raw || typeof raw !== "object") return false;
  const o = raw as Record<string, unknown>;
  return "lat" in o || "lng" in o || "zoom" in o || "markers" in o;
}

// --- pin icon (inline SVG; no external image asset, no fetch) --------------

function pinIcon(isHighlighted = false): L.DivIcon {
  // An SVG pin with a white stroke and a token-driven fill. data: URIs are
  // allowed by img-src, but we don't even need one — divIcon renders the SVG
  // as DOM, so it is governed by style-src 'unsafe-inline' (allowed). The
  // highlighted class swaps the fill via CSS (.gofastr-pin.is-highlighted).
  return L.divIcon({
    className: "gofastr-pin" + (isHighlighted ? " is-highlighted" : ""),
    html: `<svg viewBox="0 0 24 24" aria-hidden="true">
      <path class="pin-stroke" fill="none" d="M12 2C7.6 2 4 5.6 4 10c0 5.4 7 11.5 7.3 11.8.4.3.9.3 1.3 0C16.9 21.5 20 15.4 20 10c0-4.4-3.6-8-8-8z"/>
      <path class="pin-fill" d="M12 2C7.6 2 4 5.6 4 10c0 5.4 7 11.5 7.3 11.8.4.3.9.3 1.3 0C16.9 21.5 20 15.4 20 10c0-4.4-3.6-8-8-8z"/>
      <circle class="pin-dot" cx="12" cy="10" r="3"/>
    </svg>`,
    iconSize: [26, 26],
    iconAnchor: [13, 24],
    popupAnchor: [0, -22],
  });
}

// --- tile layers (same-origin proxy) ---------------------------------------

function tileUrlFor(provider: string): string {
  // Provider is interpolated into the PATH, never into a query string. The
  // Go proxy looks it up in its allowlist and 404s on unknown — so even if a
  // host-side bug sent a bad provider, the proxy refuses it. {z}/{x}/{y} are
  // Leaflet placeholders, substituted client-side by Leaflet (not by us).
  return `${TILE_PROXY_PREFIX}/${provider}/{z}/{x}/{y}`;
}

function buildBaseLayers(): Record<string, L.TileLayer> {
  const layers: Record<string, L.TileLayer> = {};
  for (const key of Object.keys(KNOWN_LAYERS) as LayerKind[]) {
    layers[key] = L.tileLayer(tileUrlFor(key), {
      attribution: key === "osm"
        ? "&copy; OpenStreetMap contributors"
        : "&copy; OpenStreetMap contributors &copy; CARTO",
      maxZoom: config.maxZoom || 19,
      minZoom: config.minZoom || 0,
      // Leaflet's default crossOrigin would force a CORS request the proxy
      // does not need (we are same-origin); leaving it unset lets the <img>
      // load as a plain same-origin resource.
    });
  }
  return layers;
}

function resolvedProvider(): string {
  // theme "auto" follows the bridged scheme: dark scheme → carto-dark,
  // otherwise the configured provider.
  if (config.theme === "auto" && (config.provider === "osm" || config.provider === "carto-light" || config.provider === "carto-dark")) {
    // If the host-bridged scheme is dark AND the configured provider is a
    // light-ish one, switch to carto-dark for legibility. We only auto-swap
    // when theme is "auto" — an explicit "light"/"dark" config.theme pins the
    // scheme and we leave the provider alone.
    if (scheme === "dark" && (config.provider === "osm" || config.provider === "carto-light")) {
      return "carto-dark";
    }
    if (scheme === "light" && config.provider === "carto-dark") {
      return "carto-light";
    }
  }
  return config.provider;
}

function setActiveProvider(provider: string, options: { viaControl?: boolean } = {}): void {
  if (!map) return;
  const target = baseLayers[provider] ?? baseLayers[config.provider] ?? baseLayers["osm"];
  if (!target) return;
  // Remove every base layer, then add the chosen one (Leaflet allows multiple
  // base layers to coexist; we want exactly one active).
  for (const key of Object.keys(baseLayers)) {
    const layer = baseLayers[key];
    if (map.hasLayer(layer)) map.removeLayer(layer);
  }
  map.addLayer(target);
  // If the layers control is mounted, reflect the selection so the radio
  // matches reality (the user may have switched via setProvider RPC rather
  // than the control).
  if (layersControl && !options.viaControl) {
    // No public API to set the control's active radio programmatically
    // without re-adding; the input's checked state is cosmetic and re-syncs
    // on the next user interaction. Acceptable: the map state is the source
    // of truth, the control is a convenience.
  }
  config.provider = provider;
}

// --- marker CRUD -----------------------------------------------------------

function renderMarkersFromDoc(): void {
  if (!map) return;
  // Tear down the previous marker set cleanly so a save round-trip (which
  // re-hydrates the whole doc) does not stack duplicates.
  for (const m of markersById.values()) {
    map.removeLayer(m);
  }
  markersById = new Map();
  for (const marker of doc.markers) {
    addMarkerToMap(marker, { silent: true });
  }
  if (highlightedId) {
    applyHighlightVisual();
  }
}

function addMarkerToMap(marker: MapMarker, opts: { silent?: boolean } = {}): L.Marker | null {
  if (!map) return null;
  if (markersById.has(marker.id)) return markersById.get(marker.id) ?? null;
  const m = L.marker([marker.lat, marker.lng], {
    icon: pinIcon(highlightedId === marker.id),
    draggable: !config.readOnly && hasCap("document:write"),
    title: marker.label || `Pin ${marker.id}`,
    alt: marker.label || "Map pin",
    keyboard: true,
  });
  m.bindPopup(() => popupContent(marker.id), { closeButton: true, minWidth: 220 });
  m.on("click", () => {
    highlightedId = marker.id;
    applyHighlightVisual();
    sendEvent("markerSelected", { id: marker.id });
  });
  m.on("dragend", () => {
    const ll = m.getLatLng();
    const existing = doc.markers.find((x) => x.id === marker.id);
    if (existing) {
      existing.lat = ll.lat;
      existing.lng = ll.lng;
      dirty = true;
      scheduleDocChanged();
      scheduleAutosave();
    }
  });
  // Double-click = edit shortcut: opens the popup with the input focused.
  m.on("dblclick", () => {
    m.openPopup();
    window.setTimeout(() => {
      const input = popupInputEl(marker.id);
      if (input) input.focus();
    }, 50);
  });
  m.addTo(map);
  markersById.set(marker.id, m);
  if (!opts.silent) {
    dirty = true;
    scheduleDocChanged();
    scheduleAutosave();
  }
  return m;
}

function popupInputEl(id: string): HTMLInputElement | null {
  if (!container) return null;
  return container.querySelector<HTMLInputElement>(`#pin-input-${cssIdent(id)}`);
}

function cssIdent(s: string): string {
  // marker ids are m-<b36>-<n> already; sanitize defensively for the rare case
  // a saved doc carries a hand-edited id.
  return s.replace(/[^A-Za-z0-9_-]/g, "_");
}

function popupContent(id: string): HTMLElement {
  const marker = doc.markers.find((x) => x.id === id);
  const wrap = document.createElement("div");
  wrap.className = "pin-popup";
  const labelEl = document.createElement("p");
  labelEl.className = "pin-label";
  labelEl.textContent = marker?.label || "(unlabelled pin)";
  const input = document.createElement("input");
  input.type = "text";
  input.value = marker?.label || "";
  input.id = `pin-input-${cssIdent(id)}`;
  input.placeholder = "Pin label";
  input.style.width = "100%";
  input.style.margin = "4px 0 8px";
  input.style.padding = "4px 6px";
  input.style.border = `1px solid var(--color-border, #e4e4e7)`;
  input.style.borderRadius = "var(--radii-sm, 6px)";
  input.style.background = "var(--color-surface, #fff)";
  input.style.color = "var(--color-text, #18181b)";
  input.style.font = "inherit";
  input.style.fontSize = "var(--font-size-sm, 0.85rem)";
  input.setAttribute("aria-label", "Pin label");

  const row = document.createElement("div");
  row.style.display = "flex";
  row.style.gap = "6px";

  const saveBtn = document.createElement("button");
  saveBtn.type = "button";
  saveBtn.textContent = "Save label";
  saveBtn.addEventListener("click", () => {
    const target = doc.markers.find((x) => x.id === id);
    if (target) {
      target.label = input.value;
      dirty = true;
      scheduleDocChanged();
      scheduleAutosave();
      labelEl.textContent = target.label || "(unlabelled pin)";
      const m = markersById.get(id);
      if (m) m.getElement()?.setAttribute("title", target.label || `Pin ${id}`);
    }
  });

  const delBtn = document.createElement("button");
  delBtn.type = "button";
  delBtn.className = "danger";
  delBtn.textContent = "Delete";
  delBtn.disabled = config.readOnly || !hasCap("document:write");
  delBtn.addEventListener("click", () => {
    removeMarker(id);
    if (map) map.closePopup();
  });

  row.appendChild(saveBtn);
  row.appendChild(delBtn);

  if (config.readOnly || !hasCap("document:write")) {
    input.disabled = true;
    saveBtn.disabled = true;
  }

  wrap.appendChild(labelEl);
  wrap.appendChild(input);
  wrap.appendChild(row);
  return wrap;
}

function removeMarker(id: string): void {
  if (!map) return;
  const m = markersById.get(id);
  if (!m) return;
  map.removeLayer(m);
  markersById.delete(id);
  const idx = doc.markers.findIndex((x) => x.id === id);
  if (idx >= 0) doc.markers.splice(idx, 1);
  if (highlightedId === id) highlightedId = null;
  dirty = true;
  scheduleDocChanged();
  scheduleAutosave();
}

function clearAllMarkers(): void {
  if (!map) return;
  for (const m of markersById.values()) map.removeLayer(m);
  markersById = new Map();
  doc.markers = [];
  highlightedId = null;
  dirty = true;
  scheduleDocChanged();
  scheduleAutosave();
}

function applyHighlightVisual(): void {
  // Re-set the icon on each marker so the highlight class reflects the
  // current highlightedId. Cheap (a handful of markers).
  for (const [id, m] of markersById) {
    m.setIcon(pinIcon(id === highlightedId));
  }
}

function addMarkerAt(latlng: L.LatLngExpression, label = ""): string {
  const id = nextMarkerId();
  const ll = L.latLng(latlng);
  const marker: MapMarker = { id, lat: ll.lat, lng: ll.lng, label };
  doc.markers.push(marker);
  addMarkerToMap(marker);
  return id;
}

// --- mount + lifecycle -----------------------------------------------------

function mount(): void {
  if (!container) return;
  if (map) return; // idempotent — re-mount goes through remount()

  map = L.map(container, {
    center: L.latLng(doc.lat, doc.lng),
    zoom: doc.zoom,
    minZoom: config.minZoom,
    maxZoom: config.maxZoom,
    zoomControl: !config.readOnly,
    // Even in read-only mode the map can pan/zoom to inspect; only marker
    // editing is locked. Leaflet has no "view-only" flag — we just don't add
    // click-to-add when readOnly.
    attributionControl: true,
  });

  baseLayers = buildBaseLayers();
  const initial = resolvedProvider();
  // Add the active base layer first; the layers control lists all three.
  const active = baseLayers[initial] ?? baseLayers[config.provider] ?? baseLayers["osm"];
  if (active) map.addLayer(active);
  layersControl = L.control.layers(baseLayers, undefined, {
    position: "topright",
    collapsed: true,
  });
  map.addControl(layersControl);
  // Reflect layers-control switches in config.provider + emit docChanged.
  map.on("baselayerchange", (e: L.LayersControlEvent) => {
    // e.name is the human label; find the provider key whose label matches.
    let found: string | null = null;
    for (const key of Object.keys(KNOWN_LAYERS) as LayerKind[]) {
      if (KNOWN_LAYERS[key].label === e.name) {
        found = key;
        break;
      }
    }
    if (found) {
      config.provider = found;
      // Keep doc providers in sync too: the saved doc has no provider field,
      // but the demo's setProvider RPC is the canonical way to persist it.
    }
  });

  map.on("moveend", () => {
    if (!map) return;
    const c = map.getCenter();
    doc.lat = c.lat;
    doc.lng = c.lng;
    dirty = true;
    scheduleDocChanged();
    scheduleAutosave();
  });
  map.on("zoomend", () => {
    if (!map) return;
    doc.zoom = map.getZoom();
    dirty = true;
    scheduleDocChanged();
    scheduleAutosave();
  });
  map.on("click", (e: L.LeafletMouseEvent) => {
    if (config.readOnly || !hasCap("document:write")) return;
    const id = addMarkerAt(e.latlng);
    highlightedId = id;
    applyHighlightVisual();
    sendEvent("markerSelected", { id });
  });

  renderMarkersFromDoc();
  updateHeight();
}

function remount(): void {
  disposeMap();
  mount();
}

function disposeMap(): void {
  if (map) {
    map.remove();
    map = null;
  }
  baseLayers = {};
  layersControl = null;
  markersById = new Map();
}

function updateHeight(): void {
  if (!container || !map) return;
  // The map fills its container; the container height is capped to MAX_MAP_HEIGHT.
  const h = Math.min(container.clientHeight || READY_MIN_HEIGHT, MAX_MAP_HEIGHT);
  container.style.height = `${h}px`;
  map.invalidateSize();
  sendEvent("resize", { height: h });
}

// --- doc sync (host persistence via docChanged + autosave) -----------------

function buildDoc(): MapDoc {
  return {
    lat: doc.lat,
    lng: doc.lng,
    zoom: doc.zoom,
    markers: doc.markers.map((m) => ({ id: m.id, lat: m.lat, lng: m.lng, label: m.label })),
  };
}

function scheduleDocChanged(): void {
  window.clearTimeout(docChangedTimer);
  docChangedTimer = window.setTimeout(emitDocChanged, DOC_CHANGED_DEBOUNCE_MS);
}

function scheduleAutosave(): void {
  window.clearTimeout(autosaveTimer);
  autosaveTimer = window.setTimeout(emitSave, AUTOSAVE_DEBOUNCE_MS);
}

function emitDocChanged(): void {
  if (!hasCap("document:write")) return;
  const d = buildDoc();
  sendEvent("docChanged", {
    lat: d.lat,
    lng: d.lng,
    zoom: d.zoom,
    markers: d.markers,
  });
}

function emitSave(): void {
  if (!hasCap("document:write")) return;
  const d = buildDoc();
  sendEvent("save", {
    lat: d.lat,
    lng: d.lng,
    zoom: d.zoom,
    markers: d.markers,
    schemaVersion: SCHEMA_VERSION,
  });
  dirty = false;
}

function emitThemeApplied(tokens: unknown): void {
  sendEvent("themeApplied", { scheme: scheme, sample: sampleAppliedTokens(tokens) });
}

// --- host -> plugin handlers ----------------------------------------------

function handleInit(params: unknown): void {
  const p = readStringRecord(params);
  capabilities = new Set(Array.isArray(p.capabilities) ? (p.capabilities as string[]) : []);
  if (typeof p.scheme === "string") scheme = p.scheme;
  config = readConfig(p.config);

  // The mount marker's data-fui-plugin-doc carries the saved doc; if present
  // it wins over config.center/zoom/markers (config is just defaults).
  if (hasDoc(p.doc)) {
    doc = readDoc(p.doc);
  } else {
    doc = {
      lat: config.center.lat,
      lng: config.center.lng,
      zoom: config.zoom,
      markers: config.markers.slice(),
    };
  }

  if (hasCap("theme:read")) {
    applyTokens(p.tokens);
    applyScheme(scheme);
  }

  if (typeof p.schemaVersion === "string" && p.schemaVersion !== SCHEMA_VERSION) {
    console.warn(`[map] schemaVersion mismatch: host=${p.schemaVersion} frame=${SCHEMA_VERSION}`);
  }

  mount();
  initialized = true;
  if (hasCap("theme:read")) emitThemeApplied(p.tokens);
  updateHeight();

  // Mirror the just-mounted doc into the host immediately (before any user
  // edit). Without this the host's hidden field stays empty until the first
  // edit, so a plain form POST right after a reload would submit an EMPTY doc,
  // and UIs that read the field (the demo's pin count) would show 0 for a
  // rehydrated map. This is a one-shot mirror over docChanged — NOT an
  // autosave; we never re-persist a doc we just loaded.
  emitDocChanged();
}

function handleThemeChanged(params: unknown): void {
  const p = readStringRecord(params);
  if (typeof p.scheme === "string") scheme = p.scheme;
  applyTokens(p.tokens);
  applyScheme(scheme);
  // In auto-theme, a scheme change may swap the active base layer (light ->
  // carto-light/osm, dark -> carto-dark) without a remount.
  if (config.theme === "auto" && map) {
    const want = resolvedProvider();
    if (want !== currentlyActiveProvider()) {
      setActiveProvider(want);
    }
  }
  emitThemeApplied(p.tokens);
}

function currentlyActiveProvider(): string | null {
  if (!map) return null;
  for (const key of Object.keys(baseLayers)) {
    if (map.hasLayer(baseLayers[key])) return key;
  }
  return null;
}

function handleRequestSave(): MapDoc {
  return buildDoc();
}

function handleSaveResult(params: unknown): void {
  const p = readStringRecord(params);
  if (!statusEl) return;
  if (readBool(p.ok, true)) {
    statusEl.textContent = "";
    statusEl.removeAttribute("role");
    return;
  }
  const status = readNumber(p.status, 0);
  const codeStr = readString(p.code, "E_SAVE");
  const msg = status === 409
    ? "Save conflict — the map changed elsewhere. Your pins were not saved."
    : `Save failed (${codeStr}). Your pins are still here.`;
  statusEl.textContent = msg;
  statusEl.setAttribute("role", "status");
}

// --- demo RPCs (host -> plugin events) -------------------------------------
//
// The demo page calls these via the broker's same postMessage channel monaco's
// `reconfigure` uses. Each is fire-and-forget: the frame applies it and emits
// its own docChanged/save if the doc changed.

function handleFlyTo(params: unknown): void {
  if (!map) return;
  const p = readStringRecord(params);
  const target = readPoint(p, { lat: doc.lat, lng: doc.lng });
  const z = readNumber(p.zoom, doc.zoom);
  map.flyTo([target.lat, target.lng], z, { duration: 0.8 });
}

function handleHighlightMarker(params: unknown): void {
  const p = readStringRecord(params);
  const id = readString(p.id, "");
  if (!id) return;
  highlightedId = id;
  applyHighlightVisual();
  const m = markersById.get(id);
  if (m && map) {
    const ll = m.getLatLng();
    map.panTo(ll, { animate: true });
    m.openPopup();
  }
}

function handleSetProvider(params: unknown): void {
  const p = readStringRecord(params);
  const provider = readString(p.provider, "");
  if (!provider) return;
  // Only accept known providers; the Go proxy 404s the rest anyway, but we
  // avoid pointing Leaflet at a layer that will never resolve.
  if (!(provider in baseLayers)) return;
  setActiveProvider(provider);
  dirty = true;
  scheduleDocChanged();
}

function handleSetReadOnly(params: unknown): void {
  const p = readStringRecord(params);
  const ro = readBool(p.readOnly, !config.readOnly);
  config.readOnly = ro;
  if (map) {
    if (ro) {
      map.dragging.disable();
      map.doubleClickZoom.disable();
      map.boxZoom.disable();
      map.keyboard.disable();
      if (map.zoomControl) map.zoomControl.remove();
    } else {
      map.dragging.enable();
      map.doubleClickZoom.enable();
      map.boxZoom.enable();
      map.keyboard.enable();
      map.addControl(L.control.zoom());
    }
  }
  // Re-set marker draggability. Leaflet exposes Marker.dragging as the
  // MarkerDrag handler (null when the marker was created non-draggable); we
  // disable / enable it in place rather than rebuilding the marker.
  for (const m of markersById.values()) {
    if (m.dragging) {
      if (ro) m.dragging.disable();
      else m.dragging.enable();
    }
  }
}
function handleAddMarker(params: unknown): void {
  if (config.readOnly || !hasCap("document:write")) return;
  const p = readStringRecord(params);
  const pt = readPoint(p, { lat: NaN, lng: NaN });
  if (!Number.isFinite(pt.lat) || !Number.isFinite(pt.lng)) return;
  const label = readString(p.label, "");
  const id = addMarkerAt([pt.lat, pt.lng], label);
  highlightedId = id;
  applyHighlightVisual();
  sendEvent("markerSelected", { id });
}

function handleClearMarkers(): void {
  clearAllMarkers();
}

function handleTeardown(): Record<string, never> {
  teardown();
  return {};
}

// --- lifecycle -------------------------------------------------------------

function teardown(): void {
  if (dirty) emitSave(); // flush before teardown (SPA nav within the debounce)
  window.clearTimeout(docChangedTimer);
  window.clearTimeout(autosaveTimer);
  docChangedTimer = undefined;
  autosaveTimer = undefined;
  disposeMap();
  if (resizeObserver) { resizeObserver.disconnect(); resizeObserver = null; }
  if (messageListener) { window.removeEventListener("message", messageListener); messageListener = null; }
  initialized = false;
}

function isolationProbes(): { cookieEmpty: boolean; parentBlocked: boolean; storageBlocked: boolean } {
  let cookieEmpty = false;
  let parentBlocked = false;
  let storageBlocked = false;
  try { cookieEmpty = document.cookie === ""; } catch { cookieEmpty = true; }
  try { void window.parent.document; parentBlocked = false; } catch { parentBlocked = true; }
  try { void window.localStorage; storageBlocked = false; } catch { storageBlocked = true; }
  return { cookieEmpty, parentBlocked, storageBlocked };
}

function announceReady(): void {
  sendEvent("ready", {
    version: PROTOCOL_VERSION,
    schemaVersion: SCHEMA_VERSION,
    minHeight: READY_MIN_HEIGHT,
    probes: isolationProbes(),
  });
}

function boot(): void {
  try {
    container = document.querySelector<HTMLElement>(CONTAINER_SELECTOR);
    statusEl = document.querySelector<HTMLElement>(STATUS_SELECTOR);
    if (!container) {
      console.error("[map] mount container not found");
      return;
    }

    const handlers: HandlerMap = {
      init: handleInit,
      themeChanged: handleThemeChanged,
      requestSave: handleRequestSave,
      saveResult: handleSaveResult,
      teardown: handleTeardown,
      // Demo RPCs (host -> plugin events; fire-and-forget).
      flyTo: handleFlyTo,
      highlightMarker: handleHighlightMarker,
      setProvider: handleSetProvider,
      setReadOnly: handleSetReadOnly,
      addMarker: handleAddMarker,
      clearMarkers: handleClearMarkers,
    };
    messageListener = createRouter(handlers);
    window.addEventListener("message", messageListener);

    if (typeof ResizeObserver !== "undefined") {
      resizeObserver = new ResizeObserver(() => updateHeight());
      resizeObserver.observe(container);
    }

    // The frame speaks first (the host cannot know when JS finished loading).
    announceReady();
  } catch (err) {
    // Surface any boot-time throw to the host instead of failing silently — a
    // frame that can't boot would otherwise just never send `ready`.
    try {
      const e = err as { stack?: unknown } | null;
      window.parent.postMessage(
        { v: 1, id: "p-booterr", type: "event", src: "plugin", method: "bootError", params: { error: String((e && e.stack) || e) } },
        "*"
      );
    } catch {
      /* postMessage itself failed — nothing more we can do */
    }
  }
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}
