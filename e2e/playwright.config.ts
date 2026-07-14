// Playwright e2e config — runs the SAME user journeys in WebKit (Safari's
// engine) and Chromium. WebKit is first-class here: every Safari-only bug this
// project has hit (opaque-origin CSP 'self' resolution, selection loss on
// click) was invisible to Chrome-based harnesses.
import { defineConfig, devices } from "@playwright/test";

const PORT = 8123;

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
    // Desktop engines: the full journey + a11y suites.
    { name: "webkit", use: { ...devices["Desktop Safari"] }, testIgnore: /mobile/ },
    { name: "chromium", use: { ...devices["Desktop Chrome"] }, testIgnore: /mobile/ },
    // Mobile engines (Phase-1 mobile gate): narrow viewport + touch, running
    // the dedicated mobile journeys. iPhone = WebKit, Pixel = Chromium.
    { name: "mobile-safari", use: { ...devices["iPhone 13"] }, testMatch: /mobile/ },
    { name: "mobile-chrome", use: { ...devices["Pixel 7"] }, testMatch: /mobile/ },
  ],
});
