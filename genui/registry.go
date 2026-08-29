package genui

// registry.go — the Go mirror of the frame's component registry: the FIXED
// set of view components a composition may name, each with its declared
// props, their types, which are required, and whether it accepts children.
//
// This is the authority [Validate] checks every composition against, and it
// is the entire containment story: a composition is a tree of THESE nodes
// with THESE props and nothing else. No markup, no CSS, no code crosses the
// seam — a model that wants a style or a className has no prop to put it in,
// so "unknown prop" is not an error message, it is the security boundary.
//
// The frame (genui/js/src/registry.tsx) carries its own copy of the same
// registry and validates again before rendering — same rules, no trust in
// the bridge. The two copies are one fact stated twice: the component NAMES
// are the shared contract, and the prop schemas below are aligned 1:1 with
// the frame's declared schemas (enum vocabularies, required flags, children
// and action flags) so a composition that passes Go never gets refused a
// hair's-breadth later inside the cage — and vice versa.
// plugin_test.go asserts the names cannot drift between the Go registry,
// the host adapter's narrowing table, and the frame sources.

// PropType is the closed vocabulary of prop value shapes a component may
// declare. Values arrive as decoded JSON (map[string]any), so "number"
// means a float64 and "int" means an integral number — JSON has no integer
// type to decode into. Go-built compositions may equally hand us an int
// literal; the validator accepts both as numbers.
type PropType string

const (
	// PropString is a plain string value.
	PropString PropType = "string"
	// PropInt is a number that must be integral (level: 2, not 2.5).
	PropInt PropType = "int"
	// PropNumber is any finite number (delta: 12.5).
	PropNumber PropType = "number"
	// PropStrings is an array of strings (Table columns).
	PropStrings PropType = "strings"
	// PropRows is an array of string arrays (Table rows).
	PropRows PropType = "rows"
)

// Bounds on prop VALUES. The depth (16) and node-count (200) bounds cap the
// tree; these cap the payloads inside a single node, so a hostile or runaway
// "Table" cannot smuggle a megabyte of string per cell past a validator that
// only counted nodes. The frame's copy caps only depth and nodes; Go being
// stricter here is the safe direction (a tree Go accepts always passes the
// frame).
const (
	// MaxStringRunes caps any single string prop value.
	MaxStringRunes = 500
	// MaxArrayLen caps the length of a "strings" prop and the number of rows
	// in a "rows" prop.
	MaxArrayLen = 64
	// MaxRowLen caps the cells in one row of a "rows" prop.
	MaxRowLen = 64
)

// PropSpec declares one prop of one component.
type PropSpec struct {
	// Name is the prop key. Anything else in a node's props object is an
	// unknown prop and the composition is refused.
	Name string
	// Type is the value shape; see the PropType constants.
	Type PropType
	// Required means the prop must be present. An optional prop may be
	// absent, but present-but-null is still a wrong type, not absent.
	Required bool
	// Enum, when non-nil, is the closed vocabulary for a PropString. A value
	// outside it is refused (gap: "huge" is not a layout the renderer knows).
	Enum []string
	// Min/Max, when non-nil, bound a PropInt/PropNumber value.
	Min, Max *float64
}

// ComponentSpec declares one registry entry.
type ComponentSpec struct {
	// Name is the component key a composition's node must carry.
	Name string
	// Props are the declared props, in declaration order.
	Props []PropSpec
	// AcceptsChildren reports whether a node of this component may carry a
	// children array at all (children on Text is refused, not ignored).
	AcceptsChildren bool
	// CarriesAction reports whether a node of this component may carry an
	// action at all. True for Button only: a generated control is the one
	// thing in a composition with behaviour, and the allow-list still gates
	// whichever name it carries.
	CarriesAction bool
}

// Registry is the fixed component table. It is built once
// ([DefaultRegistry]) and treated as immutable; Compose receives it, the
// validator reads it, and nothing mutates it, so a value copy is safe to
// hand to untrusted composer code.
type Registry struct {
	byName map[string]ComponentSpec
	order  []string
}

// DefaultRegistry returns the registry of the eight genui-v1 components:
// Stack, Card, Heading, Text, Stat, Badge, Table, Button. The schemas below
// mirror the frame's registry (genui/js/src/registry.tsx) exactly: Stack
// states its geometry (gap and direction both required), Heading's level is
// the closed 1|2|3 set, Badge's tones are neutral|good|warn|bad, and Button
// is the only entry that carries an action.
func DefaultRegistry() Registry {
	return Registry{
		byName: map[string]ComponentSpec{
			"Stack": {
				Name: "Stack",
				Props: []PropSpec{
					{Name: "gap", Type: PropString, Required: true, Enum: []string{"sm", "md", "lg"}},
					{Name: "direction", Type: PropString, Required: true, Enum: []string{"row", "column"}},
				},
				AcceptsChildren: true,
			},
			"Card": {
				Name:            "Card",
				Props:           []PropSpec{{Name: "title", Type: PropString}},
				AcceptsChildren: true,
			},
			"Heading": {
				Name: "Heading",
				Props: []PropSpec{
					{Name: "text", Type: PropString, Required: true},
					{Name: "level", Type: PropInt, Required: true, Min: new(1.0), Max: new(3.0)},
				},
			},
			"Text": {
				Name: "Text",
				Props: []PropSpec{
					{Name: "text", Type: PropString, Required: true},
					{Name: "tone", Type: PropString, Enum: []string{"default", "muted"}},
				},
			},
			"Stat": {
				Name: "Stat",
				Props: []PropSpec{
					{Name: "label", Type: PropString, Required: true},
					{Name: "value", Type: PropString, Required: true},
					{Name: "delta", Type: PropNumber},
				},
			},
			"Badge": {
				Name: "Badge",
				Props: []PropSpec{
					{Name: "label", Type: PropString, Required: true},
					{Name: "tone", Type: PropString, Enum: []string{"neutral", "good", "warn", "bad"}},
				},
			},
			"Table": {
				Name: "Table",
				Props: []PropSpec{
					{Name: "columns", Type: PropStrings, Required: true},
					{Name: "rows", Type: PropRows, Required: true},
				},
			},
			"Button": {
				Name: "Button",
				Props: []PropSpec{
					{Name: "label", Type: PropString, Required: true},
					{Name: "variant", Type: PropString, Enum: []string{"primary", "default"}},
				},
				CarriesAction: true,
			},
		},
		order: []string{"Stack", "Card", "Heading", "Text", "Stat", "Badge", "Table", "Button"},
	}
}

// Lookup returns the spec for name. ok is false for anything not in the
// registry — which is exactly the case [Validate] refuses.
func (r Registry) Lookup(name string) (ComponentSpec, bool) {
	spec, ok := r.byName[name]
	return spec, ok
}

// Names returns every registered component name in declaration order. The
// frame's registry must name the same set; the drift test pins that.
func (r Registry) Names() []string {
	return append([]string{}, r.order...)
}
