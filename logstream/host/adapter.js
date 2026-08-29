/*!
 * logstream/host/adapter.js — host-side ADAPTER for the GoFastr log stream.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init handshake,
 * theme bridging, resize/focus/metric handling, and SPA teardown. See
 * protocol-v1.md §3/§4/§6.
 *
 * This adapter is where the plugin's actual product lives: the PUSH side of
 * the stream and ALL of its backpressure. Every other plugin here answers
 * frame-initiated requests; a log tail is open-ended, so this adapter:
 *
 *   1. opens the host's NDJSON line stream (GET /stream — the host page has
 *      full privileges; the frame's CSP forbids it from ever fetching) and
 *      drains it greedily;
 *   2. batches lines and PUSHES them into the frame as unsolicited
 *      `streamBatch` events — the frame never asks;
 *   3. keeps at most MAX_IN_FLIGHT unacknowledged batches in flight. The
 *      frame answers each rendered batch with a `streamAck` carrying the
 *      last sequence number it rendered; an ack releases every in-flight
 *      batch at or below that number;
 *   4. buffers what it cannot send in a BOUNDED line buffer. When the
 *      producer outruns the frame's render loop, the OLDEST buffered lines
 *      are dropped and counted — the count is sent out of band immediately
 *      (streamDropped) AND travels with the next batch, and
 *      the frame renders a visible "N lines dropped" marker. A gap the user
 *      cannot see is worse than a gap labelled "1,432 lines dropped".
 *
 * Pause/resume is a HOST-side concern (the frame stays a pure sink whose
 * every ack is truthful): pausing stops SENDING, while the NDJSON stream
 * keeps being drained into the bounded buffer — so a long pause overflows
 * and the overflow shows up as the same visible marker on resume. Any
 * element on the page can dispatch `logstream:pause` / `logstream:resume`
 * CustomEvents on document.
 *
 * MIRRORS bridge traffic onto the iframe element so a parent-side test (and
 * the demo page's live readout) can watch the claim:
 *   __logstreamReady          handshake completed
 *   __logstreamDelivered      lines pushed into the frame this session
 *   __logstreamDropped        lines dropped host-side (never crossed)
 *   __logstreamInFlight       unacknowledged batches right now
 *   __logstreamAcks           streamAck events received
 *   __logstreamStats          latest ack {lastSeq, rendered, markers,
 *                             lastMarker, scrollback, cap}
 *   __logstreamPaused         push paused?
 *   __logstreamReconnects     NDJSON reconnects after error/close
 *   __logstreamLastError      last transport/protocol error code
 *
 * Load order: the generic platform broker MUST load before this adapter.
 * Both the demo page and logstream.UIHostOption emit pluginhost.js first,
 * then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[logstream] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly — protocol-v1.md §2/§10).
  var TERM_HTML_URL = "/__gofastr/plugin/logstream/term.html";
  var STREAM_URL = "/__gofastr/plugin/logstream/stream";
  var SCHEMA_VERSION = "logstream-v1";

  var DEFAULT_CAPS = ["stream:read", "theme:read"];

  // --- the backpressure constants (docs/logstream.md publishes these) -------
  var MAX_IN_FLIGHT = 4;   // unacknowledged batches allowed in flight
  var BATCH_MAX = 24;      // lines per streamBatch
  var FLUSH_MS = 100;      // send a short batch after this idle, never slower
  var BUFFER_MAX = 2000;   // bounded line buffer; overflow drops OLDEST
  var RECONNECT_MS = 500;  // backoff before reopening the NDJSON stream

  function jsonHeaders(api) {
    var headers = { "Content-Type": "application/json" };
    var tok = api.csrfToken();
    if (tok) headers["X-CSRF-Token"] = tok;
    return headers;
  }

  // Per-iframe push state. One entry per mount; the broker hands each onEvent
  // call the matching `api` ({iframe, marker, form, csrfToken, sendEvent,
  // request}), and the iframe element itself carries the mirrors.
  function startStream(api) {
    var frame = api.iframe;
    var st = {
      acked: 0,          // highest seq the frame acknowledged
      sent: 0,           // highest seq handed to the frame
      inFlight: [],      // [{first,last}] of unacknowledged batches
      buffer: [],        // lines waiting for a free window slot
      dropped: 0,        // lines dropped host-side, not yet reported
      droppedTotal: 0,
      delivered: 0,
      acks: 0,
      paused: false,
      reconnects: 0,
      abort: null,
      flushTimer: null,
      closed: false
    };

    function mirror(name, value) { frame[name] = value; }

    function refreshMirrors() {
      mirror("__logstreamDelivered", st.delivered);
      mirror("__logstreamDropped", st.droppedTotal);
      mirror("__logstreamInFlight", st.inFlight.length);
      mirror("__logstreamPaused", st.paused);
    }

    // Release every in-flight batch the ack covers, then top the window back
    // up from the buffer. Acks arrive per rendered batch and carry the LAST
    // sequence number rendered, so one ack can clear several batches.
    function onAck(params) {
      st.acks += 1;
      var last = typeof params.lastSeq === "number" ? params.lastSeq : 0;
      if (last > st.acked) st.acked = last;
      st.inFlight = st.inFlight.filter(function (b) { return b.last > last; });
      if (params && typeof params === "object") {
        mirror("__logstreamStats", {
          lastSeq: typeof params.lastSeq === "number" ? params.lastSeq : 0,
          rendered: typeof params.rendered === "number" ? params.rendered : 0,
          // Drop markers the frame actually WROTE, and the most recent one.
          // Narrowed like every other field: the frame is untrusted, so the
          // text is length-capped rather than mirrored verbatim.
          markers: typeof params.markers === "number" ? params.markers : 0,
          lastMarker:
            typeof params.lastMarker === "string" ? params.lastMarker.slice(0, 200) : "",
          scrollback: typeof params.scrollback === "number" ? params.scrollback : 0,
          cap: typeof params.cap === "number" ? params.cap : 0
        });
      }
      refreshMirrors();
      maybeSend();
    }

    // Out-of-band gap notice. Fire-and-forget: if the frame is gone, the same
    // teardown path maybeSend uses will notice on the next attempt.
    function notifyDropped() {
      try {
        api.sendEvent("streamDropped", { dropped: st.dropped, total: st.droppedTotal });
      } catch (e) {
        /* frame went away; maybeSend handles the teardown */
      }
    }

    // Push as many buffered batches as the window allows. The dropped count
    // travels with the FIRST batch sent after the drop (the lines were older
    // than that batch, so the marker lands above it in the terminal).
    function maybeSend() {
      if (st.closed || st.paused) return;
      while (st.inFlight.length < MAX_IN_FLIGHT && st.buffer.length > 0) {
        var lines = st.buffer.splice(0, BATCH_MAX);
        var first = lines[0].seq;
        var last = lines[lines.length - 1].seq;
        var dropped = st.dropped;
        st.dropped = 0;
        try {
          api.sendEvent("streamBatch", {
            first: first,
            last: last,
            lines: lines,
            dropped: dropped,
            // The running total lets the frame tell "this gap was already
            // announced out of band" from "this is news", so the marker is
            // written exactly once however the count reaches it.
            droppedTotal: st.droppedTotal
          });
        } catch (e) {
          // The iframe went away between checks (SPA nav race): stop quietly.
          st.closed = true;
          if (st.abort) st.abort.abort();
          return;
        }
        st.sent = last;
        st.delivered += lines.length;
        st.inFlight.push({ first: first, last: last });
      }
      refreshMirrors();
    }

    function onFlushTick() {
      st.flushTimer = null;
      maybeSend();
    }

    // One NDJSON record: {"seq":N,"text":"…"}. Newlines inside text are JSON
    // escapes, so the line framing is always safe.
    function onLine(obj) {
      if (!obj || typeof obj.seq !== "number" || typeof obj.text !== "string") return;
      if (obj.seq <= st.sent) return; // duplicate after reconnect: drop host-side
      st.buffer.push({ seq: obj.seq, text: obj.text });
      if (st.buffer.length > BUFFER_MAX) {
        // The explicit overflow path: producer outruns consumer, the window
        // is full, the buffer is full — drop the OLDEST line and count it.
        st.buffer.shift();
        st.dropped += 1;
        st.droppedTotal += 1;
      }
      // Tell the frame about a gap OUT OF BAND, immediately.
      //
      // The count used to travel only on the next batch, which under sustained
      // backpressure is queued behind the very congestion it reports: the drop
      // had happened, the frame had not been told, and on a slow machine that
      // lag ran past 20 seconds while the counter beside it was already
      // climbing. "Never a silent gap" has to mean now, not eventually.
      //
      // This is a control-plane fact about the stream rather than a line of
      // it, so it does not wait its turn behind data. The count still also
      // rides on the next batch: a frame that missed this event still learns,
      // and the frame de-duplicates so the marker is written once.
      if (st.dropped > 0) notifyDropped();
      maybeSend();
      if (!st.flushTimer && st.inFlight.length >= MAX_IN_FLIGHT) {
        // Window full: whatever survives the next ack release must still go
        // out promptly even if the stream goes quiet right after.
        st.flushTimer = setTimeout(onFlushTick, FLUSH_MS);
      }
    }

    // The NDJSON transport: open, drain greedily, reconnect on error. The
    // reconnect resumes from the last ACKED seq, not the last sent — and the
    // frame dedups anything it already rendered, so a lost-ack race cannot
    // duplicate lines.
    function open() {
      if (st.closed) return;
      if (!frame.isConnected) { st.closed = true; return; }
      st.abort = new AbortController();
      fetch(STREAM_URL + "?after=" + st.acked, {
        method: "GET",
        credentials: "same-origin",
        headers: { "Accept": "application/x-ndjson" },
        signal: st.abort.signal
      }).then(function (resp) {
        if (!resp.ok || !resp.body) {
          mirror("__logstreamLastError", "HTTP " + resp.status);
          throw new Error("HTTP " + resp.status);
        }
        var reader = resp.body.getReader();
        var decoder = new TextDecoder();
        var buf = "";
        function pump() {
          return reader.read().then(function (r) {
            if (st.closed || !frame.isConnected) {
              reader.cancel().catch(function () {});
              return;
            }
            if (r.done) {
              // Orderly server close: reconnect (the source outlives pages).
              st.reconnects += 1;
              mirror("__logstreamReconnects", st.reconnects);
              setTimeout(open, RECONNECT_MS);
              return;
            }
            buf += decoder.decode(r.value, { stream: true });
            var nl = buf.indexOf("\n");
            while (nl !== -1) {
              var line = buf.slice(0, nl);
              buf = buf.slice(nl + 1);
              if (line.trim() !== "") {
                try { onLine(JSON.parse(line)); }
                catch (e) { mirror("__logstreamLastError", "E_BAD_JSON"); }
              }
              nl = buf.indexOf("\n");
            }
            return pump();
          });
        }
        return pump();
      })["catch"](function (e) {
        if (st.closed || (e && e.name === "AbortError")) return;
        mirror("__logstreamLastError", "E_NETWORK");
        setTimeout(open, RECONNECT_MS);
      });
    }

    // Page-level pause/resume: any control on the demo page dispatches these
    // on document. Sending stops; draining continues into the bounded buffer.
    function onDocEvent(ev) {
      if (ev.type === "logstream:pause") st.paused = true;
      else st.paused = false;
      refreshMirrors();
      if (!st.paused) maybeSend();
    }
    document.addEventListener("logstream:pause", onDocEvent);
    document.addEventListener("logstream:resume", onDocEvent);

    // If the broker tore the iframe down (SPA nav), the reader loop notices
    // via frame.isConnected on the next chunk and aborts. No listeners on
    // window outlive the page, and the document listeners die with it.

    frame.__logstreamState = st; // the streamAck case reads this
    st.onAck = onAck;
    st.open = open;
    refreshMirrors();
    open();
  }

  // --- Register with the generic platform broker ---------------------------

  host.register("logstream", {
    manifest: {
      entry:        TERM_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      schema:       SCHEMA_VERSION,
      title:        "Log stream"
    },
    config: {},
    onEvent: function (method, params, api) {
      params = params || {};
      var frame = api.iframe;
      switch (method) {
        case "ready":
          frame.__logstreamReady = true;
          frame.__logstreamProbes = params.probes || null;
          frame.__logstreamDelivered = 0;
          frame.__logstreamDropped = 0;
          frame.__logstreamInFlight = 0;
          frame.__logstreamAcks = 0;
          frame.__logstreamStats = null;
          frame.__logstreamReconnects = 0;
          startStream(api);
          break;
        case "streamAck":
          // Routed through the per-mount state the ready handler built.
          if (frame.__logstreamState && frame.__logstreamState.onAck) {
            frame.__logstreamState.onAck(params);
          }
          break;
        default:
          // themeApplied / resize / focusChanged / bootError handled generically.
          break;
      }
    }
  });
})();
