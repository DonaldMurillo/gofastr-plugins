---
date: 2026-06-14
author: Ada Bell
tags: meta, workflow
summary: A post is hidden if draft is true, or if its date is in the future. The second one resolves itself.
---

# Drafts and scheduling

Two things keep a post out of the public listings, and they mean different
things.

`draft: true` is author intent. The post is not finished and no amount of
waiting changes that.

A `date` in the future is a schedule. The post is finished; it should appear on
that date. `Load` compares each post's date against the clock it is given and
sets `Future` accordingly.

## The clock is a parameter

`Load(fsys, now)` takes the current time rather than calling `time.Now()`
itself:

```go
site, err := Load(contentFS, time.Now())
```

Tests pass a fixed instant. Without that, a test fixture dated "one year from
now" quietly becomes a published post a year later, and the assertion that it
stays hidden fails on a date nobody can predict from the diff.

## Scheduling needs a restart

The index is built once at boot, so a post scheduled for midnight appears when
the process next starts, not at midnight. For a site that deploys on every
content change this is a non-issue. If you need the post to appear on time
without a deploy, run the loader on a ticker and swap the `*Site` pointer
behind a mutex.

## Both states are still addressable

`Site.PostBySlug` returns drafts and scheduled posts. Only the listings filter
them out. That is what makes a preview route possible: one handler that looks
up any slug and requires a token, without a second index.

This recipe does not ship that route, because a preview URL is an
authentication decision and the right answer depends on who is previewing.
