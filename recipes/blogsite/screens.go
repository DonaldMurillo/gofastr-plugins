package main

// The screens. Each one is a component.Component holding the data it renders,
// resolved at boot rather than per request.
//
// There are no dynamic route patterns here — no "/posts/:slug". The corpus is
// known when the process starts, so every post, tag, page, and pagination step
// gets its own registered route. Two things follow from that:
//
//   - An unknown slug matches no route, so the host's 404 screen renders with
//     a real 404 status. A ":slug" screen would have to serve its own
//     not-found body at HTTP 200, which is the soft-404 crawlers penalize.
//   - The route table IS the sitemap. uihost.WithSitemap enumerates registered
//     routes, so nothing has to keep a second list of URLs in sync.

import (
	"context"
	"fmt"
	"strconv"

	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core-ui/component"
	"github.com/DonaldMurillo/gofastr/core-ui/html"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"
)

// postsPerPage is the homepage/pagination window.
const postsPerPage = 4

// ─── Post ────────────────────────────────────────────────────────────

type postScreen struct {
	site *Site
	post *Post
}

func (s *postScreen) ScreenTitle() string       { return s.post.Title }
func (s *postScreen) ScreenDescription() string { return s.post.Summary }

func (s *postScreen) Render() render.HTML {
	p := s.post

	// DocLayout gives the article the reading measure, the breadcrumb trail,
	// and the prev/next footer. Nav and Toc are left empty, which puts it in
	// its single-column narrow mode — a blog post has no sibling rail.
	pager := &ui.DocPager{
		PrevHref: "/", PrevLabel: "All posts",
	}
	if p.Prev != nil {
		pager.PrevHref, pager.PrevLabel = "/posts/"+p.Prev.Slug, p.Prev.Title
	}
	if p.Next != nil {
		pager.NextHref, pager.NextLabel = "/posts/"+p.Next.Slug, p.Next.Title
	}

	body := []render.HTML{
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    p.Title,
			Subtitle: p.Summary,
			Eyebrow:  postMeta(p),
		}),
		tagChips(p.Tags),
		p.HTML,
	}

	if related := s.site.Related(p, 3); len(related) > 0 {
		body = append(body,
			ui.Divider(ui.DividerConfig{}),
			ui.Section(ui.SectionConfig{Heading: "Related posts"},
				postList(related, "", "")),
		)
	}

	return ui.DocLayout(ui.DocLayoutConfig{
		Crumbs: []ui.DocCrumb{
			{Label: "Posts", Href: "/"},
			{Label: p.Title},
		},
		Pager: pager,
	}, ui.Stack(ui.StackConfig{Gap: ui.GapLG}, body...))
}

// ─── Post listing (homepage + /page/N) ───────────────────────────────

type listScreen struct {
	site       *Site
	page       int // 1-based
	totalPages int
}

func (s *listScreen) ScreenTitle() string {
	// Page 1 is the site root: an empty title lets the host use the bare app
	// name for <title> rather than "Posts — Notes on a flat file".
	if s.page == 1 {
		return ""
	}
	return fmt.Sprintf("Posts, page %d", s.page)
}

func (s *listScreen) ScreenDescription() string { return tagline }

func (s *listScreen) Render() render.HTML {
	start := (s.page - 1) * postsPerPage
	end := start + postsPerPage
	if end > len(s.site.Posts) {
		end = len(s.site.Posts)
	}
	window := s.site.Posts[start:end]

	parts := []render.HTML{}

	if s.page == 1 {
		parts = append(parts, ui.Hero(ui.HeroConfig{
			Eyebrow:  "Recipe · blogsite",
			Title:    siteName,
			Subtitle: tagline + " No database, no build step, and no JavaScript framework — the whole site is markdown files rendered by GoFastr.",
			Actions: []render.HTML{
				ui.LinkButton(ui.LinkButtonConfig{Label: "Read the first post", Href: "/posts/" + s.site.Posts[0].Slug}),
				ui.LinkButton(ui.LinkButtonConfig{Label: "Source on GitHub", Href: recipeSourceURL, Variant: ui.ButtonSecondary, External: true}),
			},
		}))
	} else {
		// No eyebrow repeating "Page N of M" — the pager below already says it,
		// and two copies of the same sentence is one for a screen reader to
		// announce twice.
		parts = append(parts, ui.PageHeader(ui.PageHeaderConfig{
			Title: fmt.Sprintf("Posts, page %d", s.page),
		}))
	}

	parts = append(parts, postList(window, "Nothing here yet", "Add a markdown file to content/posts."))

	if s.totalPages > 1 {
		parts = append(parts, pager(s.page, s.totalPages))
	}
	return ui.Stack(ui.StackConfig{Gap: ui.GapXL}, parts...)
}

// pager renders the older/newer controls plus a "page N of M" readout. It is a
// plain link pair rather than a numbered list: with four posts a page, a blog
// reaches double-digit pages before the numbers would help anyone.
func pager(page, total int) render.HTML {
	var newer, older render.HTML
	if page > 1 {
		href := "/"
		if page > 2 {
			href = "/page/" + strconv.Itoa(page-1)
		}
		newer = ui.LinkButton(ui.LinkButtonConfig{Label: "← Newer posts", Href: href, Variant: ui.ButtonSecondary})
	}
	if page < total {
		older = ui.LinkButton(ui.LinkButtonConfig{Label: "Older posts →", Href: "/page/" + strconv.Itoa(page+1), Variant: ui.ButtonSecondary})
	}
	return render.Tag("nav", map[string]string{"aria-label": "Pagination"},
		ui.Cluster(ui.ClusterConfig{Gap: ui.GapMD, Align: ui.AlignCenter, Justify: ui.JustifyBetween},
			newer,
			ui.Muted(render.Text(fmt.Sprintf("Page %d of %d", page, total))),
			older,
		))
}

// ─── Tags ────────────────────────────────────────────────────────────

type tagIndexScreen struct{ site *Site }

func (s *tagIndexScreen) ScreenTitle() string { return "Tags" }
func (s *tagIndexScreen) ScreenDescription() string {
	return fmt.Sprintf("Every tag used across %d posts.", len(s.site.Posts))
}

func (s *tagIndexScreen) Render() render.HTML {
	chips := make([]render.HTML, 0, len(s.site.Tags))
	for _, t := range s.site.Tags {
		chips = append(chips, ui.Tag(ui.TagConfig{
			Label: fmt.Sprintf("%s (%d)", t.Tag, t.Count),
			Href:  "/tags/" + t.Slug,
		}))
	}
	return ui.Stack(ui.StackConfig{Gap: ui.GapLG},
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    "Tags",
			Subtitle: "Busiest first. A tag page lists every post carrying it.",
		}),
		ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM}, chips...),
	)
}

type tagScreen struct {
	tag   TagCount
	posts []*Post
}

func (s *tagScreen) ScreenTitle() string { return "Tagged " + s.tag.Tag }
func (s *tagScreen) ScreenDescription() string {
	return fmt.Sprintf("%d posts tagged %s.", s.tag.Count, s.tag.Tag)
}

func (s *tagScreen) Render() render.HTML {
	return ui.Stack(ui.StackConfig{Gap: ui.GapLG},
		ui.PageHeader(ui.PageHeaderConfig{
			Title:   s.tag.Tag,
			Eyebrow: "Tag",
			Subtitle: fmt.Sprintf("%d %s.", s.tag.Count,
				plural(s.tag.Count, "post", "posts")),
			Actions: ui.LinkButton(ui.LinkButtonConfig{Label: "All tags", Href: "/tags", Variant: ui.ButtonSecondary}),
		}),
		postList(s.posts, "No posts", "Nothing carries this tag."),
	)
}

// ─── Archive ─────────────────────────────────────────────────────────

type archiveScreen struct{ site *Site }

func (s *archiveScreen) ScreenTitle() string { return "Archive" }
func (s *archiveScreen) ScreenDescription() string {
	return "Every post, grouped by year, newest first."
}

func (s *archiveScreen) Render() render.HTML {
	sections := []render.HTML{
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    "Archive",
			Subtitle: fmt.Sprintf("All %d posts, newest first.", len(s.site.Posts)),
		}),
	}

	// Site.Posts is already sorted newest-first, so a single pass emits the
	// year groups in order without collecting into a map first (which would
	// then need re-sorting to be deterministic).
	year := 0
	var rows []render.HTML
	flush := func() {
		if year == 0 {
			return
		}
		sections = append(sections, ui.Section(ui.SectionConfig{Heading: strconv.Itoa(year)},
			html.UnorderedList(html.ListConfig{}, rows...)))
		rows = nil
	}
	for _, p := range s.site.Posts {
		if p.Date.Year() != year {
			flush()
			year = p.Date.Year()
		}
		rows = append(rows, html.ListItem(html.ListItemConfig{},
			ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM, Align: ui.AlignBaseline},
				ui.Muted(render.Text(p.Date.Format("2 Jan"))),
				html.Link(html.LinkConfig{Href: "/posts/" + p.Slug, Text: p.Title}),
			)))
	}
	flush()

	return ui.Stack(ui.StackConfig{Gap: ui.GapLG}, sections...)
}

// ─── Standalone page ─────────────────────────────────────────────────

type pageScreen struct{ page *Page }

func (s *pageScreen) ScreenTitle() string       { return s.page.Title }
func (s *pageScreen) ScreenDescription() string { return "" }

func (s *pageScreen) Render() render.HTML {
	return ui.DocLayout(ui.DocLayoutConfig{},
		ui.Stack(ui.StackConfig{Gap: ui.GapLG},
			ui.PageHeader(ui.PageHeaderConfig{Title: s.page.Title}),
			s.page.HTML,
		))
}

// ─── Search ──────────────────────────────────────────────────────────

// searchScreen is the one screen whose output depends on the request. It reads
// ?q= from the context the host attaches, so it implements RenderCtx and
// embeds ContextOnly to satisfy the plain Component interface.
type searchScreen struct {
	component.ContextOnly
	site *Site
}

func (s *searchScreen) ScreenTitle() string       { return "Search" }
func (s *searchScreen) ScreenDescription() string { return "Search every published post." }

func (s *searchScreen) RenderCtx(ctx context.Context) render.HTML {
	query := appui.QueryFromContext(ctx).Get("q")
	results := s.site.Search(query)

	parts := []render.HTML{
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    "Search",
			Subtitle: "Matches titles, summaries, and body text across every published post.",
		}),
		ui.SearchInput(ui.SearchInputConfig{
			Name:        "q",
			ID:          "search-page-q",
			Placeholder: "Search posts",
			Action:      "/search",
			ExtraAttrs:  map[string]string{"value": query},
		}),
	}

	switch {
	case query == "":
		parts = append(parts, ui.Muted(render.Text("Type a query to search.")))
	case len(results) == 0:
		parts = append(parts, ui.EmptyState(ui.EmptyStateConfig{
			Title:        "No matches for “" + query + "”",
			Description:  "Search is a substring match — it does not stem words, so try a shorter query.",
			HeadingLevel: 2,
			Action:       ui.LinkButton(ui.LinkButtonConfig{Label: "Browse all posts", Href: "/", Variant: ui.ButtonSecondary}),
		}))
	default:
		parts = append(parts, ui.Muted(render.Text(fmt.Sprintf(
			"%d %s for “%s”.", len(results), plural(len(results), "match", "matches"), query))))
		cards := make([]render.HTML, 0, len(results))
		for _, r := range results {
			cards = append(cards, ui.Card(ui.CardConfig{
				Heading:     r.Post.Title,
				Description: r.Snippet,
				Href:        "/posts/" + r.Post.Slug,
				Footer:      ui.Muted(render.Text(postMeta(r.Post))),
			}))
		}
		parts = append(parts, ui.Stack(ui.StackConfig{Gap: ui.GapMD}, cards...))
	}

	return ui.Stack(ui.StackConfig{Gap: ui.GapLG}, parts...)
}

// ─── 404 ─────────────────────────────────────────────────────────────

// notFoundScreen is handed to uihost.WithNotFoundScreen, so it renders inside
// the normal chrome and the response still carries a 404 status. It implements
// uihost.NotFoundRenderer to echo the path that missed — passed as an argument
// rather than stored, since one instance serves every concurrent 404.
type notFoundScreen struct {
	component.ContextOnly
	site *Site
}

func (s *notFoundScreen) Render() render.HTML { return s.RenderNotFound("") }

func (s *notFoundScreen) RenderNotFound(path string) render.HTML {
	desc := "That page does not exist. It may have been a draft, or the slug may have changed."
	if path != "" {
		desc = "Nothing is published at " + path + "."
	}
	recent := s.site.Posts
	if len(recent) > 3 {
		recent = recent[:3]
	}
	return ui.Stack(ui.StackConfig{Gap: ui.GapXL},
		ui.EmptyState(ui.EmptyStateConfig{
			Title:        "404 — not found",
			Description:  desc,
			HeadingLevel: 1,
			Action:       ui.LinkButton(ui.LinkButtonConfig{Label: "Back to posts", Href: "/"}),
		}),
		ui.Section(ui.SectionConfig{Heading: "Recent posts"}, postList(recent, "", "")),
	)
}

// ─── Registration ────────────────────────────────────────────────────

// registerScreens enumerates the corpus into routes. Every URL this site
// serves is registered here, which is what lets the sitemap be derived rather
// than maintained.
func registerScreens(site *Site, app *appui.App, layout *appui.Layout) {
	totalPages := (len(site.Posts) + postsPerPage - 1) / postsPerPage

	app.Register("/", &listScreen{site: site, page: 1, totalPages: totalPages}, layout)
	for page := 2; page <= totalPages; page++ {
		app.Register("/page/"+strconv.Itoa(page),
			&listScreen{site: site, page: page, totalPages: totalPages}, layout)
	}

	for _, p := range site.Posts {
		app.Register("/posts/"+p.Slug, &postScreen{site: site, post: p}, layout)
	}

	app.Register("/tags", &tagIndexScreen{site: site}, layout)
	for _, t := range site.Tags {
		posts, _, _ := site.PostsByTag(t.Slug)
		app.Register("/tags/"+t.Slug, &tagScreen{tag: t, posts: posts}, layout)
	}

	app.Register("/archive", &archiveScreen{site: site}, layout)
	app.Register("/search", &searchScreen{site: site}, layout)

	for _, p := range site.Pages {
		app.Register("/"+p.Slug, &pageScreen{page: p}, layout)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
