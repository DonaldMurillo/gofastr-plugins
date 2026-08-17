---
date: 2026-05-06
author: Ada Bell
tags: syndication, seo
summary: The site publishes RSS 2.0, JSON Feed 1.1, and a sitemap. All three come from the same in-memory post index.
---

# Feeds and sitemaps

Three machine-readable views ship with the site:

- `/feed.xml` — RSS 2.0, for feed readers
- `/feed.json` — JSON Feed 1.1, for readers that prefer it
- `/sitemap.xml` — every public URL, for crawlers

They are generated in `feed.go` from `Site.Posts`, the same slice the homepage
iterates. Drafts and scheduled posts are absent from all three, because they
are absent from that slice.

## Absolute URLs

Feed and sitemap entries have to be absolute, so the handlers need to know the
site's origin. `main.go` resolves it in this order:

1. The `SITE_URL` environment variable, when set.
2. The request's `Host` header and scheme.

The fallback keeps `go run ./recipes/blogsite` working on a random port without
configuration. Set `SITE_URL` in production so a request with a spoofed `Host`
header cannot rewrite the URLs in your feed.

## What goes in an item

Each RSS item carries the title, the absolute link, the summary as the
description, the publication date in RFC 1123Z, and one `<category>` per tag.
The GUID is the absolute post URL with `isPermaLink="true"`.

The full post body is not included. Feed readers that render partial items send
readers to the site, which is where the typography and the code blocks actually
work.

## The sitemap is the route table

`/sitemap.xml` is not written by this recipe. `uihost.WithSitemap` enumerates
the routes registered with the UI host, and `screens.go` registers one route
per published post, tag, page, and pagination step. Add a markdown file, get a
route, get a sitemap entry — there is no list of URLs to keep in sync.

`/search` is the one exclusion. It is a form endpoint whose useful content
depends on a query string a sitemap cannot express, so it is excluded there and
disallowed in `robots.txt`.

Every entry carries one `lastmod`, set once at boot to the newest post's date.
That is honest about what this site knows: nothing here tracks per-post edits,
and a `lastmod` that claims otherwise is a lie a crawler acts on. There is no
`priority` element, because a self-reported priority is a number crawlers have
ignored for years.
