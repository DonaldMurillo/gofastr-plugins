/*!
 * scanner/host/adapter.js — host-side ADAPTER for the GoFastr barcode
 * scanner.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init
 * handshake, theme bridging, resize/focus/metric handling, and SPA teardown.
 *
 * This adapter is where the camera lives, which is the whole point of the
 * plugin's shape: a camera is UNREACHABLE from an opaque-origin frame —
 * getUserMedia fails there with "SecurityError: Invalid security origin"
 * under sandbox="allow-scripts", and allow="camera" does not change it
 * (measured; the permission is bound to a real origin the frame cannot hold
 * without de-opaquing). So THIS page owns the MediaStream — the permission
 * prompt appears against the host's origin, where a user can reason about
 * it — and pushes PIXELS down the bridge; the cage decodes and returns
 * strings. Pixels never cross in the other direction, and the frame still
 * cannot open a socket.
 *
 * The wire contract v1 (scanner/assets/scan.js implements the same table):
 *
 *   host → frame
 *     init        standard; config carries {formats, scanRateHz} (merged
 *                 from window.__gofastrScannerConfig, published by the Go
 *                 side's config.js route)
 *     scanFrame   {seq, width, height, gray} — gray is GRAYSCALE LUMINANCE,
 *                 exactly width*height bytes. NOT RGBA: handing zxing an
 *                 RGBA buffer fails inside MultiFormatReader with "No
 *                 MultiFormat Readers were able to detect the code", which
 *                 reads like a bad image rather than a bad call, so the
 *                 mistake is invisible until someone measures it. (Measured.)
 *     scanSample  {} — the frame generates a QR itself, draws it, decodes
 *                 it; how the demo works with no camera and no image assets.
 *     teardown    standard.
 *
 *   frame → host
 *     scanResult  {seq, text, format, decodeMs} on a successful decode.
 *     frameDone   {seq, decoded} after EVERY scanFrame, decoded or not —
 *                 this is the flow-control ack (see below).
 *     scanStats   {framesSeen, decodes, lastDecodeMs, lastText} at most
 *                 every 500ms, for the mirrors.
 *
 * FLOW CONTROL: at most ONE scanFrame in flight. A frame with no code in it
 * is the NORMAL case at 8 Hz — NotFoundException is the frame's everyday
 * path, never an error — so the bridge must never queue frames behind a
 * slow decode. The next frame is sent only after its frameDone ack, the
 * same one-slot backpressure logstream's adapter generalises to four. The
 * capture timer ticks at scanRateHz and simply SKIPS a tick when the slot
 * is occupied, so a slow decode degrades the effective rate instead of
 * growing a queue of stale pixels (a stale frame is worthless: the code has
 * usually moved).
 *
 * CAMERA FAILURE IS A STATE, NOT AN EXCEPTION: NotAllowedError (permission
 * refused), NotFoundError (no device — most desktops and every CI runner),
 * and a missing navigator.mediaDevices all land in the four-valued
 * __scannerCameraState the page renders ("idle" | "live" | "denied" |
 * "unsupported"); the raw error name rides __scannerCameraError for
 * diagnostics. The plugin is fully usable without a camera via
 * scanSample() and scanImageFile().
 *
 * MIRRORS bridge traffic onto the iframe element (the __logstream*
 * convention) so a parent-side test and the demo page can watch the claim:
 *   __scannerReady          handshake completed
 *   __scannerCameraState    "idle" | "live" | "denied" | "unsupported"
 *   __scannerCameraError    last camera error name ("" when none)
 *   __scannerFramesSent     scanFrame events pushed this session
 *   __scannerStats          latest scanStats, narrowed
 *   __scannerLastResult     latest scanResult, narrowed
 *
 * Everything the frame sends is NARROWED before mirroring or resolving: the
 * frame is untrusted, so text is length-capped at 200 chars and format is
 * checked against the known zxing BarcodeFormat list — the way logstream's
 * adapter narrows its ack.
 *
 * One decode pipeline per page: the camera is a page-level singleton, so
 * the loop targets the FIRST ready scanner frame and the mirrors ride every
 * live one. Two mounts would share one camera and one decoder; that is the
 * boring, honest behaviour.
 *
 * Load order: the generic platform broker MUST load before this adapter,
 * and the instance config.js (window.__gofastrScannerConfig) MUST load
 * before it too. Both the demo page and scanner.UIHostOption emit
 * pluginhost.js, then config.js, then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[scanner] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly).
  var FRAME_HTML_URL = "/__gofastr/plugin/scanner/scan.html";
  var SCHEMA_VERSION = "scanner-v1";
  var MIN_HEIGHT = "460px";

  var DEFAULT_CAPS = ["scan:decode", "theme:read"];

  // The zxing BarcodeFormat vocabulary the Go side also validates against
  // (scanner.knownFormats). scanResult.format is narrowed with this table:
  // an unknown name from the frame is kept as "" rather than mirrored.
  var KNOWN_FORMATS = {
    "AZTEC": true, "CODABAR": true, "CODE_39": true, "CODE_93": true,
    "CODE_128": true, "DATA_MATRIX": true, "EAN_8": true, "EAN_13": true,
    "ITF": true, "PDF_417": true, "QR_CODE": true, "UPC_A": true, "UPC_E": true
  };

  // --- instance config (config.js global), narrowed before use -----------

  var cfg = (window.__gofastrScannerConfig &&
    typeof window.__gofastrScannerConfig === "object")
    ? window.__gofastrScannerConfig
    : {};

  var SCAN_RATE_HZ =
    (typeof cfg.scanRateHz === "number" && isFinite(cfg.scanRateHz) &&
      cfg.scanRateHz >= 1 && cfg.scanRateHz <= 30)
      ? cfg.scanRateHz
      : 8;

  // --- capture constants ---------------------------------------------------

  // Fixed working size every scanFrame is captured at. Bounded and square-
  // ish on purpose: decode cost scales with pixels, and 480x360 rides well
  // under the ~30ms worst-case measured decode for a 300x300 QR.
  var WORK_W = 480;
  var WORK_H = 360;

  var TEXT_CAP = 200;        // host-side cap on any frame-sourced string
  var FILE_TIMEOUT_MS = 10000; // a file scan that never acks resolves null
  var SAMPLE_TIMEOUT_MS = 5000;
  var FILE_QUEUE_MAX = 8;    // bounded: one per <input type=file> pick, drops oldest

  // --- shared capture pipeline (camera frames and file frames both pass) --

  var workCanvas = document.createElement("canvas");
  workCanvas.width = WORK_W;
  workCanvas.height = WORK_H;
  var workCtx = workCanvas.getContext("2d", { willReadFrequently: true });

  // drawContain paints src (a <video>, <img>, or ImageBitmap, with its own
  // natural sw x sh) into the working canvas CONTAIN-fit, letterboxed on
  // black. Aspect is preserved because stretching is not free for barcodes:
  // an anamorphic 16:9→4:3 pull scales x and y differently, which warps 1D
  // row geometry and QR's nominally-square grid enough to shed decodes.
  function drawContain(src, sw, sh) {
    workCtx.fillStyle = "#000";
    workCtx.fillRect(0, 0, WORK_W, WORK_H);
    if (!sw || !sh) return false; // video not ready yet — skip this tick
    var scale = Math.min(WORK_W / sw, WORK_H / sh);
    var w = sw * scale;
    var h = sh * scale;
    workCtx.drawImage(src, (WORK_W - w) / 2, (WORK_H - h) / 2, w, h);
    return true;
  }

  // toGray converts the working canvas to the scanFrame payload: grayscale
  // LUMINANCE, exactly WORK_W*WORK_H bytes, one per pixel.
  //
  // NOT RGBA — this is the load-bearing line of the wire contract. zxing's
  // RGBLuminanceSource treats its input as R,G,B,R,G,B… regardless of what
  // the caller meant, so an RGBA buffer "works" mechanically and then fails
  // inside MultiFormatReader with "No MultiFormat Readers were able to
  // detect the code" — a lookup failure that reads like a bad image rather
  // than a bad call. (Measured.) The conversion therefore lives HERE, in
  // exactly one place, on the only side that ever sees RGBA.
  //
  // Weights are ITU-R BT.601 in integer form (306/601/117 ≈ 0.299/0.587/
  // 0.114, sum exactly 1024), matching what zxing itself uses when IT is
  // handed RGB — so a camera frame and the same pixels decoded as RGB agree.
  function toGray() {
    var rgba = workCtx.getImageData(0, 0, WORK_W, WORK_H).data;
    var n = WORK_W * WORK_H;
    var gray = new Uint8ClampedArray(n);
    for (var i = 0, p = 0; p < n; i += 4, p++) {
      gray[p] = (306 * rgba[i] + 601 * rgba[i + 1] + 117 * rgba[i + 2]) >> 10;
    }
    return gray;
  }

  // --- page-level state (one camera, one decode slot, per the header) -----

  var frames = [];        // live scanner iframe apis, oldest first
  var camera = {
    stream: null,
    video: null,
    state: "idle",        // "idle" | "live" | "denied" | "unsupported"
    errName: ""
  };
  var tickTimer = null;
  var seqCounter = 0;
  var inFlight = 0;       // seq of the one unacked scanFrame, 0 = free
  var fileQueue = [];     // [{payload, resolve, timer}] waiting for the slot
  var pendingFiles = Object.create(null); // seq -> {resolve} once sent
  var sampleWaiter = null; // {resolve, timer} while a scanSample is out
  var framesSent = 0;
  var lastResult = null;
  var lastStats = null;

  function mirrorAll(name, value) {
    for (var i = 0; i < frames.length; i++) {
      frames[i].iframe[name] = value;
    }
  }

  function setCameraState(state, errName) {
    camera.state = state;
    camera.errName = errName || "";
    mirrorAll("__scannerCameraState", state);
    mirrorAll("__scannerCameraError", camera.errName);
  }

  // Drop apis whose iframe left the DOM (SPA nav tore the frame down). If
  // the frame the pipeline was targeting died, stop the camera too — a lit
  // camera indicator with no consumer is its own bug.
  function pruneFrames() {
    var before = frames.length;
    frames = frames.filter(function (api) { return api.iframe.isConnected; });
    if (frames.length === before) return;
    if (frames.length === 0) {
      teardownPipeline();
    }
  }

  function teardownPipeline() {
    stopTick();
    if (camera.state === "live") stopCamera();
    settleAllFiles(null);
    if (sampleWaiter) settleSample(null);
  }

  // --- the one-slot send path ----------------------------------------------

  function send(payload, entry) {
    var api = frames[0];
    if (!api) return false;
    var seq = ++seqCounter;
    inFlight = seq;
    if (entry) {
      pendingFiles[seq] = entry;
    }
    try {
      api.sendEvent("scanFrame", {
        seq: seq,
        width: payload.width,
        height: payload.height,
        gray: payload.gray
      });
    } catch (e) {
      // The iframe went away between checks (SPA nav race): stop quietly.
      inFlight = 0;
      if (entry) settleFile(seq, null);
      pruneFrames();
      return false;
    }
    framesSent += 1;
    mirrorAll("__scannerFramesSent", framesSent);
    return true;
  }

  // pump fills the single bridge slot: a queued FILE frame wins over the
  // camera (it is a one-shot the user asked for; camera frames are a
  // stream), and the camera only grabs when the slot is free anyway.
  function pump() {
    if (inFlight !== 0) return;
    while (fileQueue.length > 0) {
      var entry = fileQueue.shift();
      if (entry.settled) continue; // timed out while queued
      if (send(entry.payload, entry)) return;
      settleEntry(entry, null);
      return;
    }
    if (camera.state === "live" && camera.video &&
      drawContain(camera.video, camera.video.videoWidth, camera.video.videoHeight)) {
      send({ width: WORK_W, height: WORK_H, gray: toGray() }, null);
    }
  }

  function stopTick() {
    if (tickTimer !== null) {
      clearTimeout(tickTimer);
      tickTimer = null;
    }
  }

  // The capture metronome: tick at scanRateHz, skip when the slot is busy.
  // A slow decode therefore lowers the effective rate (correct: stale
  // frames are worthless) instead of queueing behind itself.
  function scheduleTick() {
    if (tickTimer === null && camera.state === "live") {
      tickTimer = setTimeout(function () {
        tickTimer = null;
        pruneFrames();
        pump();
        scheduleTick();
      }, Math.floor(1000 / SCAN_RATE_HZ));
    }
  }

  // --- file scans (the deterministic e2e journey) ---------------------------

  function loadBitmap(file) {
    if (!file || typeof file !== "object") {
      return Promise.reject(new Error("E_NOT_A_FILE"));
    }
    if (typeof createImageBitmap === "function") {
      return createImageBitmap(file);
    }
    // Fallback for engines without createImageBitmap-from-Blob.
    return new Promise(function (resolve, reject) {
      var url = URL.createObjectURL(file);
      var img = new Image();
      img.onload = function () { URL.revokeObjectURL(url); resolve(img); };
      img.onerror = function () { URL.revokeObjectURL(url); reject(new Error("E_BAD_IMAGE")); };
      img.src = url;
    });
  }

  function settleEntry(entry, value) {
    if (entry.settled) return;
    entry.settled = true;
    if (entry.timer !== null) clearTimeout(entry.timer);
    entry.resolve(value);
  }

  function settleFile(seq, value) {
    var entry = pendingFiles[seq];
    if (!entry) return;
    delete pendingFiles[seq];
    settleEntry(entry, value);
  }

  function settleAllFiles(value) {
    for (var seq in pendingFiles) {
      if (Object.prototype.hasOwnProperty.call(pendingFiles, seq)) {
        settleFile(Number(seq), value);
      }
    }
    while (fileQueue.length > 0) settleEntry(fileQueue.shift(), value);
  }

  // scanImageFile pushes a File/Blob down the SAME path as a camera frame:
  // drawn to the same working canvas, converted to the same grayscale
  // payload, sent as the same scanFrame event, flow-controlled by the same
  // single slot. Resolves with the narrowed scanResult for that file, or
  // null when no code was found / the frame never answered — which is what
  // makes the e2e journey deterministic on any engine.
  function scanImageFile(file) {
    pruneFrames();
    if (frames.length === 0) {
      return Promise.reject(new Error("E_NO_FRAME"));
    }
    return loadBitmap(file).then(function (src) {
      return new Promise(function (resolve) {
        drawContain(src, src.naturalWidth || src.width, src.naturalHeight || src.height);
        var entry = {
          payload: { width: WORK_W, height: WORK_H, gray: toGray() },
          resolve: resolve,
          timer: null,
          settled: false
        };
        entry.timer = setTimeout(function () {
          settleEntry(entry, null);
        }, FILE_TIMEOUT_MS);
        fileQueue.push(entry);
        if (fileQueue.length > FILE_QUEUE_MAX) {
          // Bounded queue, drop OLDEST (pathological multi-pick only; the
          // demo and e2e send one at a time).
          settleEntry(fileQueue.shift(), null);
        }
        pump();
      });
    });
  }

  // --- the sample path (no camera, no assets) ------------------------------

  function settleSample(value) {
    if (!sampleWaiter) return;
    var w = sampleWaiter;
    sampleWaiter = null;
    clearTimeout(w.timer);
    w.resolve(value);
  }

  function scanSample() {
    pruneFrames();
    if (frames.length === 0) {
      return Promise.reject(new Error("E_NO_FRAME"));
    }
    if (sampleWaiter) return sampleWaiter.promise;
    var waiter = {};
    waiter.promise = new Promise(function (resolve) {
      waiter.resolve = resolve;
    });
    sampleWaiter = waiter;
    sampleWaiter.timer = setTimeout(function () { settleSample(null); }, SAMPLE_TIMEOUT_MS);
    try {
      frames[0].sendEvent("scanSample", {});
    } catch (e) {
      settleSample(null);
      pruneFrames();
    }
    return waiter.promise;
  }

  // --- the camera ------------------------------------------------------------

  function startCamera() {
    if (camera.state === "live") {
      return Promise.resolve(camera.state);
    }
    // A denied/unsupported state does NOT short-circuit a retry: a dismissed
    // (not refused) prompt re-prompts on the next call, and a hard denial
    // rejects immediately again — the browser arbitrates, not this adapter.
    // Either way the landed state stays mirrored for the page to render.
    if (!navigator.mediaDevices || typeof navigator.mediaDevices.getUserMedia !== "function") {
      setCameraState("unsupported", "mediaDevices unavailable");
      var uerr = new Error("E_CAMERA_UNSUPPORTED");
      uerr.state = "unsupported";
      return Promise.reject(uerr);
    }
    // The HOST page owns the stream, so the prompt appears against THIS
    // origin where the user can reason about it.
    return navigator.mediaDevices
      .getUserMedia({ video: { facingMode: "environment" } })
      .then(function (stream) {
        camera.stream = stream;
        var video = document.createElement("video");
        // Detached, muted, playsinline: never visible, never audible, and
        // drawable to a canvas on every engine this repo tests.
        video.muted = true;
        video.setAttribute("playsinline", "");
        video.srcObject = stream;
        camera.video = video;
        return video.play().then(function () {
          setCameraState("live", "");
          pruneFrames();
          pump();
          scheduleTick();
          return camera.state;
        });
      })
      .catch(function (err) {
        // Permission refused vs everything else (no device, busy, insecure
        // context): NotAllowedError/SecurityError mean the user or context
        // said no; the rest are all "camera scanning unavailable here",
        // which the page renders from the state + the mirrored raw name.
        var denied = err && (err.name === "NotAllowedError" ||
          err.name === "PermissionDeniedError" ||
          err.name === "SecurityError");
        setCameraState(denied ? "denied" : "unsupported", (err && err.name) || "UnknownError");
        var out = new Error("E_CAMERA_" + camera.state.toUpperCase());
        out.state = camera.state;
        out.name = camera.errName;
        throw out;
      });
  }

  function stopCamera() {
    if (camera.stream) {
      camera.stream.getTracks().forEach(function (t) { t.stop(); });
      camera.stream = null;
    }
    if (camera.video) {
      try { camera.video.pause(); } catch (e) { /* already gone */ }
      camera.video.srcObject = null;
      camera.video = null;
    }
    if (camera.state === "live") {
      setCameraState("idle", "");
    }
    stopTick();
  }

  // --- narrowing (the frame is untrusted) ------------------------------------

  function narrowResult(params) {
    params = params || {};
    return {
      seq: typeof params.seq === "number" ? params.seq : 0,
      text: typeof params.text === "string" ? params.text.slice(0, TEXT_CAP) : "",
      format: typeof params.format === "string" && KNOWN_FORMATS[params.format] === true
        ? params.format
        : "",
      decodeMs: typeof params.decodeMs === "number" ? params.decodeMs : 0,
      // Which decoder read it. Narrowed to the two names the frame may claim:
      // the value drives what a page tells a user about coverage, so an
      // arbitrary string from the frame must not reach it.
      via: params.via === "native" || params.via === "zxing" ? params.via : ""
    };
  }

  function narrowStats(params) {
    params = params || {};
    return {
      framesSeen: typeof params.framesSeen === "number" ? params.framesSeen : 0,
      decodes: typeof params.decodes === "number" ? params.decodes : 0,
      lastDecodeMs: typeof params.lastDecodeMs === "number" ? params.lastDecodeMs : 0,
      // Capped by the HOST, never trusted from the frame.
      lastText: typeof params.lastText === "string" ? params.lastText.slice(0, TEXT_CAP) : ""
    };
  }

  // --- the demo/e2e surface ---------------------------------------------------

  // Which decoders the FRAME's engine has, as reported in its ready event.
  // The frame owns this answer: the host page and the frame can be different
  // engines only in theory, but the decoder lives in the frame, so the frame
  // is the one that knows.
  var decoders = { native: false, zxing: true };
  var decoderPref = "auto";

  /**
   * Force a decoder path in the frame.
   *
   * A test seam, deliberately exposed rather than hidden: the two decoders do
   * not read the same set of codes (zxing's JS port cannot read some valid QR
   * codes its own encoder produces), and each engine ships only one of them —
   * CI's Linux chromium has no BarcodeDetector, a mac does. Without a way to
   * force each path, every run would exercise exactly one and the other would
   * rot unnoticed.
   */
  function forceDecoder(which) {
    decoderPref = which === "native" || which === "zxing" ? which : "auto";
    // Every mounted frame, not just the first: the page may hold several and a
    // test that forces a decoder must not leave one of them on the other path.
    for (var i = 0; i < frames.length; i++) {
      try {
        frames[i].sendEvent("setDecoder", { which: decoderPref });
      } catch (e) {
        /* frame went away; the next register brings it up to date */
      }
    }
    return decoderPref;
  }

  window.__gofastrScannerDemo = {
    startCamera: startCamera,
    stopCamera: stopCamera,
    scanSample: scanSample,
    scanImageFile: scanImageFile,
    forceDecoder: forceDecoder,
    decoderAvailability: function () { return { native: decoders.native, zxing: decoders.zxing }; },
    decoder: function () { return decoderPref; },
    lastResult: function () { return lastResult; }
  };

  // --- Register with the generic platform broker -----------------------------

  host.register("scanner", {
    manifest: {
      entry:        FRAME_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      minHeight:    MIN_HEIGHT,
      schema:       SCHEMA_VERSION,
      title:        "Barcode scanner"
    },
    // Merge the instance config the Go side publishes via config.js
    // (formats + scanRateHz). The generic broker bridges this verbatim as
    // init.config, which is how the frame learns what to decode for THIS
    // mount. {} is the safe default if config.js did not load (the frame
    // then falls back to its own built-in defaults).
    config: cfg,
    onEvent: function (method, params, api) {
      params = params || {};
      switch (method) {
        case "ready":
          var fresh = frames.indexOf(api) === -1;
          if (fresh) frames.push(api);
          api.iframe.__scannerReady = true;
          // Bring the new mirror up to date with page-level state.
          api.iframe.__scannerCameraState = camera.state;
          api.iframe.__scannerCameraError = camera.errName;
          api.iframe.__scannerFramesSent = framesSent;
          api.iframe.__scannerStats = lastStats;
          api.iframe.__scannerLastResult = lastResult;
          // The frame reports which decoders its engine has. Narrowed to
          // booleans: this drives what the page claims about coverage.
          if (params && typeof params.decoders === "object" && params.decoders) {
            decoders = { native: params.decoders.native === true, zxing: params.decoders.zxing !== false };
          }
          mirrorAll("__scannerDecoders", { native: decoders.native, zxing: decoders.zxing });
          // The frame's own isolation probes, computed inside the opaque
          // origin at boot. Mirrored like every other plugin's, because the
          // guarantee is only worth anything if something checks it on every
          // run — the e2e asserts all three.
          if (params && typeof params.probes === "object" && params.probes) {
            api.iframe.__scannerProbes = {
              cookieEmpty: params.probes.cookieEmpty === true,
              parentBlocked: params.probes.parentBlocked === true,
              storageBlocked: params.probes.storageBlocked === true
            };
          }
          break;
        case "decoderChanged":
          if (params && typeof params.decoders === "object" && params.decoders) {
            decoders = { native: params.decoders.native === true, zxing: params.decoders.zxing !== false };
          }
          mirrorAll("__scannerDecoders", { native: decoders.native, zxing: decoders.zxing });
          break;
        case "scanResult": {
          var res = narrowResult(params);
          lastResult = res;
          mirrorAll("__scannerLastResult", res);
          // A file scan resolves on ITS seq; the sample waiter takes any
          // result no file claimed (camera results match the in-flight
          // camera seq, so they never satisfy a waiting sample).
          if (res.seq !== 0 && pendingFiles[res.seq]) {
            settleFile(res.seq, res);
          } else if (sampleWaiter && res.seq !== inFlight) {
            settleSample(res);
          }
          break;
        }
        case "frameDone": {
          var seq = typeof params.seq === "number" ? params.seq : 0;
          if (seq === inFlight) {
            inFlight = 0;
            pruneFrames();
            pump(); // release the slot: send the next frame now
          }
          // An UNDECODED file frame settles null — no code found. A decoded
          // one stays for its scanResult, which may arrive after this ack
          // (the contract only orders frameDone after the frame, not after
          // scanResult); if it never comes, the entry's own timer decides.
          if (seq !== 0 && params.decoded !== true && pendingFiles[seq]) {
            settleFile(seq, null);
          }
          break;
        }
        case "scanStats": {
          lastStats = narrowStats(params);
          mirrorAll("__scannerStats", lastStats);
          break;
        }
        default:
          // themeApplied / resize / focusChanged / metric / bootError
          // handled generically by the broker.
          break;
      }
    }
  });
})();
