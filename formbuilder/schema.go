package formbuilder

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// schema.go — the canonical doc (formbuilder-v1) and its validation.
//
// The doc is the form schema, and it is DATA ONLY: {version, fields[]}. Each
// field is {type, name, label, required, help, rules} plus options for select.
// Nothing in it is markup, nothing is executed, nothing is rendered from it
// without escaping — which is what makes "a form designed in a sandboxed
// browser frame is enforced by the server" a safe sentence. [ValidateDoc] is
// the boundary: a schema that does not pass it is refused with a specific
// error code at POST /save, no matter what the frame believes.

// FieldTypes is the closed set of field types a schema may declare. Adding
// one is a schema change: bump the version and teach render.go + the frame.
var FieldTypes = []string{"text", "email", "number", "textarea", "select", "checkbox", "date"}

// fieldTypeOK is the membership check for the closed set.
func fieldTypeOK(t string) bool {
	for _, ft := range FieldTypes {
		if ft == t {
			return true
		}
	}
	return false
}

// hasLengthRules reports whether a type carries string-length rules.
func hasLengthRules(t string) bool {
	return t == "text" || t == "email" || t == "textarea"
}

// hasRangeRules reports whether a type carries numeric range rules.
func hasRangeRules(t string) bool { return t == "number" }

// hasPattern reports whether a type may declare a regexp rule.
func hasPattern(t string) bool {
	return t == "text" || t == "email" || t == "textarea"
}

// nameRE is the form-field-identifier grammar: a lowercase letter, then
// lowercase letters, digits or underscores, at most 40 runes. The same names
// become HTML name attributes and Go map keys, so a narrow grammar is the
// cheapest way to keep both predictable.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Schema bounds. A doc wider than these is a mistake or an attack, not a
// form; /save refuses it instead of persisting it.
const (
	maxFields     = 48
	maxOptions    = 32
	maxLabelLen   = 120
	maxHelpLen    = 240
	maxOptionLen  = 80
	maxPatternLen = 512
	maxRuleNumber = 1e9
)

// SchemaError is a validation refusal: a stable machine-readable code plus a
// human sentence. The code is what tests (and the frame's status line) branch
// on; the message is what a person reads.
type SchemaError struct {
	Code    string
	Message string
}

func (e *SchemaError) Error() string { return e.Message }

func schemaErr(code, format string, args ...any) *SchemaError {
	return &SchemaError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Error codes, so tests and docs pin the exact vocabulary.
const (
	ErrBadJSON          = "E_BAD_JSON"
	ErrUnknownVersion   = "E_UNKNOWN_VERSION"
	ErrTooManyFields    = "E_TOO_MANY_FIELDS"
	ErrUnknownFieldType = "E_UNKNOWN_FIELD_TYPE"
	ErrEmptyName        = "E_EMPTY_NAME"
	ErrInvalidName      = "E_INVALID_NAME"
	ErrDuplicateName    = "E_DUPLICATE_NAME"
	ErrLabelTooLong     = "E_LABEL_TOO_LONG"
	ErrHelpTooLong      = "E_HELP_TOO_LONG"
	ErrMarkup           = "E_MARKUP"
	ErrBadSelect        = "E_BAD_SELECT"
	ErrBadRule          = "E_BAD_RULE"
)

// Doc is the canonical formbuilder-v1 document: the form schema as pure data.
type Doc struct {
	// Version is the schema version of the doc itself. Empty is treated as
	// the current version on save (the frame does not always stamp it); any
	// OTHER value is refused with E_UNKNOWN_VERSION — a saved schema has to
	// outlive the plugin that wrote it, so an unknown future version must
	// fail loudly, not silently degrade.
	Version string  `json:"version"`
	Fields  []Field `json:"fields"`
}

// Field is one designed form field.
type Field struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Help     string `json:"help"`
	// Options is the closed value set; select fields only.
	Options []string `json:"options,omitempty"`
	Rules   Rules    `json:"rules,omitempty"`
}

// Rules are the validation constraints. Length/range bounds ride as *float64
// (not *int) so a hostile "minLength": 2.5 decodes and is REFUSED as a bad
// rule, rather than dying as a JSON unmarshal error that reads as a transport
// problem.
type Rules struct {
	MinLength *float64 `json:"minLength,omitempty"`
	MaxLength *float64 `json:"maxLength,omitempty"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	// Pattern is a regexp, compiled with Go's RE2 at save time. The server
	// re-compiles it on every validation, so the frame and the server cannot
	// disagree about what it means.
	Pattern string `json:"pattern,omitempty"`
}

// RuleCount is the number of enforceable constraints in the doc (required
// flags plus explicit rules) — the number the demo's proof strip and the
// save verdict report.
func (d Doc) RuleCount() int {
	n := 0
	for i := range d.Fields {
		f := &d.Fields[i]
		if f.Required {
			n++
		}
		if f.Rules.MinLength != nil {
			n++
		}
		if f.Rules.MaxLength != nil {
			n++
		}
		if f.Rules.Min != nil {
			n++
		}
		if f.Rules.Max != nil {
			n++
		}
		if f.Rules.Pattern != "" {
			n++
		}
	}
	return n
}

// ValidateDoc enforces every formbuilder-v1 invariant and NORMALISES the doc
// in place (version stamped, empty labels defaulted to the name). A doc that
// returns nil is safe to persist and safe to render.
func ValidateDoc(d *Doc) error {
	if d.Version == "" {
		d.Version = SchemaVersion
	} else if d.Version != SchemaVersion {
		return schemaErr(ErrUnknownVersion,
			"doc version %q is not %q — this plugin cannot safely interpret it", d.Version, SchemaVersion)
	}
	if len(d.Fields) > maxFields {
		return schemaErr(ErrTooManyFields, "doc has %d fields; the ceiling is %d", len(d.Fields), maxFields)
	}
	seen := make(map[string]bool, len(d.Fields))
	for i := range d.Fields {
		f := &d.Fields[i]
		if !fieldTypeOK(f.Type) {
			return schemaErr(ErrUnknownFieldType,
				"field %d: unknown type %q (known: %s)", i+1, f.Type, strings.Join(FieldTypes, ", "))
		}
		if f.Name == "" {
			return schemaErr(ErrEmptyName, "field %d (%s): name is empty", i+1, f.Type)
		}
		if !nameRE.MatchString(f.Name) {
			return schemaErr(ErrInvalidName,
				"field %d: %q is not a valid field identifier (lowercase letter, then a–z 0–9 _, max 40)",
				i+1, f.Name)
		}
		if seen[f.Name] {
			return schemaErr(ErrDuplicateName, "duplicate field name: %s", f.Name)
		}
		seen[f.Name] = true

		if f.Label == "" {
			f.Label = f.Name
		}
		if utf8.RuneCountInString(f.Label) > maxLabelLen {
			return schemaErr(ErrLabelTooLong, "field %s: label longer than %d runes", f.Name, maxLabelLen)
		}
		if utf8.RuneCountInString(f.Help) > maxHelpLen {
			return schemaErr(ErrHelpTooLong, "field %s: help text longer than %d runes", f.Name, maxHelpLen)
		}
		// The data-only invariant, enforced at the boundary: a label, help
		// line or option carrying markup is a schema trying to be a document.
		// Refuse it — the moment a saved doc can contain HTML, the proof this
		// plugin exists to make is gone.
		if strings.Contains(f.Label, "<") || strings.Contains(f.Help, "<") {
			return schemaErr(ErrMarkup,
				"field %s: label/help must be plain text — the schema is data only", f.Name)
		}
		if err := validateOptions(f); err != nil {
			return err
		}
		if err := validateRules(f); err != nil {
			return err
		}
	}
	return nil
}

func validateOptions(f *Field) error {
	if f.Type == "select" {
		if len(f.Options) == 0 {
			return schemaErr(ErrBadSelect, "field %s: a select needs at least one option", f.Name)
		}
		if len(f.Options) > maxOptions {
			return schemaErr(ErrBadSelect, "field %s: %d options exceeds the ceiling of %d",
				f.Name, len(f.Options), maxOptions)
		}
		seen := make(map[string]bool, len(f.Options))
		for _, o := range f.Options {
			if o == "" {
				return schemaErr(ErrBadSelect, "field %s: empty option", f.Name)
			}
			if utf8.RuneCountInString(o) > maxOptionLen {
				return schemaErr(ErrBadSelect, "field %s: option longer than %d runes", f.Name, maxOptionLen)
			}
			if strings.Contains(o, "<") {
				return schemaErr(ErrMarkup, "field %s: options must be plain text — the schema is data only", f.Name)
			}
			if seen[o] {
				return schemaErr(ErrBadSelect, "field %s: duplicate option %q", f.Name, o)
			}
			seen[o] = true
		}
		return nil
	}
	// Options on a non-select field are refused rather than dropped: a silent
	// drop is how a doc drifts from what its author believes it says.
	if len(f.Options) > 0 {
		return schemaErr(ErrBadSelect, "field %s: only select fields carry options", f.Name)
	}
	return nil
}

// ruleNum validates one numeric rule value: finite, integral where a count is
// meant, and inside sane bounds so a "minLength" of 1e30 cannot overflow the
// live form's attribute rendering later.
func ruleNum(f *Field, what string, v float64) error {
	if v < 0 || v > maxRuleNumber {
		return schemaErr(ErrBadRule, "field %s: %s %v is out of range", f.Name, what, v)
	}
	if v != float64(int64(v)) {
		return schemaErr(ErrBadRule, "field %s: %s must be a whole number, got %v", f.Name, what, v)
	}
	return nil
}

func validateRules(f *Field) error {
	r := f.Rules
	if !hasLengthRules(f.Type) {
		if r.MinLength != nil || r.MaxLength != nil {
			return schemaErr(ErrBadRule, "field %s: length rules apply to text-like fields, not %s", f.Name, f.Type)
		}
	}
	if !hasRangeRules(f.Type) {
		if r.Min != nil || r.Max != nil {
			return schemaErr(ErrBadRule, "field %s: min/max value rules apply to number fields, not %s", f.Name, f.Type)
		}
	}
	if !hasPattern(f.Type) && r.Pattern != "" {
		return schemaErr(ErrBadRule, "field %s: pattern rules apply to text-like fields, not %s", f.Name, f.Type)
	}
	for _, bound := range []struct {
		what string
		v    *float64
	}{
		{"minLength", r.MinLength},
		{"maxLength", r.MaxLength},
		{"min", r.Min},
		{"max", r.Max},
	} {
		if bound.v == nil {
			continue
		}
		if err := ruleNum(f, bound.what, *bound.v); err != nil {
			return err
		}
	}
	if r.MinLength != nil && r.MaxLength != nil && *r.MinLength > *r.MaxLength {
		return schemaErr(ErrBadRule, "field %s: minLength %v exceeds maxLength %v",
			f.Name, *r.MinLength, *r.MaxLength)
	}
	if r.Min != nil && r.Max != nil && *r.Min > *r.Max {
		return schemaErr(ErrBadRule, "field %s: min %v exceeds max %v", f.Name, *r.Min, *r.Max)
	}
	if r.Pattern != "" {
		if len(r.Pattern) > maxPatternLen {
			return schemaErr(ErrBadRule, "field %s: pattern longer than %d bytes", f.Name, maxPatternLen)
		}
		// Compile HERE, with the server's own engine: a pattern that Go
		// cannot run is a rule the server cannot enforce, whatever the
		// frame's regex dialect thinks of it.
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return schemaErr(ErrBadRule, "field %s: pattern does not compile: %v", f.Name, err)
		}
	}
	return nil
}
