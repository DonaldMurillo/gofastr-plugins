// Playwright e2e config — runs the SAME user journeys in WebKit (Safari's
// engine) and Chromium. WebKit is first-class here: every Safari-only bug this
// project has hit (opaque-origin CSP 'self' resolution, selection loss on
// click) was invisible to Chrome-based harnesses.
import { defineConfig, devices } from "@playwright/test";
import { BLOGAPP_PORT, BLOGSITE_PORT } from "./tests/recipes";

// Ports are env-overridable so a second git worktree can run the suite at the
// same time as this one. Unset, they keep the original fixed values.
const PORT = Number(process.env.E2E_PORT ?? 8123);

// The dogfood shots (tests/shots.spec.ts) are a visual-review tool, not a
// pass/fail gate: they write PNGs for humans and assert nothing. They run ONLY
// under their own project, and only when SHOTS=1 (npm run shots) adds it — so
// the normal run collects them nowhere and reports ZERO skips instead of a wall
// of skipped shot rows. See the testIgnore on the default projects below.
const SHOTS = process.env.SHOTS === "1";
const PROFILE = process.env.LOAD_PROFILE === "1";
const SHOTS_RE = /shots\.spec\.ts/;
// The load profile is a measurement run on demand (LOAD_PROFILE=1), not part of
// any suite: it drives the producer at its full 6,000 lines/s, which is exactly
// the rate the normal e2e deliberately avoids (#66).
const PROFILE_RE = /loadprofile\.spec\.ts/;

export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  fullyParallel: false, // journeys share one persisted demo doc; run serially
  workers: 1,
  retries: 1, // absorb rare UI timing flakes; real failures fail twice
  reporter: [["list"]],
  use: {
    baseURL: `http://localhost:${PORT}`,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  // NOTE: this can be narrowed once a gofastr release carries #288 — see
  // gofastr-plugins#78. Upstream shipped GOFASTR_ISOLATION_REWRITE=0, which
  // keeps isolation ON and only stops it remapping an explicitly-assigned
  // port. That is the flag this actually wants; the blunt "off" below is what
  // existed when this was written. The fix is 45 commits ahead of the pinned
  // v0.73.0, so it is not available yet.
  //
  // GOFASTR_ISOLATION=off on every server below. gofastr's framework/isolation
  // auto-activates on a LINKED WORKTREE checkout and remaps ports, including an
  // explicitly assigned PORT. This repo is developed in two linked worktrees
  // and Playwright waits on fixed ports, so an activation would show up as a
  // webServer timeout with no explanation. Pinning it off is cheap insurance
  // against a ghost; see DonaldMurillo/gofastr#268.
  // Three servers, one per app under test. baseURL points at the plugin gallery
  // because that is what most of the suite drives; the recipe journeys use the
  // absolute URLs exported from tests/recipes.ts instead. Playwright starts all
  // three before any test runs and tears them down at the end.
  webServer: [
    {
      command: "go run ./example",
      cwd: "..",
      port: PORT,
      env: {
        PORT: String(PORT),
        GOFASTR_ISOLATION: "off",
        // The logstream flood journey asserts a property of being ABOVE the
        // frame's render ceiling (~1,480 lines/s), not of 6,000 specifically.
        // At the demo's headline rate a two-core CI runner hosting this
        // producer, a browser and the driver at once is pinned hard enough
        // that the journey's own in-page waits time out — measuring the runner
        // rather than the plugin (#66). 2,500 still overruns the ceiling by
        // 1.7x. The demo binary keeps 6,000 for humans.
        // Overridable so the on-demand load profile can drive the demo's real
        // 6,000 (LOAD_PROFILE runs set GOFASTR_DEMO_FAST_LPS=6000). The suite
        // itself stays at 2,500.
        GOFASTR_DEMO_FAST_LPS: process.env.GOFASTR_DEMO_FAST_LPS ?? "2500",
      },
      reuseExistingServer: false,
      timeout: 60_000,
    },
    {
      command: "go run ./recipes/blogsite",
      cwd: "..",
      port: BLOGSITE_PORT,
      env: { PORT: String(BLOGSITE_PORT), GOFASTR_ISOLATION: "off" },
      reuseExistingServer: false,
      timeout: 60_000,
    },
    {
      // In-memory DB (BLOG_DB unset) so each run starts from the same seed and
      // the authoring journeys below cannot inherit a post an earlier run left
      // behind.
      command: "go run ./recipes/blogapp",
      cwd: "..",
      port: BLOGAPP_PORT,
      env: { PORT: String(BLOGAPP_PORT), GOFASTR_ISOLATION: "off" },
      reuseExistingServer: false,
      timeout: 60_000,
    },
  ],
  projects: [
    // Shots run in their own chromium-based project, present ONLY under SHOTS=1
    // (npm run shots). Keeping them out of the default projects is what makes the
    // normal run report zero skips.
    ...(SHOTS ? [{ name: "shots", use: { ...devices["Desktop Chrome"] }, testMatch: SHOTS_RE }] : []),
    // The load profile runs on BOTH engines when asked for: the question it
    // answers (#66) came from webkit, and chromium is the control.
    ...(PROFILE
      ? [
          { name: "profile-webkit", use: { ...devices["Desktop Safari"] }, testMatch: PROFILE_RE },
          { name: "profile-chromium", use: { ...devices["Desktop Chrome"] }, testMatch: PROFILE_RE },
        ]
      : []),
    // Desktop engines: the full journey + a11y suites. Shots are excluded here so
    // they are not collected-then-skipped in the default run.
    { name: "webkit", use: { ...devices["Desktop Safari"] }, testIgnore: [/mobile/, SHOTS_RE, PROFILE_RE] },
    { name: "chromium", use: { ...devices["Desktop Chrome"] }, testIgnore: [/mobile/, SHOTS_RE, PROFILE_RE] },
    // Mobile engines (Phase-1 mobile gate): narrow viewport + touch, running
    // the dedicated mobile journeys. iPhone = WebKit, Pixel = Chromium. testMatch
    // already keeps shots (and everything non-mobile) out.
    { name: "mobile-safari", use: { ...devices["iPhone 13"] }, testMatch: /mobile/ },
    { name: "mobile-chrome", use: { ...devices["Pixel 7"] }, testMatch: /mobile/ },
  ],
});
