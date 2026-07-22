/*!
 * geomap/host/adapter.js — host-side ADAPTER for the GoFastr Leaflet-map plugin.
 *
 * This is a THIN adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the protocol-level machinery:
 * the sandboxed-iframe creation, the versioned postMessage envelope + source
 * check, the ready→init handshake, theme bridging, resize/focus/metric/bootError
 * handling, and SPA teardown. See protocol-v1.md §3/§4/§6 — the protocol is
 * frozen and lives entirely in the generic broker now.
 *
 * This adapter only:
 *   1. registers the map manifest (entry URL, sandbox policy, capabilities,
 *      schema) so the generic broker can build the iframe;
 *   2. handles the map-SPECIFIC events the generic broker forwards:
 *        - docChanged     → mirror the canonical {lat,lng,zoom,markers} JSON
 *          into the host form's hidden input (so a normal form POST round-trips
 *          the canonical blob);
 *        - save           → POST {doc, lat, lng, zoom, markers} to the plugin
 *          save RPC and RELAY the save outcome back to the frame as
 *          `saveResult` (mirrors monaco/host/adapter.js doSave: fetch does NOT
 *          reject on 4xx/5xx, so a 409 conflict / 403 / 500 would otherwise be
 *          dropped on the floor — a silent lost save);
 *        - markerSelected → mirror the pin id onto iframe.__mapMarkerSelected
 *          (e2e observability) AND dispatch a 'map:markerSelected' CustomEvent
 *          on the iframe element so the host demo's side panel can highlight the
 *          matching card without listening on window.message itself;
 *   3. MIRRORS the generic hooks onto the plugin-specific names the e2e reads
 *      (iframe.__mapReady / __mapProbes / __mapTheme / __mapLastMetric /
 *      __mapMarkerSelected):
 *        - ready        → __mapReady + __mapProbes
 *        - themeApplied → __mapTheme
 *        - metric       → __mapLastMetric
 *
 * There is NO upload path for the map (capabilities: document:read/write +
 * theme:read only). Tiles are served by the plugin's same-origin proxy and
 * never touch the broker.
 *
 * Config: the Go plugin serves a small host-page script (config.js) BEFORE this
 * adapter that publishes window.__gofastrMapConfig — the MapConfig the host
 * compiled in via With* options. The adapter merges it into the manifest
 * `config` it registers, so Go options reach the frame through init.config.
 *
 * Load order: the generic platform broker, then config.js, then this adapter.
 * All three are emitted by geomap.UIHostOption in that order; the demo page
 * includes the three <script>s itself.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[map] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var MAP_HTML_URL    = "/__gofastr/plugin/map/map.html";
  var SAVE_URL        = "/__gofastr/plugin/map/save";
  var SCHEMA_VERSION  = "map-v1";

  // The map advertises document:read/write + theme:read (NO upload:images).
  var DEFAULT_CAPS = ["document:read", "document:write", "theme:read"];

  // Frame-side map defaults. window.__gofastrMapConfig (published by the
  // Go-served config.js host script) overrides/augments these so With* Go
  // options reach the frame via init.config.
  var STATIC_CONFIG = {
    center: { lat: 20, lng: 0 },
    zoom: 2,
    minZoom: 0,
    maxZoom: 19,
    provider: "osm",
    readOnly: false,
    markers: [],
    theme: "auto"
  };
  function resolvedConfig() {
    var cfg = {};
    for (var k in STATIC_CONFIG) cfg[k] = STATIC_CONFIG[k];
    var overrides = window.__gofastrMapConfig;
    if (overrides && typeof overrides === "object") {
      for (var k2 in overrides) {
        if (Object.prototype.hasOwnProperty.call(overrides, k2)) {
          cfg[k2] = overrides[k2];
        }
      }
    }
    return cfg;
  }

  // --- Plugin-specific helpers ---------------------------------------------
  // Each takes the per-iframe `api` object the generic broker hands to onEvent
  // ({iframe, marker, form, csrfToken, sendEvent, request}).

  function parseFor(marker) {
    return (marker.getAttribute("data-fui-plugin-for") || "").trim();
  }

  function cssEscape(s) {
    if (window.CSS && typeof window.CSS.escape === "function") return window.CSS.escape(s);
    return String(s).replace(/["\\\]]/g, "\\$&");
  }

  function findHiddenInput(form, name) {
    if (!form || !name) return null;
    var el = form.elements ? form.elements[name] : null;
    if (el) {
      if (el.nodeType) return el;          // single element
      if (el.length) return el[0];         // RadioNodeList — take first
    }
    return form.querySelector('input[name="' + cssEscape(name) + '"]');
  }

  // The hidden field holds the CANONICAL doc JSON {lat,lng,zoom,markers} so a
  // normal form POST round-trips it. The host page never needs to decode it —
  // only forward it to /save on submit.
  function writeHiddenField(api, docJSON) {
    var form = api.form;
    if (!form) return;
    var name = parseFor(api.marker);
    if (!name) return;
    var input = findHiddenInput(form, name);
    if (input) input.value = docJSON != null ? String(docJSON) : "";
  }

  function doSave(api, params) {
    var docId = api.marker.getAttribute("data-fui-plugin-docid") || "demo";
    // params is the canonical {lat,lng,zoom,markers} emitted by the frame.
    // We send it both as the top-level fields AND as the `doc` blob so the
    // handler's either-form normalization accepts it.
    var lat = params.lat != null ? Number(params.lat) : 0;
    var lng = params.lng != null ? Number(params.lng) : 0;
    var zoom = params.zoom != null ? Number(params.zoom) : 0;
    var markers = Array.isArray(params.markers) ? params.markers : [];
    var body = {
      docId: docId,
      doc: { lat: lat, lng: lng, zoom: zoom, markers: markers },
      lat: lat,
      lng: lng,
      zoom: zoom,
      markers: markers,
      schemaVersion: SCHEMA_VERSION
    };
    var headers = { "Content-Type": "application/json" };
    var tok = api.csrfToken();
    if (tok) headers["X-CSRF-Token"] = tok;
    fetch(SAVE_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: headers,
      body: JSON.stringify(body)
    }).then(function (resp) {
      if (resp.ok) {
        api.sendEvent("saveResult", { ok: true, status: resp.status });
        return;
      }
      // fetch does NOT reject on 4xx/5xx, so a 409 conflict (or 403/500) used to
      // resolve here and be dropped on the floor — a silent lost save. Read the
      // {error} code the handler returns and hand the frame a failed saveResult
      // so it can keep the doc dirty and tell the user (esp. status 409).
      return resp.json().then(
        function (data) { return (data && data.error) || "E_SAVE"; },
        function () { return "E_SAVE"; }
      ).then(function (code) {
        api.sendEvent("saveResult", { ok: false, status: resp.status, code: code });
      });
    })["catch"](function () {
      // Network-level failure (offline, aborted, blocked): also a save that did
      // not land — surface it as status 0 rather than swallowing it.
      api.sendEvent("saveResult", { ok: false, status: 0, code: "E_NETWORK" });
    });
  }

  // --- Register with the generic platform broker ---------------------------

  host.register("map", {
    manifest: {
      entry:        MAP_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      minHeight:    "360px",
      schema:       SCHEMA_VERSION,
      title:        "Leaflet map"
    },
    config: resolvedConfig(),
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          // Mirror the handshake signal + isolation probes the e2e reads.
          frame.__mapReady = true;
          frame.__mapProbes = params.probes || null;
          break;
        case "themeApplied":
          frame.__mapTheme = params; // {scheme, sample:{--name:value}}
          break;
        case "metric":
          frame.__mapLastMetric = params; // {name, p50, p99, count, …}
          break;
        case "docChanged":
          // Persist the canonical doc JSON into the hidden field as a single
          // string. The frame is the source of truth; we trust its shape.
          writeHiddenField(api, JSON.stringify({
            lat: params.lat,
            lng: params.lng,
            zoom: params.zoom,
            markers: params.markers
          }));
          break;
        case "save":
          doSave(api, params);
          break;
        case "markerSelected":
          // Mirror for e2e + re-dispatch as a CustomEvent on the iframe so the
          // host demo's side panel can highlight the matching card without
          // tapping window.message itself (the broker owns that channel).
          frame.__mapMarkerSelected = params.id || "";
          try {
            frame.dispatchEvent(new CustomEvent("map:markerSelected", { detail: { id: params.id || "" } }));
          } catch (_) {
            // CustomEvent is universally available; swallow defensively.
          }
          break;
        default:
          // resize / focusChanged / bootError / themeChanged / saveResult are
          // handled by the generic broker (saveResult is host→plugin, relayed
          // BY this adapter's doSave above to the frame); nothing map-specific
          // to do here on the host side.
          break;
      }
    }
  });
})();
