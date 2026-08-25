package main

// HTTP-level tests over the real app: an in-memory database, the real router,
// the real middleware chain, the real plugin registration.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"

	"database/sql"

	_ "github.com/DonaldMurillo/gofastr/sqlite/stdlib"
)

// testApp boots the app against a fresh in-memory database and returns a server
// plus the app handle so tests can reach the store directly.
func testApp(t *testing.T) (*httptest.Server, *app) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1) // pooled conns each get a private :memory: db
	t.Cleanup(func() { db.Close() })

	fw, a, err := newApp(db)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	srv := httptest.NewServer(fw.Router())
	t.Cleanup(srv.Close)
	return srv, a
}

// client returns an HTTP client that keeps cookies and does NOT follow
// redirects, so tests can assert on the redirect itself rather than on wherever
// it happened to land.
func client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func post(t *testing.T, c *http.Client, target string, form url.Values) (int, string) {
	t.Helper()
	resp, err := c.PostForm(target, form)
	if err != nil {
		t.Fatalf("POST %s: %v", target, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("Location")
}

// login signs the client in and fails the test if it did not take.
func login(t *testing.T, c *http.Client, base string) {
	t.Helper()
	status, loc := post(t, c, base+"/admin/login/submit", url.Values{"password": {"demo"}})
	if status != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", status)
	}
	if loc != "/admin" {
		t.Fatalf("login redirected to %q, want /admin", loc)
	}
}

func TestPublicRoutesRender(t *testing.T) {
	srv, _ := testApp(t)
	c := client(t)

	for _, path := range []string{"/", "/tags", "/archive", "/search", "/search?q=editor",
		"/feed.xml", "/sitemap.xml", "/robots.txt", "/admin/login"} {
		t.Run(path, func(t *testing.T) {
			status, body := get(t, c, srv.URL+path)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if strings.TrimSpace(body) == "" {
				t.Fatal("empty body")
			}
		})
	}
}

func TestSeededPostRenders(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)

	posts, err := a.store.Published()
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) == 0 {
		t.Fatal("seed produced no published posts")
	}

	for _, p := range posts {
		status, body := get(t, c, srv.URL+"/posts/"+p.Slug)
		if status != http.StatusOK {
			t.Fatalf("%s: status = %d", p.Slug, status)
		}
		if !strings.Contains(body, p.Title) {
			t.Errorf("%s: title missing from the page", p.Slug)
		}
	}
}

// The point of the whole recipe: a reader gets server-rendered prose and never
// the editor bundle.
func TestReaderNeverGetsTheEditor(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)

	// Target the seeded post by slug rather than by position: the listing is
	// ordered by publication date, so posts[0] silently becomes a different
	// post whenever the seed changes.
	const slug = "why-the-reader-never-loads-the-editor"
	if _, err := a.store.BySlug(slug); err != nil {
		t.Fatalf("fixture post %s is missing from the seed: %v", slug, err)
	}
	_, body := get(t, c, srv.URL+"/posts/"+slug)

	if strings.Contains(body, "data-fui-plugin") {
		t.Error("the editor mount marker reached a public post page")
	}
	if strings.Contains(body, "richtext/plugin.js") || strings.Contains(body, "/__gofastr/plugin/richtext/frame") {
		t.Error("the editor bundle is referenced from a public post page")
	}
	// And the body actually rendered: this text lives inside the seeded
	// ProseMirror document, so seeing it proves ssr.RenderJSON ran.
	if !strings.Contains(body, "canonical ProseMirror document") {
		t.Error("the stored document did not render into the page")
	}
	// The read view's own markup, not just any HTML.
	if !strings.Contains(body, "richtext-read") {
		t.Error("the richtext read-view wrapper is missing")
	}
}

func TestDraftsAreInvisible(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)

	all, _ := a.store.All()
	var draft *Post
	for _, p := range all {
		if !p.Published() {
			draft = p
		}
	}
	if draft == nil {
		t.Fatal("the seed has no draft to test with")
	}

	status, _ := get(t, c, srv.URL+"/posts/"+draft.Slug)
	if status != http.StatusNotFound {
		t.Errorf("draft slug status = %d, want 404", status)
	}

	for _, path := range []string{"/", "/archive", "/feed.xml", "/sitemap.xml"} {
		_, body := get(t, c, srv.URL+path)
		if strings.Contains(body, draft.Slug) {
			t.Errorf("%s leaks the draft slug %q", path, draft.Slug)
		}
	}
}

// A dynamic route matches any slug-shaped path, so this asserts the middleware
// that turns a miss into a real 404 is doing its job. A soft 404 (status 200
// with a "not found" body) is the failure this guards against.
func TestUnknownPathsGetHardNotFound(t *testing.T) {
	srv, _ := testApp(t)
	c := client(t)

	for _, path := range []string{"/posts/no-such-post", "/tags/no-such-tag", "/page/99", "/nothing-here"} {
		t.Run(path, func(t *testing.T) {
			status, body := get(t, c, srv.URL+path)
			if status != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", status)
			}
			if !strings.Contains(body, "404") {
				t.Error("the 404 page does not say so")
			}
			if !strings.Contains(body, "Back to posts") {
				t.Error("the 404 page is missing its recovery link")
			}
		})
	}
}

// ─── The admin gate ──────────────────────────────────────────────────

func TestAdminRequiresLogin(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)

	all, _ := a.store.All()
	id := all[0].ID

	t.Run("GET redirects", func(t *testing.T) {
		for _, path := range []string{"/admin", "/admin/posts/" + id} {
			resp, err := c.Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Errorf("%s: status = %d, want 303", path, resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/admin/login") {
				t.Errorf("%s: redirected to %q, want the login page", path, loc)
			}
		}
	})

	t.Run("writes are refused", func(t *testing.T) {
		for _, path := range []string{
			"/admin/posts/new",
			"/admin/posts/" + id,
			"/admin/posts/" + id + "/status",
			"/admin/posts/" + id + "/delete",
		} {
			resp, err := c.PostForm(srv.URL+path, url.Values{})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: status = %d, want 403", path, resp.StatusCode)
			}
		}
		// And nothing was actually written.
		after, _ := a.store.All()
		if len(after) != len(all) {
			t.Errorf("post count changed from %d to %d despite every write being refused", len(all), len(after))
		}
	})
}

func TestWrongPasswordDoesNotSignIn(t *testing.T) {
	srv, _ := testApp(t)
	c := client(t)

	status, loc := post(t, c, srv.URL+"/admin/login/submit", url.Values{"password": {"wrong"}})
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(loc, "error=1") {
		t.Errorf("redirected to %q, want the login page with an error", loc)
	}
	resp, _ := c.Get(srv.URL + "/admin")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("/admin status = %d after a failed login, want 303", resp.StatusCode)
	}
}

// The login form's `next` is attacker-supplied, so it must only ever be a local
// path. Without the check it is an open redirect off a trusted domain.
func TestLoginNextIsNotAnOpenRedirect(t *testing.T) {
	srv, _ := testApp(t)

	for _, next := range []string{"https://evil.example/", "//evil.example/", "http://evil.example"} {
		c := client(t)
		_, loc := post(t, c, srv.URL+"/admin/login/submit",
			url.Values{"password": {"demo"}, "next": {next}})
		if loc != "/admin" {
			t.Errorf("next=%q redirected to %q, want /admin", next, loc)
		}
	}

	c := client(t)
	_, loc := post(t, c, srv.URL+"/admin/login/submit",
		url.Values{"password": {"demo"}, "next": {"/admin/posts"}})
	if loc != "/admin/posts" {
		t.Errorf("a local next was dropped: got %q", loc)
	}
}

func TestAdminListShowsDraftsAfterLogin(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)
	login(t, c, srv.URL)

	status, body := get(t, c, srv.URL+"/admin")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	all, _ := a.store.All()
	for _, p := range all {
		if !strings.Contains(body, p.ID) {
			t.Errorf("the admin list is missing post %s (%s)", p.ID, p.Title)
		}
	}
	if !strings.Contains(body, "Draft") {
		t.Error("no draft badge in the admin list")
	}
}

// ─── The authoring round trip ────────────────────────────────────────

func TestCreateEditPublishDelete(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)
	login(t, c, srv.URL)

	// Create.
	_, loc := post(t, c, srv.URL+"/admin/posts/new", url.Values{})
	if !strings.HasPrefix(loc, "/admin/posts/") {
		t.Fatalf("new post redirected to %q", loc)
	}
	id := strings.TrimPrefix(loc, "/admin/posts/")

	// The edit screen renders, and it is the ONE page that mounts the editor.
	status, editPage := get(t, c, srv.URL+"/admin/posts/"+id)
	if status != http.StatusOK {
		t.Fatalf("edit page status = %d", status)
	}
	if !strings.Contains(editPage, "data-fui-plugin") {
		t.Error("the edit page does not mount the editor")
	}
	if !strings.Contains(editPage, `name="body_json"`) || !strings.Contains(editPage, `name="body_md"`) {
		t.Error("richtext.Mount did not emit both hidden fields")
	}

	// Edit: metadata plus a body, the way the form submits them together.
	body := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello from the test."}]}]}`
	_, loc = post(t, c, srv.URL+"/admin/posts/"+id, url.Values{
		"title":     {"A test post"},
		"summary":   {"Written by the test suite."},
		"tags":      {"testing, go"},
		"status":    {StatusDraft},
		"body_json": {body},
		"body_md":   {"Hello from the test."},
	})
	if loc != "/admin/posts/"+id {
		t.Fatalf("save redirected to %q", loc)
	}

	saved, err := a.store.ByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "A test post" {
		t.Errorf("title = %q", saved.Title)
	}
	if saved.Slug != "a-test-post" {
		t.Errorf("slug = %q, want one derived from the title", saved.Slug)
	}
	if len(saved.Tags) != 2 {
		t.Errorf("tags = %v", saved.Tags)
	}
	if saved.Published() {
		t.Error("the post published itself without being asked to")
	}

	// A draft is not public yet.
	if status, _ := get(t, c, srv.URL+"/posts/"+saved.Slug); status != http.StatusNotFound {
		t.Errorf("draft is publicly readable: status %d", status)
	}

	// Publish.
	_, _ = post(t, c, srv.URL+"/admin/posts/"+id+"/status", url.Values{"status": {StatusPublished}})
	published, _ := a.store.ByID(id)
	if !published.Published() {
		t.Fatal("publish did not take")
	}
	if published.PublishedAt.IsZero() {
		t.Error("published_at was not stamped")
	}

	status, page := get(t, c, srv.URL+"/posts/"+published.Slug)
	if status != http.StatusOK {
		t.Fatalf("published post status = %d", status)
	}
	if !strings.Contains(page, "Hello from the test.") {
		t.Error("the saved body did not render on the public page")
	}
	if _, home := get(t, c, srv.URL+"/"); !strings.Contains(home, "A test post") {
		t.Error("the published post is not on the homepage")
	}
	if _, feed := get(t, c, srv.URL+"/feed.xml"); !strings.Contains(feed, "A test post") {
		t.Error("the published post is not in the feed")
	}

	// Delete.
	_, _ = post(t, c, srv.URL+"/admin/posts/"+id+"/delete", url.Values{})
	if _, err := a.store.ByID(id); err == nil {
		t.Error("the post survived deletion")
	}
	if status, _ := get(t, c, srv.URL+"/posts/"+published.Slug); status != http.StatusNotFound {
		t.Errorf("the deleted post is still readable: status %d", status)
	}
}

// A draft's slug follows its title so a post created as "Untitled post" does
// not keep /posts/untitled-post forever. Publishing freezes it, because a live
// URL that moves whenever someone tweaks a title breaks every link to it.
func TestSlugFollowsTitleUntilPublished(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)
	login(t, c, srv.URL)

	_, loc := post(t, c, srv.URL+"/admin/posts/new", url.Values{})
	id := strings.TrimPrefix(loc, "/admin/posts/")

	save := func(title, slug, status string) *Post {
		t.Helper()
		// slug carries whatever the form was rendered with, which is how the
		// real edit page submits it.
		_, _ = post(t, c, srv.URL+"/admin/posts/"+id, url.Values{
			"title": {title}, "slug": {slug}, "status": {status},
		})
		p, err := a.store.ByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Draft: retitling moves the slug.
	p := save("First title", "untitled-post", StatusDraft)
	if p.Slug != "first-title" {
		t.Fatalf("draft slug = %q, want it to follow the title", p.Slug)
	}
	p = save("Second title", p.Slug, StatusDraft)
	if p.Slug != "second-title" {
		t.Fatalf("draft slug = %q, want it to keep following the title", p.Slug)
	}

	// A hand-typed slug wins and sticks.
	p = save("Third title", "my-own-slug", StatusDraft)
	if p.Slug != "my-own-slug" {
		t.Fatalf("slug = %q, want the hand-typed value", p.Slug)
	}

	// Publishing freezes it.
	p = save("Published title", p.Slug, StatusPublished)
	if p.Slug != "my-own-slug" {
		t.Fatalf("slug = %q right after publishing, want it unchanged", p.Slug)
	}
	p = save("Renamed after publishing", p.Slug, StatusPublished)
	if p.Slug != "my-own-slug" {
		t.Fatalf("slug = %q after retitling a published post, want it frozen", p.Slug)
	}

	// Except when explicitly changed.
	p = save("Renamed after publishing", "deliberate-move", StatusPublished)
	if p.Slug != "deliberate-move" {
		t.Fatalf("slug = %q, want an explicit change to win even when published", p.Slug)
	}
}

// Two posts sharing a title must not collide on the UNIQUE slug column.
func TestSlugCollisionsGetASuffix(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)
	login(t, c, srv.URL)

	var slugs []string
	for i := 0; i < 3; i++ {
		_, loc := post(t, c, srv.URL+"/admin/posts/new", url.Values{})
		id := strings.TrimPrefix(loc, "/admin/posts/")
		_, _ = post(t, c, srv.URL+"/admin/posts/"+id, url.Values{
			"title": {"Same title"}, "slug": {""}, "status": {StatusDraft},
		})
		p, err := a.store.ByID(id)
		if err != nil {
			t.Fatal(err)
		}
		slugs = append(slugs, p.Slug)
	}

	seen := map[string]bool{}
	for _, s := range slugs {
		if s == "" {
			t.Error("a post ended up with an empty slug")
		}
		if seen[s] {
			t.Errorf("duplicate slug %q in %v", s, slugs)
		}
		seen[s] = true
	}
	if slugs[0] != "same-title" {
		t.Errorf("first slug = %q, want same-title", slugs[0])
	}
}

// ─── The security test ───────────────────────────────────────────────

// The plugin's capability gate passes for anonymous callers — auth.HasScope
// returns true when the context has no token scopes. So the ONLY thing standing
// between an anonymous POST and an overwritten post is the isAdmin check inside
// the save handler. This is the test that proves it is there.
func TestAnonymousPluginSaveCannotOverwriteAPost(t *testing.T) {
	srv, a := testApp(t)

	posts, _ := a.store.Published()
	target := posts[0]
	before, err := a.store.ByID(target.ID)
	if err != nil {
		t.Fatal(err)
	}

	anon := client(t) // no login
	payload := `{"docId":"` + target.ID + `",` +
		`"doc":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"OWNED"}]}]},` +
		`"markdown":"OWNED","schemaVersion":"richtext-v1"}`

	resp, err := anon.Post(srv.URL+"/__gofastr/plugin/richtext/save",
		"application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("the plugin accepted an anonymous save")
	}

	after, err := a.store.ByID(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BodyJSON != before.BodyJSON {
		t.Fatal("an anonymous request overwrote a post body")
	}
	if strings.Contains(after.BodyJSON, "OWNED") {
		t.Fatal("the anonymous payload landed in the database")
	}

	// And the public page still shows the real content.
	_, page := get(t, client(t), srv.URL+"/posts/"+target.Slug)
	if strings.Contains(page, "OWNED") {
		t.Fatal("the anonymous payload reached readers")
	}
}

// The same endpoint must work for a signed-in admin, or the test above would
// pass for the wrong reason — a save handler that rejects everyone.
func TestAuthenticatedPluginSaveWritesTheBody(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)
	login(t, c, srv.URL)

	posts, _ := a.store.Published()
	target := posts[0]

	payload := `{"docId":"` + target.ID + `",` +
		`"doc":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Autosaved."}]}]},` +
		`"markdown":"Autosaved.","schemaVersion":"richtext-v1"}`

	resp, err := c.Post(srv.URL+"/__gofastr/plugin/richtext/save",
		"application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("authenticated save status = %d: %s", resp.StatusCode, body)
	}

	after, _ := a.store.ByID(target.ID)
	if !strings.Contains(after.BodyJSON, "Autosaved.") {
		t.Errorf("the autosave did not persist: %q", after.BodyJSON)
	}
	if after.BodyMD != "Autosaved." {
		t.Errorf("body_md = %q", after.BodyMD)
	}
}

func TestAnonymousUploadIsRefused(t *testing.T) {
	srv, a := testApp(t)

	// A 1x1 PNG, so the plugin's own image sniffing passes and the only thing
	// that can reject this is the handler's admin check.
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89,
	}
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/__gofastr/plugin/richtext/upload", strings.NewReader(string(png)))
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("X-Upload-Name", "x.png")
	req.Header.Set("X-Upload-Type", "image/png")

	resp, err := client(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("the plugin accepted an anonymous upload")
	}

	if _, err := a.store.Image("anything"); err == nil {
		t.Error("an image was stored")
	}
}

// ─── Feed and sitemap ────────────────────────────────────────────────

func TestFeedAndSitemap(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)
	posts, _ := a.store.Published()

	_, feed := get(t, c, srv.URL+"/feed.xml")
	for _, p := range posts {
		if !strings.Contains(feed, "/posts/"+p.Slug) {
			t.Errorf("feed is missing %s", p.Slug)
		}
	}
	if !strings.Contains(feed, `rel="self"`) {
		t.Error("feed is missing the atom self-link")
	}
	if !strings.Contains(feed, srv.URL+"/posts/") {
		t.Error("feed links are not absolute")
	}

	_, sitemap := get(t, c, srv.URL+"/sitemap.xml")
	for _, p := range posts {
		if !strings.Contains(sitemap, "/posts/"+p.Slug) {
			t.Errorf("sitemap is missing %s", p.Slug)
		}
	}
	if strings.Contains(sitemap, "/admin") {
		t.Error("the sitemap lists an admin URL")
	}

	_, robots := get(t, c, srv.URL+"/robots.txt")
	if !strings.Contains(robots, "Disallow: /admin") {
		t.Error("robots.txt does not disallow /admin")
	}
}

// Duplicate element ids break label/control association and fail axe's
// duplicate-id rule. The trap here is ui.SiteHeader, which renders its Actions
// slot twice (desktop bar + mobile drawer), so a form control with a fixed id
// placed there appears twice in the DOM. Covers the admin pages too, since the
// edit form is where most of the ids live.
func TestNoDuplicateElementIDs(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)
	login(t, c, srv.URL)

	paths := []string{"/", "/tags", "/archive", "/search", "/search?q=editor",
		"/admin", "/admin/login"}
	posts, _ := a.store.Published()
	for _, p := range posts {
		paths = append(paths, "/posts/"+p.Slug)
	}
	all, _ := a.store.All()
	paths = append(paths, "/admin/posts/"+all[0].ID)

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			status, body := get(t, c, srv.URL+path)
			if status != http.StatusOK {
				t.Fatalf("status = %d", status)
			}
			if dupes := duplicateIDs(body); len(dupes) > 0 {
				t.Errorf("duplicate element ids: %v", dupes)
			}
		})
	}
}

var idAttrRe = regexp.MustCompile(`\sid="([^"]+)"`)

func duplicateIDs(body string) []string {
	seen := map[string]int{}
	for _, m := range idAttrRe.FindAllStringSubmatch(body, -1) {
		seen[m[1]]++
	}
	var dupes []string
	for id, n := range seen {
		if n > 1 {
			dupes = append(dupes, fmt.Sprintf("%s (%d×)", id, n))
		}
	}
	sort.Strings(dupes)
	return dupes
}

// Two posts created back to back: the second is born with a "-2" slug, and its
// URL must still follow the title it is eventually given. Without stripping the
// disambiguating suffix before the auto-derived check, that second post keeps
// /posts/untitled-post-2 forever.
func TestSecondNewPostSlugStillFollowsItsTitle(t *testing.T) {
	srv, a := testApp(t)
	c := client(t)
	login(t, c, srv.URL)

	var ids []string
	for i := 0; i < 2; i++ {
		_, loc := post(t, c, srv.URL+"/admin/posts/new", url.Values{})
		ids = append(ids, strings.TrimPrefix(loc, "/admin/posts/"))
	}

	second, err := a.store.ByID(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if second.Slug != "untitled-post-2" {
		t.Fatalf("second new post slug = %q, want the disambiguated untitled-post-2", second.Slug)
	}

	_, _ = post(t, c, srv.URL+"/admin/posts/"+ids[1], url.Values{
		"title": {"A real title"}, "slug": {second.Slug}, "status": {StatusDraft},
	})
	after, _ := a.store.ByID(ids[1])
	if after.Slug != "a-real-title" {
		t.Errorf("slug = %q, want it to follow the title despite the -2 suffix", after.Slug)
	}
}

func TestStripSlugSuffix(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"untitled-post-2", "untitled-post"},
		{"untitled-post-12", "untitled-post"},
		{"untitled-post", "untitled-post"},
		{"post", "post"},
		{"my-own-slug", "my-own-slug"},
		{"trailing-", "trailing-"},
		{"-2", "-2"},
		{"", ""},
	} {
		if got := stripSlugSuffix(tc.in); got != tc.want {
			t.Errorf("stripSlugSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
