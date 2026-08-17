package main

// The content layer: markdown files on disk in, an immutable in-memory index
// out. Everything the screens need — ordering, tag facets, prev/next, related
// posts, search — is computed once at boot by [Load] and only read afterwards,
// which is what makes this recipe "static": a request never parses markdown,
// touches the filesystem, or queries anything.
//
// Fail loud at boot. A post with an unparseable date or a duplicate slug is a
// content bug, and the moment to surface it is `go run`, not the first request
// that happens to hit the broken page.

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/DonaldMurillo/gofastr/core/markdown"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"
)

// wordsPerMinute is the reading-speed constant behind Post.ReadingMinutes.
// 200 wpm is the conventional figure for prose on screen.
const wordsPerMinute = 200

// Post is one article, fully rendered at load time.
type Post struct {
	Slug    string
	Title   string
	Summary string
	Author  string
	Date    time.Time
	Tags    []string
	Cover   string // optional image URL used as the card/OG image

	// Draft is the frontmatter flag. Future is derived: a post dated after
	// the load time is scheduled, not published. Both keep a post out of
	// every public listing, but only Draft is author intent — Future
	// resolves itself on the next boot, which is the whole point of being
	// able to write ahead.
	Draft  bool
	Future bool

	HTML  render.HTML // rendered body
	Body  string      // raw markdown body, kept for search and excerpting
	Words int

	// Prev/Next are chronological neighbours within the published set,
	// wired after sorting. Next is the newer post.
	Prev *Post
	Next *Post
}

// Published reports whether the post belongs in public listings and feeds.
func (p *Post) Published() bool { return !p.Draft && !p.Future }

// ReadingMinutes is the rounded-up estimate shown on cards and post headers.
func (p *Post) ReadingMinutes() int {
	m := p.Words / wordsPerMinute
	if p.Words%wordsPerMinute != 0 {
		m++
	}
	if m < 1 {
		return 1
	}
	return m
}

// Page is a standalone markdown page (about, colophon) that carries no date
// and never appears in listings, feeds, or tag facets.
type Page struct {
	Slug  string
	Title string
	Menu  string // optional nav label; empty keeps the page out of the header
	Order int    // nav sort key
	HTML  render.HTML
}

// TagCount is one row of the tag index.
type TagCount struct {
	Tag   string
	Slug  string
	Count int
}

// Site is the whole loaded corpus. Treat it as immutable after Load.
type Site struct {
	// Posts is every published post, newest first. Drafts and scheduled
	// posts are reachable only through All and BySlug — this is the slice
	// every public listing iterates.
	Posts []*Post
	All   []*Post
	Pages []*Page
	Tags  []TagCount

	bySlug     map[string]*Post
	pageBySlug map[string]*Page
	byTag      map[string][]*Post
	tagLabel   map[string]string
}

// PostBySlug returns a post regardless of publication state. Callers that
// serve public routes must check Published themselves — the index deliberately
// does not hide drafts here so a preview route can exist without a second map.
func (s *Site) PostBySlug(slug string) (*Post, bool) {
	p, ok := s.bySlug[slug]
	return p, ok
}

// PageBySlug returns a standalone page.
func (s *Site) PageBySlug(slug string) (*Page, bool) {
	p, ok := s.pageBySlug[slug]
	return p, ok
}

// PostsByTag returns the published posts carrying tagSlug, newest first, plus
// the tag's display label (tags are matched case-insensitively but rendered in
// the casing the content used).
func (s *Site) PostsByTag(tagSlug string) ([]*Post, string, bool) {
	posts, ok := s.byTag[tagSlug]
	if !ok {
		return nil, "", false
	}
	return posts, s.tagLabel[tagSlug], true
}

// Related returns up to limit published posts sharing the most tags with p,
// excluding p itself. Ties break toward the newer post, so an unrelated corpus
// still yields a sensible "read next" rather than nothing.
func (s *Site) Related(p *Post, limit int) []*Post {
	if limit <= 0 {
		return nil
	}
	want := make(map[string]bool, len(p.Tags))
	for _, t := range p.Tags {
		want[TagSlug(t)] = true
	}

	type scored struct {
		post  *Post
		score int
	}
	var candidates []scored
	for _, other := range s.Posts {
		if other.Slug == p.Slug {
			continue
		}
		score := 0
		for _, t := range other.Tags {
			if want[TagSlug(t)] {
				score++
			}
		}
		if score > 0 {
			candidates = append(candidates, scored{other, score})
		}
	}
	// s.Posts is already newest-first, so a stable sort on score alone
	// leaves recency as the tiebreak for free.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	out := make([]*Post, 0, limit)
	for _, c := range candidates {
		if len(out) == limit {
			break
		}
		out = append(out, c.post)
	}
	return out
}

// SearchResult is one hit from [Site.Search].
type SearchResult struct {
	Post    *Post
	Snippet string // body text around the first match, for the results list
}

// Search runs a case-insensitive substring query over the published corpus and
// ranks title matches above summary matches above body matches. It is
// deliberately a linear scan: a blog is a few hundred documents, and an index
// nobody can debug is worse than a loop everybody can. Reach for
// battery/search when the corpus outgrows this.
func (s *Site) Search(query string) []SearchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}

	type scored struct {
		res   SearchResult
		score int
	}
	var hits []scored
	for _, p := range s.Posts {
		score := 0
		switch {
		case strings.Contains(strings.ToLower(p.Title), q):
			score = 3
		case strings.Contains(strings.ToLower(p.Summary), q):
			score = 2
		case strings.Contains(strings.ToLower(p.Body), q):
			score = 1
		}
		if score == 0 {
			continue
		}
		hits = append(hits, scored{SearchResult{Post: p, Snippet: snippet(p, q)}, score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })

	out := make([]SearchResult, len(hits))
	for i, h := range hits {
		out[i] = h.res
	}
	return out
}

// snippet returns ~200 characters of body text centred on the first occurrence
// of q, with ellipses where it cut. Falls back to the summary when the match
// was in the title only.
func snippet(p *Post, q string) string {
	body := plainText(p.Body)
	idx := strings.Index(strings.ToLower(body), q)
	if idx < 0 {
		if p.Summary != "" {
			return p.Summary
		}
		return truncate(body, 200)
	}
	const window = 100
	start := idx - window
	if start < 0 {
		start = 0
	}
	end := idx + len(q) + window
	if end > len(body) {
		end = len(body)
	}
	out := body[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(body) {
		out += "…"
	}
	return out
}

// ─── Loading ─────────────────────────────────────────────────────────

// Load reads posts/ and pages/ from fsys and returns the built index. now
// decides which dated posts count as scheduled; pass the real clock in main
// and a fixed instant in tests so a post dated "tomorrow" stays scheduled
// forever rather than publishing itself the day the test is run.
func Load(fsys fs.FS, now time.Time) (*Site, error) {
	site := &Site{
		bySlug:     map[string]*Post{},
		pageBySlug: map[string]*Page{},
		byTag:      map[string][]*Post{},
		tagLabel:   map[string]string{},
	}

	if err := loadPosts(fsys, now, site); err != nil {
		return nil, err
	}
	if err := loadPages(fsys, site); err != nil {
		return nil, err
	}

	// Newest first. Equal dates fall back to slug so the order is total —
	// two posts written the same day must not swap places between boots,
	// or the feed re-orders itself for no reason.
	sort.Slice(site.All, func(i, j int) bool {
		a, b := site.All[i], site.All[j]
		if a.Date.Equal(b.Date) {
			return a.Slug < b.Slug
		}
		return a.Date.After(b.Date)
	})
	for _, p := range site.All {
		if p.Published() {
			site.Posts = append(site.Posts, p)
		}
	}

	linkNeighbours(site.Posts)
	buildTagIndex(site)

	sort.Slice(site.Pages, func(i, j int) bool {
		if site.Pages[i].Order != site.Pages[j].Order {
			return site.Pages[i].Order < site.Pages[j].Order
		}
		return site.Pages[i].Slug < site.Pages[j].Slug
	})

	if len(site.Posts) == 0 {
		return nil, fmt.Errorf("blogsite: no published posts found; content/posts is empty or every post is a draft")
	}
	return site, nil
}

func loadPosts(fsys fs.FS, now time.Time, site *Site) error {
	entries, err := fs.ReadDir(fsys, "posts")
	if err != nil {
		return fmt.Errorf("blogsite: reading posts/: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := path.Join("posts", e.Name())
		src, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("blogsite: reading %s: %w", name, err)
		}
		post, err := parsePost(string(src), strings.TrimSuffix(e.Name(), ".md"), now)
		if err != nil {
			return fmt.Errorf("blogsite: %s: %w", name, err)
		}
		if _, dup := site.bySlug[post.Slug]; dup {
			return fmt.Errorf("blogsite: %s: duplicate slug %q", name, post.Slug)
		}
		site.bySlug[post.Slug] = post
		site.All = append(site.All, post)
	}
	return nil
}

func loadPages(fsys fs.FS, site *Site) error {
	entries, err := fs.ReadDir(fsys, "pages")
	if err != nil {
		// pages/ is optional — a blog with only posts is a complete blog.
		if _, statErr := fs.Stat(fsys, "pages"); statErr != nil {
			return nil
		}
		return fmt.Errorf("blogsite: reading pages/: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := path.Join("pages", e.Name())
		src, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("blogsite: reading %s: %w", name, err)
		}
		doc := markdown.Render(string(src))
		slug := strings.TrimSuffix(e.Name(), ".md")
		if v := doc.Frontmatter["slug"]; v != "" {
			slug = v
		}
		if doc.Title == "" {
			return fmt.Errorf("blogsite: %s: no title (add an H1 or a `title:` frontmatter key)", name)
		}
		if _, dup := site.pageBySlug[slug]; dup {
			return fmt.Errorf("blogsite: %s: duplicate slug %q", name, slug)
		}
		page := &Page{
			Slug:  slug,
			Title: doc.Title,
			Menu:  doc.Frontmatter["menu"],
			Order: atoi(doc.Frontmatter["order"]),
			HTML:  renderProse(stripLeadingH1(stripFrontmatter(string(src)))),
		}
		site.pageBySlug[slug] = page
		site.Pages = append(site.Pages, page)
	}
	return nil
}

// parsePost turns one markdown file into a Post. defaultSlug is the filename
// stem, used when frontmatter does not override it.
func parsePost(src, defaultSlug string, now time.Time) (*Post, error) {
	doc := markdown.Render(src)
	fm := doc.Frontmatter

	if doc.Title == "" {
		return nil, fmt.Errorf("no title (add an H1 or a `title:` frontmatter key)")
	}
	date, err := parseDate(fm["date"])
	if err != nil {
		return nil, err
	}

	slug := defaultSlug
	if v := fm["slug"]; v != "" {
		slug = v
	}

	// The H1 comes back out of the body: the post page renders the title in a
	// ui.PageHeader, and leaving it in the prose would print it twice and put
	// two h1 elements on the page.
	body := stripLeadingH1(stripFrontmatter(src))
	post := &Post{
		Slug:    slug,
		Title:   doc.Title,
		Summary: fm["summary"],
		Author:  fm["author"],
		Date:    date,
		Tags:    parseTags(fm["tags"]),
		Cover:   fm["cover"],
		Draft:   fm["draft"] == "true",
		Future:  date.After(now),
		HTML:    renderProse(body),
		Body:    body,
		Words:   countWords(body),
	}
	if post.Summary == "" {
		post.Summary = firstParagraph(body)
	}
	return post, nil
}

// parseDate accepts a plain date or a full RFC 3339 timestamp. A missing or
// malformed date is an error rather than a zero value: an undated post would
// silently sort to the bottom of every listing and carry a 1970 <pubDate> into
// the feed, which readers cache.
func parseDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("missing `date:` frontmatter key")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable date %q (want 2006-01-02 or RFC 3339)", raw)
}

// parseTags splits the comma-separated `tags:` value. The frontmatter parser
// is key/value only — it has no list syntax — so a comma-separated string is
// the convention this recipe adopts rather than a limitation it works around.
func parseTags(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if key := TagSlug(t); !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	return out
}

func linkNeighbours(posts []*Post) {
	for i, p := range posts {
		if i > 0 {
			p.Next = posts[i-1] // the list is newest-first
		}
		if i < len(posts)-1 {
			p.Prev = posts[i+1]
		}
	}
}

func buildTagIndex(site *Site) {
	for _, p := range site.Posts {
		for _, t := range p.Tags {
			key := TagSlug(t)
			if _, ok := site.tagLabel[key]; !ok {
				site.tagLabel[key] = t
			}
			site.byTag[key] = append(site.byTag[key], p)
		}
	}
	for key, posts := range site.byTag {
		site.Tags = append(site.Tags, TagCount{Tag: site.tagLabel[key], Slug: key, Count: len(posts)})
	}
	// Busiest tag first, alphabetical within a count — a stable order the
	// tag page and the header chip row can both rely on.
	sort.Slice(site.Tags, func(i, j int) bool {
		if site.Tags[i].Count != site.Tags[j].Count {
			return site.Tags[i].Count > site.Tags[j].Count
		}
		return site.Tags[i].Slug < site.Tags[j].Slug
	})
}

// ─── Text helpers ────────────────────────────────────────────────────

// TagSlug normalizes a tag for URLs and for matching. Exported because the
// screens build tag hrefs from raw labels.
func TagSlug(tag string) string {
	var b strings.Builder
	lastDash := true // suppresses a leading dash
	for _, r := range strings.ToLower(strings.TrimSpace(tag)) {
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

// renderProse turns markdown into the themed prose block the screens embed.
//
// It runs at LOAD time, not render time. ui.Markdown parses the source and
// upgrades every fenced block into a highlighted ui.CodeBlock with a copy
// button, which is real work to repeat on every request for output that can
// never change. The component's style marker (data-fui-comp) travels inside
// the returned HTML, so the host still finds it when scanning the page for
// which stylesheets to link — pre-rendering costs nothing on that side.
func renderProse(body string) render.HTML {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return ui.Markdown(ui.MarkdownConfig{Source: body})
}

// stripLeadingH1 drops the first `# …` line and any blank lines after it. The
// title is rendered as page chrome instead, and a body that repeats it would
// give the page two h1 elements.
func stripLeadingH1(body string) string {
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[i]), "# ") {
		return body
	}
	i++
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	return strings.Join(lines[i:], "\n")
}

// stripFrontmatter removes a leading `--- … ---` block so word counts, search,
// and excerpts never see `title:` or `tags:` as prose.
func stripFrontmatter(src string) string {
	lines := strings.Split(src, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return src
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return src
}

// firstParagraph is the excerpt fallback: the first non-heading, non-fence
// block of the body, flattened to plain text and capped.
func firstParagraph(body string) string {
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, "#") || strings.HasPrefix(block, "```") ||
			strings.HasPrefix(block, ">") || strings.HasPrefix(block, "|") {
			continue
		}
		return truncate(plainText(block), 200)
	}
	return ""
}

// plainText strips the markdown syntax that would otherwise leak into search
// snippets and excerpts. It is intentionally shallow — it serves summaries,
// not fidelity.
func plainText(md string) string {
	repl := strings.NewReplacer(
		"**", "", "__", "", "`", "", "*", "", "_", "",
		"\n", " ", "\r", " ", "#", "",
	)
	return strings.Join(strings.Fields(repl.Replace(md)), " ")
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cut := string(runes[:max])
	if i := strings.LastIndexByte(cut, ' '); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

func countWords(body string) int {
	return len(strings.Fields(plainText(body)))
}

func atoi(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
