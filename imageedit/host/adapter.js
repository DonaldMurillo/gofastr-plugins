/*!
 * imageedit/host/adapter.js — host-side ADAPTER for the GoFastr image editor.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, resize/focus/metric/bootError handling, and SPA teardown.
 * See protocol-v1.md §3/§4/§6.
 *
 * This adapter only:
 *   1. registers the imageedit manifest (entry URL, sandbox policy,
 *      capabilities, schema), merging the optional upload:images grant the
 *      Go side publishes via config.js (window.__gofastrImageeditConfig) —
 *      the same bilateral channel pdf uses for pdf:export;
 *   2. RELAYS the frame's correlated events UP to the plugin's RPC routes
 *      and answers them back into the frame — the richtext
 *      requestUpload → uploadResult pattern, applied to image bytes:
 *        requestImage   → GET  /img/{docId}  → imageResult   {reqId, bytes, mime}
 *        requestUpload  → POST /upload       → uploadResult  {reqId, id}
 *        requestExport  → POST /export       → exportResult  {reqId, url, …}
 *      Every byte crossing the boundary does so through code the host
 *      controls: the frame has connect-src 'none' and cannot fetch.
 *   3. MIRRORS bridge traffic onto the iframe element so a parent-side test
 *      or demo page can show the plugin's proof live:
 *        __imageeditReady         handshake completed
 *        __imageeditDoc           the live operation list (JSON string)
 *        __imageeditPreview       {dataUrl,width,height} — the frame's own
 *                                 1:1 render, for the preview-vs-server panel
 *        __imageeditLastExport    {url,width,height,bytes,sha256,verify,report}
 *        __imageeditLastError     last relay error code
 *
 * Load order: the generic platform broker MUST load before this adapter, and
 * the instance config.js (window.__gofastrImageeditConfig) MUST load before
 * it too. Both the demo page and imageedit.UIHostOption emit pluginhost.js,
 * then config.js, then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[imageedit] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var EDITOR_HTML_URL = "/__gofastr/plugin/imageedit/editor.html";
  var IMG_URL         = "/__gofastr/plugin/imageedit/img/";
  var UPLOAD_URL      = "/__gofastr/plugin/imageedit/upload";
  var EXPORT_URL      = "/__gofastr/plugin/imageedit/export";
  var SAVE_URL        = "/__gofastr/plugin/imageedit/save";
  var SCHEMA_VERSION  = "imageedit-v1";

  // document:read/write + theme:read are always on; upload:images is
  // appended only when the Go side wired the matching handler (config.js
  // publishes that fact). The frame learns the same set via
  // init.capabilities, and POST /upload re-checks the gate regardless of
  // what the frame believes.
  var DEFAULT_CAPS = ["document:read", "document:write", "theme:read"];

  function capabilities() {
    var caps = DEFAULT_CAPS.slice();
    var cfg = window.__gofastrImageeditConfig;
    if (cfg && cfg.uploadEnabled) caps.push("upload:images");
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

  // Relay a requestImage up to GET /img/{docId} and answer with an
  // imageResult carrying the BYTES as an ArrayBuffer. This round trip is the
  // plugin's whole isolation story: the fetch runs HERE, in the privileged
  // host page (session + CSRF attached), and the frame receives pixels over
  // the postMessage bridge instead of the network.
  function relayImage(api, params) {
    params = params || {};
    var reqId = params.reqId || "";
    var frame = api.iframe;
    // The ref the frame's DOC names (an uploaded image id after a load),
    // falling back to the mount's docId. Both are host-namespace ids
    // resolved by WithSource — the frame cannot pick a URL.
    var ref = typeof params.ref === "string" && params.ref.trim() !== ""
      ? params.ref.trim()
      : docIdFromMarker(api.marker);
    fetch(IMG_URL + encodeURIComponent(ref), {
      method: "GET",
      credentials: "same-origin",
      headers: jsonHeaders(api, {})
    })
      .then(function (r) {
        if (!r.ok) {
          return r.json().then(
            function (j) { return { ok: false, code: (j && j.error) || ("HTTP " + r.status) }; },
            function () { return { ok: false, code: "HTTP " + r.status }; }
          );
        }
        return r.arrayBuffer().then(function (buf) {
          return { ok: true, buf: buf, mime: r.headers.get("Content-Type") || "image/png" };
        });
      })
      .then(function (res) {
        if (res.ok) {
          api.sendEvent("imageResult", { reqId: reqId, bytes: res.buf, mime: res.mime });
        } else {
          if (frame) frame.__imageeditLastError = res.code;
          api.sendEvent("imageResult", { reqId: reqId, error: res.code });
        }
      })
      ["catch"](function () {
        if (frame) frame.__imageeditLastError = "E_NETWORK";
        api.sendEvent("imageResult", { reqId: reqId, error: "E_NETWORK" });
      });
  }

  // Relay a requestUpload up to POST /upload (raw image bytes as the body).
  // The Go side caps + header-checks before storing via the host's upload
  // handler and answers {id}; the frame then points its doc.src at that id.
  function relayUpload(api, params) {
    params = params || {};
    var reqId = params.reqId || "";
    var frame = api.iframe;
    var headers = {};
    var tok = api.csrfToken();
    if (tok) headers["X-CSRF-Token"] = tok;
    if (params.type) headers["Content-Type"] = params.type;
    if (params.name) headers["X-Image-Filename"] = params.name;
    fetch(UPLOAD_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: headers,
      body: params.bytes || new ArrayBuffer(0)
    })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, json: j }; });
      })
      .then(function (res) {
        if (res.ok && res.json && res.json.id) {
          api.sendEvent("uploadResult", { reqId: reqId, id: res.json.id });
        } else {
          var code = (res.json && res.json.error) || "E_UPLOAD";
          if (frame) frame.__imageeditLastError = code;
          api.sendEvent("uploadResult", { reqId: reqId, error: code });
        }
      })
      ["catch"](function () {
        if (frame) frame.__imageeditLastError = "E_NETWORK";
        api.sendEvent("uploadResult", { reqId: reqId, error: "E_NETWORK" });
      });
  }

  // Relay a requestExport up to POST /export. The frame sends ONLY the
  // operation list; the GO side resolves the source, re-renders crop →
  // rotate → annotate → redact with the standard library, strips EXIF,
  // verifies the redactions against the produced bytes, and hands the
  // result to the host's export handler. Only the URL (plus the report)
  // crosses back.
  function relayExport(api, params) {
    params = params || {};
    var reqId = params.reqId || "";
    var frame = api.iframe;
    fetch(EXPORT_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(api, {}),
      body: JSON.stringify({
        docId: docIdFromMarker(api.marker),
        doc: params.doc || null
      })
    })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, status: r.status, json: j }; });
      })
      .then(function (res) {
        if (res.ok && res.json && res.json.url) {
          var mirror = {
            url: res.json.url,
            format: res.json.format || "",
            width: res.json.width || 0,
            height: res.json.height || 0,
            bytes: res.json.bytes || 0,
            sha256: res.json.sha256 || "",
            verify: res.json.verify === true,
            report: res.json.report || null
          };
          if (frame) frame.__imageeditLastExport = mirror;
          api.sendEvent("exportResult", {
            reqId: reqId,
            url: mirror.url,
            format: mirror.format,
            width: mirror.width,
            height: mirror.height,
            bytes: mirror.bytes,
            sha256: mirror.sha256,
            verify: mirror.verify
          });
        } else {
          var code = (res.json && res.json.error) || ("HTTP " + res.status);
          if (frame) frame.__imageeditLastError = code;
          api.sendEvent("exportResult", { reqId: reqId, error: code });
        }
      })
      ["catch"](function () {
        if (frame) frame.__imageeditLastError = "E_NETWORK";
        api.sendEvent("exportResult", { reqId: reqId, error: "E_NETWORK" });
      });
  }

  function doSave(api, params) {
    params = params || {};
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
    return name || "imageedit_doc";
  }

  function mirrorDoc(api, doc) {
    var form = api.form;
    if (!form || doc == null) return;
    // form.elements[name] avoids building an attribute selector.
    var el = form.elements ? form.elements[docFieldFromMarker(api.marker)] : null;
    if (el && el.nodeType) el.value = JSON.stringify(doc);
  }

  // --- Register with the generic platform broker ---------------------------

  host.register("imageedit", {
    manifest: {
      entry:        EDITOR_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: capabilities(),
      schema:       SCHEMA_VERSION,
      title:        "Image editor"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          frame.__imageeditReady = true;
          frame.__imageeditProbes = params.probes || null;
          break;
        case "docChanged":
          // The live operation list — the demo page renders it as JSON and
          // the e2e journeys assert against it.
          frame.__imageeditDoc = params.doc ? JSON.stringify(params.doc) : "";
          mirrorDoc(api, params.doc);
          break;
        case "previewRender":
          frame.__imageeditPreview = {
            dataUrl: params.dataUrl || "",
            width: params.width || 0,
            height: params.height || 0
          };
          break;
        case "save":
          doSave(api, params);
          break;
        case "requestImage":
          relayImage(api, params);
          break;
        case "requestUpload":
          relayUpload(api, params);
          break;
        case "requestExport":
          relayExport(api, params);
          break;
        default:
          // resize / focusChanged / themeApplied / bootError handled generically.
          break;
      }
    }
  });
})();
