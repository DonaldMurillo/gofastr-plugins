package main

// Persistence. Plain database/sql over GoFastr's pure-Go SQLite engine, so this
// recipe adds no module dependency and needs no cgo toolchain to build.
//
// The canonical body is the editor's ProseMirror JSON (body_json). The markdown
// export (body_md) rides along because it is what search greps and what you
// would hand to anything that wants text — but it is lossy, and reconstructing
// a document from it would drop callouts, tables, and colors. Never treat it as
// the source of truth.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned by the lookups rather than sql.ErrNoRows, so callers
// branch on this package's vocabulary instead of the driver's.
var ErrNotFound = errors.New("blogapp: post not found")

// Status values. Kept as strings in one column rather than a separate table:
// there are two of them and there is no third coming.
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
)

// Post is one row of the posts table.
type Post struct {
	ID          string
	Title       string
	Slug        string
	Summary     string
	Tags        []string
	BodyJSON    string // canonical ProseMirror document
	BodyMD      string // lossy markdown export
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PublishedAt time.Time // zero while a draft
}

func (p *Post) Published() bool { return p.Status == StatusPublished }

// Date is what the public pages display: the publication date for a published
// post, and the last edit for a draft (which only the admin ever sees).
func (p *Post) Date() time.Time {
	if !p.PublishedAt.IsZero() {
		return p.PublishedAt
	}
	return p.UpdatedAt
}

// ReadingMinutes estimates reading time from the markdown export at 200 wpm.
func (p *Post) ReadingMinutes() int {
	words := len(strings.Fields(p.BodyMD))
	m := words / 200
	if words%200 != 0 {
		m++
	}
	if m < 1 {
		return 1
	}
	return m
}

// Store owns the schema and every query. It holds a *sql.DB and nothing else,
// so it is safe to share across handlers.
type Store struct{ db *sql.DB }

// NewStore opens the schema on db, creating the tables if they are absent.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS posts (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			slug TEXT NOT NULL UNIQUE,
			summary TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '',
			body_json TEXT NOT NULL DEFAULT '',
			body_md TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'draft',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			published_at TEXT NOT NULL DEFAULT ''
		)`,
		// Uploaded images. Bytes live as base64 TEXT rather than a BLOB so the
		// recipe stays on the portable subset of the engine; a real app puts
		// these in object storage and keeps only the URL here.
		`CREATE TABLE IF NOT EXISTS images (
			id TEXT PRIMARY KEY,
			mime TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			data TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("blogapp: migrate: %w", err)
		}
	}
	return nil
}

const postColumns = `id, title, slug, summary, tags, body_json, body_md, status, created_at, updated_at, published_at`

func scanPost(sc interface{ Scan(...any) error }) (*Post, error) {
	var (
		p                                 Post
		tags, created, updated, published string
	)
	if err := sc.Scan(&p.ID, &p.Title, &p.Slug, &p.Summary, &tags, &p.BodyJSON,
		&p.BodyMD, &p.Status, &created, &updated, &published); err != nil {
		return nil, err
	}
	p.Tags = splitTags(tags)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	p.PublishedAt = parseTime(published)
	return &p, nil
}

// Create inserts a post and returns it. Slug collisions are resolved by
// suffixing, so two posts titled "Untitled" get distinct URLs instead of the
// second insert failing on the UNIQUE constraint.
func (s *Store) Create(p *Post) (*Post, error) {
	now := time.Now().UTC()
	p.ID = newID()
	p.CreatedAt, p.UpdatedAt = now, now
	if p.Status == "" {
		p.Status = StatusDraft
	}
	slug, err := s.uniqueSlug(p.Slug, p.Title, "")
	if err != nil {
		return nil, err
	}
	p.Slug = slug

	_, err = s.db.Exec(
		`INSERT INTO posts (`+postColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Title, p.Slug, p.Summary, joinTags(p.Tags), p.BodyJSON, p.BodyMD,
		p.Status, fmtTime(p.CreatedAt), fmtTime(p.UpdatedAt), fmtTime(p.PublishedAt))
	if err != nil {
		return nil, fmt.Errorf("blogapp: create post: %w", err)
	}
	return p, nil
}

// Update writes the editable fields of an existing post. It does not touch
// body_json/body_md — UpdateBody owns those, because the editor autosaves them
// on a different path and at a different cadence than the metadata form.
func (s *Store) Update(p *Post) error {
	slug, err := s.uniqueSlug(p.Slug, p.Title, p.ID)
	if err != nil {
		return err
	}
	p.Slug = slug
	p.UpdatedAt = time.Now().UTC()

	_, err = s.db.Exec(
		`UPDATE posts SET title=?, slug=?, summary=?, tags=?, status=?, updated_at=?, published_at=? WHERE id=?`,
		p.Title, p.Slug, p.Summary, joinTags(p.Tags), p.Status,
		fmtTime(p.UpdatedAt), fmtTime(p.PublishedAt), p.ID)
	if err != nil {
		return fmt.Errorf("blogapp: update post: %w", err)
	}
	return nil
}

// UpdateBody persists the document. This is what the editor's autosave calls,
// via the plugin's save handler.
func (s *Store) UpdateBody(id, bodyJSON, bodyMD string) error {
	res, err := s.db.Exec(`UPDATE posts SET body_json=?, body_md=?, updated_at=? WHERE id=?`,
		bodyJSON, bodyMD, fmtTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("blogapp: update body: %w", err)
	}
	// A save for an id that no longer exists means the post was deleted while
	// an editor was open. Saying so lets the plugin surface a real error rather
	// than reporting success for a write that went nowhere.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus publishes or unpublishes. Publishing stamps published_at the first
// time only, so re-publishing an unpublished post keeps its original date and
// does not jump back to the top of the feed.
func (s *Store) SetStatus(id, status string) error {
	p, err := s.ByID(id)
	if err != nil {
		return err
	}
	p.Status = status
	if status == StatusPublished && p.PublishedAt.IsZero() {
		p.PublishedAt = time.Now().UTC()
	}
	return s.Update(p)
}

func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM posts WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("blogapp: delete post: %w", err)
	}
	return nil
}

func (s *Store) ByID(id string) (*Post, error) {
	row := s.db.QueryRow(`SELECT `+postColumns+` FROM posts WHERE id=?`, id)
	p, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blogapp: post by id: %w", err)
	}
	return p, nil
}

// BySlug returns a post whatever its status. Public handlers must check
// Published themselves — keeping drafts reachable here is what lets the admin
// preview one without a second query path.
func (s *Store) BySlug(slug string) (*Post, error) {
	row := s.db.QueryRow(`SELECT `+postColumns+` FROM posts WHERE slug=?`, slug)
	p, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blogapp: post by slug: %w", err)
	}
	return p, nil
}

// Published returns every published post, newest first.
func (s *Store) Published() ([]*Post, error) {
	return s.query(`SELECT `+postColumns+` FROM posts WHERE status=? ORDER BY published_at DESC, created_at DESC`, StatusPublished)
}

// All returns every post including drafts, newest-touched first. The admin list
// is the only caller.
func (s *Store) All() ([]*Post, error) {
	return s.query(`SELECT ` + postColumns + ` FROM posts ORDER BY updated_at DESC`)
}

func (s *Store) query(q string, args ...any) ([]*Post, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("blogapp: query posts: %w", err)
	}
	defer rows.Close()

	var out []*Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, fmt.Errorf("blogapp: scan post: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PublishedByTag filters the published set in Go rather than in SQL. Tags live
// as a comma-joined string, so a SQL LIKE would match "go" inside "golang";
// splitting and comparing is both correct and, at blog scale, free.
func (s *Store) PublishedByTag(tagSlug string) ([]*Post, error) {
	posts, err := s.Published()
	if err != nil {
		return nil, err
	}
	var out []*Post
	for _, p := range posts {
		for _, t := range p.Tags {
			if TagSlug(t) == tagSlug {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

// TagCount is one row of the tag index.
type TagCount struct {
	Tag   string
	Slug  string
	Count int
}

// Tags aggregates the published corpus into a tag facet, busiest first.
func (s *Store) Tags() ([]TagCount, error) {
	posts, err := s.Published()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	labels := map[string]string{}
	for _, p := range posts {
		for _, t := range p.Tags {
			slug := TagSlug(t)
			if slug == "" {
				continue
			}
			if _, ok := labels[slug]; !ok {
				labels[slug] = t
			}
			counts[slug]++
		}
	}
	out := make([]TagCount, 0, len(counts))
	for slug, n := range counts {
		out = append(out, TagCount{Tag: labels[slug], Slug: slug, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// Search scans the published corpus, ranking title above summary above body.
// Same shape as recipes/blogsite: a linear scan is the right answer until the
// corpus is large enough that it is not, and swapping in battery/search is a
// change to this one method.
func (s *Store) Search(query string) ([]*Post, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	posts, err := s.Published()
	if err != nil {
		return nil, err
	}
	type scored struct {
		post  *Post
		score int
	}
	var hits []scored
	for _, p := range posts {
		switch {
		case strings.Contains(strings.ToLower(p.Title), q):
			hits = append(hits, scored{p, 3})
		case strings.Contains(strings.ToLower(p.Summary), q):
			hits = append(hits, scored{p, 2})
		case strings.Contains(strings.ToLower(p.BodyMD), q):
			hits = append(hits, scored{p, 1})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]*Post, len(hits))
	for i, h := range hits {
		out[i] = h.post
	}
	return out, nil
}

// ─── Images ──────────────────────────────────────────────────────────

// Image is one uploaded file.
type Image struct {
	ID   string
	Mime string
	Name string
	Data string // base64
}

func (s *Store) PutImage(img *Image) (string, error) {
	img.ID = newID()
	_, err := s.db.Exec(`INSERT INTO images (id, mime, name, data, created_at) VALUES (?,?,?,?,?)`,
		img.ID, img.Mime, img.Name, img.Data, fmtTime(time.Now().UTC()))
	if err != nil {
		return "", fmt.Errorf("blogapp: put image: %w", err)
	}
	return img.ID, nil
}

func (s *Store) Image(id string) (*Image, error) {
	var img Image
	err := s.db.QueryRow(`SELECT id, mime, name, data FROM images WHERE id=?`, id).
		Scan(&img.ID, &img.Mime, &img.Name, &img.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blogapp: image: %w", err)
	}
	return &img, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────

// uniqueSlug derives a slug from an explicit value or the title, then suffixes
// "-2", "-3", … until it is free. excludeID lets an update keep its own slug.
func (s *Store) uniqueSlug(explicit, title, excludeID string) (string, error) {
	base := TagSlug(explicit)
	if base == "" {
		base = TagSlug(title)
	}
	if base == "" {
		base = "post"
	}
	candidate := base
	for n := 2; ; n++ {
		var owner string
		err := s.db.QueryRow(`SELECT id FROM posts WHERE slug=?`, candidate).Scan(&owner)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return candidate, nil
		case err != nil:
			return "", fmt.Errorf("blogapp: checking slug: %w", err)
		case owner == excludeID:
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
}

// newID is a random 128-bit hex id. Random rather than sequential because the
// id is the editor's DocID and shows up in admin URLs; a guessable counter
// invites poking at neighbouring posts.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not a condition this app can sensibly
		// continue through — every new post would collide.
		panic("blogapp: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func splitTags(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if slug := TagSlug(t); slug != "" && !seen[slug] {
			seen[slug] = true
			out = append(out, t)
		}
	}
	return out
}

func joinTags(tags []string) string { return strings.Join(tags, ", ") }

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
