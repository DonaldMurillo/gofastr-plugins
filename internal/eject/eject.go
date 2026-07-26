package eject

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/DonaldMurillo/gofastr-plugins/internal/registry"
)

// Options is one eject request: which plugin, into which repo, where under it,
// and what to include. The CLI fills it; the engine reads it.
type Options struct {
	// Plugin is the registry name to eject, e.g. "mermaid" (or "map" for the
	// geomap package — the registry name, not the directory).
	Plugin string
	// ProjectRoot is the absolute path of the consumer repo — the directory
	// holding go.mod. The lock lives here and vendored files live under it.
	ProjectRoot string
	// DestModule is the consumer's module path, read from its go.mod. It is the
	// import-path prefix the plugin's own packages rewrite to.
	DestModule string
	// DestDir is the repo-relative parent dir for vendored plugins, e.g.
	// "internal/plugins". A plugin named mermaid lands at DestDir/mermaid/.
	DestDir string
	// WithTests also vendors *_test.go. Off by default: the tests pull chromedp
	// into the consumer's go.mod, which is a real cost, and a consumer who
	// ejects owns the code and writes their own tests against it.
	WithTests bool
	// WithJS also vendors the js/ TypeScript sources. On by default (the CLI
	// inverts a --no-js flag into this bool): owning the prebuilt bundle means
	// owning its source, otherwise a consumer who edits the bundle has no way
	// to rebuild it.
	WithJS bool
	// Force overwrites files whose hash drifted from the recorded vendored hash
	// — i.e. files the user edited. Without it, Apply refuses those rather
	// than silently destroy the only copy of the user's work.
	Force bool
}

// Status is the per-file outcome BuildPlan classifies. The four values encode
// the safe-action matrix: Create and Update may proceed; Unchanged is a no-op;
// Conflict must stop the plan unless --force, because a file the user edited is
// the one thing overwriting would destroy.
type Status int

const (
	StatusCreate Status = iota
	StatusUpdate
	StatusUnchanged
	StatusConflict
)

// String renders a Status for human-facing output. The CLI prints these so a
// user can read a plan and tell at a glance what `add` will do.
func (s Status) String() string {
	switch s {
	case StatusCreate:
		return "create"
	case StatusUpdate:
		return "update"
	case StatusUnchanged:
		return "unchanged"
	case StatusConflict:
		return "conflict"
	}
	return "?"
}

// File is one file in a Plan: where it lands, where it came from, what the
// rewrite produced, and the status BuildPlan assigned it.
type File struct {
	// Rel is the dest path relative to ProjectRoot, with forward slashes — it
	// is the lock's key and stable across platforms.
	Rel string
	// Src is the upstream path inside the embedded tree, e.g.
	// "mermaid/plugin.go" or "richtext/ssr/render.go". Carried for diagnostics.
	Src string
	// Status is what Apply will do with this file.
	Status Status
	// Content is the post-rewrite bytes to write. For non-Go files it equals
	// the upstream bytes verbatim.
	Content []byte
	// Upstream is the pre-rewrite source bytes — the original the embedded tree
	// holds. Hashed into the lock's "upstream" slot so a later diff can tell an
	// upstream change from a local edit.
	Upstream []byte
}

// Plan is the set of file actions for ejecting one plugin, plus the metadata
// Apply records in the lock. BuildPlan constructs it; Apply executes it.
type Plan struct {
	Plugin  string // registry name
	Version string // plugin's own version, from the registry row
	Dir     string // repo-relative dest dir, e.g. "internal/plugins/mermaid"
	Files   []File
}

// sha256hex returns the "sha256:<hex>" digest used in the lock. The scheme
// prefix leaves room to swap the algorithm later without a lock-version bump
// breaking readers that only need to compare strings for equality.
func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// dirOf is filepath.Dir with a fallback for the rare case of a bare filename.
// Used by SaveLock to place the temp file next to its target.
func dirOf(p string) string {
	d := filepath.Dir(p)
	if d == "" {
		return "."
	}
	return d
}

// BuildPlan walks the embedded source for one plugin, rewrites its Go imports,
// and classifies each file against what is on disk and what the lock records.
// It does not write anything — that is Apply's job, and only after the caller
// (or a --dry-run human) has seen the plan.
//
// The src FS is parameterised so unit tests can feed a synthetic tree; the CLI
// passes gofastrplugins.Source(). The registry row p carries the plugin's name,
// version, and modulePath (from which the source directory is derived).
func BuildPlan(src fs.FS, p registry.Plugin, o Options, lock *Lock) (*Plan, error) {
	srcDir, err := srcDirOf(p.ModulePath)
	if err != nil {
		return nil, err
	}
	destImport := destImportPath(o, srcDir)
	planDir := path.Join(path.Clean(o.DestDir), srcDir)

	var lockPlugin *LockPlugin
	if lock != nil {
		lockPlugin = lock.Plugins[p.Name]
	}

	plan := &Plan{Plugin: p.Name, Version: p.Version, Dir: planDir}

	walkErr := fs.WalkDir(src, srcDir, func(p2 string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p2, srcDir+"/")
		base := d.Name()

		// Filter: test files only when WithTests. The embed captures them
		// unconditionally (so a --with-tests eject can reach them), but they
		// pull chromedp into the consumer's go.mod, so the default skips them.
		if !o.WithTests && strings.HasSuffix(base, "_test.go") {
			return nil
		}
		// Filter: js/ sources only when WithJS. The prebuilt bundle lives in
		// assets/ and is always copied; js/ is the TypeScript that produced it.
		if !o.WithJS && strings.HasPrefix(rel, "js/") {
			return nil
		}

		upstream, err := fs.ReadFile(src, p2)
		if err != nil {
			return fmt.Errorf("eject: reading %s: %w", p2, err)
		}

		content := upstream
		if strings.HasSuffix(base, ".go") {
			content, err = rewriteGo(upstream, srcDir, destImport, p2)
			if err != nil {
				return err
			}
		}

		rel = path.Join(planDir, rel) // forward-slash lock key
		status := classify(o, lockPlugin, rel, content)
		plan.Files = append(plan.Files, File{
			Rel:      rel,
			Src:      p2,
			Status:   status,
			Content:  content,
			Upstream: upstream,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if len(plan.Files) == 0 {
		return nil, fmt.Errorf("eject: plugin %s has no files under %s/ in the source tree", p.Name, srcDir)
	}
	if err := ensureNoGofastrPluginsRefs(plan.Files); err != nil {
		return nil, err
	}
	return plan, nil
}

// classify decides a file's Status from three inputs: whether it exists on
// disk, whether the lock knows it, and whether the on-disk bytes match the
// recorded vendored hash. The matrix:
//
//	no lock entry + no file on disk      → Create
//	lock entry + no file on disk         → Update (user deleted; safe to restore)
//	file on disk == recorded vendored    → Unchanged (new bytes identical) or
//	                                        Update (new bytes differ but disk is
//	                                        still what we last wrote)
//	file on disk exists, hash mismatch   → Conflict (user edited)
//	  OR no lock entry for a file on disk → Conflict (untracked file in the way)
func classify(o Options, lp *LockPlugin, rel string, content []byte) Status {
	diskHash, onDisk := diskFileHash(o, rel)
	vendored := sha256hex(content)

	var entry *FileHashes
	if lp != nil {
		entry = lp.Files[rel]
	}

	if !onDisk {
		if entry != nil {
			return StatusUpdate // was ejected, then deleted; re-create
		}
		return StatusCreate
	}
	// File exists on disk.
	if entry == nil {
		// Untracked file occupies a path the eject wants to write.
		return StatusConflict
	}
	if diskHash != entry.Vendored {
		// Disk drifted from what we last wrote → user edited it.
		return StatusConflict
	}
	// Disk is still our last write. Is the new write the same bytes?
	if diskHash == vendored {
		return StatusUnchanged
	}
	return StatusUpdate
}

// readDiskFile reads the file at ProjectRoot/rel. Used by Compare to render a
// diff against the user's current bytes; absence is an error the caller maps
// to DriftMissing rather than treating as "empty".
func readDiskFile(o Options, rel string) ([]byte, error) {
	full := filepath.Join(o.ProjectRoot, filepath.FromSlash(rel))
	return os.ReadFile(full)
}

// diskFileHash reads the file at ProjectRoot/rel and returns its sha256hex. The
// boolean is false when the file does not exist (a clean create) rather than an
// error — absence is a normal plan input.
func diskFileHash(o Options, rel string) (string, bool) {
	b, err := readDiskFile(o, rel)
	if err != nil {
		return "", false
	}
	return sha256hex(b), true
}

// Apply executes a plan: writes each file atomically, then rewrites the lock.
// It refuses the entire plan — not just the conflicting file — if any file is
// StatusConflict and Force is false. Refusing atomically is what keeps a
// half-applied update out of the working tree: the user runs `add`, it either
// lands every file or none, never "five of six and a conflict on the seventh".
func (plan *Plan) Apply(o Options, lock *Lock, cliVersion string) error {
	var conflicts []string
	for _, f := range plan.Files {
		if f.Status == StatusConflict {
			conflicts = append(conflicts, f.Rel)
		}
	}
	if len(conflicts) > 0 && !o.Force {
		return fmt.Errorf("eject: refusing to overwrite %d file(s) you have edited "+
			"(pass --force to clobber them):\n  %s",
			len(conflicts), strings.Join(conflicts, "\n  "))
	}

	for _, f := range plan.Files {
		if f.Status == StatusUnchanged {
			continue
		}
		if err := writeFileAtomic(o, f); err != nil {
			return err
		}
	}

	recordPlanInLock(lock, plan, o, cliVersion, time.Now().UTC().Format(time.RFC3339))
	lockPath := filepath.Join(o.ProjectRoot, LockFileName)
	if err := SaveLock(lockPath, lock); err != nil {
		return err
	}
	return nil
}

// writeFileAtomic writes one file: mkdir the parent, write a temp file next to
// the target, fsync it, then rename. A crash between writes leaves the prior
// file intact rather than a truncated one — the same guarantee SaveLock gives
// the lock itself, because a vendored source file is just as load-bearing.
func writeFileAtomic(o Options, f File) error {
	full := filepath.Join(o.ProjectRoot, filepath.FromSlash(f.Rel))
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("eject: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".gofastr-eject-*")
	if err != nil {
		return fmt.Errorf("eject: temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(f.Content); err != nil {
		tmp.Close()
		return fmt.Errorf("eject: writing %s: %w", f.Rel, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("eject: chmod %s: %w", f.Rel, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("eject: closing %s: %w", f.Rel, err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return fmt.Errorf("eject: renaming %s into place: %w", f.Rel, err)
	}
	return nil
}

// recordPlanInLock updates the in-memory lock to reflect a successful Apply:
// every file gets fresh upstream+vendored hashes, the plugin's options are
// recorded, and the provenance fields (ejectedFrom, ejectedAt) are stamped.
// SaveLock persists it; this helper only mutates.
func recordPlanInLock(lock *Lock, plan *Plan, o Options, cliVersion, ejectedAt string) {
	if lock == nil {
		return
	}
	if lock.Plugins == nil {
		lock.Plugins = map[string]*LockPlugin{}
	}
	lp := lock.Plugins[plan.Plugin]
	if lp == nil {
		lp = &LockPlugin{Files: map[string]*FileHashes{}}
		lock.Plugins[plan.Plugin] = lp
	}
	lp.Version = plan.Version
	lp.EjectedFrom = cliVersion
	lp.EjectedAt = ejectedAt
	lp.Dir = plan.Dir
	lp.WithTests = o.WithTests
	lp.WithJS = o.WithJS

	// Drop files the plan no longer carries (upstream removed them) so the lock
	// does not accumulate stale entries across an eject that deleted a file.
	for rel := range lp.Files {
		if !planHasFile(plan, rel) {
			delete(lp.Files, rel)
		}
	}
	for _, f := range plan.Files {
		lp.Files[f.Rel] = &FileHashes{
			Upstream: sha256hex(f.Upstream),
			Vendored: sha256hex(f.Content),
		}
	}
	lock.Source = SourceModulePath
	lock.Dir = path.Clean(o.DestDir)
	if lock.Version == "" {
		lock.Version = LockVersion
	}
}

func planHasFile(plan *Plan, rel string) bool {
	for _, f := range plan.Files {
		if f.Rel == rel {
			return true
		}
	}
	return false
}
