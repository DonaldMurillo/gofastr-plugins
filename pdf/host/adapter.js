/*!
 * pdf/host/adapter.js — host-side ADAPTER for the GoFastr PDF spike plugin.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, resize/focus/metric/bootError handling, and SPA teardown.
 *
 * This adapter only:
 *   1. registers the pdf manifest (entry URL, sandbox policy, capabilities,
 *      schema) so the generic broker can build the iframe;
 *   2. FETCHES the sample PDF same-origin (the host has full network) and
 *      forwards the bytes OVER THE BRIDGE to the frame — the frame itself has
 *      connect-src 'none' and fetches NOTHING. The bytes ride a `loadBytes`
 *      plugin→frame event as a Uint8Array (structured clone);
 *   3. MIRRORS the frame's events onto the iframe element under pdf-specific
 *      names the chromedp/WebKit tests read from the parent:
 *        ready     → __pdfReady + __pdfProbes
 *        rendered  → __pdfRendered + __pdfText + __pdfPageCount +
 *                    __pdfNonBlank + __pdfNonWhitePixels + __pdfPdfjsVersion
 *        renderError → __pdfError
 *
 * Load order: the generic platform broker MUST load before this adapter so
 * window.__gofastrPluginHost is defined. Both the demo page and pdf.UIHostOption
 * emit pluginhost.js first, then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[pdf] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  var VIEWER_HTML_URL = "/__gofastr/plugin/pdf/viewer.html";
  var SAMPLE_PDF_URL  = "/__gofastr/plugin/pdf/sample.pdf";
  var SCHEMA_VERSION  = "pdf-v1";

  // Read-only render spike; document:write is advertised for platform parity.
  var DEFAULT_CAPS = ["document:read", "document:write", "theme:read"];

  // Fetch the sample PDF same-origin and forward the bytes to the frame as a
  // Uint8Array over the bridge. Returns a promise (fire-and-forget from the
  // caller's perspective; failures surface as a renderError event).
  function pushSampleBytes(api) {
    fetch(SAMPLE_PDF_URL, { credentials: "same-origin" })
      .then(function (r) {
        if (!r.ok) throw new Error("sample.pdf HTTP " + r.status);
        return r.arrayBuffer();
      })
      .then(function (buf) {
        // Structured clone carries the Uint8Array across postMessage verbatim —
        // no base64, no copy beyond the structured-clone the browser does anyway.
        api.sendEvent("loadBytes", { bytes: new Uint8Array(buf) });
      })
      ["catch"](function (e) {
        var msg = e && e.message ? e.message : String(e);
        if (typeof console !== "undefined" && console.error) {
          console.error("[pdf] failed to forward sample bytes:", msg);
        }
        api.sendEvent("renderError", { message: "host fetch failed: " + msg });
      });
  }

  host.register("pdf", {
    manifest: {
      entry:        VIEWER_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      minHeight:    "480px",
      schema:       SCHEMA_VERSION,
      title:        "PDF viewer"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          frame.__pdfReady = true;
          frame.__pdfProbes = params.probes || null;
          // The broker has already sent `init` (handshake) — now push the PDF
          // bytes so the frame can render. This is the bridge path the spike
          // exists to validate.
          pushSampleBytes(api);
          break;
        case "caps":
          frame.__pdfCaps = params; // {hasPrint, clipboardWrite, allowedFeatures, origin}
          break;
        case "rendered":
          frame.__pdfRendered = true;
          frame.__pdfText = params.text || "";
          frame.__pdfPageCount = params.pageCount || 0;
          frame.__pdfNonBlank = !!params.nonBlank;
          frame.__pdfNonWhitePixels = params.nonWhitePixels || 0;
          frame.__pdfPdfjsVersion = params.pdfjsVersion || "";
          break;
        case "renderError":
          frame.__pdfError = params.message || "unknown render error";
          break;
        case "themeApplied":
          frame.__pdfTheme = params; // {scheme, sample:{--name:value}}
          break;
        default:
          // resize / focusChanged / bootError / docChanged handled generically.
          break;
      }
    }
  });
})();
