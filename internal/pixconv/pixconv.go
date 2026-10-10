// Package pixconv converts packed RGB pictures to NV12 in Go, for the
// backends whose encoder takes NV12 only (Media Foundation, VA-API and Intel
// VPL). The public API puts an Encoder from this package in front of such a
// backend, so WithInputFormat(RGBA) works everywhere.
//
// The conversion uses the BT.709 matrix in video range (Y 16..235, CbCr
// 16..240), the one VideoToolbox applies to RGB input, and averages every
// 2x2 block for the chroma sample. The backend is asked to declare the same
// matrix in the stream (codec.EncoderConfig.BT709).
package pixconv

import (
	"runtime"
	"sync"
)

// BT.709 video-range coefficients in 16.16 fixed point. The luma row sums
// to 219/255 and the chroma rows to zero, so white maps to 235 and every
// grey to CbCr 128 exactly.
const (
	yR, yG, yB = 11966, 40254, 4064
	uR, uG, uB = -6596, -22188, 28784
	vR, vG, vB = 28784, -26145, -2639

	yBias = 16<<16 + 1<<15
	// Chroma works on the sum of four pixels, hence the two extra bits.
	cShift = 18
	cBias  = 128<<cShift + 1<<(cShift-1)
)

// parallelRows is the number of picture rows above which the conversion is
// split across goroutines; below it the hand-off costs more than it saves.
const parallelRows = 256

// RGBToNV12 converts a width x height picture of 4-byte pixels (rows of
// srcStride bytes) to NV12: luma rows of yStride bytes in y and interleaved
// CbCr rows of uvStride bytes in uv, (height+1)/2 of them, each
// (width+1)/2 pairs long. bgr selects BGRA input instead of RGBA; the
// fourth byte is ignored. For odd sizes the last column and row are
// repeated.
func RGBToNV12(y []byte, yStride int, uv []byte, uvStride int, src []byte, srcStride, width, height int, bgr bool) {
	pairs := (height + 1) / 2
	bands := 1
	if height >= parallelRows {
		bands = min(runtime.GOMAXPROCS(0), pairs/(parallelRows/4))
	}
	if bands <= 1 {
		convertRows(y, yStride, uv, uvStride, src, srcStride, width, height, 0, pairs, bgr)
		return
	}
	var wg sync.WaitGroup
	for b := 0; b < bands; b++ {
		from, to := pairs*b/bands, pairs*(b+1)/bands
		wg.Add(1)
		go func() {
			defer wg.Done()
			convertRows(y, yStride, uv, uvStride, src, srcStride, width, height, from, to, bgr)
		}()
	}
	wg.Wait()
}

// convertRows converts the row pairs [from, to): picture rows 2*from up to
// 2*to and the chroma rows between from and to.
func convertRows(y []byte, yStride int, uv []byte, uvStride int, src []byte, srcStride, width, height, from, to int, bgr bool) {
	ri, bi := 0, 2
	if bgr {
		ri, bi = 2, 0
	}
	for p := from; p < to; p++ {
		r0 := 2 * p
		r1 := min(r0+1, height-1)
		s0 := src[r0*srcStride : r0*srcStride+width*4]
		s1 := src[r1*srcStride : r1*srcStride+width*4]
		y0 := y[r0*yStride : r0*yStride+width]
		y1 := y[r1*yStride : r1*yStride+width]
		c := uv[p*uvStride : p*uvStride+(width+1)/2*2]
		for x := 0; x < width; x += 2 {
			x1 := min(x+1, width-1)
			a, b := s0[x*4:x*4+4:x*4+4], s0[x1*4:x1*4+4:x1*4+4]
			d, e := s1[x*4:x*4+4:x*4+4], s1[x1*4:x1*4+4:x1*4+4]
			ar, ag, ab := int32(a[ri]), int32(a[1]), int32(a[bi])
			br, bg, bb := int32(b[ri]), int32(b[1]), int32(b[bi])
			dr, dg, db := int32(d[ri]), int32(d[1]), int32(d[bi])
			er, eg, eb := int32(e[ri]), int32(e[1]), int32(e[bi])
			y0[x] = uint8((yR*ar + yG*ag + yB*ab + yBias) >> 16)
			y0[x1] = uint8((yR*br + yG*bg + yB*bb + yBias) >> 16)
			y1[x] = uint8((yR*dr + yG*dg + yB*db + yBias) >> 16)
			y1[x1] = uint8((yR*er + yG*eg + yB*eb + yBias) >> 16)
			r, g, bl := ar+br+dr+er, ag+bg+dg+eg, ab+bb+db+eb
			c[x] = uint8((uR*r + uG*g + uB*bl + cBias) >> cShift)
			c[x+1] = uint8((vR*r + vG*g + vB*bl + cBias) >> cShift)
		}
	}
}
