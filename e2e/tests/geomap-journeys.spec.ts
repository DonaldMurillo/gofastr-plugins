// User-journey e2e for the Geomap (Leaflet) plugin — the fourth sandboxed
// heavy-JS plugin. Pins the load-bearing risks the plugin author could not
// verify headless:
//   - Leaflet boots inside the opaque-origin iframe and requests tiles via the
//     same-origin proxy (we assert the <img> element is created, NOT that the
//     tile network succeeded — CI has no guarantee of reaching OSM/Carto).
//   - The custom inline-SVG marker icon is rendered with non-zero size (guards
//     the Leaflet-default-icon 404 gotcha).
//   - Click-to-add, persistence, and read-only all behave; the demo's
//     location-card -> flyTo round-trips through the bridge.
//
// NOTE: this spec assumes the example app registers the geomap plugin at /map.
// Plugin registration lives in example/main.go, which another agent owns — if
// /map is not registered, this spec will fail to load and report that as an
// EXPECTED external dependency, not a bug in this plugin's code.
import { test, expect, type Page } from "@playwright/test";

const MAP = "/map";
const SAVE = "/__gofastr/plugin/map/save";

const frame = (page: Page) => page.frameLocator("iframe");
const container = (page: Page) => frame(page).locator(".leaflet-container");
const tilePane = (page: Page) => frame(page).locator("img.leaflet-tile");
const markerIcon = (page: Page) => frame(page).locator(".leaflet-marker-icon");

async function ready(page: Page): Promise<void> {
  // The adapter mirrors the ready handshake onto iframe.__mapReady, same shape
  // as monaco's __monacoReady. Wait for it (or the container proxy to attach)
  // before asserting anything inside the frame.
  await page.waitForFunction(
    () => {
      const f = document.querySelector("iframe") as (HTMLIFrameElement & { __mapReady?: boolean }) | null;
      return !!f && f.__mapReady === true;
    },
    undefined,
    { timeout: 15_000 },
  );
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

test("mounts in the opaque-origin sandbox and creates tile <img> elements", async ({ page }) => {
  await expect(container(page)).toBeVisible();
  // Leaflet always builds tile <img> elements on mount; we do NOT assert on
  // successful tile loads (CI network to OSM/Carto is not guaranteed). The
  // presence of the img element proves Leaflet booted AND asked the
  // same-origin proxy for tiles.
  await expect(tilePane(page).first()).toBeAttached({ timeout: 10_000 });
});

test("click-to-add drops a visible marker (guards the icon-404 gotcha)", async ({ page }) => {
  // Before click: no markers.
  await expect(markerIcon(page)).toHaveCount(0);

  // Click roughly the center of the map. Leaflet draws the container at the
  // iframe's full size; clicking the middle avoids the layers control
  // (top-right) and the attribution (bottom-right).
  const mapBox = await container(page).boundingBox();
  expect(mapBox).not.toBeNull();
  if (!mapBox) return;
  await page.mouse.move(mapBox.x + mapBox.width / 2, mapBox.y + mapBox.height / 2);
  await page.mouse.click(mapBox.x + mapBox.width / 2, mapBox.y + mapBox.height / 2);

  // A marker is added. Assert it has NON-ZERO rendered size — the inline-SVG
  // divIcon has a fixed 26x26 size, so a zero-size marker means the icon
  // failed to mount (the default-PNG-icon 404 failure mode).
  const pin = markerIcon(page).first();
  await expect(pin).toBeVisible({ timeout: 5_000 });
  const box = await pin.boundingBox();
  expect(box, "marker must have non-zero rendered size").not.toBeNull();
  if (!box) return;
  expect(box.width).toBeGreaterThan(0);
  expect(box.height).toBeGreaterThan(0);
});

test("a dropped marker persists across reload", async ({ page }) => {
  const mapBox = await container(page).boundingBox();
  expect(mapBox).not.toBeNull();
  if (!mapBox) return;
  await page.mouse.click(mapBox.x + mapBox.width / 2, mapBox.y + mapBox.height / 2);
  await expect(markerIcon(page).first()).toBeVisible({ timeout: 5_000 });

  // Pin count in the toolbar reflects the doc (the adapter wrote it to the
  // hidden field on docChanged). Wait for it to flip, then save.
  await expect(page.locator("#pin-count")).toContainText("1", { timeout: 5_000 });
  await page.locator("#save").click();
  await expect(page.locator("#save-status")).toContainText(/saved/i, { timeout: 5_000 });

  await page.reload();
  await ready(page);
  // After reload the saved doc is re-hydrated into the mount marker; the frame
  // renders it. Pin count and marker element should both reflect it.
  await expect(page.locator("#pin-count")).toContainText("1", { timeout: 5_000 });
  await expect(markerIcon(page).first()).toBeVisible({ timeout: 5_000 });
});

test("read-only disables click-to-add", async ({ page }) => {
  await expect(markerIcon(page)).toHaveCount(0);

  // Toggle read-only via the toolbar button (the demo sends setReadOnly).
  await page.locator("#readonly").click();
  await expect(page.locator("#readonly")).toHaveClass(/active/);

  const mapBox = await container(page).boundingBox();
  expect(mapBox).not.toBeNull();
  if (!mapBox) return;
  await page.mouse.click(mapBox.x + mapBox.width / 2, mapBox.y + mapBox.height / 2);

  // Give the frame a beat to (not) react; no marker should appear.
  // eslint-disable-next-line playwright/no-wait-for-timeout
  await page.waitForTimeout(500);
  await expect(markerIcon(page)).toHaveCount(0);
});

test("side-panel card click flies the map and the frame applies it", async ({ page }) => {
  // Prove the map actually re-centered, not just that a CSS class flipped. We
  // read it from the canonical doc the frame mirrors into the host's hidden
  // field: after flyTo, Leaflet's moveend syncs doc.lat/lng to the new center,
  // and the frame emits docChanged (400ms debounce) which the adapter writes
  // back to input[name="map_doc"]. We deliberately do NOT sample the
  // .leaflet-map-pane transform: Leaflet resets it to translate3d(0,0,0) once a
  // zoom-changing flyTo settles (it re-homes the pixel origin), so it is not a
  // reliable "did the map move" signal.
  const latOf = () =>
    page.locator('input[name="map_doc"]').evaluate((el) => {
      try {
        return JSON.parse((el as HTMLInputElement).value).lat as number;
      } catch {
        return NaN;
      }
    });

  const latBefore = await latOf(); // baseline: the beforeEach reset lat=20

  // Click the Tokyo card (lat ≈ 35.68).
  const card = page.locator('.card[data-label="Tokyo"]');
  await expect(card).toBeVisible();
  await card.click();

  // flyTo animates ~800ms, then a 400ms docChanged debounce. Poll the hidden
  // field until the center reflects Tokyo (proving the postMessage reached the
  // frame and the map moved).
  await expect
    .poll(latOf, { timeout: 5_000 })
    .toBeGreaterThan(30); // Tokyo ~35.68; the world-view baseline was 20

  expect(await latOf(), "center must have moved from the baseline").not.toEqual(latBefore);
  // The clicked card is visually active.
  await expect(card).toHaveClass(/active/);
});
