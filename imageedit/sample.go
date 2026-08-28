package imageedit

// sample.go generates the plugin's demo image. Unlike pdf (which ships a
// binary sample.pdf), the sample here is DRAWN at runtime by the same
// primitives the renderer uses — deterministic by construction, reviewable
// as code, and carrying the bitmap-font text the redaction demo needs.
//
// The image is a 960×640 "field report card" scene: a smooth sky gradient,
// a sun, two mountain silhouettes, and a white card that carries
// FIELD REPORT 042 plus the deliberately visible secret token
// SK-LIVE-9F2K-77QZ — the thing the demo redacts. SampleTokenRect()
// exports the token's source-space rect so tests and the demo page can
// aim at it precisely.

import (
	"bytes"
	"image"
	"image/png"
	"sync"
)

// Sample image layout constants (source pixels). Exported pieces are part
// of the demo contract: e2e journeys and the Go tests use them to aim at
// the secret.
const (
	sampleWidth  = 960
	sampleHeight = 640

	// The card: white panel with a border, top-left area of the image.
	cardX, cardY, cardW, cardH = 72, 372, 560, 200

	// Text placement inside the card (top-left anchored).
	titleScale  = 4 // FIELD REPORT 042
	tokenScale  = 5 // SK-LIVE-9F2K-77QZ
	noticeScale = 2 // REDACT THE TOKEN BEFORE SHARING
	titleDY     = 28
	tokenDY     = 88
	noticeDY    = 158

	// Derived: the token's bounding rect in source pixels. TextWidth is
	// advance width; glyph ink is 5/6 of it at most, so the rect is the
	// advance box inset by one cell — generous enough to contain every inked
	// pixel, tight enough to be a meaningful redaction target.
	tokenText = "SK-LIVE-9F2K-77QZ"
)

var (
	tokenX = cardX + 28
	tokenY = cardY + tokenDY
	tokenW = len(tokenText) * 6 * tokenScale // chars × 6-cell advance × scale
)

// SampleTokenRect returns the source-image rect covering the sample's
// visible secret token. The redaction demo, the e2e journey and the
// Go tests all aim here.
func SampleTokenRect() Rect {
	return Rect{X: tokenX, Y: tokenY, W: tokenW, H: 7 * tokenScale}
}

var (
	sampleOnce sync.Once
	samplePNG  []byte
)

// SampleImage returns the generated demo image as PNG bytes. The result is
// a cached copy; callers must not mutate it.
func SampleImage() []byte {
	sampleOnce.Do(func() {
		samplePNG = encodeSample(generateSample())
	})
	out := make([]byte, len(samplePNG))
	copy(out, samplePNG)
	return out
}

// encodeSample is split out so tests can regenerate the image without the
// cache.
func encodeSample(img *image.NRGBA) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("imageedit: encode sample: " + err.Error())
	}
	return buf.Bytes()
}

// lerpColor blends two rgba colors by t∈[0,1] with integer math rounding
// half away from zero on the scaled sum — deterministic, no float drift.
func lerpColor(a, b rgba, tNum, tDen int) rgba {
	mix := func(av, bv uint8) uint8 {
		s := int(av)*(tDen-tNum) + int(bv)*tNum
		v := (s*2 + tDen) / (2 * tDen) // round half up on non-negatives
		if v > 255 {
			v = 255
		}
		return uint8(v)
	}
	return rgba{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: 255}
}

// generateSample draws the whole scene with the render primitives (fillRect,
// strokeRect, drawText) plus two per-pixel shapes (circle, triangle) that
// exist only here — the sample is server-side only, so they have no frame
// twin and no parity constraint.
func generateSample() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, sampleWidth, sampleHeight))
	iw, ih := sampleWidth, sampleHeight

	// Sky: vertical gradient, soft blue to warm horizon. Row-constant, so
	// the PNG compresses it to almost nothing.
	top := rgba{R: 0xBF, G: 0xD7, B: 0xEE, A: 255}
	horizon := rgba{R: 0xF3, G: 0xE7, B: 0xD3, A: 255}
	for y := range ih {
		c := lerpColor(top, horizon, y, ih-1)
		fillRect(img, 0, y, iw, 1, c)
	}

	// Sun: filled circle with a ring, upper right.
	sunCX, sunCY, sunR := 764, 132, 58
	sun := rgba{R: 0xF2, G: 0xC1, B: 0x4E, A: 255}
	sunRing := rgba{R: 0xD9, G: 0xA4, B: 0x37, A: 255}
	for y := sunCY - sunR - 6; y <= sunCY+sunR+6 && y < ih; y++ {
		for x := sunCX - sunR - 6; x <= sunCX+sunR+6 && x < iw; x++ {
			dx, dy := x-sunCX, y-sunCY
			d2 := dx*dx + dy*dy
			switch {
			case d2 <= sunR*sunR:
				setPixel(img, x, y, sun)
			case d2 <= (sunR+6)*(sunR+6):
				setPixel(img, x, y, sunRing)
			}
		}
	}

	// Mountains: two triangles from the horizon line. A triangle is drawn
	// row by row with a widening half-span — integer exact.
	mountainFar := rgba{R: 0x8A, G: 0x9B, B: 0xB0, A: 255}
	mountainNear := rgba{R: 0x54, G: 0x6A, B: 0x7F, A: 255}
	drawTriangle(img, 340, 148, 372, 190, mountainFar)
	drawTriangle(img, 660, 212, 372, 160, mountainNear)

	// Ground band below the horizon.
	ground := rgba{R: 0x6C, G: 0x8F, B: 0x5A, A: 255}
	groundDark := rgba{R: 0x53, G: 0x71, B: 0x45, A: 255}
	fillRect(img, 0, 372, iw, sampleHeight-372, ground)
	fillRect(img, 0, 372, iw, 8, groundDark)

	// The card: shadow, panel, border.
	shadow := rgba{R: 0x33, G: 0x38, B: 0x3E, A: 255}
	fillRect(img, cardX+8, cardY+8, cardW, cardH, shadow)
	card := rgba{R: 0xFA, G: 0xFB, B: 0xFC, A: 255}
	fillRect(img, cardX, cardY, cardW, cardH, card)
	border := rgba{R: 0x1F, G: 0x29, B: 0x33, A: 255}
	strokeRect(img, Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, 3, border)

	// Card text, all through the bitmap font (the same table the frame
	// renders annotations with).
	ink := rgba{R: 0x1F, G: 0x29, B: 0x33, A: 255}
	secret := rgba{R: 0xC0, G: 0x2E, B: 0x22, A: 255}
	muted := rgba{R: 0x64, G: 0x74, B: 0x82, A: 255}
	drawText(img, cardX+28, cardY+titleDY, "FIELD REPORT 042", titleScale, ink)
	drawText(img, tokenX, tokenY, tokenText, tokenScale, secret)
	drawText(img, cardX+28, cardY+noticeDY, "REDACT THE TOKEN BEFORE SHARING", noticeScale, muted)

	// Corner ticks so rotation is visibly testable even without annotations.
	tick := rgba{R: 0x1F, G: 0x29, B: 0x33, A: 255}
	strokeRect(img, Rect{X: 900, Y: 20, W: 36, H: 36}, 3, tick)
	strokeRect(img, Rect{X: 20, Y: 20, W: 36, H: 36}, 3, tick)

	return img
}

// setPixel writes one opaque pixel if in bounds.
func setPixel(img *image.NRGBA, x, y int, c rgba) {
	if x < 0 || y < 0 || x >= img.Rect.Dx() || y >= img.Rect.Dy() {
		return
	}
	i := (y*img.Rect.Dx() + x) * 4
	img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
}

// drawTriangle fills a triangle whose apex is (ax, ay) and whose base of
// half-width halfWidth sits on row by, centered under the apex. Integer
// rows with ceil-rounded spans, so the shape is exact and deterministic.
func drawTriangle(img *image.NRGBA, ax, ay, by, halfWidth int, c rgba) {
	if by <= ay || halfWidth <= 0 {
		return
	}
	for y := ay; y <= by; y++ {
		if y < 0 || y >= img.Rect.Dy() {
			continue
		}
		num, den := y-ay, by-ay
		half := (halfWidth*num + den - 1) / den // ceil: spans grow downward
		fillRect(img, ax-half, y, 2*half+1, 1, c)
	}
}
