package imageedit

// render.go is the server-side renderer: it applies an imageedit-v1 operation
// list to a decoded image and produces the authoritative output bytes. The
// frame runs the SAME pipeline in TypeScript (js/src/render.ts) for its
// preview; the two implementations are integer-exact by construction, which
// is what the preview-vs-server agreement tests assert.
//
// THE SHARED PIPELINE (both sides, both languages, pixel for pixel):
//
//   1. decode the source to straight-alpha 8-bit RGBA at origin (0,0)
//   2. crop    — copy the crop rect (nil/absent = whole image)
//   3. rotate  — 0/90/180/270 clockwise, forward pixel mapping
//   4. annotate— rects (stroked), arrows (Bresenham), text (bitmap font)
//   5. redact  — filled rects, LAST, so nothing can draw over a redaction
//
// Every primitive is integer-only: fillRect, strokeRect (four fillRects),
// Bresenham lines with square stamps, and a 5×7 bitmap font. There is no
// anti-aliasing, resampling or float rounding anywhere in the pipeline, so a
// PNG input renders to identical pixels in Go and in the frame's canvas.
// (JPEG inputs decode with ±1 channel wobble between libjpeg variants; the
// agreement tolerance for JPEG is stated in the tests and docs.)
//
// Geometry is authored in SOURCE-image pixels (origin top-left, Y down) and
// mapped forward through crop+rotate at render time, so annotations and
// redactions stay pinned to image content across later crop/rotate edits.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// Sentinel errors the handlers map onto HTTP status + code. They are the
// plugin's whole failure vocabulary for the render path.
var (
	// ErrTooLarge: bytes or declared dimensions exceed a host ceiling.
	ErrTooLarge = errors.New("image exceeds a size or dimension cap")
	// ErrUnsupportedFormat: not a decodable png/jpeg.
	ErrUnsupportedFormat = errors.New("unsupported image format (want png or jpeg)")
	// ErrBadDoc: the operation list is structurally invalid.
	ErrBadDoc = errors.New("invalid imageedit-v1 doc")
	// ErrCropOutside: the crop rect does not intersect the image.
	ErrCropOutside = errors.New("crop rect outside the image")
	// ErrSrcMismatch: doc.src.sha256 does not match the resolved bytes.
	ErrSrcMismatch = errors.New("source image digest mismatch")
	// ErrRedactionLeak: verification found redacted content in the output.
	ErrRedactionLeak = errors.New("redaction verification failed")
	// ErrConflict: a save/export handler rejected the write as stale.
	ErrConflict = errors.New("conflicting revision")
)

// Bounds on the operation list. They keep a doc small enough to round-trip
// through a hidden field and keep the render loop bounded.
const (
	maxAnnotations = 64
	maxRedactions  = 64
	maxTextLen     = 64
	maxStrokeWidth = 64
	maxGlyphScale  = 32
	maxIDLen       = 64
	maxSHA256Len   = 64
	maxRefLen      = 128
)

// rgba is a straight-alpha 8-bit color, the pipeline's only color type.
type rgba struct{ R, G, B, A uint8 }

// parseHexColor narrows a #RRGGBB literal. It is the sole accepted color
// syntax in the doc (the frame's palette emits it), so nothing ambiguous
// ever reaches the renderer.
func parseHexColor(s string) (rgba, error) {
	if len(s) != 7 || s[0] != '#' {
		return rgba{}, fmt.Errorf("color %q must be #RRGGBB", s)
	}
	var v [3]uint8
	for i := range 3 {
		c, err := hexVal(s[1+i*2])
		if err != nil {
			return rgba{}, err
		}
		d, err := hexVal(s[2+i*2])
		if err != nil {
			return rgba{}, err
		}
		v[i] = c<<4 | d
	}
	return rgba{R: v[0], G: v[1], B: v[2], A: 255}, nil
}

func hexVal(c byte) (uint8, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("bad hex digit %q", string(rune(c)))
}

// ValidateDoc checks an operation list against the structural bounds. The
// same rules run in /save (before persisting) and /export (before rendering)
// so the two routes cannot disagree about what a doc is. nil crop, rotate 0
// and empty lists are all valid.
func ValidateDoc(doc Doc) error {
	if doc.Rotate != 0 && doc.Rotate != 90 && doc.Rotate != 180 && doc.Rotate != 270 {
		return fmt.Errorf("%w: rotate must be 0, 90, 180 or 270 (got %d)", ErrBadDoc, doc.Rotate)
	}
	if doc.Src.Kind != "" && doc.Src.Kind != "id" {
		return fmt.Errorf("%w: src.kind must be \"id\"", ErrBadDoc)
	}
	if len(doc.Src.Ref) > maxRefLen {
		return fmt.Errorf("%w: src.ref too long", ErrBadDoc)
	}
	if len(doc.Src.SHA256) > maxSHA256Len {
		return fmt.Errorf("%w: src.sha256 too long", ErrBadDoc)
	}
	if doc.Crop != nil && (doc.Crop.W <= 0 || doc.Crop.H <= 0) {
		return fmt.Errorf("%w: crop must have positive w and h", ErrBadDoc)
	}
	if len(doc.Annotations) > maxAnnotations {
		return fmt.Errorf("%w: too many annotations (max %d)", ErrBadDoc, maxAnnotations)
	}
	for _, a := range doc.Annotations {
		if len(a.ID) > maxIDLen {
			return fmt.Errorf("%w: annotation id too long", ErrBadDoc)
		}
		if _, err := parseHexColor(a.Color); err != nil {
			return fmt.Errorf("%w: annotation %q: %v", ErrBadDoc, a.ID, err)
		}
		if a.Width < 1 || a.Width > maxStrokeWidth {
			return fmt.Errorf("%w: annotation %q: width must be 1..%d", ErrBadDoc, a.ID, maxStrokeWidth)
		}
		switch a.Type {
		case "rect":
			if a.W <= 0 || a.H <= 0 {
				return fmt.Errorf("%w: rect annotation %q needs positive w/h", ErrBadDoc, a.ID)
			}
		case "arrow":
			// any two points are drawable; no constraint
		case "text":
			if a.Size < 1 || a.Size > maxGlyphScale {
				return fmt.Errorf("%w: text annotation %q: size must be 1..%d", ErrBadDoc, a.ID, maxGlyphScale)
			}
			if len(a.Text) > maxTextLen {
				return fmt.Errorf("%w: text annotation %q too long (max %d)", ErrBadDoc, a.ID, maxTextLen)
			}
		default:
			return fmt.Errorf("%w: unknown annotation type %q", ErrBadDoc, a.Type)
		}
	}
	if len(doc.Redactions) > maxRedactions {
		return fmt.Errorf("%w: too many redactions (max %d)", ErrBadDoc, maxRedactions)
	}
	for _, r := range doc.Redactions {
		if len(r.ID) > maxIDLen {
			return fmt.Errorf("%w: redaction id too long", ErrBadDoc)
		}
		if r.Rect.W <= 0 || r.Rect.H <= 0 {
			return fmt.Errorf("%w: redaction %q needs positive w/h", ErrBadDoc, r.ID)
		}
		if _, err := parseHexColor(r.Fill); err != nil {
			return fmt.Errorf("%w: redaction %q: %v", ErrBadDoc, r.ID, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Decoding + caps
// ---------------------------------------------------------------------------

// decodeCapped decodes png/jpeg bytes under the host ceilings. The dimension
// check runs on the HEADER (image.DecodeConfig) before image.Decode can
// allocate anything, which is the entire point of the cap: an oversized
// image is refused without the server ever materializing its pixels.
func decodeCapped(src []byte, maxDim, maxPixels int) (image.Image, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, "", ErrUnsupportedFormat
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", ErrUnsupportedFormat
	}
	if cfg.Width > maxDim || cfg.Height > maxDim || cfg.Width*cfg.Height > maxPixels {
		return nil, "", fmt.Errorf("%w: %d×%d exceeds the dimension or pixel cap",
			ErrTooLarge, cfg.Width, cfg.Height)
	}
	img, format2, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, "", ErrUnsupportedFormat
	}
	if format2 != "" {
		format = format2
	}
	return img, format, nil
}

// toNRGBA normalizes any decoded image to a straight-alpha NRGBA canvas at
// origin (0,0): the pipeline's only internal representation, and the same
// shape a canvas ImageData gives the frame (so parity holds for every input
// Go's decoders produce — paletted, YCbCr, premultiplied).
func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// ---------------------------------------------------------------------------
// The transform: crop → rotate
// ---------------------------------------------------------------------------

// effectiveCrop intersects the doc's crop with the image bounds and returns
// it in image-local coordinates. A nil doc crop is the whole image.
func effectiveCrop(img *image.NRGBA, crop *Rect) Rect {
	full := Rect{W: img.Rect.Dx(), H: img.Rect.Dy()}
	if crop == nil {
		return full
	}
	x0 := clampInt(crop.X, 0, full.W)
	y0 := clampInt(crop.Y, 0, full.H)
	x1 := clampInt(crop.X+crop.W, 0, full.W)
	y1 := clampInt(crop.Y+crop.H, 0, full.H)
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// mapPoint maps a SOURCE-image pixel through crop+rotate to its output
// pixel. rot is degrees clockwise; the mapping is the exact integer forward
// bijection (every output pixel has exactly one input pixel — no holes, no
// interpolation).
func mapPoint(px, py int, crop Rect, rot int) (int, int) {
	x, y := px-crop.X, py-crop.Y
	switch rot {
	case 90:
		return crop.H - 1 - y, x
	case 180:
		return crop.W - 1 - x, crop.H - 1 - y
	case 270:
		return y, crop.W - 1 - x
	default:
		return x, y
	}
}

// mapRect maps a source-space rect to the output-space axis-aligned rect
// covering its four mapped corners. 90° multiples keep rects axis-aligned,
// so this is exact, not an approximation.
func mapRect(r Rect, crop Rect, rot int) Rect {
	x1, y1 := mapPoint(r.X, r.Y, crop, rot)
	x2, y2 := mapPoint(r.X+r.W-1, r.Y, crop, rot)
	x3, y3 := mapPoint(r.X, r.Y+r.H-1, crop, rot)
	x4, y4 := mapPoint(r.X+r.W-1, r.Y+r.H-1, crop, rot)
	minX := minInt(x1, minInt(x2, minInt(x3, x4)))
	maxX := maxInt(x1, maxInt(x2, maxInt(x3, x4)))
	minY := minInt(y1, minInt(y2, minInt(y3, y4)))
	maxY := maxInt(y1, maxInt(y2, maxInt(y3, y4)))
	return Rect{X: minX, Y: minY, W: maxX - minX + 1, H: maxY - minY + 1}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// outDims is the composed output size for a crop+rotate.
func outDims(crop Rect, rot int) (int, int) {
	if rot == 90 || rot == 270 {
		return crop.H, crop.W
	}
	return crop.W, crop.H
}

// ---------------------------------------------------------------------------
// Drawing primitives — THE interchange contract. js/src/render.ts implements
// each of these with the identical integer arithmetic. Do not change one
// side without the other; the agreement tests are the tripwire.
// ---------------------------------------------------------------------------

// fillRect sets every pixel of the (clipped) rect to c. Doc colors are
// always opaque (parseHexColor), so "set" needs no blending — same as Go's
// draw.Src and the frame's direct RGBA writes.
func fillRect(img *image.NRGBA, x, y, w, h int, c rgba) {
	if w <= 0 || h <= 0 {
		return
	}
	iw, ih := img.Rect.Dx(), img.Rect.Dy()
	x0, y0 := clampInt(x, 0, iw), clampInt(y, 0, ih)
	x1, y1 := clampInt(x+w, 0, iw), clampInt(y+h, 0, ih)
	for yy := y0; yy < y1; yy++ {
		row := img.Pix[(yy*iw+x0)*4 : (yy*iw+x1)*4]
		for i := 0; i < len(row); i += 4 {
			row[i], row[i+1], row[i+2], row[i+3] = c.R, c.G, c.B, c.A
		}
	}
}

// strokeRect draws a border of thickness t INSIDE r as four fillRects in a
// fixed order (top, bottom, left, right), with exact clamping when the rect
// is thinner than 2t. The order matters: where borders overlap, the later
// fill wins, and both sides must win identically.
func strokeRect(img *image.NRGBA, r Rect, t int, c rgba) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	tTop := minInt(t, r.H)
	fillRect(img, r.X, r.Y, r.W, tTop, c)
	rem := r.H - tTop
	if rem <= 0 {
		return
	}
	tBot := minInt(t, rem)
	fillRect(img, r.X, r.Y+r.H-tBot, r.W, tBot, c)
	remH := r.H - tTop - tBot
	if remH <= 0 {
		return
	}
	tL := minInt(t, r.W)
	fillRect(img, r.X, r.Y+tTop, tL, remH, c)
	remW := r.W - tL
	if remW <= 0 {
		return
	}
	tR := minInt(t, remW)
	fillRect(img, r.X+r.W-tR, r.Y+tTop, tR, remH, c)
}

// stamp fills the t×t square whose top-left is (x-off, y-off), off = (t-1)/2
// integer-divided — the line pen. Partially off-canvas stamps clip.
func stamp(img *image.NRGBA, x, y, t int, c rgba) {
	off := (t - 1) / 2
	fillRect(img, x-off, y-off, t, t, c)
}

// drawLine is integer Bresenham, stamping every visited pixel. dx/dy sign
// handling and the e2 comparisons below are the classic form; the frame
// copy is line-for-line identical.
func drawLine(img *image.NRGBA, x1, y1, x2, y2, t int, c rgba) {
	dx := absInt(x2 - x1)
	sx := 1
	if x1 > x2 {
		sx = -1
	}
	dy := -absInt(y2 - y1)
	sy := 1
	if y1 > y2 {
		sy = -1
	}
	err := dx + dy
	x, y := x1, y1
	for {
		stamp(img, x, y, t, c)
		if x == x2 && y == y2 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x += sx
		}
		if e2 <= dx {
			err += dx
			y += sy
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// isqrt is floor(sqrt(n)) by integer bisection — the shared head-length
// math must never touch floats, or the two renderers drift by a pixel.
func isqrt(n int) int {
	if n <= 0 {
		return 0
	}
	lo, hi := 0, n
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if mid*mid <= n {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// drawArrow draws a shaft plus a two-barb head. All head arithmetic is
// integer (multiply, then truncate-toward-zero divide — Go int division and
// the frame's Math.trunc agree), so both renderers emit the same barbs.
func drawArrow(img *image.NRGBA, x1, y1, x2, y2, t int, c rgba) {
	drawLine(img, x1, y1, x2, y2, t, c)
	dx := x2 - x1
	dy := y2 - y1
	length := isqrt(dx*dx + dy*dy)
	if length == 0 {
		return
	}
	L := 3*t + 5
	if L > length {
		L = length
	}
	bx := dx * L / length // truncate toward zero, both sides
	by := dy * L / length
	drawLine(img, x2, y2, x2-bx+by/2, y2-by-bx/2, t, c)
	drawLine(img, x2, y2, x2-bx-by/2, y2-by+bx/2, t, c)
}

// drawText renders s with the 5×7 bitmap font at integer cell scale. Glyphs
// are 5 cells wide + 1 cell of spacing (6-cell advance); unknown characters
// advance without drawing. The font's charset is closed under uppercasing,
// so either side may normalize case with identical output.
func drawText(img *image.NRGBA, x, y int, s string, scale int, c rgba) {
	cursor := 0
	for _, ch := range s {
		rows, ok := bitmapFont[upperRune(ch)]
		if ok {
			for row := range 7 {
				bits := rows[row]
				for col := range 5 {
					if bits&(1<<(4-col)) != 0 {
						fillRect(img, x+(cursor+col)*scale, y+row*scale, scale, scale, c)
					}
				}
			}
		}
		cursor += 6
	}
}

func upperRune(ch rune) rune {
	if ch >= 'a' && ch <= 'z' {
		return ch - 'a' + 'A'
	}
	return ch
}

// TextWidth is the advance width of s at scale in pixels (exported for the
// frame's hit-testing parity and for tests).
func TextWidth(s string, scale int) int {
	n := 0
	for range s {
		n++
	}
	return n * 6 * scale
}

// bitmapFont is the 5×7 glyph table. Each glyph is 7 rows of 5 bits, MSB =
// leftmost column. THIS TABLE IS THE INTERCHANGE: js/src/font.ts carries the
// same rows. TestFontTableWellFormed guards the shape here; the agreement
// tests guard the cross-language identity.
var bitmapFont = map[rune][7]byte{
	'A': glyph(".###." + "#...#" + "#...#" + "#####" + "#...#" + "#...#" + "#...#"),
	'B': glyph("####." + "#...#" + "#...#" + "####." + "#...#" + "#...#" + "####."),
	'C': glyph(".###." + "#...#" + "#...." + "#...." + "#...." + "#...#" + ".###."),
	'D': glyph("####." + "#...#" + "#...#" + "#...#" + "#...#" + "#...#" + "####."),
	'E': glyph("#####" + "#...." + "#...." + "####." + "#...." + "#...." + "#####"),
	'F': glyph("#####" + "#...." + "#...." + "####." + "#...." + "#...." + "#...."),
	'G': glyph(".###." + "#...#" + "#...." + "#.###" + "#...#" + "#...#" + ".###."),
	'H': glyph("#...#" + "#...#" + "#...#" + "#####" + "#...#" + "#...#" + "#...#"),
	'I': glyph("#####" + "..#.." + "..#.." + "..#.." + "..#.." + "..#.." + "#####"),
	'J': glyph("..###" + "...#." + "...#." + "...#." + "...#." + "#..#." + ".##.."),
	'K': glyph("#...#" + "#..#." + "#.#.." + "##..." + "#.#.." + "#..#." + "#...#"),
	'L': glyph("#...." + "#...." + "#...." + "#...." + "#...." + "#...." + "#####"),
	'M': glyph("#...#" + "##.##" + "#.#.#" + "#.#.#" + "#...#" + "#...#" + "#...#"),
	'N': glyph("#...#" + "##..#" + "#.#.#" + "#..##" + "#...#" + "#...#" + "#...#"),
	'O': glyph(".###." + "#...#" + "#...#" + "#...#" + "#...#" + "#...#" + ".###."),
	'P': glyph("####." + "#...#" + "#...#" + "####." + "#...." + "#...." + "#...."),
	'Q': glyph(".###." + "#...#" + "#...#" + "#...#" + "#.#.#" + "#..#." + ".##.#"),
	'R': glyph("####." + "#...#" + "#...#" + "####." + "#.#.." + "#..#." + "#...#"),
	'S': glyph(".###." + "#...#" + "#...." + ".###." + "....#" + "#...#" + ".###."),
	'T': glyph("#####" + "..#.." + "..#.." + "..#.." + "..#.." + "..#.." + "..#.."),
	'U': glyph("#...#" + "#...#" + "#...#" + "#...#" + "#...#" + "#...#" + ".###."),
	'V': glyph("#...#" + "#...#" + "#...#" + "#...#" + "#...#" + ".#.#." + "..#.."),
	'W': glyph("#...#" + "#...#" + "#...#" + "#.#.#" + "#.#.#" + "##.##" + "#...#"),
	'X': glyph("#...#" + "#...#" + ".#.#." + "..#.." + ".#.#." + "#...#" + "#...#"),
	'Y': glyph("#...#" + "#...#" + ".#.#." + "..#.." + "..#.." + "..#.." + "..#.."),
	'Z': glyph("#####" + "....#" + "...#." + "..#.." + ".#..." + "#...." + "#####"),
	'0': glyph(".###." + "#..##" + "#.#.#" + "#.#.#" + "#.#.#" + "##..#" + ".###."),
	'1': glyph("..#.." + ".##.." + "..#.." + "..#.." + "..#.." + "..#.." + "#####"),
	'2': glyph(".###." + "#...#" + "....#" + "...#." + "..#.." + ".#..." + "#####"),
	'3': glyph("####." + "....#" + "....#" + ".###." + "....#" + "....#" + "####."),
	'4': glyph("...#." + "..##." + ".#.#." + "#..#." + "#####" + "...#." + "...#."),
	'5': glyph("#####" + "#...." + "####." + "....#" + "....#" + "#...#" + ".###."),
	'6': glyph("..##." + ".#..." + "#...." + "####." + "#...#" + "#...#" + ".###."),
	'7': glyph("#####" + "....#" + "...#." + "..#.." + ".#..." + ".#..." + ".#..."),
	'8': glyph(".###." + "#...#" + "#...#" + ".###." + "#...#" + "#...#" + ".###."),
	'9': glyph(".###." + "#...#" + "#...#" + ".####" + "....#" + "...#." + ".##.."),
	'-': glyph("....." + "....." + "....." + "#####" + "....." + "....." + "....."),
	'_': glyph("....." + "....." + "....." + "....." + "....." + "....." + "#####"),
	'.': glyph("....." + "....." + "....." + "....." + "....." + "..#.." + "....."),
	':': glyph("....." + "..#.." + "....." + "....." + "....." + "..#.." + "....."),
	'/': glyph("....#" + "....#" + "...#." + "..#.." + ".#..." + "#...." + "#...."),
	'+': glyph("....." + "..#.." + "..#.." + "#####" + "..#.." + "..#.." + "....."),
	'#': glyph(".#.#." + ".#.#." + "#####" + ".#.#." + "#####" + ".#.#." + ".#.#."),
	' ': glyph("....." + "....." + "....." + "....." + "....." + "....." + "....."),
}

// glyph compiles one 35-char row-string (7 rows × 5 cols, '#' = set) into
// the [7]byte row-bitmask form the renderer walks. The + concatenation keeps
// the table readable as rows while the argument is the single 35-byte string
// the TypeScript twin joins.
func glyph(s string) [7]byte {
	var out [7]byte
	for row := range 7 {
		var b byte
		for col := range 5 {
			b <<= 1
			if row*5+col < len(s) && s[row*5+col] == '#' {
				b |= 1
			}
		}
		out[row] = b
	}
	return out
}

// ---------------------------------------------------------------------------
// Compose
// ---------------------------------------------------------------------------

// ComposeResult is everything the export path needs after rendering: the
// final image, the pre-redaction composite (annotations applied, redactions
// not — the verifier's "what was there" reference), and the output-space
// redaction rects.
type ComposeResult struct {
	Out            *image.NRGBA
	Pre            *image.NRGBA
	RedactionRects []Rect // output-space, one per doc redaction (same order)
	Width, Height  int
}

// compose applies the operation list to the normalized source. The doc must
// already have passed [ValidateDoc].
func compose(src *image.NRGBA, doc Doc) (*ComposeResult, error) {
	crop := effectiveCrop(src, doc.Crop)
	if crop.W <= 0 || crop.H <= 0 {
		return nil, ErrCropOutside
	}

	// Steps 1–2: crop, then rotate, as one forward pass over the source
	// pixels (each output pixel written exactly once from its source pixel).
	ow, oh := outDims(crop, doc.Rotate)
	base := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	sw := src.Rect.Dx()
	for py := crop.Y; py < crop.Y+crop.H; py++ {
		for px := crop.X; px < crop.X+crop.W; px++ {
			ox, oy := mapPoint(px, py, crop, doc.Rotate)
			si := (py*sw + px) * 4
			oi := (oy*ow + ox) * 4
			base.Pix[oi] = src.Pix[si]
			base.Pix[oi+1] = src.Pix[si+1]
			base.Pix[oi+2] = src.Pix[si+2]
			base.Pix[oi+3] = src.Pix[si+3]
		}
	}

	// Step 3: annotations onto a copy. Pre ends here — everything except the
	// redactions.
	pre := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	copy(pre.Pix, base.Pix)
	for _, a := range doc.Annotations {
		c, err := parseHexColor(a.Color)
		if err != nil {
			return nil, fmt.Errorf("%w: annotation %q: %v", ErrBadDoc, a.ID, err)
		}
		switch a.Type {
		case "rect":
			strokeRect(pre, mapRect(Rect{X: a.X, Y: a.Y, W: a.W, H: a.H}, crop, doc.Rotate), a.Width, c)
		case "arrow":
			x1, y1 := mapPoint(a.X, a.Y, crop, doc.Rotate)
			x2, y2 := mapPoint(a.X2, a.Y2, crop, doc.Rotate)
			drawArrow(pre, x1, y1, x2, y2, a.Width, c)
		case "text":
			tx, ty := mapPoint(a.X, a.Y, crop, doc.Rotate)
			drawText(pre, tx, ty, a.Text, a.Size, c)
		}
	}

	// Step 4: redactions, last, on a fresh copy.
	out := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	copy(out.Pix, pre.Pix)
	rects := make([]Rect, 0, len(doc.Redactions))
	for _, r := range doc.Redactions {
		c, err := parseHexColor(r.Fill)
		if err != nil {
			return nil, fmt.Errorf("%w: redaction %q: %v", ErrBadDoc, r.ID, err)
		}
		m := mapRect(r.Rect, crop, doc.Rotate)
		fillRect(out, m.X, m.Y, m.W, m.H, c)
		rects = append(rects, m)
	}

	return &ComposeResult{
		Out:            out,
		Pre:            pre,
		RedactionRects: rects,
		Width:          ow,
		Height:         oh,
	}, nil
}

// ---------------------------------------------------------------------------
// Verification — the pdf plugin's rule, restated for raster images:
// a redacted region must be GONE from the output bytes, not covered by a
// drawable object, and the server proves it before releasing anything.
// ---------------------------------------------------------------------------

// VerifyReport is the bounded audit record that travels with every export.
// Verdicts plus per-redaction ids, never an unbounded pixel dump.
type VerifyReport struct {
	RedactionsChecked int      `json:"redactionsChecked"`
	Failed            []string `json:"failed,omitempty"`  // ids whose region still leaks
	Vacuous           []string `json:"vacuous,omitempty"` // ids over content already == fill
	EXIFStripped      bool     `json:"exifStripped"`      // output bytes carry no EXIF
	DimensionsMatch   bool     `json:"dimensionsMatch"`   // composed dims == encoded dims
	PixelsSampled     int      `json:"pixelsSampled"`     // pixels the fill check walked
	Pass              bool     `json:"pass"`
}

// verifyRedactions walks every redaction rect in the OUTPUT image and
// requires every pixel to equal the fill exactly. A mapping bug, an
// off-by-one, or a "cover it with a shape" regression leaves original pixels
// behind and fails here. It also flags (as a warning, not a failure) a
// redaction whose region contained nothing but the fill color in the
// pre-redaction composite — vacuous, the raster twin of pdf's invisible-text
// note.
func verifyRedactions(out, pre *image.NRGBA, rects []Rect, doc Doc) VerifyReport {
	rep := VerifyReport{RedactionsChecked: len(rects), Pass: true}
	ow, oh := out.Rect.Dx(), out.Rect.Dy()
	for i, r := range rects {
		fill, err := parseHexColor(doc.Redactions[i].Fill)
		if err != nil {
			// ValidateDoc ran first; unreachable, but fail closed anyway.
			rep.Failed = append(rep.Failed, doc.Redactions[i].ID)
			rep.Pass = false
			continue
		}
		x0 := clampInt(r.X, 0, ow)
		y0 := clampInt(r.Y, 0, oh)
		x1 := clampInt(r.X+r.W, 0, ow)
		y1 := clampInt(r.Y+r.H, 0, oh)
		leak := false
		vacuous := true
		for y := y0; y < y1 && !leak; y++ {
			for x := x0; x < x1; x++ {
				oi := (y*ow + x) * 4
				rep.PixelsSampled++
				if out.Pix[oi] != fill.R || out.Pix[oi+1] != fill.G || out.Pix[oi+2] != fill.B {
					leak = true
					break
				}
				if pre.Pix[oi] != fill.R || pre.Pix[oi+1] != fill.G || pre.Pix[oi+2] != fill.B {
					vacuous = false
				}
			}
		}
		if leak {
			rep.Failed = append(rep.Failed, doc.Redactions[i].ID)
			rep.Pass = false
		}
		if vacuous {
			rep.Vacuous = append(rep.Vacuous, doc.Redactions[i].ID)
		}
	}
	return rep
}

// scanEXIF looks for EXIF carriers in encoded bytes: the JPEG APP1
// "Exif\0\0" signature and the PNG eXIf chunk name. Go's encoders emit
// neither — the scan exists so the strip claim is CHECKED on every export
// rather than assumed, the same belt-and-braces posture as the pdf plugin's
// incremental-save check.
func scanEXIF(format string, b []byte) bool {
	if bytes.Contains(b, []byte("Exif\x00\x00")) {
		return false
	}
	if format == "png" && bytes.Contains(b, []byte("eXIf")) {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// renderDoc: the full authoritative path (decode → caps → digest → compose
// → verify → encode). /export calls exactly this; nothing else produces
// bytes.
// ---------------------------------------------------------------------------

// RenderOutput is the finished export artifact plus its verification.
type RenderOutput struct {
	Bytes  []byte
	Format string
	Width  int
	Height int
	SHA256 string
	Report VerifyReport
}

// renderDoc decodes src, applies doc, verifies and encodes. srcDigest, when
// non-empty, must equal the digest of src (the doc was authored against
// those exact bytes).
func renderDoc(src []byte, doc Doc, srcDigest string, jpegQuality int) (*RenderOutput, error) {
	if err := ValidateDoc(doc); err != nil {
		return nil, err
	}
	if srcDigest != "" {
		sum := sha256.Sum256(src)
		if fmt.Sprintf("%x", sum) != srcDigest {
			return nil, ErrSrcMismatch
		}
	}
	img, format, err := decodeCapped(src, defaultMaxDim, defaultMaxPixels)
	if err != nil {
		return nil, err
	}
	if format == "jpg" {
		format = "jpeg"
	}
	if format != "png" && format != "jpeg" {
		return nil, ErrUnsupportedFormat
	}
	res, err := compose(toNRGBA(img), doc)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&buf, res.Out, &jpeg.Options{Quality: jpegQuality})
	} else {
		err = png.Encode(&buf, res.Out)
	}
	if err != nil {
		return nil, err
	}
	outBytes := buf.Bytes()

	rep := verifyRedactions(res.Out, res.Pre, res.RedactionRects, doc)
	rep.EXIFStripped = scanEXIF(format, outBytes)
	rep.DimensionsMatch = encodedDimsMatch(outBytes, res.Width, res.Height)
	if !rep.EXIFStripped || !rep.DimensionsMatch {
		rep.Pass = false
	}
	if !rep.Pass {
		return nil, ErrRedactionLeak
	}
	sum := sha256.Sum256(outBytes)
	return &RenderOutput{
		Bytes:  outBytes,
		Format: format,
		Width:  res.Width,
		Height: res.Height,
		SHA256: fmt.Sprintf("%x", sum),
		Report: rep,
	}, nil
}

// encodedDimsMatch decodes the OUTPUT header and confirms it carries the
// composed dimensions — the encoding half of "the bytes are what we
// verified".
func encodedDimsMatch(b []byte, wantW, wantH int) bool {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return false
	}
	return cfg.Width == wantW && cfg.Height == wantH
}
