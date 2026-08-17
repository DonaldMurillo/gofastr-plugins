package main

// Shared shell and the pieces both the public pages and the admin reuse.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core-ui/component"
	"github.com/DonaldMurillo/gofastr/core-ui/html"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"
)

const (
	siteName = "Written in the browser"
	tagline  = "A blog whose posts live in SQLite and are written with the rich text editor."

	// recipeSourceURL is where this recipe's code lives.
	recipeSourceURL = "https://github.com/DonaldMurillo/gofastr-plugins/tree/main/recipes/blogapp"

	// postsPerPage is the public listing window.
	postsPerPage = 4
)

// staticHTML adapts pre-rendered markup to component.Component for the layout
// header/footer slots.
type staticHTML render.HTML

func (s staticHTML) Render() render.HTML { return render.HTML(s) }

var _ component.Component = staticHTML("")

// ctxHTML renders per-request markup in a layout slot. The header needs it: the
// "Admin" link only appears for a signed-in admin, and that is a per-request
// fact the shared header instance cannot bake in.
type ctxHTML func(ctx context.Context) render.HTML

func (f ctxHTML) Render() render.HTML                       { return f(context.Background()) }
func (f ctxHTML) RenderCtx(ctx context.Context) render.HTML { return f(ctx) }

var _ component.ContextComponent = ctxHTML(nil)

func (a *app) newLayout() *appui.Layout {
	header := ctxHTML(func(ctx context.Context) render.HTML {
		actions := []render.HTML{ui.ThemeToggle(ui.ThemeToggleConfig{})}
		if isAdmin(ctx) {
			actions = append(actions,
				ui.LinkButton(ui.LinkButtonConfig{Label: "Admin", Href: "/admin", Variant: ui.ButtonSecondary, Size: ui.ButtonSizeSmall}))
		}
		return ui.SiteHeader(ui.SiteHeaderConfig{
			Brand:        html.Link(html.LinkConfig{Href: "/", Text: siteName}),
			MobileBrand:  html.Link(html.LinkConfig{Href: "/", Text: "Written"}),
			NavUnderline: true,
			NavItems: []ui.SiteHeaderLink{
				{Label: "Posts", Href: "/"},
				{Label: "Tags", Href: "/tags", MatchPrefix: true},
				{Label: "Archive", Href: "/archive"},
				// A nav link, not a search box in Actions: SiteHeader renders
				// Actions twice (desktop bar + mobile drawer), so a form control
				// with a fixed id there appears twice in the DOM — a duplicate-id
				// a11y violation. Repeating a link href is harmless.
				{Label: "Search", Href: "/search"},
			},
			Actions: ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM, Align: ui.AlignCenter}, actions...),
			MobileExtraLinks: []ui.SiteHeaderLink{
				{Label: "RSS", Href: "/feed.xml"},
				{Label: "Source ↗", Href: recipeSourceURL, External: true},
			},
		})
	})

	footer := staticHTML(ui.SiteFooter(ui.SiteFooterConfig{
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
			{Title: "Subscribe", Links: []ui.SiteFooterLink{
				{Label: "RSS", Href: "/feed.xml"},
				{Label: "Sitemap", Href: "/sitemap.xml"},
			}},
			{Title: "Write", Links: []ui.SiteFooterLink{
				{Label: "Admin", Href: "/admin"},
				{Label: "Source on GitHub", Href: recipeSourceURL},
			}},
		},
		Bottom: []render.HTML{
			ui.Muted(render.Text("Posts are stored as ProseMirror JSON and rendered server-side; readers load no editor.")),
		},
	}))

	return appui.NewLayout("site").
		WithHeader(header).
		WithFooter(footer).
		WithContainer()
}

// ─── Shared rendering ────────────────────────────────────────────────

func postCard(p *Post) render.HTML {
	return ui.Card(ui.CardConfig{
		Heading:     p.Title,
		Description: p.Summary,
		Href:        "/posts/" + p.Slug,
		Footer:      ui.Muted(render.Text(postMeta(p))),
	})
}

func postMeta(p *Post) string {
	parts := []string{p.Date().Format("2 January 2006"), strconv.Itoa(p.ReadingMinutes()) + " min read"}
	return strings.Join(parts, " · ")
}

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

func statusBadge(p *Post) render.HTML {
	if p.Published() {
		return ui.StatusBadge(ui.StatusBadgeConfig{Label: "Published", Variant: ui.StatusSuccess})
	}
	return ui.StatusBadge(ui.StatusBadgeConfig{Label: "Draft", Variant: ui.StatusNeutral})
}

// ─── Small helpers ───────────────────────────────────────────────────

// TagSlug normalizes a tag (or a title) into a URL segment.
func TagSlug(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// inline is ui.CalloutConfig.Landmark set to false: the callout is emphasis
// inside the main flow, not a tangential region. A nested <aside> there trips
// axe's landmark-complementary-is-top-level rule.
var inline = func() *bool { b := false; return &b }()

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// pager renders the older/newer controls for the public listing.
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

// ─── Not-found plumbing ──────────────────────────────────────────────
//
// Unlike recipes/blogsite, this app's corpus lives in a database and changes at
// runtime, so its public post and tag routes have to be dynamic (":slug"). That
// creates a problem the static recipe never has: a registered route matches an
// unknown slug, so the host has no reason to 404, and a screen that renders its
// own "not found" body would answer HTTP 200 — a soft 404, which is the thing
// crawlers penalize and monitoring never notices.
//
// The fix is a middleware that resolves the slug BEFORE the host routes: when
// nothing matches it rewrites the path to the registered /404 screen and forces
// the status code to 404. The reader gets the full themed page, the crawler
// gets the truth.

const notFoundPath = "/404"

// forcedStatus overrides whatever status the wrapped handler writes. The /404
// screen renders through the normal page pipeline, which would otherwise answer
// 200 like any other screen.
type forcedStatus struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (f *forcedStatus) WriteHeader(int) {
	if f.wrote {
		return
	}
	f.wrote = true
	f.ResponseWriter.WriteHeader(f.status)
}

func (f *forcedStatus) Write(b []byte) (int, error) {
	if !f.wrote {
		f.WriteHeader(f.status)
	}
	return f.ResponseWriter.Write(b)
}

// resolveOr404 rewrites requests for posts and tags that do not exist.
func (a *app) resolveOr404(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.publicPathExists(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		rewritten := r.Clone(r.Context())
		rewritten.URL.Path = notFoundPath
		next.ServeHTTP(&forcedStatus{ResponseWriter: w, status: http.StatusNotFound}, rewritten)
	})
}

// publicPathExists reports whether a dynamic public path resolves to something.
// Paths this middleware does not own return true so they route normally.
func (a *app) publicPathExists(path string) bool {
	switch {
	case strings.HasPrefix(path, "/posts/"):
		slug := strings.TrimPrefix(path, "/posts/")
		if slug == "" || strings.Contains(slug, "/") {
			return false
		}
		p, err := a.store.BySlug(slug)
		// Drafts are unreachable in public, exactly like an unknown slug — the
		// admin previews them from inside /admin.
		return err == nil && p.Published()

	case strings.HasPrefix(path, "/tags/"):
		tag := strings.TrimPrefix(path, "/tags/")
		if tag == "" || strings.Contains(tag, "/") {
			return false
		}
		posts, err := a.store.PublishedByTag(tag)
		return err == nil && len(posts) > 0

	case strings.HasPrefix(path, "/page/"):
		n, err := strconv.Atoi(strings.TrimPrefix(path, "/page/"))
		if err != nil || n < 2 {
			return false
		}
		posts, err := a.store.Published()
		if err != nil {
			return false
		}
		return n <= (len(posts)+postsPerPage-1)/postsPerPage

	default:
		return true
	}
}
