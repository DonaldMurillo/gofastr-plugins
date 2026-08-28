/*!
 * chart/host/adapter.js — host-side ADAPTER for the GoFastr chart plugin.
 *
 * THIN adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the protocol machinery: the
 * sandboxed-iframe creation, the versioned postMessage envelope + source
 * check, the ready→init handshake, theme bridging, resize handling, and
 * SPA teardown. See protocol-v1.md §3/§4/§6.
 *
 * This adapter only:
 *   1. registers the chart manifest so the generic broker can build the
 *      opaque-origin iframe;
 *   2. implements the SSR handoff — chart.Mount() server-renders a static
 *      SVG in a wrapper element that PRECEDES the mount marker. The static
 *      chart is the page with JavaScript off. When the frame reports
 *      `ready`, the wrapper is hidden (the interactive chart replaces it);
 *      if the frame reports `bootError`, the wrapper is un-hidden so the
 *      page degrades to the static chart instead of to nothing.
 *   3. mirrors the generic hooks onto the chart-specific names the e2e
 *      reads (iframe.__chartReady / __chartTheme).
 *   4. saves `save` events (POST to the plugin save RPC) and mirrors
 *      `docChanged` specs into the hidden field, mirroring mermaid's shape.
 *
 * Load order: the generic platform broker MUST load before this adapter.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[chart] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly).
  var CHART_HTML_URL = "/__gofastr/plugin/chart/chart.html";
  var SAVE_URL       = "/__gofastr/plugin/chart/save";
  var SCHEMA_VERSION = "chart-v1";
  var DEFAULT_CAPS   = ["document:read", "document:write", "theme:read"];

  function cssEscape(s) {
    if (window.CSS && typeof window.CSS.escape === "function") return window.CSS.escape(s);
    return String(s).replace(/["\\\]]/g, "\\$&");
  }

  // The SSR wrapper is the element immediately before the mount marker.
  // The class check keeps us honest if a host interleaves other markup.
  function ssrEl(marker) {
    var el = marker.previousElementSibling;
    return el && el.classList && el.classList.contains("gofastr-chart-ssr") ? el : null;
  }

  function hideSSR(marker) {
    var el = ssrEl(marker);
    if (el) el.setAttribute("hidden", "");
  }

  function showSSR(marker) {
    var el = ssrEl(marker);
    if (el) el.removeAttribute("hidden");
  }

  function specField(marker) {
    var raw = (marker.getAttribute("data-fui-plugin-for") || "").split(",");
    var name = (raw[0] || "").trim();
    return name || "chart_spec";
  }

  function findHiddenInput(form, name) {
    if (!form || !name) return null;
    // form.elements[name] avoids building an attribute selector.
    var el = form.elements ? form.elements[name] : null;
    if (el) {
      if (el.nodeType) return el;
      if (el.length) return el[0];
    }
    return form.querySelector('input[name="' + cssEscape(name) + '"]');
  }

  function writeHiddenField(api, specJSON) {
    var form = api.form;
    if (!form) return;
    var input = findHiddenInput(form, specField(api.marker));
    if (input) input.value = specJSON != null ? String(specJSON) : "";
  }

  function doSave(api, params) {
    var docId = api.marker.getAttribute("data-fui-plugin-docid") || "demo";
    var body = {
      docId: docId,
      doc: params.doc != null ? params.doc : null,
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
    })["catch"](function () { /* save is fire-and-forget from the frame */ });
  }

  host.register("chart", {
    manifest: {
      entry:        CHART_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      minHeight:    "360px",
      schema:       SCHEMA_VERSION,
      title:        "Chart"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          // The interactive chart is up: the static SSR chart yields.
          hideSSR(api.marker);
          frame.__chartReady = true;
          break;
        case "bootError":
          // Frame could not boot: fall back to the static chart.
          showSSR(api.marker);
          break;
        case "themeApplied":
          frame.__chartTheme = params;
          break;
        case "docChanged":
          writeHiddenField(api, params.spec != null ? JSON.stringify(params.spec) : "");
          break;
        case "save":
          doSave(api, params);
          break;
        default:
          // resize / focusChanged / metric handled by the generic broker.
          break;
      }
    }
  });
})();
