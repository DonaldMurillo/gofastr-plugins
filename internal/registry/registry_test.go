package registry

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// repoRoot is where plugins.json lives and where the plugin packages sit.
const repoRoot = "../.."

func indexPath() string { return filepath.Join(repoRoot, "plugins.json") }

func mustLoad(t *testing.T) Index {
	t.Helper()
	idx, err := load(indexPath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return idx
}

func TestIndexParses(t *testing.T) {
	idx := mustLoad(t)
	if idx.RegistryVersion == "" {
		t.Error("registryVersion is empty")
	}
	if idx.Framework.ModulePath != "github.com/DonaldMurillo/gofastr" {
		t.Errorf("framework.modulePath = %q, want the gofastr module path", idx.Framework.ModulePath)
	}
	if len(idx.Plugins) == 0 {
		t.Fatal("no plugins in the index")
	}
}

// Every field a host's generated page needs must be present. A row missing one
// would render a blank cell rather than fail.
func TestPluginRowsAreComplete(t *testing.T) {
	for _, p := range mustLoad(t).Plugins {
		t.Run(p.Name, func(t *testing.T) {
			// Fields every row needs regardless of isolation.
			for _, f := range []struct{ name, val string }{
				{"name", p.Name},
				{"modulePath", p.ModulePath},
				{"version", p.Version},
				{"description", p.Description},
				{"isolation", p.Isolation},
				{"frameworkCompat", p.FrameworkCompat},
				{"routePrefix", p.RoutePrefix},
				{"docs", p.Docs},
			} {
				if strings.TrimSpace(f.val) == "" {
					t.Errorf("%s is empty", f.name)
				}
			}
			if len(p.Capabilities) == 0 {
				t.Error("capabilities is empty")
			}
			// An optional grant that is also always-on is a contradiction: a
			// reader would take it as "only if you opt in" for something the
			// plugin holds unconditionally, which understates its authority.
			always := make(map[string]bool, len(p.Capabilities))
			for _, c := range p.Capabilities {
				always[c] = true
			}
			for _, c := range p.OptionalCapabilities {
				if strings.TrimSpace(c) == "" {
					t.Error("optionalCapabilities contains an empty entry")
				}
				if always[c] {
					t.Errorf("optionalCapabilities repeats %q, which is already always-on", c)
				}
			}
			// Fields only a sandboxed (framed) plugin has: the entry document, its
			// interchange schema, and the sandbox tokens. A trusted host-page
			// plugin has none of these — it is not framed — so requiring them
			// would wrongly reject it.
			if p.Isolation == isolationSandboxOpaque {
				for _, f := range []struct{ name, val string }{
					{"entry", p.Entry},
					{"schema", p.Schema},
				} {
					if strings.TrimSpace(f.val) == "" {
						t.Errorf("%s is empty (required for a sandboxed plugin)", f.name)
					}
				}
				if len(p.Sandbox) == 0 {
					t.Error("sandbox is empty (required for a sandboxed plugin)")
				}
				if !strings.HasPrefix(p.Entry, p.RoutePrefix) {
					t.Errorf("entry %q is not under routePrefix %q", p.Entry, p.RoutePrefix)
				}
			}
		})
	}
}

const (
	isolationSandboxOpaque = "sandbox-iframe-opaque"
	isolationTrustedHost   = "trusted-host-page"
)

// The isolation invariant, mechanically enforced PER CATEGORY. The whole
// platform's security rests on this switch:
//   - a sandboxed plugin must stay opaque-origin: no "allow-same-origin" (which
//     alongside "allow-scripts" would collapse the opaque origin and hand the
//     frame the host's cookies + DOM), and it must NOT be marked trusted.
//   - a trusted-host-page plugin runs with full host access by design (the tour
//     plugin must reach host DOM), so it must be an EXPLICIT vouch (trusted=true)
//     and must declare NO sandbox — it is not framed, and a stray sandbox token
//     would misdescribe it.
//
// An unknown isolation value is rejected outright, so a typo can never slip a
// plugin past both arms of the switch into an unchecked state.
func TestIsolationInvariantsPerCategory(t *testing.T) {
	for _, p := range mustLoad(t).Plugins {
		switch p.Isolation {
		case isolationSandboxOpaque:
			if slices.Contains(p.Sandbox, "allow-same-origin") {
				t.Errorf("plugin %q requests allow-same-origin; that defeats the opaque-origin sandbox", p.Name)
			}
			if p.Trusted {
				t.Errorf("plugin %q is sandboxed but marked trusted; a sandboxed plugin is never trusted", p.Name)
			}
		case isolationTrustedHost:
			if !p.Trusted {
				t.Errorf("plugin %q is trusted-host-page but trusted!=true; a trusted plugin must be an explicit vouch", p.Name)
			}
			if len(p.Sandbox) != 0 {
				t.Errorf("plugin %q is trusted-host-page but declares sandbox %v; a trusted host-page plugin is not framed", p.Name, p.Sandbox)
			}
		default:
			t.Errorf("plugin %q has unknown isolation %q", p.Name, p.Isolation)
		}
	}
}

// A host vendors this file by copying it, so it must be valid, self-contained
// JSON — no comments, no trailing commas, nothing a stricter parser on the
// other side would choke on.
func TestIndexIsPlainValidJSON(t *testing.T) {
	src, err := os.ReadFile(indexPath())
	if err != nil {
		t.Fatalf("reading index: %v", err)
	}
	if !strings.HasSuffix(string(src), "\n") {
		t.Error("index should end with a newline")
	}
	if strings.Contains(string(src), "//") && !strings.Contains(string(src), "://") {
		t.Error("index appears to contain a // comment; JSON has none")
	}
}

// The stamp is applied when the asset is published, so the file in git must not
// carry one — a committed stamp would be a lie the moment it was committed.
func TestIndexInGitCarriesNoReleaseStamp(t *testing.T) {
	if r := mustLoad(t).Release; r != nil {
		t.Errorf("plugins.json in git has a release stamp (%+v); it is stamped at publish time, not committed", r)
	}
}

// Guards the release workflow's stamping step against this parser: the strict
// unknown-field check means a `release` key the structs did not know about
// would make the very asset we publish unreadable. Mirrors the jq filter in
// .github/workflows/release.yml.
func TestStampedIndexParses(t *testing.T) {
	src, err := os.ReadFile(indexPath())
	if err != nil {
		t.Fatalf("reading index: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(src, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw["release"] = map[string]string{
		"tag":       "v0.1.0",
		"commit":    "0123456789abcdef0123456789abcdef01234567",
		"published": "2026-07-16T12:00:00Z",
		"source":    "https://github.com/DonaldMurillo/gofastr-plugins",
	}
	stamped, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "plugins.json")
	if err := os.WriteFile(path, stamped, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	idx, err := load(path)
	if err != nil {
		t.Fatalf("a release-stamped index must parse, got: %v", err)
	}
	if idx.Release == nil || idx.Release.Tag != "v0.1.0" {
		t.Errorf("release stamp did not round-trip: %+v", idx.Release)
	}
	if len(idx.Plugins) == 0 {
		t.Error("stamping dropped the plugins")
	}
}

var (
	nameConstRe        = regexp.MustCompile(`(?m)^\s*Name\s*=\s*"([^"]+)"`)
	routePrefixConstRe = regexp.MustCompile(`(?m)^\s*RoutePrefix\s*=\s*"([^"]+)"`)
)

// scanRepoPlugins finds each plugin package by its declared identity consts,
// returning name -> routePrefix. Anchoring on the consts (rather than on, say,
// a pluginhost.Manifest literal) is deliberate: every plugin declares them,
// whereas richtext predates the platform extraction and builds its mount
// without a Manifest value.
func scanRepoPlugins(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		t.Fatalf("reading repo root: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repoRoot, e.Name(), "plugin.go"))
		if err != nil {
			continue // not a plugin package
		}
		name := nameConstRe.FindSubmatch(src)
		route := routePrefixConstRe.FindSubmatch(src)
		if name == nil || route == nil {
			continue
		}
		out[string(name[1])] = string(route[1])
	}
	return out
}

// Catches the "added a plugin, forgot the registry row" drift. The index is
// hand-maintained, so nothing else would notice it went stale.
func TestIndexCoversEveryPluginInRepo(t *testing.T) {
	repo := scanRepoPlugins(t)
	if len(repo) == 0 {
		t.Fatal("found no plugin packages; this guard would never bite")
	}

	found := slices.Collect(maps.Keys(repo))
	var listed []string
	for _, p := range mustLoad(t).Plugins {
		listed = append(listed, p.Name)
	}
	slices.Sort(found)
	slices.Sort(listed)
	if !slices.Equal(found, listed) {
		t.Fatalf("registry rows %v do not match plugin packages %v; update plugins.json", listed, found)
	}
}

// The index must not merely list the right plugins, it must describe them
// correctly: a routePrefix that drifts from the plugin's own const sends every
// generated page's link to a 404.
func TestIndexRoutePrefixMatchesPluginConst(t *testing.T) {
	repo := scanRepoPlugins(t)
	for _, p := range mustLoad(t).Plugins {
		want, ok := repo[p.Name]
		if !ok {
			continue // covered by TestIndexCoversEveryPluginInRepo
		}
		if p.RoutePrefix != want {
			t.Errorf("%s: registry routePrefix = %q, but the package const is %q", p.Name, p.RoutePrefix, want)
		}
	}
}
