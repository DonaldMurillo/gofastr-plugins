package ssr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr/core/render"
)

// Golden tests: fixture specs render to stable SVG, byte-compared against
// committed files in testdata/. Regenerate intentionally with:
//
//	go test ./chart/ssr -run TestGolden -update
//
// (richtext/ssr pins behavior with contains-assertions; chart/ssr has a
// stronger contract — byte-stable output — because the agreement test
// compares this SVG's tick labels against the client's, so ANY byte drift
// in the tick text is a behavioral change worth reviewing.)
var updateGoldens = os.Getenv("UPDATE") == "1"

// pts builds points from flattened x,y pairs.
func pts(xy ...float64) []Point {
	out := make([]Point, 0, len(xy)/2)
	for i := 0; i+1 < len(xy); i += 2 {
		out = append(out, Point{X: new(xy[i]), Y: new(xy[i+1])})
	}
	return out
}

// specFor builds a normalized spec with defaults applied.
func specFor(t *testing.T, s Spec) Spec {
	t.Helper()
	n, err := Normalize(s)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return n
}

var goldenSpecs = map[string]Spec{
	"line-awkward": {
		Type:          TypeLine,
		SchemaVersion: SchemaVersion,
		Title:         "Signups by week",
		Axes:          Axes{X: Axis{Label: "week"}, Y: Axis{Label: "signups"}},
		Series: []Series{
			{Name: "Alpha", Points: pts(0, 1, 1, 3, 2, 2, 3, 5, 4, 4, 5, 6, 6, 7)},
			{Name: "Beta", Points: pts(0, 0.5, 1, 1.5, 2, 1, 3, 2.5, 4, 2, 5, 3.5, 6, 4)},
		},
	},
	"bar-fractional": {
		Type: TypeBar,
		Axes: Axes{X: Axis{Label: "bucket"}, Y: Axis{Label: "rate"}},
		Series: []Series{
			{Name: "hit rate", Points: pts(0, 0.1, 1, 0.35, 2, 0.5, 3, 0.42, 4, 0.9, 5, 1)},
		},
	},
	"area-negative": {
		Type:  TypeArea,
		Title: "Drift",
		Series: []Series{
			{Name: "delta", Points: pts(-3, -3.5, -2, -1, -1, 0.5, 0, 2.2, 1, -0.4, 2, 3.5, 3, 1)},
		},
	},
	"scatter-millions": {
		Type: TypeScatter,
		Axes: Axes{Y: Axis{Label: "requests"}},
		Series: []Series{
			{Name: "rps", Points: pts(0, 0, 1, 120000, 2, 480000, 3, 760000, 4, 1000000)},
			{Name: "errors", Points: pts(0, 0, 1, 9000, 2, 41000, 3, 300000, 4, 640000)},
		},
	},
	// The hostile-input fixture: every user-visible string carries markup.
	// The golden bytes prove the escaping discipline end to end.
	"hostile-labels": {
		Type:  TypeScatter,
		Title: `T"tle <script>alert(1)</script> & </title>`,
		Axes:  Axes{X: Axis{Label: `x" on_load="boom`}, Y: Axis{Label: `y<&>'`}},
		Series: []Series{
			{Name: `<script>alert("name")</script> 'q'`, Points: pts(0, 0, 1, 1)},
			{Name: `b & c <b>`, Points: pts(0, 1, 1, 0)},
		},
	},
	// No-legend, custom size, custom tick count.
	"line-no-legend": {
		Type:    TypeLine,
		Axes:    Axes{X: Axis{TickCount: new(5)}, Y: Axis{TickCount: new(4)}},
		Options: Options{Legend: new(false), Width: new(480.0), Height: new(320.0)},
		Series: []Series{
			{Name: "solo", Points: pts(0, 2, 1, 3, 2, 3.5)},
		},
	},
}

func TestGoldenRenders(t *testing.T) {
	for name, spec := range goldenSpecs {
		t.Run(name, func(t *testing.T) {
			out := string(Render(specFor(t, spec)))
			golden := filepath.Join("testdata", name+".golden.svg")
			if updateGoldens {
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading golden (run with UPDATE=1 to create): %v", err)
			}
			if out != string(want) {
				t.Errorf("render drift for %s:\n--- want (%d bytes)\n%s\n--- got (%d bytes)\n%s",
					name, len(want), truncate(string(want)), len(out), truncate(out))
			}
		})
	}
}

func truncate(s string) string {
	if len(s) > 1600 {
		return s[:1600] + "…"
	}
	return s
}

// TestRenderDeterministic pins the same-spec-same-bytes invariant.
func TestRenderDeterministic(t *testing.T) {
	s := specFor(t, goldenSpecs["line-awkward"])
	if Render(s) != Render(s) {
		t.Fatal("Render is not deterministic for the same spec")
	}
}

// TestRenderEscapesUserData is the injection gate: nothing user-authored
// may reach the markup unescaped. Raw quotes can never survive
// render.Escape, so an attribute breakout like on_load=" is structurally
// impossible; the needles check the realistic breakouts.
func TestRenderEscapesUserData(t *testing.T) {
	out := string(Render(specFor(t, goldenSpecs["hostile-labels"])))
	for _, needle := range []string{"<script", `on_load="`, "javascript:"} {
		if strings.Contains(out, needle) {
			t.Errorf("unescaped %q leaked into SVG output", needle)
		}
	}
	// The hostile title contains a literal </title>; exactly ONE real
	// <title> pair may exist in the output, or the user data broke out.
	if strings.Count(out, "<title>") != 1 || strings.Count(out, "</title>") != 1 {
		t.Error("hostile title broke out of the <title> element")
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;", "&quot;", "&#39;"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected escaped entity %s in output", want)
		}
	}
}

// TestRenderAgreementHooks verifies the machine-readable surfaces the e2e
// agreement test reads: tick labels under data-axis groups, per-series
// data-series groups, legend items, and the data-domain extents.
func TestRenderAgreementHooks(t *testing.T) {
	out := string(Render(specFor(t, goldenSpecs["line-awkward"])))
	for _, want := range []string{
		`data-axis="x"`,
		`data-axis="y"`,
		`data-domain-y="0.5,7"`,
		`data-series="Alpha"`,
		`data-series="Beta"`,
		`data-legend-item`,
		`data-domain-x="0,6"`,
		`data-domain-y="0.5,7"`,
		`>0.0</text>`,
		`>7.0</text>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agreement hook missing from SVG: %q", want)
		}
	}
}

// TestRenderJSONRoundTrip covers the convenience entry point.
func TestRenderJSONRoundTrip(t *testing.T) {
	raw := `{"type":"line","series":[{"name":"a","points":[{"x":0,"y":0},{"x":1,"y":1}]}]}`
	out, err := RenderJSON([]byte(raw))
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(string(out), "gofastr-chart-svg") {
		t.Error("RenderJSON did not produce an SVG root")
	}
	if _, err := RenderJSON([]byte(`{"type":"nope"}`)); err == nil {
		t.Error("RenderJSON accepted an invalid spec")
	}
}

// ─── Normalize / Extents ───────────────────────────────────────────────

func TestNormalizeRejects(t *testing.T) {
	bad := []string{"unknown type", "no series", "bad schema", "missing y", "too many series", "non-finite", "too long name"}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			var s Spec
			switch name {
			case "unknown type":
				s = Spec{Type: "pie", Series: []Series{{Points: pts(0, 0)}}}
			case "no series":
				s = Spec{Type: TypeLine}
			case "bad schema":
				s = Spec{SchemaVersion: "chart-v2", Type: TypeLine, Series: []Series{{Points: pts(0, 0)}}}
			case "missing y":
				s = Spec{Type: TypeLine, Series: []Series{{Points: []Point{{X: new(0.0)}}}}}
			case "too many series":
				s = Spec{Type: TypeLine}
				for range MaxSeries + 1 {
					s.Series = append(s.Series, Series{Points: pts(0, 0)})
				}
			case "non-finite":
				inf := infValue()
				s = Spec{Type: TypeLine, Series: []Series{{Points: []Point{{X: new(0.0), Y: &inf}}}}}
			case "too long name":
				s = Spec{Type: TypeLine, Series: []Series{{Name: strings.Repeat("n", 201), Points: pts(0, 0)}}}
			}
			if _, err := Normalize(s); err == nil {
				t.Errorf("Normalize accepted %s", name)
			}
		})
	}
}

func TestNormalizePointCap(t *testing.T) {
	s := Spec{Type: TypeLine}
	big := make([]Point, MaxPoints+1)
	for i := range big {
		big[i] = Point{X: new(float64(i)), Y: new(float64(i))}
	}
	s.Series = []Series{{Name: "big", Points: big}}
	if _, err := Normalize(s); err == nil {
		t.Error("Normalize accepted more than the point cap")
	}
}

func TestNormalizeDefaults(t *testing.T) {
	s, err := Normalize(Spec{Type: TypeBar, Series: []Series{{Points: pts(1, 2)}}})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("schemaVersion = %q", s.SchemaVersion)
	}
	if s.Series[0].Name != "series 1" {
		t.Errorf("unnamed series = %q", s.Series[0].Name)
	}
	if *s.Axes.X.TickCount != 10 || *s.Axes.Y.TickCount != 10 {
		t.Errorf("default tick counts = %d/%d", *s.Axes.X.TickCount, *s.Axes.Y.TickCount)
	}
	if !*s.Options.Legend {
		t.Error("legend should default on")
	}
	if *s.Options.Width != 720 || *s.Options.Height != 420 {
		t.Errorf("default size = %v×%v", *s.Options.Width, *s.Options.Height)
	}
	// Clamps.
	tc := 99
	s.Axes.Y.TickCount = &tc
	n2, err := Normalize(s)
	if err != nil || *n2.Axes.Y.TickCount != 20 {
		t.Errorf("tickCount clamp: %v %d", err, *n2.Axes.Y.TickCount)
	}
}

func TestExtentsRules(t *testing.T) {
	// bar/area include the zero baseline (mirrors Plot's maybeZero).
	s := specFor(t, Spec{Type: TypeBar, Series: []Series{{Points: pts(0, 2, 1, 5)}}})
	_, y := Extents(s)
	if y != [2]float64{0, 5} {
		t.Errorf("bar y extents = %v, want [0 5]", y)
	}
	// line does not.
	s.Type = TypeLine
	s, _ = Normalize(s)
	_, y = Extents(s)
	if y != [2]float64{2, 5} {
		t.Errorf("line y extents = %v, want [2 5]", y)
	}
	// negative bars widen downward through zero.
	s2 := specFor(t, Spec{Type: TypeBar, Series: []Series{{Points: pts(0, -4, 1, -1)}}})
	_, y2 := Extents(s2)
	if y2 != [2]float64{-4, 0} {
		t.Errorf("negative bar y extents = %v, want [-4 0]", y2)
	}
	// degenerate domains widen.
	s3 := specFor(t, Spec{Type: TypeLine, Series: []Series{{Points: pts(3, 3)}}})
	x3, y3 := Extents(s3)
	if x3 != [2]float64{2, 4} || y3 != [2]float64{2, 4} {
		t.Errorf("degenerate extents = %v %v", x3, y3)
	}
	// empty chart gets unit domains.
	s4 := specFor(t, Spec{Type: TypeScatter, Series: []Series{{}}})
	x4, y4 := Extents(s4)
	if x4 != [2]float64{0, 1} || y4 != [2]float64{0, 1} {
		t.Errorf("empty extents = %v %v", x4, y4)
	}
}

// TestTickLabelsPresentInEveryGolden ticks the golden files themselves: if
// a golden has an axis group, it must carry tick labels.
func TestTickLabelsPresentInEveryGolden(t *testing.T) {
	for name, spec := range goldenSpecs {
		out := string(Render(specFor(t, spec)))
		if !strings.Contains(out, `data-axis="x"`) || !strings.Contains(out, `data-axis="y"`) {
			t.Errorf("%s: missing axis groups", name)
		}
		if !strings.Contains(out, `gofastr-chart-tick-label`) {
			t.Errorf("%s: missing tick labels", name)
		}
	}
}

var _ render.HTML

func infValue() float64 { return 1 / zero() }
func zero() float64     { return 0 }
