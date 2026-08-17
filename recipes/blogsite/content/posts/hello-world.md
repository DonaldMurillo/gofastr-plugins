---
date: 2026-03-02
author: Ada Bell
tags: meta, markdown
summary: Every post on this site is a markdown file in content/posts. There is no database, no admin, and no build step.
---

# Hello world

This blog is a directory of markdown files. `content/posts/hello-world.md` is
the file you are reading. Adding a post means adding a file and restarting the
server; there is nothing else to update.

The whole content layer is `content.go`. At boot it walks `content/posts` and
`content/pages`, renders each file with `core/markdown`, and builds the
ordering, tag facets, and prev/next links once. After that a request never
parses markdown or touches the filesystem — it reads a slice.

## Why not a database

A blog with one author has no concurrent writers, no per-user views, and no
data that outlives the git history. Storing posts in Postgres would add a
migration story and a backup story to a site whose backup is `git clone`.

The trade is real, though: publishing requires a deploy. If you want to write
from a browser and hit publish, that is the other recipe — `recipes/blogapp`
stores posts in SQLite and edits them with the rich text editor.

## What "static" means here

Nothing is written to disk at build time. The server renders HTML per request
from the in-memory index, which takes microseconds because the markdown was
already parsed. You get the operational profile of a static site (one binary,
no external services) without a generator step.

The content is embedded with `go:embed`, so the compiled binary carries every
post. Copy it to a server and run it — there is no directory to ship alongside.
