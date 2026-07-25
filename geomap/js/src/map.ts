// GoFastr geomap runtime — a TRUSTED host-page MapLibre GL + OpenFreeMap plugin.
//
// This is NOT a sandboxed plugin (the old Leaflet/raster build was). It runs in
// the host page's own origin with the host page's own CSP, so it can fetch()
// vector tiles and spawn the MapLibre web worker — both impossible under the old
// opaque-origin frame's `connect-src 'none'`. It renders free OpenFreeMap vector
// tiles (https://tiles.openfreemap.org — MIT, no API key, no rate limits).
//
// It ships as a non-framed same-origin <script> injected via UIHostOption (or
// loaded by the demo page). On DOMContentLoaded it scans for mount elements
// `[data-fui-geomap]` and renders a MapLibre map into each. MapLibre's own CSS
// is imported by this bundle and injected as a <style> at load.
//
// Features beyond the base map: editable pin labels + per-pin delete (the popup
// is a live editor, not static text), geolocate + scale controls, a place-search
// control backed by the plugin's SAME-ORIGIN /geocode proxy (never a direct
// third-party call from the browser), and optional marker clustering.
//
// Clustering note: clusters are computed by a `cluster: true` GeoJSON source but
// rendered as DOM markers, NOT as circle/symbol layers. That is deliberate —
// individual pins must stay draggable/editable DOM markers, and DOM cluster
// bubbles need no style glyphs and are themeable + assertable from the host page.
//
// Public controller API is attached to window.__gofastrGeomap (first map) and
// window.__gofastrGeomapAll (every map). See the GeomapController interface.

import * as maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";

// ---------------------------------------------------------------------------
// Public types (named, owned here — consumers import these names, not helpers)
// ---------------------------------------------------------------------------

/** A {lat, lng} coordinate pair. lng/lat order is never confused: this is lat-first. */
export interface GeoPoint {
  lat: number;
  lng: number;
}

/** A single persisted pin. `id` is stable across the session (m_<n>). */
export interface MapMarker {
  id: string;
  lat: number;
  lng: number;
  label?: string;
}

/** The canonical map-v1 document mirrored into the hidden field and POSTed on save. */
export interface MapDoc {
  lat: number;
  lng: number;
  zoom: number;
  markers: MapMarker[];
}

/** The map configuration rendered into the mount element's data-config attribute. */
export interface MapConfig {
  center: GeoPoint;
  zoom: number;
  minZoom: number;
  maxZoom: number;
  /** A style name ("liberty") or full URL; empty ⇒ derived from `theme`. */
  style: string;
  /** Base joined onto a style name; default the OpenFreeMap styles host. */
  styleBaseURL: string;
  /** Options offered by the style-switcher control. */
  styles: string[];
  readOnly: boolean;
  /** "light" | "dark" | "auto" — only consulted when `style` is empty. */
  theme: string;
  markers: MapMarker[];
  /** Show MapLibre's GeolocateControl ("find me"). */
  geolocate: boolean;
  /** Show MapLibre's ScaleControl. */
  scale: boolean;
  /** Same-origin geocode proxy URL. Empty ⇒ no search control is rendered. */
  searchURL: string;
  /** Cluster pins into counted bubbles. */
  cluster: boolean;
  clusterRadius: number;
  clusterMaxZoom: number;
}

/** Parameters for flyTo. */
export interface FlyToParams {
  lat: number;
  lng: number;
  zoom?: number;
}

/** Parameters for addMarker. lat/lng default to the current map center. */
export interface AddMarkerParams {
  lat?: number;
  lng?: number;
  label?: string;
  id?: string;
}

/** One geocoder hit as returned by the plugin's /geocode proxy. */
export interface SearchResult {
  label: string;
  lat: number;
  lng: number;
}

/** Cluster/marker render state — the assertable view of what clustering did. */
export interface ClusterState {
  /** Clustering requested via config or setCluster(). */
  enabled: boolean;
  /** Clustering is actually running (enabled AND the source is live). */
  active: boolean;
  /** Number of cluster bubbles currently rendered. */
  clusters: number;
  /** Number of individual pin markers currently attached to the map. */
  markers: number;
}

/** The controller the host page / demo drives directly (no postMessage bridge). */
export interface GeomapController {
  flyTo(params: FlyToParams): void;
  addMarker(params?: AddMarkerParams): string;
  removeMarker(id: string): boolean;
  setMarkerLabel(id: string, label: string): boolean;
  clearMarkers(): void;
  setStyle(name: string): void;
  setReadOnly(readOnly: boolean): void;
  setCluster(on: boolean): void;
  getClusterState(): ClusterState;
  search(query: string): Promise<SearchResult[]>;
  getDoc(): MapDoc;
  onMarkerSelected(cb: (id: string) => void): void;
  save(): void;
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const DEFAULT_STYLE_BASE = "https://tiles.openfreemap.org/styles/";
const DEFAULT_STYLES = ["liberty", "positron", "dark"];
const SCHEMA_VERSION = "map-v1";
const FIELD_DEBOUNCE_MS = 400;
const SAVE_DEBOUNCE_MS = 2000;
const LABEL_DEBOUNCE_MS = 200;

// OpenFreeMap scheme → style mapping used when no explicit style is set.
const LIGHT_STYLE = "positron";
const DARK_STYLE = "dark";

// The clustering source, plus the invisible layer that keeps it alive.
//
// MapLibre only loads tiles for sources referenced by a layer, so a source with
// no layers at all is never tiled and querySourceFeatures() returns nothing
// forever. We therefore attach one fully transparent circle layer whose entire
// job is to make the renderer materialize the clustered tiles we then read. The
// visible bubbles are DOM markers — see the header note.
const CLUSTER_SOURCE = "fui-geomap-pins";
const CLUSTER_LAYER = "fui-geomap-pins-probe";

// ---------------------------------------------------------------------------
// Config parsing + style resolution
// ---------------------------------------------------------------------------

/** Coerce a parsed JSON value to a finite number, falling back to `def`. */
function num(v: unknown, def: number): number {
  return typeof v === "number" && Number.isFinite(v) ? v : def;
}

/** Coerce a parsed JSON value to a boolean, falling back to `def`. */
function bool(v: unknown, def: boolean): boolean {
  return typeof v === "boolean" ? v : def;
}

/** Coerce a parsed JSON value to a string, falling back to `def`. */
function str(v: unknown, def: string): string {
  return typeof v === "string" ? v : def;
}

/** Narrow a parsed-JSON value to a string-indexed record (call after a guard). */
function asRecord(v: unknown): Record<string, unknown> {
  return v && typeof v === "object" ? (v as Record<string, unknown>) : {};
}

function isMarker(v: unknown): v is MapMarker {
  const m = asRecord(v);
  return typeof m.id === "string" && typeof m.lat === "number" && typeof m.lng === "number";
}

/** Parse and fully default the data-config JSON on the mount element. */
function parseConfig(raw: string | null): MapConfig {
  let parsed: unknown;
  if (raw && raw.trim()) {
    try {
      parsed = JSON.parse(raw);
    } catch {
      parsed = undefined;
    }
  }
  const o = asRecord(parsed);
  const c = asRecord(o["center"]);
  const stylesRaw = o["styles"];
  const styles =
    Array.isArray(stylesRaw) && stylesRaw.length
      ? stylesRaw.filter((s): s is string => typeof s === "string" && s.length > 0)
      : DEFAULT_STYLES.slice();
  return {
    center: { lat: num(c["lat"], 20), lng: num(c["lng"], 0) },
    zoom: num(o["zoom"], 2),
    minZoom: num(o["minZoom"], 0),
    maxZoom: num(o["maxZoom"], 19),
    style: str(o["style"], "liberty"),
    styleBaseURL: str(o["styleBaseURL"], "") || DEFAULT_STYLE_BASE,
    styles,
    readOnly: !!o["readOnly"],
    theme: str(o["theme"], "auto"),
    markers: Array.isArray(o["markers"]) ? o["markers"].filter(isMarker) : [],
    geolocate: bool(o["geolocate"], true),
    scale: bool(o["scale"], true),
    searchURL: str(o["searchURL"], ""),
    cluster: bool(o["cluster"], false),
    clusterRadius: num(o["clusterRadius"], 50),
    clusterMaxZoom: num(o["clusterMaxZoom"], 14),
  };
}

/** Parse the saved-doc JSON (data-doc) for reload re-hydration. */
function parseDoc(raw: string | null): MapDoc | null {
  if (!raw || !raw.trim()) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  const o = asRecord(parsed);
  const lat = o["lat"];
  if (typeof lat !== "number") return null;
  return {
    lat,
    lng: num(o["lng"], 0),
    zoom: num(o["zoom"], 2),
    markers: Array.isArray(o["markers"]) ? o["markers"].filter(isMarker) : [],
  };
}

/** Resolve a config to the OpenFreeMap style NAME plus whether it is explicit. */
function resolveStyleName(cfg: MapConfig, prefersDark: boolean): { name: string; explicit: boolean } {
  const explicit = cfg.style && cfg.style.trim().length > 0;
  if (explicit) return { name: cfg.style.trim(), explicit: true };
  const theme = cfg.theme || "auto";
  const name =
    theme === "dark" ? DARK_STYLE : theme === "auto" && prefersDark ? DARK_STYLE : LIGHT_STYLE;
  return { name, explicit: false };
}

/** Join a style name onto its base, unless it is already a full URL. */
function resolveStyleURL(name: string, base: string): string {
  if (/^https?:\/\//i.test(name)) return name;
  const b = base && base.trim().length ? base : DEFAULT_STYLE_BASE;
  return b.replace(/\/+$/, "") + "/" + name.replace(/^\/+/, "");
}

/** The active color scheme: data-color-scheme on <html>, else the media query. */
function prefersDarkScheme(): boolean {
  const ds = document.documentElement.dataset.colorScheme;
  if (ds === "dark" || ds === "light") return ds === "dark";
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-color-scheme: dark)").matches;
}

/** Round to 6 decimal places — ~11cm, well past what a pin needs. */
function round6(n: number): number {
  return Math.round(n * 1e6) / 1e6;
}

// ---------------------------------------------------------------------------
// Style-switcher control (the "layers" feature)
// ---------------------------------------------------------------------------

class StyleSwitcherControl implements maplibregl.IControl {
  private map?: maplibregl.Map;
  private readonly container: HTMLElement;
  private readonly onPick: (name: string) => void;

  constructor(styles: string[], activeName: string, onPick: (name: string) => void) {
    this.onPick = onPick;
    this.container = document.createElement("div");
    this.container.className = "maplibregl-ctrl maplibregl-ctrl-group fui-style-switcher";
    this.container.setAttribute("aria-label", "Map style");
    for (const name of styles) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "fui-style-opt";
      btn.textContent = name;
      btn.title = "Switch to " + name;
      btn.setAttribute("data-style", name);
      if (name === activeName) btn.classList.add("active");
      btn.addEventListener("click", () => {
        this.setActive(name);
        this.onPick(name);
      });
      this.container.appendChild(btn);
    }
  }

  /** Reflect the active style in the control — also called when the host changes it. */
  setActive(name: string): void {
    this.container.querySelectorAll(".fui-style-opt").forEach((b) => {
      b.classList.toggle("active", b.getAttribute("data-style") === name);
    });
  }

  onAdd(map: maplibregl.Map): HTMLElement {
    this.map = map;
    return this.container;
  }

  onRemove(): void {
    this.container.parentNode?.removeChild(this.container);
    this.map = undefined;
  }
}

// ---------------------------------------------------------------------------
// Search control — talks ONLY to the plugin's same-origin /geocode proxy
// ---------------------------------------------------------------------------

class SearchControl implements maplibregl.IControl {
  private map?: maplibregl.Map;
  private readonly container: HTMLElement;
  private readonly input: HTMLInputElement;
  private readonly results: HTMLElement;
  private readonly status: HTMLElement;
  private readonly run: (q: string) => Promise<SearchResult[]>;
  private readonly onPick: (r: SearchResult) => void;
  private seq = 0;

  constructor(run: (q: string) => Promise<SearchResult[]>, onPick: (r: SearchResult) => void) {
    this.run = run;
    this.onPick = onPick;

    this.container = document.createElement("div");
    this.container.className = "maplibregl-ctrl fui-search";

    const form = document.createElement("form");
    form.className = "fui-search-form";
    form.setAttribute("role", "search");

    this.input = document.createElement("input");
    this.input.type = "search";
    this.input.className = "fui-search-input";
    this.input.placeholder = "Search places…";
    this.input.setAttribute("aria-label", "Search places");

    const btn = document.createElement("button");
    btn.type = "submit";
    btn.className = "fui-search-btn";
    btn.textContent = "Go";
    btn.setAttribute("aria-label", "Search");

    form.appendChild(this.input);
    form.appendChild(btn);

    this.status = document.createElement("div");
    this.status.className = "fui-search-status";
    this.status.setAttribute("role", "status");
    this.status.setAttribute("aria-live", "polite");

    this.results = document.createElement("ul");
    this.results.className = "fui-search-results";
    this.results.hidden = true;

    this.container.appendChild(form);
    this.container.appendChild(this.status);
    this.container.appendChild(this.results);

    form.addEventListener("submit", (e) => {
      e.preventDefault();
      void this.submit();
    });
    // The map swallows keystrokes bound to its own shortcuts; keep them in the box.
    this.container.addEventListener("keydown", (e) => e.stopPropagation());
  }

  private async submit(): Promise<void> {
    const q = this.input.value.trim();
    if (!q) return;
    const mine = ++this.seq;
    this.status.textContent = "Searching…";
    this.results.hidden = true;
    this.results.textContent = "";
    let hits: SearchResult[] = [];
    try {
      hits = await this.run(q);
    } catch (e) {
      if (mine !== this.seq) return;
      this.status.textContent = "Search failed";
      console.warn("geomap: search failed", e);
      return;
    }
    if (mine !== this.seq) return; // a newer search superseded this one
    if (!hits.length) {
      this.status.textContent = "No results";
      return;
    }
    this.status.textContent = hits.length === 1 ? "1 result" : hits.length + " results";
    for (const hit of hits) {
      const li = document.createElement("li");
      const b = document.createElement("button");
      b.type = "button";
      b.className = "fui-search-result";
      b.textContent = hit.label;
      b.title = hit.label;
      b.setAttribute("data-lat", String(hit.lat));
      b.setAttribute("data-lng", String(hit.lng));
      b.addEventListener("click", () => {
        this.results.hidden = true;
        this.status.textContent = hit.label;
        this.onPick(hit);
      });
      li.appendChild(b);
      this.results.appendChild(li);
    }
    this.results.hidden = false;
  }

  onAdd(map: maplibregl.Map): HTMLElement {
    this.map = map;
    return this.container;
  }

  onRemove(): void {
    this.container.parentNode?.removeChild(this.container);
    this.map = undefined;
  }
}

// ---------------------------------------------------------------------------
// Controller
// ---------------------------------------------------------------------------

/** Per-pin popup DOM we keep handles on so read-only can re-gate it in place. */
interface PinUI {
  input: HTMLInputElement;
  del: HTMLButtonElement;
}

// The clustering source's input. Spelled out rather than pulled from a `GeoJSON`
// global namespace: @types/geojson is not a direct dependency here, and this is
// the only GeoJSON shape the runtime ever builds.
interface PinFeature {
  type: "Feature";
  geometry: { type: "Point"; coordinates: [number, number] };
  properties: { id: string; label: string };
}

interface PinCollection {
  type: "FeatureCollection";
  features: PinFeature[];
}

class MapController implements GeomapController {
  readonly el: HTMLElement;
  private readonly cfg: MapConfig;
  private readonly docField: string;
  private readonly docId: string;
  private readonly saveURL: string;
  private readonly field: HTMLInputElement | null;
  private readonly autosave: boolean;
  private readonly map: maplibregl.Map;
  private readonly liveMarkers = new Map<string, maplibregl.Marker>();
  private readonly pinUI = new Map<string, PinUI>();
  private readonly attached = new Set<string>();
  private clusterMarkers: maplibregl.Marker[] = [];
  private markerDocs: MapMarker[] = [];
  private readOnly: boolean;
  private counter = 0;
  private selectedCb: ((id: string) => void) | null = null;
  private explicitStyle: boolean;
  private currentStyleName: string;
  private styleSwitcher: StyleSwitcherControl | null = null;
  private clusterEnabled: boolean;
  private styleReady = false;
  private clusterSourceReady = false;
  private clusterCount = 0;
  private syncFrame = 0;
  private fieldTimer: number | undefined;
  private saveTimer: number | undefined;

  constructor(el: HTMLElement) {
    this.el = el;
    this.cfg = parseConfig(el.getAttribute("data-config"));

    // A saved doc (data-doc) OVERRIDES config center/zoom/markers on reload.
    const doc = parseDoc(el.getAttribute("data-doc"));
    if (doc) {
      this.cfg.center = { lat: doc.lat, lng: doc.lng };
      this.cfg.zoom = doc.zoom;
      this.cfg.markers = doc.markers;
    }

    this.docField = el.getAttribute("data-doc-field") || "map_doc";
    this.docId = el.getAttribute("data-doc-id") || "demo";
    this.saveURL =
      el.getAttribute("data-save-url") ||
      (window.__gofastrGeomapSaveURL as string | undefined) ||
      "";
    // Autosave is on unless the mount explicitly opts out with data-autosave="false".
    this.autosave = el.getAttribute("data-autosave") !== "false";
    this.readOnly = this.cfg.readOnly;
    this.clusterEnabled = this.cfg.cluster;

    const resolved = resolveStyleName(this.cfg, prefersDarkScheme());
    this.currentStyleName = resolved.name;
    this.explicitStyle = resolved.explicit;

    this.field = document.querySelector<HTMLInputElement>('input[name="' + this.docField + '"]');

    // Construction. __mapReady is set SYNCHRONOUSLY here, before style/tiles
    // load — it is the e2e readiness signal and MUST NOT depend on the network
    // (CI may have no route to tiles.openfreemap.org). On a construct-time
    // failure (e.g. no WebGL) we surface __mapError and let it throw to the
    // factory, which logs + reports it.
    this.map = new maplibregl.Map({
      container: el,
      style: resolveStyleURL(this.currentStyleName, this.cfg.styleBaseURL),
      center: [this.cfg.center.lng, this.cfg.center.lat],
      zoom: this.cfg.zoom,
      minZoom: this.cfg.minZoom,
      maxZoom: this.cfg.maxZoom,
      // Attribution defaults ON (OpenFreeMap/OSM license REQUIRES it) — never disable.
    });
    window.__mapReady = true;

    this.map.on("load", () => {
      window.__mapStyleLoaded = true;
      this.styleReady = true;
      this.ensureClusterSource();
    });
    // styledata fires continuously as a vector style streams tiles — it is NOT a
    // "style replaced" signal. Treat it as a nudge and re-derive readiness from
    // whether the source actually survived; resetting a flag here unconditionally
    // races the tile stream and leaves clustering permanently inactive.
    this.map.on("styledata", () => {
      if (!this.clusterEnabled) return;
      if (this.clusterSourceReady && !this.map.getSource(CLUSTER_SOURCE)) {
        this.clusterSourceReady = false;
      }
      this.ensureClusterSource();
    });

    this.addControls();

    // Center/zoom changes flow back into the canonical doc.
    const onMove = (): void => {
      this.scheduleSync();
      this.scheduleRender();
    };
    this.map.on("moveend", onMove);
    this.map.on("zoomend", onMove);
    this.map.on("idle", () => this.scheduleRender());
    // The clustered tiles land asynchronously after addSource/setData. Without
    // this the first render finds an empty source and nothing ever re-runs.
    this.map.on("sourcedata", (e) => {
      if (e.sourceId === CLUSTER_SOURCE && e.isSourceLoaded) this.scheduleRender();
    });

    // Click-to-add (disabled in read-only mode).
    //
    // The guard is load-bearing. MapLibre toggles a marker's popup from the MAP's
    // click event (Marker._onMapClick inspects the event target), so a marker
    // click MUST be allowed to reach the map — calling stopPropagation on the
    // marker silently disables every popup. Instead we let the event through and
    // ignore clicks that landed on a marker or inside a popup, which also covers
    // the popup's own close button and the label editor.
    this.map.on("click", (e) => {
      if (this.readOnly) return;
      const target = e.originalEvent.target as Element | null;
      if (target && typeof target.closest === "function") {
        if (target.closest(".maplibregl-marker, .maplibregl-popup")) return;
      }
      this.addMarker({ lat: e.lngLat.lat, lng: e.lngLat.lng, label: "Pin" });
    });

    // Seed the initial markers from config (or the saved-doc override).
    this.seedMarkers(this.cfg.markers);

    // Follow host-page scheme changes when the style is theme-derived. With an
    // explicit style (the default "liberty") this is a no-op.
    this.observeScheme();

    // Populate the hidden field baseline immediately so consumers (e2e, the demo
    // pin-count) read a valid doc before the first interaction.
    this.writeField();
  }

  // --- public API ----------------------------------------------------------

  flyTo(params: FlyToParams): void {
    const zoom = typeof params.zoom === "number" ? params.zoom : this.map.getZoom();
    this.map.flyTo({ center: [params.lng, params.lat], zoom });
  }

  addMarker(params?: AddMarkerParams): string {
    const p = params || {};
    const c = this.map.getCenter();
    const lat = typeof p.lat === "number" ? p.lat : c.lat;
    const lng = typeof p.lng === "number" ? p.lng : c.lng;
    const label = typeof p.label === "string" && p.label.length ? p.label : "Pin";
    const id = p.id && p.id.length ? p.id : "m_" + ++this.counter;
    this.createMarker(id, lat, lng, label);
    this.markerDocs.push({ id, lat: round6(lat), lng: round6(lng), label });
    this.afterMarkersChanged();
    return id;
  }

  removeMarker(id: string): boolean {
    const marker = this.liveMarkers.get(id);
    if (!marker) return false;
    marker.remove();
    this.liveMarkers.delete(id);
    this.pinUI.delete(id);
    this.attached.delete(id);
    this.markerDocs = this.markerDocs.filter((m) => m.id !== id);
    this.afterMarkersChanged();
    return true;
  }

  setMarkerLabel(id: string, label: string): boolean {
    const entry = this.markerDocs.find((m) => m.id === id);
    if (!entry) return false;
    entry.label = label;
    const ui = this.pinUI.get(id);
    if (ui && ui.input.value !== label) ui.input.value = label;
    this.scheduleSync();
    return true;
  }

  clearMarkers(): void {
    this.liveMarkers.forEach((m) => m.remove());
    this.liveMarkers.clear();
    this.pinUI.clear();
    this.attached.clear();
    this.markerDocs = [];
    this.afterMarkersChanged();
  }

  setStyle(name: string): void {
    this.currentStyleName = name;
    this.explicitStyle = true;
    this.styleSwitcher?.setActive(name);
    this.applyStyle(name);
  }

  /**
   * Swap the base style. setStyle() tears down every source and layer, so our
   * clustering source has to be rebuilt on the far side — and until the new
   * style exists there is nothing to add it to.
   */
  private applyStyle(name: string): void {
    this.styleReady = false;
    this.clusterSourceReady = false;
    this.clearClusterMarkers();
    this.map.setStyle(resolveStyleURL(name, this.cfg.styleBaseURL));
    // setStyle() rebuilds sources/layers but does NOT remove maplibregl.Marker
    // overlays — they live in a DOM container setStyle never touches, so they
    // persist across a swap (verified). We defensively re-attach any marker
    // whose element lost its parent, in case a future MapLibre changes that.
    this.map.once("styledata", () => {
      this.styleReady = true;
      this.liveMarkers.forEach((m, id) => {
        if (this.attached.has(id) && !m.getElement().isConnected) m.addTo(this.map);
      });
      this.ensureClusterSource();
      this.scheduleRender();
    });
  }

  setReadOnly(readOnly: boolean): void {
    this.readOnly = readOnly;
    this.liveMarkers.forEach((m) => m.setDraggable(!readOnly));
    // Re-gate every open/closed popup editor in place rather than rebuilding it.
    this.pinUI.forEach((ui) => {
      ui.input.readOnly = readOnly;
      ui.del.hidden = readOnly;
    });
  }

  setCluster(on: boolean): void {
    if (this.clusterEnabled === on) return;
    this.clusterEnabled = on;
    if (on) {
      this.ensureClusterSource();
    } else {
      this.removeClusterSource();
    }
    this.renderMarkers();
  }

  getClusterState(): ClusterState {
    return {
      enabled: this.clusterEnabled,
      active: this.clusterActive(),
      clusters: this.clusterCount,
      markers: this.attached.size,
    };
  }

  async search(query: string): Promise<SearchResult[]> {
    const q = query.trim();
    if (!q || !this.cfg.searchURL) return [];
    const sep = this.cfg.searchURL.indexOf("?") >= 0 ? "&" : "?";
    const url = this.cfg.searchURL + sep + "q=" + encodeURIComponent(q);
    const res = await fetch(url, {
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    });
    if (!res.ok) throw new Error("geocode failed: " + res.status);
    const body: unknown = await res.json();
    const raw = asRecord(body)["results"];
    if (!Array.isArray(raw)) return [];
    return raw
      .map((r) => asRecord(r))
      .filter((r) => typeof r["lat"] === "number" && typeof r["lng"] === "number")
      .map((r) => ({ label: str(r["label"], ""), lat: r["lat"] as number, lng: r["lng"] as number }));
  }

  getDoc(): MapDoc {
    return this.currentDoc();
  }

  onMarkerSelected(cb: (id: string) => void): void {
    this.selectedCb = cb;
  }

  save(): void {
    void this.postSave();
  }

  // --- controls ------------------------------------------------------------

  private addControls(): void {
    this.map.addControl(new maplibregl.NavigationControl(), "top-left");
    if (this.cfg.geolocate) {
      // trackUserLocation gives the control its follow/lock states. It only ever
      // prompts for permission on an explicit user click — never on load.
      this.map.addControl(
        new maplibregl.GeolocateControl({
          positionOptions: { enableHighAccuracy: true },
          trackUserLocation: true,
          showUserLocation: true,
        }),
        "top-left",
      );
    }
    this.styleSwitcher = new StyleSwitcherControl(this.cfg.styles, this.currentStyleName, (name) =>
      this.setStyle(name),
    );
    this.map.addControl(this.styleSwitcher, "top-right");
    if (this.cfg.searchURL) {
      this.map.addControl(
        new SearchControl(
          (q) => this.search(q),
          (hit) => this.flyTo({ lat: hit.lat, lng: hit.lng, zoom: Math.max(this.map.getZoom(), 11) }),
        ),
        "top-right",
      );
    }
    if (this.cfg.scale) {
      this.map.addControl(new maplibregl.ScaleControl({ maxWidth: 96, unit: "metric" }), "bottom-left");
    }
  }

  // --- markers -------------------------------------------------------------

  /** Seed markers from an initial list without scheduling a sync. */
  private seedMarkers(markers: MapMarker[]): void {
    for (const m of markers) {
      const id = m.id && m.id.length ? m.id : "m_" + ++this.counter;
      const label = m.label && m.label.length ? m.label : "Pin";
      this.createMarker(id, m.lat, m.lng, label);
      this.markerDocs.push({ id, lat: round6(m.lat), lng: round6(m.lng), label });
    }
    this.updateClusterData();
  }

  /**
   * Build one pin: a draggable MapLibre marker whose popup is a LIVE EDITOR
   * (label input + delete), not static text. The input writes straight through
   * to the canonical doc on a short debounce; delete drops the pin entirely.
   */
  private createMarker(id: string, lat: number, lng: number, label: string): void {
    const content = document.createElement("div");
    content.className = "fui-pin-pop";

    const input = document.createElement("input");
    input.type = "text";
    input.className = "fui-pin-label";
    input.value = label;
    input.readOnly = this.readOnly;
    input.setAttribute("aria-label", "Pin label");
    input.setAttribute("data-pin", id);

    const actions = document.createElement("div");
    actions.className = "fui-pin-actions";

    const coord = document.createElement("span");
    coord.className = "fui-pin-coord";
    coord.textContent = round6(lat).toFixed(4) + ", " + round6(lng).toFixed(4);

    const del = document.createElement("button");
    del.type = "button";
    del.className = "fui-pin-delete";
    del.textContent = "Delete";
    del.hidden = this.readOnly;
    del.setAttribute("data-pin", id);

    actions.appendChild(coord);
    actions.appendChild(del);
    content.appendChild(input);
    content.appendChild(actions);

    let labelTimer: number | undefined;
    input.addEventListener("input", () => {
      clearTimeout(labelTimer);
      const value = input.value;
      labelTimer = setTimeout(() => this.setMarkerLabel(id, value), LABEL_DEBOUNCE_MS);
    });
    // Blur commits immediately — a popup closed before the debounce fired must
    // not silently drop the edit.
    input.addEventListener("blur", () => {
      clearTimeout(labelTimer);
      this.setMarkerLabel(id, input.value);
    });
    // The map binds its own keyboard shortcuts; keep typing inside the input.
    content.addEventListener("keydown", (e) => e.stopPropagation());

    del.addEventListener("click", () => {
      this.removeMarker(id);
    });

    const popup = new maplibregl.Popup({ offset: 25, closeButton: true }).setDOMContent(content);
    const marker = new maplibregl.Marker({ draggable: !this.readOnly })
      .setLngLat([lng, lat])
      .setPopup(popup);

    marker.on("dragend", () => {
      const ll = marker.getLngLat();
      const entry = this.markerDocs.find((m) => m.id === id);
      if (entry) {
        entry.lat = round6(ll.lat);
        entry.lng = round6(ll.lng);
      }
      coord.textContent = round6(ll.lat).toFixed(4) + ", " + round6(ll.lng).toFixed(4);
      this.afterMarkersChanged();
    });

    // Cluster bubbles are maplibregl.Markers too, so `.maplibregl-marker` alone
    // cannot tell a pin from a bubble. Tag pins explicitly.
    const markerEl = marker.getElement();
    markerEl.classList.add("fui-pin");
    markerEl.setAttribute("data-pin", id);
    // NO stopPropagation here — see the map click handler. MapLibre needs this
    // event to reach the map to toggle the popup.
    markerEl.addEventListener("click", () => {
      if (this.selectedCb) this.selectedCb(id);
    });

    this.liveMarkers.set(id, marker);
    this.pinUI.set(id, { input, del });
    // Attachment is owned by renderMarkers(); in cluster mode this pin may
    // legitimately never attach (it lives inside a bubble instead).
    this.attach(id, marker);
  }

  private attach(id: string, marker: maplibregl.Marker): void {
    if (this.attached.has(id)) return;
    marker.addTo(this.map);
    this.attached.add(id);
  }

  private detach(id: string, marker: maplibregl.Marker): void {
    if (!this.attached.has(id)) return;
    marker.remove();
    this.attached.delete(id);
  }

  /** One place to run everything a marker mutation implies. */
  private afterMarkersChanged(): void {
    this.updateClusterData();
    this.scheduleSync();
    this.renderMarkers();
  }

  // --- clustering ----------------------------------------------------------

  private clusterActive(): boolean {
    return this.clusterEnabled && this.clusterSourceReady;
  }

  /** The pin set as GeoJSON — the clustering source's only input. */
  private pinGeoJSON(): PinCollection {
    return {
      type: "FeatureCollection",
      features: this.markerDocs.map((m) => ({
        type: "Feature" as const,
        geometry: { type: "Point" as const, coordinates: [m.lng, m.lat] as [number, number] },
        properties: { id: m.id, label: m.label || "" },
      })),
    };
  }

  /**
   * Add the clustering source once the style is live. It carries NO layers: we
   * use it purely as supercluster-in-a-source and render the result as DOM.
   */
  private ensureClusterSource(): void {
    if (!this.clusterEnabled || this.clusterSourceReady) return;
    // NOT isStyleLoaded(): that reports "no pending style work" and flickers
    // false for as long as a vector style keeps streaming tiles, which on a
    // busy map is essentially always. We only need the style to EXIST.
    if (!this.styleReady) return;
    try {
      if (!this.map.getSource(CLUSTER_SOURCE)) {
        this.map.addSource(CLUSTER_SOURCE, {
          type: "geojson",
          data: this.pinGeoJSON(),
          cluster: true,
          clusterRadius: this.cfg.clusterRadius,
          clusterMaxZoom: this.cfg.clusterMaxZoom,
        });
      }
      if (!this.map.getLayer(CLUSTER_LAYER)) {
        // Transparent, but present: without a layer the source is never tiled.
        this.map.addLayer({
          id: CLUSTER_LAYER,
          type: "circle",
          source: CLUSTER_SOURCE,
          paint: { "circle-radius": 1, "circle-opacity": 0 },
        });
      }
      this.clusterSourceReady = true;
      this.scheduleRender();
    } catch (e) {
      // A source add can only fail if the style went away mid-flight; leave
      // clustering inactive and keep rendering plain markers.
      console.warn("geomap: cluster source unavailable", e);
      this.clusterSourceReady = false;
    }
  }

  private removeClusterSource(): void {
    this.clusterSourceReady = false;
    this.clearClusterMarkers();
    try {
      // Layer first: a source still referenced by a layer cannot be removed.
      if (this.map.getLayer(CLUSTER_LAYER)) this.map.removeLayer(CLUSTER_LAYER);
      if (this.map.getSource(CLUSTER_SOURCE)) this.map.removeSource(CLUSTER_SOURCE);
    } catch {
      // Style already torn down — nothing to remove.
    }
  }

  /** Push the current pin set into the clustering source. */
  private updateClusterData(): void {
    if (!this.clusterSourceReady) return;
    const src = this.map.getSource(CLUSTER_SOURCE) as maplibregl.GeoJSONSource | undefined;
    if (src && typeof src.setData === "function") src.setData(this.pinGeoJSON());
  }

  private clearClusterMarkers(): void {
    for (const m of this.clusterMarkers) m.remove();
    this.clusterMarkers = [];
    this.clusterCount = 0;
  }

  /** Coalesce render passes — moveend/idle/sourcedata all land in one frame. */
  private scheduleRender(): void {
    if (this.syncFrame) return;
    const raf =
      typeof requestAnimationFrame === "function"
        ? requestAnimationFrame
        : (cb: FrameRequestCallback): number => setTimeout(() => cb(0), 16) as unknown as number;
    this.syncFrame = raf(() => {
      this.syncFrame = 0;
      this.renderMarkers();
    });
  }

  /**
   * Decide, for the current viewport, which pins are their own DOM marker and
   * which are folded into a cluster bubble. With clustering off (or the source
   * not live — e.g. the style never loaded) every pin simply shows.
   */
  private renderMarkers(): void {
    if (!this.clusterActive()) {
      this.clearClusterMarkers();
      this.liveMarkers.forEach((m, id) => this.attach(id, m));
      return;
    }

    let features: maplibregl.GeoJSONFeature[] = [];
    try {
      features = this.map.querySourceFeatures(CLUSTER_SOURCE);
    } catch {
      // Source not queryable yet; keep the previous frame's rendering.
      return;
    }
    // The source has no tiles loaded yet — showing nothing would blank the map,
    // so hold the plain rendering until it does.
    if (!features.length && this.markerDocs.length) {
      this.liveMarkers.forEach((m, id) => this.attach(id, m));
      return;
    }

    const loose = new Set<string>();
    const clusters = new Map<number, { lng: number; lat: number; count: number }>();
    for (const f of features) {
      const props = asRecord(f.properties);
      const geom = f.geometry;
      if (props["cluster"]) {
        const cid = props["cluster_id"];
        if (typeof cid !== "number" || clusters.has(cid)) continue;
        if (geom.type !== "Point") continue;
        clusters.set(cid, {
          lng: geom.coordinates[0],
          lat: geom.coordinates[1],
          count: num(props["point_count"], 0),
        });
      } else {
        const id = props["id"];
        if (typeof id === "string") loose.add(id);
      }
    }

    this.liveMarkers.forEach((m, id) => {
      if (loose.has(id)) this.attach(id, m);
      else this.detach(id, m);
    });

    this.clearClusterMarkers();
    clusters.forEach((c, cid) => {
      const el = document.createElement("button");
      el.type = "button";
      el.className = "fui-cluster";
      el.textContent = String(c.count);
      el.setAttribute("data-count", String(c.count));
      el.setAttribute("aria-label", c.count + " pins — click to zoom in");
      el.addEventListener("click", (ev) => {
        ev.stopPropagation();
        void this.expandCluster(cid, c.lng, c.lat);
      });
      this.clusterMarkers.push(new maplibregl.Marker({ element: el }).setLngLat([c.lng, c.lat]).addTo(this.map));
    });
    this.clusterCount = this.clusterMarkers.length;
  }

  /** Zoom to the level at which a cluster breaks apart. */
  private async expandCluster(clusterId: number, lng: number, lat: number): Promise<void> {
    const src = this.map.getSource(CLUSTER_SOURCE) as maplibregl.GeoJSONSource | undefined;
    let zoom = this.map.getZoom() + 2;
    if (src && typeof src.getClusterExpansionZoom === "function") {
      try {
        // MapLibre v5 returns a Promise here; older builds took a callback.
        zoom = await Promise.resolve(src.getClusterExpansionZoom(clusterId));
      } catch {
        // Fall back to the +2 nudge above.
      }
    }
    this.map.easeTo({ center: [lng, lat], zoom });
  }

  // --- doc sync ------------------------------------------------------------

  /** Build the canonical doc from the live map center/zoom + marker docs. */
  private currentDoc(): MapDoc {
    const c = this.map.getCenter();
    const z = this.map.getZoom();
    return {
      lat: round6(c.lat),
      lng: round6(c.lng),
      zoom: Math.round(z * 1e4) / 1e4,
      markers: this.markerDocs.map((m) => ({ ...m })),
    };
  }

  /** Debounce field writes + autosaves so a pan/zoom storm stays cheap. */
  private scheduleSync(): void {
    clearTimeout(this.fieldTimer);
    this.fieldTimer = setTimeout(() => this.writeField(), FIELD_DEBOUNCE_MS);
    if (this.autosave && this.saveURL) {
      clearTimeout(this.saveTimer);
      this.saveTimer = setTimeout(() => void this.postSave(), SAVE_DEBOUNCE_MS);
    }
  }

  private writeField(): void {
    if (!this.field) return;
    this.field.value = JSON.stringify(this.currentDoc());
  }

  private async postSave(): Promise<void> {
    if (!this.saveURL) return;
    const doc = this.currentDoc();
    try {
      const res = await fetch(this.saveURL, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          docId: this.docId,
          doc,
          lat: doc.lat,
          lng: doc.lng,
          zoom: doc.zoom,
          markers: doc.markers,
          schemaVersion: SCHEMA_VERSION,
        }),
      });
      if (!res.ok) console.warn("geomap: save failed", res.status);
    } catch (e) {
      console.warn("geomap: save error", e);
    }
  }

  /** Re-derive the theme style when the host scheme flips and no explicit style is set. */
  private observeScheme(): void {
    if (typeof MutationObserver !== "function") return;
    const obs = new MutationObserver(() => {
      if (this.explicitStyle) return;
      const { name } = resolveStyleName(this.cfg, prefersDarkScheme());
      if (name !== this.currentStyleName) {
        this.currentStyleName = name;
        this.styleSwitcher?.setActive(name);
        // applyStyle, not setStyle: a theme-driven swap must NOT set
        // explicitStyle, or the map stops following the host scheme after one flip.
        this.applyStyle(name);
      }
    });
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ["data-color-scheme"] });
  }
}

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

function createController(el: HTMLElement): MapController | null {
  try {
    return new MapController(el);
  } catch (e) {
    window.__mapError = String(e);
    console.error("geomap: mount failed", e);
    return null;
  }
}

function boot(): void {
  const mounts = Array.from(document.querySelectorAll<HTMLElement>("[data-fui-geomap]"));
  const all = mounts.map(createController).filter((c): c is MapController => c !== null);
  window.__gofastrGeomapAll = all;
  if (all.length > 0 && !window.__gofastrGeomap) window.__gofastrGeomap = all[0];
  window.dispatchEvent(new Event("gofastr:geomap-ready"));
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}

// ---------------------------------------------------------------------------
// Window augmentation
// ---------------------------------------------------------------------------

declare global {
  interface Window {
    __mapReady?: boolean;
    __mapStyleLoaded?: boolean;
    __mapError?: string;
    __gofastrGeomap?: GeomapController;
    __gofastrGeomapAll?: GeomapController[];
    __gofastrGeomapSaveURL?: string;
  }
}
