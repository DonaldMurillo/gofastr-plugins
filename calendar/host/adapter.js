/*!
 * calendar/host/adapter.js — host-side ADAPTER for the GoFastr calendar.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, resize/focus/metric/bootError handling, and SPA teardown.
 * See protocol-v1.md §3/§4/§6.
 *
 * This adapter only:
 *   1. registers the calendar manifest (entry URL, sandbox policy,
 *      capabilities, schema) — document:read / document:write / theme:read,
 *      the fixed set this plugin ships;
 *   2. RELAYS the frame's correlated data events UP to the plugin's RPC
 *      routes and answers them back into the frame — the richtext
 *      requestUpload → uploadResult pattern, applied to calendar windows:
 *        requestOccurrences {reqId, from, to}
 *                       → POST /occurrences
 *                       → occurrencesResult {reqId, occurrences, conflicts, transitions, zone}
 *        requestMove     {reqId, eventId, date, dayDelta, minuteDelta}
 *                       → POST /move
 *                       → moveResult {reqId, occurrence, requestedWallMinutes,
 *                                     actualWallMinutes, elapsedMinutes, note, …}
 *   3. MIRRORS the proof payloads onto the iframe element so the host page
 *      (and e2e) can watch the plugin's claim live — the frame only ever
 *      receives server-resolved occurrences, and every move is answered by
 *      the server, not the frame:
 *        __calendarReady        handshake completed
 *        __calendarOccCount     {count, conflicts, zone, from, to} per window
 *        __calendarLastMove     the server's answer to the latest move intent
 *        __calendarLastError   last relay error code
 *   4. publishes window.__gofastrCalendar = { jump(date) } — the demo page's
 *      DST jump buttons go through here (a host→frame event; the host page
 *      can never reach into the opaque frame itself).
 *
 * Load order: the generic platform broker MUST load before this adapter.
 */

(function () {
  "use strict";

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[calendar] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var APP_HTML_URL = "/__gofastr/plugin/calendar/app.html";
  var OCC_URL = "/__gofastr/plugin/calendar/occurrences";
  var MOVE_URL = "/__gofastr/plugin/calendar/move";
  var SAVE_URL = "/__gofastr/plugin/calendar/save";
  var SCHEMA_VERSION = "calendar-v1";

  var CAPS = ["document:read", "document:write", "theme:read"];

  // The most recent mounted instance's api (jump() needs sendEvent; the
  // broker hands the api to onEvent, we keep the latest for the helper).
  var lastApi = null;

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

  function relay(api, url, body, resultMethod, mirror) {
    var frame = api.iframe;
    var reqId = body.reqId || "";
    fetch(url, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(api, {}),
      body: JSON.stringify(body)
    })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, status: r.status, json: j }; });
      })
      .then(function (res) {
        if (res.ok) {
          var params = Object.assign({}, res.json, { reqId: reqId });
          if (mirror) mirror(frame, res.json);
          api.sendEvent(resultMethod, params);
        } else {
          var code = (res.json && res.json.error) || ("HTTP " + res.status);
          if (frame) frame.__calendarLastError = code;
          api.sendEvent(resultMethod, { reqId: reqId, error: code });
        }
      })
      ["catch"](function (e) {
        var msg = e && e.message ? e.message : String(e);
        if (frame) frame.__calendarLastError = msg;
        api.sendEvent(resultMethod, { reqId: reqId, error: "E_NETWORK" });
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

  // The hidden-input name is per-mount (published on the marker by the Go
  // Mount helper) — a hard-coded name here would silently drop the view
  // state of any mount that named its own field.
  function docFieldFromMarker(marker) {
    var name = marker && marker.getAttribute ? marker.getAttribute("data-fui-plugin-field") : "";
    name = (name || "").trim();
    return name || "calendar_doc";
  }

  function mirrorDoc(api, doc) {
    var form = api.form;
    if (!form || doc == null) return;
    var el = form.elements ? form.elements[docFieldFromMarker(api.marker)] : null;
    if (el && el.nodeType) el.value = JSON.stringify(doc);
  }

  host.register("calendar", {
    manifest: {
      entry: APP_HTML_URL,
      isolation: "sandbox-iframe-opaque",
      sandbox: ["allow-scripts"],
      capabilities: CAPS,
      schema: SCHEMA_VERSION,
      title: "Calendar"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          lastApi = api;
          frame.__calendarReady = true;
          frame.__calendarProbes = params.probes || null;
          frame.__calendarOccCount = null;
          frame.__calendarLastMove = null;
          break;
        case "occCount":
          // The frame publishes what one window delivered: occurrences the
          // SERVER resolved, plus the conflict count it computed.
          frame.__calendarOccCount = {
            count: typeof params.count === "number" ? params.count : 0,
            conflicts: typeof params.conflicts === "number" ? params.conflicts : 0,
            zone: params.zone || "",
            from: params.from || "",
            to: params.to || ""
          };
          break;
        case "moveResolved":
          // The proof payload: requested vs wall vs elapsed, straight from
          // the frame's own postMessage. The demo readout polls this.
          frame.__calendarLastMove = {
            title: params.title || "",
            from: params.from || "",
            to: params.to || "",
            requestedWallMinutes:
              typeof params.requestedWallMinutes === "number" ? params.requestedWallMinutes : null,
            actualWallMinutes:
              typeof params.actualWallMinutes === "number" ? params.actualWallMinutes : null,
            elapsedMinutes:
              typeof params.elapsedMinutes === "number" ? params.elapsedMinutes : null,
            zone: params.zone || "",
            zoneAbbr: params.zoneAbbr || "",
            offsetMinutes:
              typeof params.offsetMinutes === "number" ? params.offsetMinutes : null,
            note: params.note || ""
          };
          break;
        case "themeApplied":
          frame.__calendarTheme = params; // {scheme, sample:{--name:value}}
          break;
        case "docChanged":
          mirrorDoc(api, params.doc);
          break;
        case "save":
          doSave(api, params);
          break;
        case "requestOccurrences":
          relay(api, OCC_URL, {
            docId: docIdFromMarker(api.marker),
            from: params.from,
            to: params.to,
            reqId: params.reqId
          }, "occurrencesResult", null);
          break;
        case "requestMove":
          relay(api, MOVE_URL, {
            docId: docIdFromMarker(api.marker),
            eventId: params.eventId,
            date: params.date,
            dayDelta: params.dayDelta,
            minuteDelta: params.minuteDelta,
            reqId: params.reqId
          }, "moveResult", function (f, json) {
            f.__calendarLastMoveResult = json; // full server answer, for tests
          });
          break;
        default:
          // resize / focusChanged / bootError handled generically.
          break;
      }
    }
  });

  // Demo helper: jump the (latest) mounted calendar to a date. A host→frame
  // event through the broker's api.sendEvent — the only sanctioned channel.
  window.__gofastrCalendar = {
    jump: function (date) {
      if (lastApi && typeof date === "string" && /^\d{4}-\d{2}-\d{2}$/.test(date)) {
        lastApi.sendEvent("jumpToDate", { date: date });
        return true;
      }
      return false;
    }
  };
})();
