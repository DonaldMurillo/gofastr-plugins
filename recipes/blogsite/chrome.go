package main

// The shared shell: header, footer, and the small helpers the screens use to
// render a post card or a tag chip the same way everywhere.

import (
	"fmt"
	"strconv"
	"strings"

	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core-ui/component"
	"github.com/DonaldMurillo/gofastr/core-ui/html"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"
)

// siteName is the blog's title, used in the header, the feeds, and the
// document <title> suffix.
const siteName = "Notes on a flat file"

// tagline is the one-line description under the brand and in the feed.
const tagline = "A blog that is a directory of markdown files."

// staticHTML adapts pre-rendered HTML to component.Component so it can be
// handed to Layout.WithHeader / WithFooter, which take components rather than
// markup. The chrome is identical on every page, so it is built once at boot.
type staticHTML render.HTML

func (s staticHTML) Render() render.HTML { return render.HTML(s) }

// newLayout builds the shell every screen renders inside. The nav is
// data-driven: content pages that declare a `menu:` key appear in it, so
// adding content/pages/uses.md with `menu: Uses` puts it in the header
// without touching this file.
func newLayout(site *Site) *appui.Layout {
	nav := []ui.SiteHeaderLink{
		{Label: "Posts", Href: "/"},
		{Label: "Tags", Href: "/tags", MatchPrefix: true},
		{Label: "Archive", Href: "/archive"},
		// Search is a nav link rather than a box in the Actions slot. SiteHeader
		// renders Actions TWICE — once in the desktop bar, once in the mobile
		// drawer — so a form control with a fixed id there lands in the DOM
		// twice, which is a duplicate-id a11y violation and breaks the
		// label/control association for whichever copy loses. Nav items are
		// links, and a repeated href is harmless.
		{Label: "Search", Href: "/search"},
	}
	for _, p := range site.Pages {
		if p.Menu != "" {
			nav = append(nav, ui.SiteHeaderLink{Label: p.Menu, Href: "/" + p.Slug})
		}
	}

	header := ui.SiteHeader(ui.SiteHeaderConfig{
		Brand: html.Link(html.LinkConfig{Href: "/", Text: siteName}),
		// The full name wraps awkwardly under 720px; the header swaps in
		// the short mark rather than letting the identity push the nav off
		// the screen.
		MobileBrand:  html.Link(html.LinkConfig{Href: "/", Text: "Notes"}),
		NavItems:     nav,
		NavUnderline: true,
		Actions:      ui.ThemeToggle(ui.ThemeToggleConfig{}),
		MobileExtraLinks: []ui.SiteHeaderLink{
			{Label: "RSS", Href: "/feed.xml"},
			{Label: "Source ↗", Href: recipeSourceURL, External: true},
		},
	})

	// The footer's tag column is the five busiest tags. Site.Tags is already
	// sorted by count, so this is a slice, not a sort.
	tagLinks := make([]ui.SiteFooterLink, 0, 5)
	for _, t := range site.Tags {
		if len(tagLinks) == 5 {
			break
		}
		tagLinks = append(tagLinks, ui.SiteFooterLink{
			Label: fmt.Sprintf("%s (%d)", t.Tag, t.Count),
			Href:  "/tags/" + t.Slug,
		})
	}

	footer := ui.SiteFooter(ui.SiteFooterConfig{
		Lead: ui.Stack(ui.StackConfig{Gap: ui.GapXS},
			html.Strong(html.TextConfig{}, render.Text(siteName)),
			ui.Muted(render.Text(tagline)),
		),
		Columns: []ui.SiteFooterColumn{
			{Title: "Read", Links: []ui.SiteFooterLink{
				{Label: "All posts", Href: "/"},
				{Label: "Archive", Href: "/archive"},
				{Label: "Tags", Href: "/tags"},
			}},
			{Title: "Tags", Links: tagLinks},
			{Title: "Subscribe", Links: []ui.SiteFooterLink{
				{Label: "RSS", Href: "/feed.xml"},
				{Label: "JSON Feed", Href: "/feed.json"},
				{Label: "Sitemap", Href: "/sitemap.xml"},
			}},
		},
		Bottom: []render.HTML{
			ui.Muted(render.Text(fmt.Sprintf("%d posts, %d tags.", len(site.Posts), len(site.Tags)))),
			html.Link(html.LinkConfig{Href: recipeSourceURL, Text: "Source on GitHub"}),
		},
	})

	return appui.NewLayout("site").
		WithHeader(staticHTML(header)).
		WithFooter(staticHTML(footer)).
		WithContainer()
}

var _ component.Component = staticHTML("")

// ─── Shared pieces ───────────────────────────────────────────────────

// postCard is one post in a listing. The whole surface is a link (CardConfig
// .Href), so the click target is the card rather than the title alone.
func postCard(p *Post) render.HTML {
	return ui.Card(ui.CardConfig{
		Heading:     p.Title,
		Description: p.Summary,
		Href:        "/posts/" + p.Slug,
		Footer:      ui.Muted(render.Text(postMeta(p))),
	})
}

// postMeta is the byline strip: date, reading time, and author when set.
func postMeta(p *Post) string {
	parts := []string{
		p.Date.Format("2 January 2006"),
		strconv.Itoa(p.ReadingMinutes()) + " min read",
	}
	if p.Author != "" {
		parts = append(parts, p.Author)
	}
	return strings.Join(parts, " · ")
}

// tagChips renders a post's tags as links to their archive pages.
func tagChips(tags []string) render.HTML {
	if len(tags) == 0 {
		return ""
	}
	chips := make([]render.HTML, 0, len(tags))
	for _, t := range tags {
		chips = append(chips, ui.Tag(ui.TagConfig{Label: t, Href: "/tags/" + TagSlug(t)}))
	}
	return ui.Cluster(ui.ClusterConfig{Gap: ui.GapXS}, chips...)
}

// postList renders a run of post cards, or an empty state when there are
// none. Every listing screen goes through here so "no results" looks the same
// on a tag page, a search page, and an empty archive.
func postList(posts []*Post, emptyTitle, emptyDescription string) render.HTML {
	if len(posts) == 0 {
		return ui.EmptyState(ui.EmptyStateConfig{
			Title:        emptyTitle,
			Description:  emptyDescription,
			HeadingLevel: 2,
			Action:       ui.LinkButton(ui.LinkButtonConfig{Label: "All posts", Href: "/", Variant: ui.ButtonSecondary}),
		})
	}
	cards := make([]render.HTML, 0, len(posts))
	for _, p := range posts {
		cards = append(cards, postCard(p))
	}
	return ui.Grid(ui.GridConfig{Min: "20rem", Gap: ui.GapLG}, cards...)
}
