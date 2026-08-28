package ssr

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/gofastr/core/render"
)

// Render turns a NORMALIZED spec into a standalone SVG. Pure and
// deterministic: the same spec always yields the same bytes (pinned by the
// golden tests). The SVG is themed through the host page's design tokens
// with conservative fallbacks, so it is correct on its own — that is the
// whole point of the SSR path.
func Render(s Spec) render.HTML {
	xDom, yDom := Extents(s)
	W, H := *s.Options.Width, *s.Options.Height

	// ── geometry ───────────────────────────────────────────────────────
	// Fixed, boring margins. The SVG scales via viewBox; nothing here needs
	// to be clever.
	const (
		padLeft   = 56.0
		padRight  = 18.0
		padTop    = 12.0
		padBottom = 10.0
		xTickH    = 22.0 // tick line + label
		labelH    = 16.0 // axis caption
		headerH   = 30.0 // title row (shared with the legend)
	)

	hasHeader := s.Title != "" || *s.Options.Legend
	bottomExtra := xTickH
	if s.Axes.X.Label != "" {
		bottomExtra += labelH
	}
	top := padTop
	if hasHeader {
		top += headerH
	}
	plotL, plotR := padLeft, W-padRight
	plotT, plotB := top, H-padBottom-bottomExtra
	plotW, plotH := plotR-plotL, plotB-plotT

	sx := func(v float64) float64 { return plotL + (v-xDom[0])/(xDom[1]-xDom[0])*plotW }
	sy := func(v float64) float64 { return plotB - (v-yDom[0])/(yDom[1]-yDom[0])*plotH }

	xTicks := Ticks(xDom[0], xDom[1], float64(*s.Axes.X.TickCount))
	yTicks := Ticks(yDom[0], yDom[1], float64(*s.Axes.Y.TickCount))
	xLabels := TickLabels(xDom, *s.Axes.X.TickCount)
	yLabels := TickLabels(yDom, *s.Axes.Y.TickCount)

	var b strings.Builder
	b.Grow(4096)

	aria := s.Title
	if aria == "" {
		aria = "chart"
	}
	fmt.Fprintf(&b,
		`<svg class="gofastr-chart-svg" viewBox="0 0 %s %s" width="100%%" role="img" aria-label="%s" data-domain-x="%s" data-domain-y="%s" aria-description="Server-rendered chart (chart-v1).">`,
		fnum(W), fnum(H), render.Escape(aria), fdom(xDom), fdom(yDom))
	b.WriteString(chartStyleCSS)

	// <title> for the accessibility tree.
	b.WriteString(`<title>`)
	b.WriteString(render.Escape(aria))
	b.WriteString(`</title>`)

	// ── header: title (left) + legend (right) ──────────────────────────
	if hasHeader {
		if s.Title != "" {
			fmt.Fprintf(&b,
				`<text class="gofastr-chart-title" x="%s" y="%s">%s</text>`,
				fnum(plotL), fnum(top-9), render.Escape(s.Title))
		}
		if *s.Options.Legend {
			writeLegend(&b, s, W-padRight, top-9)
		}
	}

	// ── grid (under everything) ────────────────────────────────────────
	b.WriteString(`<g class="gofastr-chart-grid" aria-hidden="true">`)
	for _, v := range yTicks {
		yy := sy(v)
		fmt.Fprintf(&b, `<line x1="%s" y1="%s" x2="%s" y2="%s"/>`,
			fnum(plotL), fnum(yy), fnum(plotR), fnum(yy))
	}
	for _, v := range xTicks {
		xx := sx(v)
		fmt.Fprintf(&b, `<line x1="%s" y1="%s" x2="%s" y2="%s"/>`,
			fnum(xx), fnum(plotT), fnum(xx), fnum(plotB))
	}
	b.WriteString(`</g>`)

	// ── series ─────────────────────────────────────────────────────────
	for i, ser := range s.Series {
		fmt.Fprintf(&b, `<g class="gofastr-chart-series gofastr-chart-s%d" data-series="%s" aria-label="%s">`,
			i%MaxSeries, render.Escape(ser.Name), render.Escape(ser.Name))
		switch s.Type {
		case TypeLine:
			pts := sortedByX(ser.Points)
			writePolyline(&b, pts, sx, sy)
			writeDots(&b, pts, sx, sy, 3.5)
		case TypeArea:
			pts := sortedByX(ser.Points)
			writeArea(&b, pts, sx, sy, sy(0))
			writeDots(&b, pts, sx, sy, 3.5)
		case TypeBar:
			writeBars(&b, s, i, sx, sy, yDom)
		case TypeScatter:
			for _, p := range ser.Points {
				fmt.Fprintf(&b, `<circle class="gofastr-chart-dot" cx="%s" cy="%s" r="4"/>`,
					fnum(sx(*p.X)), fnum(sy(*p.Y)))
			}
		}
		b.WriteString(`</g>`)
	}

	// ── axes (over the marks so data never hides the frame) ────────────
	writeXAxis(&b, s, xTicks, xLabels, sx, plotL, plotR, plotB, H-padBottom)
	writeYAxis(&b, s, yTicks, yLabels, sy, plotL, plotT, plotB)

	b.WriteString(`</svg>`)
	return render.HTML(b.String())
}

// RenderJSON parses then renders. Convenience for handlers.
func RenderJSON(raw []byte) (render.HTML, error) {
	s, err := ParseSpec(raw)
	if err != nil {
		return "", err
	}
	return Render(s), nil
}

// ── pieces ────────────────────────────────────────────────────────────

func writeXAxis(b *strings.Builder, s Spec, ticks []float64, labels []string, sx func(float64) float64, l, r, axisY, labelY float64) {
	b.WriteString(`<g class="gofastr-chart-axis gofastr-chart-axis-x" data-axis="x">`)
	fmt.Fprintf(b, `<line class="gofastr-chart-axis-line" x1="%s" y1="%s" x2="%s" y2="%s"/>`,
		fnum(l), fnum(axisY), fnum(r), fnum(axisY))
	for i, v := range ticks {
		xx := sx(v)
		fmt.Fprintf(b, `<line class="gofastr-chart-tick" x1="%s" y1="%s" x2="%s" y2="%s"/>`,
			fnum(xx), fnum(axisY), fnum(xx), fnum(axisY+5))
		fmt.Fprintf(b, `<text class="gofastr-chart-tick-label" x="%s" y="%s" text-anchor="middle">%s</text>`,
			fnum(xx), fnum(axisY+16), render.Escape(labelAt(labels, i)))
	}
	if s.Axes.X.Label != "" {
		fmt.Fprintf(b, `<text class="gofastr-chart-axis-label" x="%s" y="%s" text-anchor="middle">%s</text>`,
			fnum((l+r)/2), fnum(labelY), render.Escape(s.Axes.X.Label))
	}
	b.WriteString(`</g>`)
}

func writeYAxis(b *strings.Builder, s Spec, ticks []float64, labels []string, sy func(float64) float64, axisX, t, bot float64) {
	b.WriteString(`<g class="gofastr-chart-axis gofastr-chart-axis-y" data-axis="y">`)
	fmt.Fprintf(b, `<line class="gofastr-chart-axis-line" x1="%s" y1="%s" x2="%s" y2="%s"/>`,
		fnum(axisX), fnum(t), fnum(axisX), fnum(bot))
	for i, v := range ticks {
		yy := sy(v)
		fmt.Fprintf(b, `<line class="gofastr-chart-tick" x1="%s" y1="%s" x2="%s" y2="%s"/>`,
			fnum(axisX-5), fnum(yy), fnum(axisX), fnum(yy))
		fmt.Fprintf(b, `<text class="gofastr-chart-tick-label" x="%s" y="%s" text-anchor="end">%s</text>`,
			fnum(axisX-8), fnum(yy+3.5), render.Escape(labelAt(labels, i)))
	}
	if s.Axes.Y.Label != "" {
		cy := (t + bot) / 2
		fmt.Fprintf(b, `<text class="gofastr-chart-axis-label" x="%s" y="%s" text-anchor="middle" transform="rotate(-90 %s %s)">%s</text>`,
			fnum(14), fnum(cy), fnum(14), fnum(cy), render.Escape(s.Axes.Y.Label))
	}
	b.WriteString(`</g>`)
}

func writeLegend(b *strings.Builder, s Spec, right, y float64) {
	b.WriteString(`<g class="gofastr-chart-legend" data-legend>`)
	// Right-aligned row: positions are computed from the right edge
	// backwards, but items are EMITTED in series order so the DOM order of
	// [data-legend-name] matches the frame legend (and the series groups).
	// Width is estimated at ~7px per rune (11-12px font); only the layout
	// changes if the estimate is off, never the bytes.
	//
	// Note the attribute is data-legend-name, NOT data-series: data-series
	// is reserved for the series mark groups so `[data-series]` selects
	// exactly one element per series.
	xs := make([]float64, len(s.Series))
	x := right
	for i := len(s.Series) - 1; i >= 0; i-- {
		name := s.Series[i].Name
		x -= 12 + 6 + 7*float64(len([]rune(name))) + 14
		xs[i] = x
	}
	for i, ser := range s.Series {
		fmt.Fprintf(b, `<g class="gofastr-chart-legend-item gofastr-chart-s%d" data-legend-item data-legend-name="%s" transform="translate(%s %s)">`,
			i%MaxSeries, render.Escape(ser.Name), fnum(xs[i]), fnum(y-11))
		b.WriteString(`<rect class="gofastr-chart-legend-swatch" width="12" height="12" rx="2"/>`)
		fmt.Fprintf(b, `<text class="gofastr-chart-legend-text" x="18" y="11">%s</text>`, render.Escape(ser.Name))
		b.WriteString(`</g>`)
	}
	b.WriteString(`</g>`)
}

func writePolyline(b *strings.Builder, pts []Point, sx, sy func(float64) float64) {
	if len(pts) == 0 {
		return
	}
	var pb strings.Builder
	for i, p := range pts {
		if i > 0 {
			pb.WriteByte(' ')
		}
		pb.WriteString(fnum(sx(*p.X)))
		pb.WriteByte(',')
		pb.WriteString(fnum(sy(*p.Y)))
	}
	fmt.Fprintf(b, `<polyline class="gofastr-chart-line" points="%s"/>`, pb.String())
}

// writeDots beads each data point with a small dot so the values are
// readable on line and area charts — the same treatment the hydrated Plot
// chart gives (the frame draws r=3.5 dots on line/area too).
func writeDots(b *strings.Builder, pts []Point, sx, sy func(float64) float64, r float64) {
	for _, p := range pts {
		fmt.Fprintf(b, `<circle class="gofastr-chart-dot" cx="%s" cy="%s" r="%s"/>`,
			fnum(sx(*p.X)), fnum(sy(*p.Y)), fnum(r))
	}
}

func writeArea(b *strings.Builder, pts []Point, sx, sy func(float64) float64, baseY float64) {
	if len(pts) == 0 {
		return
	}
	var pb strings.Builder
	pb.WriteString("M")
	pb.WriteString(fnum(sx(*pts[0].X)))
	pb.WriteByte(',')
	pb.WriteString(fnum(sy(*pts[0].Y)))
	for _, p := range pts[1:] {
		pb.WriteString(" L")
		pb.WriteString(fnum(sx(*p.X)))
		pb.WriteByte(',')
		pb.WriteString(fnum(sy(*p.Y)))
	}
	last := pts[len(pts)-1]
	pb.WriteString(" L")
	pb.WriteString(fnum(sx(*last.X)))
	pb.WriteByte(',')
	pb.WriteString(fnum(baseY))
	pb.WriteString(" L")
	pb.WriteString(fnum(sx(*pts[0].X)))
	pb.WriteByte(',')
	pb.WriteString(fnum(baseY))
	pb.WriteString(" Z")
	fmt.Fprintf(b, `<path class="gofastr-chart-area" d="%s"/>`, pb.String())
	writePolyline(b, pts, sx, sy)
}

// writeBars draws series seriesIdx as dodged bars: the group at each x
// spans 0.8× the smallest adjacent-x gap, split into one slot per series;
// each bar fills 90% of its slot, centered on its slot. The frame bundle
// computes the identical layout in data units (chart/js/src/chart.ts), so
// both renderers draw the same side-by-side bars.
func writeBars(b *strings.Builder, s Spec, seriesIdx int, sx, sy func(float64) float64, yDom [2]float64) {
	ser := s.Series[seriesIdx]
	if len(ser.Points) == 0 {
		return
	}
	n := len(s.Series)
	zero := sy(math.Min(math.Max(0, yDom[0]), yDom[1]))

	var barRect func(px float64) (left, width float64)
	if g := minXGap(s); math.IsInf(g, 1) {
		// Single distinct x: no gap to scale from. Fixed 24px bars,
		// dodged by 24px slots (pinned by the golden tests).
		slot := 24.0
		off := (float64(seriesIdx) - (float64(n)-1)/2) * slot
		barRect = func(px float64) (float64, float64) { return sx(px) + off - 12, 24 }
	} else {
		slot := 0.8 * g / float64(n)
		barW := 0.9 * slot
		off := (float64(seriesIdx) - (float64(n)-1)/2) * slot
		barRect = func(px float64) (float64, float64) {
			l, r := sx(px+off-barW/2), sx(px+off+barW/2)
			return l, r - l
		}
	}
	for _, p := range ser.Points {
		yy := sy(*p.Y)
		top, h := yy, zero-yy
		if h < 0 {
			top, h = zero, yy-zero
		}
		l, w := barRect(*p.X)
		fmt.Fprintf(b, `<rect class="gofastr-chart-bar" x="%s" y="%s" width="%s" height="%s"/>`,
			fnum(l), fnum(top), fnum(w), fnum(math.Max(h, 0)))
	}
}

func sortedByX(pts []Point) []Point {
	out := append([]Point(nil), pts...)
	sort.SliceStable(out, func(i, j int) bool { return *out[i].X < *out[j].X })
	return out
}

func labelAt(labels []string, i int) string {
	if i < len(labels) {
		return labels[i]
	}
	return ""
}

// fnum formats a pixel coordinate deterministically (2 fraction digits).
func fnum(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// fdom formats a domain pair for the data-domain-* attributes. The frame
// sets the twin attributes from the same doubles; the e2e agreement test
// parses both back to numbers, so shortest-round-trip is the right format.
func fdom(d [2]float64) string {
	return strconv.FormatFloat(d[0], 'g', -1, 64) + "," + strconv.FormatFloat(d[1], 'g', -1, 64)
}

// chartStyleCSS themes the SVG through the HOST page's tokens with literal
// fallbacks, so the static chart is presentable standalone and flips with
// light/dark for free. Series slots 0–4 read distinct semantic color tokens
// series are told apart at 1px stroke; slots 5–11 are color-mix derivations
// of the same tokens (the frame bundle computes the same mixes from the
// bridged token values — see chart/js/src/palette.ts).
const chartStyleCSS = `<style>
.gofastr-chart-svg{--gcc-bg:var(--color-background,#fff);--gcc-text:var(--color-text,#1c2024);--gcc-muted:var(--color-text-muted,#5b6470);--gcc-border:var(--color-border,#e3e6ea);--gcc-primary:var(--color-primary,#4f46e5);--gcc-info:var(--color-info,#1d4ed8);--gcc-success:var(--color-success,#166534);--gcc-accent:var(--color-accent,#7c3aed);--gcc-danger:var(--color-danger,#b91c1c);font-family:var(--font-body,system-ui,-apple-system,sans-serif);font-size:12px;display:block}
.gofastr-chart-svg text{fill:var(--gcc-text)}
.gofastr-chart-title{font-size:15px;font-weight:600}
.gofastr-chart-grid line{stroke:var(--gcc-border);stroke-width:1;stroke-opacity:.6}
.gofastr-chart-axis-line{stroke:var(--gcc-muted);stroke-width:1}
.gofastr-chart-tick{stroke:var(--gcc-muted);stroke-width:1}
.gofastr-chart-tick-label{font-size:11px;fill:var(--gcc-muted)}
.gofastr-chart-axis-label{font-size:11px;fill:var(--gcc-muted)}
.gofastr-chart-legend-text{font-size:12px}
.gofastr-chart-legend-swatch{fill:currentColor;stroke:none}
.gofastr-chart-line{fill:none;stroke:currentColor;stroke-width:2.5;stroke-linejoin:round;stroke-linecap:round}
.gofastr-chart-area{fill:currentColor;fill-opacity:.18;stroke:none}
.gofastr-chart-bar{fill:currentColor;stroke:none}
.gofastr-chart-dot{fill:currentColor;stroke:var(--gcc-bg);stroke-width:1.5}
.gofastr-chart-s0{color:var(--gcc-primary)}
.gofastr-chart-s1{color:var(--gcc-info)}
.gofastr-chart-s2{color:var(--gcc-success)}
.gofastr-chart-s3{color:var(--gcc-accent)}
.gofastr-chart-s4{color:var(--gcc-danger)}
.gofastr-chart-s5{color:color-mix(in srgb,var(--gcc-info) 55%,var(--gcc-text))}
.gofastr-chart-s6{color:color-mix(in srgb,var(--gcc-primary) 45%,var(--gcc-text))}
.gofastr-chart-s7{color:color-mix(in srgb,var(--gcc-success) 45%,var(--gcc-text))}
.gofastr-chart-s8{color:color-mix(in srgb,var(--gcc-danger) 50%,var(--gcc-text))}
.gofastr-chart-s9{color:color-mix(in srgb,var(--gcc-accent) 50%,var(--gcc-text))}
.gofastr-chart-s10{color:color-mix(in srgb,var(--gcc-info) 35%,var(--gcc-text))}
.gofastr-chart-s11{color:color-mix(in srgb,var(--gcc-primary) 25%,var(--gcc-text))}
</style>`
