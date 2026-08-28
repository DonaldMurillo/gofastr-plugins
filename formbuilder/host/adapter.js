/*!
 * formbuilder/host/adapter.js — host-side ADAPTER for the GoFastr form builder.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, resize/focus/metric/bootError handling, and SPA teardown.
 * See protocol-v1.md §3/§4/§6.
 *
 * This adapter only:
 *   1. registers the formbuilder manifest (entry URL, sandbox policy,
 *      capabilities, schema);
 *   2. relays the frame's `save` event to the plugin's Go route, which
 *      VALIDATES the schema server-side and either persists it or refuses
 *      with a specific error code — the refusal crosses back into the frame
 *      as `saveResult` and renders in the builder's status line;
 *   3. mirrors the doc into the hidden form field on docChanged (the normal
 *      form-POST round trip every plugin here uses), and
 *   4. mirrors the save verdicts onto the IFRAME ELEMENT so the host page
 *      (and the e2e suite / the demo's live proof strip) can read them:
 *        __formbuilderReady       handshake completed
 *        __formbuilderProbes      frame self-isolation probes (§8a)
 *        __formbuilderTheme       last themeApplied sample
 *        __formbuilderSave        last saveResult {ok, code, fields, rules}
 *        __formbuilderSaves       count of accepted saves this session
 *
 * Load order: the generic platform broker MUST load before this adapter
 * (both the demo page and formbuilder.UIHostOption emit pluginhost.js first,
 * then this script).
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[formbuilder] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var BUILDER_HTML_URL = "/__gofastr/plugin/formbuilder/builder.html";
  var SAVE_URL         = "/__gofastr/plugin/formbuilder/save";
  var SCHEMA_VERSION   = "formbuilder-v1";

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

  // The save round trip the plugin exists to exercise: the frame edits DATA,
  // this adapter POSTs it, Go validates it, and the verdict (including a
  // refusal code) crosses back into the frame. The frame cannot persist
  // anything itself — the framed CSP has no connect-src at all.
  function doSave(api, params) {
    params = params || {};
    var frame = api.iframe;
    fetch(SAVE_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(api, {}),
      body: JSON.stringify({
        docId: docIdFromMarker(api.marker),
        doc: params.doc,
        schemaVersion: SCHEMA_VERSION
      })
    }).then(function (resp) {
      return resp.json().then(
        function (j) { return { ok: resp.ok, status: resp.status, json: j }; },
        function () { return { ok: resp.ok, status: resp.status, json: null }; }
      );
    }).then(function (res) {
      var j = res.json || {};
      if (res.ok) {
        if (frame) {
          frame.__formbuilderSaves = (frame.__formbuilderSaves || 0) + 1;
          frame.__formbuilderSave = {
            ok: true,
            fields: typeof j.fields === "number" ? j.fields : 0,
            rules: typeof j.rules === "number" ? j.rules : 0
          };
        }
        api.sendEvent("saveResult", { ok: true, fields: j.fields, rules: j.rules });
        return;
      }
      var code = j.error || ("HTTP " + res.status);
      if (frame) frame.__formbuilderSave = { ok: false, code: code };
      api.sendEvent("saveResult", { ok: false, code: code });
    })["catch"](function () {
      if (frame) frame.__formbuilderSave = { ok: false, code: "E_NETWORK" };
      api.sendEvent("saveResult", { ok: false, code: "E_NETWORK" });
    });
  }

  // The hidden-input name is per-mount (MountConfig.Field, published on the
  // marker as data-fui-plugin-field by the Go Mount helper) — a hard-coded
  // name here would silently drop the schema of any mount that named its own.
  function docFieldFromMarker(marker) {
    var name = marker && marker.getAttribute ? marker.getAttribute("data-fui-plugin-field") : "";
    name = (name || "").trim();
    return name || "formbuilder_doc";
  }

  function mirrorDoc(api, doc) {
    var form = api.form;
    if (!form || doc == null) return;
    var el = form.elements ? form.elements[docFieldFromMarker(api.marker)] : null;
    if (el && el.nodeType) el.value = JSON.stringify(doc);
  }

  // --- Register with the generic platform broker ---------------------------

  host.register("formbuilder", {
    manifest: {
      entry:        BUILDER_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: ["document:read", "document:write", "theme:read"],
      schema:       SCHEMA_VERSION,
      title:        "Form builder"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          frame.__formbuilderReady = true;
          frame.__formbuilderProbes = params.probes || null;
          frame.__formbuilderSaves = 0;
          break;
        case "themeApplied":
          frame.__formbuilderTheme = params; // {scheme, sample:{--name:value}}
          break;
        case "docChanged":
          mirrorDoc(api, params.doc);
          break;
        case "save":
          doSave(api, params);
          break;
        default:
          // resize / focusChanged / bootError handled generically.
          break;
      }
    }
  });
})();
