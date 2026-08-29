/*!
 * sqlnotebook/host/adapter.js — host-side ADAPTER for the GoFastr SQL
 * notebook plugin.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, resize/focus/metric/bootError handling, and SPA teardown.
 * See protocol-v1.md §3/§4/§6 — the protocol is frozen and lives in the
 * generic broker.
 *
 * This adapter is the engine's DELIVERY side. The frame is an opaque origin
 * under connect-src 'none': it can never fetch its own .wasm (sql.js's
 * documented `locateFile` option dies exactly there — "both async and sync
 * fetching of the wasm failed"). So this adapter, running UNFRAMED on the
 * host page with full privileges:
 *
 *   1. waits for the broker's ready handshake (onEvent "ready"), then
 *      FETCHES RoutePrefix + "/sql-wasm.wasm" same-origin and relays the
 *      ArrayBuffer INTO the frame as `sqlnb/init` {v, wasm, seed} — the
 *      frame calls initSqlJs({ wasmBinary }) and never fetches anything;
 *   2. relays `sqlnb/query` {v, id, sql} down (from window.__sqlnbDebug.query)
 *      and `sqlnb/result` / `sqlnb/error` back up, correlating by id;
 *   3. exposes window.__sqlnbDebug so the e2e suite can assert progress
 *      without scraping the frame's DOM (the parent cannot read across the
 *      opaque origin — the mirror IS the only channel):
 *        __sqlnbDebug.initSent    number  sqlnb/init events posted
 *        __sqlnbDebug.results     number  sqlnb/result events received
 *        __sqlnbDebug.errors      number  sqlnb/error events received
 *        __sqlnbDebug.queries     number  sqlnb/query events relayed down
 *        __sqlnbDebug.ready       object|null  last sqlnb/ready
 *                                    {sqliteVersion, ms}
 *        __sqlnbDebug.wasmBytes   number  byte length of the wasm handed in
 *        __sqlnbDebug.fetchError  string  wasm fetch/relay failure ("" = ok)
 *        __sqlnbDebug.query(sql)  Promise<{columns, rows, truncated, ms}>
 *                                 — rejects with the frame's error message
 *
 * Query ids are positive host-side; the frame mints NEGATIVE ids for
 * UI-initiated runs (Run button / Ctrl+Enter), so the ranges can never
 * collide and only ids this adapter has outstanding are ever settled.
 *
 * The seed SQL for sqlnb/init comes from the mount marker's generic
 * data-fui-plugin-doc attribute (MountConfig.Doc): a JSON-encoded string or
 * {"sql": "…"} object is decoded, any other non-empty string is taken as
 * raw SQL, and an absent/empty attribute falls back to DEFAULT_SEED so the
 * notebook is never empty. One contract: the frame seeds ONLY from
 * sqlnb/init.
 *
 * Load order: the generic platform broker MUST load before this adapter.
 * Both the demo page and sqlnotebook.UIHostOption emit pluginhost.js first,
 * then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[sqlnotebook] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var ROUTE_PREFIX   = "/__gofastr/plugin/sqlnotebook";
  var FRAME_HTML_URL = ROUTE_PREFIX + "/frame.html";
  var WASM_URL       = ROUTE_PREFIX + "/sql-wasm.wasm";
  var SCHEMA_VERSION = "sqlnotebook.v1";

  // The sqlnotebook wire-protocol version carried inside every sqlnb/* params.
  var SQLNB_VERSION = 1;

  // Host-side budget for one relayed query. The engine is local wasm
  // (measured init ~28ms), so this is generous headroom, not a tuning knob.
  var QUERY_TIMEOUT_MS = 10000;

  // Fallback seed when the marker carries none: a table worth querying.
  var DEFAULT_SEED = [
    "CREATE TABLE notes (",
    "  id INTEGER PRIMARY KEY,",
    "  body TEXT NOT NULL,",
    "  created_at TEXT DEFAULT (datetime('now'))",
    ");",
    "INSERT INTO notes (body) VALUES",
    "  ('the wasm bytes crossed the bridge'),",
    "  ('connect-src none holds — the frame never fetched a thing'),",
    "  ('sqlite is running inside the cage');"
  ].join("\n");

  // --- debug surface (the e2e contract — see header) ------------------------

  var queryCounter = 0;
  // id (as string) -> {resolve, reject, timer} for relayed queries. NULL-PROTO,
  // not {}: the frame controls the id on the way back, and a {} lookup of
  // "__proto__" returns an Object.prototype member — truthy, so a hostile
  // frame could otherwise settle an id nobody issued. Object.create(null)
  // makes unknown ids uniformly undefined.
  var outstanding = Object.create(null);

  // The api of the most recently readied mount; __sqlnbDebug.query targets it.
  var lastReadyApi = null;

  var debug = {
    initSent: 0,
    results: 0,
    errors: 0,
    queries: 0,
    ready: null,
    wasmBytes: 0,
    fetchError: ""
  };

  function settle(id, err, result) {
    var key = String(id);
    var entry = outstanding[key];
    if (!entry) return; // not ours (or a UI-minted negative id): just counted
    clearTimeout(entry.timer);
    delete outstanding[key];
    if (err) entry.reject(err);
    else entry.resolve(result);
  }

  debug.query = function (sql, timeoutMs) {
    return new Promise(function (resolve, reject) {
      if (!lastReadyApi) {
        reject(new Error("sqlnotebook: engine not ready (no sqlnb/ready yet)"));
        return;
      }
      if (typeof sql !== "string" || sql === "") {
        reject(new Error("sqlnotebook: query needs a non-empty SQL string"));
        return;
      }
      var budget = (typeof timeoutMs === "number" && isFinite(timeoutMs) && timeoutMs > 0)
        ? timeoutMs
        : QUERY_TIMEOUT_MS;
      var id = ++queryCounter;
      outstanding[String(id)] = {
        resolve: resolve,
        reject: reject,
        timer: setTimeout(function () {
          delete outstanding[String(id)];
          reject(new Error("sqlnotebook: query timed out after " + budget + " ms"));
        }, budget)
      };
      debug.queries += 1;
      lastReadyApi.sendEvent("sqlnb/query", { v: SQLNB_VERSION, id: id, sql: sql });
    });
  };

  window.__sqlnbDebug = debug;

  // --- Plugin-specific helpers ----------------------------------------------
  // Each takes the per-iframe `api` object the generic broker hands to onEvent
  // ({iframe, marker, form, csrfToken, sendEvent, request}).

  function seedFromMarker(marker) {
    var raw = marker && marker.getAttribute ? marker.getAttribute("data-fui-plugin-doc") : "";
    raw = (raw || "").trim();
    if (raw !== "") {
      try {
        var parsed = JSON.parse(raw);
        if (typeof parsed === "string" && parsed.trim() !== "") return parsed;
        if (parsed && typeof parsed === "object" &&
            typeof parsed.sql === "string" && parsed.sql.trim() !== "") {
          return parsed.sql;
        }
      } catch (e) {
        // Not JSON — take the raw attribute as SQL. MountConfig.Doc is a
        // free string; only the JSON-encoded forms round-trip above.
        return raw;
      }
    }
    return DEFAULT_SEED;
  }

  // Fetch the wasm same-origin and hand the bytes into the frame. The frame
  // has connect-src 'none' and can never do this itself — this relay IS the
  // engine's only road in.
  function pushInit(api) {
    var frame = api.iframe;
    if (frame.__sqlnbInitSent) return; // one init per mount, however ready re-fires
    frame.__sqlnbInitSent = true;
    fetch(WASM_URL, { credentials: "same-origin" })
      .then(function (r) {
        if (!r.ok) throw new Error("wasm HTTP " + r.status);
        return r.arrayBuffer();
      })
      .then(function (buf) {
        debug.wasmBytes = buf.byteLength;
        debug.initSent += 1;
        api.sendEvent("sqlnb/init", {
          v: SQLNB_VERSION,
          wasm: buf,
          seed: seedFromMarker(api.marker)
        });
      })
      ["catch"](function (e) {
        // No sqlnb/init will ever arrive; record why where a test can see
        // it. The frame keeps its "waiting for engine" hint visible.
        var msg = e && e.message ? e.message : String(e);
        debug.fetchError = msg;
        if (typeof console !== "undefined" && console.error) {
          console.error("[sqlnotebook] failed to relay wasm bytes:", msg);
        }
      });
  }

  // --- Register with the generic platform broker ---------------------------

  host.register("sqlnotebook", {
    manifest: {
      entry:        FRAME_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      // The database lives inside the frame; the only host resource this
      // plugin touches is the theme token bridge.
      capabilities: ["theme:read"],
      minHeight:    "360px",
      schema:       SCHEMA_VERSION,
      title:        "SQL notebook"
    },
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          frame.__sqlnbReady = true;
          // The broker has already sent `init` (handshake + tokens) — now
          // deliver the engine bytes so sql.js can start.
          pushInit(api);
          break;
        case "sqlnb/ready":
          if (params.v !== SQLNB_VERSION) break; // mismatched v → ignore
          lastReadyApi = api;
          debug.ready = {
            sqliteVersion: typeof params.sqliteVersion === "string" ? params.sqliteVersion : "",
            ms: typeof params.ms === "number" ? params.ms : 0
          };
          frame.__sqlnbEngineReady = true;
          frame.__sqlnbSqliteVersion = debug.ready.sqliteVersion;
          break;
        case "sqlnb/result":
          if (params.v !== SQLNB_VERSION) break; // mismatched v → ignore
          debug.results += 1;
          settle(params.id, null, {
            columns: Array.isArray(params.columns) ? params.columns : [],
            rows: Array.isArray(params.rows) ? params.rows : [],
            truncated: !!params.truncated,
            ms: typeof params.ms === "number" ? params.ms : 0
          });
          break;
        case "sqlnb/error":
          if (params.v !== SQLNB_VERSION) break; // mismatched v → ignore
          debug.errors += 1;
          // The frame is untrusted: length-cap the text, mirror it narrowed.
          var msg = typeof params.message === "string" ? params.message.slice(0, 500) : "";
          frame.__sqlnbLastError = msg;
          settle(params.id, new Error("sqlnotebook: " + msg), null);
          break;
        default:
          // resize / focusChanged / bootError / themeApplied handled generically.
          break;
      }
    }
  });
})();
