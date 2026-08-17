---
menu: About
order: 1
---

# About

This is the demo content for `recipes/blogsite`, one of two blog recipes in
[gofastr-plugins](https://github.com/DonaldMurillo/gofastr-plugins). Every post
here is about the recipe itself, so each claim can be checked against the code
sitting next to it.

Pages like this one live in `content/pages`. They carry no date, never appear
in listings or feeds, and are registered at the top level — this file is
`content/pages/about.md` and it is served at `/about`.

Two frontmatter keys are specific to pages:

| Key | Meaning |
| --- | --- |
| `menu` | Nav label. Omit it and the page stays unlinked but reachable. |
| `order` | Sort key for the nav. Lower comes first. |

## The other recipe

If you want to write posts in a browser instead of a text editor,
`recipes/blogapp` stores posts in SQLite and edits them with the sandboxed
ProseMirror editor from this repository. The reading side of that app renders
without JavaScript, the same as this one.
