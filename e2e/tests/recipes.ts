// Shared constants for the two recipe journeys.
//
// The recipes run as their own servers alongside the plugin gallery (see the
// webServer array in playwright.config.ts), so their specs cannot use the
// suite-wide baseURL. They build absolute URLs from these instead.
//
// Ports sit just above the gallery's 8123 and are fixed rather than
// OS-assigned: playwright.config.ts has to know them to health-check each
// server before the suite starts.

export const BLOGSITE_PORT = 8124;
export const BLOGAPP_PORT = 8125;

export const BLOGSITE = `http://localhost:${BLOGSITE_PORT}`;
export const BLOGAPP = `http://localhost:${BLOGAPP_PORT}`;

/** The demo admin password recipes/blogapp falls back to when BLOG_ADMIN_PASSWORD is unset. */
export const ADMIN_PASSWORD = "demo";
