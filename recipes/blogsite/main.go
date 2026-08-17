// Command blogsite is the markdown blog recipe: a complete blog whose content
// is a directory of markdown files, with no database, no build step, and none
// of the plugins in this repository.
//
// It is the baseline half of a pair. This recipe is what you want when the
// author is comfortable in a text editor and publishing means a deploy. Its
// sibling, recipes/blogapp, keeps posts in SQLite and edits them in the
// browser with the sandboxed rich text plugin — same reading experience, a
// different authoring story.
//
// Run with:
//
//	go run ./recipes/blogsite
//
// Then open the URL it prints. PORT pins the port; SITE_URL sets the absolute
// origin used in the feeds and sitemap.
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/framework"
	uitheme "github.com/DonaldMurillo/gofastr/framework/ui/theme"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
)

// recipeSourceURL is where this recipe's code lives. The header, footer, and
// homepage link to it, since a demo whose point is "read the implementation"
// should say where the implementation is.
const recipeSourceURL = "https://github.com/DonaldMurillo/gofastr-plugins/tree/main/recipes/blogsite"

// contentFS carries every post and page into the binary. Embedding rather than
// reading from disk means `go run ./recipes/blogsite` works from any directory
// and a deployed build is one file with no assets directory beside it.
//
//go:embed content
var contentFS embed.FS

// newApp builds the whole site ready to serve. Shared by main and the tests so
// both exercise the same wiring; now is the clock Load compares post dates
// against, passed in so a test can pin it.
func newApp(now time.Time) (*framework.App, *Site, error) {
	content, err := fs.Sub(contentFS, "content")
	if err != nil {
		return nil, nil, fmt.Errorf("blogsite: opening embedded content: %w", err)
	}
	site, err := Load(content, now)
	if err != nil {
		return nil, nil, err
	}

	uiApp := appui.NewApp(siteName)
	uiApp.WithTheme(uitheme.Default())
	layout := newLayout(site)
	uiApp.SetDefaultLayout(layout)
	registerScreens(site, uiApp, layout)

	host := uihost.New(uiApp,
		uihost.WithDescription(tagline),
		uihost.WithNotFoundScreen(&notFoundScreen{site: site}),
		// The sitemap enumerates the registered routes, which for this site is
		// every published post, tag, page, and pagination step. /search is
		// excluded: it is a form endpoint whose useful content depends on a
		// query string the sitemap cannot express.
		uihost.WithSitemap(uihost.SitemapConfig{
			BaseURL:      siteOrigin(),
			LastMod:      site.Posts[0].Date,
			ExcludePaths: []string{"/search"},
		}),
		uihost.WithRobots(uihost.RobotsConfig{Disallow: []string{"/search"}}),
		uihost.WithOpenGraph(uihost.OG{
			Title:       siteName,
			Description: tagline,
			Type:        "website",
		}),
	)

	app := framework.NewUIHostApp(host,
		framework.WithConfig(framework.AppConfig{Name: "blogsite"}),
	)
	if err := app.InitPlugins(); err != nil {
		return nil, nil, err
	}

	registerFeeds(site, app.Router())
	return app, site, nil
}

// siteOrigin is the boot-time origin for the sitemap. The host generates that
// file once from the route table rather than per request, so unlike the feeds
// it cannot fall back to the request's Host header. Without SITE_URL it emits
// localhost URLs, which is wrong for a deploy and harmless for a local demo
// nobody submits to a crawler.
func siteOrigin() string {
	if v := os.Getenv("SITE_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func main() {
	app, site, err := newApp(time.Now())
	if err != nil {
		log.Fatal(err)
	}

	// ":0" lets the OS pick a free port so repeated dev runs never collide;
	// PORT pins it, which is what the e2e harness relies on.
	addr := ":0"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("blogsite: %d posts, %d tags, %d pages", len(site.Posts), len(site.Tags), len(site.Pages))
	fmt.Printf("blogsite listening on http://localhost:%d/\n", ln.Addr().(*net.TCPAddr).Port)
	if err := http.Serve(ln, app.Router()); err != nil {
		log.Fatal(err)
	}
}
