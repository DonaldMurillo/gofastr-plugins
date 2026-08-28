/*!
 * datagrid/host/adapter.js — host-side ADAPTER for the GoFastr data grid.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, resize/focus/metric/bootError handling, and SPA teardown.
 * See protocol-v1.md §3/§4/§6.
 *
 * This adapter only:
 *   1. registers the datagrid manifest (entry URL, sandbox policy,
 *      capabilities, schema), merging the optional data:write / data:export
 *      grants the Go side publishes via config.js
 *      (window.__gofastrDatagridConfig) — the same bilateral channel pdf
 *      uses for pdf:export;
 *   2. RELAYS the frame's correlated data events UP to the plugin's RPC
 *      routes and answers them back into the frame — the richtext
 *      requestUpload → uploadResult pattern, applied to pages of rows:
 *        requestRows      → POST /rows   → rowsResult      {reqId, rows, lastRow}
 *        requestCellWrite → POST /cell   → cellWriteResult {reqId, ok}
 *        requestExport    → POST /export → exportResult    {reqId, url}
 *   3. MIRRORS bridge traffic onto the iframe element so a parent-side test
 *      can prove the volume claim (the frame only ever receives pages):
 *        __datagridRowsDelivered  running total of rows crossed into the frame
 *        __datagridMaxRowsDelivered largest single rowsResult this session
 *        __datagridRowsRequests   count of requestRows round trips
 *        __datagridLastRowsRequest {startRow, endRow, sort, filter} of the last fetch
 *        __datagridLastExportUrl / __datagridLastError / __datagridReady /
 *        __datagridCacheBlocks   latest frame cacheState {count, cap} — what
 *                                AG Grid currently retains, against the cap
 *
 * CSV export note: the DOWNLOAD is started here, in the host page — a
 * sandboxed frame has no allow-downloads/allow-popups and cannot start one.
 * The plugin generated the bytes host-side; we click a transient <a download>
 * on the URL the export handler returned.
 *
 * Load order: the generic platform broker MUST load before this adapter, and
 * the instance config.js (window.__gofastrDatagridConfig) MUST load before it
 * too. Both the demo page and datagrid.UIHostOption emit pluginhost.js, then
 * config.js, then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[datagrid] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var GRID_HTML_URL = "/__gofastr/plugin/datagrid/grid.html";
  var ROWS_URL      = "/__gofastr/plugin/datagrid/rows";
  var CELL_URL      = "/__gofastr/plugin/datagrid/cell";
  var EXPORT_URL    = "/__gofastr/plugin/datagrid/export";
  var SAVE_URL      = "/__gofastr/plugin/datagrid/save";
  var SCHEMA_VERSION = "datagrid-v1";

  // data:read + theme:read are always on; data:write / data:export are
  // appended only when the Go side wired the matching handler (config.js
  // publishes that fact). The frame learns the same set via init.capabilities,
  // and the routes re-check the gate regardless of what the frame believes.
  var DEFAULT_CAPS = ["data:read", "theme:read"];

  function capabilities() {
    var caps = DEFAULT_CAPS.slice();
    var cfg = window.__gofastrDatagridConfig;
    if (cfg && cfg.writeEnabled) caps.push("data:write");
    if (cfg && cfg.exportEnabled) caps.push("data:export");
    return caps;
  }

  // --- Plugin-specific helpers ---------------------------------------------
  // Each takes the per-iframe `api` object the generic broker hands to onEvent
  // ({iframe, marker, form, csrfToken, sendEvent, request}).

  function docIdFromMarker(marker) {
    var id = marker && marker.getAttribute ? marker.getAttribute("data-fui-plugin-docid") : "";
    id = (id || "").trim();
    return id || "demo";
  }

  function jsonHeaders(api, extra) {
    var headers = { "Content-Type": "application/json" };
    var tok = api.csrfToken();
    if (tok) headers["X-CSRF-Token"] = tok;
    for (var k in extra) {
      if (Object.prototype.hasOwnProperty.call(extra, k)) headers[k] = extra[k];
    }
    return headers;
  }

  // Relay a frame-originated requestRows up to POST /rows and answer with a
  // rowsResult event. This is the round trip the whole plugin exists to
  // exercise: sort/filter/paging run in Go, and only the requested window
  // crosses the bridge.
  function relayRows(api, params) {
    params = params || {};
    var reqId = params.reqId || "";
    var frame = api.iframe;
    if (frame) {
      frame.__datagridRowsRequests = (frame.__datagridRowsRequests || 0) + 1;
      frame.__datagridLastRowsRequest = {
        startRow: params.startRow,
        endRow: params.endRow,
        sort: params.sort || [],
        filter: params.filter || ""
      };
    }
    fetch(ROWS_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(api, {}),
      body: JSON.stringify({
        docId: docIdFromMarker(api.marker),
        startRow: params.startRow,
        endRow: params.endRow,
        sort: params.sort || [],
        filter: params.filter || "",
        columns: params.columns || []
      })
    })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, status: r.status, json: j }; });
      })
      .then(function (res) {
        if (res.ok && res.json && Array.isArray(res.json.rows)) {
          if (frame) {
            // The volume metric: rows that actually crossed into the frame
            // this session. The e2e asserts this stays far below the table
            // size — the test that makes the plugin worth having.
            // Per-RESPONSE volume too, not just the session total: the
            // bridge limit is per round trip (500 rows), so the max single
            // delivery is the mirror a test can pin directly.
            frame.__datagridRowsDelivered = (frame.__datagridRowsDelivered || 0) + res.json.rows.length;
            frame.__datagridMaxRowsDelivered = Math.max(frame.__datagridMaxRowsDelivered || 0, res.json.rows.length);
          }
          api.sendEvent("rowsResult", {
            reqId: reqId,
            rows: res.json.rows,
            lastRow: typeof res.json.lastRow === "number" ? res.json.lastRow : -1
          });
        } else {
          var code = (res.json && res.json.error) || ("HTTP " + res.status);
          if (frame) frame.__datagridLastError = code;
          api.sendEvent("rowsResult", { reqId: reqId, error: code });
        }
      })
      ["catch"](function (e) {
        var msg = e && e.message ? e.message : String(e);
        if (frame) frame.__datagridLastError = msg;
        api.sendEvent("rowsResult", { reqId: reqId, error: "E_NETWORK" });
      });
  }

  function relayCellWrite(api, params) {
    params = params || {};
    var reqId = params.reqId || "";
    fetch(CELL_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(api, {}),
      body: JSON.stringify({
        docId: docIdFromMarker(api.marker),
        rowId: params.rowId,
        field: params.field,
        value: params.value
      })
    })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, status: r.status, json: j }; });
      })
      .then(function (res) {
        if (res.ok) {
          api.sendEvent("cellWriteResult", { reqId: reqId, ok: true });
        } else {
          var code = (res.json && res.json.error) || ("HTTP " + res.status);
          api.iframe.__datagridLastError = code;
          api.sendEvent("cellWriteResult", { reqId: reqId, error: code });
        }
      })
      ["catch"](function () {
        api.sendEvent("cellWriteResult", { reqId: reqId, error: "E_NETWORK" });
      });
  }

  // Relay a requestExport up to POST /export. The Go side scans the rows
  // source under the request's sort/filter, generates the CSV host-side and
  // stores it via the host's export handler; we answer exportResult with the
  // URL and start the download HERE (the frame cannot).
  function relayExport(api, params) {
    params = params || {};
    var reqId = params.reqId || "";
    fetch(EXPORT_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(api, {}),
      body: JSON.stringify({
        docId: docIdFromMarker(api.marker),
        format: params.format || "csv",
        columns: params.columns || [],
        sort: params.sort || [],
        filter: params.filter || ""
      })
    })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, status: r.status, json: j }; });
      })
      .then(function (res) {
        if (res.ok && res.json && res.json.url) {
          api.iframe.__datagridLastExportUrl = res.json.url;
          api.sendEvent("exportResult", {
            reqId: reqId,
            url: res.json.url,
            rowCount: res.json.rowCount || 0
          });
          // The host page owns the download: a sandboxed frame has no
          // allow-downloads / allow-popups, so clicking a transient anchor
          // here is the only way the produced CSV reaches the user's disk.
          var a = document.createElement("a");
          a.href = res.json.url;
          a.download = "";
          document.body.appendChild(a);
          a.click();
          a.remove();
        } else {
          var code = (res.json && res.json.error) || ("HTTP " + res.status);
          api.iframe.__datagridLastError = code;
          api.sendEvent("exportResult", { reqId: reqId, error: code });
        }
      })
      ["catch"](function () {
        api.sendEvent("exportResult", { reqId: reqId, error: "E_NETWORK" });
      });
  }

  function doSave(api, params) {
    params = params || {};
    var body = {
      docId: docIdFromMarker(api.marker),
      doc: params.doc,
      schemaVersion: SCHEMA_VERSION
    };
    fetch(SAVE_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(api, {}),
      body: JSON.stringify(body)
    }).then(function (resp) {
      if (resp.ok) {
        api.sendEvent("saveResult", { ok: true, status: resp.status });
        return;
      }
      return resp.json().then(
        function (data) { return (data && data.error) || "E_SAVE"; },
        function () { return "E_SAVE"; }
      ).then(function (code) {
        api.sendEvent("saveResult", { ok: false, status: resp.status, code: code });
      });
    })["catch"](function () {
      api.sendEvent("saveResult", { ok: false, status: 0, code: "E_NETWORK" });
    });
  }

  // The hidden-input name is per-mount (MountConfig.Field, published on the
  // marker as data-fui-plugin-field by the Go Mount helper) — a hard-coded
  // name here would silently drop the view state of any mount that named
  // its own field.
  function docFieldFromMarker(marker) {
    var name = marker && marker.getAttribute ? marker.getAttribute("data-fui-plugin-field") : "";
    name = (name || "").trim();
    return name || "datagrid_doc";
  }

  function mirrorDoc(api, doc) {
    var form = api.form;
    if (!form || doc == null) return;
    // form.elements[name] avoids building an attribute selector.
    var el = form.elements ? form.elements[docFieldFromMarker(api.marker)] : null;
    if (el && el.nodeType) el.value = JSON.stringify(doc);
  }

  // --- Register with the generic platform broker ---------------------------

  host.register("datagrid", {
    manifest: {
      entry:        GRID_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: capabilities(),
      schema:       SCHEMA_VERSION,
      title:        "Data grid"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          frame.__datagridReady = true;
          frame.__datagridProbes = params.probes || null;
          frame.__datagridRowsDelivered = 0;
          frame.__datagridRowsRequests = 0;
          frame.__datagridMaxRowsDelivered = 0;
          break;
        case "cacheState":
          // The frame publishes AG Grid's own cache-block state after each
          // page settles; a host page cannot read into the opaque frame, so
          // THIS mirror is its only live view of the retention claim.
          frame.__datagridCacheBlocks = {
            count: typeof params.count === "number" ? params.count : 0,
            cap: typeof params.cap === "number" ? params.cap : 0
          };
          break;
        case "themeApplied":
          frame.__datagridTheme = params; // {scheme, sample:{--name:value}}
          break;
        case "docChanged":
          mirrorDoc(api, params.doc);
          break;
        case "save":
          doSave(api, params);
          break;
        case "requestRows":
          relayRows(api, params);
          break;
        case "requestCellWrite":
          relayCellWrite(api, params);
          break;
        case "requestExport":
          relayExport(api, params);
          break;
        default:
          // resize / focusChanged / bootError handled generically.
          break;
      }
    }
  });
})();
