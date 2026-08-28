/*!
 * whiteboard/host/adapter.js — host-side ADAPTER for the GoFastr whiteboard.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, and SPA teardown. See protocol-v1.md §3/§4/§6.
 *
 * This adapter is the NETWORK LEG the frame is forbidden to have. The frame's
 * CSP sets connect-src 'none'; its Yjs updates cross the postMessage bridge
 * as opaque ArrayBuffers, and THIS file carries them the rest of the way:
 *
 *   frame syncUpdate    → POST /room/publish {kind:"sync"}     → hub fan-out
 *   hub SSE event:sync  → bridge event syncApply {update}      → frame
 *   frame presenceUpdate→ POST /room/publish {kind:"presence"} → hub fan-out
 *   hub SSE presence    → bridge event presenceApply {pid,color,x,y,down}
 *
 * Identity flows one way: the hub's SSE `hello` assigns THIS participant an
 * opaque pid and a colour, which the adapter forwards into the frame. Names
 * never cross — the frame is untrusted (docs/whiteboard.md).
 *
 * Reconnect (the demo's drop control drives it): on connect the adapter asks
 * the frame for its full CRDT state (`syncSnapshot` request) and publishes
 * it, so edits made while offline reach everyone; the hub replays its
 * persisted state down the new stream, so the frame receives everyone else's
 * offline edits. Yjs merges both directions; nobody's strokes lose.
 *
 * Mirrors on the iframe element (the parent cannot read into the opaque
 * frame, so these ARE the demo's/e2e's live view):
 *   __wbReady __wbConnected __wbPid __wbColor __wbParticipants __wbStrokes
 *   __wbSent {updates,bytes} __wbRecv {updates,bytes} __wbSyncEnabled
 *
 * Load order: the platform broker, then config.js
 * (window.__gofastrWhiteboardConfig), then this script — both the demo page
 * and whiteboard.UIHostOption emit exactly that order.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[whiteboard] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var BOARD_HTML_URL = "/__gofastr/plugin/whiteboard/board.html";
  var STREAM_URL     = "/__gofastr/plugin/whiteboard/room/stream";
  var PUBLISH_URL    = "/__gofastr/plugin/whiteboard/room/publish";
  var SCHEMA_VERSION = "whiteboard-v1";

  var DEFAULT_CAPS = ["theme:read"];

  function capabilities() {
    var cfg = window.__gofastrWhiteboardConfig;
    return cfg && cfg.syncEnabled
      ? DEFAULT_CAPS.concat(["sync:room"])
      : DEFAULT_CAPS.slice();
  }

  // --- helpers -----------------------------------------------------------------

  function docIdFromMarker(marker) {
    var id = marker && marker.getAttribute ? marker.getAttribute("data-fui-plugin-docid") : "";
    id = (id || "").trim();
    return id || "demo";
  }

  function b64encode(buf) {
    var bytes = new Uint8Array(buf);
    var bin = "";
    var CHUNK = 0x8000; // avoid call-stack limits on large snapshots
    for (var i = 0; i < bytes.length; i += CHUNK) {
      bin += String.fromCharCode.apply(null, bytes.subarray(i, i + CHUNK));
    }
    return btoa(bin);
  }

  function b64decode(text) {
    var bin = atob(text);
    var bytes = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return bytes;
  }

  function jsonHeaders(api) {
    var headers = { "Content-Type": "application/json" };
    var tok = api.csrfToken();
    if (tok) headers["X-CSRF-Token"] = tok;
    return headers;
  }

  // --- per-iframe room session ---------------------------------------------------
  // One room connection per mount. State kept in a closure per onEvent
  // registration; the adapter registers once, and the generic broker hands
  // each mount's api object to every event, so state is keyed off the iframe.

  var sessions = new WeakMap(); // iframe element → session

  function session(api) {
    var frame = api.iframe;
    var s = sessions.get(frame);
    if (!s) {
      s = {
        api: api,
        docId: docIdFromMarker(api.marker),
        es: null,
        connected: false,
        pid: "",
        color: "",
        participants: 0,
        strokes: 0,
        sent: { updates: 0, bytes: 0 },
        recv: { updates: 0, bytes: 0 },
        syncEnabled: !!(window.__gofastrWhiteboardConfig && window.__gofastrWhiteboardConfig.syncEnabled)
      };
      sessions.set(frame, s);
    }
    return s;
  }

  function mirror(s) {
    var f = s.api.iframe;
    f.__wbConnected = s.connected;
    f.__wbPid = s.pid;
    f.__wbColor = s.color;
    f.__wbParticipants = s.participants;
    f.__wbStrokes = s.strokes;
    f.__wbSent = { updates: s.sent.updates, bytes: s.sent.bytes };
    f.__wbRecv = { updates: s.recv.updates, bytes: s.recv.bytes };
    f.__wbSyncEnabled = s.syncEnabled;
  }

  function pushStatus(s) {
    mirror(s);
    s.api.sendEvent("syncStatus", {
      connected: s.connected,
      pid: s.pid,
      color: s.color,
      participants: s.participants
    });
  }

  function publish(s, payload) {
    payload.docId = s.docId;
    payload.pid = s.pid;
    return fetch(PUBLISH_URL, {
      method: "POST",
      credentials: "same-origin",
      headers: jsonHeaders(s.api),
      body: JSON.stringify(payload)
    }).then(function (r) {
      if (!r.ok) {
        return r.json().then(function (j) {
          throw new Error((j && j.error) || "HTTP " + r.status);
        }, function () {
          throw new Error("HTTP " + r.status);
        });
      }
      return r.json();
    });
  }

  function publishUpdate(s, buf) {
    s.sent.updates += 1;
    s.sent.bytes += buf.byteLength;
    mirror(s);
    return publish(s, { kind: "sync", update: b64encode(buf) })["catch"](function (e) {
      if (typeof console !== "undefined" && console.warn) {
        console.warn("[whiteboard] publish failed:", e && e.message);
      }
    });
  }

  // The reconnect handshake: publish the frame's full CRDT state so offline
  // edits reach the room. Runs after the stream's hello so the pid is known.
  function publishSnapshot(s) {
    s.api.request("syncSnapshot", {}, 8000).then(
      function (result) {
        var state = result && result.state;
        if (state && state.byteLength > 0) publishUpdate(s, state);
      },
      function (e) {
        if (typeof console !== "undefined" && console.warn) {
          console.warn("[whiteboard] snapshot request failed:", e && e.code);
        }
      }
    );
  }

  function connect(s) {
    if (!s.syncEnabled || s.es) return;
    var es = new EventSource(STREAM_URL + "?docId=" + encodeURIComponent(s.docId));
    s.es = es;

    es.addEventListener("hello", function (e) {
      var d = {};
      try { d = JSON.parse(e.data); } catch (err) { /* drop malformed */ }
      s.pid = d.pid || "";
      s.color = d.color || "";
      s.participants = d.participants || 1;
      s.connected = true;
      pushStatus(s);
      // Everyone's offline edits must cross now: ours out, theirs in (the
      // stream replays the room's persisted state on join).
      publishSnapshot(s);
    });

    es.addEventListener("sync", function (e) {
      var bytes = b64decode(e.data);
      s.recv.updates += 1;
      s.recv.bytes += bytes.byteLength;
      mirror(s);
      var buf = new ArrayBuffer(bytes.byteLength);
      new Uint8Array(buf).set(bytes);
      s.api.sendEvent("syncApply", { update: buf });
    });

    es.addEventListener("presence", function (e) {
      var d = {};
      try { d = JSON.parse(e.data); } catch (err) { return; }
      s.api.sendEvent("presenceApply", {
        pid: d.pid, color: d.color, x: d.x, y: d.y, down: d.down
      });
    });

    es.addEventListener("participants", function (e) {
      var d = {};
      try { d = JSON.parse(e.data); } catch (err) { return; }
      s.participants = d.count || 0;
      pushStatus(s);
    });

    es.onerror = function () {
      // A dropped stream is a DEMO EVENT here, not a failure to hide: mark
      // offline and stop. Reconnection is explicit (the demo control or a
      // page reload), which is what makes the offline-drawing → converge
      // journey honest and testable. Auto-reconnect would blur exactly the
      // property this plugin claims.
      var was = s.es;
      s.es = null;
      s.connected = false;
      if (was) was.close();
      pushStatus(s);
    };
  }

  function disconnect(s) {
    var was = s.es;
    s.es = null;
    s.connected = false;
    s.participants = 0;
    if (was) was.close();
    pushStatus(s);
  }

  // --- demo control (the drop/reconnect affordance) ------------------------------
  // One page-level handle for the FIRST whiteboard mount; the demo page and
  // the e2e suite drive it. Multi-mount hosts wire their own controls.
  var firstSession = null;

  window.__gofastrWhiteboardDemo = {
    disconnect: function () { if (firstSession) disconnect(firstSession); },
    reconnect: function () { if (firstSession) connect(firstSession); },
    state: function () {
      if (!firstSession) return null;
      return {
        connected: firstSession.connected,
        pid: firstSession.pid,
        color: firstSession.color,
        participants: firstSession.participants
      };
    }
  };

  // --- register with the generic platform broker ---------------------------------

  host.register("whiteboard", {
    manifest: {
      entry:        BOARD_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: capabilities(),
      schema:       SCHEMA_VERSION,
      title:        "Whiteboard"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var s = session(api);
      if (!firstSession) firstSession = s;

      switch (method) {
        case "ready":
          var f = api.iframe;
          f.__wbReady = true;
          f.__wbProbes = params.probes || null;
          mirror(s);
          connect(s);
          break;
        case "syncUpdate":
          // The frame edited its CRDT. Connected: relay immediately.
          // Offline: dropped here — the reconnect snapshot carries it.
          if (s.connected && params.update) publishUpdate(s, params.update);
          break;
        case "presenceUpdate":
          if (s.connected) {
            publish(s, { kind: "presence", x: params.x, y: params.y, down: params.down === true })
              ["catch"](function () { /* transient; cursors refresh */ });
          }
          break;
        case "boardState":
          s.strokes = typeof params.strokes === "number" ? params.strokes : s.strokes;
          mirror(s);
          break;
        default:
          // themeApplied / resize / focusChanged / bootError handled generically.
          break;
      }
    }
  });
})();
