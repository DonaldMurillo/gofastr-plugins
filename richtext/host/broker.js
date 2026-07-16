/*!
 * richtext/host/broker.js — host-side ADAPTER for the GoFastr Rich Text plugin.
 *
 * This is now a THIN adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the protocol-level machinery:
 * the sandboxed-iframe creation, the versioned postMessage envelope + source
 * check, the ready→init handshake, theme bridging, resize/focus/metric/bootError
 * handling, and SPA teardown. See protocol-v1.md §3/§4/§6 — the protocol is
 * frozen and lives entirely in the generic broker now.
 *
 * This adapter only:
 *   1. registers the richtext manifest (entry URL, sandbox policy, capabilities,
 *      schema) so the generic broker can build the iframe;
 *   2. handles the richtext-SPECIFIC events the generic broker forwards:
 *        - docChanged    → mirror the doc + markdown into the host form's hidden
 *          inputs (so a normal form POST round-trips the canonical blob);
 *        - save          → POST {doc, markdown} to the plugin save RPC;
 *        - requestUpload → POST the image bytes to the plugin upload RPC and
 *          reply uploadResult back to the frame;
 *   3. MIRRORS the generic hooks onto the plugin-specific names the Phase-0
 *      e2e reads (constraint: keep iframe.__richtextReady / __richtextProbes /
 *      __richtextTheme / __richtextLastMetric alive):
 *        - ready        → __richtextReady + __richtextProbes
 *        - themeApplied → __richtextTheme
 *        - metric       → __richtextLastMetric
 *
 * Load order: the generic platform broker MUST load before this adapter so
 * window.__gofastrPluginHost is defined. Both the demo page and
 * richtext.UIHostOption emit pluginhost.js first, then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[richtext] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var EDITOR_HTML_URL = "/__gofastr/plugin/richtext/editor.html";
  var SAVE_URL        = "/__gofastr/plugin/richtext/save";
  var UPLOAD_URL      = "/__gofastr/plugin/richtext/upload";
  var SCHEMA_VERSION  = "richtext-v1";

  var DEFAULT_CAPS = ["document:read", "document:write", "upload:images", "theme:read"];

  // --- Plugin-specific helpers ---------------------------------------------
  // Each takes the per-iframe `api` object the generic broker hands to onEvent
  // ({iframe, marker, form, csrfToken, sendEvent, request}).

  function parseFor(marker) {
    var raw = marker.getAttribute("data-fui-plugin-for") || "";
    var parts = raw.split(",");
    return { json: (parts[0] || "").trim(), md: (parts[1] || "").trim() };
  }

  function cssEscape(s) {
    if (window.CSS && typeof window.CSS.escape === "function") return window.CSS.escape(s);
    return String(s).replace(/["\\\]]/g, "\\$&");
  }

  function findHiddenInput(form, name) {
    if (!form || !name) return null;
    // form.elements[name] avoids building an attribute selector (injection-safe).
    var el = form.elements ? form.elements[name] : null;
    if (el) {
      if (el.nodeType) return el;          // single element
      if (el.length) return el[0];         // RadioNodeList — take first
    }
    return form.querySelector('input[name="' + cssEscape(name) + '"]');
  }

  function writeHiddenFields(api, doc, markdown) {
    var form = api.form;
    if (!form) return;
    var fields = parseFor(api.marker);
    var jsonInput = findHiddenInput(form, fields.json);
    var mdInput = findHiddenInput(form, fields.md);
    if (jsonInput) jsonInput.value = doc != null ? JSON.stringify(doc) : "";
    if (mdInput) mdInput.value = markdown != null ? String(markdown) : "";
  }

  function doSave(api, params) {
    var docId = api.marker.getAttribute("data-fui-plugin-docid") || "demo";
    var body = {
      docId: docId,
      doc: params.doc,
      markdown: params.markdown,
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
    })["catch"](function () { /* save is a fire-and-forget event from the frame */ });
  }

  function doUpload(api, params) {
    var headers = {};
    if (params.name) headers["X-Upload-Name"] = params.name;
    if (params.type) {
      headers["X-Upload-Type"] = params.type;
      headers["Content-Type"] = params.type;
    }
    var tok = api.csrfToken();
    if (tok) headers["X-CSRF-Token"] = tok;
    var body = params.bytes; // ArrayBuffer (structured clone) per §4
    fetch(UPLOAD_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: headers,
      body: body
    }).then(function (resp) { return resp.json(); }).then(function (data) {
      var p = data && data.url
        ? { reqId: params.reqId, url: data.url }
        : { reqId: params.reqId, error: (data && data.error) || "E_UPLOAD" };
      api.sendEvent("uploadResult", p);
    })["catch"](function (err) {
      api.sendEvent("uploadResult", {
        reqId: params.reqId,
        error: (err && err.message) ? err.message : "E_UPLOAD"
      });
    });
  }

  // --- Register with the generic platform broker ---------------------------

  host.register("richtext", {
    manifest: {
      entry:        EDITOR_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      minHeight:    "240px",
      schema:       SCHEMA_VERSION,
      title:        "Rich Text editor"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          // Mirror the handshake signal + isolation probes the e2e reads.
          frame.__richtextReady = true;
          frame.__richtextProbes = params.probes || null;
          break;
        case "themeApplied":
          frame.__richtextTheme = params; // {scheme, sample:{--name:value}}
          break;
        case "metric":
          frame.__richtextLastMetric = params; // {p50, p99, count, …}
          break;
        case "docChanged":
          writeHiddenFields(api, params.doc, params.markdown);
          break;
        case "save":
          doSave(api, params);
          break;
        case "requestUpload":
          doUpload(api, params);
          break;
        default:
          // resize / focusChanged / bootError / themeChanged are handled by the
          // generic broker; nothing richtext-specific to do.
          break;
      }
    }
  });
})();
