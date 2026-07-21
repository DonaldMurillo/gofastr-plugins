// Playwright e2e config — runs the SAME user journeys in WebKit (Safari's
// engine) and Chromium. WebKit is first-class here: every Safari-only bug this
// project has hit (opaque-origin CSP 'self' resolution, selection loss on
// click) was invisible to Chrome-based harnesses.
import { defineConfig, devices } from "@playwright/test";

const PORT = 8123;

// The dogfood shots (tests/shots.spec.ts) are a visual-review tool, not a
// pass/fail gate: they write PNGs for humans and assert nothing. They run ONLY
// under their own project, and only when SHOTS=1 (npm run shots) adds it — so
// the normal run collects them nowhere and reports ZERO skips instead of a wall
// of skipped shot rows. See the testIgnore on the default projects below.
const SHOTS = process.env.SHOTS === "1";
const SHOTS_RE = /shots\.spec\.ts/;

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
  webServer: {
    command: "go run ./example",
    cwd: "..",
    port: PORT,
    env: { PORT: String(PORT) },
    reuseExistingServer: false,
    timeout: 60_000,
  },
  projects: [
    // Shots run in their own chromium-based project, present ONLY under SHOTS=1
    // (npm run shots). Keeping them out of the default projects is what makes the
    // normal run report zero skips.
    ...(SHOTS ? [{ name: "shots", use: { ...devices["Desktop Chrome"] }, testMatch: SHOTS_RE }] : []),
    // Desktop engines: the full journey + a11y suites. Shots are excluded here so
    // they are not collected-then-skipped in the default run.
    { name: "webkit", use: { ...devices["Desktop Safari"] }, testIgnore: [/mobile/, SHOTS_RE] },
    { name: "chromium", use: { ...devices["Desktop Chrome"] }, testIgnore: [/mobile/, SHOTS_RE] },
    // Mobile engines (Phase-1 mobile gate): narrow viewport + touch, running
    // the dedicated mobile journeys. iPhone = WebKit, Pixel = Chromium. testMatch
    // already keeps shots (and everything non-mobile) out.
    { name: "mobile-safari", use: { ...devices["iPhone 13"] }, testMatch: /mobile/ },
    { name: "mobile-chrome", use: { ...devices["Pixel 7"] }, testMatch: /mobile/ },
  ],
});
