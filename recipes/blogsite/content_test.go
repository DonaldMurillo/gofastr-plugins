package main

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// loadClock is the instant every test loads the corpus at. It is fixed rather
// than time.Now() for one reason: content/posts/scheduled-for-later.md is dated
// 2027-12-01, and a test that asserts it stays hidden must not start failing on
// 1 December 2027 for reasons nobody can see in the diff.
var loadClock = time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)

func loadTestSite(t *testing.T) *Site {
	t.Helper()
	site, err := Load(embeddedContent(t), loadClock)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return site
}

func TestLoadEmbeddedContent(t *testing.T) {
	site := loadTestSite(t)

	if len(site.Posts) == 0 {
		t.Fatal("no published posts loaded")
	}
	if len(site.Pages) == 0 {
		t.Fatal("no pages loaded")
	}

	// Newest first, with a total order — equal dates must not swap between
	// boots or the feed re-orders itself for no reason.
	for i := 1; i < len(site.Posts); i++ {
		prev, cur := site.Posts[i-1], site.Posts[i]
		if cur.Date.After(prev.Date) {
			t.Errorf("posts out of order: %s (%s) after %s (%s)",
				cur.Slug, cur.Date, prev.Slug, prev.Date)
		}
	}

	for _, p := range site.Posts {
		if p.Title == "" {
			t.Errorf("%s: empty title", p.Slug)
		}
		if p.Summary == "" {
			t.Errorf("%s: empty summary (frontmatter and first-paragraph fallback both failed)", p.Slug)
		}
		if p.Date.IsZero() {
			t.Errorf("%s: zero date", p.Slug)
		}
		if p.HTML == "" {
			t.Errorf("%s: empty rendered body", p.Slug)
		}
		if p.ReadingMinutes() < 1 {
			t.Errorf("%s: reading time %d, want at least 1", p.Slug, p.ReadingMinutes())
		}
	}
}

// The two hidden-post mechanisms mean different things and this is the test
// that keeps them distinct.
func TestDraftAndScheduledPostsAreHidden(t *testing.T) {
	site := loadTestSite(t)

	for _, tc := range []struct {
		slug     string
		wantWhy  string
		isDraft  bool
		isFuture bool
	}{
		{"tags-are-just-strings", "draft: true", true, false},
		{"scheduled-for-later", "date in the future", false, true},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			p, ok := site.PostBySlug(tc.slug)
			if !ok {
				t.Fatalf("fixture post %s is missing from content/posts", tc.slug)
			}
			if p.Draft != tc.isDraft {
				t.Errorf("Draft = %v, want %v", p.Draft, tc.isDraft)
			}
			if p.Future != tc.isFuture {
				t.Errorf("Future = %v, want %v", p.Future, tc.isFuture)
			}
			if p.Published() {
				t.Errorf("Published() = true, want false (%s)", tc.wantWhy)
			}
			for _, pub := range site.Posts {
				if pub.Slug == tc.slug {
					t.Errorf("appears in Site.Posts despite %s", tc.wantWhy)
				}
			}
			// Hidden posts must not leak into the tag facets either — a tag
			// page that counted them would list a post the reader cannot open.
			for _, tag := range p.Tags {
				posts, _, _ := site.PostsByTag(TagSlug(tag))
				for _, tp := range posts {
					if tp.Slug == tc.slug {
						t.Errorf("appears under tag %q", tag)
					}
				}
			}
			for _, r := range site.Search("the") {
				if r.Post.Slug == tc.slug {
					t.Error("appears in search results")
				}
			}
		})
	}
}

func TestNeighbourLinks(t *testing.T) {
	site := loadTestSite(t)
	posts := site.Posts

	if posts[0].Next != nil {
		t.Error("newest post has a Next; it should be nil")
	}
	if posts[len(posts)-1].Prev != nil {
		t.Error("oldest post has a Prev; it should be nil")
	}
	for i, p := range posts {
		if i > 0 && p.Next != posts[i-1] {
			t.Errorf("%s: Next = %v, want the newer neighbour %s", p.Slug, p.Next, posts[i-1].Slug)
		}
		if i < len(posts)-1 && p.Prev != posts[i+1] {
			t.Errorf("%s: Prev = %v, want the older neighbour %s", p.Slug, p.Prev, posts[i+1].Slug)
		}
	}
}

func TestTagIndex(t *testing.T) {
	site := loadTestSite(t)

	if len(site.Tags) == 0 {
		t.Fatal("no tags built")
	}
	// Busiest first, alphabetical within a count.
	for i := 1; i < len(site.Tags); i++ {
		a, b := site.Tags[i-1], site.Tags[i]
		if b.Count > a.Count {
			t.Errorf("tags out of order: %s(%d) before %s(%d)", a.Slug, a.Count, b.Slug, b.Count)
		}
		if a.Count == b.Count && a.Slug > b.Slug {
			t.Errorf("equal-count tags not alphabetical: %s before %s", a.Slug, b.Slug)
		}
	}
	// Every count must equal the length of the list it labels, or the tag
	// index promises a number the tag page then contradicts.
	for _, tag := range site.Tags {
		posts, label, ok := site.PostsByTag(tag.Slug)
		if !ok {
			t.Errorf("%s: in Tags but PostsByTag missed it", tag.Slug)
			continue
		}
		if len(posts) != tag.Count {
			t.Errorf("%s: Count = %d but PostsByTag returned %d", tag.Slug, tag.Count, len(posts))
		}
		if label != tag.Tag {
			t.Errorf("%s: label = %q, want %q", tag.Slug, label, tag.Tag)
		}
	}
}

func TestSearchRanking(t *testing.T) {
	site := loadTestSite(t)

	// "frontmatter" is in one title and several bodies, so the title match
	// must come first regardless of date.
	results := site.Search("Frontmatter")
	if len(results) == 0 {
		t.Fatal("no results for Frontmatter")
	}
	if got := results[0].Post.Slug; got != "frontmatter-reference" {
		t.Errorf("top hit = %s, want frontmatter-reference (title matches outrank body matches)", got)
	}
	for _, r := range results {
		if r.Snippet == "" {
			t.Errorf("%s: empty snippet", r.Post.Slug)
		}
	}

	if got := site.Search("  "); len(got) != 0 {
		t.Errorf("blank query returned %d results, want none", len(got))
	}
	if got := site.Search("zzzznotarealword"); len(got) != 0 {
		t.Errorf("miss returned %d results, want none", len(got))
	}
}

func TestRelatedPostsShareTags(t *testing.T) {
	site := loadTestSite(t)

	p, ok := site.PostBySlug("frontmatter-reference")
	if !ok {
		t.Fatal("fixture post missing")
	}
	related := site.Related(p, 3)
	if len(related) == 0 {
		t.Fatal("no related posts for a tagged post")
	}
	if len(related) > 3 {
		t.Errorf("Related returned %d, want at most the limit of 3", len(related))
	}
	want := map[string]bool{}
	for _, tag := range p.Tags {
		want[TagSlug(tag)] = true
	}
	for _, r := range related {
		if r.Slug == p.Slug {
			t.Error("Related included the post itself")
		}
		shared := false
		for _, tag := range r.Tags {
			if want[TagSlug(tag)] {
				shared = true
			}
		}
		if !shared {
			t.Errorf("%s shares no tag with %s", r.Slug, p.Slug)
		}
	}
}

// ─── Parsing rules, driven off synthetic fixtures ────────────────────

func TestLoadRejectsBadContent(t *testing.T) {
	good := "---\ndate: 2026-01-01\n---\n\n# Fine\n\nBody.\n"

	for _, tc := range []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name:    "missing date",
			files:   map[string]string{"posts/a.md": "# No date\n\nBody.\n"},
			wantErr: "missing `date:`",
		},
		{
			name:    "unparseable date",
			files:   map[string]string{"posts/a.md": "---\ndate: last tuesday\n---\n\n# T\n\nBody.\n"},
			wantErr: "unparseable date",
		},
		{
			name:    "no title",
			files:   map[string]string{"posts/a.md": "---\ndate: 2026-01-01\n---\n\nJust a body.\n"},
			wantErr: "no title",
		},
		{
			name: "duplicate slug",
			files: map[string]string{
				"posts/a.md": good,
				"posts/b.md": "---\ndate: 2026-01-02\nslug: a\n---\n\n# Other\n\nBody.\n",
			},
			wantErr: "duplicate slug",
		},
		{
			name:    "every post is a draft",
			files:   map[string]string{"posts/a.md": "---\ndate: 2026-01-01\ndraft: true\n---\n\n# T\n\nBody.\n"},
			wantErr: "no published posts",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for name, body := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(body)}
			}
			_, err := Load(fsys, loadClock)
			if err == nil {
				t.Fatalf("Load succeeded; want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestFrontmatterHandling(t *testing.T) {
	src := "---\n" +
		"date: 2026-01-02\n" +
		"slug: custom-slug\n" +
		"summary: An explicit summary.\n" +
		"author: Someone\n" +
		"tags: Go, go, Markdown ,, static site\n" +
		"---\n\n" +
		"# The title\n\n" +
		"First paragraph.\n"

	post, err := parsePost(src, "filename-stem", loadClock)
	if err != nil {
		t.Fatalf("parsePost: %v", err)
	}

	if post.Slug != "custom-slug" {
		t.Errorf("Slug = %q, want the frontmatter override", post.Slug)
	}
	if post.Title != "The title" {
		t.Errorf("Title = %q", post.Title)
	}
	if post.Summary != "An explicit summary." {
		t.Errorf("Summary = %q", post.Summary)
	}
	if post.Author != "Someone" {
		t.Errorf("Author = %q", post.Author)
	}
	// Case-insensitive de-dup, first casing wins, empties dropped.
	want := []string{"Go", "Markdown", "static site"}
	if len(post.Tags) != len(want) {
		t.Fatalf("Tags = %v, want %v", post.Tags, want)
	}
	for i, tag := range want {
		if post.Tags[i] != tag {
			t.Errorf("Tags[%d] = %q, want %q", i, post.Tags[i], tag)
		}
	}
	// Frontmatter must not reach the body, or "date:" would be counted as
	// prose and turn up in search snippets.
	if strings.Contains(post.Body, "date:") || strings.Contains(post.Body, "summary:") {
		t.Errorf("frontmatter leaked into Body: %q", post.Body)
	}
	// The H1 is page chrome, not body content — leaving it in would render
	// the title twice and put two h1 elements on the page.
	if strings.Contains(post.Body, "# The title") {
		t.Errorf("H1 left in Body: %q", post.Body)
	}
	if !strings.Contains(post.Body, "First paragraph.") {
		t.Errorf("body content lost: %q", post.Body)
	}
}

func TestSummaryFallsBackToFirstParagraph(t *testing.T) {
	src := "---\ndate: 2026-01-02\n---\n\n# T\n\n## A heading first\n\n" +
		"The real opening paragraph.\n"
	post, err := parsePost(src, "x", loadClock)
	if err != nil {
		t.Fatalf("parsePost: %v", err)
	}
	if post.Summary != "The real opening paragraph." {
		t.Errorf("Summary = %q, want the first non-heading block", post.Summary)
	}
}

func TestTagSlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Go", "go"},
		{"static site", "static-site"},
		{"  Spaced  Out  ", "spaced-out"},
		{"C++", "c"},
		{"Ünicode Tag", "ünicode-tag"},
		{"-leading-and-trailing-", "leading-and-trailing"},
		{"", ""},
	} {
		if got := TagSlug(tc.in); got != tc.want {
			t.Errorf("TagSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReadingMinutesRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		words int
		want  int
	}{
		{0, 1}, {1, 1}, {199, 1}, {200, 1}, {201, 2}, {400, 2}, {401, 3},
	} {
		p := &Post{Words: tc.words}
		if got := p.ReadingMinutes(); got != tc.want {
			t.Errorf("%d words: ReadingMinutes = %d, want %d", tc.words, got, tc.want)
		}
	}
}
