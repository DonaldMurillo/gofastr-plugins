package genui

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// validate.go — the composition tree and the validator that guards it.
//
// Go is authoritative: a composition is validated HERE before it is
// persisted and again (same function) before it is served, and the frame
// validates a third time before rendering because "the host already checked
// it" is exactly the assumption that makes a second bug fatal. The rules are
// identical on both sides: fixed registry, declared props only, host-named
// actions, bounded depth and node count.
//
// Every rejection carries the offending PATH (root.children[2].props.tone):
// a composition is model output, and model output is debugged by a human
// reading that message. [ValidationError.Error] therefore prefixes the path,
// and the test suite asserts the path for every rule, one by one.

// Composition bounds. A model that emits a runaway tree must fail VALIDATION
// (here, with a path), never the renderer.
const (
	// MaxDepth is the deepest node allowed, root = depth 1.
	MaxDepth = 16
	// MaxNodes is the total node count allowed, root included.
	MaxNodes = 200
)

// Error codes, so routes, tests and the frame's status line branch on a
// stable vocabulary instead of parsing free text.
const (
	ErrBadVersion       = "E_BAD_VERSION"
	ErrNoRoot           = "E_NO_ROOT"
	ErrUnknownComponent = "E_UNKNOWN_COMPONENT"
	ErrUnknownProp      = "E_UNKNOWN_PROP"
	ErrPropType         = "E_PROP_TYPE"
	ErrPropValue        = "E_PROP_VALUE"
	ErrRequiredProp     = "E_REQUIRED_PROP"
	ErrChildren         = "E_CHILDREN"
	ErrAction           = "E_ACTION"
	ErrDepth            = "E_DEPTH"
	ErrNodeCount        = "E_NODE_COUNT"
)

// Node is one element of a composition tree: a registry component, its
// declared props, an optional host-allow-listed action, and children only
// where the component declares them. There is deliberately no field for
// markup, style, classes or code — a node is data the renderer interprets,
// never input it executes.
type Node struct {
	Component string         `json:"component"`
	Props     map[string]any `json:"props,omitempty"`
	Action    string         `json:"action,omitempty"`
	Children  []Node         `json:"children,omitempty"`
}

// Composition is the canonical genui-v1 document.
type Composition struct {
	SchemaVersion string `json:"schemaVersion"`
	Root          *Node  `json:"root"`
}

// ValidationError is a validation refusal: a stable machine-readable code,
// the offending path, and a human sentence. The path is the contract —
// tests pin it per rule.
type ValidationError struct {
	Code    string
	Path    string
	Message string
}

func (e *ValidationError) Error() string { return e.Path + ": " + e.Message }

func validationErr(code, path, format string, args ...any) *ValidationError {
	return &ValidationError{Code: code, Path: path, Message: fmt.Sprintf(format, args...)}
}

// Validate enforces every genui-v1 invariant against the given registry and
// host-supplied action allow-list. A nil error means the composition is safe
// to persist and safe to render. Rejections are deterministic (prop keys are
// walked in sorted order) so the same bad tree always yields the same path.
func Validate(c Composition, reg Registry, actions []string) error {
	if c.SchemaVersion != SchemaVersion {
		return validationErr(ErrBadVersion, "schemaVersion",
			"schema version %q is not %q", c.SchemaVersion, SchemaVersion)
	}
	if c.Root == nil {
		return validationErr(ErrNoRoot, "root", "composition has no root node")
	}
	count := 0
	return validateNode("root", c.Root, 1, reg, actions, &count)
}

// validateNode checks one node (depth counts the root as 1) and recurses.
// count accumulates across the whole walk for the node budget.
func validateNode(path string, n *Node, depth int, reg Registry, actions []string, count *int) error {
	*count++
	if *count > MaxNodes {
		return validationErr(ErrNodeCount, path,
			"composition exceeds the %d-node budget here (node #%d)", MaxNodes, *count)
	}
	if depth > MaxDepth {
		return validationErr(ErrDepth, path,
			"node at depth %d exceeds the depth budget of %d", depth, MaxDepth)
	}

	spec, ok := reg.Lookup(n.Component)
	if !ok {
		return validationErr(ErrUnknownComponent, path,
			"component %q is not in the registry", n.Component)
	}

	if len(n.Children) > 0 && !spec.AcceptsChildren {
		return validationErr(ErrChildren, path+".children",
			"component %q does not accept children", n.Component)
	}

	if n.Action != "" {
		if !spec.CarriesAction {
			return validationErr(ErrAction, path+".action",
				"component %q does not accept an action", n.Component)
		}
		if !slices.Contains(actions, n.Action) {
			return validationErr(ErrAction, path+".action",
				"action %q is not in the host allow-list", n.Action)
		}
	}

	if err := validateProps(path, spec, n.Props); err != nil {
		return err
	}

	for i := range n.Children {
		child := &n.Children[i]
		if err := validateNode(fmt.Sprintf("%s.children[%d]", path, i), child, depth+1, reg, actions, count); err != nil {
			return err
		}
	}
	return nil
}

// validateProps checks a node's props object against the component's
// declaration: every present key must be declared with the right shape, and
// every required key must be present. Keys are visited in sorted order so a
// hostile props object cannot shuffle which error surfaces.
func validateProps(path string, spec ComponentSpec, props map[string]any) error {
	if len(props) == 0 {
		// Still enforce required props on an absent/empty object.
		for _, ps := range spec.Props {
			if ps.Required {
				return validationErr(ErrRequiredProp, path+".props."+ps.Name,
					"required prop %q is missing", ps.Name)
			}
		}
		return nil
	}
	declared := make(map[string]PropSpec, len(spec.Props))
	for _, ps := range spec.Props {
		declared[ps.Name] = ps
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		ps, ok := declared[k]
		if !ok {
			return validationErr(ErrUnknownProp, path+".props."+k,
				"component %q has no prop %q", spec.Name, k)
		}
		if err := validatePropValue(path+".props."+k, ps, props[k]); err != nil {
			return err
		}
	}
	for _, ps := range spec.Props {
		if ps.Required {
			if _, present := props[ps.Name]; !present {
				return validationErr(ErrRequiredProp, path+".props."+ps.Name,
					"required prop %q is missing", ps.Name)
			}
		}
	}
	return nil
}

// validatePropValue checks one present prop value against its declaration.
// JSON decoding hands us any as string | float64 | bool | []any | map[string]any
// | nil; anything but the declared shape is refused.
func validatePropValue(path string, ps PropSpec, v any) error {
	switch ps.Type {
	case PropString:
		s, ok := v.(string)
		if !ok {
			return validationErr(ErrPropType, path,
				"prop %q must be a string, got %s", ps.Name, jsonKind(v))
		}
		if len([]rune(s)) > MaxStringRunes {
			return validationErr(ErrPropValue, path,
				"prop %q exceeds the %d-rune cap", ps.Name, MaxStringRunes)
		}
		if ps.Enum != nil && !slices.Contains(ps.Enum, s) {
			return validationErr(ErrPropValue, path,
				"prop %q must be one of %s, got %q", ps.Name, strings.Join(ps.Enum, "|"), s)
		}
	case PropInt:
		f, ok := asNumber(v)
		if !ok {
			return validationErr(ErrPropType, path,
				"prop %q must be a number, got %s", ps.Name, jsonKind(v))
		}
		if f != math.Trunc(f) {
			return validationErr(ErrPropType, path,
				"prop %q must be an integer, got %v", ps.Name, f)
		}
		if err := checkNumericBounds(path, ps, f); err != nil {
			return err
		}
	case PropNumber:
		f, ok := asNumber(v)
		if !ok {
			return validationErr(ErrPropType, path,
				"prop %q must be a number, got %s", ps.Name, jsonKind(v))
		}
		if err := checkNumericBounds(path, ps, f); err != nil {
			return err
		}
	case PropStrings:
		arr, ok := v.([]any)
		if !ok {
			return validationErr(ErrPropType, path,
				"prop %q must be an array of strings, got %s", ps.Name, jsonKind(v))
		}
		if len(arr) > MaxArrayLen {
			return validationErr(ErrPropValue, path,
				"prop %q exceeds the %d-entry cap", ps.Name, MaxArrayLen)
		}
		for i, el := range arr {
			s, ok := el.(string)
			if !ok {
				return validationErr(ErrPropType, fmt.Sprintf("%s[%d]", path, i),
					"prop %q entries must be strings, got %s", ps.Name, jsonKind(el))
			}
			if len([]rune(s)) > MaxStringRunes {
				return validationErr(ErrPropValue, fmt.Sprintf("%s[%d]", path, i),
					"prop %q entry exceeds the %d-rune cap", ps.Name, MaxStringRunes)
			}
		}
	case PropRows:
		arr, ok := v.([]any)
		if !ok {
			return validationErr(ErrPropType, path,
				"prop %q must be an array of rows, got %s", ps.Name, jsonKind(v))
		}
		if len(arr) > MaxArrayLen {
			return validationErr(ErrPropValue, path,
				"prop %q exceeds the %d-row cap", ps.Name, MaxArrayLen)
		}
		for i, rowAny := range arr {
			row, ok := rowAny.([]any)
			if !ok {
				return validationErr(ErrPropType, fmt.Sprintf("%s[%d]", path, i),
					"prop %q rows must be arrays, got %s", ps.Name, jsonKind(rowAny))
			}
			if len(row) > MaxRowLen {
				return validationErr(ErrPropValue, fmt.Sprintf("%s[%d]", path, i),
					"row exceeds the %d-cell cap", MaxRowLen)
			}
			for j, cellAny := range row {
				cell, ok := cellAny.(string)
				if !ok {
					return validationErr(ErrPropType, fmt.Sprintf("%s[%d][%d]", path, i, j),
						"prop %q cells must be strings, got %s", ps.Name, jsonKind(cellAny))
				}
				if len([]rune(cell)) > MaxStringRunes {
					return validationErr(ErrPropValue, fmt.Sprintf("%s[%d][%d]", path, i, j),
						"cell exceeds the %d-rune cap", MaxStringRunes)
				}
			}
		}
	default:
		// A registry entry with an undeclared PropType is a programming
		// error, not model output; refuse it loudly rather than guessing.
		return validationErr(ErrPropType, path,
			"prop %q has unsupported registry type %q", ps.Name, ps.Type)
	}
	return nil
}

func checkNumericBounds(path string, ps PropSpec, f float64) error {
	if ps.Min != nil && f < *ps.Min {
		return validationErr(ErrPropValue, path,
			"prop %q is below its minimum of %v", ps.Name, *ps.Min)
	}
	if ps.Max != nil && f > *ps.Max {
		return validationErr(ErrPropValue, path,
			"prop %q is above its maximum of %v", ps.Name, *ps.Max)
	}
	return nil
}

// jsonKind names the decoded JSON shape of v for error messages.
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// asNumber widens a prop value to float64. JSON decoding always produces
// float64; Go-built compositions (fixtures, tests) may equally carry an int
// literal — both are numbers on the wire and both validate.
func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}
