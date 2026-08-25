// Command blogapp is the authored blog recipe: posts live in SQLite and are
// written in the browser with this repository's sandboxed rich text plugin.
//
// It is the second half of a pair. Its sibling, recipes/blogsite, keeps posts
// as markdown files and needs a deploy to publish. This one needs a login. The
// reading experience is deliberately the same in both: server-rendered HTML,
// no JavaScript required, working feeds.
//
// What this recipe is really about is the boundary around the editor:
//
//   - The plugin's capability gate does NOT authenticate. See requireAdmin in
//     session.go. This app adds the real check inside the save and upload
//     handlers below, and a test proves an anonymous save is refused.
//   - Readers never load the plugin. The stored ProseMirror document is
//     rendered server-side by richtext/ssr; the ~600 KB editor bundle appears
//     on exactly one route, behind the login.
//
// Run with:
//
//	go run ./recipes/blogapp
//
// Then open the URL it prints and sign in at /admin/login with the password
// "demo". PORT pins the port, BLOG_DB points at a database file (default is
// in-memory), BLOG_ADMIN_PASSWORD replaces the demo password, and SITE_URL sets
// the absolute origin used in the feed and sitemap.
package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	appui "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/framework"
	uitheme "github.com/DonaldMurillo/gofastr/framework/ui/theme"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
	_ "github.com/DonaldMurillo/gofastr/sqlite/stdlib"

	"github.com/DonaldMurillo/gofastr-plugins/richtext"
	"github.com/DonaldMurillo/gofastr-plugins/richtext/ssr"
)

// app carries the two things every handler and screen needs.
type app struct {
	store    *Store
	sessions *sessions
}

// openDB opens the database through gofastr's sqlite/stdlib driver
// (modernc, pure Go — no cgo toolchain needed, CGO_ENABLED=0 builds).
// The in-memory default caps the pool at one connection: each pooled
// connection would otherwise get its own private database and the
// second connection would see an empty schema.
func openDB() (*sql.DB, error) {
	if path := os.Getenv("BLOG_DB"); path != "" {
		return sql.Open("sqlite3", path)
	}
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// newApp builds the whole application ready to serve. Shared by main and the
// tests so both exercise identical wiring.
func newApp(db *sql.DB) (*framework.App, *app, error) {
	store, err := NewStore(db)
	if err != nil {
		return nil, nil, err
	}
	a := &app{store: store, sessions: newSessions()}

	uiApp := appui.NewApp(siteName)
	uiApp.WithTheme(uitheme.Default())
	layout := a.newLayout()
	uiApp.SetDefaultLayout(layout)
	a.registerPublicScreens(uiApp, layout)
	a.registerAdminScreens(uiApp, layout)

	// richtext.UIHostOption injects the two host scripts into every page the
	// host renders: the generic platform broker first, then this plugin's
	// adapter, which registers with it. Order matters, which is why it is one
	// option rather than two calls.
	host := uihost.New(uiApp,
		richtext.UIHostOption(),
		uihost.WithDescription(tagline),
		uihost.WithNotFoundScreen(&notFoundScreen{app: a}),
		// The read-view stylesheet. richtext/ssr emits class-scoped markup and
		// ships the CSS separately so a host can serve it once rather than
		// inline it into every post.
		uihost.WithCustomCSS(ssr.ReadCSS()),
		uihost.WithOpenGraph(uihost.OG{Title: siteName, Description: tagline, Type: "website"}),
	)

	fw := framework.NewUIHostApp(host,
		framework.WithDB(db),
		framework.WithConfig(framework.AppConfig{Name: "blogapp"}),
	)

	// Order matters, and getting it wrong fails in a way that looks like a
	// routing bug rather than an ordering one.
	//
	// sessionMiddleware must come FIRST: it only annotates the context, and
	// everything downstream — gateAdminScreens, the screens, and the plugin's
	// save/upload handlers — decides what to do by reading that annotation. It
	// is installed app-wide precisely so it also runs for the plugin's own
	// /__gofastr/plugin/richtext/* endpoints.
	//
	// gateAdminScreens then protects the host-rendered /admin pages, and
	// resolveOr404 turns a request for a post that does not exist into a real
	// 404 before the host can answer 200 for it.
	fw.Use(a.sessionMiddleware, a.gateAdminScreens, a.resolveOr404)

	// No WithDevGrantAll here, unlike example/. That flag skips BOTH sides of
	// the capability gate for unauthenticated demo pages; an app with a real
	// admin must not use it. But note that leaving it off is not what secures
	// these endpoints — see the comment on requireAdmin in session.go. The
	// checks inside the two handlers are.
	fw.RegisterPlugin(richtext.New(
		richtext.WithSaveHandler(a.savePostBody),
		richtext.WithUploadHandler(a.uploadImage),
	))

	if err := fw.InitPlugins(); err != nil {
		return nil, nil, err
	}

	a.registerRoutes(fw.Router())
	if err := a.seedIfEmpty(); err != nil {
		return nil, nil, err
	}
	return fw, a, nil
}

// errNotAdmin is what the plugin handlers return for an anonymous caller. The
// plugin maps a non-conflict error to HTTP 500 with an E_SAVE code; the write
// not happening is the part that matters.
var errNotAdmin = errors.New("blogapp: not signed in")

// savePostBody is richtext.WithSaveHandler: the editor's autosave lands here.
//
// The isAdmin check is the whole point. The plugin already ran its capability
// gate before calling this, and that gate passes for anonymous callers — it
// asks whether the PLUGIN holds document:write, not whether this REQUEST may
// use it. Without the line below, anyone who can reach the server could
// overwrite any post by POSTing its id.
func (a *app) savePostBody(ctx context.Context, req richtext.SaveRequest) error {
	if !isAdmin(ctx) {
		return errNotAdmin
	}
	if err := a.store.UpdateBody(req.DocID, req.DocJSON, req.Markdown); err != nil {
		if errors.Is(err, ErrNotFound) {
			// The post was deleted while an editor was open on it. Reported as
			// a conflict so the plugin answers 409 and the editor keeps the
			// document dirty and warns, instead of showing a saved state for a
			// write that went nowhere.
			return fmt.Errorf("post %s no longer exists: %w", req.DocID, richtext.ErrConflict)
		}
		return err
	}
	return nil
}

// uploadImage is richtext.WithUploadHandler: images dropped into the editor
// land here and come back as a URL the document embeds. Gated for the same
// reason savePostBody is — otherwise the endpoint is an open file host.
func (a *app) uploadImage(ctx context.Context, req richtext.UploadRequest) (richtext.UploadResult, error) {
	if !isAdmin(ctx) {
		return richtext.UploadResult{}, errNotAdmin
	}
	// The plugin already sniffed the bytes and rejected non-images before
	// calling this, so req.Type is the detected type rather than the caller's
	// claim.
	id, err := a.store.PutImage(&Image{
		Mime: req.Type,
		Name: req.Name,
		Data: base64.StdEncoding.EncodeToString(req.Bytes),
	})
	if err != nil {
		return richtext.UploadResult{}, err
	}
	return richtext.UploadResult{URL: "/uploads/" + id}, nil
}

// registerRoutes mounts everything that is a handler rather than a screen.
func (a *app) registerRoutes(rt interface {
	Get(string, http.Handler)
	Post(string, http.Handler)
}) {
	rt.Get("/feed.xml", http.HandlerFunc(a.handleFeed))
	rt.Get("/sitemap.xml", http.HandlerFunc(a.handleSitemap))
	rt.Get("/robots.txt", http.HandlerFunc(a.handleRobots))
	rt.Get("/uploads/{id}", http.HandlerFunc(a.handleImage))

	rt.Post("/admin/login/submit", http.HandlerFunc(a.handleLogin))
	rt.Get("/admin/logout", http.HandlerFunc(a.handleLogout))

	// Every admin write goes through requireAdmin. The GET screens are gated
	// separately, by the middleware in gateAdminScreens.
	rt.Post("/admin/posts/new", requireAdmin(http.HandlerFunc(a.handleNewPost)))
	rt.Post("/admin/posts/{id}", requireAdmin(http.HandlerFunc(a.handleSavePost)))
	rt.Post("/admin/posts/{id}/status", requireAdmin(http.HandlerFunc(a.handleSetStatus)))
	rt.Post("/admin/posts/{id}/delete", requireAdmin(http.HandlerFunc(a.handleDeletePost)))
}

// handleImage serves an uploaded image back out of the database.
func (a *app) handleImage(w http.ResponseWriter, r *http.Request) {
	img, err := a.store.Image(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		http.Error(w, "corrupt image", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", img.Mime)
	// nosniff matters here: these bytes came from an upload, and letting a
	// browser sniff a stored file into something executable is how an image
	// host becomes an XSS vector.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Write(raw)
}

// gateAdminScreens protects the host-rendered /admin GET pages. They are
// screens rather than handlers, so the UI host serves them and there is no
// route of this app's to wrap with requireAdmin — a path check in middleware is
// the way to reach them.
//
// It must be installed with fw.Use, after sessionMiddleware. Wrapping
// fw.Router() from main() instead puts it OUTSIDE the router's middleware
// chain, where the context has not been annotated yet, so isAdmin is false for
// everyone and a signed-in admin bounces back to the login forever.
func (a *app) gateAdminScreens(next http.Handler) http.Handler {
	// The paths that must stay reachable while signed out. Without /admin/login
	// on this list, the redirect target is itself gated and the browser loops.
	open := map[string]bool{
		"/admin/login":        true,
		"/admin/login/submit": true,
		"/admin/logout":       true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin") && !open[r.URL.Path] {
			requireAdmin(next).ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	db, err := openDB()
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	fw, a, err := newApp(db)
	if err != nil {
		log.Fatalf("newApp: %v", err)
	}

	addr := ":0"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	posts, _ := a.store.All()
	log.Printf("blogapp: %d posts in the database; sign in at /admin/login (password %q)",
		len(posts), adminPassword())
	fmt.Printf("blogapp listening on http://localhost:%d/\n", ln.Addr().(*net.TCPAddr).Port)
	if err := http.Serve(ln, fw.Router()); err != nil {
		log.Fatal(err)
	}
}
