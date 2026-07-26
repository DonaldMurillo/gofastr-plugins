// Package registry defines the schema of the curated plugin index and keeps it
// honest.
//
// The index itself is plugins.json at the repo root. It is consumed by COPY,
// not by import: a host — GoFastr's docs site first — fetches the file from a
// release (or raw URL) and vendors it, then parses it on its own terms. That is
// why nothing here is exported for outside use and why this package lives under
// internal/: the published artifact is the JSON file, not a Go API.
//
// What this package IS for is the drift the JSON cannot catch by itself. The
// index is hand-maintained, so a row can silently fall out of step with the
// plugin it claims to describe. The tests here fail loudly instead:
//
//   - every plugin package in the repo has exactly one row, and vice versa
//   - each row's routePrefix equals that package's own RoutePrefix const
//   - no row requests allow-same-origin (which would collapse the opaque origin)
//   - the parse rejects unknown fields, so a new JSON key must be added to the
//     structs below in the same change rather than being quietly ignored
//
// Consumers parse the JSON themselves, so the structs here are a description of
// the contract, not the thing enforcing it on their end. Treat a change to them
// as a change to the published schema: bump registryVersion on a breaking one,
// since a vendored copy elsewhere is now out of date.
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Index is the whole curated registry: the framework it targets, plus every
// plugin in it.
type Index struct {
	// RegistryVersion is the schema version of the file's SHAPE, bumped only on
	// a breaking change to the field layout. It is not a release version.
	RegistryVersion string `json:"registryVersion"`
	// Comment is the "$comment" preamble — editorial context for humans reading
	// the raw JSON.
	Comment     string    `json:"$comment,omitempty"`
	Description string    `json:"description"`
	Framework   Framework `json:"framework"`
	Plugins     []Plugin  `json:"plugins"`
	// Release identifies the release a copy came from. It is ABSENT from the
	// file in git and stamped in by .github/workflows/release.yml as the asset
	// is published — so a vendored copy says what it is, rather than depending
	// on whoever fetched it to write that down.
	Release *Release `json:"release,omitempty"`
}

// Release is the provenance stamp on a published copy of the index.
type Release struct {
	Tag       string `json:"tag"`
	Commit    string `json:"commit"`
	Published string `json:"published"`
	Source    string `json:"source"`
}

// Framework identifies the host framework this plugin set targets.
type Framework struct {
	Name       string `json:"name"`
	ModulePath string `json:"modulePath"`
	Note       string `json:"note,omitempty"`
}

// Plugin describes one curated plugin. Every field mirrors a constant the
// plugin itself declares (see <pkg>/plugin.go) — this is a description of a
// plugin, never a live handle to one.
type Plugin struct {
	Name       string `json:"name"`
	ModulePath string `json:"modulePath"`
	// Version is the PLUGIN's own release.
	Version     string `json:"version"`
	Description string `json:"description"`

	// Isolation is the mount posture. Two categories exist:
	//   - "sandbox-iframe-opaque": the default heavy-JS posture (richtext,
	//     mermaid, monaco). Runs in an opaque-origin sandboxed iframe, walled off
	//     from the host DOM/cookies/DB; talks over the postMessage bridge only.
	//   - "trusted-host-page": a plugin that runs IN the host page with full DOM
	//     access because it must (the tour plugin spotlights host elements). It
	//     is NOT sandboxed, so it must set Trusted=true (an explicit vouch) and
	//     declare no Sandbox tokens. The per-isolation guards in the tests here
	//     enforce that a trusted plugin can never masquerade as a sandboxed one
	//     and vice-versa.
	Isolation string `json:"isolation"`
	// Trusted marks a "trusted-host-page" plugin: it runs with full host access
	// and is vouched for by the app owner who compiles it in. It MUST be true for
	// a trusted-host-page row and MUST be absent/false for a sandboxed row.
	Trusted bool `json:"trusted,omitempty"`
	// Sandbox lists the iframe sandbox tokens granted (sandboxed rows only). Note
	// the absence of "allow-same-origin" across the set: that omission is what
	// keeps the frame on an opaque origin, walled off from host cookies and the
	// DB. A trusted-host-page row declares no sandbox (it is not framed).
	Sandbox []string `json:"sandbox,omitempty"`
	// Capabilities are the postMessage bridge scopes the plugin may request.
	// For a trusted host-page plugin there is no bridge, but the same names still
	// gate its server endpoints. These are the ALWAYS-ON grants: a plugin
	// advertises them whatever options it is constructed with.
	Capabilities []string `json:"capabilities"`
	// OptionalCapabilities are grants a plugin adds only when the host opts into
	// the feature that needs them — geomap's "geocode:search" appears only under
	// WithSearch, along with the route it gates. They are listed separately
	// because a reader deciding whether to adopt a plugin needs to know the
	// difference between "this plugin can reach the network" and "this plugin can
	// reach the network if you switch it on". Optional grants MUST NOT repeat
	// anything already in Capabilities.
	OptionalCapabilities []string `json:"optionalCapabilities,omitempty"`

	// FrameworkCompat is a best-effort floor (a semver range), not a tested
	// support matrix.
	FrameworkCompat string `json:"frameworkCompat"`

	RoutePrefix string `json:"routePrefix"`
	Entry       string `json:"entry"`
	Schema      string `json:"schema"`
	MinHeight   string `json:"minHeight"`
	Docs        string `json:"docs"`
}

// load reads and parses the index at path. It is read from disk rather than
// go:embed because the only consumer is the test binary next door — embedding
// would force the JSON to live in this directory, away from the repo root where
// hosts fetch it from.
func load(path string) (Index, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return Index{}, fmt.Errorf("registry: reading index: %w", err)
	}
	idx, err := ParseIndex(src)
	if err != nil {
		return Index{}, fmt.Errorf("registry: parsing %s: %w", path, err)
	}
	return idx, nil
}

// ParseIndex parses the curated index from its JSON bytes. It is the in-memory
// twin of load: same strict decoder, same structs, no filesystem. It is exposed
// so in-module tooling — the eject CLI, which reads the index from the embedded
// copy in package gofastrplugins — can parse it without going to disk or
// re-stating the schema. The strict unknown-field check still applies, so a key
// added to plugins.json without a matching struct field fails here too.
func ParseIndex(src []byte) (Index, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.DisallowUnknownFields()
	var idx Index
	if err := dec.Decode(&idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}
