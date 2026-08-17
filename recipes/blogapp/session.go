package main

// The admin gate.
//
// This is a deliberately small stand-in, not production auth: one shared
// password, sessions in a map, no accounts, no rotation, no lockout. It exists
// so the recipe can show a REAL authorization boundary around the editor
// without turning into an auth tutorial. A real app deletes this file and wires
// battery/auth, which has accounts, password reset, OAuth, 2FA, and API tokens.
//
// What is NOT a stand-in is where the boundary sits. Read the comment on
// requireAdmin below — the plugin's capability gate does not authenticate, and
// getting that wrong is how an "isolated" editor ends up world-writable.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"os"
	"sync"
	"time"
)

const sessionCookie = "blogapp_session"

// sessionTTL bounds how long a login lasts. Short enough that a forgotten
// browser on a shared machine expires; long enough to write a post.
const sessionTTL = 12 * time.Hour

// ctxKey is unexported so nothing outside this package can plant an admin
// marker in a context it does not own.
type ctxKey struct{}

// sessions is the in-memory session set. A restart logs everyone out, which is
// the honest consequence of not persisting them and is fine for a demo.
type sessions struct {
	mu      sync.Mutex
	expires map[string]time.Time
}

func newSessions() *sessions { return &sessions{expires: map[string]time.Time{}} }

func (s *sessions) create() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("blogapp: crypto/rand unavailable: " + err.Error())
	}
	id := hex.EncodeToString(b[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistic sweep: without it the map grows for the life of the
	// process, one entry per login.
	now := time.Now()
	for k, exp := range s.expires {
		if now.After(exp) {
			delete(s.expires, k)
		}
	}
	s.expires[id] = now.Add(sessionTTL)
	return id
}

func (s *sessions) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.expires[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.expires, id)
		return false
	}
	return true
}

func (s *sessions) destroy(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.expires, id)
}

// adminPassword is the single credential. Default "demo" so `go run` works with
// no setup; the login page says so out loud rather than pretending otherwise.
func adminPassword() string {
	if v := os.Getenv("BLOG_ADMIN_PASSWORD"); v != "" {
		return v
	}
	return "demo"
}

// checkPassword compares in constant time. Overkill against a one-password demo
// and still the right habit to show in a recipe people copy.
func checkPassword(attempt string) bool {
	want := adminPassword()
	return subtle.ConstantTimeCompare([]byte(attempt), []byte(want)) == 1
}

// sessionMiddleware marks authenticated requests. It is installed with
// app.Use, so it runs for EVERY route — including the plugin's own
// /__gofastr/plugin/richtext/* endpoints, which is exactly why the save and
// upload handlers can gate on it.
//
// It only annotates; it never rejects. Rejection is requireAdmin's job, so that
// public pages can still ask "is an admin reading this?" to show an edit link.
func (a *app) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && a.sessions.valid(c.Value) {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

// isAdmin reports whether ctx belongs to a signed-in admin. Screens read it to
// decide what to render; the plugin handlers read it to decide whether to write.
func isAdmin(ctx context.Context) bool {
	ok, _ := ctx.Value(ctxKey{}).(bool)
	return ok
}

// requireAdmin gates the /admin routes. Anonymous GETs are redirected to the
// login page; anonymous writes get a bare 403, because a redirect in response
// to a POST is a confusing way to say "no".
//
// THE IMPORTANT PART, and the reason this recipe exists rather than copying
// example/: the rich text plugin's capability gate is not an authentication
// gate. Its chain is
//
//	pluginhost.Allow(ctx, granted, cap)
//	  = auth.ScopeMatch(granted, cap) && auth.HasScope(ctx, cap)
//
// and auth.HasScope returns TRUE when the context carries no token scopes —
// "session/JWT, unscoped by design". So an anonymous POST to the plugin's save
// endpoint passes it. The gate answers "does this plugin hold this capability",
// not "may this caller use it". The app has to answer the second question, and
// in this recipe that happens inside the save and upload handlers in main.go,
// which call isAdmin on the request context this middleware annotated.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAdmin(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.Redirect(w, r, "/admin/login?next="+r.URL.Path, http.StatusSeeOther)
			return
		}
		http.Error(w, "sign in first", http.StatusForbidden)
	})
}

// setSessionCookie writes the login cookie. Secure is conditional because a
// plaintext loopback dev server cannot round-trip a Secure cookie, and a demo
// that cannot log in locally teaches nobody anything.
func setSessionCookie(w http.ResponseWriter, r *http.Request, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(sessionTTL / time.Second),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
