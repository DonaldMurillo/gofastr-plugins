package pdf

import (
	"encoding/json"
	"errors"
)

// overlay.go defines the canonical pdf-v1 document model and the request/
// response payloads the save/export routes hand to host-supplied handlers.
//
// The canonical document is NOT the PDF file — it is a small JSON *overlay*
// (annotations, form fills, redactions, page operations) that round-trips
// through the hidden form field like every other plugin here. The PDF itself
// is an external resource the host resolves via [WithSource] and pushes over
// the bridge; the frame never fetches it (connect-src 'none').
//
// All geometry is in PDF USER SPACE — points, origin bottom-left, alongside
// the page's own /Rotate — never CSS pixels. The overlay therefore survives
// zoom, view rotation and re-render, and maps 1:1 into pdf-lib at export time.
// See docs/pdf.md §"The document model" for the full rationale.
//
// These structs are precise by design (typed fields, no map[string]any): a
// host save/export handler inspects a real shape, not an opaque blob. Unknown
// JSON keys are tolerated (forward-compatible, matching every other plugin's
// RawMessage posture) — only the well-known fields are typed.

// Overlay is the canonical pdf-v1 document handed to [WithSaveHandler] and
// surfaced from [Plugin.LoadDoc]. It is the annotation layer over an external
// PDF, never the file bytes.
type Overlay struct {
	// SchemaVersion is the interchange version ("pdf-v1"). Echoed verbatim.
	SchemaVersion string `json:"schemaVersion,omitempty"`
	// Src binds the overlay to the external PDF it was authored against. The
	// frame recomputes src.sha256 on load and refuses to apply annotations on
	// mismatch, so an overlay never silently paints boxes at stale coordinates.
	Src Source `json:"src,omitempty"`
	// Annotations is the ordered list of markup (highlights, stamps, drawings,
	// …). Only the common fields are typed; see the note on [Annotation] for
	// where the type-specific ones live.
	Annotations []Annotation `json:"annotations,omitempty"`
	// FormFields maps an AcroForm field name to its value. Values are opaque
	// JSON (string / bool / number / …): typed as json.RawMessage rather than
	// any so the struct stays precise without modelling every field shape.
	FormFields map[string]json.RawMessage `json:"formFields,omitempty"`
	// Redactions is the ordered list of regions to remove at export. Redaction
	// is destructive and irreversible; each rect may carry a reason label.
	Redactions []Redaction `json:"redactions,omitempty"`
	// PageOps is the ordered list of page operations (rotate / delete / move /
	// insert / append) applied at export.
	PageOps []PageOp `json:"pageOps,omitempty"`
	// Rev is the optimistic-concurrency revision. A save whose rev does not
	// match the stored revision loses the race and surfaces as [ErrConflict]
	// (HTTP 409), so two editors cannot silently clobber each other.
	Rev int `json:"rev,omitempty"`
}

// Source identifies the external PDF an overlay belongs to.
type Source struct {
	// Kind is how the host resolves the PDF: "url" (a fetchable location) or
	// "id" (an opaque key the host's [WithSource] understands). The host
	// adapter fetches /doc/{ref}; the frame never does.
	Kind string `json:"kind,omitempty"`
	// Ref is the url or id; it is the {id} path segment of GET /doc/{id}.
	Ref string `json:"ref,omitempty"`
	// SHA256 binds the overlay to exact bytes. On load the frame recomputes it
	// and, on mismatch, refuses to apply annotations (soft warning over plain
	// http:// to a non-localhost host — never a hard fail).
	SHA256 string `json:"sha256,omitempty"`
	// Pages is the page count of the referenced PDF, cached in the overlay so
	// the frame can lay out the page rail before the bytes arrive.
	Pages int `json:"pages,omitempty"`
}

// Annotation is one markup item on a page. The Rect is the common hit box;
// type-specific extras (color, stroke width, label text, …) survive verbatim
// in Raw for round-trip fidelity.
type Annotation struct {
	// ID is the stable client-assigned identity (so a re-save updates an
	// existing annotation rather than duplicating it).
	ID string `json:"id,omitempty"`
	// Page is the 1-indexed page number the annotation lives on.
	Page int `json:"page,omitempty"`
	// Type is the annotation kind ("highlight", "stamp", "drawing", …). The
	// set is open: an unknown type round-trips without rejection.
	Type string `json:"type,omitempty"`
	// Rect is the hit box in PDF user space as [x, y, w, h] (origin
	// bottom-left). A slice rather than a fixed [4]float64 so a malformed
	// entry cannot fail the whole overlay parse — the raw DocJSON is the
	// authoritative record either way.
	Rect []float64 `json:"rect,omitempty"`

	// NOTE on type-specific fields (colour, stroke width, ink points, label
	// text, …): they are deliberately NOT modelled here. They survive on
	// [SaveRequest.DocJSON], which is the verbatim canonical JSON and the
	// authoritative record — this struct exists for typed *inspection* by a
	// host handler, not for re-serialisation. Persisting a re-marshal of this
	// struct instead of DocJSON would silently drop every extra field, so
	// don't.
}

// Redaction is one destructive removal region. At export the content under
// Rect is excised: pages with any redaction are rasterized at [WithRedactDPI],
// masked, and embedded as images into a freshly built document; pages without
// one are copied through losslessly.
type Redaction struct {
	ID     string    `json:"id,omitempty"`
	Page   int       `json:"page,omitempty"`
	Rect   []float64 `json:"rect,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// PageOp is one ordered page mutation applied at export.
type PageOp struct {
	// Op is the operation: "rotate", "delete", "move", "insert", or "append".
	Op string `json:"op,omitempty"`
	// Page is the 1-indexed target page (the page being rotated/deleted/moved,
	// or the anchor for insert/append).
	Page int `json:"page,omitempty"`
	// Value is the operation parameter (e.g. rotation degrees for "rotate", or
	// the source page for "move"/"insert").
	Value int `json:"value,omitempty"`
}

// ErrConflict is the sentinel a [WithSaveHandler] hook returns to signal that
// the save lost an optimistic-concurrency check — the stored document changed
// under the editor since it loaded (the overlay's rev is stale). handleSave
// maps it to HTTP 409 (E_CONFLICT) rather than the generic 500 (E_SAVE),
// which is the one status the host adapter relays back to the frame as a
// distinct saveResult so the editor can keep the doc dirty and warn the user
// instead of silently dropping their edits — the exact contract richtext and
// monaco ship. Wrap it (fmt.Errorf("...: %w", pdf.ErrConflict)) to add
// context; handleSave uses errors.Is.
var ErrConflict = errors.New("pdf: save conflict")

// SaveRequest is the persistence payload handed to [WithSaveHandler]. Doc is
// the parsed overlay (typed access for inspection / mode checks); DocJSON is
// the raw canonical JSON for verbatim persistence — the authoritative record
// that round-trips through the hidden field, so type-specific annotation
// extras never get dropped by a struct re-marshal.
type SaveRequest struct {
	DocID         string  // persistence key (the mount marker's data-fui-plugin-docid)
	Doc           Overlay // parsed overlay (zero-valued if the body failed to parse)
	DocJSON       string  // raw canonical overlay JSON (verbatim, authoritative)
	SchemaVersion string  // interchange version ("pdf-v1")
	Rev           int     // optimistic-concurrency revision carried by the overlay
}

// ExportRequest is the payload handed to [WithExportHandler]. Bytes is the
// produced PDF (already rasterized for redacted pages); Report is the in-frame
// verification report (opaque JSON — the frame owns its shape); Kind is the
// export intent, which the route has already mode-checked.
type ExportRequest struct {
	DocID  string          // persistence key
	Kind   string          // "export" | "download" | "print" | "redact"
	Bytes  []byte          // the produced PDF bytes
	Report json.RawMessage // the in-frame verification report (opaque; may be nil)
}

// ExportKind values the mode enforcement branches on. They are the only kinds
// the frame is permitted to request; any other value is rejected as a bad
// request.
const (
	ExportKindExport   = "export"   // produce + store the file
	ExportKindDownload = "download" // produce + return a download URL
	ExportKindPrint    = "print"    // produce for the host print path
	ExportKindRedact   = "redact"   // produce a redacted document (ModeRedact only)
)
