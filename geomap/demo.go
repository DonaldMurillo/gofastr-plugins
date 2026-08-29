package geomap

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/DonaldMurillo/gofastr/core-ui/style"
	"github.com/DonaldMurillo/gofastr/core/render"
)

func demoTheme() style.Theme {
	t := style.DefaultTheme()
	// The house amber, in both schemes — the same values the other demo pages
	// set. Left on style.DefaultTheme() this page rendered with the framework's
	// purple accent while every plugin beside it was amber, which reads as a
	// different product rather than a different plugin.
	t.Colors.Primary = style.Color{Name: "primary", Value: "oklch(0.82 0.155 78)"}
	t.Colors.PrimaryFg = style.Color{Name: "primary-fg", Value: "oklch(0.14 0.005 75)"}
	t.Colors.Accent = style.Color{Name: "accent", Value: "oklch(0.82 0.155 78)"}
	t.DarkColors = map[string]string{
		"background":    "#0B0B0E",
		"surface":       "#18181B",
		"surface-soft":  "#1F1F23",
		"text":          "#F4F4F5",
		"text-muted":    "#A1A1AA",
		"text-subtle":   "#71717A",
		"border":        "#27272A",
		"border-strong": "#3F3F46",
		"primary":       "#E0A040",
		"primary-fg":    "#18181B",
		// The pin-popup Delete label. The light-mode red is unreadable on this
		// surface (~3.4:1); map.css falls back to the same value when a host
		// defines no danger token.
		"danger": "#F87171",
	}
	return t
}

// demoPlaces is the side-panel list. Clicking a card flies the map to the
// coordinates and (when writable) drops a labelled pin there. The card list is
// deliberately a handful of well-known places: enough to exercise flyTo +
// addMarker + markerSelected without bloating the page. The stable card
// attributes (data-id/lat/lng/zoom/label) are read by the demo script + e2e.
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

// demoStyles populates the toolbar style switcher (the OpenFreeMap style names).
// The in-map style switcher control is seeded separately from MapConfig.Styles.
var demoStyles = []struct{ Key, Label string }{
	{"liberty", "Liberty"},
	{"positron", "Positron"},
	{"dark", "Dark"},
	{"bright", "Bright"},
	{"fiord", "Fiord"},
}

func (p *Plugin) renderDemo(r *http.Request) render.HTML {
	tokens := demoTheme().CSSCustomProperties()

	// Seed the mount with the saved doc (if any); else an empty doc that map.js
	// expands into defaults. map.js treats an absent data-doc as "use config
	// defaults", so an empty blob is fine.
	docJSON, _ := p.LoadDoc(r.Context(), defaultDocID)

	var styleOpts strings.Builder
	for _, s := range demoStyles {
		styleOpts.WriteString(`<option value="` + s.Key + `">` + s.Label + `</option>`)
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

	mount := p.Mount(MountConfig{DocID: defaultDocID, Doc: docJSON})
	return render.HTML(strings.NewReplacer(
		"{{TOKENS}}", tokens,
		"{{SAVE_URL}}", SaveURL,
		"{{STYLE_OPTIONS}}", styleOpts.String(),
		"{{CARDS}}", cardsStr.String(),
		"{{PLACES}}", string(placesJSONBytes),
		"{{MOUNT}}", string(mount),
		"{{MAP_CSS}}", MapCSSURL,
		"{{MAP_JS}}", MapJSURL,
	).Replace(demoPage))
}

// jsonFloat formats a float the way encoding/json does, so the data-* attributes
// match the wire shape. (fmt verbs would clash with the literal % in the CSS.)
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
// custom properties), {{SAVE_URL}}, {{STYLE_OPTIONS}} (select options),
// {{CARDS}} (location buttons), {{PLACES}} (JSON for the client), {{MOUNT}}
// (the inline host-page mount element), {{MAP_CSS}}/{{MAP_JS}} (asset URLs). It
// uses strings.NewReplacer (NOT fmt.Sprintf) because the CSS carries literal %
// characters (translateX(-100%), color-mix(… 10% …)) that Sprintf would misread.
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
#map-mount{border:1px solid var(--color-border);border-radius:14px;overflow:hidden;background:var(--color-surface)}
.hint{color:var(--color-text-muted);font-size:13px;margin:0 0 10px}
.saverow{display:flex;align-items:center;gap:10px;margin-top:12px}
#save-status{font-size:13px;color:var(--color-text-muted)}
.side{display:flex;flex-direction:column;gap:10px}
.side h2{font-size:13px;margin:0;color:var(--color-text-muted);text-transform:uppercase;letter-spacing:.04em}
/* House classes shared with the other demo pages (docs/demo-page-design.md).
   .card-info is named apart from .card because .card is already this page's
   location buttons and the journeys select on it. */
.brand{display:flex;align-items:center;gap:10px}
.brand-mark{width:24px;height:24px;flex:none;border-radius:var(--radii-md,8px);background:linear-gradient(135deg,var(--color-primary),color-mix(in srgb,var(--color-primary) 45%,var(--color-text)));box-shadow:0 2px 8px color-mix(in srgb,var(--color-primary) 45%,transparent)}
.brand h1{font-size:15px;font-weight:600;margin:0}
.brand-dim{color:var(--color-text-subtle);font-weight:500}
.navlink{font-size:.875rem;text-decoration:none;color:var(--color-text-muted);padding:6px 12px;border-radius:999px;line-height:1.2}
.navlink:hover{color:var(--color-text);background:var(--color-surface-soft)}
.navlink.is-active{color:var(--color-text);background:var(--color-surface-soft);font-weight:600}
.fui-btn{font:inherit;font-size:.875rem;padding:6px 12px;border:1px solid var(--color-border);border-radius:var(--radii-md,8px);background:var(--color-surface);color:var(--color-text);cursor:pointer}
.fui-btn:hover{border-color:color-mix(in srgb,var(--color-primary) 45%,var(--color-border))}
.hero{margin:clamp(28px,6vw,48px) 0 clamp(20px,4vw,28px)}
.hero h2{font-size:clamp(28px,5vw,44px);line-height:1.1;letter-spacing:-.02em;margin:0 0 16px;font-weight:700}
.lead{font-size:clamp(15px,2vw,17px);line-height:1.6;color:var(--color-text-muted);max-width:62ch;margin:0 0 20px}
.badges{display:flex;flex-wrap:wrap;gap:8px;margin:0}
.badge{font-family:var(--font-mono,ui-monospace,monospace);font-size:12px;padding:5px 12px;border:1px solid var(--color-border);border-radius:999px;color:var(--color-text-muted);background:var(--color-surface)}
.badge-primary{background:var(--color-primary);border-color:var(--color-primary);color:var(--color-primary-fg,#18181b);font-weight:600}
.editor-card{border:1px solid var(--color-border);border-radius:var(--radii-lg,14px);overflow:hidden;background:var(--color-surface)}
.editor-chrome{display:flex;align-items:center;gap:8px;padding:10px 14px;border-bottom:1px solid var(--color-border);background:var(--color-surface-soft)}
.dot{width:10px;height:10px;border-radius:50%}
.dot-r{background:#ff5f57}.dot-y{background:#febc2e}.dot-g{background:#28c840}
.editor-title{font-family:var(--font-mono,ui-monospace,monospace);font-size:12px;color:var(--color-text-muted);margin-left:6px}
.editor-mode{margin-left:auto;font-family:var(--font-mono,ui-monospace,monospace);font-size:11px;padding:3px 10px;border:1px solid var(--color-border);border-radius:999px;color:var(--color-text-muted)}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:16px;margin:clamp(28px,5vw,40px) 0}
.card-info{border:1px solid var(--color-border);border-radius:var(--radii-lg,12px);padding:20px;background:var(--color-surface)}
.card-info h3{margin:0 0 10px;font-size:15px;font-weight:650}
.card-info p{margin:0;font-size:14px;line-height:1.6;color:var(--color-text-muted)}
footer{margin-top:clamp(28px,5vw,40px);padding-top:20px;border-top:1px solid var(--color-border);font-size:13px;color:var(--color-text-subtle)}
footer a{color:var(--color-text-muted)}
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
<link rel="stylesheet" href="{{MAP_CSS}}">
</head>
<body>
<header>
  <div class="brand">
    <span class="brand-mark" aria-hidden="true"></span>
    <h1>GoFastr <span class="brand-dim">/ plugins / map</span></h1>
  </div>
  <nav aria-label="Demo pages">
    <a class="navlink" href="/">Gallery</a>
    <a class="navlink is-active" href="/map" aria-current="page">Trusted</a>
    <button type="button" class="fui-btn" id="fui-scheme-toggle" aria-label="Toggle color scheme">◐ Theme</button>
  </nav>
</header>

<section class="hero">
  <h2>Vector tiles need the network.<br>So this one is not in the cage.</h2>
  <p class="lead">Every other plugin here runs in an opaque-origin iframe with
  <code>connect-src 'none'</code>. A vector map cannot: MapLibre streams tiles from
  <code>tiles.openfreemap.org</code>, and a frame that cannot fetch cannot draw a map. So this
  one is a <strong>trusted host-page plugin</strong> — no iframe, no cage, running with the
  page's own privileges because its job requires them. That is a trade the host makes
  deliberately, and the badge below says so rather than implying otherwise.</p>
  <p class="badges" aria-label="Facts">
    <span class="badge badge-primary">trusted host page</span>
    <span class="badge">MapLibre GL · vector</span>
    <span class="badge">tiles.openfreemap.org</span>
    <span class="badge">geocode via same-origin proxy</span>
  </p>
</section>

<div class="toolbar" role="toolbar" aria-label="Map controls">
  <label class="ctl">Style
    <select id="style" data-tip="Switch the OpenFreeMap vector base style">{{STYLE_OPTIONS}}</select>
  </label>
  <button type="button" class="btn toggle" id="readonly" data-tip="Disable marker editing">Read-only</button>
  <button type="button" class="btn toggle" id="cluster" data-tip="Group nearby pins into counted bubbles">Cluster pins</button>
  <span class="sep"></span>
  <button type="button" class="btn" id="add-random" data-tip="Drop a pin at the map center">+ Pin at center</button>
  <button type="button" class="btn" id="clear" data-tip="Remove every pin">Clear pins</button>
  <span class="sep"></span>
  <span class="ctl">Pins <span class="count" id="pin-count">0</span></span>
</div>

<main>
  <div class="map-area">
    <p class="hint">An OpenFreeMap <strong>vector</strong> map (MapLibre GL), running as a trusted host-page plugin. Click the map to drop a pin; click a pin to rename or delete it; drag pins to move them. Tiles stream directly from tiles.openfreemap.org — the host page CSP allows it (the opaque sandbox could not). Place search, when enabled, goes through the plugin's own same-origin proxy.</p>
    <div class="editor-card">
      <div class="editor-chrome" aria-hidden="true">
        <span class="dot dot-r"></span><span class="dot dot-y"></span><span class="dot dot-g"></span>
        <span class="editor-title">world.pins</span>
        <span class="editor-mode">trusted host page</span>
      </div>
      <div id="map-mount">
        {{MOUNT}}
      </div>
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

<section class="grid" aria-label="How it works">
  <article class="card-info">
    <h3>🔓 Trusted, and it says so</h3>
    <p>The chrome above reads <code>trusted host page</code>, not
    <code>sandboxed iframe</code>. This plugin runs with the page's privileges: it can reach
    the DOM, the network and host storage. Copying the sandboxed badge would be the one
    dishonest thing a demo could do here.</p>
  </article>
  <article class="card-info">
    <h3>🗺️ Why the cage does not fit</h3>
    <p>The framed CSP sets <code>connect-src 'none'</code>. MapLibre needs to stream vector
    tiles, so a sandboxed map renders nothing at all. The host's own CSP allows
    <code>tiles.openfreemap.org</code>; the opaque frame never could.</p>
  </article>
  <article class="card-info">
    <h3>📍 Pins are the host's data</h3>
    <p>Markers live in the host page and persist through the plugin's save route. Search,
    when enabled, goes through a <strong>same-origin proxy</strong> rather than letting the
    map call a geocoder directly — the one network path the host keeps for itself.</p>
  </article>
</section>

<footer>
  gofastr-plugins · geomap 0.3.0 · <a href="/">all plugins →</a>
</footer>

<script>
(function () {
  var SAVE_URL = '{{SAVE_URL}}';
  var html = document.documentElement;
  var themeBtn = document.getElementById('fui-scheme-toggle');
  var styleSel = document.getElementById('style');
  var readonlyBtn = document.getElementById('readonly');
  var pinCount = document.getElementById('pin-count');
  var saveStatus = document.getElementById('save-status');
  var hidden = document.querySelector('input[name="map_doc"]');

  function ctrl() { return window.__gofastrGeomap; }
  // The runtime fires 'gofastr:geomap-ready' on window after it mounts every
  // [data-fui-geomap]. If the script already ran (readyState !== loading), the
  // controller exists and we proceed immediately.
  function whenReady(fn) {
    if (ctrl()) { fn(); return; }
    window.addEventListener('gofastr:geomap-ready', fn, { once: true });
  }

  // --- theme toggle (host page scheme) -----------------------------------
  themeBtn.addEventListener('click', function () {
    var next = html.dataset.colorScheme === 'dark' ? 'light' : 'dark';
    html.dataset.colorScheme = next;
    themeBtn.textContent = next === 'dark' ? 'Light theme' : 'Toggle theme';
  });

  // --- controls wired to the controller ----------------------------------
  whenReady(function () {
    var c = ctrl();
    styleSel.addEventListener('change', function () { c.setStyle(styleSel.value); });
    readonlyBtn.addEventListener('click', function () {
      var on = !readonlyBtn.classList.contains('active');
      readonlyBtn.classList.toggle('active', on);
      readonlyBtn.setAttribute('aria-pressed', String(on));
      c.setReadOnly(on);
    });
    var clusterBtn = document.getElementById('cluster');
    clusterBtn.addEventListener('click', function () {
      var on = !clusterBtn.classList.contains('active');
      clusterBtn.classList.toggle('active', on);
      clusterBtn.setAttribute('aria-pressed', String(on));
      c.setCluster(on);
    });
    document.getElementById('add-random').addEventListener('click', function () { c.addMarker({ label: 'Pin' }); });
    document.getElementById('clear').addEventListener('click', function () { c.clearMarkers(); });

    // --- side-panel cards: flyTo + addMarker -----------------------------
    function clearActiveCards() { document.querySelectorAll('.card.active').forEach(function (el) { el.classList.remove('active'); }); }
    document.querySelectorAll('.card').forEach(function (card) {
      card.addEventListener('click', function () {
        var lat = parseFloat(card.getAttribute('data-lat'));
        var lng = parseFloat(card.getAttribute('data-lng'));
        var zoom = parseFloat(card.getAttribute('data-zoom'));
        var label = card.getAttribute('data-label');
        c.flyTo({ lat: lat, lng: lng, zoom: zoom });
        c.addMarker({ lat: lat, lng: lng, label: label });
        clearActiveCards();
        card.classList.add('active');
      });
    });

    // --- inbound: markerSelected highlights the matching card -----------
    c.onMarkerSelected(function (id) {
      var d = c.getDoc();
      if (!d || !Array.isArray(d.markers)) return;
      var m = null;
      for (var i = 0; i < d.markers.length; i++) { if (d.markers[i].id === id) { m = d.markers[i]; break; } }
      if (!m) return;
      clearActiveCards();
      document.querySelectorAll('.card').forEach(function (el) {
        if (el.getAttribute('data-label') === (m.label || '')) el.classList.add('active');
      });
    });
  });

  // --- pin count: re-read the hidden field when it changes ---------------
  function hiddenDoc() {
    if (!hidden || !hidden.value) return null;
    try { return JSON.parse(hidden.value); } catch (_) { return null; }
  }
  function refreshPinCount() {
    var d = hiddenDoc();
    pinCount.textContent = String((d && Array.isArray(d.markers)) ? d.markers.length : 0);
  }
  refreshPinCount();
  if (hidden && typeof MutationObserver !== 'undefined') {
    new MutationObserver(refreshPinCount).observe(hidden, { attributes: true, attributeFilter: ['value'] });
  }
  window.setInterval(refreshPinCount, 1000);

  // --- save / load / reset (POSTs the canonical doc from the controller) -
  function postSave(doc) {
    saveStatus.textContent = 'Saving…';
    fetch(SAVE_URL, {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ docId: 'demo', doc: doc, lat: doc.lat, lng: doc.lng, zoom: doc.zoom, markers: doc.markers, schemaVersion: 'map-v1' })
    }).then(function (r) { saveStatus.textContent = r.ok ? 'Saved ✓' : ('Save failed (' + r.status + ')'); })
      ['catch'](function () { saveStatus.textContent = 'Save failed'; });
  }
  document.getElementById('save').addEventListener('click', function () {
    var c = ctrl(); if (!c) return;
    var d = c.getDoc(); if (!d) { saveStatus.textContent = 'Nothing to save yet.'; return; }
    postSave(d);
  });
  document.getElementById('load').addEventListener('click', function () { location.reload(); });
  document.getElementById('reset').addEventListener('click', function () {
    var c = ctrl(); if (c) c.clearMarkers();
    window.setTimeout(function () {
      var c2 = ctrl(); var d = c2 ? c2.getDoc() : null;
      postSave(d || { lat: 20, lng: 0, zoom: 2, markers: [] });
    }, 400);
  });
})();
</script>
<script src="{{MAP_JS}}"></script>
</body>
</html>`
