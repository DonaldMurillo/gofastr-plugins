# genui — generative UI in the cage

A model composes a view out of a **fixed registry of React components**. It
names components and props. It never emits markup, CSS, or code.

That sentence is the entire security design, and everything below is what makes
it true rather than aspirational.

## The registry is the boundary

Eight components — `Stack`, `Card`, `Heading`, `Text`, `Stat`, `Badge`,
`Table`, `Button` — each declaring exactly which props it takes and their
types. There is no `style`, no `className`, no `children` as a prop, and no
`dangerouslySetInnerHTML`, because no component declares them.

A composition is a tree of `{component, props, children?, action?}`. Anything
outside the registry is rejected **whole**, not sanitised. There is nothing to
sanitise: generated output never becomes markup on any path.

Bounded in two more ways a model can trip over without meaning to: depth 16,
and 200 nodes. A runaway tree fails validation instead of the renderer.

## Validated twice, on purpose

Go validates before storing and before serving. The frame validates again
before rendering.

This is not belt and braces. "The host already checked it" is precisely the
assumption that turns one bug into a rendered payload, and the frame is the
last thing standing between a composition and a DOM. The frame's copy is cheap
— same rules, same registry, no trust in the bridge.

Rejections name the path (`root.children[2].props.tone`), because a model's
output is debugged by a human reading that message.

## The model runs in Go

The composer and its credentials live host-side. The frame receives a finished
tree over the postMessage bridge, holds no key, and keeps `connect-src 'none'`.

Both halves of that matter. An API key in a browser is not a key. And a frame
that could call a model could also send it the document it was composing over —
so the frame cannot call anything at all.

```go
type Composer interface {
    Compose(ctx context.Context, prompt string, r Registry) (Composition, error)
}
```

The default is `FixtureComposer`: deterministic, offline, a small prompt→tree
table with a fallback card. That is what the demo and **every test** use. A
plugin whose tests need an API key is a plugin nobody can contribute to. A real
client goes behind the same interface, and `WithComposer` swaps it in.

## The real client

`AnthropicComposer` talks to the Messages API over plain `net/http` — no SDK, so
this module gains no dependency and no version to track for about forty lines of
request building. A host that prefers an SDK implements `Composer` itself; that
is what the interface is for.

```go
genui.New(genui.WithComposer(genui.NewAnthropicComposer(genui.AnthropicConfig{})))
```

The key comes from `APIKey` or `ANTHROPIC_API_KEY`, read at compose time rather
than construction so a host that builds its plugin graph before loading secrets
still works.

Three properties worth stating, because they are what the tests pin:

- **The tool call is forced.** Without `tool_choice` the model may answer in
  prose containing JSON, and the caller is back to scraping. Forcing it makes
  the answer arrive as a typed object.
- **Model output is not trusted because it came from a model.** It goes through
  the same `Validate` as everything else. A refusal is handed back verbatim —
  the validator's message names the offending path, which is what a model needs
  to correct it and what a human would need too — and the model gets one more
  attempt.
- **A composition that fails twice is an error, not a fallback.** Quietly
  repairing bad output, or silently dropping to the fixture composer, would hide
  exactly the case this plugin exists to demonstrate.

The tool's JSON schema describes the *shape* and deliberately does not encode
every per-component prop rule. A schema enumerating eight components' prop sets
would be a second copy of the registry, drifting from the first. The schema gets
the model into the right shape; the validator decides whether the answer is
acceptable.

Errors never carry the response body — an API error message can echo a key back
— but they do carry the API's error type, so a 401 still says
`authentication_error`.

Every test runs against an `httptest` server with canned responses. No test
calls a model: a test that did would depend on the model, and what is under test
is what this code does with an answer, including answers a model should not have
given.

## Actions are allow-listed

A generated `Button` may carry an `action`, and validation rejects any name the
host did not put in the allow-list (`WithActions`). A generated control cannot
point anywhere the host never named — the frame emits `uiAction` and the host
decides what, if anything, that means.

## Async by design

`POST /compose` returns an id immediately and generates in the background;
`GET /composition/{id}` reports `pending` / `ready` / `failed`. The frame shows
a placeholder, then the finished view. No tokens streamed into the DOM.

## Watch it refuse

The demo page has a **Try an unsafe composition** button. It posts a
composition naming a component that does not exist straight at the frame,
skipping the Go validator, and the frame refuses it in front of you with the
reason. The e2e drives the same seam for an unknown component, an undeclared
prop, and an action outside the allow-list.

That seam grants nothing: a host page can already `postMessage` to a frame it
mounted. What it demonstrates is that doing so gets you refused — and it is the
only way the second validator is ever exercised, since everything arriving
through `compose()` has already passed the first one.
