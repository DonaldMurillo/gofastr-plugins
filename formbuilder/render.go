package formbuilder

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DonaldMurillo/gofastr/core-ui/html"
	"github.com/DonaldMurillo/gofastr/core/render"
	"github.com/DonaldMurillo/gofastr/framework/ui"
)

// render.go — the server half of the loop: render a saved schema as a REAL
// GoFastr form through the framework's own ui.Form, and re-derive every rule
// from the schema on POST. This is the code that makes the plugin's claim
// true: a form designed inside an opaque-origin sandbox is rendered and
// ENFORCED by the server, with no client trust anywhere in the path.
//
// The rendered inputs deliberately carry NO native constraint attributes —
// no `required`, no `pattern`, no `min`/`minlength`. Those would make the
// browser the enforcer and mask the very thing this page exists to
// demonstrate. The input TYPES stay (email/number/date are typing aids);
// every constraint is checked in [ValidateValues], in this process.

// liveInputID is the DOM id for a field's input (label association).
func liveInputID(name string) string { return "fb-live-" + name }

// RenderForm renders doc as a complete ui.Form posting to action. values
// pre-fills controls (a re-render after a refused submit keeps what the user
// typed); errs maps field names to messages and wires the per-field error
// chrome plus the form-level error summary.
func RenderForm(action string, doc Doc, values url.Values, errs ui.FieldErrors) render.HTML {
	if errs == nil {
		errs = ui.FieldErrors{}
	}
	fields := make([]render.HTML, 0, len(doc.Fields))
	for i := range doc.Fields {
		f := &doc.Fields[i]
		fields = append(fields, renderField(f, values, errs))
	}
	// novalidate opts the form out of ALL native client-side validation —
	// not just the constraint attributes (which renderField omits) but the
	// type-builtin checks an email/number/date input would otherwise run on
	// submit. The page exists to prove the SERVER enforces the schema;
	// letting the browser answer first would hide exactly that.
	return ui.Form(ui.FormConfig{
		Action:      action,
		Method:      "POST",
		Errors:      errs,
		SubmitLabel: "Submit",
		ExtraAttrs:  html.Attrs{"novalidate": ""},
	}, fields...)
}

// renderField maps one schema field onto the framework's own components:
// ui.FormField + html.Input for the input types, ui.Select and ui.Checkbox
// for the controls that own their label chrome. All doc data crosses through
// the framework's escaping renderers — the schema is data, and data never
// becomes markup here.
func renderField(f *Field, values url.Values, errs ui.FieldErrors) render.HTML {
	id := liveInputID(f.Name)
	value := values.Get(f.Name)
	errMsg := errs[f.Name]
	help := f.Help
	switch f.Type {
	case "select":
		// Required is deliberately NOT passed to ui.Select: it emits the
		// native `required` attribute, which would make the BROWSER refuse
		// the submit this page exists to let through to the server. The
		// schema's required flag is enforced in [ValidateValues].
		opts := make([]ui.SelectOption, 0, len(f.Options)+1)
		opts = append(opts, ui.SelectOption{Value: "", Text: "Choose…"})
		for _, o := range f.Options {
			opts = append(opts, ui.SelectOption{Value: o, Text: o, Selected: value == o})
		}
		return ui.Select(ui.SelectConfig{
			Name: f.Name, Label: f.Label, ID: id, Options: opts,
			Help: help, Error: errMsg,
		})
	case "checkbox":
		// Same omission as select: a native required checkbox blocks form
		// submission client-side, and the unchecked-checkbox refusal is one
		// of the rules this route demonstrates server-side.
		return ui.Checkbox(ui.ToggleConfig{
			Name: f.Name, Label: f.Label, Help: help, Error: errMsg,
			Checked: values.Has(f.Name),
		})
	case "textarea":
		input := html.TextArea(html.TextAreaConfig{
			Name: f.Name, ID: id, Content: value, Rows: 4,
			Placeholder: f.Label,
		})
		return ui.FormField(ui.FormFieldConfig{
			Label: f.Label, For: id, Help: help, Error: errMsg,
			Required: f.Required, Input: input,
		})
	default:
		// text / email / number / date — one input, typed by the schema.
		return ui.FormField(ui.FormFieldConfig{
			Label: f.Label, For: id, Help: help, Error: errMsg,
			Required: f.Required,
			Input: html.Input(html.InputConfig{
				Type: f.Type, Name: f.Name, ID: id, Value: value,
				Placeholder: f.Label,
			}),
		})
	}
}

// emailOK is the server's email rule: parse as an address AND round-trip —
// mail.ParseAddress happily accepts "Name <a@b>", which is not what an email
// field is for.
func emailOK(s string) bool {
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s
}

// ValidateValues re-derives every rule from the schema and applies it to the
// submitted form values, server-side. The browser was not consulted; neither
// was the frame. Returned errors are ui.FieldErrors so they drop straight
// into the re-rendered form.
func ValidateValues(doc Doc, vals url.Values) ui.FieldErrors {
	errs := ui.FieldErrors{}
	for i := range doc.Fields {
		f := &doc.Fields[i]
		value := strings.TrimSpace(vals.Get(f.Name))
		checked := vals.Has(f.Name)

		if f.Type == "checkbox" {
			if f.Required && !checked {
				errs[f.Name] = "This box must be checked."
			}
			continue
		}
		if value == "" {
			if f.Required {
				errs[f.Name] = "This field is required."
			}
			continue
		}
		switch f.Type {
		case "email":
			if !emailOK(value) {
				errs[f.Name] = "Enter a valid email address."
				continue
			}
		case "number":
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				errs[f.Name] = "Enter a number."
				continue
			}
			if f.Rules.Min != nil && n < *f.Rules.Min {
				errs[f.Name] = fmt.Sprintf("Must be at least %v.", *f.Rules.Min)
				continue
			}
			if f.Rules.Max != nil && n > *f.Rules.Max {
				errs[f.Name] = fmt.Sprintf("Must be at most %v.", *f.Rules.Max)
				continue
			}
		case "date":
			if _, err := time.Parse("2006-01-02", value); err != nil {
				errs[f.Name] = "Enter a date as YYYY-MM-DD."
				continue
			}
		case "select":
			ok := false
			for _, o := range f.Options {
				if value == o {
					ok = true
					break
				}
			}
			if !ok {
				// A crafted POST can carry any string; membership is enforced
				// even when the field is optional.
				errs[f.Name] = "Choose one of the listed options."
				continue
			}
		}
		if hasLengthRules(f.Type) {
			n := utf8.RuneCountInString(value)
			if f.Rules.MinLength != nil && n < int(*f.Rules.MinLength) {
				errs[f.Name] = fmt.Sprintf("Use at least %d characters.", int(*f.Rules.MinLength))
				continue
			}
			if f.Rules.MaxLength != nil && n > int(*f.Rules.MaxLength) {
				errs[f.Name] = fmt.Sprintf("Use at most %d characters.", int(*f.Rules.MaxLength))
				continue
			}
		}
		if hasPattern(f.Type) && f.Rules.Pattern != "" {
			re, err := regexp.Compile(f.Rules.Pattern)
			if err != nil {
				// Unreachable past /save (the pattern was compiled there
				// before the doc was persisted); treat as a server fault
				// rather than silently passing the field.
				errs[f.Name] = "This field's rule is broken on the server; it was refused."
				continue
			}
			if !re.MatchString(value) {
				errs[f.Name] = "This value does not match the required pattern (" + f.Rules.Pattern + ")."
				continue
			}
		}
	}
	return errs
}
