---
date: 2026-07-09
author: Ada Bell
tags: search, go
summary: Site search is a linear scan over every post with a three-tier ranking. For a few hundred documents that is the right answer.
---

# Search without an index

`/search?q=markdown` scans every published post, scores each one, and sorts.
No inverted index, no separate service, no background job.

The scoring is three tiers:

```go
switch {
case strings.Contains(strings.ToLower(p.Title), q):
    score = 3
case strings.Contains(strings.ToLower(p.Summary), q):
    score = 2
case strings.Contains(strings.ToLower(p.Body), q):
    score = 1
}
```

A title match beats a summary match beats a body match. Within a tier, the
sort is stable and `Site.Posts` is already newest-first, so recency is the
tiebreak without any extra code.

## Why this is fine

Nine posts is nine string searches. A thousand posts, each 5 KB, is 5 MB of
`strings.Contains` per query — a few milliseconds, all of it in memory that is
already resident. The point at which this becomes the bottleneck is well past
the point at which a personal blog becomes something else.

## When to replace it

Substring matching has real limits. It does not stem, so "running" misses
"run". It does not tokenize, so "go" matches "algorithm". It has no
phrase-versus-term distinction and no relevance model beyond field position.

When those matter, `battery/search` in GoFastr core has a `search.Backend`
interface with an in-memory implementation, and the blog example in the core
repo shows the wiring. Swapping is a change to one method.

## Snippets

Results show 200 characters of body text centred on the match, with ellipses
where the text was cut. When the match was in the title only, the snippet falls
back to the summary — there is no body occurrence to centre on, and showing the
first 200 characters of the article instead would look like a bug.
