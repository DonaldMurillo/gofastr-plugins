package main

// RSS and the sitemap. Both read the published set from the store on each
// request, so publishing a post makes it subscribable immediately with no cache
// to invalidate.
//
// The sitemap is hand-built here rather than taken from uihost.WithSitemap:
// that option enumerates REGISTERED ROUTES, which for this app is the handful
// of patterns in public.go ("/posts/:slug"), not the posts themselves. The
// static recipe can use it because it registers a route per post; this one
// cannot, so it walks the database instead.

import (
	"encoding/xml"
	"net/http"
	"os"
	"strings"
	"time"
)

// baseURL resolves the absolute origin for feed and sitemap links. SITE_URL
// wins; otherwise it is reconstructed from the request so `go run` works on a
// random port. Set SITE_URL in production — Host is attacker-controlled, and a
// spoofed header would put someone else's origin in your feed.
func baseURL(r *http.Request) string {
	if v := strings.TrimRight(os.Getenv("SITE_URL"), "/"); v != "" {
		return v
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Atom    string     `xml:"xmlns:atom,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Language    string    `xml:"language"`
	Generator   string    `xml:"generator"`
	LastBuild   string    `xml:"lastBuildDate,omitempty"`
	AtomLink    atomLink  `xml:"atom:link"`
	Items       []rssItem `xml:"item"`
}

// atomLink is the self-reference feed validators ask for.
type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        rssGUID  `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	Description string   `xml:"description"`
	Categories  []string `xml:"category,omitempty"`
}

type rssGUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}

func (a *app) handleFeed(w http.ResponseWriter, r *http.Request) {
	posts, err := a.store.Published()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	base := baseURL(r)

	feed := rssFeed{
		Version: "2.0",
		Atom:    "http://www.w3.org/2005/Atom",
		Channel: rssChannel{
			Title:       siteName,
			Link:        base + "/",
			Description: tagline,
			Language:    "en",
			Generator:   "gofastr-plugins/recipes/blogapp",
			AtomLink:    atomLink{Href: base + "/feed.xml", Rel: "self", Type: "application/rss+xml"},
		},
	}
	if len(posts) > 0 {
		feed.Channel.LastBuild = posts[0].Date().Format(time.RFC1123Z)
	}
	for _, p := range posts {
		url := base + "/posts/" + p.Slug
		feed.Channel.Items = append(feed.Channel.Items, rssItem{
			Title:       p.Title,
			Link:        url,
			GUID:        rssGUID{Value: url, IsPermaLink: true},
			PubDate:     p.Date().Format(time.RFC1123Z),
			Description: p.Summary,
			Categories:  p.Tags,
		})
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(feed)
}

// ─── Sitemap ─────────────────────────────────────────────────────────

type urlSet struct {
	XMLName xml.Name   `xml:"urlset"`
	NS      string     `xml:"xmlns,attr"`
	URLs    []sitemapU `xml:"url"`
}

type sitemapU struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

func (a *app) handleSitemap(w http.ResponseWriter, r *http.Request) {
	posts, err := a.store.Published()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags, err := a.store.Tags()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	base := baseURL(r)

	set := urlSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	add := func(path, lastmod string) {
		set.URLs = append(set.URLs, sitemapU{Loc: base + path, LastMod: lastmod})
	}

	newest := ""
	if len(posts) > 0 {
		newest = posts[0].Date().Format("2006-01-02")
	}
	add("/", newest)
	add("/archive", newest)
	add("/tags", newest)
	for _, p := range posts {
		// Per-post lastmod is the post's own updated_at, which this app does
		// track — unlike the static recipe, where every entry shares one date.
		add("/posts/"+p.Slug, p.UpdatedAt.Format("2006-01-02"))
	}
	for _, t := range tags {
		add("/tags/"+t.Slug, newest)
	}

	// /search and everything under /admin are deliberately absent: one is a
	// form endpoint whose content depends on a query string, the other is
	// behind a login and has no business in an index.
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(set)
}

func (a *app) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /admin\nDisallow: /search\nSitemap: " +
		baseURL(r) + "/sitemap.xml\n"))
}
