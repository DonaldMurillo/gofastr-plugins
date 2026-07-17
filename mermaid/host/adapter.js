/*!
 * mermaid/host/adapter.js — host-side ADAPTER for the GoFastr Mermaid plugin.
 *
 * This is a THIN adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the protocol-level machinery:
 * the sandboxed-iframe creation, the versioned postMessage envelope + source
 * check, the ready→init handshake, theme bridging, resize/focus/metric/bootError
 * handling, and SPA teardown. See protocol-v1.md §3/§4/§6 — the protocol is
 * frozen and lives entirely in the generic broker now.
 *
 * This adapter only:
 *   1. registers the mermaid manifest (entry URL, sandbox policy, capabilities,
 *      schema) so the generic broker can build the iframe;
 *   2. handles the mermaid-SPECIFIC events the generic broker forwards:
 *        - docChanged → mirror the diagram source into the host form's hidden
 *          input (so a normal form POST round-trips the canonical blob);
 *        - save       → POST {doc, source} to the plugin save RPC;
 *   3. MIRRORS the generic hooks onto the plugin-specific names the e2e reads
 *      (iframe.__mermaidReady / __mermaidProbes / __mermaidTheme /
 *      __mermaidLastMetric):
 *        - ready        → __mermaidReady + __mermaidProbes
 *        - themeApplied → __mermaidTheme
 *        - metric       → __mermaidLastMetric
 *
 * There is NO upload path for mermaid (capabilities: document:read/write +
 * theme:read only).
 *
 * Load order: the generic platform broker MUST load before this adapter so
 * window.__gofastrPluginHost is defined. Both the demo page and
 * mermaid.UIHostOption emit pluginhost.js first, then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[mermaid] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var DIAGRAM_HTML_URL = "/__gofastr/plugin/mermaid/diagram.html";
  var SAVE_URL         = "/__gofastr/plugin/mermaid/save";
  var SCHEMA_VERSION   = "mermaid-v1";

  // Mermaid advertises document:read/write + theme:read (NO upload:images).
  var DEFAULT_CAPS = ["document:read", "document:write", "theme:read"];

  // --- Plugin-specific helpers ---------------------------------------------
  // Each takes the per-iframe `api` object the generic broker hands to onEvent
  // ({iframe, marker, form, csrfToken, sendEvent, request}).

  function cssEscape(s) {
    if (window.CSS && typeof window.CSS.escape === "function") return window.CSS.escape(s);
    return String(s).replace(/["\\\]]/g, "\\$&");
  }

  // data-fui-plugin-for lists the single hidden field that holds the diagram
  // source (mermaid has no markdown sibling — the canonical doc IS the source).
  function sourceField(marker) {
    var raw = (marker.getAttribute("data-fui-plugin-for") || "").split(",");
    var name = (raw[0] || "").trim();
    return name || "diagram_source";
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

  function writeHiddenField(api, source) {
    var form = api.form;
    if (!form) return;
    var input = findHiddenInput(form, sourceField(api.marker));
    if (input) input.value = source != null ? String(source) : "";
  }

  function doSave(api, params) {
    var docId = api.marker.getAttribute("data-fui-plugin-docid") || "demo";
    var source = params.source != null ? String(params.source) : "";
    var body = {
      docId: docId,
      doc: { source: source },
      source: source,
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

  // --- Register with the generic platform broker ---------------------------

  host.register("mermaid", {
    manifest: {
      entry:        DIAGRAM_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      minHeight:    "320px",
      schema:       SCHEMA_VERSION,
      title:        "Mermaid diagram editor"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          // Mirror the handshake signal + isolation probes the e2e reads.
          frame.__mermaidReady = true;
          frame.__mermaidProbes = params.probes || null;
          break;
        case "themeApplied":
          frame.__mermaidTheme = params; // {scheme, sample:{--name:value}}
          break;
        case "metric":
          frame.__mermaidLastMetric = params; // {name:"render", p50, p99, count, …}
          break;
        case "docChanged":
          writeHiddenField(api, params.source);
          break;
        case "save":
          doSave(api, params);
          break;
        default:
          // resize / focusChanged / bootError / themeChanged are handled by the
          // generic broker; nothing mermaid-specific to do.
          break;
      }
    }
  });
})();
