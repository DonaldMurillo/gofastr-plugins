package eject

import (
	"encoding/json"
	"fmt"
	"os"
)

// LockFileName is the consumer-visible lock file the eject workflow reads and
// writes at the root of the consumer's repo. It records exactly what was
// ejected, where, and what each file's bytes hashed to — the pair of hashes
// that lets a later `gofastr-plugin diff` tell a local edit from an upstream
// change without re-running the eject.
const LockFileName = "gofastr-plugins.json"

// LockVersion is the schema version of the lock file's SHAPE. Bump only on a
// breaking change to the fields below; a vendored lock from an older CLI keeps
// parsing under the same value.
const LockVersion = "1"

// Lock is the whole eject lock: the source repo each plugin came from, the
// parent dir vendored plugins live under, and one entry per ejected plugin.
type Lock struct {
	Version string                 `json:"version"`
	Source  string                 `json:"source"`
	Dir     string                 `json:"dir"`
	Plugins map[string]*LockPlugin `json:"plugins"`
}

// LockPlugin is one ejected plugin's record: its version, where it landed, the
// options it was ejected with, and a per-file hash pair.
type LockPlugin struct {
	Version     string                 `json:"version"`
	EjectedFrom string                 `json:"ejectedFrom"`
	EjectedAt   string                 `json:"ejectedAt"`
	Dir         string                 `json:"dir"`
	WithTests   bool                   `json:"withTests"`
	WithJS      bool                   `json:"withJS"`
	Files       map[string]*FileHashes `json:"files"`
}

// FileHashes carries the two hashes that make drift classification possible:
//
//   - Vendored is what we WROTE (post-rewrite). A mismatch between this and the
//     file currently on disk means the USER edited the file — the one case
//     Apply must refuse without --force, because clobbering it would destroy
//     the only copy of work the user did.
//   - Upstream is the pre-rewrite source bytes (what the embedded tree held at
//     eject time). A mismatch between this and the current embedded copy means
//     UPSTREAM moved under us.
//
// With both, `diff` can distinguish "you edited" from "they shipped a new
// version" from "both" — and do it without re-running the original eject.
type FileHashes struct {
	Upstream string `json:"upstream"`
	Vendored string `json:"vendored"`
}

// LoadLock reads the lock at path. A missing lock is not an error: it returns a
// fresh, empty Lock so the first eject starts from a clean slate. Malformed
// JSON is an error; the lock is ours, but a truncated write is still possible,
// and silently treating it as empty would mask a half-written file.
//
// The decoder is deliberately lenient about unknown fields: a future CLI that
// adds a key must still read locks written by this one (and vice-versa), so we
// do not impose the registry's strictness here.
func LoadLock(path string) (*Lock, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Lock{Version: LockVersion, Plugins: map[string]*LockPlugin{}}, nil
		}
		return nil, fmt.Errorf("eject: reading lock %s: %w", path, err)
	}
	var l Lock
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("eject: parsing lock %s: %w", path, err)
	}
	if l.Plugins == nil {
		l.Plugins = map[string]*LockPlugin{}
	}
	if l.Version == "" {
		l.Version = LockVersion
	}
	return &l, nil
}

// SaveLock writes the lock atomically: a temp file in the same dir, then a
// rename, so a crash mid-write leaves the previous lock intact rather than a
// truncated one. Keys are sorted (encoding/json sorts map keys) and a trailing
// newline is appended so the file is diff-stable across runs and across
// machines — a re-eject that changes nothing should produce a byte-identical
// lock, which makes code review on a lock commit tractable.
func SaveLock(path string, l *Lock) error {
	if l.Plugins == nil {
		l.Plugins = map[string]*LockPlugin{}
	}
	for _, p := range l.Plugins {
		if p.Files == nil {
			p.Files = map[string]*FileHashes{}
		}
	}
	out, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("eject: marshalling lock: %w", err)
	}
	out = append(out, '\n')
	dir := dirOf(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("eject: creating lock dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".gofastr-plugins-*.json.tmp")
	if err != nil {
		return fmt.Errorf("eject: creating lock temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if any step after this fails; once Rename succeeds
	// the temp name is gone and Remove is a no-op.
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("eject: writing lock temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("eject: chmod lock temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("eject: closing lock temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("eject: renaming lock into place: %w", err)
	}
	return nil
}
