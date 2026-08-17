---
date: 2027-12-01
author: Ada Bell
tags: meta, workflow
summary: A scheduled post. Its date is in the future, so it is hidden until that date passes and the process restarts.
---

# Scheduled for later

This post is dated 1 December 2027. `Load` compares that against the clock it
was given, sets `Future` to true, and leaves the post out of `Site.Posts`.

Unlike the draft, nothing about this post is unfinished. It is waiting. When
the date passes and the process restarts, it appears with no edit — the same
mechanism a `publish_at` column gives you in a database-backed blog, without
the database.

The date is far enough out that the test asserting this post stays hidden will
keep passing for a long time. The test also pins the clock, so it would pass
regardless.
