package pixconv

import (
	"math"
	"runtime"
	"sync"
)

// Matrix names the equations that relate the YCbCr samples of a picture to
// RGB.
type Matrix uint8

const (
	BT601 Matrix = iota
	BT709
	BT2020
	numMatrices
)

func (m Matrix) String() string {
	switch m {
	case BT601:
		return "bt601"
	case BT709:
		return "bt709"
	case BT2020:
		return "bt2020"
	}
	return "matrix(?)"
}

// Color describes the YCbCr samples of a picture: the matrix, and whether
// they use the full 0..255 range instead of the video range (Y 16..235,
// CbCr 16..240).
type Color struct {
	Matrix    Matrix
	FullRange bool
}

// ColorFor picks the description of a stream's pictures from the video
// signal fields of its sequence parameter set: matrix is matrix_coeffs and
// fullRange video_full_range_flag, both as written in the stream, with
// matrix 2 ("unspecified") for a stream that carries no colour description.
//
// A stream that does not name a matrix gets BT.709 when the picture is wider
// than 704 or taller than 576 pixels and BT.601 otherwise, which is where
// VideoToolbox draws the line (measured on an M3, 2026-10-10), so that
// every backend shows such a stream in the same colours.
func ColorFor(matrix uint8, fullRange bool, width, height int) Color {
	c := Color{FullRange: fullRange}
	switch matrix {
	case 1, 7: // BT.709; SMPTE 240M is within a percent of it
		c.Matrix = BT709
	case 4, 5, 6: // FCC, BT.470BG, SMPTE 170M
		c.Matrix = BT601
	case 9, 10: // BT.2020 non-constant and constant luminance
		c.Matrix = BT2020
	default:
		if width > 704 || height > 576 {
			c.Matrix = BT709
		}
	}
	return c
}

// tableShift is the precision of the conversion tables.
const tableShift = 16

// tables hold the terms of the YCbCr to RGB equations for every sample
// value, in tableShift fixed point: R = y[Y] + rv[Cr], G = y[Y] + gu[Cb] +
// gv[Cr], B = y[Y] + bu[Cb]. The rounding term is part of y.
type tables struct {
	y, rv, gu, gv, bu [256]int32
}

var colorTables [numMatrices][2]tables

func init() {
	for m, k := range [numMatrices][2]float64{BT601: {0.299, 0.114}, BT709: {0.2126, 0.0722}, BT2020: {0.2627, 0.0593}} {
		kr, kb := k[0], k[1]
		kg := 1 - kr - kb
		for full := 0; full < 2; full++ {
			// Luma spans 219 steps from 16 and chroma 224 steps around
			// 128 in video range, 255 steps each in full range.
			yScale, cScale, yOff := 255.0/219, 255.0/224, 16.0
			if full == 1 {
				yScale, cScale, yOff = 1, 1, 0
			}
			t := &colorTables[m][full]
			fix := func(v float64) int32 { return int32(math.Round(v * (1 << tableShift))) }
			for i := 0; i < 256; i++ {
				c := float64(i) - 128
				t.y[i] = fix(yScale*(float64(i)-yOff)) + 1<<(tableShift-1)
				t.rv[i] = fix(cScale * 2 * (1 - kr) * c)
				t.bu[i] = fix(cScale * 2 * (1 - kb) * c)
				t.gu[i] = fix(-cScale * 2 * (1 - kb) * kb / kg * c)
				t.gv[i] = fix(-cScale * 2 * (1 - kr) * kr / kg * c)
			}
		}
	}
}

func (c Color) tables() *tables {
	if c.FullRange {
		return &colorTables[c.Matrix][1]
	}
	return &colorTables[c.Matrix][0]
}

// NV12ToRGB converts a width x height NV12 picture (luma rows of yStride
// bytes in y, interleaved CbCr rows of uvStride bytes in uv) to 4-byte
// pixels in rows of dstStride bytes: R, G, B, 255, or B, G, R, 255 when bgr
// is set. Every chroma sample colours its 2x2 block of pixels.
func NV12ToRGB(dst []byte, dstStride int, y []byte, yStride int, uv []byte, uvStride int, width, height int, c Color, bgr bool) {
	bands := 1
	if height >= parallelRows {
		bands = min(runtime.GOMAXPROCS(0), height/(parallelRows/2))
	}
	if bands <= 1 {
		rgbRows(dst, dstStride, y, yStride, uv, uvStride, width, 0, height, c.tables(), bgr)
		return
	}
	var wg sync.WaitGroup
	for b := 0; b < bands; b++ {
		from, to := height*b/bands, height*(b+1)/bands
		wg.Add(1)
		go func() {
			defer wg.Done()
			rgbRows(dst, dstStride, y, yStride, uv, uvStride, width, from, to, c.tables(), bgr)
		}()
	}
	wg.Wait()
}

// rgbRows converts the picture rows [from, to).
func rgbRows(dst []byte, dstStride int, y []byte, yStride int, uv []byte, uvStride int, width, from, to int, t *tables, bgr bool) {
	ri, bi := 0, 2
	if bgr {
		ri, bi = 2, 0
	}
	pairs := width / 2
	for r := from; r < to; r++ {
		yRow := y[r*yStride : r*yStride+width]
		cRow := uv[(r/2)*uvStride : (r/2)*uvStride+(width+1)/2*2]
		d := dst[r*dstStride : r*dstStride+width*4]
		for i := 0; i < pairs; i++ {
			cb, cr := cRow[2*i], cRow[2*i+1]
			rc, gc, bc := t.rv[cr], t.gu[cb]+t.gv[cr], t.bu[cb]
			l0, l1 := t.y[yRow[2*i]], t.y[yRow[2*i+1]]
			p := d[8*i : 8*i+8 : 8*i+8]
			p[ri] = clamp8(l0 + rc)
			p[1] = clamp8(l0 + gc)
			p[bi] = clamp8(l0 + bc)
			p[3] = 255
			p[4+ri] = clamp8(l1 + rc)
			p[5] = clamp8(l1 + gc)
			p[4+bi] = clamp8(l1 + bc)
			p[7] = 255
		}
		if width&1 != 0 {
			x := width - 1
			cb, cr := cRow[x], cRow[x+1]
			l := t.y[yRow[x]]
			p := d[4*x : 4*x+4 : 4*x+4]
			p[ri] = clamp8(l + t.rv[cr])
			p[1] = clamp8(l + t.gu[cb] + t.gv[cr])
			p[bi] = clamp8(l + t.bu[cb])
			p[3] = 255
		}
	}
}

// clampTable maps a sample value that may lie outside 0..255, offset by
// clampOffset, to a byte. The equations stay within about -200..460 for any
// input, well inside the table.
const (
	clampSize   = 1024
	clampOffset = 384
)

var clampTable [clampSize]uint8

func init() {
	for i := range clampTable {
		clampTable[i] = uint8(min(max(i-clampOffset, 0), 255))
	}
}

// clamp8 turns a tableShift fixed-point sum into a byte without a branch
// the content could mispredict.
func clamp8(v int32) uint8 {
	return clampTable[(v>>tableShift+clampOffset)&(clampSize-1)]
}
