# relayboard — the measured product

A three-screen app whose funnel is measured end to end through this
repository's [`posthog`](../../posthog/) integration: campaign
attribution, identified users, an A/B-tested hero, and a server-side
gate — all against a self-hosted PostHog, all first-party.

```sh
go run ./recipes/relayboard
```

It listens on http://localhost:8099/.

| Variable | Effect |
|---|---|
| `ADDR` | listen address (default `:8099`) |
| `RELAYBOARD_DB` | sqlite file for accounts and sessions (default `relayboard.db` under `os.TempDir`) |
| `POSTHOG_KEY` | PostHog project API key (`phc_...`). Unset: the app runs without analytics |
| `POSTHOG_HOST` | self-hosted PostHog origin (default `http://localhost`) |

With `POSTHOG_KEY` unset the app still runs and every page works: no
plugin, no flag store, `/beta` answers invite-only, and the A/B script
no-ops because `window.posthog` never appears. One log line says so.

## What is here

```
relayboard/
├── main.go            screens, accounts, the flag store, the gate, the A/B script
└── relayboard_test.go HTTP smoke tests against the real builder
```

## The funnel, attributed

Land on `/?utm_source=twitter&utm_campaign=launch`, navigate to
`/pricing`, click **Buy Pro**. The `purchase` event still carries
`utm_source=twitter` even though client-side navigation dropped the
parameters from the address bar — posthog-js registers `utm_*` and
friends from the first URL it sees and attaches them to every capture
afterwards. The click handler lives in the page script below and reads
the `data-buy` / `data-price` attributes off the pricing buttons.

## Identity is real

`/account` renders register / login forms (framework/ui `Form` +
`FormField` in cards; a signed-in session sees its identity and a
sign-out instead), all posting to
[`battery/auth`](https://github.com/DonaldMurillo/gofastr/tree/main/battery/auth)'s
core plugin routes, backed by durable sqlite entity stores. The posthog
integration's `whoami` endpoint answers from that session: anonymous
visitors get `{"id":null}`, a logged-in user gets their id, and the
bootstrap merges the anonymous person into the identified one on login.
No analytics-side identity exists; the app's auth is the only source.

## The A/B hero

The flag `hero-copy-test` (multivariate, variants `control` / `punchy`,
50/50) picks the landing page's heading. The branch happens client-side
in the page script — `posthog.getFeatureFlag('hero-copy-test')` inside
`onFeatureFlags` — and `getFeatureFlag` records the
`$feature_flag_called` exposure PostHog's experiment analysis keys on,
so there is no manual capture to forget. The script is served as an
external script via `uihost.ScriptHandler` + `RegisterExternalScript`,
never inlined: the host's strict default CSP has no inline exceptions,
and the handler gives the file an ETag and an immutable `?v=` for free.

## The server-side gate

`/beta` asks PostHog, per request, whether `beta-access` (boolean, 50%)
is on for the *current subject* — the logged-in user's id, or
`anonymous` — before any HTML leaves the server. The adapter is
`phFlagStore` in `main.go`: a `featureflag.Store` of forty lines of
stdlib HTTP that POSTs `{host}/flags/?v=2` and reads
`{"flags":{"beta-access":{"enabled":true}}}`.

Two things worth knowing:

- **The endpoint is `/flags/?v=2`, not `/decide`.** Current self-hosted
  PostHog answers `/decide` with 403; the local-evaluation flags
  endpoint is the supported replacement.
- **Errors fail closed.** An unknown key returns `(nil, nil)` —
  provably absent, which preserves `BoolDefault`'s fallback semantics —
  and any transport or decode error makes the evaluator answer false.
  A gate that guesses open is not a gate.

## Running it against a real PostHog

The recipe targets a self-hosted instance — the docker
[hobby deploy](https://posthog.com/docs/self-host/deploy/hobby-deploy)
is enough. With it running (it listens on `:8000`):

```sh
POSTHOG_HOST=http://localhost:8000 \
POSTHOG_KEY=phc_yourprojectkey \
RELAYBOARD_DB=./relayboard.db \
go run ./recipes/relayboard
```

Then, in PostHog:

1. **Create the flags.** `beta-access`: boolean, release 50%. And
   `hero-copy-test`: experiment / multivariate, variants `control` and
   `punchy`, 50/50.
2. **The experiment needs the `purchase` event first.** PostHog only
   offers a goal metric it has already seen, so click a buy button once
   (or send one `purchase` event) *before* creating the experiment, and
   pick `purchase` as the goal.

URLs to try:

- `/?utm_source=twitter&utm_campaign=launch` — the attributed landing
- `/pricing` — the conversion buttons
- `/account` — register, and watch the person merge in PostHog
- `/beta` — the gate; register first and PostHog decides per user

## Driving it with automation

posthog-js ships bot detection: it silently drops every capture when
`navigator.webdriver` is set or the user agent looks headless. A
Playwright or chromedp run will load the SDK, fetch config, and capture
nothing. For such runs launch the browser with
`--disable-blink-features=AutomationControlled` and a regular user
agent. Real visitors are unaffected either way — which is also why this
recipe's own tests stay at the HTTP layer.
