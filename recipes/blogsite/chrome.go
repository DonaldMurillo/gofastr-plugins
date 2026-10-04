package main

// The shared shell: header, footer, and the small helpers the screens use to
// render a post card or a tag chip the same way everywhere.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/recipes/blogsite/sitefooter"
	"github.com/DonaldMurillo/gofastr-plugins/recipes/blogsite/siteheader"
	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"
)

// siteName is the blog's title, used in the header, the feeds, and the
// document <title> suffix.
const siteName = "Notes on a flat file"

// tagline is the one-line description under the brand and in the feed.
const tagline = "A blog that is a directory of markdown files."

// newLayout builds the shell every screen renders inside: the site's own
// header and footer packages around the routed content, on one page-tall
// stack that pins the footer to the bottom. The nav is data-driven: content
// pages that declare a `menu:` key appear in it, so adding
// content/pages/uses.md with `menu: Uses` puts it in the header without
// touching this file.
func newLayout(site *Site) *appui.Layout {
	return appui.NewLayout("site", appui.LayoutSpec{}, func(ctx context.Context, l *appui.LayoutTree) render.HTML {
		return ui.Stack(ui.StackConfig{Screen: true, Gap: ui.GapNone},
			siteheader.Render(siteheader.Config{
				Ctx:  ctx,
				Name: siteName,
				// Search is a nav link rather than a box in the Actions
				// slot. The siteheader renders its links TWICE — once in
				// the desktop bar, once in the phone menu — which is
				// harmless for links (a repeated href duplicates nothing
				// that has to be unique) but wrong for a form control,
				// whose fixed id would land in the DOM twice.
				Links:   navLinks(site),
				Actions: ui.ThemeToggle(ui.ThemeToggleConfig{}),
			}),
			ui.Container(ui.ContainerConfig{Width: ui.ContainerPage, Pad: ui.ContainerPadPage}, l.Primary()),
			siteFooter(site),
		)
	})
}

// navLinks is the header's nav: the fixed sections plus every content page
// that declares a `menu:` key.
func navLinks(site *Site) []siteheader.Link {
	nav := []siteheader.Link{
		{Label: "Posts", Href: "/"},
		{Label: "Tags", Href: "/tags", Section: true},
		{Label: "Archive", Href: "/archive"},
		{Label: "Search", Href: "/search"},
	}
	for _, p := range site.Pages {
		if p.Menu != "" {
			nav = append(nav, siteheader.Link{Label: p.Menu, Href: "/" + p.Slug})
		}
	}
	return nav
}

// siteFooter is the colophon: the reading links, the five busiest tags, the
// feeds, and where the code lives, over a quiet count of the corpus.
func siteFooter(site *Site) render.HTML {
	// The tag column is the five busiest tags. Site.Tags is already
	// sorted by count, so this is a slice, not a sort.
	tagLinks := make([]sitefooter.Link, 0, 5)
	for _, t := range site.Tags {
		if len(tagLinks) == 5 {
			break
		}
		tagLinks = append(tagLinks, sitefooter.Link{
			Label: fmt.Sprintf("%s (%d)", t.Tag, t.Count),
			Href:  "/tags/" + t.Slug,
		})
	}

	return sitefooter.Render(sitefooter.Config{
		Name:    siteName,
		Tagline: tagline,
		Columns: []sitefooter.Column{
			{Title: "Read", Links: []sitefooter.Link{
				{Label: "All posts", Href: "/"},
				{Label: "Archive", Href: "/archive"},
				{Label: "Tags", Href: "/tags"},
			}},
			{Title: "Tags", Links: tagLinks},
			{Title: "Subscribe", Links: []sitefooter.Link{
				{Label: "RSS", Href: "/feed.xml"},
				{Label: "JSON Feed", Href: "/feed.json"},
				{Label: "Sitemap", Href: "/sitemap.xml"},
			}},
			{Title: "Project", Links: []sitefooter.Link{
				{Label: "Source on GitHub", Href: recipeSourceURL},
			}},
		},
		Note: fmt.Sprintf("%d posts, %d tags.", len(site.Posts), len(site.Tags)),
	})
}

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
