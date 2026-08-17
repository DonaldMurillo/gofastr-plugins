package main

// Syndication: RSS 2.0 and JSON Feed 1.1, both built from Site.Posts. Drafts
// and scheduled posts are absent because they are absent from that slice —
// there is no second filter to keep in sync.

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"os"
	"strings"
	"time"
)

// baseURL resolves the site's absolute origin for feed links and sitemap
// entries.
//
// SITE_URL wins when set. Otherwise the origin is reconstructed from the
// request, which keeps `go run ./recipes/blogsite` working on whatever port
// the OS handed out. Deriving it from a request is fine for a demo and wrong
// for production: Host is attacker-controlled, so a spoofed header would put
// someone else's origin in your feed, and feed readers cache what they are
// given. Set SITE_URL when you deploy.
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

// ─── RSS 2.0 ─────────────────────────────────────────────────────────

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

// atomLink is the self-reference feed validators ask for. Without it a reader
// that finds the feed by discovery has no canonical URL to re-fetch.
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
	Author      string   `xml:"author,omitempty"`
	Categories  []string `xml:"category,omitempty"`
}

type rssGUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}

func rssHandler(site *Site) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := baseURL(r)
		feed := rssFeed{
			Version: "2.0",
			Atom:    "http://www.w3.org/2005/Atom",
			Channel: rssChannel{
				Title:       siteName,
				Link:        base + "/",
				Description: tagline,
				Language:    "en",
				Generator:   "gofastr-plugins/recipes/blogsite",
				AtomLink:    atomLink{Href: base + "/feed.xml", Rel: "self", Type: "application/rss+xml"},
			},
		}
		if len(site.Posts) > 0 {
			feed.Channel.LastBuild = site.Posts[0].Date.Format(time.RFC1123Z)
		}
		for _, p := range site.Posts {
			url := base + "/posts/" + p.Slug
			feed.Channel.Items = append(feed.Channel.Items, rssItem{
				Title:       p.Title,
				Link:        url,
				GUID:        rssGUID{Value: url, IsPermaLink: true},
				PubDate:     p.Date.Format(time.RFC1123Z),
				Description: p.Summary,
				Categories:  p.Tags,
			})
		}

		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		w.Write([]byte(xml.Header))
		enc := xml.NewEncoder(w)
		enc.Indent("", "  ")
		// A half-written feed is worse than none, but the headers are already
		// out by the time an encode can fail (which, for these types, means
		// the client hung up). Nothing useful left to say to the client.
		_ = enc.Encode(feed)
	}
}

// ─── JSON Feed 1.1 ───────────────────────────────────────────────────

type jsonFeed struct {
	Version     string     `json:"version"`
	Title       string     `json:"title"`
	HomePageURL string     `json:"home_page_url"`
	FeedURL     string     `json:"feed_url"`
	Description string     `json:"description"`
	Language    string     `json:"language"`
	Items       []jsonItem `json:"items"`
}

type jsonItem struct {
	ID            string       `json:"id"`
	URL           string       `json:"url"`
	Title         string       `json:"title"`
	Summary       string       `json:"summary,omitempty"`
	DatePublished string       `json:"date_published"`
	Tags          []string     `json:"tags,omitempty"`
	Authors       []jsonAuthor `json:"authors,omitempty"`
	Image         string       `json:"image,omitempty"`
}

type jsonAuthor struct {
	Name string `json:"name"`
}

func jsonFeedHandler(site *Site) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := baseURL(r)
		feed := jsonFeed{
			Version:     "https://jsonfeed.org/version/1.1",
			Title:       siteName,
			HomePageURL: base + "/",
			FeedURL:     base + "/feed.json",
			Description: tagline,
			Language:    "en",
			Items:       make([]jsonItem, 0, len(site.Posts)),
		}
		for _, p := range site.Posts {
			url := base + "/posts/" + p.Slug
			item := jsonItem{
				ID:            url,
				URL:           url,
				Title:         p.Title,
				Summary:       p.Summary,
				DatePublished: p.Date.Format(time.RFC3339),
				Tags:          p.Tags,
			}
			if p.Author != "" {
				item.Authors = []jsonAuthor{{Name: p.Author}}
			}
			if p.Cover != "" {
				item.Image = p.Cover
			}
			feed.Items = append(feed.Items, item)
		}

		w.Header().Set("Content-Type", "application/feed+json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(feed)
	}
}

// registerFeeds mounts both feeds. The sitemap and robots.txt come from
// uihost options in main.go rather than from here — the host already
// enumerates the registered routes, and a hand-written sitemap would drift the
// first time a post was added.
func registerFeeds(site *Site, rt interface {
	Get(string, http.Handler)
}) {
	rt.Get("/feed.xml", rssHandler(site))
	rt.Get("/feed.json", jsonFeedHandler(site))
}
