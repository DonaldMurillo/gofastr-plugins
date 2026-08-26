// gofastr-plugins/posthog — the page bootstrap.
//
// Rendered once per app at construction (posthog.go): the config
// token below (the CFG assignment) is replaced by the encoding/json
// encoding of this instance's config. The token's name must appear
// NOWHERE else in this file: rendering replaces the first occurrence
// only, and a mention in a comment would swallow the replacement —
// which is exactly the bug the first live run of this file had. Go's JSON encoder
// HTML-escapes <, > and &, which is what keeps a hostile value inside
// the config inert in any HTML context.
//
// In order: load the real posthog-js array.js loader through the
// relay's assets route (never the vendor origin); init it pointed at
// the relay's ingestion route; resolve identity from the same-origin
// whoami endpoint and keep it current across navigations; fire the
// initial $pageview after identity; then one $pageview per completed
// client-side navigation.
(function () {
  'use strict';
  var CFG = __GOFASTR_POSTHOG_CONFIG__;

  var INGEST = CFG.mount + '/ph';        // relay → {region}.i.posthog.com
  var ASSETS = CFG.mount + '/ph-assets'; // relay → {region}-assets.i.posthog.com

  // RespectDNT: a visitor who opted out of tracking gets no script, no
  // beacons, nothing.
  if (CFG.respectDNT) {
    var dnt = navigator.doNotTrack || window.doNotTrack || '';
    if (dnt === '1' || dnt === 'yes') return;
  }

  var s = document.createElement('script');
  s.src = ASSETS + '/static/array.js'; // the real posthog-js loader
  s.async = true;
  s.onload = function () {
    var ph = window.posthog;
    if (!ph) return;

    var initCfg = {
      api_host: location.origin + INGEST,
      asset_host: location.origin + ASSETS,
      ui_host: CFG.uiHost,     // the real region UI (toolbar, replay player)
      capture_pageview: false, // pageviews are ours, below
      capture_pageleave: false // GoFastr navigations are not leaves
    };
    // PersonProfiles: only pass it when the host set one — an undefined
    // value would override the SDK's own default resolution, and the
    // valid values are exactly the three PostHog documents.
    if (CFG.personProfiles) initCfg.person_profiles = CFG.personProfiles;
    ph.init(CFG.apiKey, initCfg);

    // Identity: anonymous until whoami says otherwise. The generation
    // counter makes a stale response unable to overwrite a newer
    // refresh's answer.
    var known = null;
    var idGen = 0;
    function applyIdentity(id) {
      if (id === known) return;
      if (known) ph.reset(); // A→anon and A→B: sever the old chain first
      known = id;
      if (id) ph.identify(id);
    }
    function refreshIdentity(after) {
      var my = ++idGen;
      fetch(CFG.mount + '/whoami', {
        credentials: 'same-origin',
        headers: { Accept: 'application/json' }
      })
        .then(function (r) { return r.json(); })
        .then(function (me) {
          if (my !== idGen) return; // superseded by a newer refresh
          applyIdentity(me && me.id ? me.id : null);
          if (after) after();
        })
        .catch(function () {
          // Analytics must never break the page — but the initial
          // pageview still owes the visitor nothing identity-shaped.
          if (my === idGen && after) after();
        });
    }

    // The initial pageview: gofastr:navigate does not fire on first
    // load, and it fires after identity so the first event is already
    // attributed.
    refreshIdentity(function () {
      ph.capture('$pageview', { $current_url: location.href });
    });

    // One pageview per completed client-side navigation — the same
    // timing as the content it names. Never gofastr:beforenavigate: it
    // is cancelable and fires before the router commits, so counting
    // there records visits that never happened.
    window.addEventListener('gofastr:navigate', function (e) {
      var path = (e.detail && e.detail.path) || location.pathname;
      ph.capture('$pageview', { $current_url: location.origin + path });
      // A navigation can change identity (login/logout destinations);
      // re-check after every one.
      refreshIdentity();
    });
  };
  document.head.appendChild(s);
})();
