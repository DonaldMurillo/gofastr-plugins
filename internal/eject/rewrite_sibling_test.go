package eject

import (
	"strings"
	"testing"
)

// TestRewriteDoesNotSwallowNameSharingSiblings pins the anchoring in rewriteGo.
//
// The rewrite has to catch a plugin's own subpackages (richtext imports
// richtext/ssr, and the vendored copy must point at the vendored ssr). The
// tempting way to do that is to replace the bare prefix
// `"…/gofastr-plugins/richtext` and let the closing quote or the `/ssr` suffix
// ride along untouched. That also matches `richtext2` and `richtextlite`.
//
// The failure it would cause is quiet, which is why it is worth a test: the bad
// rewrite produces `<dest>/richtext2`, a path that reads as correct. The
// gofastr-plugins prefix is gone, so ensureNoGofastrPluginsRefs sees nothing
// wrong, and the plan is written out. The consumer gets a package importing a
// directory that was never vendored, and finds out at build time with an error
// pointing at a path they never typed.
func TestRewriteDoesNotSwallowNameSharingSiblings(t *testing.T) {
	src := []byte(`package richtext

import (
	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr-plugins/richtext/ssr"
	"github.com/DonaldMurillo/gofastr-plugins/richtext2"
	"github.com/DonaldMurillo/gofastr-plugins/richtextlite/deep"
)

var _ = pluginhost.DefaultSandbox
var _ = ssr.Render
var _ = richtext2.X
var _ = deep.Y
`)

	got, err := rewriteGo(src, "richtext", "app/internal/plugins/richtext", "richtext/plugin.go")
	if err != nil {
		t.Fatalf("rewriteGo: %v", err)
	}
	out := string(got)

	// The plugin's own subpackage must be rewired to the vendored copy.
	if !strings.Contains(out, `"app/internal/plugins/richtext/ssr"`) {
		t.Errorf("own subpackage was not rewritten to the vendored path:\n%s", out)
	}
	// The alias always resolves to the framework package.
	if !strings.Contains(out, `"github.com/DonaldMurillo/gofastr/framework/pluginhost"`) {
		t.Errorf("pluginhost alias was not rewritten to the framework path:\n%s", out)
	}
	// Siblings that merely share a name prefix must be left exactly as they
	// were, so ensureNoGofastrPluginsRefs can then fail the plan loudly rather
	// than shipping a copy that reaches back into this repo.
	for _, untouched := range []string{
		`"github.com/DonaldMurillo/gofastr-plugins/richtext2"`,
		`"github.com/DonaldMurillo/gofastr-plugins/richtextlite/deep"`,
	} {
		if !strings.Contains(out, untouched) {
			t.Errorf("name-sharing sibling %s was rewritten; the match is not anchored:\n%s", untouched, out)
		}
	}

	// And the guard must then refuse the plan, rather than let the unrewritten
	// sibling ship. This is the pairing that matters: anchoring keeps the
	// rewrite honest, the guard turns what it could not handle into a failure.
	err = ensureNoGofastrPluginsRefs([]File{{Src: "richtext/plugin.go", Rel: "x/plugin.go", Content: got}})
	if err == nil {
		t.Fatal("ensureNoGofastrPluginsRefs accepted a file still importing gofastr-plugins")
	}
	if !strings.Contains(err.Error(), "x/plugin.go") {
		t.Errorf("guard error should name the offending file, got: %v", err)
	}
}
