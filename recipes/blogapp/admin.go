package main

// The writing side: login, the post list, and the edit screen that mounts the
// rich text editor.
//
// Two things persist a post body, on purpose:
//
//   - The editor autosaves over the plugin bridge, keyed by DocID (the post id).
//     That is what makes a long writing session safe; nothing is lost if the tab
//     closes. It reaches Store.UpdateBody through the save handler in main.go.
//   - The form POST below writes title, slug, summary, tags, and status, and
//     carries the body along in the two hidden inputs richtext.Mount emits.
//
// The form is the source of truth for metadata; the bridge is the source of
// truth for the body between submits. They meet in Store.Update (metadata) and
// Store.UpdateBody (body), which write disjoint columns so the two paths cannot
// clobber each other.

import (
	"context"
	"net/http"
	"strings"

	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core-ui/component"
	"github.com/DonaldMurillo/gofastr/core-ui/html"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"

	"github.com/DonaldMurillo/gofastr-plugins/richtext"
)

// ─── Login ───────────────────────────────────────────────────────────

type loginScreen struct {
	component.ContextOnly
	app *app
}

func (s *loginScreen) ScreenTitle() string { return "Sign in" }

func (s *loginScreen) RenderCtx(ctx context.Context) render.HTML {
	q := appui.QueryFromContext(ctx)
	if isAdmin(ctx) {
		return ui.Stack(ui.StackConfig{Gap: ui.GapLG},
			ui.PageHeader(ui.PageHeaderConfig{Title: "Already signed in"}),
			ui.LinkButton(ui.LinkButtonConfig{Label: "Go to the admin", Href: "/admin"}),
		)
	}

	parts := []render.HTML{}
	if q.Get("error") != "" {
		parts = append(parts, ui.Callout(ui.CalloutConfig{Title: "Wrong password", Variant: ui.StatusDanger},
			render.Text("Try again.")))
	}

	// The form posts to a DIFFERENT path than the screen it renders on. An
	// explicit POST route on /admin/login would shadow the host-served GET
	// screen and turn it into a 405.
	form := ui.Form(ui.FormConfig{Action: "/admin/login/submit", Method: "POST", HideSubmit: true},
		html.Input(html.InputConfig{Type: "hidden", Name: "next", Value: q.Get("next")}),
		ui.FormField(ui.FormFieldConfig{
			Label: "Password", For: "admin-password", Required: true,
			Input: ui.PasswordInput(ui.PasswordInputConfig{
				Name: "password", ID: "admin-password", Required: true, Autocomplete: "current-password",
			}),
		}),
		ui.Button(ui.ButtonConfig{Label: "Sign in", Type: "submit", ID: "sign-in"}),
	)

	parts = append(parts,
		ui.PageHeader(ui.PageHeaderConfig{
			Title:    "Sign in",
			Subtitle: "The admin is where posts are written. Readers never need it.",
		}),
		form,
		ui.Callout(ui.CalloutConfig{Title: "This is a demo credential", Variant: ui.StatusInfo, Landmark: inline},
			render.Text("The password is “demo” unless BLOG_ADMIN_PASSWORD says otherwise. "+
				"A real app replaces session.go with battery/auth — accounts, reset, 2FA, the lot.")),
	)
	return ui.Stack(ui.StackConfig{Gap: ui.GapLG}, parts...)
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !checkPassword(r.PostFormValue("password")) {
		http.Redirect(w, r, "/admin/login?error=1", http.StatusSeeOther)
		return
	}
	setSessionCookie(w, r, a.sessions.create())

	// Only follow `next` when it is a local absolute path. Without that check
	// the login form is an open redirect: ?next=https://evil.example bounces a
	// freshly authenticated admin off-site.
	next := r.PostFormValue("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/admin"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *app) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.destroy(c.Value)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ─── Post list ───────────────────────────────────────────────────────

type adminListScreen struct {
	component.ContextOnly
	app *app
}

func (s *adminListScreen) ScreenTitle() string { return "Admin" }

func (s *adminListScreen) RenderCtx(context.Context) render.HTML {
	posts, err := s.app.store.All()
	if err != nil {
		return errorPanel(err)
	}

	header := ui.PageHeader(ui.PageHeaderConfig{
		Title:    "Posts",
		Subtitle: "Drafts are visible here and nowhere else.",
		Actions: ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM},
			// A POST, not a link: creating a post is a write, and a GET that
			// writes is one crawler away from a table full of empty drafts.
			ui.Form(ui.FormConfig{Action: "/admin/posts/new", Method: "POST", HideSubmit: true},
				ui.Button(ui.ButtonConfig{Label: "New post", Type: "submit"})),
			ui.LinkButton(ui.LinkButtonConfig{Label: "Sign out", Href: "/admin/logout", Variant: ui.ButtonGhost}),
		),
	})

	if len(posts) == 0 {
		return ui.Stack(ui.StackConfig{Gap: ui.GapLG}, header,
			ui.EmptyState(ui.EmptyStateConfig{
				Title:        "No posts yet",
				Description:  "Create one and the editor opens on it.",
				HeadingLevel: 2,
			}))
	}

	rows := make([]render.HTML, 0, len(posts))
	for _, p := range posts {
		title := p.Title
		if strings.TrimSpace(title) == "" {
			title = "(untitled)"
		}
		publishLabel, publishTo := "Publish", StatusPublished
		if p.Published() {
			publishLabel, publishTo = "Unpublish", StatusDraft
		}
		rows = append(rows, ui.Card(ui.CardConfig{
			Header: ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM, Align: ui.AlignCenter, Justify: ui.JustifyBetween},
				html.Link(html.LinkConfig{Href: "/admin/posts/" + p.ID, Text: title}),
				statusBadge(p),
			),
			Footer: ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM, Align: ui.AlignCenter},
				ui.Muted(render.Text("Updated "+p.UpdatedAt.Format("2 Jan 2006 15:04"))),
				ui.LinkButton(ui.LinkButtonConfig{Label: "Edit", Href: "/admin/posts/" + p.ID, Variant: ui.ButtonSecondary, Size: ui.ButtonSizeSmall}),
				ui.Form(ui.FormConfig{Action: "/admin/posts/" + p.ID + "/status", Method: "POST", HideSubmit: true},
					html.Input(html.InputConfig{Type: "hidden", Name: "status", Value: publishTo}),
					ui.Button(ui.ButtonConfig{Label: publishLabel, Type: "submit", Variant: ui.ButtonSecondary, Size: ui.ButtonSizeSmall})),
				ui.Form(ui.FormConfig{Action: "/admin/posts/" + p.ID + "/delete", Method: "POST", HideSubmit: true},
					ui.Button(ui.ButtonConfig{Label: "Delete", Type: "submit", Variant: ui.ButtonGhost, Size: ui.ButtonSizeSmall})),
			),
		}, ui.Muted(render.Text(p.Summary))))
	}

	return ui.Stack(ui.StackConfig{Gap: ui.GapLG}, header,
		ui.Stack(ui.StackConfig{Gap: ui.GapMD}, rows...))
}

// ─── Edit ────────────────────────────────────────────────────────────

// editScreen is the only page in the app that loads the plugin.
type editScreen struct {
	component.ContextOnly
	app *app
	id  string
}

func (s *editScreen) SetParams(params map[string]string) { s.id = params["id"] }

func (s *editScreen) ScreenTitle() string {
	if p, err := s.app.store.ByID(s.id); err == nil && p.Title != "" {
		return "Editing " + p.Title
	}
	return "Edit post"
}

func (s *editScreen) RenderCtx(ctx context.Context) render.HTML {
	p, err := s.app.store.ByID(s.id)
	if err != nil {
		return ui.EmptyState(ui.EmptyStateConfig{
			Title:        "That post is gone",
			Description:  "It may have been deleted from another tab.",
			HeadingLevel: 1,
			Action:       ui.LinkButton(ui.LinkButtonConfig{Label: "Back to the admin", Href: "/admin"}),
		})
	}

	statusOptions := []ui.SelectOption{
		{Value: StatusDraft, Text: "Draft", Selected: !p.Published()},
		{Value: StatusPublished, Text: "Published", Selected: p.Published()},
	}

	// richtext.Mount emits the mount marker plus the two hidden inputs the host
	// broker keeps in sync with the document. Both live INSIDE the form, so a
	// plain submit carries the body even if the bridge never fired.
	editor := richtext.Mount(richtext.MountConfig{
		DocID:     p.ID,
		JSONField: "body_json",
		MDField:   "body_md",
		MinHeight: "420px",
		Doc:       p.BodyJSON,
	})

	form := ui.Form(ui.FormConfig{Action: "/admin/posts/" + p.ID, Method: "POST", HideSubmit: true},
		ui.TextField(ui.TextFieldConfig{Label: "Title", Name: "title", ID: "post-title", Value: p.Title, Required: true}),
		ui.TextField(ui.TextFieldConfig{Label: "Slug", Name: "slug", ID: "post-slug", Value: p.Slug,
			Help: slugHelp(p)}),
		ui.TextArea(ui.TextAreaConfig{Label: "Summary", Name: "summary", ID: "post-summary", Value: p.Summary, Rows: 2,
			Help: "Shown on cards and in the feed."}),
		ui.TextField(ui.TextFieldConfig{Label: "Tags", Name: "tags", ID: "post-tags", Value: joinTags(p.Tags),
			Help: "Comma separated."}),
		ui.Select(ui.SelectConfig{Label: "Status", Name: "status", ID: "post-status", Options: statusOptions}),
		editor,
		ui.Cluster(ui.ClusterConfig{Gap: ui.GapSM},
			ui.Button(ui.ButtonConfig{Label: "Save", Type: "submit", ID: "save-post"}),
			ui.LinkButton(ui.LinkButtonConfig{Label: "Back", Href: "/admin", Variant: ui.ButtonGhost}),
		),
	)

	head := []render.HTML{
		ui.PageHeader(ui.PageHeaderConfig{
			Title:   "Edit post",
			Eyebrow: "Admin",
			Actions: statusBadge(p),
		}),
	}
	if p.Published() {
		head = append(head, ui.LinkButton(ui.LinkButtonConfig{
			Label: "View published post", Href: "/posts/" + p.Slug, Variant: ui.ButtonSecondary}))
	}
	head = append(head,
		ui.Callout(ui.CalloutConfig{Title: "The editor autosaves", Variant: ui.StatusInfo, Landmark: inline},
			render.Text("Body changes save over the plugin bridge as you type. "+
				"Save writes the fields above — title, slug, summary, tags, status.")),
		form,
	)
	return ui.Stack(ui.StackConfig{Gap: ui.GapLG}, head...)
}

// stripSlugSuffix removes a trailing "-<digits>" — the disambiguator
// Store.uniqueSlug appends when a slug is already taken.
func stripSlugSuffix(slug string) string {
	i := strings.LastIndexByte(slug, '-')
	if i <= 0 || i == len(slug)-1 {
		return slug
	}
	for _, r := range slug[i+1:] {
		if r < '0' || r > '9' {
			return slug
		}
	}
	return slug[:i]
}

// slugHelp explains which of the two slug rules this post is currently under,
// so the field says what will happen rather than making the author find out by
// saving.
func slugHelp(p *Post) string {
	if p.Published() {
		return "Frozen: this post is published, so its URL stays put unless you change it here."
	}
	return "Follows the title while this is a draft. Type one to fix it in place."
}

// ─── Admin write handlers ────────────────────────────────────────────

func (a *app) handleNewPost(w http.ResponseWriter, r *http.Request) {
	// The row is created before the editor opens because the editor autosaves
	// by post id: with no row, the first autosave would have nowhere to land.
	p, err := a.store.Create(&Post{Title: "Untitled post", Status: StatusDraft})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/posts/"+p.ID, http.StatusSeeOther)
}

func (a *app) handleSavePost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := a.store.ByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	// Both of these describe what the post WAS, and both have to be read before
	// the form overwrites the struct.
	wasPublished := p.Published()
	// autoSlug: the stored slug is what the stored title would have produced, so
	// nobody has chosen it — it is a derived value, not a decision.
	//
	// The comparison drops a trailing "-2"/"-3" first, because uniqueSlug adds
	// one whenever two posts want the same slug. Create two posts in a row and
	// the second is born "untitled-post-2"; without the strip that reads as a
	// deliberate choice and its URL never follows the title it is finally given.
	autoSlug := stripSlugSuffix(p.Slug) == TagSlug(p.Title)

	p.Title = strings.TrimSpace(r.PostFormValue("title"))
	p.Summary = strings.TrimSpace(r.PostFormValue("summary"))

	// Slug rule, in one place: a hand-typed value always wins; otherwise a
	// derived slug follows the title while the post is a draft; publishing
	// freezes it.
	//
	// The derived-slug clause is what stops a post created as "Untitled post"
	// keeping /posts/untitled-post forever — the form round-trips the slug it
	// was rendered with, so without it every save looks like an explicit
	// choice. Checking autoSlug rather than only the draft status is what stops
	// the opposite bug: a slug you typed by hand on a draft must survive the
	// save that publishes it under a new title.
	switch submitted := strings.TrimSpace(r.PostFormValue("slug")); {
	case submitted != "" && submitted != p.Slug:
		p.Slug = submitted // hand-edited in the form
	case !wasPublished && autoSlug:
		p.Slug = "" // derived, still a draft — let uniqueSlug re-derive it
	}

	p.Tags = splitTags(r.PostFormValue("tags"))
	if status := r.PostFormValue("status"); status == StatusDraft || status == StatusPublished {
		p.Status = status
	}
	if p.Status == StatusPublished && p.PublishedAt.IsZero() {
		p.PublishedAt = p.UpdatedAt
	}
	if p.Title == "" {
		p.Title = "Untitled post"
	}
	if err := a.store.Update(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The hidden fields are only written when the editor bridge ran, so an
	// empty value means "no editor on this submit" rather than "empty body".
	// Writing it through unconditionally would blank the post whenever the
	// form was submitted with scripts off.
	if bodyJSON := r.PostFormValue("body_json"); strings.TrimSpace(bodyJSON) != "" {
		if err := a.store.UpdateBody(p.ID, bodyJSON, r.PostFormValue("body_md")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/admin/posts/"+p.ID, http.StatusSeeOther)
}

func (a *app) handleSetStatus(w http.ResponseWriter, r *http.Request) {
	status := r.PostFormValue("status")
	if status != StatusDraft && status != StatusPublished {
		http.Error(w, "unknown status", http.StatusBadRequest)
		return
	}
	if err := a.store.SetStatus(r.PathValue("id"), status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *app) handleDeletePost(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Delete(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// registerAdminScreens registers the admin pages. The write handlers are
// registered on the router in main.go, where requireAdmin wraps them.
func (a *app) registerAdminScreens(uiApp *appui.App, layout *appui.Layout) {
	uiApp.Register("/admin/login", &loginScreen{app: a}, layout)
	uiApp.Register("/admin", &adminListScreen{app: a}, layout)
	uiApp.Register("/admin/posts/:id", &editScreen{app: a}, layout)
}
