---
date: 2026-04-11
author: Ada Bell
tags: markdown, reference
summary: GoFastr's markdown renderer is a dependency-free subset, not CommonMark. Here is everything it handles, rendered.
---

# The markdown subset

`core/markdown` is about 900 lines of Go with no dependencies. It is not
CommonMark-strict. It covers what people actually write in project docs and
posts, and it escapes raw HTML rather than passing it through.

## Text

**Bold**, *italic*, `inline code`, and [links](https://github.com/DonaldMurillo/gofastr).
Both `**` and `__` work for bold; both `*` and `_` for italic.

## Lists

Unordered:

- One item
- Another item
- A third

Ordered:

1. First
2. Second
3. Third

## Quotes

> A quote renders as a blockquote. Nesting is capped at 32 levels, because each
> level re-parses the remaining text and unbounded nesting is quadratic on a
> single line of `> > > >`.

## Code

Fenced blocks keep their language tag as a class. There is no syntax
highlighting — the renderer emits `<pre><code class="language-go">` and stops
there:

```go
site, err := Load(contentFS, time.Now())
if err != nil {
    log.Fatal(err)
}
```

## Tables

GFM-style pipe tables work:

| Element | Supported |
| --- | --- |
| Headings | `#` through `######` |
| Horizontal rules | `---`, `***`, `___` |
| Images | `![alt](url)` |
| Task lists | no |
| Footnotes | no |
| Inline HTML | escaped, never passed through |

---

## What it does not do

Task lists, footnotes, definition lists, and autolinks are all absent. So is
inline HTML — `<div>` in a source file renders as the literal text `<div>`.

That last one is a security property, not an oversight. A renderer that passes
HTML through is an XSS sink the moment any content is not written by you.
