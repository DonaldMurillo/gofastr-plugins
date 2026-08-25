# posthog

PostHog behind your origin, in one call. This is the packaged version of
the PostHog recipe from gofastr's
[analytics-recipes](https://github.com/DonaldMurillo/gofastr/blob/main/framework/docs/content/analytics-recipes.md)
doc: the relay route table, the page bootstrap, and the identity
endpoint, composed instead of re-derived.

```go
p := posthog.New(posthog.Config{
	Key:    "phc_...", // the public project API key; required
	Region: "us",      // "us" (default) | "eu"
})
app.RegisterPlugin(p)  // relay routes + {mount}/boot.js + {mount}/whoami
host.RegisterExternalScript(p.ScriptURL())
// or, the same thing: p.Attach(host)
```

Unlike every other package in this repo, this is **not a sandboxed
iframe plugin**. posthog-js instruments the whole host document — page-
views, rage clicks, session replay — so it runs in the host page by
design and cannot be fenced. What stays first-party is the wire: the
visitor's browser only ever talks to your origin, the strict default
CSP (`default-src 'self'`) needs no exceptions, and no third-party
cookie ever lands on your domain. The isolation contract here is
[`battery/relay`](https://github.com/DonaldMurillo/gofastr/blob/main/battery/relay/relay.go)'s,
not the plugin cage's.

## What traffic flows where

With the default mount (`/__gofastr/t`):

| Browser requests | Served by | Goes to (us / eu) |
|---|---|---|
| `{mount}/boot.js` | your app (rendered bootstrap, ETag + immutable on `?v=`) | — |
| `{mount}/whoami` | your app (identity from the session) | — |
| `{mount}/ph-assets/**` | relay, `CacheOK` | `us-assets.i.posthog.com` / `eu-assets.i.posthog.com` |
| `{mount}/ph/**` | relay (beacons: `/e`, `/s`, `/i/v0/e`, `/flags`, `/decide`, `/batch`) | `us.i.posthog.com` / `eu.i.posthog.com` |

`ui_host` in the bootstrap stays the **real** region UI
(`us.posthog.com` / `eu.posthog.com`) and is never relayed: the toolbar
and session-replay player load UI assets from that host directly, and
pointing it at the relay breaks them. The SDK itself is loaded through
the relay (`{mount}/ph-assets/static/array.js`).

The bootstrap: init with `capture_pageview:false` +
`capture_pageleave:false`, identity resolved from `{mount}/whoami`
(`{"id":...}` or `{"id":null}`) with a generation guard, transitions
anon→A `identify`, A→anon `reset`, A→B `reset` then `identify`, the
initial `$pageview` fired after identity, and one `$pageview` per
`gofastr:navigate` (never `beforenavigate` — it is cancelable, and
counting there records visits that never happened).

## Config

| Field | Default | Meaning |
|---|---|---|
| `Key` | — (required) | Project API key, `phc_...`. **Panics** on the secret shapes: `phx_` (personal) and `sk_` (server) would ship to every visitor in the served bootstrap. |
| `Region` | `"us"` | `"us"` or `"eu"`. Picks both relay upstreams and the `ui_host`. Anything else panics at `New`. |
| `SelfHost` | Point every route (assets, ingestion, ui_host) at one self-hosted PostHog origin, e.g. the docker hobby deploy at `http://localhost:8000`. Mutually exclusive with `Region`. |
| `Path` | `relay.DefaultPath` (`/__gofastr/t`) | Relay mount; every route this package serves lives under it. Validated by `relay.New`. |
| `SessionReplay` | `false` | Raises the ingestion route's body cap from the relay's 8 MiB default to 64 MiB, what replay uploads reach. Read it as an egress number: every accepted byte is billed to your bandwidth. |
| `RespectDNT` | `false` | Visitors whose browser reports Do-Not-Track get nothing: no SDK script, no beacons. |
| `Identify` | `handler.GetUser` + recipes' normalization | Resolves the whoami answer. A `string` principal passes through, a `fmt.Stringer` is `String()`ed, anything else is anonymous; return `ok=false` to force anonymous. |

`New` renders the bootstrap once — the config (including the key) is
`encoding/json`-encoded **into the served bytes**, so no script-tag
attributes are needed and a hostile key value stays inert: Go's JSON
encoder HTML-escapes `<`, `>`, `&`, and a test pins that a key
containing `</script>` never appears raw.

## When PostHog moves an endpoint

PostHog has reshuffled hosts before (the `-assets` split is one), and
when it happens this package's two upstreams may not cover the new
shape. The escape hatch is to stop composing and declare your own relay
alongside — the same mechanism this package uses internally:

```go
app.RegisterPlugin(relay.New(relay.Config{
	Routes: []relay.Route{
		{Prefix: "ph-new/", Upstream: "https://us.i.posthog.com/new-endpoint-base",
			Methods: []string{"GET", "POST"}},
	},
}))
```

…and point the SDK at it with the per-endpoint overrides, or vendor
this package (`go run ./cmd/gofastr-plugin add` does not apply here —
this is a plain Go package, so copy it) and edit the table. Deliberate-
ly, there is no `ExtraIngestPaths` config knob: a list of extra paths
whose upstream is implied rather than declared is how an open proxy
starts.

## Egress, ad-blocks, CSRF — the honest notes

These are inherited from the relay and worth reading once:
[`framework/docs/content/relay.md`](https://github.com/DonaldMurlo/gofastr/blob/main/framework/docs/content/relay.md)
covers the egress-cost model, ad-block honesty (first-party origin
defeats domain-based lists only; path-based rules still match), the CSRF
exemption your app-wide middleware may need for the beacon routes, and
the credential-stripping contract that makes the exemption safe.
