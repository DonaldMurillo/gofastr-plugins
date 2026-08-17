---
date: 2026-07-25
author: Ada Bell
tags: meta, markdown
draft: true
summary: A draft post. It has a date in the past and is still hidden, because draft is true.
---

# Tags are just strings

This post is a draft. It exists so the recipe's tests have something to assert
against: `draft: true` keeps a post out of the homepage, the tag pages, the
archive, the search results, both feeds, and the sitemap, even though its date
is in the past.

Visit `/posts/tags-are-just-strings` directly and you get a 404. The lookup in
`Site.PostBySlug` finds it, and the post screen refuses to render anything that
is not published.

Draft content is still in the binary. `go:embed` does not know about
frontmatter, so anyone with the binary can read this text with `strings`. If a
draft is sensitive, keep it out of the repository rather than out of the
listings.
