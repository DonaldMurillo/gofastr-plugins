---
date: 2026-07-20
author: Ada Bell
tags: go, deploy
summary: go:embed puts every post inside the binary. Deploying is copying one file, and there is no assets directory to forget.
---

# One binary, no assets directory

The content directory is embedded:

```go
//go:embed content
var contentFS embed.FS
```

`go build ./recipes/blogsite` produces one file that contains every post, every
page, and the CSS the framework generates. Deploy by copying it.

## What this rules out

Serving content from disk means the program has to find that directory, and the
answer differs between `go run ./recipes/blogsite`, `./blogsite` from the
repo root, and a container where the binary sits in `/usr/local/bin`. The
static-site example in GoFastr core handles this with a list of candidate paths
and a `log.Fatal` when none exist — honest, and still a class of bug that
embedding deletes.

Embedding also makes the content immutable at runtime. Nothing can write a post
except a rebuild, which means the deployed artifact and the git history cannot
disagree.

## The cost

Editing a post requires a restart. With `gofastr dev` that is automatic — the
watcher rebuilds on any file change and refreshes the browser — so the loop
during writing is the same as it would be with on-disk content.

The binary also grows with the corpus. Nine posts of markdown is about 20 KB.
Ten thousand posts with images would be a different conversation, and the
answer there is a CDN for the images and probably a real CMS for the posts.

## Images

There are no images in this recipe's content. When you add them, put them in
`content/` alongside the posts and mount that subtree with `core/static`, or
point `cover:` at a URL your CDN serves. Embedding a few hundred kilobytes of
JPEG per post works but stops being sensible quickly.
