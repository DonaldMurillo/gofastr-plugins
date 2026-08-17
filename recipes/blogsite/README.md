# blogsite — the markdown blog

A blog whose content is a directory of markdown files. No database, no build
step, no JavaScript framework, and no plugin from this repository.

```sh
go run ./recipes/blogsite
```

It prints its URL. `PORT` pins the port; `SITE_URL` sets the absolute origin
used in the feeds and sitemap.

## What is here

```
blogsite/
├── content/
│   ├── posts/*.md      one file per post, frontmatter on top
│   └── pages/*.md      standalone pages (about, colophon)
├── content.go          parse + index: ordering, tags, prev/next, search
├── chrome.go           header, footer, and the shared post card
├── screens.go          one screen per page type, and route registration
├── feed.go             RSS 2.0 + JSON Feed
└── main.go             wiring
```

## Frontmatter

```yaml
---
date: 2026-03-19        # required; 2006-01-02 or RFC 3339
title: Optional         # defaults to the first H1
slug: optional          # defaults to the filename
summary: Optional       # defaults to the first paragraph
author: Optional
tags: go, markdown      # comma separated — the parser has no list syntax
draft: true             # hides it everywhere
---
```

A post with no date, an unparseable date, no title, or a slug that collides with
another post stops the server from starting. That is deliberate: skipping the
file or defaulting the date hides the mistake until someone notices a post
sorted below everything else with a 1970 timestamp in the feed, and feed readers
cache timestamps.

Standalone pages take two more keys: `menu` (nav label — omit it and the page
stays unlinked but reachable) and `order` (nav sort key).

## Features

Tag pages and a tag index, a year-grouped archive, pagination, substring search
with a three-tier ranking, related posts by shared tags, reading-time estimates,
prev/next links, RSS 2.0, JSON Feed 1.1, `sitemap.xml`, `robots.txt`, and a
themed 404 that carries recovery links.

Drafts (`draft: true`) and future-dated posts are both hidden, and they mean
different things: a draft is unfinished, a future date is a schedule that
resolves itself when the process next starts.

## Two decisions worth knowing

**Every route is registered at boot.** There is no `/posts/:slug` pattern — the
corpus is fixed when the process starts, so each post, tag, page, and pagination
step gets its own route. An unknown slug therefore matches nothing and gets a
real 404 instead of a soft one, and the sitemap can be derived from the route
table rather than kept in a second list.

**Markdown is parsed once, at load.** `ui.Markdown` turns each body into themed
prose with syntax-highlighted code blocks; doing that per request would repeat
real work for output that cannot change. The component's style marker travels
inside the returned HTML, so the host still finds it when deciding which
stylesheets to link.

## The other recipe

If you want to write posts in a browser instead of a text editor, see
[`recipes/blogapp`](../blogapp/) — same reading experience, posts in SQLite,
edited with the rich text plugin.
