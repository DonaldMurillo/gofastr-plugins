package main

// The reading side. Nothing here loads the editor.
//
// A post is stored as ProseMirror JSON — the editor's canonical document — and
// rendered to HTML on the server by richtext/ssr. That package is pure Go with
// no JavaScript at all, so a reader gets a plain document with working links,
// tables, and code blocks whether or not scripts run. The plugin's ~600 KB
// bundle is loaded on exactly one route, and it is behind the admin login.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core-ui/component"
	"github.com/DonaldMurillo/gofastr/core-ui/html"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"

	"github.com/DonaldMurillo/gofastr-plugins/richtext/ssr"
)

// renderBody turns a stored document into read-view HTML. An unreadable
// document is reported in place rather than crashing the page: the post's
// title, date, and tags are still worth serving, and a blank page would hide
// the fact that one row is corrupt.
func renderBody(p *Post) render.HTML {
	if strings.TrimSpace(p.BodyJSON) == "" {
		return ui.Muted(render.Text("This post has no content yet."))
	}
	body, err := ssr.RenderJSON(p.BodyJSON)
	if err != nil {
		return ui.Callout(ui.CalloutConfig{Title: "This post could not be rendered", Variant: ui.StatusDanger},
			render.Text("The stored document is not valid ProseMirror JSON."))
	}
	return body
}

// ─── Listing ─────────────────────────────────────────────────────────

// listScreen backs "/" and "/page/:n". It reads its data per request rather
// than at boot, because the admin can publish a post at any moment.
type listScreen struct {
	component.ContextOnly
	app  *app
	page int // 0 means "read it from the :n param"
}

func (s *listScreen) SetParams(params map[string]string) {
	if n, err := strconv.Atoi(params["n"]); err == nil {
		s.page = n
	}
}

func (s *listScreen) ScreenTitle() string {
	if s.page <= 1 {
		return ""
	}
	return fmt.Sprintf("Posts, page %d", s.page)
}

func (s *listScreen) ScreenDescription() string { return tagline }

func (s *listScreen) RenderCtx(ctx context.Context) render.HTML {
	posts, err := s.app.store.Published()
	if err != nil {
		return errorPanel(err)
	}
	page := s.page
	if page < 1 {
		page = 1
	}
	totalPages := (len(posts) + postsPerPage - 1) / postsPerPage
	if totalPages < 1 {
		totalPages = 1
	}

	start := (page - 1) * postsPerPage
	if start > len(posts) {
		start = len(posts)
	}
	end := start + postsPerPage
	if end > len(posts) {
		end = len(posts)
	}

	parts := []render.HTML{}
	if page == 1 {
		actions := []render.HTML{
			ui.LinkButton(ui.LinkButtonConfig{Label: "Source on GitHub", Href: recipeSourceURL, Variant: ui.ButtonSecondary, External: true}),
		}
		if len(posts) > 0 {
			actions = append([]render.HTML{
				ui.LinkButton(ui.LinkButtonConfig{Label: "Read the latest", Href: "/posts/" + posts[0].Slug}),
			}, actions...)
		}
		parts = append(parts, ui.Hero(ui.HeroConfig{
			Eyebrow:  "Recipe · blogapp",
			Title:    siteName,
			Subtitle: tagline + " Readers get server-rendered HTML and no JavaScript; the editor lives behind the admin login.",
			Actions:  actions,
		}))
	} else {
		// No eyebrow repeating "Page N of M" — the pager below already says it.
		parts = append(parts, ui.PageHeader(ui.PageHeaderConfig{
			Title: fmt.Sprintf("Posts, page %d", page),
		}))
	}

	parts = append(parts, postList(posts[start:end],
		"No posts published yet",
		"Sign in to the admin and write one."))
	if totalPages > 1 {
		parts = append(parts, pager(page, totalPages))
	}
	return ui.Stack(ui.StackConfig{Gap: ui.GapXL}, parts...)
}

// ─── Post ────────────────────────────────────────────────────────────

type postScreen struct {
	component.ContextOnly
	app  *app
	slug string
}

func (s *postScreen) SetParams(params map[string]string) { s.slug = params["slug"] }

// ScreenTitle is re-read per request after params are injected, which is what
// gives each post its own <title> and Open Graph title.
func (s *postScreen) ScreenTitle() string {
	if p, err := s.app.store.BySlug(s.slug); err == nil {
		return p.Title
	}
	return ""
}

func (s *postScreen) ScreenDescription() string {
	if p, err := s.app.store.BySlug(s.slug); err == nil {
		return p.Summary
	}
	return ""
}

func (s *postScreen) RenderCtx(ctx context.Context) render.HTML {
	p, err := s.app.store.BySlug(s.slug)
	// resolveOr404 already rewrote unknown and unpublished slugs to /404, so
	// reaching this branch means the post vanished between the middleware and
	// the render. Rendering the not-found body keeps it from being a panic.
	if err != nil || !p.Published() {
		return notFoundBody(s.app)
	}

	body := []render.HTML{
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    p.Title,
			Subtitle: p.Summary,
			Eyebrow:  postMeta(p),
		}),
		tagChips(p.Tags),
		renderBody(p),
	}
	// An admin reading the public page gets a way back to the editor. A
	// reader sees nothing extra.
	if isAdmin(ctx) {
		body = append(body,
			ui.Divider(ui.DividerConfig{}),
			ui.LinkButton(ui.LinkButtonConfig{Label: "Edit this post", Href: "/admin/posts/" + p.ID, Variant: ui.ButtonSecondary}))
	}

	return ui.DocLayout(ui.DocLayoutConfig{
		Crumbs: []ui.DocCrumb{{Label: "Posts", Href: "/"}, {Label: p.Title}},
	}, ui.Stack(ui.StackConfig{Gap: ui.GapLG}, body...))
}

// ─── Tags ────────────────────────────────────────────────────────────

type tagIndexScreen struct {
	component.ContextOnly
	app *app
}

func (s *tagIndexScreen) ScreenTitle() string       { return "Tags" }
func (s *tagIndexScreen) ScreenDescription() string { return "Every tag in use." }

func (s *tagIndexScreen) RenderCtx(context.Context) render.HTML {
	tags, err := s.app.store.Tags()
	if err != nil {
		return errorPanel(err)
	}
	if len(tags) == 0 {
		return ui.EmptyState(ui.EmptyStateConfig{
			Title:        "No tags yet",
			Description:  "Tags come from the comma-separated field on each post.",
			HeadingLevel: 1,
			Action:       ui.LinkButton(ui.LinkButtonConfig{Label: "All posts", Href: "/"}),
		})
	}
	chips := make([]render.HTML, 0, len(tags))
	for _, t := range tags {
		chips = append(chips, ui.Tag(ui.TagConfig{
			Label: fmt.Sprintf("%s (%d)", t.Tag, t.Count),
			Href:  "/tags/" + t.Slug,
		}))
	}
	return ui.Stack(ui.StackConfig{Gap: ui.GapLG},
		ui.PageHeader(ui.PageHeaderConfig{Title: "Tags", Subtitle: "Busiest first."}),
		ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM}, chips...),
	)
}

type tagScreen struct {
	component.ContextOnly
	app *app
	tag string
}

func (s *tagScreen) SetParams(params map[string]string) { s.tag = params["tag"] }
func (s *tagScreen) ScreenTitle() string                { return "Tagged " + s.tag }

func (s *tagScreen) RenderCtx(context.Context) render.HTML {
	posts, err := s.app.store.PublishedByTag(s.tag)
	if err != nil {
		return errorPanel(err)
	}
	label := s.tag
	if len(posts) > 0 {
		for _, t := range posts[0].Tags {
			if TagSlug(t) == s.tag {
				label = t
			}
		}
	}
	return ui.Stack(ui.StackConfig{Gap: ui.GapLG},
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    label,
			Eyebrow:  "Tag",
			Subtitle: fmt.Sprintf("%d %s.", len(posts), plural(len(posts), "post", "posts")),
			Actions:  ui.LinkButton(ui.LinkButtonConfig{Label: "All tags", Href: "/tags", Variant: ui.ButtonSecondary}),
		}),
		postList(posts, "No posts", "Nothing carries this tag."),
	)
}

// ─── Archive ─────────────────────────────────────────────────────────

type archiveScreen struct {
	component.ContextOnly
	app *app
}

func (s *archiveScreen) ScreenTitle() string { return "Archive" }

func (s *archiveScreen) RenderCtx(context.Context) render.HTML {
	posts, err := s.app.store.Published()
	if err != nil {
		return errorPanel(err)
	}
	sections := []render.HTML{
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    "Archive",
			Subtitle: fmt.Sprintf("All %d published %s, newest first.", len(posts), plural(len(posts), "post", "posts")),
		}),
	}
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
	for _, p := range posts {
		if p.Date().Year() != year {
			flush()
			year = p.Date().Year()
		}
		rows = append(rows, html.ListItem(html.ListItemConfig{},
			ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM, Align: ui.AlignBaseline},
				ui.Muted(render.Text(p.Date().Format("2 Jan"))),
				html.Link(html.LinkConfig{Href: "/posts/" + p.Slug, Text: p.Title}),
			)))
	}
	flush()
	return ui.Stack(ui.StackConfig{Gap: ui.GapLG}, sections...)
}

// ─── Search ──────────────────────────────────────────────────────────

type searchScreen struct {
	component.ContextOnly
	app *app
}

func (s *searchScreen) ScreenTitle() string { return "Search" }

func (s *searchScreen) RenderCtx(ctx context.Context) render.HTML {
	query := appui.QueryFromContext(ctx).Get("q")
	posts, err := s.app.store.Search(query)
	if err != nil {
		return errorPanel(err)
	}

	parts := []render.HTML{
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    "Search",
			Subtitle: "Matches titles, summaries, and the markdown export of each post's body.",
		}),
		ui.SearchInput(ui.SearchInputConfig{
			Name: "q", ID: "search-page-q", Placeholder: "Search posts",
			Action: "/search", ExtraAttrs: map[string]string{"value": query},
		}),
	}
	switch {
	case query == "":
		parts = append(parts, ui.Muted(render.Text("Type a query to search.")))
	case len(posts) == 0:
		parts = append(parts, ui.EmptyState(ui.EmptyStateConfig{
			Title:        "No matches for “" + query + "”",
			Description:  "Search is a substring match — it does not stem words, so try a shorter query.",
			HeadingLevel: 2,
			Action:       ui.LinkButton(ui.LinkButtonConfig{Label: "Browse all posts", Href: "/", Variant: ui.ButtonSecondary}),
		}))
	default:
		parts = append(parts,
			ui.Muted(render.Text(fmt.Sprintf("%d %s for “%s”.",
				len(posts), plural(len(posts), "match", "matches"), query))),
			postList(posts, "", ""))
	}
	return ui.Stack(ui.StackConfig{Gap: ui.GapLG}, parts...)
}

// ─── 404 ─────────────────────────────────────────────────────────────

// notFoundScreen is registered at /404 AND handed to uihost.WithNotFoundScreen,
// so both routes into a miss — an unknown slug rewritten by resolveOr404, and a
// path no route matches at all — render the same page.
type notFoundScreen struct {
	component.ContextOnly
	app *app
}

func (s *notFoundScreen) ScreenTitle() string { return "Not found" }

func (s *notFoundScreen) RenderCtx(context.Context) render.HTML { return notFoundBody(s.app) }

// RenderNotFound satisfies uihost.NotFoundRenderer. The path is ignored on
// purpose: resolveOr404 rewrites the URL before the host sees it, so echoing
// what the host was asked for would print "/404" back at a reader who typed
// something else.
func (s *notFoundScreen) RenderNotFound(string) render.HTML { return notFoundBody(s.app) }

func notFoundBody(a *app) render.HTML {
	recent, err := a.store.Published()
	if err != nil {
		return errorPanel(err)
	}
	if len(recent) > 3 {
		recent = recent[:3]
	}
	parts := []render.HTML{
		ui.EmptyState(ui.EmptyStateConfig{
			Title:        "404 — not found",
			Description:  "No published post lives at that address. It may be a draft, or the slug may have changed.",
			HeadingLevel: 1,
			Action:       ui.LinkButton(ui.LinkButtonConfig{Label: "Back to posts", Href: "/"}),
		}),
	}
	if len(recent) > 0 {
		parts = append(parts, ui.Section(ui.SectionConfig{Heading: "Recent posts"}, postList(recent, "", "")))
	}
	return ui.Stack(ui.StackConfig{Gap: ui.GapXL}, parts...)
}

// errorPanel renders a store failure in place. A blog is a read-mostly app and
// a query error is a bug, not a user state — surfacing it beats a blank region.
func errorPanel(err error) render.HTML {
	return ui.Callout(ui.CalloutConfig{Title: "Something went wrong", Variant: ui.StatusDanger},
		render.Text(err.Error()))
}

// registerPublicScreens wires the reading side.
func (a *app) registerPublicScreens(uiApp *appui.App, layout *appui.Layout) {
	uiApp.Register("/", &listScreen{app: a, page: 1}, layout)
	uiApp.Register("/page/:n:int", &listScreen{app: a}, layout)
	uiApp.Register("/posts/:slug", &postScreen{app: a}, layout)
	uiApp.Register("/tags", &tagIndexScreen{app: a}, layout)
	uiApp.Register("/tags/:tag", &tagScreen{app: a}, layout)
	uiApp.Register("/archive", &archiveScreen{app: a}, layout)
	uiApp.Register("/search", &searchScreen{app: a}, layout)
	uiApp.Register(notFoundPath, &notFoundScreen{app: a}, layout)
}
