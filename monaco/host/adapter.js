/*!
 * monaco/host/adapter.js — host-side ADAPTER for the GoFastr Monaco plugin.
 *
 * This is a THIN adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the protocol-level machinery:
 * the sandboxed-iframe creation, the versioned postMessage envelope + source
 * check, the ready→init handshake, theme bridging, resize/focus/metric/bootError
 * handling, and SPA teardown. See protocol-v1.md §3/§4/§6 — the protocol is
 * frozen and lives entirely in the generic broker now.
 *
 * This adapter only:
 *   1. registers the monaco manifest (entry URL, sandbox policy, capabilities,
 *      schema) so the generic broker can build the iframe;
 *   2. handles the monaco-SPECIFIC events the generic broker forwards:
 *        - docChanged → mirror the code + language into the host form's hidden
 *          inputs (so a normal form POST round-trips the canonical blob);
 *        - save       → POST {doc, code, language} to the plugin save RPC and
 *          RELAY the save outcome back to the frame as `saveResult` (mirrors
 *          richtext/host/broker.js doSave: fetch does NOT reject on 4xx/5xx, so
 *          a 409 conflict / 403 / 500 would otherwise be dropped on the floor
 *          — a silent lost save);
 *   3. MIRRORS the generic hooks onto the plugin-specific names the e2e reads
 *      (iframe.__monacoReady / __monacoProbes / __monacoTheme /
 *      __monacoLastMetric):
 *        - ready        → __monacoReady + __monacoProbes
 *        - themeApplied → __monacoTheme
 *        - metric       → __monacoLastMetric
 *
 * There is NO upload path for monaco (capabilities: document:read/write +
 * theme:read only).
 *
 * Config: the Go plugin serves a small host-page script (config.js) BEFORE this
 * adapter that publishes window.__gofastrMonacoConfig — the EditorConfig the
 * host compiled in via With* options. The adapter merges it into the manifest
 * `config` it registers, so Go options reach the frame through init.config.
 *
 * Load order: the generic platform broker, then config.js, then this adapter.
 * All three are emitted by monaco.UIHostOption in that order; the demo page
 * includes the three <script>s itself.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[monaco] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var EDITOR_HTML_URL = "/__gofastr/plugin/monaco/editor.html";
  var SAVE_URL        = "/__gofastr/plugin/monaco/save";
  var SCHEMA_VERSION  = "monaco-v1";

  // Monaco advertises document:read/write + theme:read (NO upload:images).
  var DEFAULT_CAPS = ["document:read", "document:write", "theme:read"];

  // Frame-side editor defaults. window.__gofastrMonacoConfig (published by the
  // Go-served config.js host script) overrides/augments these so With* Go
  // options reach the frame via init.config.
  var STATIC_CONFIG = {
    language: "plaintext",
    theme: "auto",
    readOnly: false,
    minimap: false,
    wordWrap: false,
    lineNumbers: true,
    fontSize: 14,
    workers: false,
    diff: null
  };
  function resolvedConfig() {
    var cfg = {};
    for (var k in STATIC_CONFIG) cfg[k] = STATIC_CONFIG[k];
    var overrides = window.__gofastrMonacoConfig;
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
    var raw = marker.getAttribute("data-fui-plugin-for") || "";
    var parts = raw.split(",");
    return { code: (parts[0] || "").trim(), language: (parts[1] || "").trim() };
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

  function writeHiddenFields(api, code, language) {
    var form = api.form;
    if (!form) return;
    var fields = parseFor(api.marker);
    var codeInput = findHiddenInput(form, fields.code);
    var langInput = findHiddenInput(form, fields.language);
    if (codeInput) codeInput.value = code != null ? String(code) : "";
    if (langInput) langInput.value = language != null ? String(language) : "";
  }

  function doSave(api, params) {
    var docId = api.marker.getAttribute("data-fui-plugin-docid") || "demo";
    var code = params.code != null ? String(params.code) : "";
    var language = params.language != null ? String(params.language) : "plaintext";
    var body = {
      docId: docId,
      doc: { code: code, language: language },
      code: code,
      language: language,
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

  host.register("monaco", {
    manifest: {
      entry:        EDITOR_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      minHeight:    "320px",
      schema:       SCHEMA_VERSION,
      title:        "Monaco code editor"
    },
    config: resolvedConfig(),
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          // Mirror the handshake signal + isolation probes the e2e reads.
          frame.__monacoReady = true;
          frame.__monacoProbes = params.probes || null;
          break;
        case "themeApplied":
          frame.__monacoTheme = params; // {scheme, sample:{--name:value}}
          break;
        case "metric":
          frame.__monacoLastMetric = params; // {name, p50, p99, count, …}
          break;
        case "docChanged":
          writeHiddenFields(api, params.code, params.language);
          break;
        case "save":
          doSave(api, params);
          break;
        default:
          // resize / focusChanged / bootError / themeChanged / saveResult are
          // handled by the generic broker (saveResult is host→plugin, relayed
          // BY this adapter's doSave above to the frame); nothing monaco-
          // specific to do here on the host side.
          break;
      }
    }
  });
})();
