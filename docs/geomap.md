# Geomap (Leaflet) plugin

An interactive [Leaflet](https://leafletjs.com/) map plugin — pan, zoom,
click-to-drop pins, draggable markers with editable labels — mounted the same
way as the richtext, mermaid, and monaco plugins: inside an **opaque-origin
sandboxed iframe**, talking to the host only over the versioned postMessage
bridge. It is the fourth sandboxed heavy-JS plugin.

The Go package is `geomap` (the identifier `map` is a Go keyword), but the
user-facing identity strings are `"map"`:

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/geomap`
- **Name:** `map`
- **Route prefix:** `/__gofastr/plugin/map`
- **Demo URL:** `/map`
- **Isolation:** `sandbox-iframe-opaque` (`allow-scripts`, no `allow-same-origin`)
- **Canonical doc (schema `map-v1`):** `{lat, lng, zoom, markers}` where each
  marker is `{id, lat, lng, label?}`
- **Capabilities:** `document:read`, `document:write`, `theme:read`

## Mounting

```go
app.RegisterPlugin(geomap.New(
    geomap.WithDevGrantAll(),    // demo/dev only — opens the capability gate
    geomap.WithDemoPage(),       // serves the themed showcase at /map
    geomap.WithCenter(40.7128, -74.006),
    geomap.WithZoom(11),
    geomap.WithProvider("carto-light"),
))
```

Persist with `geomap.WithSaveHandler(func(ctx, req) error { ... })`. Return
`geomap.ErrConflict` to signal an optimistic-concurrency conflict — it maps to
HTTP 409 (`E_CONFLICT`), which the host adapter relays back to the frame as a
`saveResult` so the map warns rather than silently dropping the edit (the same
contract as monaco and richtext).

## Why a same-origin tile proxy (the opaque-origin CSP)

The framed page runs under gofastr core's `framedCSP`
(`framework/pluginhost/assets.go` → `framedCSP`):

```
sandbox allow-scripts;
default-src <origin>; script-src <origin>;
style-src <origin> 'unsafe-inline'; img-src <origin> data:;
font-src <origin> data:; connect-src 'none';
frame-ancestors <origin>; base-uri <origin>
```

Two consequences shape this plugin:

1. **`connect-src 'none'`** → no `fetch`/`XHR`/`WebSocket` from the frame. Every
   host interaction is `postMessage` via the broker. Leaflet raster tiles are
   `<img>` (not fetch), so raster tiles work; **vector tiles and geocoding do
   not** (they need fetch).
2. **`img-src <origin> data:`** → the frame may ONLY load images from its own
   origin (the host origin) or `data:` URIs. **External tile hosts
   (`tile.openstreetmap.org`, Carto, Mapbox, …) are blocked.**

The plugin therefore ships a **same-origin tile proxy** that fetches upstream
server-side and serves the bytes back to the frame as a same-origin `<img>`
source.

### The proxy endpoint

```
GET /__gofastr/plugin/map/tiles/{provider}/{z}/{x}/{y}
```

`{provider}` is looked up in a fixed **allowlist** of provider→URL-template:

| key          | upstream                                                         |
|--------------|------------------------------------------------------------------|
| `osm`        | `https://tile.openstreetmap.org/{z}/{x}/{y}.png`                 |
| `carto-light`| `https://basemaps.cartocdn.com/light_all/{z}/{x}/{y}.png`        |
| `carto-dark` | `https://basemaps.cartocdn.com/dark_all/{z}/{x}/{y}.png`         |

Unknown provider → **404** (no upstream request). Non-integer or out-of-range
`z`/`x`/`y` → **400** (no upstream request). `z` ∈ `[0, 22]`; `x`, `y` ∈
`[0, 2^z - 1]`. **Only the validated integers are interpolated into the
template** — never a raw client string. This is the SSRF guard: the upstream
host is never client-controlled, only chosen from the allowlist.

The upstream fetch uses an `http.Client` with a 10s timeout, sends
`User-Agent: gofastr-plugins-geomap/0.1` (OSM usage policy requires a
identifying UA), follows up to 3 redirects, caps responses at 8 MiB, and
streams the body back with `Content-Type: image/png` (or the upstream's, if it
advertises an `image/*` type) and `Cache-Control: public, max-age=86400`.

A small bounded in-memory LRU cache (256 entries by default) keyed by
`provider/z/x/y` serves repeat tiles without a round-trip, respecting
tile-server policy under demo/e2e pan-and-zoom load.

### Extending the allowlist

`geomap.WithTileProviders(map[string]string{...})` overrides or extends the
built-in map. Each value MUST contain literal `{z}`, `{x}`, `{y}` placeholders;
an invalid template panics at `New()` (fail-loud at construction rather than
500ing at first tile request).

```go
geomap.New(
    geomap.WithProvider("my-host"),
    geomap.WithTileProviders(map[string]string{
        "my-host": "https://tiles.internal.example.com/{z}/{x}/{y}.png",
    }),
)
```

## The persisted document

The canonical doc (schema `map-v1`) is plain JSON:

```json
{
  "lat": 40.7128,
  "lng": -74.006,
  "zoom": 11,
  "markers": [
    { "id": "m-abc-1", "lat": 40.7128, "lng": -74.006, "label": "New York" }
  ]
}
```

**The json struct tags on `mapDoc` / `mapMarker` are load-bearing**: the frame's
`deriveDoc` reads lowercase `{lat, lng, zoom, markers}` from the mount marker's
`data-fui-plugin-doc`. Without the tags Go would emit `{Lat, Lng, Zoom, Markers}`
and the frame would silently mount an empty map on reload — the exact regression
that bit the monaco `savedDoc`. `TestSaveRoundTripDevGrant` asserts the wire
shape.

## Configuration

Map behaviour is configurable per-instance via `With…` options. Config is
marshalled at `Init` and delivered to the frame in `init.config` through the
host-page `config.js` script + the adapter's manifest config.

| Option           | Field       | Default                          |
|------------------|-------------|----------------------------------|
| `WithCenter`     | `center`    | `{lat: 20, lng: 0}`              |
| `WithZoom`       | `zoom`      | `2`                              |
| `WithMinZoom`    | `minZoom`   | `0`                              |
| `WithMaxZoom`    | `maxZoom`   | `19`                             |
| `WithProvider`   | `provider`  | `"osm"`                          |
| `WithReadOnly`   | `readOnly`  | `false`                          |
| `WithMarkers`    | `markers`   | `[]`                             |
| `WithTheme`      | `theme`     | `"auto"`                         |

`theme: "auto"` follows the bridged host scheme: dark scheme → `carto-dark`,
light scheme → the configured provider (or `carto-light` if the configured
provider was `carto-dark`). Explicit `"light"` / `"dark"` pin the scheme and
leave the provider alone.

## The postMessage bridge

The frame speaks protocol v1 to the host. The demo page (and any host page)
calls these **inbound** methods on the frame's `contentWindow` as
`{v:1, type:"event", src:"host", method, params}` envelopes, exactly like
monaco's `reconfigure`:

| Method            | Params                              | Effect                                          |
|-------------------|-------------------------------------|-------------------------------------------------|
| `flyTo`           | `{lat, lng, zoom}`                  | Smoothly pan/zoom the map to the coordinate.    |
| `highlightMarker` | `{id}`                              | Highlight the pin with `id`, pan to it, open its popup. |
| `setProvider`     | `{provider}`                        | Switch the active base layer (must be allowlisted). |
| `setReadOnly`     | `{readOnly}`                        | Toggle marker editing / map drag in place.      |
| `addMarker`       | `{lat?, lng?, label?}`              | Drop a pin (at the map center if no coord).     |
| `clearMarkers`    | `{}`                                | Remove every pin.                               |

The frame emits these **outbound** events to the host:

| Method           | Params                                 | When                                  |
|------------------|----------------------------------------|---------------------------------------|
| `ready`          | `{version, schemaVersion, probes}`     | Frame booted.                         |
| `docChanged`     | `{lat, lng, zoom, markers}`            | Map moved, zoomed, or a marker changed (debounced). |
| `save`           | `{lat, lng, zoom, markers, schemaVersion}` | Autosave (debounced).            |
| `markerSelected` | `{id}`                                 | User clicked a pin.                   |
| `resize`         | `{height}`                             | Frame resized.                        |
| `themeApplied`   | `{scheme, sample}`                     | Tokens applied.                       |
| `bootError`      | `{error}`                              | Boot-time throw surfaced to the host. |

The host adapter mirrors `markerSelected` onto `iframe.__mapMarkerSelected`
(e2e observability) AND dispatches a `map:markerSelected` `CustomEvent` on the
iframe element, so a host page's side panel can highlight the matching card
without listening on `window.message` itself.

## Marker icons under the opaque origin

Leaflet's default icon PNGs 404 under a bundler (the path resolves to a
bundled-asset URL) and would be blocked at runtime under `img-src <origin>`
anyway. The frame therefore renders markers as an **inline-SVG `L.divIcon`** —
no image asset to resolve, no fetch. The SVG is themed via the bridged
`--color-primary` token; the highlight class swaps the fill to
`--color-danger`. `data:` URIs are also allowed by the CSP, but the divIcon
sidesteps even that.

## Bundle size

`map.js` is ~180 KB raw / ~50 KB gzip (Leaflet is small). It is served at its
own route and is not subject to the core-ui runtime budget.

## Endpoints

- `GET  /__gofastr/plugin/map/map.html` — frame document (framed CSP).
- `GET  /__gofastr/plugin/map/map.js`   — frame bundle (framed CSP).
- `GET  /__gofastr/plugin/map/map.css`  — frame stylesheet (framed CSP).
- `GET  /__gofastr/plugin/map/adapter.js` — host adapter (non-framed).
- `GET  /__gofastr/plugin/map/config.js`  — host-page config script (non-framed).
- `POST /__gofastr/plugin/map/save`       — save RPC (capability-gated).
- `GET  /__gofastr/plugin/map/tiles/{provider}/{z}/{x}/{y}` — same-origin tile proxy.

## Accessibility

The map container carries `role="region"` + `aria-label`. Pins carry `title`
and `alt` text (the label, or "Map pin"). Popups are focusable and the
edit/delete buttons have programmatic labels. The frame's status line on a
failed save (especially a 409 conflict) is `role="status"` + `aria-live="polite"`.

## Capabilities

- `document:read` — required to receive the saved doc on `init`.
- `document:write` — required for click-to-add, marker dragging, label editing,
  deletion, and the `addMarker` / `clearMarkers` / `setReadOnly` RPCs.
- `theme:read` — required to receive bridged tokens.

A frame missing `document:write` boots in view-only mode (map pan/zoom work,
marker editing is disabled). The save RPC is gated on `document:write`:
unauthorized callers get 403 `E_CAPABILITY_DENIED`.
