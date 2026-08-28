# Demo page design standard

Every plugin in this repo ships a demo page. That page is the plugin's only
public face: it is what the gallery frames, what the screenshots capture, and
what anyone deciding whether to use the plugin actually looks at. A plugin that
works perfectly behind an ugly demo page has failed the part of the job people
can see.

The standard is [`richtext/demo.go`](../richtext/demo.go). Read it before
writing a new one. This document exists because the four plugins built after
richtext each drifted a little further from it, and the drift was invisible
until someone put two screenshots side by side.

## The beats every demo page hits

In order, top to bottom.

**1. Brand bar.** Product mark, breadcrumb (`GoFastr / plugins / <name>`), any
mode links this plugin offers, and the theme toggle. Same height and treatment
on every page.

**2. Hero.** A headline of two short lines that says what the thing is and what
is surprising about it. richtext uses "A Notion-class editor. / Genuinely
sandboxed." Not "Data grid - Demo". Then one lead paragraph, at most three
sentences, in larger-than-body type. Then a row of fact chips: the one
architectural fact, plus two or three measured numbers.

The headline is the single highest-leverage element on the page. Write it last,
after you know what the plugin actually proves, and make it a claim rather than
a label.

**3. The plugin, in chrome.** The mount sits inside a card with a title bar:
three dots, a filename or document title, and a mode badge on the right
(`sandboxed iframe`, `trusted host page`). The chrome is not decoration. It is
the visual statement that this thing is a contained program, which is the whole
argument of the project.

**4. Affordance strip.** Directly under the frame: keyboard hints as `<kbd>`
chips on pointer devices, a touch-appropriate variant on small screens, and the
primary action button right-aligned with a live status region next to it.

**5. Three feature cards.** The claim, restated as three concrete facts with
real values in them. Cite actual capability strings, actual byte counts, actual
CSP directives. Never three vague virtues.

**6. Footer.** Plugin name and version, plus a link to a related view.

## Rules

**The page must resolve.** No plugin demo may end with a screenful of empty
background. If the plugin's own area is short, the frame grows or the cards
move up. The datagrid demo shipped with 400px of dead white space under a
430px grid; that is what this rule exists to stop.

**Declare the whole palette on `:root` in the frame stylesheet.** Per-property
`var(--token, fallback)` fallbacks work, but they scatter the defaults across
every rule that uses one, and keeping them coherent then depends on each author
pairing light with light. A single `:root` block puts them in one reviewable
place and makes the frame legible before any token arrives.

This is a tidiness rule, not a defence against a known failure. It was
originally written up as the latter, on the strength of a contrast failure that
turned out to be a measurement artifact — axe cannot resolve a background across
an opaque-origin frame and assumes white. Worth doing anyway; not worth
believing a story about.

**Tokens only.** No hardcoded colors. The accent is the amber already in the
design system, not whatever the plugin's upstream library defaults to. The pdf
demo's purple Export button came straight from a vendor stylesheet and it looks
like it.

**Both themes are designed, not merely supported.** Take a screenshot in each
and look at both. Dark is not light with inverted values: check chip contrast,
frame borders, and any vendor-supplied widget that brings its own palette.

**Data gets visual weight.** A status column of the words active, pending,
blocked, expired is a design failure; those are pills with color. A number that
is the point of the plugin belongs in display type, not 11px grey beside a
filter box. Money and quantities are right-aligned and tabular.

**Controls use the house classes.** `fui-btn`, `fui-btn-primary`, `badge`,
`chip`, `kbd`. A browser-default `<input>` or `<button>` on a demo page is a
bug.

**Show the proof.** Whatever claim the plugin exists to make, the page displays
it live. The datagrid claim is that the frame only ever holds pages, so the
page shows a running "rows delivered / 100,000" counter and the resident block
count. The pdf claim is that the frame has no network, so the page states
`connect-src 'none'` where you can see it. If the proof is only in a test file,
the demo is not doing its job.

**Responsive is part of it.** Check 390px. The affordance strip switches to the
touch variant, the cards stack, the frame keeps a usable height, and nothing
overflows horizontally.

## Before you call a demo page done

1. Run it and take a full-page screenshot in light and dark, desktop and 390px.
2. Look at all four. Not the DOM, the pixels.
3. Put the richtext demo next to yours. If yours looks like a different
   product, it is not finished.

`SHOTS=1 npm run shots` in `e2e/` writes PNGs to `e2e/shots/` for exactly this.
