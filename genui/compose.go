package genui

import (
	"context"
	"strings"
)

// compose.go — the Composer seam and the deterministic default.
//
// The model runs HOST-side, never in the frame: an API key in a browser is
// not a key, and a frame that could call a model could exfiltrate the
// document it was composing over (the framed CSP's connect-src 'none' is
// what keeps that direction impossible). [Composer] is therefore a HOST
// interface: the plugin calls it from the POST /compose route, validates
// whatever comes back, and only then stores and serves the tree.
//
// [FixtureComposer] is the default implementation: deterministic, offline,
// keyless — a small prompt→composition table plus a fallback card. The demo
// and every test use it, because a plugin whose tests need an API key is a
// plugin nobody can contribute to. A real model client goes behind the same
// interface later via [WithComposer] and is NOT in scope here.

// Composer produces a composition for a prompt. Implementations run host-
// side, may take arbitrarily long (the route polls), and must expect their
// output to be validated against r before anything persists — a Composer
// never gets to bypass the registry by construction.
type Composer interface {
	Compose(ctx context.Context, prompt string, r Registry) (Composition, error)
}

// FixtureComposer is the deterministic offline default: keyword matching
// over a fixed fixture table, then a fallback card for anything else. No
// network, no key, no randomness — the same prompt always yields the same
// tree, which is what makes the demo and the tests reproducible.
type FixtureComposer struct{}

// fixture is one table row: any keyword contained in the normalized prompt
// selects it. Rows are scanned in order, first match wins.
type fixture struct {
	keywords []string
	build    func() Composition
}

// fixtures is the prompt→composition table. Every fixture must itself pass
// [Validate] against [DefaultRegistry] and [DefaultActions] — the tests
// assert exactly that, so the table and the registry cannot drift apart.
var fixtures = []fixture{
	{
		keywords: []string{"revenue", "q3", "dashboard", "quarter"},
		build:    fixtureDashboard,
	},
	{
		keywords: []string{"table", "compare", "comparison", "plans", "pricing"},
		build:    fixtureComparisonTable,
	},
	{
		keywords: []string{"status", "health", "systems", "incident"},
		build:    fixtureStatus,
	},
}

// Compose maps prompt to a fixture composition. The prompt is normalized
// (lowercased, whitespace-collapsed) before keyword matching; anything that
// matches no row gets the fallback card, never an error — "I did not
// understand that" is a renderable answer, a failed generation is not.
func (FixtureComposer) Compose(_ context.Context, prompt string, _ Registry) (Composition, error) {
	norm := normalizePrompt(prompt)
	for _, f := range fixtures {
		for _, kw := range f.keywords {
			if strings.Contains(norm, kw) {
				return f.build(), nil
			}
		}
	}
	return fixtureFallback(), nil
}

func normalizePrompt(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// fixtureDashboard is the contract's own example tree (see the package doc):
// a stacked dashboard view with a heading, two stats and an allow-listed
// export button.
func fixtureDashboard() Composition {
	return Composition{
		SchemaVersion: SchemaVersion,
		Root: &Node{
			Component: "Stack",
			Props:     map[string]any{"gap": "lg", "direction": "column"},
			Children: []Node{
				{Component: "Heading", Props: map[string]any{"text": "Q3 revenue", "level": 2}},
				{Component: "Stat", Props: map[string]any{"label": "Revenue", "value": "$1.2M", "delta": 12.5}},
				{Component: "Stat", Props: map[string]any{"label": "Costs", "value": "$410K", "delta": -3.2}},
				{Component: "Button", Props: map[string]any{"label": "Export", "variant": "primary"}, Action: "export"},
			},
		},
	}
}

// fixtureComparisonTable exercises the Table component: structured rows are
// declared props, validated cell by cell, never markup.
func fixtureComparisonTable() Composition {
	return Composition{
		SchemaVersion: SchemaVersion,
		Root: &Node{
			Component: "Stack",
			Props:     map[string]any{"gap": "md", "direction": "column"},
			Children: []Node{
				{Component: "Heading", Props: map[string]any{"text": "Plan comparison", "level": 2}},
				{
					Component: "Table",
					Props: map[string]any{
						"columns": []any{"Plan", "Price", "Seats"},
						"rows": []any{
							[]any{"Starter", "$9", "1"},
							[]any{"Team", "$29", "10"},
							[]any{"Scale", "$99", "50"},
						},
					},
				},
				{Component: "Badge", Props: map[string]any{"label": "Best value: Team", "tone": "good"}},
			},
		},
	}
}

// fixtureStatus exercises Card, Text's tone vocabulary and Badge's.
func fixtureStatus() Composition {
	return Composition{
		SchemaVersion: SchemaVersion,
		Root: &Node{
			Component: "Card",
			Props:     map[string]any{"title": "Status"},
			Children: []Node{
				{Component: "Badge", Props: map[string]any{"label": "All systems normal", "tone": "good"}},
				{Component: "Text", Props: map[string]any{"text": "Last check ran 30 seconds ago across 4 regions.", "tone": "muted"}},
			},
		},
	}
}

// fixtureFallback is the "I did not understand that" card every
// unrecognized prompt gets.
func fixtureFallback() Composition {
	return Composition{
		SchemaVersion: SchemaVersion,
		Root: &Node{
			Component: "Card",
			Props:     map[string]any{"title": "Not understood"},
			Children: []Node{
				{Component: "Text", Props: map[string]any{"text": "I did not understand that. Ask for a dashboard, a comparison table, or a status card."}},
			},
		},
	}
}
