# testdata

## posthog-js-1.418.17.min.js

The real posthog-js browser bundle (array.js), version 1.418.17, MIT
licensed (PostHog, Inc.). Captured 2026-08-25 from the `/static/array.js`
route of a running self-hosted PostHog instance during the live dogfooding
session that shaped this package.

Why it is vendored: the e2e suite (`e2e_test.go`) runs the REAL SDK in a
real Chrome against the plugin's relay, with a fake ingestion upstream —
no network, no vendor account. Pinning the exact bundle keeps that hermetic:
the suite's assertions (flags wire shape, beacon compression, bot
detection) are tied to this version's behavior, so a floating SDK would
turn an SDK upgrade into a CI mystery instead of a deliberate refresh.

How to refresh it:

1. `curl -o posthog/testdata/posthog-js-<version>.min.js
   http://<running-posthog>/static/array.js` (self-hosted, or
   `https://us-assets.i.posthog.com/static/array.js` for US cloud).
2. Update the version in the filename and in `e2e_test.go`'s loader.
3. Delete the old file, re-run `go test -count=1 ./posthog/` and read the
   empirically-discovered request shapes from the test log if anything
   fails — a new SDK version may speak `/i/v0/e/` or a new flags shape.
