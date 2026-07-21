# Guided-tour ("app cues") plugin

An Appcues-style **guided product tour**: it spotlights real elements on the host
page with stepped tooltip bubbles (Back / Next / Skip, progress, keyboard nav),
and remembers completion so a tour does not auto-run twice.

Unlike every other plugin in this repo it is **NOT sandboxed**. A tour must reach
the host page's real DOM to highlight elements, so it cannot run in an
opaque-origin iframe. It is the platform's first **trusted host-page** plugin: a
vouched-for, same-origin runtime script the app owner compiles in.

- **Module:** `github.com/DonaldMurillo/gofastr-plugins/tour`
- **Route prefix:** `/__gofastr/plugin/tour`
- **Isolation:** `trusted-host-page` (`trusted: true`, no sandbox — see the
  per-isolation guards in `internal/registry`)
- **Capabilities:** `tour:read`, `tour:write`

## Mounting

```go
app.RegisterPlugin(tour.New(
    tour.WithDevGrantAll(),   // demo/dev only — opens the tour:read/write gate
    tour.WithDemoPage(),      // serves a self-contained demo at /tour
    tour.WithTour("welcome", []tour.Step{
        {Selector: "#toolbar", Title: "Toolbar", Body: "Format text here.", Placement: tour.PlacementBottom},
        {Selector: "#editor",  Title: "Write",   Body: "Your content goes here."},
        {Selector: "#save",    Title: "Save",    Body: "Persist your work."},
    }),
))
```

Inject the runtime into host pages with `tour.UIHostOption()` (a single
same-origin `<script>`; the runtime loads its own stylesheet). Then trigger a
tour from page JS:

```js
window.gofastrTour.autoRun("welcome"); // runs only if not already seen
window.gofastrTour.run("welcome");     // always runs
window.gofastrTour.restart("welcome"); // clears seen state and runs
```

## Step actions — reaching buried UI

A step can perform actions on the host page to reveal a target that isn't
visible yet (open a sidebar, expand a toggle, or navigate) — so a tour never
dead-ends on a hidden element. `Before` runs on entering the step; `After` runs
when advancing past it.

```go
tour.Step{
    Selector: "#buried-link",
    Title:    "Buried setting",
    Body:     "It lives inside the Advanced panel.",
    Before: []tour.Action{
        {Type: "click", Selector: "#open-advanced"}, // reveal it
        {Type: "wait",  Selector: "#buried-link"},   // wait for it to appear
    },
    After: []tour.Action{{Type: "click", Selector: "#open-advanced"}}, // collapse again
}
```

Action types: `click` (a selector), `wait` (poll for a selector, best-effort up
to a few seconds), `navigate` (a `URL`). Actions are best-effort — a bad selector
never bricks the walkthrough.

## Custom content and styling

A step's bubble can render app-authored content instead of the default
title/body — pass **`html`** (a trusted HTML string; server- or JS-defined
tours), or, when driving the tour from JS, a live **`content`** node or a
**`render(el)`** function (mount any component / framework output):

```js
window.gofastrTour.run({ id: "welcome", steps: [
  { selector: "#chart", render: (el) => el.appendChild(MyReactRoot()) },
  { selector: "#save",  html: "<h3>Custom</h3><p>Your markup here.</p>" },
]});
```

Tour-level options (`tour.WithTourOptions` / the JS `options` bag) configure
behaviour and styling — each defaults ON:

```go
tour.WithTourOptions("welcome", tour.TourOptions{
    Accent:    "#7c3aed",  // accent color (--gofastr-tour-accent)
    Width:     "420px",    // bubble max-width
    ClassName: "my-tour",  // extra class on every bubble for your CSS
    // *bool toggles: ShowProgress, ShowDots, AllowKeyboard, CloseOnEscape, Backdrop
})
```

## Endpoints

- `GET  /__gofastr/plugin/tour/tours/{id}` — the registered tour's steps as JSON.
- `POST /__gofastr/plugin/tour/seen` `{tourId}` — mark completion.
- `GET  /__gofastr/plugin/tour/seen?tourId=…` — `{seen: bool}`.

## Persistence

Completion is recorded **client-side** (localStorage) for instant auto-run
decisions and **server-side** via the seen endpoint. The default server store is
in-memory keyed by tour id; supply `tour.WithSeenHandler` to persist per-user in
a real database. `run` on Done **and** Skip both mark the tour seen; `restart`
clears it.

## Accessibility

`role="dialog"` bubble, `aria-live` step body, focus trap across the controls,
and keyboard navigation (`→`/`Enter` next, `←` back, `Esc` skip). Targets are
scrolled into view before each step; the overlay repositions on scroll/resize.
