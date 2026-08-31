# Changelog

All notable changes to gofastr-plugins. Follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are
`0.x-phase` until the platform API stabilises.

### Fixed — the load profile blamed the producer when the browser was the one that died (2026-08-31)

CI went red on the opt-in profile job. The reading is the interesting part:

```
webkit    linesPerSec 0   ticks -1   evaluateMs -1   delivered 0   dropped 0
          serverMs [15, 9, 10, 6, 9, 7]
chromium  linesPerSec 6338   lagP95 2 ms   delivered 10948          PASS
```

The #94 fix worked exactly as designed: instead of hanging for the full 180 s,
webkit recorded that the page never answered and finished in 44 s. On a two-core
runner at 6,000 lines/s it is not slow, it is **completely pinned**: nothing
delivered, and it cannot evaluate `1+1` within ten seconds.

Then the one assertion in the file failed with **"the producer never reached
flood rate"**, which is precisely backwards. `linesPerSec` is derived from
counters read *out of the page*, so a pinned page reports zero for the same
reason a dead producer does, and the assertion could not tell them apart. The
`serverMs` values of 6 to 18 ms disprove it in the same JSON blob it printed.

Now it distinguishes them. When the page answered, the original assertion is
unchanged. When it did not, the test proves the producer was alive **from
outside the page** and lets total saturation stand as the reading it is: the
extreme measurement, not a missing one.

Both branches verified by forcing the condition, because the new one only runs
on hardware slow enough to pin webkit and this laptop is not:

```
pinned page + healthy server  -> passes, annotated "saturated"
pinned page + slow server     -> fails, "this is a stalled producer,
                                 not a saturated browser"
```

An escape hatch that could never fail would have been worse than the wrong
assertion it replaced.

### Changed — gofastr v0.76.0 (2026-08-30)

A quiet bump. Builds clean, the full Go suite passes, 232 chromium journeys
pass, and `go.sum` moves only gofastr's own lines.

Expected to be quiet, and checked rather than assumed. The 36 commits between
v0.75.0 and this tag are Web Bot Auth and agent-surface work
(`core/webbotauth`, `core/jcs`, `core/mcp`), an audit-tests branch of middleware
and `uinoderender` hardening, and the release commit. **Nothing touched
`framework/pluginhost`, `core-ui/style` or `framework/embed`**, which is the
whole of this repo's exposure.

One commit named "clear host theme state" and was worth opening for that reason,
since token bridging is exactly what this repo depends on. It turned out to be
`core/mcp/widgetclient.js`, the MCP Apps widget client, a different subsystem
from pluginhost's bridge.

The `gofastr-main` tripwire had already built this repo against `c9ffac5` and
reported the third-party dependency set unchanged, so the tag arrived
pre-verified.

### Added — a journey for mermaid's parse error, and a correction to the sweep's coverage map (2026-08-30)

`mermaid` reports invalid syntax well, with a caret under the offending token,
and removes the stale diagram rather than leaving it standing:

```
valid     "postMessage renders Host page Opaque-origin frame SVG diagram"
invalid   "Parse error on line 2: ... Expecting 'AMP', 'COLON', 'PIPE'..."  svg count 0
```

The journey asserts both, and the second is the one that matters. Reporting the
error is good; leaving the previous diagram up **while** reporting it would be
worse than either, because the picture would no longer describe the source and
nothing would say which to believe.

## The coverage map I published was wrong

The #102 sweep classified plugins by whether their spec calls `page.route`. That
is one failure mechanism, not the definition, and it mislabelled at least two:

- **scanner** was listed as having no failure journey. It has one. Camera
  failure is a *state* there by deliberate design (`__scannerCameraState` is
  `idle | live | denied | unsupported`, with a comment saying "CAMERA FAILURE IS
  A STATE, NOT AN EXCEPTION"), and the spec asserts it. No route needed.
- **mermaid** had a genuine gap, but not one a route sweep could ever find: its
  failure is local, a parse error, with no request to intercept.

So the detector answered a narrower question than the one I asked it, and I
reported its answer as the coverage picture. The honest count is better than I
said in one place and the gap was somewhere I was not looking.

### Fixed — genui reported a failed compose as "nothing composed yet" (2026-08-30)

The composition is produced host-side, so `POST /compose` is the whole
pipeline. `failGeneration` was wired only to `pollComposition`, so the likeliest
failure of all, the compose request not landing, reached nothing: the promise
chain rejected, `compose()` had no `catch`, the state stayed `idle`, and the
demo's verdict kept reading **"nothing composed yet"**. The caller's
`.catch(function () {})` swallowed the rejection, so not even a console line
survived.

A request that failed looked exactly like one never made.

Measured, then fixed, then measured again on both engines:

```
before   blocked -> state idle      verdict "nothing composed yet"
after    blocked -> state failed    verdict "generation failed"
after    allowed -> state rendered  verdict "rendered 5 nodes"
```

The fix routes a rejected compose into the same `failed` state the polling
paths already used, excluding `E_SUPERSEDED` because a newer prompt winning is
not a failure. The plugin already had the state and the demo already had the
message; nothing ever reached them.

Third real defect from the #102 sweep, after pdf and imageedit, and the same
shape every time: the failure path was designed, was documented, and was
unreachable, because no journey ever drove it.

### Added — a journey for whiteboard drawing into a room it cannot reach (2026-08-30)

The dangerous failure for a collaborative tool is not an error, it is silence:
you keep drawing, the strokes never leave, and the board looks exactly as it
does when they do.

`whiteboard` already handles this, with the right words:

```
stream blocked   "offline — drawing locally"
stream allowed   "synced · 1 participant"
```

Correct, and proved by nothing until now.

The journey asserts the stream was actually intercepted before judging the
status. That guard earned its place immediately: the first version of this
probe used a glob (`**/whiteboard/room/stream`) that did not match the real
request because of its `?docId=` query string, blocked nothing, and reported
demo-page prose as the status. It looked exactly like "the plugin says nothing
when it goes offline". A URL predicate matches; a glob did not.

One observation rather than a claim: the pre-existing
`drawing offline on both sides converges after reconnect` journey went flaky
once in webkit during this work and passed on retry, then passed two further
webkit runs cleanly with the new test present. It does not look related, and
it is recorded here rather than dismissed because "did not reproduce locally"
was also true of the blogapp race that then failed twice on CI.

### Added — a journey for formbuilder's refused save (2026-08-30)

Same treatment as calendar, on the plugin where the schema **is** the document:
Go validates and stores every save and the frame keeps nothing durable, so a
save the server never receives must not read like one it accepted.

It already gets this right, and distinctly:

```
save blocked   "Refused by the server: E_NETWORK"
save allowed   "Saved, Go validated 6 fields, 8 rules"
```

Correct and, until now, proved by nothing. The journey asserts the save was
**attempted** before judging the outcome, and separately that a failure does not
contain the word "Saved". Both halves matter: two earlier probes of other
plugins fired no request at all and produced a status line identical to the
success case, which reads exactly like a plugin that says nothing on failure.

Running tally for the #102 sweep: two real defects fixed (pdf #103,
imageedit #104), three plugins proved correct by journey (calendar, formbuilder,
and richtext which already had one), one judgment call left open (datagrid's
dead `saveResult`), and six plugins still without a failure journey.

### Added — a journey for calendar's refused move, and the sweep's other half reframed (2026-08-29)

`calendar` already refuses a failed move out loud: `move refused: ...` on the
status line, with the chip left showing the server's last known truth rather
than where the pointer suggested. Correct, and proved by nothing. Given this
exact class has now recurred three times here (richtext fixed it once, then
imageedit and pdf rebuilt it wrong), a plugin doing it right deserves a test
that keeps it that way.

The journey asserts the request was actually attempted before asserting
anything about the outcome. That is not decoration: **two earlier ad-hoc
attempts at this measurement fired no request at all** and reported a status
line identical to the success case, which reads exactly like "the plugin says
nothing on failure". The same thing happened while probing datagrid. A failure
journey that silently never triggers the failure passes every assertion by
doing nothing.

## The ticket's framing was wrong, and doing the work is what showed it

#102 proposed adding a `window.__<plugin>Debug` error mirror to every adapter so
tests could observe failures. Having now written three of these journeys, that
is the wrong instrument in most cases. pdf, imageedit and calendar are all
asserted on **what the person actually sees**: a status line, an error banner.
That is a better test than a debug counter, because a mirror can be correct
while the user is told nothing, which was the whole defect in two of them.

The mirror earns its place only where the observable is genuinely invisible,
which so far is `sqlnotebook`'s wasm relay: a fetch the frame cannot make, whose
failure has no natural UI. So the remaining sweep is cheaper than the ticket
claimed: mostly journeys, rarely new product surface.
### Fixed — and the fix for that race introduced a second one (2026-08-30)

Waiting on the POST response was half right. It removed the stale-homepage
problem and created a navigation collision: the POST answers **303**, the
browser then navigates to `/admin`, and the caller's next `goto` raced that.
CI named it precisely:

```
page.goto: Navigation to "http://localhost:8125/" is interrupted by
another navigation to "http://localhost:8125/admin"
```

Which is why the test kept failing on the same shard after the first fix
merged, on a PR that touched only calendar.

It now waits for the **redirect to land** rather than for the response to
arrive: `page.waitForURL(u => u.pathname.startsWith("/admin"))`. Landing on
`/admin` implies the POST both completed and succeeded, so the explicit status
assertion goes too.

Verified against the same 2.5 s injected delay, on both engines:

```
webkit    waited 2577 ms for the redirect, then navigated cleanly
chromium  waited 2542 ms for the redirect, then navigated cleanly
```

Both properties at once: it waits out a slow POST, and the following navigation
does not collide.

The honest shape of this: a real diagnosis, a fix that addressed it, and a
second defect the fix itself introduced, caught by CI on the next run rather
than by me. The first change was not wrong, it was incomplete, and "the tests
pass locally" would have hidden the difference both times.

### Fixed — the blogapp publish/unpublish journey raced its own POST (2026-08-29)

`publish and unpublish move a post on and off the public site` has failed twice
on CI webkit, both times on an unrelated PR, and passes locally every time
(8 runs, two engines). The second failure named the cause: after unpublishing,
the post's link was still on the homepage.

The test clicked the status form's button and immediately called `page.goto`.
The button submits a form, so the click starts a POST **and** a navigation, and
the `goto` raced that round trip. On a loaded runner the homepage renders before
the status change is committed.

The part that makes it fail rather than flake-and-recover: `toHaveCount(0)`
retries for five seconds, but a locator re-query does not reload the page. Once
a stale homepage is loaded, no amount of retrying can see the new state. The
assertion was retrying against a snapshot that could never change.

It now waits for the POST response before navigating. Demonstrated rather than
argued, by injecting a 2.5 s delay into the status route:

```
OLD  navigated away without awaiting the POST
NEW  awaited the POST for 2536 ms before navigating (status 303)
```

Worth being precise: that shows the timing difference the fix makes, not a
red-to-green reproduction of the original assertion, which needs the runner's
own load to surface.

Same shape as #71 and the load profile in #94: an assertion whose correctness
quietly depends on the machine being fast enough. Three now, in three places.

### Changed — gofastr v0.75.0, and sqlnotebook builds its AssetServer from the module (2026-08-29)

The release carrying the two bugs this repo filed today. Verified rather than
assumed: `compare v0.75.0...06956a1a` answers `behind`, so the tag contains
them.

**gofastr#300** shipped as `ClientModule.AssetServer(prefix, specs)`, which
reads the module's assets and threads `Manifest.CSP` through `WithCSP` itself.
`sqlnotebook/handlers.go` now uses it. The hand-threaded
`NewAssetServer(...).WithCSP(...)` it replaces was correct, but correct by
remembering a line, and forgetting that line produced a frame that validated,
mounted, and refused to compile WebAssembly with nothing naming the cause.

**gofastr#303** now derives a missing `ContentType` from the filename. The five
explicit ones in `assets.go` stay: the default is a safety net for what an
author forgets, not a reason to stop saying what a file is, and
`application/wasm` is worth stating out loud.

`TestServedFrameHeaderCarriesWasmUnsafeEval` is the check that mattered through
this change, and it still passes: the served `script-src` carries
`'wasm-unsafe-eval'` after the swap. Asserting the manifest would have proved
nothing, which was the whole of #300. Full Go suite green, 14/14 sqlnotebook
journeys on both engines, no third-party dependency movement.

Filed at roughly 06:00, fixed upstream by 19:19, released and adopted here the
same day.

### Fixed — imageedit lost edits silently when the autosave failed (2026-08-29)

The same shape as the pdf defect below, with higher stakes. `imageedit`'s
adapter has always reported the autosave's verdict as `saveResult`
(`{ok, status, code}`, including `E_NETWORK`), and the frame never listened, so
the event was dropped by the router.

Measured by aborting only `/imageedit/save` and rotating the image:

```
save blocked   status: "Image 960x640 resident, preview is local, export is server-side"
save allowed   status: "Image 960x640 resident, preview is local, export is server-side"
```

**Byte-identical.** A person crops, annotates or redacts, the document fails to
persist, and nothing anywhere says so. The work is gone on reload.

The frame now surfaces a failure: `Not saved (E_NETWORK). Your edits are only in
this tab.` Success stays quiet on purpose, matching how this repo already treats
transient "Saved" chrome, because only a failure needs a decision from the
person who made the edits.

`datagrid` drops the same event and was checked too. There it is weaker: its
`/save` carries view state, not cell data, and cell edits go through
`requestCellWrite`, which the frame does handle. A silent failure costs a column
layout rather than work, so it is left for the #102 sweep rather than fixed here.

Fourth failure-path defect today, third found by the sweep that #102 proposes.
The pattern is consistent enough to name: an adapter reports a failure
correctly, the frame's half was never written, and the router drops the event
without a sound. Nothing in review looks wrong, because the sending side reads
exactly as intended.

### Fixed — pdf showed nothing when the host could not fetch the document (2026-08-29)

`pdf/host/adapter.js` has always sent a `renderError` event when the document
relay fails, and its comment has always said failures "surface as a renderError
event the frame turns into a visible error rather than a blank page".

The frame had no handler for it. Its router dispatches `init`, `themeChanged`,
`loadBytes`, `documentBytes`, `requestSave`, `exportResult` and `teardown`, and
dropped everything else, so the event went nowhere. `renderError` existed in the
frame only as something it **sends upward**; the adapter was using it as a
host-to-frame event and nothing on the receiving side had been written.

Measured by aborting only `/doc/{id}`: 366 characters of toolbar, no error text
anywhere on the page, and the only trace a console line nobody is watching. A
full PDF toolbar over an empty document area reads as a broken plugin, not as a
document that could not be loaded.

The frame now handles it and routes the message to the same
`toolbar.setStatus(msg, "error")` the assembler already uses for its own
failures. Verified both engines: `host fetch failed: Failed to fetch` on
chromium, `Load failed` on webkit. A journey covers it.

Found by the sweep #102 describes, on the plugin that ticket nominated as
worst-first. The adapter's half was correct and the intent was documented; only
the frame's half was missing, which is why nothing looked wrong in review. The
third failure-path defect today, after the two in sqlnotebook, and the first in
a plugin that has been shipped for weeks.

### Fixed — a failed engine fetch left sqlnotebook's mount unable to recover (2026-08-29)

The host adapter sets `frame.__sqlnbInitSent = true` **before** fetching the
wasm, so a re-fired ready cannot post a second init. On failure that flag stayed
set while nothing had been sent, so the guard claimed an init that never
happened and any later ready returned early. The frame waited for an engine no
one could ask for again.

Measured by aborting only `sql-wasm.wasm` and leaving everything else alone:

```
initSent 0    fetchError "Failed to fetch"    __sqlnbInitSent true
```

Zero inits sent, and the guard saying otherwise. It now clears on failure, which
is the invariant the guard actually wants: one **successful** init per mount.
After the fix the same probe reports `__sqlnbInitSent false`, and with the road
open again the engine comes up on both engines, SQLite 3.49.1 from 658,410
bytes.

A journey covers it, and asserts the degraded state is *honest* rather than
merely empty: the frame must say "waiting for engine", `ready` must be null, and
the guard must not claim a send that did not occur. 14/14 pass on both engines.

Second defect from the same adversarial read as the schema sidebar. Both were
in the failure paths rather than the happy path, which is where a merged plugin
with green gates hides them.

### Fixed — sqlnotebook's schema sidebar never noticed a table being created (2026-08-29)

`refreshSchema()` ran once at boot and never again, so typing `CREATE TABLE` in
the notebook left the sidebar showing the seed for the rest of the session. In a
SQL notebook the sidebar is the only map of the database, and DDL is an ordinary
thing to type into one. It now refreshes after any statement that succeeded: two
small queries against an in-memory database, and not after a failure.

Found by an adversarial read of the merged plugin rather than by anything
failing. Worth recording what that read got wrong as well as what it got right,
since the wrong part came first.

**The hypothesis that did not survive.** `db.exec(sql)` materialises the whole
result set and the 500-row cap is applied afterwards, which looked like it made
`docs/sqlnotebook.md`'s claim false: that the cap means a runaway query "costs
one bounded message instead of a hung frame". Measured instead of argued about:
a recursive CTE producing **2,000,000** rows answered in 458 ms, and
**20,000,000** rows in 5.8 s, both correctly truncated to 500 with the frame
responsive throughout. The claim holds and the code was fine.

**What the same read did find.** Message validation gates on
`event.source === window.parent` and never consults `event.origin`, which is the
platform contract. Identifier interpolation into `PRAGMA table_info` doubles
embedded quotes, which is the correct SQLite escape, and a table named `od"d`
round-trips. The stale sidebar was the one real defect, and a journey now covers
CREATE and DROP on both engines.

### Added — what `origin` reports inside the cage, which is not what it reports outside (2026-08-29)

`docs/plugin-platform.md` says an opaque-origin frame's origin is `"null"`, and
it is right: `event.origin`, as the host sees it for a message from the frame,
is `"null"` on both engines. Measured, not assumed.

What is **not** `"null"` is `location.origin` read inside that frame. It returns
the ordinary URL. Both values are correct answers to different questions, and
the second is a trap: code inside the frame that checks `location.origin` to
decide whether it is sandboxed gets a normal-looking origin and can conclude it
is same-origin with the host. It is not, and `localStorage`,
`document.cookie` and `window.parent.document` all throw `SecurityError` in the
same frame to prove it. The reliable in-frame test is that those throw.

I nearly filed this as a correction to the doc instead of an addition. An
earlier reading of `location.origin` came through Playwright's `evaluate`,
which is the same context that had already produced a false `eval()` result
during the sqlnotebook build, so the number was not trustworthy. Re-measuring
from genuine page script gave the same value, but reading what the paragraph
actually claimed showed it was about `event.origin` and was correct. The
finding was real; the correction would have been wrong.

### Added — the latency gate's readings, per run, as a downloadable artifact (2026-08-29)

The step summary from the previous entry fixed half the problem. It shows a
human one run's numbers, and #71 asks whether p99 **clusters** near the ceiling,
which nobody answers by opening twelve summary pages. GitHub also exposes no API
for step summaries, so I could not verify from here that the change had actually
taken effect, and machinery that has never been seen to fire is
indistinguishable from machinery that cannot.

The gate now also writes `example/latency-gate.json` (p50, p99, count, verdict,
both thresholds, the commit sha and run id), and CI uploads it with
`if: always()`, because a run that TRIPPED the ceiling is the most interesting
point in the series. `gh run download <id> -n latency-gate` makes the sweep the
ticket asks for possible, and makes the mechanism checkable rather than assumed.

Second local sample, for the record: p50 6.40 ms, p99 8.60 ms, after 4.60 and
8.40. Against the 59.80 ms p99 of the CI failure in #71.

### Fixed — the latency gate only reported its numbers when it failed (2026-08-29)

#71 asks a question that turned out to be unanswerable: track the Phase 0
keystroke p99 across runs and see whether it clusters near the 50 ms ceiling.
There was no history to track.

The gate reports through `t.Logf`, and `go test` swallows log output unless
`-v` is set or the test fails. CI runs plain `go test ./... -count=1`. So the
only runs that ever published a p50 or p99 were the ones that tripped the
ceiling, and the ticket's own instruction to compare against a distribution had
no distribution to compare against. Verified by reading a passing run's job log
directly: zero occurrences of the gate line.

**A correction to how this was first established.** The sweep that sent me
looking here queried `select(.name == "go")`, and the job is called
`go (root module)`, so it matched nothing and skipped every run without
fetching a single log. It returned "no numbers" for the wrong reason. The
conclusion survived a proper check, but the evidence originally cited for it
did not support it, and the difference is worth keeping in the record.

The gate now also writes a small table to `GITHUB_STEP_SUMMARY` when it is set,
so every CI run publishes its numbers whether it passes or not. Local runs are
unchanged: `t.Logf` under `-v` is enough there.

First passing measurement, recorded here because until now none existed:

| | p50 | p99 | samples |
|---|---|---|---|
| this laptop | 4.60 ms | 8.40 ms | 100 |
| the CI failure in #71 | 8.00 ms | **59.80 ms** | 100 |

A **7x** gap in the tail on a p50 that barely moved, which is what #71 suspected
and could not show. A gate whose measurement is invisible until it trips can
only be argued about after the fact.

### Fixed — the load profile could not measure the engine it was built to measure (2026-08-29)

The opt-in `LOAD_PROFILE=1` job timed out on webkit, twice, at the full 180
second budget, having produced no number. Chromium finished the same job in 8
seconds. So the one job whose entire purpose is measuring a saturated page could
never report on the engine that actually saturates.

The cause is worth naming precisely: **the instrument depended on the thing it
was measuring.** `page.waitForTimeout` is implemented via the page, so it cannot
resolve while the page's main thread is pinned, which is the exact condition
being profiled. The file's own comment already said control "must not depend on
it" and routed the rate change out of band through the request context. The
timing loop and the final `page.evaluate` never got the same treatment.

Sleeps are driver-side now, and every page-dependent call is bounded. A page
that cannot answer is recorded as data, with sentinels, rather than killing the
run: "webkit could not evaluate 1+1 within 10s under flood" is the finding, and
it belongs in the report instead of a stack trace.

With the instrument fixed, both engines report in about eight seconds each:

| | lines/s | lag p95 | delivered | dropped |
|---|---|---|---|---|
| chromium | 6,013 | 1 ms | 10,852 | 25,223 |
| webkit | 6,012 | 5 ms | 5,980 | 30,092 |

Which refines what #66 concluded. On adequate hardware webkit does **not** stall
under a 6,000 lines/s flood: its event loop stays within 5 ms and it keeps up,
delivering about 55% of what chromium delivers from the same input. The
saturation that raised the question is a property of a two-core runner hosting
producer, browser and driver at once, not of the engine.
### Fixed — the upstream tripwire warned on every run, and documented limits that had expired (2026-08-29)

Two things that had quietly stopped being true.

**The `gofastr-main` job's dependency warning was a false positive by
construction.** It compared `go.sum` before and after adding a `replace`, and a
replace ALWAYS drops the replaced module's own hash lines, so the warning fired
whether or not a real dependency moved. Confirmed against upstream main
`da4e943`: the only delta was gofastr's own `v0.71.2` / `v0.73.0` / `v0.74.0`
entries, and the job still printed "upstream main changes this module's
dependency set". It now compares third-party modules only. Verified both ways:
silent on the real case, and still firing when a genuine new module is added.

A warning that cannot not fire is noise, and noise is how a real one gets
ignored. That is the same argument that retired the awaiting-upstream manifest
this morning, applied to something I had written a few hours earlier.

**`docs/plugin-platform.md` still said WebAssembly was impossible.** The
capability table read *"not today — CompileError, both engines. One CSP token
would fix it"*, written when that was true. v0.74.0 shipped the token and
`sqlnotebook` runs SQLite on it. The row now says opt-in, names the manifest
field and the host call it needs, and carries the measured init times.

A Worker row joins it, because that constraint decides which libraries can live
in the cage and was written down nowhere: `new Worker("same-origin.js")` is a
`SecurityError` on chromium and works on webkit, while a blob Worker works on
both. It is also a fifth entry under "the ones that surprise people", since an
engine divergence means a worker-based plugin can be built and tested happily in
Safari and fail at mount in Chrome, with an error naming the `Worker`
constructor rather than the isolation model.

### Added — `sqlnotebook`, and the answer to whether wasm runs in the cage (2026-08-29)

A real SQLite engine, compiled to WebAssembly, running inside an opaque-origin
frame that cannot open a socket. Measured on both engines: **SQLite 3.49.1,
init 28 ms chromium and 26 ms webkit**, 658,410 engine bytes handed across the
bridge, and zero network requests from the frame while a query runs.

This closes a question that had been standing in for an answer since Phase 0.
`pdf` runs pdf.js worker-free for exactly this reason. gofastr v0.74.0 shipped
the narrow `'wasm-unsafe-eval'` tier (gofastr#255), and this is the first plugin
to opt into it.

**The tier must be handed to the AssetServer, not merely declared.** A manifest
carrying `CSP` changes nothing on its own; the header is assembled by
`AssetServer`, which never reads the manifest. So `handlers.go` threads it
explicitly, and the test asserts the **served response header** rather than that
`Validate()` passed. Checking the manifest would go green on a frame that
refuses WebAssembly, which is the whole of gofastr#300. Verified it can fail:
removing the `.WithCSP` call turns the served `script-src` back into
`script-src <origin>` and the test red.

**The engine cannot fetch itself.** sql.js's documented `locateFile` fails with
`both async and sync fetching of the wasm failed`, because `connect-src 'none'`
is real. The host adapter fetches the `.wasm` (the host page may) and posts the
bytes in; the frame calls `initSqlJs({ wasmBinary })` and never fetches
anything. A database that cannot phone home is the property that makes the tier
worth granting.

**Not DuckDB, on three independent grounds.** Its wasm is 35 MB against this
repo's 20 MB embed cap, which 16.9 MB of was already spent. Its API needs a
Worker, and chromium refuses to construct one from a same-origin script inside
an opaque origin (`SecurityError`); webkit allows it, which is an engine
divergence worth knowing on its own. And sql.js needs no worker at all. The
whole plugin costs 705 KB, taking the embedded tree to 17.7 MB.

`internal/registry` gains a `csp` key so `plugins.json` can express the tier at
all: the parser rejects unknown fields, so the row was unrepresentable without
it. It is the one field that WIDENS the cage, and a reader deciding whether to
adopt a plugin should see that from the index rather than the source.

## Two tests that lied, and what they cost

Worth recording, because both nearly produced a confident wrong conclusion.

**A journey probed `eval("1+1")` inside the frame and got `ALLOWED`** — which
would mean the tier had granted string eval, a real security regression. The
same probe against `pdf`, which has no tier at all, also returned `ALLOWED`.
Playwright's `evaluate` runs in a context the page's CSP does not apply to. The
fix was not to loosen the assertion: origin probes (`localStorage`,
`document.cookie`, `window.parent.document`) ARE faithful and stayed, and the
CSP half moved to the served header, where it can be asserted honestly. The test
carries a comment so nobody re-adds the eval probe.

**A teeth-test said the CSP header survived removing `.WithCSP`**, which briefly
looked like evidence that gofastr#300 was wrong. The regex had matched the
string inside a doc comment rather than the call, which is split across two
lines; the code was never modified. #300 stands.

Also: `pkill -f "go run ./example"` kills the wrapper and leaves the compiled
binary serving. Five stale servers were holding the port, which is why a rebuilt
bundle appeared not to take effect.

### Removed — the awaiting-upstream manifest, having done its job (2026-08-29)

`.github/awaiting-upstream.tsv`, the nightly step that read it, and
`TestAwaitingUpstreamManifestIsWellFormed` are gone. v0.74.0 released all four
commits they tracked, so every row now fires "unblocked" every night, three of
them for tickets that are already closed. That is noise, and noise in a daily
job is how a real signal gets ignored.

The guard's own failure message is what settled it: *"if nothing is awaited,
delete the file and the CI step that reads it rather than leaving both to pass
on an empty list."* Weakening a guard to keep inert machinery alive is the
failure mode this repo spent the day removing, so following the instruction was
the only consistent option.

It was worth having for the six hours it existed. It is what caught that
gofastr#255 had shipped, which is what prompted reading the wasm tier's
implementation, which is what found gofastr#300. Bringing it back is one
revert; the shape is a TSV of `sha / upstream / issue / note` plus a compare-API
lookup against the newest tag.

`gofastr-main`, the daily build against upstream main, stays. That one tracks
drift rather than a list, so it has nothing to go stale.

### Changed — the e2e isolation workaround narrows, and turns out to have been inert (2026-08-29)

`GOFASTR_ISOLATION=off` on all three e2e servers becomes
`GOFASTR_ISOLATION_REWRITE=0`, the knob gofastr#268 shipped in v0.74.0. It
keeps isolation on and only stops it remapping an explicitly assigned port,
which is what a harness waiting on fixed ports needs.

The interesting part is what measuring first turned up, because the old comment
asserted a failure nobody had reproduced.

**The hazard is real, and invisible from the primary checkout.** In a linked
worktree isolation activates by default and remaps: `Addr(":8742")` returns
`":10604"`. In the primary checkout it is inert, `active=false`. Anyone
verifying this change where they normally work would have learned nothing.

**But it could never have reached these servers.** `example`, `blogsite` and
`blogapp` each call `net.Listen` on the raw `PORT` and `http.Serve` the router.
None goes through `App.Start`, which is the only path the remap touches. The
first attempt to reproduce the problem through the app honoured the port in
every configuration, including with no flag at all — which looked like evidence
the fix was unnecessary, and was actually the app bypassing isolation entirely.
Probing the isolation runtime directly is what separated the two.

**The swap is not cosmetic.** `off` disables isolation wholly; this keeps it on.
In a worktree the recipes' sqlite DSN moves to
`.gofastr/isolation/<id>/blog.db`. That is isolation doing its job and is
almost certainly what a worktree wants, but it is a behaviour change rather
than a narrower spelling of the same one, and the ticket did not anticipate it.

Verified where it is observable: the full chromium suite inside a linked
worktree, with this flag, passes 218/218, and `.gofastr/` is created during the
run — so isolation was genuinely active rather than silently off.

### Added — scanner declares its camera requirement, and this app enforces it (2026-08-29)

The scanner needs the camera on the **host page**, because an opaque origin
cannot hold the permission at all: the host captures and hands frames in. Until
now that requirement lived only in prose. `docs/scanner.md` told adopters to
relax the `Permissions-Policy`, and `example/main.go` did it in a comment beside
the config. A prose requirement is one careless edit away from being untrue,
and the symptom of getting it wrong is a `getUserMedia` console error that
reads like a plugin bug.

v0.74.0 carries gofastr#294, so the manifest declares it:

```go
HostRequirements: []string{pluginhost.HostRequirementPrefix + "camera"}
```

**Declaring it grants nothing, and cannot.** A `Permissions-Policy` is the app's
response header, and a plugin must not be able to rewrite it. The example's
`camera=(self)` line is still what opens the camera. What the declaration buys
is that `CheckHostRequirements` names the plugin at boot when a host has not
opted in.

The policy is now a named constant so the boot check and the tests read the
same string the app serves. Two copies would let a test pass while the app
shipped a policy that denies the camera.

Two tests, because upstream's check logs and never fails by design:

- the app's real policy must satisfy every declared requirement, and must
  literally contain `camera=(self)`. The check is deliberately narrow, warning
  only on the empty allowlist `camera=()`, so a policy that dropped the
  directive entirely would stay silent. **Silence is not proof.**
- teeth: the framework default must produce a warning naming both `scanner` and
  the token, or the check is inert and would pass just as happily on an app
  that denies the camera.

The real boot log was checked too, and is silent with the configured policy.

### Removed — calendar's hand-declared palette, and the light-only contrast gap (2026-08-29)

`calendar/demo.go` declared fourteen `--color-*` custom properties whose values
were only the light defaults. They were never design decisions: the old broker
discovered token NAMES by walking `document.styleSheets`, so a sheet still
parsing when the frame booted contributed no names and the frame got a partial
palette. A partial dark palette on light fallbacks rendered near-white text on
white, at 1.12:1. gofastr v0.74.0 carries the fix (gofastr#271): the broker now
reads a fixed 78-token vocabulary from computed style and never walks a sheet,
so declaring names buys nothing. calendar's `:root` is now the same one line as
every sibling demo.

**The guard had to be fixed before the deletion meant anything.** The in-frame
contrast test ran in the default theme only, and the bug it exists to catch is
a *dark* one, so it could not have seen a regression of that exact shape. It
now runs both schemes, and asserts the scheme it asked for is the one in effect,
because a dark test that silently gets light measures the light theme twice.

Its skip path went too. An unresolved token used to `continue`, which meant a
frame that received NO palette passed every pair by measuring nothing. A
palette arriving incomplete is the entire failure mode here, so an unresolved
token is now the finding.

Measured rather than assumed, with the block deleted: the frame resolves the
full palette in both schemes, `oklch(0.96 0.006 80)` on `oklch(0.17 0.006 75)`
in dark and `#18181B` on `#FFFFFF` in light, nothing unset. Light now resolves
`--color-background` to the theme's `#F9FAFB` where the hand-declared block had
been forcing `#ffffff`, so the page is more correct than before, not merely
smaller. 58 a11y and calendar journeys pass on both engines, and the dark page
was screenshotted.

### Changed — gofastr v0.74.0, which unblocks four tickets at once (2026-08-29)

The release the awaiting-upstream manifest was built to watch for. Every fix
this repo was tracking is in it, checked against the compare API rather than
inferred from the changelog:

| awaited | upstream | in v0.74.0 | unblocks |
|---|---|---|---|
| `c0266af3` | gofastr#255, the `wasm-unsafe-eval` tier | yes | #21 |
| `34300c92` | gofastr#268, `GOFASTR_ISOLATION_REWRITE=0` | yes | #78 |
| `ad7a2168` | gofastr#271, token names from a loading stylesheet | yes | #81 |
| `627bf1af` | gofastr#294, a manifest declaring a host permission | yes | #82 |

The bump itself is quiet: builds clean, every Go test passes, and the only
`go.sum` change is the gofastr line. That was not luck. This repo had already
been run against the exact commit v0.74.0 was cut from, on both engines
(chromium 211/211, webkit 211/211), by the drift check that became the
`gofastr-main` job.

**The one breaking change that reaches this repo does not break it.** 24
`framework/ui` components now drop caller `ExtraAttrs` that override keys the
component owns. All nine `ExtraAttrs` uses here were checked one by one:
`recipes/blogsite` and `recipes/blogapp` both pass `{"value": query}` to
`ui.SearchInput` to prefill the search box, and upstream deliberately left
`value` unowned for exactly that pattern. The rest are `html.Input` or
`ui.Form`, neither of which is in the contract.

#78, #81 and #82 are now ordinary work rather than waiting, and #21 keeps only
gofastr#300 in front of it.

### Fixed — relayboard was shipped and unlisted (2026-08-29)

`recipes/relayboard` merged into `recipes/README.md` and this changelog while
every list the gallery builds from stayed at two recipes. It had no landing
page, no sidebar row, no home-grid card, no entry in `docs/recipes.md`, and no
screenshot in the sweep that is this repo's only visual review. The app was
complete; it was just invisible.

This is the second time. `TestGalleryListsEveryShippedPlugin` exists because
scanner merged fully wired and unlisted, and its own comment says so. That
guard covers plugins, and recipes live in a different slice
(`recipeEntries`, not `demoEntries`), so it could not have caught this.

`TestGalleryListsEveryShippedRecipe` is the missing half: every directory under
`recipes/` must have a `recipePages` entry, so `/recipes/<slug>` serves, and a
`recipeEntries` row, so the gallery links it. It fails on an empty recipe list
rather than passing vacuously.

The landing page itself says the things worth knowing: attribution surviving
client-side navigation, the app's auth being the only identity, a gate that
fails closed, and that it has no browser tests on purpose, because posthog-js
drops captures it believes came from a bot and a headless browser is exactly
what that looks like. A browser suite there would assert against an empty
funnel and pass.

Checked at 1280 and at 390, where the run block's long comment pushed the URL
off-screen. The `pre` scrolls (`overflow-x:auto`), so nothing was clipped, but
the useful half was out of view; the line is now short enough to read before
the scroll edge.

Listing relayboard then turned the suite red, which is the interesting part.
`gallery-journeys.spec.ts` asserted `toHaveCount(17)` against a hand-counted
list, so the gallery growing by one was a failure — the test pinned that the
repo had not changed rather than that the gallery matched it. The count is now
derived from `plugins.json` plus the directories under `recipes/`, shared with
the screenshot sweep through `e2e/tests/pages.ts`, and the sidebar's slugs are
compared as a set against that list so rendering eighteen of the wrong things
cannot pass. Verified it can still fail: an extra directory under `recipes/`
turns it red.

### Added — a daily tripwire against upstream gofastr main (2026-08-29)

`gofastr-latest` answers "does the newest gofastr **release** still work here".
On a day when go.mod already pins the newest release, that job proves nothing,
and that is most days. Meanwhile upstream main sits 66 commits ahead of
v0.73.0, carrying all four fixes this repo is waiting on, and the first thing
that would tell us one of them broke us is the tag itself.

So `gofastr-main` clones upstream main, points the module at it with a
`replace`, and builds and tests. It runs on the daily schedule and on demand,
never on push or pull_request: upstream's main is not this repo's to keep
green, and a third party's broken commit must not block a merge here. A red
scheduled run means the next tag will break us, and blocks nobody.

Measured before writing the job, so it is not shipping on faith:

| against upstream main `916cf0f` | result |
|---|---|
| `go build ./...` | clean |
| `go test ./...` | all pass |
| dependency set | unchanged, no new modules in go.sum |
| e2e, chromium | 211/211 |
| e2e, webkit | 211/211 |

The e2e halves are the ones worth having run. A bridge change like gofastr#271,
which replaced stylesheet-walking with a fixed 78-token vocabulary, cannot fail
a Go test and would surface only as a frame rendering wrong. That is why the
tripwire's Go-only scope is a deliberate limit rather than a claim: it catches
API breakage daily and cheaply, and the browser half stays a thing to run on
demand.

### Changed — the nightly upstream check now names which ticket a release unblocks (2026-08-29)

`gofastr#255` was closed and shipped upstream while nothing here noticed. The
wasm CSP tier landed in gofastr's #293 (`c0266af3`), which means #21
(sqlnotebook) stopped being blocked on a design question and started being
blocked on a tag. Nothing in this repo would have surfaced that, because the
daily `gofastr-latest` job only knew how to say "a newer release exists" in a
`::notice::` inside a green job, and a notice inside a green job is a thing
nobody reads.

So the job now reads `.github/awaiting-upstream.tsv`, which pairs each upstream
commit this repo waits on with the local ticket it unblocks, and asks the compare API whether the newest gofastr *tag* contains
each of them. Containment is `status` being `behind` or `identical`; `ahead`
means the fix is on main and no release carries it. Today both rows answer
`ahead`:

| awaited | upstream | in v0.73.0? | ticket |
|---|---|---|---|
| `c0266af3` | gofastr#255, the `wasm-unsafe-eval` tier | no | #21 |
| `34300c92` | gofastr#268, `GOFASTR_ISOLATION_REWRITE=0` | no | #78 |
| `ad7a2168` | gofastr#271, token names from a loading stylesheet | no | #81 |
| `627bf1af` | gofastr#294, a manifest declaring a host permission | no | #82 |

Every row was checked against the live API rather than assumed, and the
positive branch was checked too, with a commit known to be in the tag, because
a poller that can only ever print "still waiting" is the same vacuous green as
a test that asserts `expect.any(Number)`.

The last two rows are a correction. The file shipped with two rows because
those were the two upstream fixes I happened to remember, which is the same
kind of incompleteness the file exists to prevent. Walking every closed
upstream issue this repo references turned up two more, each with a real
workaround here waiting to be deleted: calendar's hand-declared token block
and scanner's hand-written host permission line. gofastr#273 is deliberately
absent, having been closed on the conclusion that no fix was possible, so
nothing here is waiting on it.

`TestAwaitingUpstreamManifestIsWellFormed` guards the file's shape. A typo in a
sha does not fail the nightly job: the compare call 404s, the row prints a
warning, and the ticket it guards stays blocked forever with nobody the wiser.
An empty manifest is the same failure wearing green, so a zero-row file fails
too.

### Added — `genui`, where the untrusted input is a model's output (2026-08-29)

Generative UI in the cage. A model answers with a tree of component names and
typed props — never markup, never CSS, never a script. Eight React components
exist, each declaring a closed set of props, so there is no `style`, no
`className` and no `dangerouslySetInnerHTML` for generated output to travel
through. The bounded registry IS the containment story; there is nothing to
sanitise because generated output never becomes markup on any path.

Validated twice on purpose: Go before storing and serving, the frame again
before rendering. Not belt and braces — "the host already checked it" is
exactly the assumption that turns one bug into a rendered payload, and the
frame is the last thing between a composition and a DOM. Rejections name the
offending path, because a model's output is debugged by a human reading that
message.

The model runs in Go. The frame receives a finished tree over the bridge, holds
no credentials, and keeps `connect-src 'none'`. Both halves matter: an API key
in a browser is not a key, and a frame that could call a model could also send
it the document it was composing over.

`AnthropicComposer` talks to the Messages API over plain `net/http` — no SDK,
so the module gains no dependency and no version to track for about forty lines
of request building. It is not the default: `FixtureComposer` is, deterministic
and offline, so the demo and the entire suite run with no credentials. The tool
call is forced, because a model left to answer in prose returns JSON inside a
paragraph. A refused composition is handed back verbatim for one correction and
then fails — repairing bad output, or silently dropping to fixtures, would hide
the exact case the plugin exists to demonstrate.

The demo page demonstrates its own containment: "Try an unsafe composition"
posts a tree naming a component that does not exist straight at the frame, past
the Go validator, and it is refused in front of you. The three e2e journeys that
matter most are the ones where nothing renders.

Found in review, and worth remembering: the host adapter used a constant it
never declared. It parses, it loads, the plugin mounts, and the first call
throws `ReferenceError`. Go never runs these files and `node --check` cannot see
it, so a guard now reads every adapter for that class across all nine.

### Changed — the framework pin, and a job that notices next time (2026-08-29)

gofastr 0.71.2 → 0.73.0. The pin was two releases behind with every build green,
which is the problem in one sentence: pinning made drift invisible rather than
impossible. CI now also builds and tests against the LATEST gofastr release,
resolved at run time — on `main`, on a schedule and on demand, but deliberately
not on pull requests, because a third party publishing a release should not turn
someone's unrelated PR red.

Every demo page is now captured on every run too: 17 pages, light and dark plus
390px, 51 screenshots uploaded as an artifact. It cannot judge a design. What it
removes is the excuse that looking was inconvenient — every visual defect this
repo has shipped was found by looking at pixels and by nothing else, and all of
them passed the whole test suite.

### Added — `scanner`, the plugin whose input is a device (2026-08-29)

Barcode and QR scanning in an opaque-origin sandboxed iframe, and the plugin
that answers a question the others could not: what happens when a plugin needs
a capability the cage is not allowed to have.

The camera is not awkward to reach from an `allow-scripts` frame. It is
refused. Four iframes on one page, the same child document calling
`getUserMedia`, a fake camera and permission granted: a plain same-origin
frame works; the sandboxed frame fails with `SecurityError: Invalid security
origin`; adding `allow="camera *"` changes **nothing**; and only
`allow-same-origin` works, which this platform bans. All four reported
`isSecureContext: true`. So a manifest permissions field would have shipped as
a no-op, and upstream closed gofastr#273 on the same conclusion.

The shape that works is the pdf plugin's applied to a device instead of a file:
the **host page** owns the `MediaStream`, where the permission prompt is
against an origin a user can read, and pushes grayscale luminance frames over
the bridge one at a time, released by the frame's `frameDone` ack. The frame
decodes and returns text plus format. Pixels never come back and the cage keeps
`connect-src 'none'` — it can read your barcode and it cannot tell anyone what
it read.

Two decoders, native first. Not for speed: zxing's JS port cannot read some
valid QR codes **its own encoder produces**. Bisected across payloads rendered
identically at 300×300, `GOFASTR_SCANNER_E2E` (19 bytes) fails where 17, 18 and
20 succeed, and the platform's `BarcodeDetector` reads the failing symbol
without complaint — so the code is valid and the decoder is wrong. The results
therefore report which decoder read them, the e2e forces **both** paths on
every engine (CI's Linux chromium has no `BarcodeDetector` and a developer's
mac does, so an unforced test would exercise one path per machine and neither
run would notice the other rotting), and the fixture generator refuses to write
a code the decoder cannot read.

zxing is pure JavaScript, so unlike the SQL notebook this plugin is not blocked
on a wasm tier in the framed CSP.

A host that mounts it must relax the framework's default `Permissions-Policy`,
which denies the camera to the host document itself; without that the failure
is a console error rather than a prompt, which reads like a plugin bug.
`example/main.go` opts in with `camera=(self)` and
[docs/scanner.md](docs/scanner.md) says so. The manifest has no way to declare
that requirement, which is filed upstream as gofastr#294.

### Security — every open dependency advisory cleared, 30 → 0 (2026-08-29)

`pdfjs-dist` 6.1.200 → 6.2.108 (high: arbitrary JavaScript execution on opening
a malicious PDF), `mermaid` 11.4.1 → 11.16.1 (seven advisories: XSS via
sequence-diagram labels and architecture `iconText`, CSS injection via
`classDef` and configuration, infinite-loop DoS in Gantt and XY charts,
prototype pollution), `markdown-it` 14.1.0 → 14.2.0 (ReDoS and quadratic
smartquotes), `esbuild` 0.23.1 → 0.25.0 across all thirteen bundles (dev server
answers cross-origin requests), and `dompurify` 3.4.12 → 3.4.14 transitively.

Worth stating plainly: the XSS entries all execute inside an opaque-origin
frame with `connect-src 'none'`, so none of them was a path to the host. The
cage held. They were fixed anyway, because "not exploitable here" is an
argument that has to be re-made every time it comes up and only has to be wrong
once. The DoS entries are the ones the cage does nothing about — a hung frame
is a hung frame.

Eight of the thirteen bundles rebuilt byte-identical; the five that moved
shifted between 6 and 67 bytes on files of 20 KB to 2.8 MB.

The mermaid bump also revealed that its demo page had been claiming
"mermaid 11.4.1, bundled" in two places, twelve releases stale, because the
version is prose in a Go string. Every plugin that states a library version now
reads it from the bundle's `package.json` and fails if the page disagrees.

### Changed — five early demo pages brought up to the demo-page standard (2026-08-28)

The mermaid, monaco, pdf, geomap and tour demos predate
[docs/demo-page-design.md](docs/demo-page-design.md) and had drifted from it:
thin shells (mermaid/monaco), grey prose and an indigo vendor accent bridged
in from the demo theme (pdf), and missing beats (geomap/tour). All five now
ship the shared shell the richtext and datagrid demos established — brand
bar, a hero whose headline is a claim, fact chips with real values, the mount
inside window chrome with an honest mode badge (`sandboxed iframe` vs
`trusted host page`), an affordance strip, three feature cards, and a
footer — on the same warm-amber token palette, with the light/dark toggle
persisted to the same cookie as richtext's demo.

Each page now also shows its proof live instead of asserting it in prose:
the pdf demo displays a redaction receipt (armed count, pipeline state, and
the export verifier's six checks) polled from the adapter's bridge mirrors;
monaco's toolbar reconfigures the live editor over postMessage as before,
now under the shell; geomap states the trusted-host trade it made and why,
with a live pin count; mermaid seeds a default diagram so the render round
trip is visible on first load; tour walks a demo surface inside the same
window chrome. Two frame-side constraints surfaced during the retrofit and
shaped the pages: mermaid and monaco parse bridged token values in
JavaScript and reject oklch, so their demo themes bridge exact sRGB hex
equivalents of the same palette; and pdf.js schedules its render loop on
requestAnimationFrame, which headless Chrome starves for frames below the
fold, so the pdf hero is tightened to keep the mount near the viewport (the
plugin's chromedp gate loads the page at 756×413).

e2e: monaco's toggle selector moved with the house class (`.btn.toggle` →
`.fui-btn.toggle`); geomap's theme-toggle journey now asserts the scheme
FLIPS rather than landing on "dark", because these pages default dark via
the shared cookie like richtext's. No assertion was weakened.

### Added — whiteboard: collaboration without a socket in the cage (2026-08-28)

[`whiteboard/`](whiteboard/) is the collaborative whiteboard plugin, and the
answer to "surely a live multi-user board needs the frame to open a
connection". It does not. The board runs in the same opaque-origin sandboxed
iframe as every other heavy-JS plugin here, under `connect-src 'none' — the
exfiltration guard the whole isolation design rests on — because CRDT
updates are order-insensitive binary blobs: they cross the postMessage
bridge as `ArrayBuffer`s, and the HOST relays them between browsers (SSE
fan-out, replay-on-join, presence). The frame collaborates with people it
cannot reach. The document is a Yjs CRDT (`yjs` 13.6, bundled to a ~88 KB /
28 KB-gzip IIFE alongside a small canvas controller); strokes are normalized
`[0..1]` coords in a `Y.Map`, erases are CRDT deletes, and a joiner is
replayed the room's accumulated state and converges.

- **New optional capability `sync:room`**, granted exactly when the host
  wires a room hub via `WithRoomHub(subscribe, publish)` (the datagrid
  optional-capability pattern). The hub is host-owned by design — the
  example app ships one (`example/whiteboard.go`) with SSE fan-out,
  history-replay persistence, presence, and a 32 MiB-per-room cap that
  fails `E_ROOM_FULL` rather than silently grow. Both room routes
  (`GET /room/stream`, `POST /room/publish`) fail closed without the hub
  (403; 503 `E_NO_ROOM_HUB` under the dev bypass), and a grant that implies
  `sync:room` — including the `sync:*` / `*:*` wildcards — without a hub is
  a construction panic, matched with `access.ScopeMatch` so wildcard grants
  cannot slip between the construction check and the runtime gate.
- **Identity is the host's to decide.** The hub assigns each participant an
  opaque pid and a colour; presence carries `{pid, color, x, y, down}` and
  never a name, and the hub attaches the assigned colour on fan-out so a
  client cannot forge another participant's. The frame draws in its assigned
  colour — there is no picker in the cage. This is documented as an
  isolation property ([docs/whiteboard.md](docs/whiteboard.md)).
- **Convergence, not last-writer.** The adapter's reconnect handshake asks
  the frame for its full CRDT state (`syncSnapshot`) and publishes it while
  the hub replays everyone else's — so the demo's drop-connection control
  (draw offline on both sides, reconnect) merges both sets of strokes.
- **The no-network claim is asserted, not assumed.** The frame bundle wraps
  `fetch`/`XHR`/`EventSource`/`WebSocket`/`sendBeacon` at boot and records
  every attempt on `window.__wbNetProbe`; the e2e suite additionally
  filters the page's own request log by initiating frame and expects zero.
  Two-browser-context journeys pin the whole claim: a stroke drawn in one
  window appears in the other with identical canvas pixels, a late joiner
  gets the existing board, and the presence cursor from the other
  participant appears carrying no name. Suite passes in WebKit and Chromium.
- Demo at `/whiteboard` in the example gallery, with live relay telemetry
  (bytes sent/received via the host, participants, strokes), the
  drop/reconnect control, and a second-window button; docs in
  [`docs/whiteboard.md`](docs/whiteboard.md).


### Added — calendar: no calendar library; Go owns the clocks (2026-08-28)

[`calendar/`](calendar/) is the month/week/day calendar plugin, written from
scratch — FullCalendar was the obvious pick and was deliberately rejected:
wrapping a library proves the wrapper works, not the platform. The frame
bundle is ~24 KB raw / ~8.8 KB gzip with **zero npm dependencies**, and the
parts everyone gets wrong run in Go: RRULE expansion (DAILY/WEEKLY/MONTHLY
with COUNT/UNTIL/BYDAY, wall-clock anchored, everything else rejected
loudly with the offender named), timezone resolution with an explicit
gap/fold policy (spring-forward times carried by the pre-transition offset
so they land *after* the gap; ambiguous hours resolved to the first
occurrence — table-tested against America/New_York's 2026 transitions,
Lord Howe's 30-minute step, and Kolkata's half-hour offset), and conflict
detection over resolved instants (all-day included, end-touching excluded,
same-series instances exempt). Events are host-owned wall-clock strings
plus an IANA zone — the frame never receives an RRULE (asserted on the
wire) and renders occurrences already resolved to explicit instants AND
wall clocks. Moves are INTENTS: a pointer drag or arrow key sends a
wall-clock delta, `POST /move` re-resolves it through the zone, and the
answer can differ from the drag — dragging the 01:30 EST event one hour
onto the nonexistent 02:30 comes back at 03:30 EDT with requested +60 min,
wall result +120 min, elapsed +60 min, and a plain-language note. Per-instance
edits are overrides keyed by event + original series date (RECURRENCE-ID
semantics), so moving one occurrence never touches the series. The demo at
`/calendar` jump-buttons straight to both 2026 DST weekends and shows the
three deltas live; docs in [`docs/calendar.md`](docs/calendar.md).

### Added — imageedit: crop, annotate, redact; the server decides what's true (2026-08-28)

[`imageedit/`](imageedit/) is the image crop / annotate / redact plugin, and
the proof that the pdf plugin's bytes-over-the-bridge design generalizes
beyond one file format. The frame (dependency-free, ~7 KB gzip) runs under
`connect-src 'none'` and receives the image as an `ArrayBuffer` over
postMessage — decoded with `createImageBitmap`, no CSP-covered fetch. The
canonical doc (schema `imageedit-v1`) is an OPERATION LIST —
`{src, crop, rotate, annotations[], redactions[]}` in source-image pixels,
never the bitmap — with a FIXED operation order (crop → rotate → annotate →
redact) documented and enforced on both sides. Every export is re-rendered
by Go from that list using only the standard library: EXIF stripped by full
re-encode (and scanned for, not assumed), 16 MiB / 24 MP / 8192px caps
enforced at the header stage before `image.Decode` allocates, and redactions
VERIFIED against the produced bytes — every pixel in every redaction rect
must equal the fill or the export fails closed with `E_REDACT_VERIFY`
(regression test: an unleaked compose must be rejected by the verifier).
Preview and server share one integer-exact pipeline (fillRect borders,
Bresenham arrows, a 5×7 bitmap font mirrored byte-for-byte between
`render.go` and `js/src/font.ts`), so a PNG composes to identical pixels in
the canvas and in Go — asserted by a Go golden test and an e2e journey that
diffs the frame's own 1:1 render against the exported bytes at deterministic
sample points. Capabilities: `document:read`/`document:write`/`theme:read`
always; optional `upload:images` granted only when
`WithUploadHandler` is wired (wildcard-safe construction panics, datagrid
rule). Demo at `/imageedit` shows the operation list as live JSON and the
server-rendered result beside the in-frame preview with a sampled-pixel
agreement counter; docs in [`docs/imageedit.md`](docs/imageedit.md).


### Added — formbuilder: author forms the server enforces (2026-08-28)

The `formbuilder` plugin is the first here that authors the framework
itself rather than content: its output is a form schema the server consumes
and enforces. The canonical doc ([`formbuilder/`](formbuilder/),
schema `formbuilder-v1`) is **data only** — `{version, fields[]}`, never
markup; Go validates every save and refuses bad schemas with specific `400`
codes (unknown type, duplicate/empty/invalid name, malformed rule, unknown
version, `E_MARKUP` for a label containing `<`), persisting nothing on
refusal. The demo closes the loop the plugin exists to make: design a form
in the opaque-origin sandbox (a drag-to-reorder field list + property panel,
deliberately no vendor library — ~13 KB bundle), then `/formbuilder/live`
renders the SAVED schema as a real working form through GoFastr's own
`ui.Form` and re-derives every rule server-side on POST. The live inputs
carry no native constraint attributes on purpose — submit garbage from a
browser or a bare `curl` and the same Go validator answers `422` with field
errors; select membership is enforced even on optional fields. Versioned
from the first commit (unknown doc versions are refused, not degraded).
Docs in [`docs/formbuilder.md`](docs/formbuilder.md); e2e in both webkit and
chromium drives the design → save → live → reject/accept round trip.

### Added — logstream: a live log tail pushed across the sandbox (2026-08-28)

The `logstream` plugin is the first whose traffic is not turn-based. Every
other plugin here loads a document and saves it; even the datagrid moves its
100,000 rows one request at a time in answer to a question the frame asked.
A log tail is open-ended, host-initiated, and produced faster than it can be
rendered — so [`logstream/`](logstream/) exists to prove the bridge carries
a live push and to make the overflow answer EXPLICIT. The host adapter
drains the plugin's chunked NDJSON route (`GET /stream`, capability
`stream:read`, no-store, per-line flush) and pushes unsolicited
`streamBatch` events into an xterm.js frame whose CSP forbids every fetch;
the frame renders one batch per ~16 ms tick and acks with `streamAck`
carrying the last rendered sequence number. Backpressure is the product: a
4-batch ack window and a 2,000-line bounded host buffer that drops from the
OLDEST end when the producer outruns the frame, with the dropped count
riding the next batch and rendering as a visible "N lines dropped" marker —
never a silent gap. Scrollback is bounded at 10,000 lines and every ack
publishes the live depth against that cap. Read-only by design: no PTY, no
shell, no command input, no write surface at all. The demo's deterministic
generator switches between 5 lines/s and a 6,000 lines/s flood the frame
provably cannot absorb, and the demo page's live telemetry (lines/s,
delivered, dropped, scrollback/cap, in flight) shows the whole mechanism at
a glance. e2e in both webkit and chromium drives the drop path for real;
docs in [`docs/logstream.md`](docs/logstream.md).

### Added — chart: one spec, two agreeing renderers (2026-08-27)

The `chart` plugin generalizes what `richtext/ssr` does for documents: a
canonical chart-v1 doc (`{type, series[], axes, options}`; line / bar /
area / scatter; data in the doc, capped at 10,000 points across 12 series)
rendered by TWO implementations that must agree — a pure-Go static SVG
(`chart/ssr`, the page works with JavaScript off) and a sandboxed
Observable Plot frame that hydrates it (hover tooltips; the host adapter
hides the SSR node on `ready` and restores it on `bootError`). The crux
is tick agreement: `chart/ssr/ticks.go` is a line-for-line port of
d3-array 3.2.4's `ticks`/`tickIncrement`/`tickStep` (cited), replayed
against a committed ~3000-case ground-truth sweep recorded from real d3;
both renderers use explicit domains, tick counts and a shared
round-trip-precision label format, and an e2e journey asserts SSR and
frame agree on tick labels, series names and extents across four awkward
ranges (0–7, 0–1, −3.5–3.5, 0–1e6) in both chromium and webkit. Mounted
in the example gallery at `/chart`; docs in
[`docs/chart.md`](docs/chart.md). Demo page built to
`docs/demo-page-design.md` (hero + fact chips, framed
mount, type switcher, server-SVG reveal, feature cards). Series marks
carry equal visual weight in both renderers: 2.5px strokes, value dots,
token-driven grid; bars are dodged per series with a half-group x-domain
pad (identical arithmetic in `ssr.minXGap` / `spec.ts minXGap`, covered
by the agreement journey).

### Added — datagrid: 100,000 rows over the bridge (2026-08-26)


[`datagrid/`](datagrid/) is the data-grid plugin: AG Grid Community's
infinite row model in the same opaque-origin sandboxed iframe as every
other heavy-JS plugin, talking to the host over the same versioned
postMessage bridge — but with a different traffic profile. Where the
other plugins move one small document, a grid moves rows by the
thousand, and the framed CSP's `connect-src 'none'` means the frame can
never fetch its own rows. So every page arrives from the host as a
correlated `requestRows` → `rowsResult` event pair (the richtext
`requestUpload` pattern; no protocol change), and **sorting, filtering
and paging run in the host's Go rows source** behind `POST /rows`. The
canonical doc (schema `datagrid-v1`) is view state only —
`{columns[], sort, filter, pageSize}` — rows are never part of it.

Capabilities: `data:read` + `theme:read` always; `data:write` (cell
edits via `POST /cell`, view-state saves) and `data:export` (CSV) are
optional, granted exactly when the host wires `WithCellWriteHandler` /
`WithExportHandler` — the pdf `pdf:export` pattern, complete with the
construction-time panic when a capability outlives its handler. Grants
match with the framework's scope grammar, so the panic fires for wildcard
grants too (`data:*`, `*:*`, `*:write` all imply `data:write`), and every
route fails closed with a clear error if its handler is somehow unwired —
`WithDevGrantAll` bypasses the gate, never the nil check. CSV export runs
host-side (a sandboxed frame cannot start a download), paged through the
rows source in 5,000-row chunks that spill to a temp file, so peak memory
is one chunk whatever the table size; the host adapter clicks the download
link in the privileged host page. A 500-row page ceiling on `/rows` is
the integrity behind the volume claim: one request can never pull the
whole table, `/rows` projects each page to the requested columns, and the
request envelopes are exactly one JSON value — 64 KiB cap, trailing data
rejected. The docs say the load-bearing thing about authorization too:
`pluginhost.Allow` is a capability gate, not authentication —
`WithCellWriteHandler` is where a production host checks the session.

Retention is bounded to match delivery: AG Grid's default block cache is
unlimited, so the frame caps it at ⌈2,500 / pageSize⌉ blocks — 25 blocks
/ 2,500 resident rows at the default 100-row page — with older blocks
evicted as new ones load. On the write side, `/save` persists the doc it
validated (columns normalised, pageSize clamped, sort/filter bounds
applied — the same bounds `/rows` enforces), and exported CSV fields —
headers included — are formula-sanitised (a leading `=`, `+`, `-`, `@`,
tab or CR is quoted) so spreadsheet clients render them as text.

The demo (`/datagrid` in the example gallery) serves 100,000 rows
generated deterministically in Go (`example/datagrid.go`, fixed
formulas, no database), with a cell-edit overlay so edits survive
reloads; its export store keeps at most 8 files, FIFO-evicted. The demo
page itself is built to `docs/demo-page-design.md` — the richtext shell
(window chrome around the mount, hero, fact chips, feature cards) plus a
live bridge-telemetry strip that reads the adapter mirrors and AG Grid's
own cache state, so the volume claim is on the page, not just in the
tests. The e2e journey recomputes the same formulas in TypeScript and
asserts exact cells at row 50,000, proves a sort click refetches the
first page from the server, pins the CSV (100,001 lines), reads the
`__datagridRowsDelivered` / `__datagridMaxRowsDelivered` mirrors to
assert a deep-scroll session delivered only a few hundred rows and no
single response exceeded the page cap, and reads AG Grid's own
`getCacheBlockState()` inside the frame to assert the resident block
count stays at or below the cap. Console/page errors are captured from
before navigation, so boot errors are visible to the assertions. Theming
uses AG Grid's Theming API mapped from the bridged tokens — verified in
WebKit, not assumed.


### Changed — gofastr v0.71.1 → v0.71.2 (2026-08-26)

Picks up the upstream `ui.Card` footer fix this repo's relayboard
recipe surfaced: footers pin to the card's bottom edge in stretch
contexts (plain and linked cards), so config-only cards in a `ui.Grid`
row no longer float their footers mid-card. Also carries the upstream
fix for `App.Shutdown` racing `App.Start`'s pre-listen phases.

### Added — recipes/relayboard: the measured funnel (2026-08-26)

[`recipes/relayboard/`](recipes/relayboard/) is the analytics recipe: a
three-screen product whose funnel runs end to end through the `posthog`
integration against a self-hosted PostHog — campaign attribution from a
UTM-tagged landing page through client-side navigation to the
`purchase` event, identified users from real `battery/auth` accounts
(whoami answers from the session, so login merges the anonymous person
into the identified one), an A/B hero branched client-side on the
`hero-copy-test` variant, and a server-side `/beta` gate backed by
`phFlagStore`: a `featureflag.Store` of forty lines of stdlib HTTP
POSTing `{host}/flags/?v=2` (`/decide` is 403 on current self-hosted
PostHog; the flags endpoint is the replacement). Unknown keys return
`(nil, nil)` so `BoolDefault` semantics survive, and errors fail closed.
With `POSTHOG_KEY` unset the same app degrades cleanly: no plugin, no
store, `/beta` invite-only, the A/B script a no-op, one log line. HTTP
smoke tests (no browser — posthog-js bot detection would drop every
capture) pin the routes, the gate against a fake PostHog, and the
register→whoami identity chain.


### Added — posthog: first-party PostHog in one call (2026-08-25)

[`posthog/`](posthog/) packages the PostHog recipe from gofastr's
analytics-recipes doc — the two-route relay table, the page bootstrap,
the whoami identity endpoint — behind `posthog.New(Config{Key, Region})`
+ `app.RegisterPlugin` + `host.RegisterExternalScript(p.ScriptURL())`
(or `p.Attach(host)`).

It is deliberately **not** one of this repo's sandboxed iframe plugins,
and the README's new "Integrations" section says so: posthog-js
instruments the whole host document, so it runs in the host page and
cannot be fenced. What stays first-party is the wire — the browser
talks only to your origin through `battery/relay`, the strict default
CSP needs no `script-src`/`connect-src` exceptions, and no vendor
cookie lands on your domain.

Three decisions worth writing down:

- **The config is baked into the served bytes, not script attributes.**
  `RegisterExternalScript` emits a bare `<script src>` tag, so the
  key, mount, region UI host and DNT flag are rendered into boot.js at
  `New` via `encoding/json`. That is load-bearing twice: the bootstrap
  is one file with no globals to race, and Go's JSON encoder
  HTML-escapes `<`/`>`/`&`, so a key containing `</script>` stays
  inert — pinned by a test that feeds exactly that key and asserts the
  escaped form (mutation-proven: appending the raw key to the served
  bytes fails the suite).
- **Secret key shapes panic at construction.** `phx_` (personal) and
  `sk_` (server) keys are secrets, and this package puts the key in
  bytes served to every visitor. `New` refuses them rather than ship
  one; `phc_` (public project) is the only shape that belongs
  client-side.
- **No `ExtraIngestPaths` knob.** PostHog has moved endpoints before
  (the `-assets` split), and when it happens again the escape hatch is
  a hand-declared `relay.New` alongside — a knob whose upstream is
  implied rather than named is how an open proxy starts. The package
  README documents the pattern instead.

Unit-tested with zero vendor account and zero egress: an unexported
`newWithUpstreams` seam lets the suite point both relay upstreams at
loopback httptest servers, so the region table, tail/query forwarding,
404/405 posture, the 8 MiB → 64 MiB session-replay body cap and the
rendered bootstrap are all asserted against real HTTP. The deep relay
matrix (hostile tails, credential stripping) stays in battery/relay's
own suite; five mutations (secret-key guard, region table, replay cap,
raw-key leak, Stringer arm) each fail their test.


### Changed — gofastr v0.46.0 → v0.65.0 (2026-08-17)

- Nineteen framework releases in one step. No plugin code changed: build, vet,
  the full Go suite, the eject canary and the 302 WebKit + Chromium journeys all
  pass untouched.

- **The `go` directive moves to 1.26.6**, because gofastr v0.65.0 requires it. A
  `go.mod` asking for less fails as a toolchain resolution error naming a
  transitive gofastr package, which is the trap `docs/eject.md` already
  describes. CI reads `go-version-file: go.mod`, so the job follows the bump
  without an edit.

- **New indirect dependencies, all from one upstream swap.** gofastr v0.56.0
  made `modernc.org/sqlite` the `sqlite3` driver, so `modernc.org/{sqlite,libc,
  mathutil,memory}` and four smaller transitives are now in the graph. It is a
  pure-Go driver, so nothing here needs cgo. The edge reaches this repo only
  through `recipes/blogapp`; no plugin imports `gofastr/sqlite`. The bump needed
  a `go mod tidy` for the new `go.sum` entries — `go get` alone left the build
  red.

- `frameworkCompat` stays at `>=0.28.0`, verified rather than carried forward:
  all six registry plugins still build against v0.28.0. `recipes/blogapp` does
  not (it uses `ui.TextField`, which landed later), but recipes are whole apps
  rather than registry entries and make no compat claim.

- `docs/eject.md` now shows v0.65.0 and `go 1.26.6` in its install examples. The
  CLI's own install block needed no change: it parses both numbers out of the
  embedded `go.mod` at init (`GoFastrVersion`, `GoVersion` in `source.go`), so
  `gofastr-plugin add` printed the new floor as soon as the require moved.

### Added — recipes: two complete blogs (2026-07-28)

`recipes/` holds whole apps rather than plugin demos. `example/` exists to mount
every plugin at once; a recipe exists to answer what an app that uses one
actually looks like, including the parts a demo skips — auth, persistence,
feeds, 404s.

The first two are a matched pair. Same domain, same reading experience, opposite
answers to "where does the content live?":

- **[`recipes/blogsite`](recipes/blogsite/)** — markdown files with frontmatter.
  `content.go` parses them once at boot and builds the ordering, tag facets,
  prev/next links, and search index in memory; a request never touches the
  filesystem. The content is `go:embed`'d, so a build is one binary with no
  assets directory beside it. Tag pages, a year-grouped archive, pagination,
  substring search, RSS 2.0, JSON Feed 1.1, sitemap, `robots.txt`, drafts, and
  future-dated posts that publish themselves on the next boot. It uses **no
  plugin from this repo** — that is the point: it is the baseline `blogapp` is
  measured against, and it exercises the GoFastr core UI path end to end with no
  CSS of its own.
- **[`recipes/blogapp`](recipes/blogapp/)** — posts in SQLite (GoFastr's pure-Go
  engine, so no cgo and **no new module dependency**), written in the browser
  with the `richtext` plugin. The canonical document is ProseMirror JSON;
  `richtext/ssr` renders it server-side, so readers get plain HTML and the
  ~600 KB editor bundle loads on exactly one route, behind a login.

**The capability gate is not an authentication gate.** This is the finding
`blogapp` was worth building for. `pluginhost.Allow(ctx, granted, cap)` is
`auth.ScopeMatch(granted, cap) && auth.HasScope(ctx, cap)`, and `HasScope`
returns **true** when the context carries no token scopes — sessions and JWTs
are unscoped by design. So an anonymous `POST /__gofastr/plugin/richtext/save`
passes it. The gate answers "does this plugin hold this capability", a question
about the plugin's authority, not the caller's identity. Every host that grants
a write capability has to add the second check itself. `blogapp` does, inside
its save and upload handlers, reading the admin session off the request context
that app-wide middleware annotated; `TestAnonymousPluginSaveCannotOverwriteAPost`
asserts an anonymous save leaves the stored body untouched. It also does **not**
set `richtext.WithDevGrantAll()`, which `example/` uses only because its demos
are unauthenticated.

**Soft 404s.** A database-backed app needs dynamic routes, and `/posts/:slug`
matches slugs that name nothing. Serving a not-found body at HTTP 200 is the
failure crawlers index and monitoring never notices. `blogapp` resolves the slug
in middleware before the host routes and rewrites a miss to its 404 screen with
the real status. `blogsite` avoids the problem entirely by registering one route
per post — its corpus is fixed at boot, which also makes the route table the
sitemap.

Two bugs the tests caught while building these, both now guarded:

- `ui.SiteHeader` renders its `Actions` slot **twice** (desktop bar + mobile
  drawer), so a form control with a fixed `id` there lands in the DOM twice — a
  duplicate-id a11y violation. Both recipes moved site search from `Actions` to
  a nav link, and both suites now walk every page asserting no duplicate ids.
- A slug rule that only checked draft status discarded a hand-typed slug on the
  save that published it, and a slug that `uniqueSlug` had suffixed
  (`untitled-post-2`, from a second new post) never followed its title. The rule
  is now: a hand-typed value wins; otherwise a derived slug follows the title
  while the post is a draft; publishing freezes it.

Both recipes are covered by `go test ./recipes/...` (37 tests: routing, feed and
sitemap shape, draft exclusion, the admin gate, the authoring round trip, the
anonymous-save refusal) and by Playwright journeys in **WebKit and Chromium**
(46 tests, including the editor mounting in its opaque-origin frame, typing
reaching the database over the bridge, and the reader getting no plugin).
`e2e/playwright.config.ts` now starts three servers; the recipe ports live in
[`e2e/tests/recipes.ts`](e2e/tests/recipes.ts).

**In the gallery.** `example/` now carries a **Recipes** section in its sidebar
and on its home grid, with a landing page per recipe
([`example/recipes.go`](example/recipes.go)) explaining the basics, giving the
one command that runs it, and linking to the implementation on GitHub. The
landing pages are served by the gallery itself, so they load in the same content
iframe and the sidebar persists — a recipe cannot be framed directly, because
two UIHost apps cannot share a router (each claims the whole `/__gofastr/*`
namespace) and uihost ships `frame-ancestors 'none'` by default.

See [`docs/recipes.md`](docs/recipes.md) and
[`recipes/README.md`](recipes/README.md).

### Added — gofastr-plugin: eject a plugin into your own repo (2026-07-26)

`cmd/gofastr-plugin` is the eject CLI. It copies a plugin's source into a
consumer repo the way shadcn copies a component, and the consumer owns the
result. It works for one reason: every plugin in this repo imports this repo's
`pluginhost`, and that package is a pure alias forwarding to
`gofastr/framework/pluginhost` in the core (see
[`pluginhost/pluginhost.go`](pluginhost/pluginhost.go)). The CLI rewrites that
import on the way out, so an ejected plugin depends on `gofastr` and nothing
else from this repo. The `gofastr-plugins` require can come straight out of the
consumer's `go.mod`.

- **What lands.** The plugin's Go sources (imports rewritten), the prebuilt
  bundle under `assets/` served same-origin via `go:embed` (unchanged), the host
  adapter, and the `js/` TypeScript sources plus `build.mjs` so the bundle can
  be rebuilt after an edit. Default target: `internal/plugins/<name>`.
- **It writes files and nothing else.** No `go get`, no `go mod tidy`, no `npm
  install`, and no edit to your `go.mod`. Dependency resolution belongs to
  whoever runs the project — they own the lockfiles, the proxy, and the choice
  of which `gofastr` patch to move to. What lands is code plus configuration
  (`package.json`, `package-lock.json`, `tsconfig.json`, `build.mjs`), which is
  enough to make the install reproducible when they run it. The CLI prints the
  commands; it does not execute them.
- **The lock file** (`gofastr-plugins.json` at the project root) records two
  hashes per vendored file: what the CLI wrote, and the upstream source it came
  from. `gofastr-plugin diff` reads that pair to tell whether *you* moved (your
  file no longer matches the written hash) or *upstream* moved, and exits
  non-zero on drift, so it works as a CI check. A plain `cp -r` fork cannot tell
  the two apart.
- **`--force` is the conflict rule.** `add` refuses to overwrite a file whose
  hash no longer matches what the CLI wrote (one you have edited) unless you
  pass `--force`. `--with-tests` also vendors the `*_test.go` files, which pulls
  `chromedp` into the consumer's `go.mod`; off by default for that reason.
  `--no-js` takes only the prebuilt bundle.
- **The tradeoff, stated plainly.** Eject when you need to change the plugin — a
  different toolbar, a different canonical document, a capability upstream will
  not grant. Do not eject just to use it: importing keeps you on `go get -u` for
  fixes, and ejecting takes you off it. Upstream fixes reach an ejected copy only
  when you run `diff` and merge them by hand. Owning the source moves no security
  boundary: a vendored sandboxed plugin is still opaque-origin sandboxed and
  still talks over the same versioned `postMessage` bridge; `geomap` and `pdf`
  still need the host CSP their docs specify.

See [`docs/eject.md`](docs/eject.md).

### Fixed — a finished redaction could look like it never finished (2026-07-26)

`redactState` reached the host only as a passenger on `docChanged` — an event
that is debounced 250 ms, gated on `document:write`, and emitted only when the
**document** changes. A status transition is none of those things. So the
terminal move to `done` arrived only if some unrelated document mutation
happened to be emitted after it, and whether one was is a race. When it lost,
the redaction had rasterized, verified and exported correctly while the host
sat on a stale `working` forever: no error, no console message, a progress UI
that never finished, and an export the user had no reason to believe existed.

This is why `e2e (webkit)` was red on `main` — the same shape as the tour
overlay bug below, engine timing deciding whether a later event rescued a state
nobody had announced. Every transition now goes through one `setRedactState`
that assigns **and** emits a dedicated `redactStateChanged`, undebounced and
independent of write capability. The assignment and the announcement being two
separate statements is precisely what let `done` be set without ever being sent.

- **The occurrences assist wedged redaction permanently.** Its button closed the
  confirm modal calling neither `onConfirm` nor `onCancel`, so the promise the
  arm/confirm flow awaits never settled and `redactBusy` stayed `true` — and the
  re-entrancy guard at the top of `armRedaction` then swallowed every later
  Apply in silence. A user who took the assist's advice lost the ability to
  redact for the rest of the session. `showConfirm` now resolves an explicit
  outcome (`confirm` / `cancel` / `added-occurrences`) so each exit is total;
  inferring it from a mutated flag is what hid this. The cancel path never
  resolved either, leaking a suspended call per dismissal.
- **The assist was unreachable from the demo, which is why nothing caught it.**
  A needle is a whole text item, never clipped to the rect, and no line in
  `sample.pdf` repeated — so the button never rendered. Page 2 now repeats page
  1's title verbatim, and a journey drives the button and then requires a
  subsequent redaction to actually complete. It fails against the previous
  bundle.


### Added — pdf 0.1.0: viewer, editor and redactor (2026-07-26)

The fourth sandboxed heavy-JS plugin, built on pdf.js (render) and pdf-lib
(write). Route `/__gofastr/plugin/pdf`, schema `pdf-v1`, demo at `/pdf`.
Bundle 2733 KB raw / 869 KB gzip.

**The sandbox is the product here, not the tax.** The framed CSP gives the
frame `connect-src 'none'`, no workers, no `blob:` and an opaque origin, so a
frame holding a confidential PDF *cannot exfiltrate it*. The host pushes the
document in over the bridge and takes produced bytes back the same way. This
plugin therefore has **no trusted-mount opt-out**, unlike richtext. Three
consequences fall out of it: download, print and clipboard-write are host
capabilities (the CSP sandbox token grants none of them in-frame); `/doc/{id}`
is fetched by the privileged host adapter, never the frame; and no code
splitting is possible, because a dynamic `import()` is a CORS-mode fetch an
opaque origin can never satisfy.

**Redaction removes content.** Pages carrying a redaction are rasterized and
embedded into a newly built document; untouched pages are `copyPages`'d
losslessly. Six checks run in-frame *before any bytes are released*, and
verification failure emits nothing. Two traps this catches that a naive
implementation would not:

- A raw substring scan is not a byte search. pdf-lib writes text as hex strings
  and packs the Info dict into a compressed object stream as UTF-16BE hex, so
  the bytes must be inflated and the tokens decoded first.
- Absence is asserted **per rect**. The same string may legitimately appear
  elsewhere, so a content-stream hit that text extraction places only on
  non-redacted pages is a warning — but it fails closed: present in the bytes
  yet extractable nowhere (invisible text) is a leak.

`/Annots` was measured surviving `copyPages` with a planted secret, so redact
mode strips annotations by default. A black-rectangle "redaction" is kept as a
regression test, proven to still leak three ways with the verifier rejecting
it, and the shipped pipeline's output was judged by an independent
implementation of the same six checks.

**Scanned documents would have rendered blank.** pdf.js decodes JPEG 2000 and
JBIG2 — the codecs real scans use, and scans are what people redact — through
WebAssembly, which cannot instantiate under the framed CSP at all; its pure-JS
fallbacks are reached by a dynamic `import()` an opaque origin cannot satisfy
either. Both paths dead meant a **blank white page with no error, no console
message and no CSP violation**. `pdf/js/build.mjs` rewrites that one dynamic
import into a static dispatcher over the inlined fallbacks, asserted at build
time so a pdfjs-dist upgrade fails loudly rather than silently restoring blank
scans. No gofastr core change was required.

Mode (`view` / `annotate` / `redact`) is a host decision enforced on both sides
of the bridge; `ModeRedact` requires the optional `pdf:export` capability and
panics at construction without it. Capability denial answers 403 on every route
— `protocol-v1.md` prose says 412, but `pluginhost.WriteCapabilityDenied`, which
every shipped plugin calls, writes 403; logged as an upstream thread rather than
split inside one plugin.

### Fixed — the pdf annotation editor behaved nothing like an editor (2026-07-26)

Found by driving the UI, not by reading tests — the suite was green throughout,
because every annotation assertion checked `annotationCount` or exported bytes,
never that an annotation lands where the user drew it. Position fidelity is now
the acceptance criterion, checked across zoom and rotation.

- Readiness lied: `__pdfRendered` fired ~500 ms before layout settled, with the
  page slot at the raw PDF height and a 0×0 canvas.
- Annotations painted at PDF coordinates — a drag from page-y 120 to 180 landed
  at y=719, 41 px tall for a 60 px gesture. Now dx=0, dy=0 against the gesture.
- Highlight never created anything; the stamp modal could not be closed with
  Escape; the tool stayed armed after placing, so the click meant to select what
  you just drew drew another one instead.

Selection itself was never broken — it was unreachable, because nothing was
where you left it.

### Changed — gofastr v0.38.1 → v0.46.0 (2026-07-26)

- Eight framework releases in one step. No plugin code changed: build, vet, the
  full Go suite and the WebKit + Chromium journeys all pass untouched.

  Two of the breaking changes land squarely on this repo's contract, so both were
  checked rather than assumed. The **iframe sandbox sanitizer is now an allow-list
  on both sides** (v0.45.0 flipped the Go half; v0.46.0 fixed the JS sink that
  actually sets the attribute), which breaks a manifest requesting
  `allow-popups-to-escape-sandbox`, `allow-top-navigation` or `allow-downloads` —
  ours request only `pluginhost.DefaultSandbox` (`allow-scripts`), the single
  token that keeps the frame opaque. And **`Manifest.Entry` must now be a
  same-origin absolute path**, dual-enforced in Go and the JS broker; mermaid's
  and monaco's entries already were, and `Validate()` runs at registration, which
  the suite exercises.

- `frameworkCompat` stays at `>=0.28.0`, verified rather than carried forward:
  the repo still builds against v0.28.0, v0.38.1 and v0.42.0. The field remains a
  best-effort build floor, not a tested runtime matrix.

### Fixed — the tour overlay showed before it was positioned (2026-07-25)

- `showStep` made the overlay visible and only then deferred the **first**
  `position()` until after `scrollIntoView` plus two animation frames. Until it
  ran, the scrim and cutout carried no geometry — the cutout spanned the whole
  viewport instead of hugging its target. On a fast machine that is a sub-frame
  flash; on a loaded CI runner it was a visibly misplaced spotlight on every
  step, and it is why `e2e (webkit)` had been red on `main` since the tour plugin
  landed while passing on every developer's machine.

  Positioning now happens synchronously before the frame wait, which still
  re-runs afterwards to pick up the smooth scroll. The regression guard asserts
  the invariant instead of racing it: drain microtasks without ever yielding an
  animation frame, and require the cutout to already hug its target.

### Changed — `optionalCapabilities` in the registry index (2026-07-25)

- `plugins.json` rows can now declare grants a plugin takes on **only when the
  host opts into the feature that needs them** — geomap's `geocode:search`
  appears solely under `WithSearch`, along with the route it gates. Someone
  deciding whether to adopt a plugin needs the difference between "this can reach
  the network" and "this can reach the network if you switch it on", and folding
  both into `capabilities` erased it. Modelled in `internal/registry` with a
  guard (mutation-tested) that an optional grant may not repeat an always-on one.

- Corrected two stale claims in the index's own `$comment`/`note`: it pointed
  maintainers at a sibling `./registry` Go module versioned via `registry/vX.Y.Z`
  tags. Neither exists — the parser is `internal/registry` (test-only) and the
  root module is versioned by this repo's own tags. The comment is instructions
  to whoever edits the file next, and it was sending them to the wrong place.

### Added — geomap 0.3.0: pin editing, search, clustering (2026-07-25)

- **The pin popup is a live editor.** A label input that writes through to the
  canonical doc plus a per-pin Delete button, replacing the static text popup;
  `removeMarker(id)` / `setMarkerLabel(id, label)` join the controller API, and
  read-only re-gates already-open popups in place rather than only blocking new
  pins.

  Building this surfaced a bug that had shipped silently: MapLibre toggles a
  marker's popup from the **map's** click event, so the marker click handler's
  `stopPropagation()` had been disabling every popup since the plugin landed.
  Nothing tested popups, so nothing caught it. The runtime now lets the event
  reach the map and ignores map clicks targeting a marker or popup instead.

- **Geolocate + scale controls**, on by default, off via
  `WithoutGeolocateControl` / `WithoutScaleControl`.

- **Opt-in place search** (`WithSearch`) — an in-map search box backed by a new
  **same-origin** proxy at `/__gofastr/plugin/map/geocode`, gated on a new
  `geocode:search` capability and registered only when search is enabled. The
  browser never calls a geocoder directly: proxying is what allows a
  policy-compliant identifying `User-Agent`, an application-wide 1 req/s limit
  (Nominatim's cap is per-application, so only the server can hold it), the
  caching that policy asks for, and a host page CSP that stays at
  `connect-src 'self'`. `WithGeocoder` swaps the lookup wholesale;
  `WithGeocodeEndpoint` points at a self-hosted instance. A failed lookup (502)
  is deliberately distinct from an empty result set. The example app wires a
  fixed offline dataset — the e2e journeys must not depend on a third party, and
  a demo has no business spending donated geocoding capacity.

- **Opt-in marker clustering** (`WithClustering`). Clusters are computed by a
  `cluster: true` GeoJSON source but rendered as **DOM markers**, so individual
  pins stay draggable and editable, no style glyphs are needed, and bubbles
  theme from the same tokens. Two MapLibre constraints are now pinned in code
  and docs: a source with no layers is never tiled (so `querySourceFeatures`
  returns nothing forever — a transparent probe layer keeps it alive), and
  `isStyleLoaded()` is a "no pending work" flag that flickers false while a
  vector style streams, so gating source creation on it leaves clustering
  permanently inactive.

- **Twelve new e2e journeys** across WebKit + Chromium covering rename/delete
  persistence, read-only popup gating, drag persistence, the style switcher and
  host-theme flip, the toolbar add/clear/reset/load paths, control presence,
  search (hit and no-match), and clustering fold/expand.

### Changed — the repo now stands on its own (2026-07-16)

- **Builds from a published GoFastr.** Dropped the `replace` directive pointing
  at a local `/Users/.../gofastr` checkout and pinned
  `github.com/DonaldMurillo/gofastr v0.28.0`. A fresh clone now builds with
  `go build ./...` and nothing else; previously it built only on a machine with
  a sibling checkout at exactly the right path. For the local two-repo edit
  loop, use a gitignored `go.work` (see README) — never a committed `replace`.
  Check a change the way a consumer sees it with `GOWORK=off go build ./...`.

- **`frameworkCompat`** raised to `>=0.28.0` on both plugins, and the
  `plugins.json` note corrected: it claimed development against "the v0.20.0
  local checkout via a replace directive", which is no longer how this builds.

### Added

- **`internal/registry`** — the schema of `plugins.json` plus the tests that
  keep it honest. The index is consumed by **copy, not import**: a host fetches
  the file from a release and vendors it, so the published artifact is the JSON
  and this repo exposes no Go API for it — nothing to import means no module
  cycle (GoFastr would otherwise import a repo that imports GoFastr) and no path
  by which chromedp or an embedded bundle reaches the core's `go.mod`.

  Because the index is hand-maintained, a stale row is invisible to every other
  test. The guards: rows must cover exactly the plugin packages present, each
  `routePrefix` must equal that package's own const, no row may request
  `allow-same-origin`, required fields must be non-empty, and the parser rejects
  unknown fields (a new JSON key must reach the structs in the same change
  rather than vanishing from every generated page). Each was mutation-tested to
  bite. The anchor is the `Name`/`RoutePrefix` consts, not a
  `pluginhost.Manifest` literal — richtext predates the extraction and declares
  none.

  Bump `registryVersion` on a breaking field change — vendored copies are then
  stale.

- **Releases** (`.github/workflows/release.yml`) — tagging `vX.Y.Z` now
  publishes a GitHub release with `plugins.json` attached, which is how hosts
  get the index:

  ```sh
  curl -fsSL -O https://github.com/DonaldMurillo/gofastr-plugins/releases/download/v0.1.0/plugins.json
  curl -fsSL -O https://github.com/DonaldMurillo/gofastr-plugins/releases/latest/download/plugins.json
  ```

  The workflow re-runs the index guards first, so a broken index fails the
  release instead of reaching a host, where it would stay invisible until a page
  rendered wrong.

  It also **stamps provenance into the published copy** — `release.tag`,
  `.commit`, `.published`, `.source` — closing the gap that makes copying risky:
  a vendored `plugins.json` otherwise cannot say how old it is, and a year-old
  one looks exactly like a fresh one. The file in git carries no stamp, and a
  test enforces that (a committed stamp would be false immediately).

- **LICENSE** — MIT, matching GoFastr. The repo is public and previously shipped
  without one, which left its terms undefined.
- **CI** (`.github/workflows/ci.yml`) — builds, vets, and tests the module, and
  runs the Playwright journeys in WebKit + Chromium. Also gates bundle
  freshness: `richtext/assets` is committed prebuilt and served by `go:embed`,
  so a source edit without a rebuild ships stale JS that no Go test would catch.

### Added — full editor feature set (2026-07-14)

- **Persistent formatting toolbar** — sticky bar with undo/redo, a block-type
  dropdown (Text/H1-3/Quote/Code/lists), B/I/U/S/inline-code, link, text color
  + highlight, alignment, and clear-formatting. Plugin-driven active states;
  keyboard-operable; configurable via `uiPlugins({ toolbar: false })`. Also the
  mobile undo path (no ⌘Z on touch keyboards).
- **Text alignment** — left/center/right/justify on paragraphs + headings
  (schema `align` attr, editor toDOM + SSR parity, toolbar buttons).
- **Word/character count** status bar below the editable (debounced, off the
  keystroke path).
- **Smart paste** — plain text that looks like markdown pastes as real blocks
  (via the markdown parser); a URL pasted over a selection wraps it in a link;
  image paste and HTML paste are preserved.
- **Find & replace** (Mod-F) — match highlighting via decorations, next/prev,
  replace one, replace all, case toggle, live match counter.
- **Table controls** — floating add/remove row+column, header-row toggle, and
  delete-table on cell focus; Tab at the last cell appends a row.
- **Code block language selector** — a dropdown on the focused code block.
- **Code syntax highlighting (2026-07-15)** — once a language is set, code
  blocks tokenize into `comment`/`string`/`number`/`keyword`/`function` spans in
  BOTH the live editor (ProseMirror inline decorations, `js/src/highlight.ts` +
  `js/src/codehighlight.ts`) and the no-JS read view (`ssr/highlight.go` +
  `renderCodeBlock`). No new JS/CSS dependency — a small config-driven lexer is
  hand-mirrored across the two languages and pinned by a shared parity fixture
  (`richtext/highlight-cases.json`) that both test suites assert against, so the
  editor and read view can't drift. Theme via overridable `--richtext-hl-*`
  tokens (GitHub-primer light defaults), defined identically in
  `frame/editor.css` and `ssr/style.go`. Supported: js/ts, go, python, rust,
  json, css, sql, bash, html (+ aliases); unknown languages render plain.

### Added — Phases 1–3 complete (2026-07-13)

- **Trusted in-page mount (the sandbox opt-out)** — `richtext.WithTrustedMount()`
  serves `editor-inline.js` (`window.__gofastrRichText.mountTrusted`), a
  page-scoped stylesheet (`editor-scoped.css`, rescoped under
  `.gofastr-richtext-trusted`; framework-token fallbacks dropped so host tokens
  inherit, plugin-local `--richtext-*` slot tokens kept), and a frameless demo
  at `/__gofastr/plugin/richtext/trusted`. Same protocol envelopes over a
  swapped transport (`protocol.setTransport` + `routeEnvelope`). Host-side
  opt-in only — never a default, never plugin-selectable (docs/DECISIONS.md
  "secure by default, opt out").
- **Platform extracted into gofastr core (Phase 2)** — the proven `pluginhost`
  package + host broker now live at `gofastr/framework/pluginhost`;
  `pluginhost/` here is a thin compatibility alias. `data-fui-plugin*` markers
  registered in core's ARCHITECTURE/runtime-contract docs.
- **User-journey e2e suite** (`e2e/`, Playwright, strict TypeScript) — 13
  journeys driven like a person (real clicks/taps/typing) against BOTH mounts
  in WebKit + Chromium, plus mobile gates (iPhone/Pixel touch) and an axe
  a11y gate (zero serious/critical across framed/trusted/SSR + open menus).
  Any console/page error fails a test. Dogfood shots: `npm run shots`
  (light/dark × desktop/mobile × framed/trusted/SSR).
- **TypeScript strict everywhere Node runs** — `richtext/js`, `mermaid/js`,
  `e2e/` all migrated; `tsc --noEmit` gates every build.

### Fixed

- Slash-menu selection (hover rebuild loop destroyed the element under the
  cursor; heading items double-invoked their command), Enter-in-list splitting
  the paragraph instead of the item, bubble-toolbar buttons throwing on every
  click, Safari CSP refusing frame subresources (`'self'` is `null` in an
  opaque frame — origin-keyed CSP + inlined styles), overlay clipping at the
  iframe edge (overlay-aware frame autosize), the frame-height ratchet
  (content-extent measurement), menu dismissal (outside click/tap + frame
  blur + `hostPointerdown` broker relay for iOS), and slash-menu keyboard
  scroll (active item kept in view; hover moved to `mousemove` so it can't
  fight arrow keys).

### Added — the Phase-0 build

The isolation spike is built and verified end to end. The opaque-origin sandboxed
iframe + versioned `postMessage` RPC is a usable, secure editing surface, and the
platform machinery that proved it is now extracted into a reusable package.

- **`pluginhost` — the platform** (`pluginhost/`). The reusable, plugin-agnostic
  host glue distilled out of the editor so a second heavy-JS plugin can reuse it:
  - `Manifest` / `ClientModule` — declarative client-module description;
    `Validate()` enforces the v1 invariants (no `allow-same-origin`, requires
    `allow-scripts`) so a mis-configured plugin fails loudly at construction.
  - `AssetServer` — serves embedded assets with correct Content-Types AND the
    framing/CORP/CSP header relaxation GoFastr's global security middleware
    otherwise blocks (the client-side isolation contract).
  - `Allow` — the capability gate reusing `battery/auth`'s `resource:verb`
    scopes (`auth.HasScope`).
  - `MountMarker` / `MountConfig` — the generic `data-fui-plugin*` mount marker
    + hidden-field HTML the generic broker scans for.
  - `RegisterBrokerRoute` / `UIHostOption` — serving + injecting the generic
    host broker (`host/pluginhost.js`), idempotent across plugins.
  - See [`docs/plugin-platform.md`](docs/plugin-platform.md).

- **`richtext` — the Rich Text editor** (`richtext/`). ProseMirror block editor,
  block-JSON canonical, markdown export + the pure-Go SSR read view (`richtext/ssr`).
  Plugin #1 and the forcing function that proved the third-party contract.
  See [`docs/richtext-editor.md`](docs/richtext-editor.md).

- **`richtext/ssr` — the SSR read renderer** (`richtext/ssr/`). Pure, deterministic
  Go `Render(doc map[string]any) → render.HTML` / `RenderJSON(docJSON)`, the
  server-side dual of the in-frame editor. Both implement the single schema in
  [`docs/design/schema-v1.md`](docs/design/schema-v1.md). Token-only HTML; unknown
  node/mark types degrade gracefully (forward-compatible).

- **`mermaid` — the second plugin** (`mermaid/`). An isolated Mermaid diagram
  editor/renderer — the completeness canary proving the extracted `pluginhost`
  platform generalizes beyond the editor. See [`docs/mermaid.md`](docs/mermaid.md).

- **`example` — the integration host** (`example/`). One GoFastr app that imports
  and mounts every plugin, serving both demos. The visual/e2e test surface and
  the completeness canary. Run with `go run ./example`.

- **Registry** — [`plugins.json`](plugins.json), the curated index (a
  convention, not a service): module path, version, isolation, capabilities,
  route prefix, schema, per plugin. Hosts fetch and vendor the file to generate
  a page per plugin.

- **Docs** — [`docs/plugin-platform.md`](docs/plugin-platform.md) (isolation +
  capability protocol + trust tiers + header/CSP contract + #37 relation +
  quickstart), [`docs/richtext-editor.md`](docs/richtext-editor.md),
  [`docs/mermaid.md`](docs/mermaid.md).

### Isolation / latency gate — CLEARED (2026-07-12)

The Phase-0 go/no-go gate (measured **p99 keystroke latency ≤ 16 ms** inside the
frame) is **PASS**:

- **p50 = 3.5 ms, p99 = 8.6 ms** (target ≤ 16 ms). All editing stays in-frame;
  the boundary carries only coarse events.
- Isolation proven from both sides: `sandbox="allow-scripts"` (no
  `allow-same-origin`), `iframe.contentDocument === null` from the parent, and
  in-frame probes confirm `document.cookie` / `localStorage` / `parent.document`
  are all unreachable.
- Round-trip (type → `docChanged` → host hidden fields), theme-token bridge
  (light/dark re-sync), and autosize all verified in `example/smoke_test.go`.

Load-bearing gotchas discovered (fed into `pluginhost`):
1. GoFastr's global security middleware sends `X-Frame-Options: DENY`, CSP
   `frame-ancestors 'none'`, and `Cross-Origin-Resource-Policy: same-origin` on
   every response — which blocks framing the editor AND blocks the opaque frame
   from fetching its own JS/CSS. `AssetServer` overrides these on framed assets
   (CSP `frame-ancestors 'self'` supersedes XFO; CORP `cross-origin`). See
   [`docs/DECISIONS.md`](docs/DECISIONS.md).
2. The frame CSP allows `style-src 'self' 'unsafe-inline'` (ProseMirror inline
   styles + the theme bridge `<style>:root{…}` block); `script-src` stays
   `'self'`.
3. `EditorState.create` needs an explicit `schema` when there's no initial doc.
4. Driving input into an opaque OOPIF via chromedp requires disabling site
   isolation in the test browser (a harness concern only).

### Notes

- The repo depends on a local GoFastr checkout via a `replace` directive
  (`../gofastr`), developed against gofastr `v0.20.0`. No published module
  version exists yet.
- Frozen design records: [`docs/PLAN.md`](docs/PLAN.md),
  [`docs/DECISIONS.md`](docs/DECISIONS.md), [`docs/design/`](docs/design/).
