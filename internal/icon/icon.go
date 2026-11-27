// Package icon renders the app icon so the desktop shortcut does not fall back
// to the generic executable glyph. Everything is drawn with signed distances,
// so there is no font or image asset to ship.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Ensure writes app.ico into dir and returns its path.
func Ensure(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	data, err := ICO(256)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "app.ico")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// PNG renders the icon at size and encodes it as a standalone PNG.
func PNG(size int) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, Render(size)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ICO wraps the rendered image in a single-entry .ico container.
//
// The payload is a classic 32bpp BMP rather than a PNG: Explorer and .NET both
// handle PNG entries inconsistently, while the DIB form works everywhere.
func ICO(size int) ([]byte, error) {
	payload, err := dibBytes(Render(size), size)
	if err != nil {
		return nil, err
	}

	head := make([]byte, 22)
	head[2], head[3] = 1, 0 // type: icon
	head[4], head[5] = 1, 0 // image count
	dim := byte(size)
	if size >= 256 {
		dim = 0 // 0 means 256 in the ICO header
	}
	head[6], head[7] = dim, dim
	binary.LittleEndian.PutUint16(head[10:], 1)  // color planes
	binary.LittleEndian.PutUint16(head[12:], 32) // bits per pixel
	binary.LittleEndian.PutUint32(head[14:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(head[18:], 22) // payload offset
	return append(head, payload...), nil
}

// dibBytes encodes img as a BITMAPINFOHEADER followed by bottom-up BGRA pixels
// and a fully transparent AND mask (alpha carries the shape).
func dibBytes(img *image.RGBA, size int) ([]byte, error) {
	var b bytes.Buffer
	u32 := func(v uint32) { _ = binary.Write(&b, binary.LittleEndian, v) }
	u16 := func(v uint16) { _ = binary.Write(&b, binary.LittleEndian, v) }

	u32(40)                      // biSize
	u32(uint32(size))            // biWidth
	u32(uint32(size * 2))        // biHeight: XOR bitmap + AND mask
	u16(1)                       // biPlanes
	u16(32)                      // biBitCount
	u32(0)                       // biCompression = BI_RGB
	u32(uint32(size * size * 4)) // biSizeImage
	u32(0)                       // biXPelsPerMeter
	u32(0)                       // biYPelsPerMeter
	u32(0)                       // biClrUsed
	u32(0)                       // biClrImportant

	for y := size - 1; y >= 0; y-- {
		for x := 0; x < size; x++ {
			c := img.RGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	maskRow := (size + 31) / 32 * 4
	b.Write(make([]byte, maskRow*size))
	return b.Bytes(), nil
}

// Render draws the icon: a dark rounded tile, a green disc, and a white "G".
func Render(size int) *image.RGBA {
	s := float64(size)
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // supersampling grid per pixel

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var acc [4]float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := float64(x) + (float64(sx)+0.5)/ss
					fy := float64(y) + (float64(sy)+0.5)/ss
					px := sample(fx, fy, s)
					acc[0] += px[0] * px[3]
					acc[1] += px[1] * px[3]
					acc[2] += px[2] * px[3]
					acc[3] += px[3]
				}
			}
			n := float64(ss * ss)
			a := acc[3] / n
			var r, g, b float64
			if acc[3] > 0 {
				r, g, b = acc[0]/acc[3], acc[1]/acc[3], acc[2]/acc[3]
			}
			img.Set(x, y, color.RGBA{
				R: uint8(clamp(r, 0, 1) * 255),
				G: uint8(clamp(g, 0, 1) * 255),
				B: uint8(clamp(b, 0, 1) * 255),
				A: uint8(clamp(a, 0, 1) * 255),
			})
		}
	}
	return img
}

// sample composites the three layers at one point and returns straight RGBA.
func sample(px, py, size float64) [4]float64 {
	var out [4]float64
	c := size / 2

	// tile
	tile := cov(sdRoundBox(px, py, c, c, size*0.47, size*0.47, size*0.22))
	out = over(out, [3]float64{0x15 / 255.0, 0x17 / 255.0, 0x1a / 255.0}, tile)

	// green disc with a soft radial highlight
	discR := size * 0.365
	dist := math.Hypot(px-c, py-c)
	if d := dist - discR; d < 1 {
		t := clamp(dist/discR, 0, 1)
		col := mix([3]float64{0x86 / 255.0, 0xf8 / 255.0, 0xc1 / 255.0},
			[3]float64{0x1e / 255.0, 0x9a / 255.0, 0x61 / 255.0}, t)
		out = over(out, col, cov(d))
	}

	// white G
	out = over(out, [3]float64{1, 1, 1}, glyphAlpha(px, py, c, size))
	return out
}

// glyphAlpha builds a "G": an annulus with an opening in the upper right plus a
// horizontal bar at mid height, which is what makes the letter legible even
// without a font.
func glyphAlpha(px, py, c, size float64) float64 {
	dx, dy := px-c, py-c
	dist := math.Hypot(dx, dy)

	mid := size * 0.172
	half := size * 0.032
	ring := cov(math.Abs(dist-mid) - half)

	// Opening sits between roughly -68° and -22° (upper right quadrant).
	theta := math.Atan2(dy, dx)
	ring *= 1 - wedgeMask(theta, -1.20, -0.40)

	// Bar from just right of the centre out to the ring.
	bar := cov(sdRoundBox(px, py, c+size*0.115, c, size*0.085, size*0.030, size*0.028))

	return math.Max(ring, bar)
}

// wedgeMask returns 1 inside [lo, hi] and 0 outside, with soft edges so the cut
// stays anti-aliased.
func wedgeMask(theta, lo, hi float64) float64 {
	const soft = 0.10
	switch {
	case theta < lo:
		return clamp(1-(lo-theta)/soft, 0, 1)
	case theta > hi:
		return clamp(1-(theta-hi)/soft, 0, 1)
	default:
		return 1
	}
}

func sdRoundBox(px, py, cx, cy, hw, hh, r float64) float64 {
	qx := math.Abs(px-cx) - hw + r
	qy := math.Abs(py-cy) - hh + r
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

// cov converts a signed distance (pixels) to anti-aliased coverage.
func cov(d float64) float64 { return clamp(0.5-d, 0, 1) }

// over composites src (straight alpha) onto dst (straight alpha).
func over(dst [4]float64, src [3]float64, a float64) [4]float64 {
	if a <= 0 {
		return dst
	}
	outA := a + dst[3]*(1-a)
	if outA <= 0 {
		return [4]float64{}
	}
	var out [4]float64
	for i := 0; i < 3; i++ {
		out[i] = (src[i]*a + dst[i]*dst[3]*(1-a)) / outA
	}
	out[3] = outA
	return out
}

func mix(a, b [3]float64, t float64) [3]float64 {
	return [3]float64{
		a[0] + (b[0]-a[0])*t,
		a[1] + (b[1]-a[1])*t,
		a[2] + (b[2]-a[2])*t,
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
