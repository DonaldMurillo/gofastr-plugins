// User-journey e2e for the Geomap plugin — a TRUSTED host-page MapLibre GL +
// OpenFreeMap vector map (the fourth heavy-JS plugin, now trusted like tour).
// The map mounts INLINE in the /map page (NO inner iframe): we locate directly
// on `page`, not a frameLocator.
//
// We pin the load-bearing risks the plugin author could not verify headless:
//   - MapLibre boots on the host page and creates its WebGL canvas (we assert the
//     canvas attaches, NOT that tiles rendered — CI/WebGL/network to
//     tiles.openfreemap.org is not guaranteed).
//   - Click-to-add drops a .maplibregl-marker with non-zero rendered size.
//   - A dropped marker persists across reload (save → reload → re-hydrate).
//   - Read-only disables click-to-add.
//   - The side-panel Tokyo card flyTo moves the map center (read from the
//     canonical doc the controller mirrors into input[name="map_doc"]).
//
// NOTE: this spec assumes the example app registers the geomap plugin at /map
// (example/main.go). MapLibre needs WebGL; Playwright's bundled Chromium
// (SwiftShader) and WebKit on macOS both provide it. Markers + flyTo are DOM/
// state operations that work even if tiles fail to fetch. If `new
// maplibregl.Map` throws for lack of WebGL, __mapError is set and ready() fails
// with an explicit message — report that as an environment gap, not a code bug.
import { test, expect, type Page } from "@playwright/test";

const MAP = "/map";
const SAVE = "/__gofastr/plugin/map/save";

const container = (page: Page) => page.locator(".maplibregl-map");
const canvas = (page: Page) => page.locator("canvas.maplibregl-canvas");
// Pins carry .fui-pin. Cluster bubbles are maplibregl.Markers too, so
// `.maplibregl-marker` alone cannot tell a pin from a bubble — never use it here.
const marker = (page: Page) => page.locator(".fui-pin");
const cluster = (page: Page) => page.locator(".fui-cluster");

/** The canonical doc the controller mirrors into the hidden field. */
async function doc(page: Page): Promise<MapDoc> {
  return page.locator('input[name="map_doc"]').evaluate((el) => {
    try {
      return JSON.parse((el as HTMLInputElement).value) as MapDoc;
    } catch {
      return { lat: NaN, lng: NaN, zoom: NaN, markers: [] };
    }
  });
}

interface MapDoc {
  lat: number;
  lng: number;
  zoom: number;
  markers: { id: string; lat: number; lng: number; label?: string }[];
}

/** Click the map at a point offset from its centre, in CSS pixels. */
async function clickMap(page: Page, dx = 0, dy = 0): Promise<void> {
  const box = await container(page).boundingBox();
  expect(box, "map container must have a bounding box").not.toBeNull();
  if (!box) return;
  const x = box.x + box.width / 2 + dx;
  const y = box.y + box.height / 2 + dy;
  await page.mouse.move(x, y);
  await page.mouse.click(x, y);
}

declare global {
  interface Window {
    __mapReady?: boolean;
    __mapStyleLoaded?: boolean;
    __mapError?: string;
    // Presence proves the controller mounted + the DOMContentLoaded boot
    // completed (the demo wires its toolbar on the gofastr:geomap-ready event,
    // dispatched synchronously just after this is assigned).
    __gofastrGeomap?: unknown;
  }
}

async function ready(page: Page): Promise<void> {
  // Wait until boot finished (controller assigned) or a construct error landed.
  // We wait on the controller rather than just __mapReady because the demo wires
  // its toolbar buttons in the gofastr:geomap-ready handler, which fires AFTER
  // __mapReady — waiting on the controller guarantees that handler has run.
  await page.waitForFunction(
    () => Boolean(window.__gofastrGeomap) || Boolean(window.__mapError),
    undefined,
    { timeout: 20_000 },
  );
  const err = await page.evaluate(() => window.__mapError);
  if (err) {
    throw new Error(`geomap failed to construct (likely no WebGL): ${err}`);
  }
  await expect(container(page)).toBeVisible({ timeout: 10_000 });
}

test.beforeEach(async ({ page, request, baseURL }) => {
  // Reset the shared demo doc to a known baseline so a marker-count assertion
  // is not polluted by a prior test's pins. The handler accepts either the
  // top-level fields or the `doc` blob form; we send both.
  await request.post(`${baseURL}${SAVE}`, {
    data: {
      docId: "demo",
      doc: { lat: 20, lng: 0, zoom: 2, markers: [] },
      lat: 20,
      lng: 0,
      zoom: 2,
      markers: [],
      schemaVersion: "map-v1",
    },
  });
  await page.goto(MAP);
  await ready(page);
});

test("mounts inline on the host page and creates the MapLibre canvas", async ({ page }) => {
  await expect(container(page)).toBeVisible();
  // The canvas proves MapLibre booted. We do NOT assert tiles rendered
  // (tiles.openfreemap.org network + WebGL are not guaranteed in CI).
  await expect(canvas(page)).toBeAttached({ timeout: 10_000 });
});

test("click-to-add drops a visible marker", async ({ page }) => {
  await expect(marker(page)).toHaveCount(0);

  // Click roughly the center of the map. The container fills the mount; the
  // middle avoids the navigation control (top-left), the style switcher
  // (top-right) and the attribution (bottom-right).
  await clickMap(page);

  // A marker is added. MapLibre's default marker is an inline SVG with a fixed
  // non-zero size, so a zero-size marker means it failed to mount.
  const pin = marker(page).first();
  await expect(pin).toBeVisible({ timeout: 5_000 });
  const box = await pin.boundingBox();
  expect(box, "marker must have non-zero rendered size").not.toBeNull();
  if (!box) return;
  expect(box.width).toBeGreaterThan(0);
  expect(box.height).toBeGreaterThan(0);
});

test("a dropped marker persists across reload", async ({ page }) => {
  await clickMap(page);
  await expect(marker(page).first()).toBeVisible({ timeout: 5_000 });

  // Pin count in the toolbar reflects the doc (the controller writes it to the
  // hidden field on a 400ms debounce). Wait for it to flip, then save.
  await expect(page.locator("#pin-count")).toContainText("1", { timeout: 5_000 });
  await page.locator("#save").click();
  await expect(page.locator("#save-status")).toContainText(/saved/i, { timeout: 5_000 });

  await page.reload();
  await ready(page);
  // After reload the saved doc is re-hydrated into the mount element's data-doc;
  // the controller re-seeds it. Pin count and marker element should both reflect it.
  await expect(page.locator("#pin-count")).toContainText("1", { timeout: 5_000 });
  await expect(marker(page).first()).toBeVisible({ timeout: 5_000 });
});

test("read-only disables click-to-add", async ({ page }) => {
  await expect(marker(page)).toHaveCount(0);

  // Toggle read-only via the toolbar button (the demo calls setReadOnly).
  await page.locator("#readonly").click();
  await expect(page.locator("#readonly")).toHaveClass(/active/);

  await clickMap(page);

  // Give the map a beat to (not) react; no marker should appear.
  // eslint-disable-next-line playwright/no-wait-for-timeout
  await page.waitForTimeout(500);
  await expect(marker(page)).toHaveCount(0);
});

test("side-panel card click flies the map and the controller applies it", async ({ page }) => {
  // Prove the map actually re-centered. We read it from the canonical doc the
  // controller mirrors into the hidden field: after flyTo, MapLibre's moveend
  // syncs doc.lat/lng to the new center, and the controller writes it back to
  // input[name="map_doc"] on a 400ms debounce.
  const latOf = () =>
    page.locator('input[name="map_doc"]').evaluate((el) => {
      const input = el as HTMLInputElement;
      try {
        return JSON.parse(input.value).lat as number;
      } catch {
        return NaN;
      }
    });

  const latBefore = await latOf(); // baseline: the beforeEach reset lat=20

  // Click the Tokyo card (lat ≈ 35.68).
  const card = page.locator('.card[data-label="Tokyo"]');
  await expect(card).toBeVisible();
  await card.click();

  // flyTo animates, then a moveend + a 400ms doc debounce. Poll the hidden
  // field until the center reflects Tokyo (proving the flyTo reached the map).
  await expect
    .poll(latOf, { timeout: 8_000 })
    .toBeGreaterThan(30); // Tokyo ~35.68; the world-view baseline was 20

  expect(await latOf(), "center must have moved from the baseline").not.toEqual(latBefore);
  // The clicked card is visually active.
  await expect(card).toHaveClass(/active/);
});

// ---------------------------------------------------------------------------
// Pin editing — the popup is a live editor, not a static label
// ---------------------------------------------------------------------------

test("renaming a pin in its popup persists across reload", async ({ page }) => {
  await clickMap(page);
  await expect(marker(page).first()).toBeVisible({ timeout: 5_000 });

  // Clicking a pin opens its popup. The popup owns a label input, not text.
  await marker(page).first().click();
  const label = page.locator(".fui-pin-label");
  await expect(label).toBeVisible({ timeout: 5_000 });
  await expect(label).toHaveValue("Pin"); // the click-to-add default

  await label.fill("Coffee stop");
  // The input commits on a 200ms debounce, then the doc mirrors on 400ms.
  await expect.poll(async () => (await doc(page)).markers[0]?.label, { timeout: 5_000 })
    .toBe("Coffee stop");

  await page.locator("#save").click();
  await expect(page.locator("#save-status")).toContainText(/saved/i, { timeout: 5_000 });

  await page.reload();
  await ready(page);
  // The label — not just the pin — must survive the round-trip. A doc that keeps
  // coordinates but drops labels still renders a map that looks right.
  await expect.poll(async () => (await doc(page)).markers[0]?.label, { timeout: 8_000 })
    .toBe("Coffee stop");
  await marker(page).first().click();
  await expect(page.locator(".fui-pin-label")).toHaveValue("Coffee stop");
});

test("deleting one pin leaves the others and persists", async ({ page }) => {
  // Three pins, spread far enough apart to click individually.
  await clickMap(page, -120, -60);
  await clickMap(page, 0, 40);
  await clickMap(page, 130, -50);
  await expect(marker(page)).toHaveCount(3);
  await expect(page.locator("#pin-count")).toContainText("3", { timeout: 5_000 });

  // Name them so we can prove WHICH one went.
  const names = ["alpha", "bravo", "charlie"];
  for (let i = 0; i < 3; i++) {
    await marker(page).nth(i).click();
    await page.locator(".fui-pin-label").fill(names[i]);
    await page.keyboard.press("Escape"); // close the popup without touching the map
  }
  await expect.poll(async () => (await doc(page)).markers.map((m) => m.label), { timeout: 5_000 })
    .toEqual(names);

  // Delete the middle one from its own popup.
  await marker(page).nth(1).click();
  const del = page.locator(".fui-pin-delete");
  await expect(del).toBeVisible();
  await del.click();

  await expect(marker(page)).toHaveCount(2);
  await expect.poll(async () => (await doc(page)).markers.map((m) => m.label), { timeout: 5_000 })
    .toEqual(["alpha", "charlie"]);

  await page.locator("#save").click();
  await expect(page.locator("#save-status")).toContainText(/saved/i, { timeout: 5_000 });
  await page.reload();
  await ready(page);
  await expect.poll(async () => (await doc(page)).markers.map((m) => m.label), { timeout: 8_000 })
    .toEqual(["alpha", "charlie"]);
});

test("read-only gates the popup editor, not just click-to-add", async ({ page }) => {
  await clickMap(page);
  await expect(marker(page).first()).toBeVisible({ timeout: 5_000 });

  await page.locator("#readonly").click();
  await expect(page.locator("#readonly")).toHaveClass(/active/);

  await marker(page).first().click();
  const label = page.locator(".fui-pin-label");
  await expect(label).toBeVisible({ timeout: 5_000 });
  // Read-only must reach the OPEN editor too — a read-only map whose popup still
  // renames and deletes pins is read-only in name only.
  await expect(label).toHaveAttribute("readonly", /.*/);
  await expect(page.locator(".fui-pin-delete")).toBeHidden();
});

// ---------------------------------------------------------------------------
// Drag
// ---------------------------------------------------------------------------

test("dragging a pin moves it and the new position persists", async ({ page }) => {
  await clickMap(page);
  const pin = marker(page).first();
  await expect(pin).toBeVisible({ timeout: 5_000 });

  // The doc mirrors into the hidden field on a 400ms debounce — poll, do not
  // read it the instant the pin appears.
  await expect.poll(async () => (await doc(page)).markers.length, { timeout: 5_000 }).toBe(1);
  const before = (await doc(page)).markers[0];

  const box = await pin.boundingBox();
  expect(box).not.toBeNull();
  if (!box) return;
  // Grab the pin body (its anchor is the bottom tip) and drag it left+up.
  const fromX = box.x + box.width / 2;
  const fromY = box.y + box.height / 2;
  await page.mouse.move(fromX, fromY);
  await page.mouse.down();
  // Intermediate moves: MapLibre starts a drag on movement, not on mousedown.
  await page.mouse.move(fromX - 40, fromY - 25, { steps: 8 });
  await page.mouse.move(fromX - 80, fromY - 50, { steps: 8 });
  await page.mouse.up();

  await expect
    .poll(async () => (await doc(page)).markers[0]?.lng, { timeout: 5_000 })
    .not.toBe(before.lng);
  const moved = (await doc(page)).markers[0];
  // Dragged left and up ⇒ west and north of where it started.
  expect(moved.lng, "drag left must decrease longitude").toBeLessThan(before.lng);
  expect(moved.lat, "drag up must increase latitude").toBeGreaterThan(before.lat);

  await page.locator("#save").click();
  await expect(page.locator("#save-status")).toContainText(/saved/i, { timeout: 5_000 });
  await page.reload();
  await ready(page);

  const after = (await doc(page)).markers[0];
  expect(after, "the dragged pin must survive the reload").toBeTruthy();
  // Round-tripped through JSON at 6dp; compare with a tolerance well under that.
  expect(Math.abs(after.lat - moved.lat)).toBeLessThan(0.001);
  expect(Math.abs(after.lng - moved.lng)).toBeLessThan(0.001);
});

// ---------------------------------------------------------------------------
// Style switcher + theme
// ---------------------------------------------------------------------------

test("the style switcher swaps the base style and keeps the pins", async ({ page }) => {
  await clickMap(page);
  await expect(marker(page)).toHaveCount(1);

  const dark = page.locator('.fui-style-opt[data-style="dark"]');
  await expect(dark).toBeVisible();
  await dark.click();
  await expect(dark).toHaveClass(/active/);

  // setStyle() tears down every source and layer. Marker overlays live outside
  // that teardown — this is the assertion that keeps it true.
  await expect(marker(page)).toHaveCount(1, { timeout: 10_000 });
  await expect(marker(page).first()).toBeVisible();
  await expect.poll(async () => (await doc(page)).markers.length, { timeout: 5_000 }).toBe(1);
});

test("the toolbar style select drives the same switcher control", async ({ page }) => {
  // Two routes to one piece of state: the host page's <select> and the in-map
  // control must not disagree about which style is active.
  await page.locator("#style").selectOption("positron");
  await expect(page.locator('.fui-style-opt[data-style="positron"]')).toHaveClass(/active/, {
    timeout: 10_000,
  });
  await expect(page.locator('.fui-style-opt[data-style="dark"]')).not.toHaveClass(/active/);
});

test("toggling the host theme keeps the map mounted", async ({ page }) => {
  await clickMap(page);
  await expect(marker(page)).toHaveCount(1);

  await page.locator("#fui-scheme-toggle").click();
  await expect(page.locator("html")).toHaveAttribute("data-color-scheme", "dark");

  // The demo ships an explicit style, so a scheme flip must NOT swap the base
  // style out from under the user — but the map (and its pins) must stay alive.
  await expect(container(page)).toBeVisible();
  await expect(marker(page)).toHaveCount(1);
  await expect(canvas(page)).toBeAttached();
});

// ---------------------------------------------------------------------------
// Toolbar
// ---------------------------------------------------------------------------

test("toolbar add / clear / reset drive the doc", async ({ page }) => {
  await page.locator("#add-random").click();
  await page.locator("#add-random").click();
  await expect(marker(page)).toHaveCount(2);
  await expect(page.locator("#pin-count")).toContainText("2", { timeout: 5_000 });

  await page.locator("#clear").click();
  await expect(marker(page)).toHaveCount(0);
  await expect(page.locator("#pin-count")).toContainText("0", { timeout: 5_000 });

  // Reset clears AND persists the empty map, so a reload stays empty. (Clear
  // alone only mutates the live doc — the distinction is the whole point.)
  await page.locator("#add-random").click();
  await expect(marker(page)).toHaveCount(1);
  await page.locator("#reset").click();
  await expect(page.locator("#save-status")).toContainText(/saved/i, { timeout: 5_000 });

  await page.locator("#load").click(); // reloads the page
  await ready(page);
  await expect(marker(page)).toHaveCount(0);
  await expect(page.locator("#pin-count")).toContainText("0", { timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Controls
// ---------------------------------------------------------------------------

test("geolocate and scale controls are mounted", async ({ page }) => {
  // We assert presence only. Clicking geolocate triggers a real permission
  // prompt, which is an environment interaction, not a journey.
  await expect(page.locator(".maplibregl-ctrl-geolocate")).toBeVisible();
  await expect(page.locator(".maplibregl-ctrl-scale")).toBeVisible();
});

// ---------------------------------------------------------------------------
// Place search (backed by the plugin's same-origin /geocode proxy; the example
// app wires a fixed offline dataset so this never touches a third party)
// ---------------------------------------------------------------------------

test("searching a place flies the map to it", async ({ page }) => {
  const input = page.locator(".fui-search-input");
  await expect(input).toBeVisible();

  await input.fill("tokyo");
  await page.locator(".fui-search-btn").click();

  const hit = page.locator(".fui-search-result").first();
  await expect(hit).toBeVisible({ timeout: 10_000 });
  await expect(hit).toContainText(/tokyo/i);

  await hit.click();
  // flyTo animates; the doc mirrors the new centre after moveend + 400ms.
  await expect.poll(async () => (await doc(page)).lat, { timeout: 10_000 }).toBeGreaterThan(30);
  const after = await doc(page);
  expect(Math.abs(after.lat - 35.6762)).toBeLessThan(1);
  expect(Math.abs(after.lng - 139.6503)).toBeLessThan(1);
  // Searching must not silently drop a pin — it is navigation, not editing.
  expect(after.markers).toHaveLength(0);
});

test("a search with no matches says so instead of failing silently", async ({ page }) => {
  await page.locator(".fui-search-input").fill("atlantis");
  await page.locator(".fui-search-btn").click();
  await expect(page.locator(".fui-search-status")).toContainText(/no results/i, { timeout: 10_000 });
  await expect(page.locator(".fui-search-result")).toHaveCount(0);
});

// ---------------------------------------------------------------------------
// Clustering
// ---------------------------------------------------------------------------

test("clustering folds nearby pins into a counted bubble and expands on click", async ({ page }) => {
  // Clustering is computed by a GeoJSON source, which only exists once the style
  // has loaded — that needs a route to tiles.openfreemap.org. Everything else in
  // this spec works offline; this one test cannot, so skip rather than fail on a
  // network gap.
  const styled = await page
    .waitForFunction(() => window.__mapStyleLoaded === true, undefined, { timeout: 15_000 })
    .then(() => true)
    .catch(() => false);
  test.skip(!styled, "map style did not load (no route to tiles.openfreemap.org) — clustering needs it");

  // Three pins within a cluster radius of each other at the world view.
  await clickMap(page, -15, -10);
  await clickMap(page, 0, 0);
  await clickMap(page, 15, 10);
  await expect(marker(page)).toHaveCount(3);
  // Let the doc settle (400ms debounce) before clustering hides the pins.
  await expect.poll(async () => (await doc(page)).markers.length, { timeout: 5_000 }).toBe(3);

  await page.locator("#cluster").click();
  await expect(page.locator("#cluster")).toHaveClass(/active/);

  // The three pins collapse into one bubble labelled with their count.
  const bubble = cluster(page).first();
  await expect(bubble).toBeVisible({ timeout: 10_000 });
  await expect(bubble).toHaveText("3");
  await expect(marker(page)).toHaveCount(0);
  // Clustering is a rendering concern — the document still holds all three.
  expect((await doc(page)).markers).toHaveLength(3);

  // Clicking a bubble zooms toward the level where it breaks apart. Assert the
  // SPLIT, not a pin count: supercluster's expansion zoom is where a cluster
  // divides into its children, and those children may themselves be clusters —
  // and once zoomed in, pins outside the viewport are legitimately not rendered.
  const zoomBefore = (await doc(page)).zoom;
  await bubble.click();
  await expect.poll(async () => (await doc(page)).zoom, { timeout: 10_000 }).toBeGreaterThan(zoomBefore);
  await expect
    .poll(async () => (await marker(page).count()) + (await cluster(page).count()), { timeout: 10_000 })
    .toBeGreaterThan(1);

  // Toggling clustering off restores every pin — that path is viewport-independent.
  await page.locator("#cluster").click();
  await expect(cluster(page)).toHaveCount(0);
  await expect(marker(page)).toHaveCount(3);
});
