package main

// End-to-end-ish HTTP tests: boot the real app over httptest and walk it the
// way a reader (and a crawler) does. These are the tests that would catch a
// route that stopped being registered, a feed that stopped excluding drafts, or
// a 404 that quietly became a 200.

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func embeddedContent(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(contentFS, "content")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	return sub
}

func startServer(t *testing.T) (*httptest.Server, *Site) {
	t.Helper()
	app, site, err := newApp(loadClock)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	srv := httptest.NewServer(app.Router())
	t.Cleanup(srv.Close)
	return srv, site
}

// get fetches path and returns the status and body.
func get(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: reading body: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

func TestEveryPublicRouteRenders(t *testing.T) {
	srv, site := startServer(t)

	paths := []string{"/", "/tags", "/archive", "/search", "/search?q=markdown",
		"/feed.xml", "/feed.json", "/sitemap.xml", "/robots.txt"}
	for _, p := range site.Posts {
		paths = append(paths, "/posts/"+p.Slug)
	}
	for _, tag := range site.Tags {
		paths = append(paths, "/tags/"+tag.Slug)
	}
	for _, page := range site.Pages {
		paths = append(paths, "/"+page.Slug)
	}
	if len(site.Posts) > postsPerPage {
		paths = append(paths, "/page/2")
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			status, body := get(t, srv, path)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if strings.TrimSpace(body) == "" {
				t.Fatal("empty body")
			}
		})
	}
}

func TestPostPageRendersTitleBodyAndNavigation(t *testing.T) {
	srv, site := startServer(t)
	post, ok := site.PostBySlug("frontmatter-reference")
	if !ok {
		t.Fatal("fixture post missing")
	}

	status, body := get(t, srv, "/posts/"+post.Slug)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	for _, want := range []string{
		post.Title,
		"Frontmatter is the",           // body prose
		"<table",                       // the post's markdown table rendered
		"/tags/" + TagSlug("markdown"), // tag chip links out
	} {
		if !strings.Contains(body, want) {
			t.Errorf("post page missing %q", want)
		}
	}

	// The prev/next pager must point at the real chronological neighbours.
	if post.Prev != nil && !strings.Contains(body, "/posts/"+post.Prev.Slug) {
		t.Errorf("missing link to previous post %s", post.Prev.Slug)
	}
	if post.Next != nil && !strings.Contains(body, "/posts/"+post.Next.Slug) {
		t.Errorf("missing link to next post %s", post.Next.Slug)
	}
}

// Code fences must reach the reader as highlighted code blocks rather than as
// literal backticks — the whole reason bodies are pre-rendered through
// ui.Markdown instead of being handed over as raw text.
func TestCodeBlocksAreRendered(t *testing.T) {
	srv, _ := startServer(t)
	_, body := get(t, srv, "/posts/one-binary-no-assets")

	if strings.Contains(body, "```go") {
		t.Error("a literal ``` fence reached the page; the markdown was not rendered")
	}
	if !strings.Contains(body, "<pre") {
		t.Error("no <pre> element; the fenced block did not become a code block")
	}
	if !strings.Contains(body, "go:embed content") {
		t.Error("the code block's contents are missing")
	}
}

// Raw HTML in a source file must render as text. core/markdown escapes rather
// than passing through, and this is the test that says so out loud.
func TestSourceHTMLIsEscaped(t *testing.T) {
	srv, _ := startServer(t)
	_, body := get(t, srv, "/posts/markdown-subset")

	if !strings.Contains(body, "&lt;div&gt;") {
		t.Error("the post's literal <div> was not escaped into the output")
	}
}

func TestUnknownPathsReturn404(t *testing.T) {
	srv, _ := startServer(t)

	for _, path := range []string{
		"/posts/does-not-exist",
		"/posts/tags-are-just-strings", // a draft — hidden means unreachable
		"/posts/scheduled-for-later",   // scheduled, likewise
		"/tags/not-a-tag",
		"/page/99",
		"/nope",
	} {
		t.Run(path, func(t *testing.T) {
			status, body := get(t, srv, path)
			if status != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", status)
			}
			// The 404 renders through the site's own chrome, so it should
			// carry the recovery links rather than a bare browser error.
			if !strings.Contains(body, "404") {
				t.Error("404 body does not say so")
			}
			if !strings.Contains(body, "Back to posts") {
				t.Error("404 body is missing the recovery action")
			}
		})
	}
}

func TestSearchPage(t *testing.T) {
	srv, _ := startServer(t)

	t.Run("hit", func(t *testing.T) {
		_, body := get(t, srv, "/search?q=frontmatter")
		if !strings.Contains(body, "Frontmatter reference") {
			t.Error("expected the frontmatter post in the results")
		}
	})
	t.Run("miss", func(t *testing.T) {
		_, body := get(t, srv, "/search?q=zzzznotarealword")
		if !strings.Contains(body, "No matches") {
			t.Error("expected the empty state")
		}
	})
	t.Run("blank", func(t *testing.T) {
		status, body := get(t, srv, "/search")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if !strings.Contains(body, "Type a query") {
			t.Error("expected the prompt for an empty query")
		}
	})
	t.Run("drafts stay out", func(t *testing.T) {
		_, body := get(t, srv, "/search?q=Tags+are+just+strings")
		if strings.Contains(body, "/posts/tags-are-just-strings") {
			t.Error("a draft turned up in search results")
		}
	})
}

// ─── Feeds ───────────────────────────────────────────────────────────

func TestRSSFeed(t *testing.T) {
	srv, site := startServer(t)

	resp, err := srv.Client().Get(srv.URL + "/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
		t.Errorf("Content-Type = %q", ct)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var feed rssFeed
	if err := xml.Unmarshal(raw, &feed); err != nil {
		t.Fatalf("feed is not well-formed XML: %v", err)
	}

	if got, want := len(feed.Channel.Items), len(site.Posts); got != want {
		t.Errorf("feed has %d items, want %d (one per published post)", got, want)
	}
	// Asserted against the raw bytes rather than the decoded struct:
	// encoding/xml matches elements by namespace URI, not by the literal
	// "atom:" prefix, so it never populates a field tagged `xml:"atom:link"`
	// even though the element is right there in the output.
	if !strings.Contains(string(raw), `rel="self"`) {
		t.Error("missing the atom:link self-reference validators ask for")
	}
	for _, item := range feed.Channel.Items {
		if item.Title == "" || item.Link == "" || item.PubDate == "" {
			t.Errorf("incomplete item: %+v", item)
		}
		// Absolute links, or a reader cannot resolve them.
		if !strings.HasPrefix(item.Link, "http://") && !strings.HasPrefix(item.Link, "https://") {
			t.Errorf("item link %q is not absolute", item.Link)
		}
		if !item.GUID.IsPermaLink || item.GUID.Value != item.Link {
			t.Errorf("guid %+v does not match the permalink %q", item.GUID, item.Link)
		}
	}
	for _, item := range feed.Channel.Items {
		if strings.Contains(item.Link, "tags-are-just-strings") ||
			strings.Contains(item.Link, "scheduled-for-later") {
			t.Errorf("a hidden post reached the feed: %s", item.Link)
		}
	}
}

func TestJSONFeed(t *testing.T) {
	srv, site := startServer(t)

	resp, err := srv.Client().Get(srv.URL + "/feed.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var feed jsonFeed
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		t.Fatalf("feed is not valid JSON: %v", err)
	}
	if feed.Version != "https://jsonfeed.org/version/1.1" {
		t.Errorf("version = %q", feed.Version)
	}
	if got, want := len(feed.Items), len(site.Posts); got != want {
		t.Errorf("%d items, want %d", got, want)
	}
	for _, item := range feed.Items {
		if item.ID == "" || item.URL == "" || item.DatePublished == "" {
			t.Errorf("incomplete item: %+v", item)
		}
	}
}

// The sitemap comes from the route table rather than a hand-kept list, so this
// asserts the derivation actually covers the corpus and excludes what it should.
func TestSitemapCoversPublishedRoutes(t *testing.T) {
	srv, site := startServer(t)
	_, body := get(t, srv, "/sitemap.xml")

	for _, p := range site.Posts {
		if !strings.Contains(body, "/posts/"+p.Slug) {
			t.Errorf("sitemap is missing /posts/%s", p.Slug)
		}
	}
	for _, tag := range site.Tags {
		if !strings.Contains(body, "/tags/"+tag.Slug) {
			t.Errorf("sitemap is missing /tags/%s", tag.Slug)
		}
	}
	for _, hidden := range []string{"tags-are-just-strings", "scheduled-for-later"} {
		if strings.Contains(body, hidden) {
			t.Errorf("sitemap lists the hidden post %s", hidden)
		}
	}
	// Compare whole paths. A substring check trips over the legitimately
	// listed /posts/search-without-an-index and /tags/search, both of which
	// end in "/search".
	for _, loc := range sitemapPaths(t, body) {
		if loc == "/search" {
			t.Error("sitemap lists /search, which was supposed to be excluded")
		}
	}
}

// sitemapPaths decodes the sitemap and returns each <loc> as a path, so
// assertions can compare whole paths instead of substrings.
func sitemapPaths(t *testing.T, body string) []string {
	t.Helper()
	var doc struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("sitemap is not well-formed XML: %v", err)
	}
	if len(doc.URLs) == 0 {
		t.Fatal("sitemap has no <url> entries")
	}
	paths := make([]string, 0, len(doc.URLs))
	for _, u := range doc.URLs {
		parsed, err := url.Parse(u.Loc)
		if err != nil {
			t.Fatalf("sitemap loc %q is not a URL: %v", u.Loc, err)
		}
		if !parsed.IsAbs() {
			t.Errorf("sitemap loc %q is not absolute; crawlers require absolute URLs", u.Loc)
		}
		paths = append(paths, parsed.Path)
	}
	return paths
}

func TestRobotsDisallowsSearch(t *testing.T) {
	srv, _ := startServer(t)
	status, body := get(t, srv, "/robots.txt")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, "Disallow: /search") {
		t.Errorf("robots.txt does not disallow /search:\n%s", body)
	}
	if !strings.Contains(body, "Sitemap:") {
		t.Errorf("robots.txt does not point at the sitemap:\n%s", body)
	}
}

// SITE_URL is unset in tests, so the feed falls back to the request Host. This
// pins that fallback: without it the feed would emit relative links, which no
// reader can resolve.
func TestFeedLinksUseRequestHost(t *testing.T) {
	srv, _ := startServer(t)
	_, body := get(t, srv, "/feed.xml")

	host := strings.TrimPrefix(srv.URL, "http://")
	if !strings.Contains(body, "http://"+host+"/posts/") {
		t.Errorf("feed links do not use the request host %q", host)
	}
}

func TestPaginationLinksChain(t *testing.T) {
	srv, site := startServer(t)
	totalPages := (len(site.Posts) + postsPerPage - 1) / postsPerPage
	if totalPages < 2 {
		t.Skip("corpus is a single page; nothing to chain")
	}

	_, first := get(t, srv, "/")
	if !strings.Contains(first, "/page/2") {
		t.Error("page 1 does not link to page 2")
	}
	if strings.Contains(first, "Newer posts") {
		t.Error("page 1 offers a newer-posts link with nothing newer to show")
	}

	status, second := get(t, srv, "/page/2")
	if status != http.StatusOK {
		t.Fatalf("page 2 status = %d", status)
	}
	// Page 2's "newer" link goes to "/" rather than "/page/1", which is not a
	// registered route.
	if !strings.Contains(second, "Newer posts") {
		t.Error("page 2 has no newer-posts link")
	}
	if strings.Contains(second, `href="/page/1"`) {
		t.Error("page 2 links to /page/1, which is not registered")
	}
}

// Duplicate element ids break label/control association and fail axe's
// duplicate-id rule. They are easy to introduce here because ui.SiteHeader
// renders its Actions slot TWICE — once in the desktop bar, once in the mobile
// drawer — so anything with a fixed id placed there lands in the DOM twice.
// This walks every public page rather than spot-checking one.
func TestNoDuplicateElementIDs(t *testing.T) {
	srv, site := startServer(t)

	paths := []string{"/", "/tags", "/archive", "/search", "/search?q=markdown"}
	for _, p := range site.Posts {
		paths = append(paths, "/posts/"+p.Slug)
	}
	for _, tag := range site.Tags {
		paths = append(paths, "/tags/"+tag.Slug)
	}
	for _, page := range site.Pages {
		paths = append(paths, "/"+page.Slug)
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, srv, path)
			if dupes := duplicateIDs(body); len(dupes) > 0 {
				t.Errorf("duplicate element ids: %v", dupes)
			}
		})
	}
}

var idAttrRe = regexp.MustCompile(`\sid="([^"]+)"`)

// duplicateIDs returns every id appearing more than once in the markup.
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
