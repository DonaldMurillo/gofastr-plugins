/*!
 * genui/host/adapter.js — host-side ADAPTER for the GoFastr generative-UI
 * plugin.
 *
 * Thin adapter over the generic platform broker
 * (pluginhost/host/pluginhost.js), which owns the sandboxed-iframe creation,
 * the versioned postMessage envelope + source check, the ready→init
 * handshake, theme bridging, resize/focus/metric handling, and SPA teardown.
 *
 * This adapter is where the MODEL runs, which is the whole point of the
 * plugin's shape: the composition is produced HOST-side (POST /compose on
 * this origin) and arrives at the frame already validated by Go. The frame
 * renders a tree; it never talks to a model, holds a key, or opens a
 * socket — its CSP still says connect-src 'none'. An API key in a browser
 * is not a key, and a frame that could call a model could exfiltrate the
 * document it was composing over. So the fetch calls live HERE and only
 * here.
 *
 * The wire contract v1 (genui/assets/genui.js implements the same table):
 *
 *   host → frame
 *     init            standard; config carries {actions} (merged from
 *                     window.__gofastrGenuiConfig, published by the Go
 *                     side's config.js route) — the SAME allow-list the Go
 *                     validator enforces, so both sides refuse one
 *                     vocabulary.
 *     composePending  {id} — a generation started; render the placeholder.
 *     composition     {id, tree} — the finished, host-validated
 *                     composition. The frame validates it AGAIN before
 *                     rendering (same registry, same rules): "the host
 *                     already checked it" is exactly the assumption that
 *                     makes a second bug fatal.
 *     composeFailed   {id, error} — the generation failed validation or
 *                     the composer errored; render the failure card.
 *     teardown        standard.
 *
 *   frame → host
 *     ready           standard; params.probes carries the frame's own
 *                     isolation probes (cookieEmpty/parentBlocked/
 *                     storageBlocked), mirrored because the guarantee is
 *                     only worth anything if something checks it on every
 *                     run — the e2e asserts all three. params.registry
 *                     carries the frame's compiled-in component ids; this
 *                     adapter asserts they agree with the host's registry
 *                     (the authoritative agreement check — two copies of
 *                     one fact, asserted where they meet).
 *     renderResult    {id, ok, nodeCount?} | {id, ok: false, error} — the
 *                     frame's verdict on the last tree it was handed,
 *                     AFTER its own validation.
 *     uiAction        {action, nodeId} — the user clicked a generated
 *                     Button; the name is narrowed against the allow-list
 *                     before anything mirrors it. What the page DOES with
 *                     an action is the host's decision, never the frame's.
 *
 * MIRRORS bridge traffic onto the iframe element (the __scanner*
 * convention) so a parent-side test and the demo page can watch the claim:
 *   __genuiReady        handshake completed
 *   __genuiState        "idle" | "pending" | "ready" | "failed"
 *   __genuiLastId       id of the latest started generation
 *   __genuiRenderResult latest renderResult, narrowed
 *   __genuiLastAction   latest allow-listed action name from the frame
 *   __genuiProbes       the frame's own isolation probes, narrowed
 *
 * Everything the frame sends is NARROWED before mirroring or resolving: the
 * frame is untrusted, so strings are length-capped and the action name is
 * checked against the host's allow-list — the way scanner's adapter narrows
 * its acks.
 *
 * Load order: the generic platform broker MUST load before this adapter,
 * and the instance config.js (window.__gofastrGenuiConfig) MUST load before
 * it too. Both the demo page and genui.UIHostOption emit pluginhost.js,
 * then config.js, then this script.
 */
(function () {
  'use strict';

  var host = window.__gofastrPluginHost;
  if (!host || typeof host.register !== "function") {
    if (typeof console !== "undefined" && console.error) {
      console.error("[genui] pluginhost broker not loaded before adapter; mount aborted");
    }
    return;
  }

  // Route constants (mirror the Go plugin consts exactly).
  var FRAME_HTML_URL = "/__gofastr/plugin/genui/genui.html";
  var SCHEMA_VERSION = "genui-v1";
  var MIN_HEIGHT = "360px";

  var DEFAULT_CAPS = ["genui:compose", "theme:read"];

  var COMPOSE_URL = "/__gofastr/plugin/genui/compose";
  var COMPOSITION_URL = "/__gofastr/plugin/genui/composition/";

  // Poll pacing: fast enough that a finished generation looks instant,
  // slow enough that a demo page never hammers the route.
  var POLL_MS = 250;
  // How long to keep polling a generation before giving up. The fixture
  // composer answers instantly; a real model client behind the same Composer
  // interface will not, and a poll with no deadline is a page that spins
  // forever when the far end never answers.
  var POLL_TIMEOUT_MS = 60000;
  var TEXT_CAP = 200;   // host-side cap on any frame-sourced string
  var PROMPT_CAP = 2000; // mirrors the Go route's rune cap

  // The component vocabulary the Go registry also declares
  // (genui.DefaultRegistry). Used to narrow renderResult diagnostics and
  // pinned against the Go side by plugin_test.go — two copies of one fact,
  // asserted so they cannot drift.
  var KNOWN_COMPONENTS = ["Stack", "Card", "Heading", "Text", "Stat", "Badge", "Table", "Button"];

  // --- instance config (config.js global), narrowed before use -----------

  var cfg = (window.__gofastrGenuiConfig &&
    typeof window.__gofastrGenuiConfig === "object")
    ? window.__gofastrGenuiConfig
    : {};

  // The action allow-list this instance enforces. The Go validator refuses
  // any composition naming an action outside it, and this table narrows
  // what the frame may report back — one vocabulary, both sides.
  var KNOWN_ACTIONS = {};
  if (typeof cfg.actions === "object" && cfg.actions !== null) {
    for (var i = 0; i < cfg.actions.length; i++) {
      if (typeof cfg.actions[i] === "string" && cfg.actions[i]) {
        KNOWN_ACTIONS[cfg.actions[i]] = true;
      }
    }
  }

  // --- page-level state (one generation pipeline, per the header) --------

  var frames = [];       // live genui iframe apis, oldest first
  var state = "idle";    // "idle" | "pending" | "ready" | "failed"
  var gen = 0;           // generation counter; supersede = gen++
  var lastId = "";
  var lastComposition = null;
  var renderResult = null;
  var lastAction = "";

  function mirrorAll(name, value) {
    for (var i = 0; i < frames.length; i++) {
      frames[i].iframe[name] = value;
    }
  }

  function setState(next) {
    state = next;
    mirrorAll("__genuiState", state);
  }

  function pruneFrames() {
    var before = frames.length;
    frames = frames.filter(function (api) { return api.iframe.isConnected; });
    if (frames.length === before) return;
    // Unlike scanner there is no camera to stop when the last frame goes;
    // an in-flight poll simply finds no frame to push to and resolves to
    // the page alone.
  }

  // --- pushing down the bridge ---------------------------------------------

  function pushEvent(method, params) {
    pruneFrames();
    for (var i = 0; i < frames.length; i++) {
      try {
        frames[i].sendEvent(method, params);
      } catch (e) {
        // The iframe went away between checks (SPA nav race); stop quietly.
      }
    }
  }

  // --- narrowing (the frame is untrusted; the route JSON is ours) ---------

  function narrowError(s) {
    return typeof s === "string" ? s.slice(0, TEXT_CAP) : "";
  }

  function narrowRenderResult(params) {
    params = params || {};
    return {
      id: typeof params.id === "string" ? params.id.slice(0, 64) : "",
      ok: params.ok === true,
      nodeCount: typeof params.nodeCount === "number" && isFinite(params.nodeCount)
        ? params.nodeCount
        : 0,
      error: narrowError(params.error)
    };
  }

  function narrowProbes(params) {
    if (!params || typeof params !== "object") return null;
    return {
      cookieEmpty: params.cookieEmpty === true,
      parentBlocked: params.parentBlocked === true,
      storageBlocked: params.storageBlocked === true
    };
  }

  // narrowTree structurally checks what the HOST route returned. The tree
  // was validated host-side before storage; this refuses garbage shapes
  // (a proxy, a truncated body) rather than forwarding them into the cage.
  function narrowTree(tree) {
    if (!tree || typeof tree !== "object") return null;
    if (tree.schemaVersion !== SCHEMA_VERSION) return null;
    if (!tree.root || typeof tree.root !== "object") return null;
    return { schemaVersion: tree.schemaVersion, root: tree.root };
  }

  // --- the compose pipeline -------------------------------------------------

  function failGeneration(myGen, message) {
    if (myGen !== gen) return; // superseded; the newest prompt owns the state
    setState("failed");
    pushEvent("composeFailed", { id: lastId, error: message });
  }

  function pollComposition(id, myGen, resolve, reject, deadline) {
    if (myGen !== gen) return; // superseded; stop quietly, settle nothing
    fetch(COMPOSITION_URL + encodeURIComponent(id), {
      headers: { Accept: "application/json" }
    }).then(function (resp) {
      if (!resp.ok) throw new Error("E_POLL_HTTP_" + resp.status);
      return resp.json();
    }).then(function (body) {
      if (myGen !== gen) return; // superseded mid-poll
      var st = body && typeof body.state === "string" ? body.state : "";
      if (st === "pending") {
        if (Date.now() >= deadline) {
          failGeneration(myGen, "E_TIMEOUT");
          reject(new Error("E_TIMEOUT"));
          return;
        }
        setTimeout(function () {
          pollComposition(id, myGen, resolve, reject, deadline);
        }, POLL_MS);
        return;
      }
      if (st === "ready") {
        var tree = narrowTree(body.tree);
        if (!tree) {
          failGeneration(myGen, "E_BAD_TREE");
          reject(new Error("E_BAD_TREE"));
          return;
        }
        lastComposition = tree;
        setState("ready");
        pushEvent("composition", { id: lastId, tree: tree });
        resolve(tree);
        return;
      }
      if (st === "failed") {
        var msg = narrowError(body.error) || "E_COMPOSE_FAILED";
        failGeneration(myGen, msg);
        reject(new Error(msg));
        return;
      }
      failGeneration(myGen, "E_BAD_STATE");
      reject(new Error("E_BAD_STATE"));
    }).catch(function (err) {
      if (myGen !== gen) return; // superseded; the error belongs to nobody
      failGeneration(myGen, (err && err.message) || "E_POLL_FAILED");
      reject(err instanceof Error ? err : new Error("E_POLL_FAILED"));
    });
  }

  /**
   * Start a generation for prompt. Resolves with the finished composition
   * tree, rejects with an Error (E_* code in .message) when the route
   * refuses, the composer fails, or polling times out. A new call
   * supersedes any in-flight generation: the old poll stops quietly and
   * its result is dropped, so the frame's placeholder always matches the
   * newest prompt.
   */
  function compose(prompt) {
    if (typeof prompt !== "string") {
      return Promise.reject(new Error("E_BAD_PROMPT"));
    }
    var p = prompt.trim();
    if (!p) return Promise.reject(new Error("E_BAD_PROMPT"));
    if (p.length > PROMPT_CAP) return Promise.reject(new Error("E_PROMPT_TOO_LONG"));

    var myGen = ++gen;
    return fetch(COMPOSE_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt: p })
    }).then(function (resp) {
      if (!resp.ok) {
        // 403 capability denial, 400 bad prompt, anything else: all land
        // in the failed state with the status encoded, the same way the
        // camera adapter treats its failures as states, not exceptions.
        throw new Error("E_COMPOSE_HTTP_" + resp.status);
      }
      return resp.json();
    }).then(function (body) {
      if (myGen !== gen) {
        throw new Error("E_SUPERSEDED");
      }
      if (!body || typeof body.id !== "string" || !body.id) {
        throw new Error("E_BAD_ID");
      }
      lastId = body.id;
      mirrorAll("__genuiLastId", lastId);
      setState("pending");
      pushEvent("composePending", { id: lastId });
      return new Promise(function (resolve, reject) {
        pollComposition(body.id, myGen, resolve, reject, Date.now() + POLL_TIMEOUT_MS);
      });
    });
  }

  // --- the demo/e2e surface ---------------------------------------------------

  /**
   * Post a composition straight to the frame, skipping the Go validator.
   *
   * A test seam, deliberately exposed. The plugin validates twice on purpose —
   * Go before storing, the frame before rendering — and the second validator is
   * only worth having if something exercises it. Nothing else can: every tree
   * that reaches the frame through compose() has already passed Go, so the
   * frame's own rules would be dead code that nobody ever ran.
   *
   * It grants nothing. A host page can already postMessage to a frame it
   * mounted; what this proves is that doing so gets you refused.
   */
  function pushRawComposition(tree) {
    setState("pending");
    for (var i = 0; i < frames.length; i++) {
      try {
        frames[i].sendEvent("composition", { id: "raw-" + (rawCounter += 1), tree: tree });
      } catch (e) {
        /* frame went away; pruneFrames handles it */
      }
    }
  }
  var rawCounter = 0;

  window.__gofastrGenuiDemo = {
    pushRawComposition: pushRawComposition,
    compose: compose,
    lastComposition: function () { return lastComposition; },
    state: function () { return state; },
    lastId: function () { return lastId; }
  };

  // --- Register with the generic platform broker -----------------------------

  host.register("genui", {
    manifest: {
      entry:        FRAME_HTML_URL,
      isolation:    "sandbox-iframe-opaque",
      sandbox:      ["allow-scripts"],
      capabilities: DEFAULT_CAPS,
      minHeight:    MIN_HEIGHT,
      schema:       SCHEMA_VERSION,
      title:        "Generative UI"
    },
    // Merge the instance config the Go side publishes via config.js (the
    // action allow-list). The generic broker bridges this verbatim as
    // init.config, which is how the frame learns the vocabulary for THIS
    // mount. {} is the safe default if config.js did not load (the frame
    // then accepts no action at all).
    config: cfg,
    onEvent: function (method, params, api) {
      params = params || {};
      switch (method) {
        case "ready":
          var fresh = frames.indexOf(api) === -1;
          if (fresh) frames.push(api);
          api.iframe.__genuiReady = true;
          // Bring the new mirror up to date with page-level state.
          api.iframe.__genuiState = state;
          api.iframe.__genuiLastId = lastId;
          api.iframe.__genuiRenderResult = renderResult;
          api.iframe.__genuiLastAction = lastAction;
          // The frame's own isolation probes, computed inside the opaque
          // origin at boot. Mirrored like every other plugin's, because the
          // guarantee is only worth anything if something checks it on
          // every run — the e2e asserts all three.
          var probes = narrowProbes(params.probes);
          if (probes) api.iframe.__genuiProbes = probes;
          // The authoritative registry agreement check: the frame announces
          // its compiled-in component ids on ready, and this adapter asserts
          // they match the host's KNOWN_COMPONENTS — two copies of one fact,
          // asserted where they meet. A disagreement is mirrored AND logged:
          // every composition is validated against BOTH tables, so skew
          // here means trees will be refused on one side only.
          if (Array.isArray(params.registry)) {
            var agree = params.registry.length === KNOWN_COMPONENTS.length;
            if (agree) {
              var have = {};
              for (var k = 0; k < params.registry.length; k++) {
                if (typeof params.registry[k] !== "string") { agree = false; break; }
                have[params.registry[k]] = true;
              }
              for (var c = 0; agree && c < KNOWN_COMPONENTS.length; c++) {
                if (!have[KNOWN_COMPONENTS[c]]) agree = false;
              }
            }
            api.iframe.__genuiRegistryAgrees = agree;
            if (!agree && typeof console !== "undefined" && console.error) {
              console.error("[genui] frame registry disagrees with host registry: " +
                JSON.stringify(params.registry) + " vs " + JSON.stringify(KNOWN_COMPONENTS));
            }
          }
          break;
        case "renderResult": {
          // The frame's verdict AFTER its own validation of the last tree.
          // Narrowed: an arbitrary error string from the frame must not
          // reach the page unbounded.
          renderResult = narrowRenderResult(params);
          mirrorAll("__genuiRenderResult", renderResult);
          // The frame's verdict is the state. The host knows whether it SENT a
          // composition; only the frame knows whether it rendered one, and a
          // refusal is the outcome this plugin most needs to be visible. Host
          // vocabulary (idle/pending/ready/failed) stops at "ready" — the frame
          // resolves that into rendered or refused.
          setState(renderResult.ok ? "rendered" : "refused");
          break;
        }
        case "uiAction": {
          // A generated Button was clicked; the frame emits the validated
          // action name plus the nodeId of the control that fired. Narrowed
          // against the host's allow-list: a frame-sourced name the host
          // never named is dropped, not mirrored.
          var act = typeof params.action === "string" ? params.action.slice(0, TEXT_CAP) : "";
          var nid = typeof params.nodeId === "string" ? params.nodeId.slice(0, TEXT_CAP) : "";
          if (act && KNOWN_ACTIONS[act] === true) {
            lastAction = act;
            mirrorAll("__genuiLastAction", lastAction);
            mirrorAll("__genuiLastNodeId", nid);
          }
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
