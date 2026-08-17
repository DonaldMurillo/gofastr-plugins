---
menu: Colophon
order: 2
---

# Colophon

## What renders this

| Piece | Package |
| --- | --- |
| Markdown | `gofastr/core/markdown` |
| Pages and routing | `gofastr/core-ui/app` |
| Components | `gofastr/framework/ui` |
| HTML shell, theme, SEO | `gofastr/framework/uihost` |

There is no CSS in this recipe. Every visual decision comes from the theme
tokens set in `main.go` and the component styles the framework registers.

## What it does not use

No JavaScript framework, no database, no template files, no build step, and
none of the plugins in this repository. The plugins are heavy client-side
features; a blog that only reads content does not need one.

`recipes/blogapp` is where a plugin earns its place — writing posts needs a
real editor, and that is the rich text plugin.

## Fonts and colors

The type is the system UI stack, so the site renders in the reader's own
interface font with no network request for a webfont. Colors come from the
adaptive theme, which ships complete light and dark palettes; the toggle in the
header switches between them and the OS preference sets the initial value.
