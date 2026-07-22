package geomap

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)
func demoTheme() style.Theme {
	t := style.DefaultTheme()
	t.DarkColors = map[string]string{
		"background":    "#0B0B0E",
		"surface":       "#18181B",
		"surface-soft":  "#1F1F23",
		"text":          "#F4F4F5",
		"text-muted":    "#A1A1AA",
		"text-subtle":   "#71717A",
		"border":        "#27272A",
		"border-strong": "#3F3F46",
		"primary":       "#6366F1",
		"primary-fg":    "#FFFFFF",
	}
	return t
}

// demoPlaces is the side-panel list. Clicking a card flies the map to the
// coordinates and (when writable) drops a labelled pin there. The card list is
// deliberately a handful of well-known places: enough to exercise flyTo +
// addMarker + markerSelected without bloating the page.
var demoPlaces = []struct {
	ID, Label string
	Lat, Lng  float64
	Zoom      float64
}{
	{"nyc", "New York", 40.7128, -74.0060, 11},
	{"london", "London", 51.5074, -0.1278, 11},
	{"tokyo", "Tokyo", 35.6762, 139.6503, 11},
	{"sydney", "Sydney", -33.8688, 151.2093, 11},
	{"saopaulo", "São Paulo", -23.5505, -46.6333, 11},
}

// demoProviders mirrors the frame-known layer keys. The select option value is
// the provider key the proxy / setProvider RPC accepts.
var demoProviders = []struct{ Key, Label string }{
	{"osm", "OpenStreetMap"},
	{"carto-light", "Carto · Light"},
	{"carto-dark", "Carto · Dark"},
}

func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()

	// Seed the mount marker with the saved doc (if any); else an empty doc that
	// the frame expands into defaults. The frame's hasDoc() treats an absent
	// blob as "use config defaults", so empty string is fine.
	docJSON, _ := p.LoadDoc(r.Context(), defaultDocID)

	var providerOpts strings.Builder
	for _, prov := range demoProviders {
		providerOpts.WriteString(`<option value="` + prov.Key + `">` + prov.Label + `</option>`)
	}

	var cardsStr strings.Builder
	placesJSONBytes, _ := json.Marshal(demoPlaces)
	for _, pl := range demoPlaces {
		cardsStr.WriteString(`<button type="button" class="card" data-id="` + pl.ID + `" data-lat="` +
			jsonFloat(pl.Lat) + `" data-lng="` + jsonFloat(pl.Lng) + `" data-zoom="` + jsonFloat(pl.Zoom) +
			`" data-label="` + pl.Label + `" data-tip="Fly the map to ` + pl.Label + `">` +
			`<span class="card-emoji">` + placeEmoji(pl.ID) + `</span>` +
			`<span class="card-label">` + pl.Label + `</span>` +
			`<span class="card-coord">` + jsonFloat(pl.Lat) + `, ` + jsonFloat(pl.Lng) + `</span>` +
			`</button>`)
	}

	mount := Mount(MountConfig{DocID: defaultDocID, Doc: docJSON})
	return render.HTML(strings.NewReplacer(
		"{{TOKENS}}", tokens,
		"{{SAVE_URL}}", SaveURL,
		"{{PROVIDER_OPTIONS}}", providerOpts.String(),
		"{{CARDS}}", cardsStr.String(),
		"{{PLACES}}", string(placesJSONBytes),
		"{{MOUNT}}", string(mount),
		"{{BROKER}}", pluginhost.BrokerScriptURL,
		"{{CONFIG}}", ConfigScriptURL,
		"{{ADAPTER}}", AdapterScriptURL,
	).Replace(demoPage))
}

// Tiny helpers keep the template readable AND avoid pulling fmt (whose verbs
// would clash with the literal % in the page's CSS). jsonFloat formats a float
// the way encoding/json does, so the data-* attributes match the wire shape.
func jsonFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func placeEmoji(id string) string {
	switch id {
	case "nyc":
		return "🗽"
	case "london":
		return "🎡"
	case "tokyo":
		return "🗼"
	case "sydney":
		return "🌉"
	case "saopaulo":
		return "🏙️"
	default:
		return "📍"
	}
}

// demoPage is the showcase. Placeholders are {{TOKENS}} (bridged theme CSS
// custom properties), {{SAVE_URL}}, {{PROVIDER_OPTIONS}} (select options),
// {{CARDS}} (location buttons), {{PLACES}} (JSON for the client), {{MOUNT}}
// (the iframe mount marker), {{BROKER}}/{{CONFIG}}/{{ADAPTER}} (script srcs).
// It uses strings.NewReplacer (NOT fmt.Sprintf) because the CSS carries
// literal % characters (translateX(-100%), color-mix(… 10% …)) that Sprintf
// would misread.
const demoPage = `<!doctype html>
<html lang="en" data-color-scheme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Geomap — Showcase</title>
<style>
{{TOKENS}}
*{box-sizing:border-box}
body{margin:0;font-family:var(--font-body,system-ui,sans-serif);background:var(--color-background);color:var(--color-text)}
header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:12px 20px;border-bottom:1px solid var(--color-border)}
header h1{font-size:16px;margin:0}
.hgroup{display:flex;gap:8px}
.btn{font:inherit;font-size:13px;padding:6px 11px;border:1px solid var(--color-border);border-radius:8px;background:var(--color-surface);color:var(--color-text);cursor:pointer;line-height:1}
.btn:hover{border-color:var(--color-primary)}
.btn.toggle.active{background:var(--color-primary);color:var(--color-primary-fg,#fff);border-color:var(--color-primary)}
.btn.primary{background:var(--color-primary);color:var(--color-primary-fg,#fff);border-color:var(--color-primary)}
.toolbar{display:flex;flex-wrap:wrap;align-items:center;gap:8px;padding:10px 20px;border-bottom:1px solid var(--color-border);background:var(--color-surface-soft,transparent);position:sticky;top:0;z-index:20}
.ctl{display:inline-flex;align-items:center;gap:6px;font-size:12px;color:var(--color-text-muted)}
.ctl select{font:inherit;font-size:13px;padding:5px 8px;border:1px solid var(--color-border);border-radius:8px;background:var(--color-surface);color:var(--color-text)}
.sep{width:1px;height:22px;background:var(--color-border)}
.count{font-variant-numeric:tabular-nums;color:var(--color-text);min-width:22px;text-align:center}
main{max-width:1280px;margin:0 auto;padding:16px 20px 40px;display:grid;grid-template-columns:1fr 280px;gap:16px}
.map-area{min-width:0}
.map-area form{margin:0}
#map-frame-wrap{border:1px solid var(--color-border);border-radius:14px;overflow:hidden;background:var(--color-surface)}
#map-frame-wrap iframe{display:block;width:100%;border:0}
.hint{color:var(--color-text-muted);font-size:13px;margin:0 0 10px}
.saverow{display:flex;align-items:center;gap:10px;margin-top:12px}
#save-status{font-size:13px;color:var(--color-text-muted)}
.side{display:flex;flex-direction:column;gap:10px}
.side h2{font-size:13px;margin:0;color:var(--color-text-muted);text-transform:uppercase;letter-spacing:.04em}
.card{display:grid;grid-template-columns:28px 1fr;grid-template-rows:auto auto;column-gap:10px;align-items:center;text-align:left;padding:10px 12px;border:1px solid var(--color-border);border-radius:10px;background:var(--color-surface);color:var(--color-text);cursor:pointer;font:inherit;transition:transform .08s ease,border-color .12s ease}
.card:hover{border-color:var(--color-primary);transform:translateY(-1px)}
.card.active{border-color:var(--color-primary);box-shadow:0 0 0 2px color-mix(in srgb,var(--color-primary) 25%,transparent)}
.card-emoji{grid-row:1 / span 2;font-size:20px;line-height:1}
.card-label{font-size:14px;font-weight:600}
.card-coord{grid-column:2;font-size:11px;color:var(--color-text-muted);font-variant-numeric:tabular-nums}
/* Tooltips */
[data-tip]{position:relative}
[data-tip]:hover::after,[data-tip]:focus-visible::after{
  content:attr(data-tip);position:absolute;left:50%;top:calc(100% + 8px);transform:translateX(-50%);
  white-space:nowrap;background:var(--color-text);color:var(--color-background);font-size:11px;
  padding:5px 8px;border-radius:6px;pointer-events:none;z-index:50;box-shadow:0 4px 14px rgba(0,0,0,.25)}
@media (max-width: 820px){ main{grid-template-columns:1fr} }
</style>
</head>
<body>
<header>
  <h1>Geomap — Showcase</h1>
  <div class="hgroup">
    <button type="button" class="btn" id="fui-scheme-toggle" data-tip="Toggle light / dark">Toggle theme</button>
  </div>
</header>

<div class="toolbar" role="toolbar" aria-label="Map controls">
  <label class="ctl">Base layer
    <select id="provider" data-tip="Switch the tile provider (same-origin proxy)">{{PROVIDER_OPTIONS}}</select>
  </label>
  <button type="button" class="btn toggle" id="readonly" data-tip="Disable marker editing">Read-only</button>
  <span class="sep"></span>
  <button type="button" class="btn" id="add-random" data-tip="Drop a pin at the map center">+ Pin at center</button>
  <button type="button" class="btn" id="clear" data-tip="Remove every pin">Clear pins</button>
  <span class="sep"></span>
  <span class="ctl">Pins <span class="count" id="pin-count">0</span></span>
</div>

<main>
  <div class="map-area">
    <p class="hint">A sandboxed Leaflet map. Click the map to drop a pin, drag pins to move them, click a pin to edit or delete. Tiles stream same-origin through the plugin's proxy — the opaque-origin CSP blocks external hosts.</p>
    <div id="map-frame-wrap">
      <form method="post" action="{{SAVE_URL}}" id="fui-demo-form">
        {{MOUNT}}
      </form>
    </div>
    <div class="saverow">
      <button type="button" class="btn primary" id="save">Save</button>
      <button type="button" class="btn" id="load" data-tip="Reload the page to re-hydrate from the store">Load</button>
      <button type="button" class="btn" id="reset" data-tip="Clear pins and persist the empty map">Reset</button>
      <span id="save-status" role="status" aria-live="polite"></span>
    </div>
  </div>
  <aside class="side" aria-label="Locations">
    <h2>Locations</h2>
    {{CARDS}}
  </aside>
</main>

<script>
(function () {
  var PLACES = {{PLACES}};

  // --- theme toggle (host page scheme → broker re-bridges tokens) ----------
  var html = document.documentElement;
  var themeBtn = document.getElementById('fui-scheme-toggle');
  themeBtn.addEventListener('click', function () {
    var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
    html.dataset.colorScheme = next;
    themeBtn.textContent = next === 'dark' ? 'Light theme' : 'Toggle theme';
  });

  // --- postMessage bridge to the frame (same shape as monaco's demo) ------
  var seq = 0;
  function frameIframe() {
    return document.querySelector('#fui-demo-form iframe') || document.querySelector('iframe');
  }
  function frameWin() {
    var f = frameIframe();
    return f && f.contentWindow ? f.contentWindow : null;
  }
  function send(method, params) {
    var w = frameWin();
    if (!w) return;
    w.postMessage({ v: 1, id: 'demo-' + (++seq), type: 'event', src: 'host', method: method, params: params || {} }, '*');
  }

  // --- controls -----------------------------------------------------------
  var providerSel = document.getElementById('provider');
  var readonlyBtn = document.getElementById('readonly');
  var pinCount = document.getElementById('pin-count');
  var saveStatus = document.getElementById('save-status');

  providerSel.addEventListener('change', function () {
    send('setProvider', { provider: providerSel.value });
  });
  readonlyBtn.addEventListener('click', function () {
    var on = !readonlyBtn.classList.contains('active');
    readonlyBtn.classList.toggle('active', on);
    readonlyBtn.setAttribute('aria-pressed', String(on));
    send('setReadOnly', { readOnly: on });
  });
  document.getElementById('add-random').addEventListener('click', function () {
    send('addMarker', { label: 'Pin' });
  });
  document.getElementById('clear').addEventListener('click', function () {
    send('clearMarkers', {});
  });

  // --- side-panel cards: flyTo + addMarker --------------------------------
  function clearActiveCards() {
    document.querySelectorAll('.card.active').forEach(function (c) { c.classList.remove('active'); });
  }
  document.querySelectorAll('.card').forEach(function (card) {
    card.addEventListener('click', function () {
      var lat = parseFloat(card.getAttribute('data-lat'));
      var lng = parseFloat(card.getAttribute('data-lng'));
      var zoom = parseFloat(card.getAttribute('data-zoom'));
      var label = card.getAttribute('data-label');
      send('flyTo', { lat: lat, lng: lng, zoom: zoom });
      send('addMarker', { lat: lat, lng: lng, label: label });
      clearActiveCards();
      card.classList.add('active');
    });
  });

  // --- inbound: markerSelected highlights the matching card ---------------
  // The adapter dispatches a 'map:markerSelected' CustomEvent on the iframe
  // element. We map marker id → label by reading the hidden field's doc JSON,
  // then find the card whose data-label matches.
  function hiddenDoc() {
    var inp = document.querySelector('input[name="map_doc"]');
    if (!inp || !inp.value) return null;
    try { return JSON.parse(inp.value); } catch (_) { return null; }
  }
  function highlightCardByMarkerId(id) {
    var d = hiddenDoc();
    if (!d || !Array.isArray(d.markers)) return;
    var m = null;
    for (var i = 0; i < d.markers.length; i++) { if (d.markers[i].id === id) { m = d.markers[i]; break; } }
    if (!m) return;
    var label = m.label || '';
    clearActiveCards();
    document.querySelectorAll('.card').forEach(function (c) {
      if (c.getAttribute('data-label') === label) c.classList.add('active');
    });
  }
  function bindIframeEvents() {
    var f = frameIframe();
    if (!f) return;
    f.addEventListener('map:markerSelected', function (e) {
      highlightCardByMarkerId(e.detail.id);
    });
  }

  // The iframe is created by the broker after it sees the mount marker. Poll
  // briefly so we can attach the CustomEvent listener (the broker owns the
  // iframe element reference; we only add a non-blocking event listener).
  var iframePoll = window.setInterval(function () {
    if (frameIframe()) { bindIframeEvents(); window.clearInterval(iframePoll); }
  }, 200);

  // --- pin count: re-read the hidden field when it changes (MutationObserver)
  var hidden = document.querySelector('input[name="map_doc"]');
  function refreshPinCount() {
    var d = hiddenDoc();
    pinCount.textContent = String((d && Array.isArray(d.markers)) ? d.markers.length : 0);
  }
  refreshPinCount();
  if (hidden && typeof MutationObserver !== 'undefined') {
    var obs = new MutationObserver(refreshPinCount);
    obs.observe(hidden, { attributes: true, attributeFilter: ['value'] });
    // MutationObserver does not fire on programmatic .value sets in all engines
    // (WebKit included); poll as a belt-and-braces fallback.
    window.setInterval(refreshPinCount, 1000);
  }

  // --- save / load / reset ------------------------------------------------
  function postSave(payload) {
    saveStatus.textContent = 'Saving…';
    fetch('{{SAVE_URL}}', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    }).then(function (r) {
      saveStatus.textContent = r.ok ? 'Saved ✓' : ('Save failed (' + r.status + ')');
    })['catch'](function () { saveStatus.textContent = 'Save failed'; });
  }
  document.getElementById('save').addEventListener('click', function () {
    var d = hiddenDoc();
    if (!d) { saveStatus.textContent = 'Nothing to save yet.'; return; }
    postSave({
      docId: 'demo',
      doc: d,
      lat: d.lat, lng: d.lng, zoom: d.zoom, markers: d.markers,
      schemaVersion: 'map-v1'
    });
  });
  document.getElementById('load').addEventListener('click', function () {
    location.reload();
  });
  document.getElementById('reset').addEventListener('click', function () {
    send('clearMarkers', {});
    window.setTimeout(function () {
      var d = hiddenDoc();
      postSave({
        docId: 'demo',
        doc: d || { lat: 20, lng: 0, zoom: 2, markers: [] },
        lat: d ? d.lat : 20, lng: d ? d.lng : 0, zoom: d ? d.zoom : 2,
        markers: [],
        schemaVersion: 'map-v1'
      });
    }, 500);
  });
})();
</script>
<script src="{{BROKER}}"></script>
<script src="{{CONFIG}}"></script>
<script src="{{ADAPTER}}"></script>
</body>
</html>`
