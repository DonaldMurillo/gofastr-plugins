# Geomap (MapLibre GL + OpenFreeMap) plugin

An interactive **vector** map plugin built on [MapLibre GL JS](https://maplibre.org/)
rendering free [OpenFreeMap](https://openfreemap.org/) vector tiles. Pan, zoom,
click-to-drop pins, draggable markers with editable labels and per-pin delete,
an in-map style switcher, geolocate + scale controls, optional place search
through a same-origin geocode proxy, optional marker clustering, and a fly-to
side panel.

**This is a TRUSTED host-page plugin** — like the `tour` plugin, it runs in the
host page's own origin with the host page's own CSP, NOT inside a sandboxed
opaque-origin iframe. A vector map MUST `fetch()` tiles and spawn the MapLibre
web worker, both impossible under an opaque frame's `connect-src 'none'`. The
old Leaflet/raster build ran sandboxed and proxied raster tiles server-side to
work around that; vector tiles made the tradeoff explicit: the map is trusted.

The Go package is `geomap` (the identifier `map` is a Go keyword), but the
user-facing identity strings are `"map"`:

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/geomap`
- **Name:** `map`
- **Version:** `0.3.0`
- **Route prefix:** `/__gofastr/plugin/map`
- **Demo URL:** `/map`
- **Isolation:** `trusted-host-page` (no sandbox, no broker)
- **Canonical doc (schema `map-v1`):** `{lat, lng, zoom, markers}` where each
  marker is `{id, lat, lng, label?}`
- **Capabilities:** `document:read`, `document:write`, `theme:read`, plus
  `geocode:search` **only when place search is enabled** (see below)

## OpenFreeMap

OpenFreeMap (served from `https://tiles.openfreemap.org`) is **MIT-licensed,
free for commercial use, no API key, no rate limits, no cookies**. Every asset a
style references — style JSON, `planet` TileJSON, `.pbf` vector tiles, `fonts/`
glyphs, `sprites/`, `natural_earth/` raster insets — lives under that single
host, so the CSP host allowlist below is exactly that origin.

Attribution (OpenStreetMap + OpenMapTiles) is **auto-added by MapLibre from the
style**. Do not strip the attribution control — it is a license requirement.

Available styles: `liberty`, `bright`, `positron`, `dark`, `fiord` (style URLs
are `https://tiles.openfreemap.org/styles/{name}`).

## The host-page CSP consumers MUST set

Because the map runs in the host page, the host page's own CSP governs it.
MapLibre GL needs to fetch OpenFreeMap, spawn a blob web worker, and load
glyphs/sprites/raster insets. It does **NOT** need `unsafe-eval` or WASM. A page
rendering the map MUST advertise a policy equivalent to:

```
default-src 'self';
connect-src 'self' https://tiles.openfreemap.org;
img-src 'self' data: blob: https://tiles.openfreemap.org;
worker-src blob:;
child-src blob:;
style-src 'self' 'unsafe-inline';
script-src 'self' 'unsafe-inline';
frame-ancestors 'self';
base-uri 'self'
```

(`script-src 'unsafe-inline'` is only for pages that carry an inline `<script>`;
`map.js` itself is served `'self'`. The demo page sets this header verbatim — see
`geomap/plugin.go` → `hostPageCSP`.)

If you self-host OpenFreeMap or front it with your own CDN via
`WithStyleBaseURL`, allowlist **your** host in `connect-src`/`img-src` instead.

## Mounting

```go
app.RegisterPlugin(geomap.New(
    geomap.WithDevGrantAll(),    // demo/dev only — opens the capability gate
    geomap.WithDemoPage(),       // serves the themed showcase at /map
    geomap.WithCenter(40.7128, -74.006),
    geomap.WithZoom(11),
    geomap.WithStyle("positron"),
))
```

There is **no platform manifest and no broker** (this is a trusted host-page
plugin). `Init` serves `map.js` + `map.css` as NON-framed host-page assets and
registers the save endpoint. `UIHostOption()` injects the runtime into every
UIHost-rendered page; a host wanting the demo overlay links `MapCSSURL` itself.

`geomap.WithSaveHandler(func(ctx, req) error { ... })` overrides persistence. The
default stores the canonical doc in memory keyed by DocID. Return
`geomap.ErrConflict` to signal an optimistic-concurrency conflict — it maps to
HTTP 409 (`E_CONFLICT`) rather than the generic 500 (`E_SAVE`) (same contract as
monaco and richtext).

### Mounting the map into a page

Render `Plugin.Mount(MountConfig{...})` where you want the map. It emits a plain
host-page element (NOT the sandboxed-iframe broker marker):

```html
<div data-fui-geomap data-doc-id="demo" data-doc-field="map_doc"
     data-save-url="/__gofastr/plugin/map/save" data-min-height="360px"
     data-config="{...MapConfig JSON...}" data-doc="{...saved doc...}"
     style="min-height:360px"></div>
<input type="hidden" name="map_doc">
```

`map.js` scans for `[data-fui-geomap]` on `DOMContentLoaded` and constructs a
MapLibre map into each. `data-config` carries the instance's `MapConfig` (set via
the `With*` options); `data-doc` carries an optional saved doc that OVERRIDES
config center/zoom/markers on reload. The hidden input named by `data-doc-field`
(default `map_doc`) is the canonical doc mirror the controller writes on every
change (debounced) — read it to observe the map state.

## The persisted document

The canonical doc (schema `map-v1`) is plain JSON:

```json
{
  "lat": 40.7128,
  "lng": -74.006,
  "zoom": 11,
  "markers": [
    { "id": "m_1", "lat": 40.7128, "lng": -74.006, "label": "New York" }
  ]
}
```

**The json struct tags on `mapDoc` / `mapMarker` are load-bearing**: `map.js`
reads lowercase `{lat, lng, zoom, markers}`. Without the tags Go would emit
`{Lat, Lng, Zoom, Markers}` and the map would silently mount empty on reload.
`TestSaveRoundTripDevGrant` asserts the wire shape.

## Configuration

Map behaviour is configurable per-instance via `With…` options, serialized into
the mount element's `data-config`:

| Option              | Field          | Default                              |
|---------------------|----------------|--------------------------------------|
| `WithCenter`        | `center`       | `{lat: 20, lng: 0}`                  |
| `WithZoom`          | `zoom`         | `2`                                  |
| `WithMinZoom`       | `minZoom`      | `0`                                  |
| `WithMaxZoom`       | `maxZoom`      | `19`                                 |
| `WithStyle`         | `style`        | `"liberty"`                          |
| `WithStyleBaseURL`  | `styleBaseURL` | `https://tiles.openfreemap.org/styles/` |
| `WithStyles`        | `styles`       | `["liberty","positron","dark"]`      |
| `WithReadOnly`      | `readOnly`     | `false`                              |
| `WithMarkers`       | `markers`      | `[]`                                 |
| `WithTheme`         | `theme`        | `"auto"`                             |
| `WithoutGeolocateControl` | `geolocate` | `true`                            |
| `WithoutScaleControl`     | `scale`     | `true`                            |
| `WithSearch` (and friends) | `searchURL` | `""` (search off)                |
| `WithClustering`    | `cluster`      | `false`                              |
| `WithClusterRadius` | `clusterRadius` | `50`                                |
| `WithClusterMaxZoom`| `clusterMaxZoom` | `14`                               |

`style` is either an OpenFreeMap style name (`"liberty"`) or a full style URL.
`New()` rejects an empty `style` or an empty `style`-switcher entry (fail-loud);
a wrong-but-non-empty style name is left alone (it just 404s a tile, non-fatal).

`WithStyleBaseURL` is the **self-host / CDN hook**: point it at your own base and
allowlist that host in your page CSP. `WithStyles` sets the in-map style
switcher's options.

`theme` (`"light"` | `"dark"` | `"auto"`) is only consulted by `map.js` when no
explicit `style` is set (the Go default always ships an explicit style, so it is
a fallback for hand-written mounts): `"auto"` picks `positron` (light) / `dark`
based on `document.documentElement.dataset.colorScheme` (fallback
`prefers-color-scheme`).

## Pin editing

Clicking a pin opens its popup, which is a **live editor**, not static text: a
label input that writes straight through to the canonical doc (200 ms debounce,
plus an immediate commit on blur) and a Delete button that removes that one pin.
`WithReadOnly` / `setReadOnly(true)` re-gates open popups in place — the input
goes read-only and Delete is hidden — so a read-only map cannot be edited through
a popup that was already on screen.

> **Implementation note.** MapLibre toggles a marker's popup from the **map's**
> click event (`Marker._onMapClick` inspects the event target), so a marker click
> must be allowed to reach the map. Calling `stopPropagation()` on the marker
> element silently disables every popup. The runtime therefore lets the event
> through and instead ignores map clicks whose target sits inside
> `.maplibregl-marker` or `.maplibregl-popup`.

## Place search

Search is **opt-in**. Enabled, it renders a search box in the map's top-right
corner and registers `GET /__gofastr/plugin/map/geocode?q=…`, which answers
`{"results":[{"label","lat","lng"}, …]}`.

```go
geomap.New(
    geomap.WithSearch(),
    geomap.WithGeocodeUserAgent("acme-maps/1.4 (+https://acme.example/contact)"),
)
```

**The browser never calls a geocoder directly.** `map.js` only ever talks to the
plugin's own origin, and the plugin proxies upstream. That is what buys:

- a policy-compliant, identifying `User-Agent` — Nominatim requires one and
  blocks anonymous traffic; a page `fetch()` cannot set it;
- a **server-side** rate limit — Nominatim's policy caps an *application* at
  1 request/second, an application-wide budget only the server can enforce;
- caching (the same policy explicitly asks for it — results are cached 10 min);
- a host page CSP that stays at `connect-src 'self'`. No third-party origin has
  to be allowlisted for search.

| Option                   | Effect                                                        |
|--------------------------|---------------------------------------------------------------|
| `WithSearch()`           | Enable search against the default public Nominatim endpoint.  |
| `WithGeocodeUserAgent(s)`| Identify your app upstream. **Set this** for real deployments.|
| `WithGeocodeEndpoint(u)` | Point at a self-hosted Nominatim / mirror. Fixed at config time — only `q` is user-controlled, so this is not an SSRF surface. |
| `WithGeocoder(fn)`       | Replace the lookup entirely (commercial geocoder, internal place index, fixed dataset). Skips all Nominatim machinery; caching still applies. |

Each of these implies `WithSearch()`.

The public Nominatim instance is a courtesy service on donated hardware — read
its [usage policy](https://operations.osmfoundation.org/policies/nominatim/)
before pointing a production app at it, and prefer a self-hosted instance or a
commercial provider for anything beyond light interactive use.

Responses: `400 E_BAD_REQUEST` (empty `q`), `403 E_CAPABILITY_DENIED`,
`429 E_RATE_LIMITED` (with `Retry-After`) when the 1 req/s queue is saturated,
`502 E_GEOCODE` when the upstream fails. A failed lookup is deliberately distinct
from an empty result set, so the search box can say "search failed" instead of
silently showing nothing.

The example app (`example/main.go`) wires `WithGeocoder` to a small fixed
dataset — the e2e journeys must not depend on a third party being reachable, and
a demo has no business spending someone else's donated geocoding capacity.

## Marker clustering

`WithClustering()` (or `setCluster(true)`) folds nearby pins into counted
bubbles. It is off by default.

Clusters are computed by a `cluster: true` GeoJSON source but **rendered as DOM
markers**, not circle/symbol layers. That is deliberate: individual pins must
stay draggable, editable DOM markers, and DOM bubbles need no style glyphs, theme
from the same `--color-*` tokens, and are assertable from the host page. Cluster
bubbles carry `.fui-cluster`; pins carry `.fui-pin`. (Both are
`maplibregl.Marker`s, so `.maplibregl-marker` alone cannot tell them apart.)

Two constraints worth knowing before changing this code:

- **A source with no layers is never tiled.** MapLibre only loads tiles for
  sources referenced by a layer, so `querySourceFeatures()` on a layer-less
  source returns nothing, forever. The runtime attaches one fully transparent
  circle layer whose only job is to make the renderer materialize the clustered
  tiles it then reads.
- **`isStyleLoaded()` is not a readiness check.** It reports "no pending style
  work" and flickers false for as long as a vector style keeps streaming tiles.
  Gating source creation on it leaves clustering permanently inactive on a busy
  map; the runtime tracks style existence itself.

Clustering is a *rendering* concern: the canonical doc always holds every pin,
and toggling clustering off restores them all. While clustering is on, pins
outside the viewport are legitimately not rendered as DOM markers. Clicking a
bubble zooms to its expansion zoom — where supercluster splits it into children,
which may themselves be clusters.

## The `window.__gofastrGeomap` controller API

The controller for the first mounted map is exposed as
`window.__gofastrGeomap` (plus a `window.__gofastrGeomapAll` array of every
map). The host page / demo drives it directly — there is no postMessage bridge:

| Method             | Signature                                   | Effect                                             |
|--------------------|---------------------------------------------|----------------------------------------------------|
| `flyTo`            | `({lat, lng, zoom?})`                       | Smoothly pan/zoom to the coordinate.              |
| `addMarker`        | `({lat?, lng?, label?, id?}) → id`          | Drop a pin (at the map center if no coord).       |
| `removeMarker`     | `(id) → bool`                               | Remove one pin. `false` if the id is unknown.     |
| `setMarkerLabel`   | `(id, label) → bool`                        | Rename one pin (also updates its open editor).    |
| `clearMarkers`     | `()`                                        | Remove every pin.                                  |
| `setStyle`         | `(name)`                                    | Switch the base style (markers persist).          |
| `setReadOnly`      | `(bool)`                                    | Toggle click-to-add, draggability, and popup editing. |
| `setCluster`       | `(bool)`                                    | Turn clustering on/off at runtime.                |
| `getClusterState`  | `() → {enabled, active, clusters, markers}` | What clustering is currently doing.               |
| `search`           | `(query) → Promise<[{label,lat,lng}]>`      | Query the geocode proxy (empty if search is off). |
| `getDoc`           | `() → {lat,lng,zoom,markers}`               | The current canonical doc.                         |
| `onMarkerSelected` | `((id) => void)`                            | Register a pin-click callback.                     |
| `save`             | `()`                                        | POST the current doc to the save endpoint.        |

The controller also sets `window.__mapReady = true` synchronously on map
construction (before style/tiles load — network-independent readiness signal),
`window.__mapStyleLoaded = true` on the map `'load'` event (network-dependent),
and dispatches a `gofastr:geomap-ready` event on `window` after mounting.

### Markers across a style swap

`map.setStyle()` rebuilds the map's sources/layers but does **NOT** remove
`maplibregl.Marker` overlays — they live in a DOM container `setStyle` never
touches, so they persist across a swap. The controller defensively re-attaches
any marker whose element lost its parent, in case a future MapLibre changes that.

## Save endpoint

```
POST /__gofastr/plugin/map/save
Content-Type: application/json
```

Body: `{docId, doc, lat, lng, zoom, markers, schemaVersion:"map-v1"}` with
`credentials:'same-origin'`. `doc` is the canonical `{lat,lng,zoom,markers}`
blob (decoded as `json.RawMessage` so it survives verbatim); the top-level
`lat`/`lng`/`zoom`/`markers` are convenience fields (preferred when present).
Gated on `document:write` (403 `E_CAPABILITY_DENIED` otherwise). A handler
returning `ErrConflict` → 409 `E_CONFLICT`; any other error → 500 `E_SAVE`.

## Endpoints

- `GET  /__gofastr/plugin/map/map.js`   — runtime bundle (NON-framed; bundles maplibre-gl + injects its CSS).
- `GET  /__gofastr/plugin/map/map.css`  — overlay stylesheet (NON-framed; host links it optionally).
- `POST /__gofastr/plugin/map/save`     — save RPC (gated on `document:write`).
- `GET  /__gofastr/plugin/map/geocode`  — place-search proxy (gated on `geocode:search`). **Registered only when search is enabled** — a plugin that never opts in exposes no egress endpoint at all.

## Bundle size

`map.js` is ~1.13 MB raw / ~298 KB gzip. MapLibre GL is a full WebGL map renderer
(substantially larger than the old Leaflet raster bundle, which could not render
vector tiles). It is served at its own route and is not subject to the core-ui
runtime budget.

## Security posture

**This plugin is trusted.** It runs in the host page's origin with full DOM and
network access — that is the requirement for vector tiles, and it is why it is
NOT sandboxed. Consumers opt into it deliberately:

- The host page CSP (above) is the network guardrail — it pins tile/data access
  to the OpenFreeMap host (or your self-hosted/CDN host via `WithStyleBaseURL`).
- The capability gate still governs the endpoints: `document:write` for save,
  `geocode:search` for the search proxy. `WithDevGrantAll` bypasses it for
  demos/dev only.
- Place search is opt-in and proxied. When it is off, no geocode route exists.
  When it is on, the upstream endpoint is fixed at configuration time and only
  the `q` parameter is user-controlled, so it is not an SSRF surface; the query
  is length-capped and the upstream call is rate-limited and timeout-bounded.
- All interpolated mount values are HTML-escaped (`render.Escape`).

If your threat model cannot tolerate a trusted host-page plugin, do not register
this one — the sandboxed raster-map posture is not available in 0.3.0.
