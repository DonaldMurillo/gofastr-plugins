---
date: 2026-03-19
author: Ada Bell
tags: markdown, reference
summary: The eight frontmatter keys this recipe reads, what each one does, and what happens when you get one wrong.
---

# Frontmatter reference

Frontmatter is the `--- … ---` block at the top of a post. GoFastr's parser
handles `key: value` pairs and nothing else — no lists, no nesting, no
multi-line strings. That constraint shapes the schema below.

| Key | Required | Meaning |
| --- | --- | --- |
| `date` | yes | Publication date. `2006-01-02` or RFC 3339. |
| `title` | no | Overrides the first H1. Most posts just use the H1. |
| `slug` | no | URL segment. Defaults to the filename without `.md`. |
| `summary` | no | Card and feed excerpt. Falls back to the first paragraph. |
| `author` | no | Byline. |
| `tags` | no | Comma-separated. `go, markdown` becomes two tags. |
| `cover` | no | Image URL for the card and the Open Graph tag. |
| `draft` | no | `true` keeps the post out of every listing and feed. |

## Errors are boot errors

A post with no `date`, an unparseable `date`, no title, or a slug that collides
with another post stops the server from starting:

```
blogsite: posts/broken.md: unparseable date "last tuesday" (want 2006-01-02 or RFC 3339)
```

That is deliberate. The alternative — skipping the bad file, or defaulting the
date to zero — hides the mistake until someone notices a post sorted below
everything else with a 1970 timestamp in the feed. Feed readers cache
timestamps, so that mistake outlives the fix.

## Tags are comma-separated strings

There is no list syntax in the frontmatter parser, so `tags` is one string that
`parseTags` splits on commas. Tags are matched case-insensitively and
de-duplicated per post, but rendered in whatever casing you wrote:

```
tags: Go, go, GO
```

produces one tag, displayed as `Go`.
