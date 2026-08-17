package main

// Seed content, so a fresh `go run ./recipes/blogapp` opens on something to
// read instead of an empty state.
//
// The bodies are real ProseMirror documents — the same shape the editor emits
// and richtext/ssr consumes. Writing them by hand here is the honest way to
// prove the read path works without the editor: if these render, the server
// side is doing the whole job.

import (
	"strings"
	"time"
)

// seedIfEmpty loads the starter posts, but only into a database that has none.
// A file-backed run (BLOG_DB set) keeps whatever the author wrote; only the
// default in-memory database gets seeded on every boot.
func (a *app) seedIfEmpty() error {
	existing, err := a.store.All()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}

	now := time.Now().UTC()
	for i, s := range seedPosts {
		p := &Post{
			Title:    s.title,
			Summary:  s.summary,
			Tags:     splitTags(s.tags),
			BodyJSON: s.body,
			BodyMD:   s.markdown,
			Status:   s.status,
		}
		if s.status == StatusPublished {
			// Space them out so the listing, archive, and feed have a real
			// chronology rather than five posts sharing one timestamp.
			p.PublishedAt = now.AddDate(0, 0, -7*(len(seedPosts)-i))
		}
		created, err := a.store.Create(p)
		if err != nil {
			return err
		}
		// Create stamps created_at/updated_at with the clock. Push the
		// published ones back so "newest first" matches PublishedAt.
		if !p.PublishedAt.IsZero() {
			created.UpdatedAt = p.PublishedAt
			if err := a.store.Update(created); err != nil {
				return err
			}
		}
	}
	return nil
}

type seedPost struct {
	title, summary, tags, status string
	body, markdown               string
}

// doc wraps top-level blocks into a ProseMirror document.
func doc(blocks ...string) string {
	return `{"type":"doc","content":[` + strings.Join(blocks, ",") + `]}`
}

func para(text string) string {
	return `{"type":"paragraph","content":[{"type":"text","text":` + jsonString(text) + `}]}`
}

func heading(level int, text string) string {
	return `{"type":"heading","attrs":{"level":` + itoa(level) + `},"content":[{"type":"text","text":` + jsonString(text) + `}]}`
}

func quote(text string) string {
	return `{"type":"blockquote","content":[` + para(text) + `]}`
}

func code(lang, text string) string {
	return `{"type":"code_block","attrs":{"language":` + jsonString(lang) + `},"content":[{"type":"text","text":` + jsonString(text) + `}]}`
}

func bullets(items ...string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, `{"type":"list_item","content":[`+para(it)+`]}`)
	}
	return `{"type":"bullet_list","content":[` + strings.Join(parts, ",") + `]}`
}

var seedPosts = []seedPost{
	{
		title:   "Why the reader never loads the editor",
		summary: "Posts are stored as ProseMirror JSON and rendered to HTML on the server, so reading this page runs no JavaScript at all.",
		tags:    "architecture, richtext",
		status:  StatusPublished,
		body: doc(
			para("The rich text plugin is a sandboxed iframe carrying a ProseMirror bundle. It is the right tool for writing and the wrong thing to ship to a reader who only wants to read."),
			heading(2, "Two representations, one source of truth"),
			para("Every post keeps two columns. body_json is the canonical ProseMirror document. body_md is a markdown export."),
			bullets(
				"body_json is what the editor loads and what the read view renders.",
				"body_md is lossy — it is what search greps, and nothing reconstructs a post from it.",
			),
			heading(2, "The read path"),
			para("richtext/ssr turns the stored document into design-token HTML in pure Go:"),
			code("go", "html, err := ssr.RenderJSON(post.BodyJSON)"),
			quote("Callouts, tables, task lists, code blocks, and colored text all survive the trip. So does a browser with JavaScript switched off."),
		),
		markdown: "The rich text plugin is a sandboxed iframe carrying a ProseMirror bundle. " +
			"Every post keeps two columns: body_json is canonical, body_md is a lossy export. " +
			"richtext/ssr turns the stored document into HTML in pure Go.",
	},
	{
		title:   "The capability gate is not an authentication gate",
		summary: "The plugin's grant check answers whether the plugin holds a capability, not whether the caller may use it. The app has to answer the second question.",
		tags:    "security, richtext",
		status:  StatusPublished,
		body: doc(
			para("This is the trap worth reading the source for. The rich text plugin gates its save endpoint like this:"),
			code("go", "pluginhost.Allow(ctx, granted, cap)\n  = auth.ScopeMatch(granted, cap) && auth.HasScope(ctx, cap)"),
			para("And auth.HasScope returns true when the context carries no token scopes — sessions and JWTs are unscoped by design. So an anonymous POST to the save endpoint passes that check."),
			heading(2, "What the gate actually means"),
			para("It answers: is this capability inside the set this plugin was granted? That is a question about the plugin's authority, not the caller's identity."),
			heading(2, "Where this app puts the real gate"),
			para("Inside the save and upload handlers. Session middleware runs for every route, including the plugin's own endpoints, so the handlers read the admin session off the request context and refuse anonymous writes."),
			quote("There is a test for it: an anonymous save for a real post id must leave the stored body untouched."),
		),
		markdown: "The plugin's capability gate answers whether the plugin holds a capability, " +
			"not whether the caller may use it. auth.HasScope returns true for unscoped contexts, " +
			"so the app gates the save and upload handlers on its own admin session.",
	},
	{
		title:   "Slugs, drafts, and the soft-404 problem",
		summary: "A database-backed blog needs dynamic routes, and a dynamic route matches an unknown slug. Answering 200 for a post that does not exist is the bug.",
		tags:    "architecture, seo",
		status:  StatusPublished,
		body: doc(
			para("The sibling recipe, blogsite, registers one route per post because its corpus is fixed when the process starts. This one cannot: posts are created while the server runs."),
			para("So the public post route is a pattern, and a pattern matches everything shaped like a slug — including slugs that name nothing."),
			heading(2, "Why 200 is wrong"),
			bullets(
				"Crawlers index the not-found page once per bad URL.",
				"Uptime checks pass while every link is broken.",
				"A client cannot tell a missing post from an empty one.",
			),
			heading(2, "The fix"),
			para("A middleware resolves the slug before the host routes. When nothing matches it rewrites the path to the /404 screen and forces the status code, so the reader gets a full themed page and the crawler gets the truth."),
		),
		markdown: "A database-backed blog needs dynamic routes, and a dynamic route matches an unknown slug. " +
			"A middleware resolves the slug before routing and rewrites misses to /404 with a real 404 status.",
	},
	{
		title:   "A draft nobody can read",
		summary: "This post is a draft. It is listed in the admin and nowhere else.",
		tags:    "meta",
		status:  StatusDraft,
		body: doc(
			para("Drafts are excluded from the listing, the tag pages, the archive, search, the feed, and the sitemap."),
			para("Visiting the slug directly returns 404 rather than the post, because the not-found middleware treats an unpublished post exactly like a missing one."),
		),
		markdown: "Drafts are excluded everywhere and their slugs return 404.",
	},
}

// jsonString quotes and escapes a Go string for embedding in the hand-written
// JSON above. Enough for the seed text, which is why it lives here rather than
// pretending to be a general encoder.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func itoa(n int) string { return string(rune('0' + n)) }
