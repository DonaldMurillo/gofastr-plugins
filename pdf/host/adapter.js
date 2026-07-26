/*!
 * pdf/host/adapter.js — host-side ADAPTER for the GoFastr PDF plugin.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, resize/focus/metric/bootError handling, and SPA teardown.
 * See protocol-v1.md §3/§4/§6 — the protocol is frozen and lives in the
 * generic broker.
 *
 * This adapter only:
 *   1. registers the pdf manifest (entry URL, sandbox policy, capabilities,
 *      schema) so the generic broker can build the iframe; and merges in the
 *      instance config the Go side publishes as window.__gofastrPdfConfig
 *      (mode + redact DPI + max bytes) — the bilateral enforcement channel
 *      for mode (the frame hides the UI its mode does not grant, init.config
 *      carries it);
 *   2. FETCHES the document same-origin from GET /doc/{id} (the host page has
 *      full network + the session/CSRF token) and relays the bytes INTO the
 *      frame over the bridge in ~4 MiB chunks as `documentBytes` events
 *      ({reqId, seq, total, bytes}) — the frame itself has connect-src 'none'
 *      and fetches NOTHING, which is the structural reason a confidential PDF
 *      cannot be exfiltrated;
 *   3. RELAYS the frame's `requestExport` events UP to POST /export (gated
 *      pdf:export on the Go side) and answers with an `exportResult` event
 *      carrying the produced URL (or the error code);
 *   4. MIRRORS the frame's events onto the iframe element under pdf-specific
 *      names the chromedp/WebKit tests read from the parent:
 *        ready       → __pdfReady + __pdfProbes
 *        rendered    → __pdfRendered + __pdfText + __pdfPageCount +
 *                      __pdfNonBlank + __pdfNonWhitePixels + __pdfPdfjsVersion
 *        renderError → __pdfError
 *        caps        → __pdfCaps
 *        themeApplied→ __pdfTheme
 *
 * Load order: the generic platform broker MUST load before this adapter, and
 * the instance config.js (window.__gofastrPdfConfig) MUST load before this
 * adapter too. Both the demo page and pdf.UIHostOption emit pluginhost.js,
 * then config.js, then this script.
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

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var VIEWER_HTML_URL = "/__gofastr/plugin/pdf/viewer.html";
  var DOC_BASE_URL    = "/__gofastr/plugin/pdf/doc"; // + "/" + encodeURIComponent(id)
  var EXPORT_URL      = "/__gofastr/plugin/pdf/export";
  var SCHEMA_VERSION  = "pdf-v1";

  // Read-only render + edit; document:write is advertised for platform parity,
  // theme:read bridges tokens. pdf:export is appended by the Go side only when
  // the host supplies WithExportHandler — the gate lives there, not here.
  var DEFAULT_CAPS = ["document:read", "document:write", "theme:read"];

  // The capability set bridged to the frame as init.capabilities.
  //
  // pdf:export is OPTIONAL: the Go side only grants it when the host wired
  // WithExportHandler, and it publishes that fact through config.js. Without
  // this merge the frame would never learn it holds the grant, so it would
  // either hide export permanently or — worse — offer it and let the user
  // discover the refusal only after doing the work. The frame still cannot
  // grant itself anything: this reports a decision the Go side already made,
  // and POST /export re-checks it regardless of what the frame believes.
  function capabilities() {
    var caps = DEFAULT_CAPS.slice();
    var cfg = window.__gofastrPdfConfig;
    if (cfg && cfg.exportEnabled) caps.push("pdf:export");
    return caps;
  }

  // 4 MiB per documentBytes chunk. Small enough that a single postMessage
  // structured clone stays well under any reasonable transport bound, large
  // enough that a typical multi-page PDF crosses in one or two chunks. The
  // frame accumulates chunks by reqId until seq === total - 1, then assembles
  // and renders.
  var CHUNK_BYTES = 4 * 1024 * 1024;

  // Monotonic request ids for document deliveries + export relays, so the frame
  // can correlate a chunk stream / an exportResult to the request that started
  // it even if two deliveries overlap.
  var reqCounter = 0;

  // --- Plugin-specific helpers ---------------------------------------------
  // Each takes the per-iframe `api` object the generic broker hands to onEvent
  // ({iframe, marker, form, csrfToken, sendEvent, request}).

  function docIdFromMarker(marker) {
    var id = marker && marker.getAttribute ? marker.getAttribute("data-fui-plugin-docid") : "";
    id = (id || "").trim();
    return id || "demo";
  }

  // Fetch /doc/{id} same-origin and stream the bytes into the frame as
  // documentBytes chunks. Failures surface as a renderError event the frame
  // turns into a visible error rather than a blank page.
  function pushDocBytes(api, docId) {
    var url = DOC_BASE_URL + "/" + encodeURIComponent(docId);
    fetch(url, { credentials: "same-origin" })
      .then(function (r) {
        if (!r.ok) throw new Error("doc " + docId + " HTTP " + r.status);
        return r.arrayBuffer();
      })
      .then(function (buf) {
        var all = new Uint8Array(buf);
        var reqId = "doc-" + (++reqCounter);
        var total = Math.max(1, Math.ceil(all.length / CHUNK_BYTES));
        for (var seq = 0; seq < total; seq++) {
          var start = seq * CHUNK_BYTES;
          var end = Math.min(start + CHUNK_BYTES, all.length);
          // slice() copies the chunk into its own buffer so the structured
          // clone across postMessage carries exactly the chunk's bytes (a
          // subarray view would clone the viewed region too, but slice is the
          // unambiguous choice and the copy cost is negligible at this size).
          var chunk = all.slice(start, end);
          api.sendEvent("documentBytes", {
            reqId: reqId,
            seq: seq,
            total: total,
            bytes: chunk
          });
        }
        // No loadBytes fallback: the frame ships the documentBytes assembler
        // (src/docbytes.ts), so a second whole-document event would be a
        // redundant multi-megabyte structured clone whose only protection
        // against double-rendering was a guard in the frame. One contract.
      })
      ["catch"](function (e) {
        var msg = e && e.message ? e.message : String(e);
        if (typeof console !== "undefined" && console.error) {
          console.error("[pdf] failed to relay document bytes:", msg);
        }
        api.sendEvent("renderError", { message: "host fetch failed: " + msg });
      });
  }

  // Relay a frame-originated requestExport up to POST /export. The body is the
  // raw produced PDF bytes; kind / docId / the in-frame verification report
  // ride headers (the repo's raw-body + headers encoding). The Go side gates
  // pdf:export + mode-checks the kind, then the export handler stores the
  // bytes and returns a URL we relay back as exportResult.
  // Conservative ceiling for a single header value. Real limits are 8-16 KB
  // across servers and proxies; 6 KB leaves room for the rest of the request.
  var REPORT_HEADER_LIMIT = 6144;

  // Keep the verdicts, drop the evidence. A host reading this still learns
  // whether verification passed and which checks failed — it only loses the
  // per-item detail, and it is told that it did.
  function compactReport(report) {
    var out = { truncated: true };
    if (report && typeof report === "object") {
      if (typeof report.ok === "boolean") out.ok = report.ok;
      if (Array.isArray(report.checks)) {
        out.checks = report.checks.map(function (c) {
          return { name: c && c.name, ok: !!(c && c.ok) };
        });
      }
      if (report.failed != null) out.failed = report.failed;
    }
    return out;
  }

  // A filename crosses into a Content-Disposition on the way back out, so
  // strip anything that could break out of it or walk a path. The host is
  // free to ignore the hint entirely; it is a suggestion, not a destination.
  function sanitizeFilename(name) {
    return String(name).replace(/[^\w.\- ]+/g, "_").slice(0, 128);
  }

  function relayExport(api, params) {
    params = params || {};
    var reqId = params.reqId || ("exp-" + (++reqCounter));
    var kind = params.kind || "export";
    var bytes = params.bytes; // Uint8Array (may be absent on a dry run)
    var headers = { "X-Export-Kind": kind };
    if (params.docId) headers["X-Export-DocID"] = String(params.docId);
    if (params.filename) headers["X-Export-Filename"] = sanitizeFilename(params.filename);
    if (params.report != null) {
      // The verification report rides a header, so it MUST stay small — and
      // for a redaction export it is the audit record, so it must never
      // shrink silently. Reports are bounded by design (six verdicts plus a
      // capped sample of evidence, never an unbounded list of occurrences).
      // If one still exceeds the limit, we substitute a compact record that
      // KEEPS the verdicts and says truncated:true, rather than dropping the
      // header and leaving the host to infer a pass from silence.
      try {
        var json = JSON.stringify(params.report);
        if (json.length > REPORT_HEADER_LIMIT) {
          json = JSON.stringify(compactReport(params.report));
        }
        headers["X-Export-Report"] = json;
      } catch (e) {
        headers["X-Export-Report"] = '{"truncated":true,"error":"report not serialisable"}';
      }
    }
    if (bytes) headers["Content-Type"] = "application/pdf";
    var tok = api.csrfToken();
    if (tok) headers["X-CSRF-Token"] = tok;
    fetch(EXPORT_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: headers,
      body: bytes || null
    })
      .then(function (r) {
        // The route always answers JSON (even errors); parse it regardless.
        return r.json().then(function (j) { return { ok: r.ok, status: r.status, json: j }; });
      })
      .then(function (res) {
        if (res.ok && res.json && res.json.url) {
          // Mirror the produced URL so a test can fetch the ACTUAL bytes the
          // host stored and inspect them. Asserting on the frame's own report
          // alone would only prove the frame agrees with itself.
          api.iframe.__pdfLastExportUrl = res.json.url;
          api.sendEvent("exportResult", { reqId: reqId, url: res.json.url });
        } else {
          var code = (res.json && res.json.error) || ("HTTP " + res.status);
          api.iframe.__pdfLastExportError = code;
          api.sendEvent("exportResult", { reqId: reqId, error: code });
        }
      })
      ["catch"](function (e) {
        var msg = e && e.message ? e.message : String(e);
        api.sendEvent("exportResult", { reqId: reqId, error: msg });
      });
  }

  // --- Register with the generic platform broker ---------------------------

  host.register("pdf", {
    manifest: {
      entry:        VIEWER_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: capabilities(),
      minHeight:    "480px",
      schema:       SCHEMA_VERSION,
      title:        "PDF viewer"
    },
    // Merge the instance config the Go side publishes via config.js (mode +
    // redact DPI + max bytes). The generic broker bridges this verbatim as
    // init.config, which is how the bilateral mode enforcement reaches the
    // frame: the frame hides the UI its mode does not grant. {} is the safe
    // default if config.js did not load (the frame then assumes view-only).
    config: (window.__gofastrPdfConfig && typeof window.__gofastrPdfConfig === "object")
      ? window.__gofastrPdfConfig
      : {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          frame.__pdfReady = true;
          frame.__pdfProbes = params.probes || null;
          // The broker has already sent `init` (handshake) — now push the
          // document bytes so the frame can render. This is the bridge path
          // the whole cage design exists to validate.
          pushDocBytes(api, docIdFromMarker(api.marker));
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
        case "caps":
          frame.__pdfCaps = params; // {hasPrint, clipboardWrite, allowedFeatures, origin}
          break;
        case "themeApplied":
          frame.__pdfTheme = params; // {scheme, sample:{--name:value}}
          break;
        case "docChanged":
          // Mirror the editing state the e2e suite asserts on. These live in
          // the frame, which the parent cannot read across an opaque origin —
          // the mirror IS the only channel, so an unmirrored counter is an
          // untestable feature.
          frame.__pdfDirty = !!params.dirty;
          frame.__pdfAnnotationCount = params.annotationCount || 0;
          frame.__pdfUndoDepth = params.undoDepth || 0;
          frame.__pdfRedactionCount = params.redactionCount || 0;
          frame.__pdfRev = params.rev || 0;
          // Redaction progress + the last verification report. The report also
          // travels to the server on the export request; this mirror is what
          // lets a browser test assert the SIX CHECKS passed without going
          // through the host round-trip — the parent cannot read into the
          // opaque frame, so without it the verdict is untestable from here.
          if (params.redactState !== undefined) frame.__pdfRedactState = params.redactState;
          if (params.lastVerifyReport !== undefined) frame.__pdfLastVerifyReport = params.lastVerifyReport;
          break;
        case "redactStateChanged":
          // The redaction state machine announces its own transitions. It used
          // to ride along on docChanged, which is debounced and only fires when
          // the DOCUMENT changes — so a redaction that rasterized, verified and
          // exported correctly could leave this mirror stuck on "working"
          // forever, because no document change happened to follow it. Nothing
          // surfaced: no error, no console message, just a UI that never
          // finished.
          frame.__pdfRedactState = params.redactState;
          if (params.lastVerifyReport !== undefined) frame.__pdfLastVerifyReport = params.lastVerifyReport;
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
