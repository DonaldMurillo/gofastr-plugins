package ssr

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

// d3GroundTruth is a sweep recorded from the REAL d3-array 3.2.4 in Node
// (the exact implementation the frame bundle runs, via @observablehq/plot's
// dependency). Regenerate with, from chart/js (after npm install):
//
//	node --input-type=module -e "
//	import { ticks, tickStep } from 'd3-array';
//	// ...build cases, write {start,stop,count,step,ticks,labels} with
type d3Case struct {
	Start  float64   `json:"start"`
	Stop   float64   `json:"stop"`
	Count  float64   `json:"count"`
	Step   *float64  `json:"step"`
	Ticks  []float64 `json:"ticks"`
	Labels []string  `json:"labels"`
}

// The sweep covers the four awkward agreement domains (0–7, 0–1, −3.5–3.5,
// 0–1,000,000), 3000 pseudo-random domains spanning 1e-10…1e9, and the
// edge cases: reversed domains, degenerate (start === stop), and the
// count<2 doubling recursion. If this test fails, the Go port drifted from
// d3 and the server/client agreement is broken — fix the port, do not
// regenerate the fixture to match.

func loadD3GroundTruth(t *testing.T) []d3Case {
	t.Helper()
	raw, err := os.ReadFile("testdata/d3ticks.json")
	if err != nil {
		t.Fatalf("reading ground truth: %v", err)
	}
	var cases []d3Case
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parsing ground truth: %v", err)
	}
	if len(cases) < 1000 {
		t.Fatalf("ground truth looks truncated: %d cases", len(cases))
	}
	return cases
}

// TestTicksMatchD3GroundTruth is THE port-fidelity test: every tick value
// must match d3 bit-for-bit (float64 equality), and every label string must
// match what the frame renders.
func TestTicksMatchD3GroundTruth(t *testing.T) {
	for i, c := range loadD3GroundTruth(t) {
		got := Ticks(c.Start, c.Stop, c.Count)
		if c.Ticks == nil {
			c.Ticks = []float64{}
		}
		if len(got) != len(c.Ticks) {
			t.Fatalf("case %d ticks(%.17g, %.17g, %v): got %d ticks %v, d3 has %d: %v",
				i, c.Start, c.Stop, c.Count, len(got), got, len(c.Ticks), c.Ticks)
		}
		for j, v := range got {
			if v != c.Ticks[j] {
				t.Fatalf("case %d ticks(%.17g, %.17g, %v)[%d]: got %.17g, d3 has %.17g",
					i, c.Start, c.Stop, c.Count, j, v, c.Ticks[j])
			}
		}
		if c.Step != nil {
			if s := TickStep(c.Start, c.Stop, c.Count); s != *c.Step {
				t.Fatalf("case %d tickStep(%.17g, %.17g, %v): got %.17g, d3 has %.17g",
					i, c.Start, c.Stop, c.Count, s, *c.Step)
			}
		}
		if c.Labels != nil {
			// TickLabels takes the spec's integer count; rebuild the label
			// pipeline for fractional ground-truth counts directly.
			step := TickStep(c.Start, c.Stop, c.Count)
			p := tickStepLabelPrecision(step)
			var labels []string
			for _, v := range Ticks(c.Start, c.Stop, c.Count) {
				labels = append(labels, FormatFloatFixed(v, p))
			}
			if !reflect.DeepEqual(labels, c.Labels) {
				t.Fatalf("case %d labels(%.17g, %.17g, %v): got %q, d3 has %q",
					i, c.Start, c.Stop, c.Count, labels, c.Labels)
			}
		}
	}
}

// TestTicksAwkwardRanges pins the four awkward domains the agreement e2e
// uses, so a regression here fails in Go tests before it ever reaches a
// browser.
func TestTicksAwkwardRanges(t *testing.T) {
	cases := []struct {
		domain [2]float64
		want   []string
	}{
		{[2]float64{0, 7}, []string{"0.0", "0.5", "1.0", "1.5", "2.0", "2.5", "3.0", "3.5", "4.0", "4.5", "5.0", "5.5", "6.0", "6.5", "7.0"}},
		{[2]float64{0, 1}, []string{"0.0", "0.1", "0.2", "0.3", "0.4", "0.5", "0.6", "0.7", "0.8", "0.9", "1.0"}},
		{[2]float64{-3.5, 3.5}, []string{"-3.5", "-3.0", "-2.5", "-2.0", "-1.5", "-1.0", "-0.5", "0.0", "0.5", "1.0", "1.5", "2.0", "2.5", "3.0", "3.5"}},
		{[2]float64{0, 1e6}, []string{"0", "100000", "200000", "300000", "400000", "500000", "600000", "700000", "800000", "900000", "1000000"}},
	}
	for _, c := range cases {
		if got := TickLabels(c.domain, 10); !reflect.DeepEqual(got, c.want) {
			t.Errorf("TickLabels(%v, 10) = %q, want %q", c.domain, got, c.want)
		}
	}
}

// TestTicksEdgeCases pins the JS-semantics corners the port could get wrong.
func TestTicksEdgeCases(t *testing.T) {
	if got := Ticks(0, 1, 0); got != nil {
		t.Errorf("count=0: want nil, got %v", got)
	}
	if got := Ticks(0, 1, -3); got != nil {
		t.Errorf("count<0: want nil, got %v", got)
	}
	if got := Ticks(5, 5, 10); !reflect.DeepEqual(got, []float64{5}) {
		t.Errorf("start==stop: want [5], got %v", got)
	}
	if got := Ticks(math.NaN(), 1, 10); got != nil {
		t.Errorf("NaN start: want nil, got %v", got)
	}
	// Reversed domains come back descending, as d3 does.
	rev := Ticks(7, 0, 10)
	fwd := Ticks(0, 7, 10)
	if !reflect.DeepEqual(rev, reverseCopy(fwd)) {
		t.Errorf("reversed domain: %v is not %v reversed", rev, fwd)
	}
	// The count<2 recursion branch.
	if got := Ticks(0, 1, 0.6); !reflect.DeepEqual(got, []float64{0}) {
		t.Errorf("count=0.6: want [0], got %v", got)
	}
}

// TestFormatFloatFixedMatchesJSToFixed guards the negative-zero divergence:
// JS (-0).toFixed(1) === "0.0" while Go prints "-0.0" unless normalized.
func TestFormatFloatFixedMatchesJSToFixed(t *testing.T) {
	if got := FormatFloatFixed(math.Copysign(0, -1), 1); got != "0.0" {
		t.Errorf("(-0).toFixed(1): got %q, want %q", got, "0.0")
	}
	if got := FormatFloatFixed(2.5, 1); got != "2.5" {
		t.Errorf("2.5 fixed 1: got %q", got)
	}
	if got := FormatFloatFixed(100000, 0); got != "100000" {
		t.Errorf("100000 fixed 0: got %q", got)
	}
	// A sub-unit step label: 3 * 0.1 is 0.30000000000000004 as a double,
	// and toFixed(1) collapses it, as the frame does.
	if got := FormatFloatFixed(3*0.1, 1); got != "0.3" {
		t.Errorf("0.30000000000000004 fixed 1: got %q, want 0.3", got)
	}
}

// TestTickStepLabelPrecisionIsEngineIndependent pins the round-trip
// property: the precision must render the step exactly, for steps across
// every magnitude d3 produces.
func TestTickStepLabelPrecisionIsEngineIndependent(t *testing.T) {
	for _, step := range []float64{100000, 1, 0.5, 0.1, 0.2, 0.05, 0.01, 2, 5, 50, 1e-5, 1e-7} {
		p := tickStepLabelPrecision(step)
		if mustParseFloat(FormatFloatFixed(step, p)) != step {
			t.Errorf("precision %d does not round-trip step %v", p, step)
		}
	}
}

func reverseCopy(in []float64) []float64 {
	out := make([]float64, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}
