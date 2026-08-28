// Package ssr renders a chart-v1 spec to a standalone SVG in pure Go.
//
// It generalizes what richtext/ssr does for documents: the same canonical
// blob the interactive frame consumes is rendered server-side, so a page
// works with JavaScript off. The renderer is pure, deterministic (same spec
// → identical bytes), and depends on nothing outside the Go standard
// library plus the framework's core/render. The plugin's two renderers —
// this package (server SVG) and the Observable Plot frame (client SVG) —
// must AGREE on axis tick labels, series count, and data extents. Agreement
// on ticks is why ticks.go is a
// faithful port of d3-array's algorithm rather than a hand-rolled
// nice-number pass; see ticks.go for the citation.
package ssr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// SchemaVersion is the canonical-doc schema this renderer speaks.
const SchemaVersion = "chart-v1"

// Caps enforced at normalization. A chart's data lives in the doc (unlike
// the datagrid plugin) because a chart is small by definition — but "small"
// needs a hard edge, so normalization rejects specs past these caps.
// Both caps are part of the published contract; see docs/chart.md.
const (
	// MaxPoints is the maximum TOTAL number of points across all series.
	MaxPoints = 10000
	// MaxSeries is the maximum number of series in one chart.
	MaxSeries = 12
)

// Chart types supported by chart-v1. This is the whole set; a fifth type is
// out of scope by design (this is not a chart grammar).
const (
	TypeLine    = "line"
	TypeBar     = "bar"
	TypeArea    = "area"
	TypeScatter = "scatter"
)

// Point is one datum. Both coordinates are required and finite.
type Point struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

// Series is a named sequence of points.
type Series struct {
	Name   string  `json:"name"`
	Points []Point `json:"points"`
}

// Axis carries an optional caption and the approximate tick count. The tick
// count is passed VERBATIM to the tick algorithm on both renderers, which is
// what keeps server and client labels identical.
type Axis struct {
	Label     string `json:"label"`
	TickCount *int   `json:"tickCount"`
}

// Axes describes both axes.
type Axes struct {
	X Axis `json:"x"`
	Y Axis `json:"y"`
}

// Options carries rendering hints. Width/Height are the SVG viewBox size
// (the SVG itself is width="100%" and scales).
type Options struct {
	Legend *bool    `json:"legend,omitempty"`
	Width  *float64 `json:"width,omitempty"`
	Height *float64 `json:"height,omitempty"`
}

// Spec is the canonical chart document.
type Spec struct {
	SchemaVersion string   `json:"schemaVersion"`
	Type          string   `json:"type"`
	Title         string   `json:"title"`
	Series        []Series `json:"series"`
	Axes          Axes     `json:"axes"`
	Options       Options  `json:"options"`
}

// ParseSpec decodes and normalizes a chart-v1 spec from JSON. Every rule
// here has a twin in the frame bundle (chart/js/src/spec.ts); the two MUST
// stay in lockstep because agreement between the renderers is the product.
func ParseSpec(raw []byte) (Spec, error) {
	var s Spec
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("ssr: invalid spec JSON: %w", err)
	}
	return Normalize(s)
}

// Normalize validates and fills defaults. It is pure: no mutation of the
// input's backing arrays.
func Normalize(s Spec) (Spec, error) {
	if s.SchemaVersion == "" {
		s.SchemaVersion = SchemaVersion
	}
	if s.SchemaVersion != SchemaVersion {
		return Spec{}, fmt.Errorf("ssr: unknown schemaVersion %q (want %q)", s.SchemaVersion, SchemaVersion)
	}
	switch s.Type {
	case TypeLine, TypeBar, TypeArea, TypeScatter:
	default:
		return Spec{}, fmt.Errorf("ssr: unknown chart type %q (want line|bar|area|scatter)", s.Type)
	}
	if len(s.Series) == 0 {
		return Spec{}, fmt.Errorf("ssr: chart has no series")
	}
	if len(s.Series) > MaxSeries {
		return Spec{}, fmt.Errorf("ssr: %d series exceeds the %d-series cap", len(s.Series), MaxSeries)
	}
	total := 0
	for i := range s.Series {
		if s.Series[i].Name == "" {
			s.Series[i].Name = fmt.Sprintf("series %d", i+1)
		}
		if len(s.Series[i].Name) > maxTextRunes {
			return Spec{}, fmt.Errorf("ssr: series %d name exceeds %d characters", i+1, maxTextRunes)
		}
		for j, p := range s.Series[i].Points {
			if p.X == nil || p.Y == nil {
				return Spec{}, fmt.Errorf("ssr: series %q point %d: x and y are both required", s.Series[i].Name, j)
			}
			if math.IsNaN(*p.X) || math.IsInf(*p.X, 0) || math.IsNaN(*p.Y) || math.IsInf(*p.Y, 0) {
				return Spec{}, fmt.Errorf("ssr: series %q point %d: coordinates must be finite", s.Series[i].Name, j)
			}
		}
		total += len(s.Series[i].Points)
	}
	if total > MaxPoints {
		return Spec{}, fmt.Errorf("ssr: %d total points exceeds the %d-point cap", total, MaxPoints)
	}
	if n := len(s.Title); n > maxTextRunes {
		return Spec{}, fmt.Errorf("ssr: title exceeds %d characters", maxTextRunes)
	}
	if n := len(s.Axes.X.Label); n > maxTextRunes {
		return Spec{}, fmt.Errorf("ssr: x-axis label exceeds %d characters", maxTextRunes)
	}
	if n := len(s.Axes.Y.Label); n > maxTextRunes {
		return Spec{}, fmt.Errorf("ssr: y-axis label exceeds %d characters", maxTextRunes)
	}
	s.Axes.X.TickCount = clampTickCount(s.Axes.X.TickCount)
	s.Axes.Y.TickCount = clampTickCount(s.Axes.Y.TickCount)
	if s.Options.Legend == nil {
		def := true
		s.Options.Legend = &def
	}
	if s.Options.Width == nil {
		w := defaultWidth
		s.Options.Width = &w
	} else if *s.Options.Width < minWidth || *s.Options.Width > maxWidth {
		return Spec{}, fmt.Errorf("ssr: width %g outside [%g, %g]", *s.Options.Width, minWidth, maxWidth)
	}
	if s.Options.Height == nil {
		h := defaultHeight
		s.Options.Height = &h
	} else if *s.Options.Height < minHeight || *s.Options.Height > maxHeight {
		return Spec{}, fmt.Errorf("ssr: height %g outside [%g, %g]", *s.Options.Height, minHeight, maxHeight)
	}
	return s, nil
}

// maxTextRunes caps user text (title, axis labels, series names). Go's len
// counts bytes; cap on runes so multi-byte names get a fair allowance.
const maxTextRunes = 200

const (
	defaultWidth   = 720.0
	defaultHeight  = 420.0
	minWidth       = 200.0
	maxWidth       = 1200.0
	minHeight      = 120.0
	maxHeight      = 900.0
	defaultTicks   = 10
	minTicks, maxT = 2, 20
)

func clampTickCount(n *int) *int {
	if n == nil {
		v := defaultTicks
		return &v
	}
	if *n < minTicks {
		v := minTicks
		return &v
	}
	if *n > maxT {
		v := maxT
		return &v
	}
	return n
}

// Extents returns the x and y domains the renderers use. Both renderers
// compute domains from these rules (and the frame is given the domain
// explicitly), so they agree by construction:
//
//   - x: min..max of x across all series
//   - y: min..max of y across all series
//   - bar/area include the zero baseline (Plot's maybeZero does the same)
//   - bar pads x by half a bar group on each side, so the first and last
//     bars sit fully inside the plot instead of straddling the axis
//   - a degenerate extent widens to m-1..m+1
func Extents(s Spec) (x, y [2]float64) {
	has := false
	var x0, x1, y0, y1 float64
	for _, ser := range s.Series {
		for _, p := range ser.Points {
			if !has {
				x0, x1, y0, y1 = *p.X, *p.X, *p.Y, *p.Y
				has = true
				continue
			}
			x0 = math.Min(x0, *p.X)
			x1 = math.Max(x1, *p.X)
			y0 = math.Min(y0, *p.Y)
			y1 = math.Max(y1, *p.Y)
		}
	}
	if !has {
		return [2]float64{0, 1}, [2]float64{0, 1}
	}
	if s.Type == TypeBar || s.Type == TypeArea {
		y0 = math.Min(y0, 0)
		y1 = math.Max(y1, 0)
	}
	if x1 <= x0 {
		x0, x1 = x0-1, x0+1
	}
	if y1 <= y0 {
		y0, y1 = y0-1, y0+1
	}
	if s.Type == TypeBar {
		if g := minXGap(s); !math.IsInf(g, 1) {
			x0 -= 0.4 * g
			x1 += 0.4 * g
		}
	}
	x = [2]float64{x0, x1}
	y = [2]float64{y0, y1}
	return x, y
}

// minXGap returns the smallest positive gap between adjacent x values
// across all series (x values pooled and sorted; duplicates collapse to a
// zero gap and are skipped). +Inf when fewer than two distinct x values
// exist. It sizes bar groups and the bar-type x padding; the frame bundle's
// twin (chart/js/src/spec.ts minXGap) computes the identical double — the
// agreement test compares the padded data-domain-x attributes, so the two
// implementations must stay in lockstep.
func minXGap(s Spec) float64 {
	var xs []float64
	for _, ser := range s.Series {
		for _, p := range ser.Points {
			xs = append(xs, *p.X)
		}
	}
	sort.Float64s(xs)
	minGap := math.Inf(1)
	for i := 1; i < len(xs); i++ {
		if g := xs[i] - xs[i-1]; g > 0 && g < minGap {
			minGap = g
		}
	}
	return minGap
}
