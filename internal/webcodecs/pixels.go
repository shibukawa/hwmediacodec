package webcodecs

import (
	"fmt"
	"math"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// planeLayout is one entry of the list VideoFrame.copyTo resolves with:
// where a plane starts in the destination buffer and how long its rows are.
type planeLayout struct {
	offset int
	stride int
}

// planeFits reports whether rows rows of rowBytes meaningful bytes at the
// given layout lie inside a buffer of size bytes.
func planeFits(l planeLayout, rows, rowBytes, size int) bool {
	if l.offset < 0 || l.stride < rowBytes || rows <= 0 {
		return false
	}
	return l.offset+l.stride*(rows-1)+rowBytes <= size
}

// nv12Planes returns the two planes of an NV12 picture that copyTo wrote
// into buf, without copying.
func nv12Planes(buf []byte, layout []planeLayout, width, height int) (planes [][]byte, strides []int, err error) {
	if len(layout) != 2 {
		return nil, nil, fmt.Errorf("NV12 picture with %d planes", len(layout))
	}
	for i, l := range layout {
		rows, rowBytes := codec.NV12.PlaneLayout(i, width, height)
		if !planeFits(l, rows, rowBytes, len(buf)) {
			return nil, nil, fmt.Errorf("NV12 plane %d (offset %d, stride %d) does not fit %d bytes for %dx%d", i, l.offset, l.stride, len(buf), width, height)
		}
		planes = append(planes, buf[l.offset:l.offset+l.stride*(rows-1)+rowBytes])
		strides = append(strides, l.stride)
	}
	return planes, strides, nil
}

// i420ToNV12 packs the I420 picture that copyTo wrote into src (three
// planes; a fourth, alpha, is ignored) into dst as tightly packed NV12,
// which is what the browser's software decoders need to become: they
// return planar frames where hardware decoders return NV12.
func i420ToNV12(dst, src []byte, layout []planeLayout, width, height int) error {
	if len(layout) < 3 {
		return fmt.Errorf("I420 picture with %d planes", len(layout))
	}
	cw, ch := (width+1)/2, (height+1)/2
	if len(dst) < width*height+2*cw*ch {
		return fmt.Errorf("NV12 buffer of %d bytes is too small for %dx%d", len(dst), width, height)
	}
	if !planeFits(layout[0], height, width, len(src)) || !planeFits(layout[1], ch, cw, len(src)) || !planeFits(layout[2], ch, cw, len(src)) {
		return fmt.Errorf("I420 planes do not fit %d bytes for %dx%d", len(src), width, height)
	}
	y := layout[0]
	for r := 0; r < height; r++ {
		copy(dst[r*width:r*width+width], src[y.offset+r*y.stride:])
	}
	u, v := layout[1], layout[2]
	uv := dst[width*height:]
	for r := 0; r < ch; r++ {
		us := src[u.offset+r*u.stride : u.offset+r*u.stride+cw]
		vs := src[v.offset+r*v.stride : v.offset+r*v.stride+cw]
		row := uv[r*2*cw : r*2*cw+2*cw]
		for x := 0; x < cw; x++ {
			row[2*x] = us[x]
			row[2*x+1] = vs[x]
		}
	}
	return nil
}

// packedRGB returns the single plane of a packed RGB picture that copyTo
// wrote into buf with rows of exactly 4*width bytes, moving the rows
// together in place when the browser padded them.
func packedRGB(buf []byte, layout []planeLayout, width, height int) ([]byte, error) {
	if len(layout) != 1 {
		return nil, fmt.Errorf("RGB picture with %d planes", len(layout))
	}
	l := layout[0]
	if !planeFits(l, height, width*4, len(buf)) {
		return nil, fmt.Errorf("RGB plane (offset %d, stride %d) does not fit %d bytes for %dx%d", l.offset, l.stride, len(buf), width, height)
	}
	if l.stride == width*4 {
		return buf[l.offset : l.offset+width*4*height], nil
	}
	for r := 0; r < height; r++ {
		copy(buf[r*width*4:(r+1)*width*4], buf[l.offset+r*l.stride:])
	}
	return buf[:width*4*height], nil
}

// narrowLuma and narrowChroma map full-range samples (0..255) to video
// range (Y 16..235, CbCr 16..240).
var narrowLuma, narrowChroma [256]byte

func init() {
	for v := range narrowLuma {
		narrowLuma[v] = byte(math.Round(float64(v)*219/255) + 16)
		narrowChroma[v] = byte(math.Round(float64(v-128)*224/255) + 128)
	}
}

// narrowRange brings the planes of an NV12 picture from full range to
// video range in place. Safari decodes video-range streams into full-range
// frames; the frames of this library keep the range of the stream. Every
// video-range value comes back as it was (what the widening clipped, a
// sample outside the video range, does not).
func narrowRange(planes [][]byte) {
	for i, p := range planes {
		table := &narrowLuma
		if i > 0 {
			table = &narrowChroma
		}
		for j, v := range p {
			p[j] = table[v]
		}
	}
}

// unshiftChroma undoes what WebKit's decoders do to the chroma plane on
// top of widening it: every row arrives as three quarters of itself and
// one quarter of the row below (the picture's chroma moved up a quarter
// line, measured on Safari 27 for H.264 and HEVC; the last row is left as
// it is). With t the rows as they arrive and c the rows as decoded,
// t[y] = (3*c[y] + c[y+1]) / 4, so from the last row up
// c[y] = (4*t[y] - c[y+1]) / 3. An error in a row reaches the row above
// divided by three, so the rounding of the samples on the way in costs at
// most a step or two; sharp vertical chroma edges, which the shift blurred
// by tens of steps, come back.
//
// uv holds rows of stride bytes with rowBytes meaningful ones, in video
// range (after narrowRange).
func unshiftChroma(uv []byte, stride, rowBytes, rows int) {
	for y := rows - 2; y >= 0; y-- {
		row := uv[y*stride : y*stride+rowBytes]
		below := uv[(y+1)*stride : (y+1)*stride+rowBytes]
		for x, t := range row {
			v := 4*int(t) - int(below[x])
			switch {
			case v <= 0:
				row[x] = 0
			case v >= 3*255:
				row[x] = 255
			default:
				row[x] = byte((v + 1) / 3)
			}
		}
	}
}

// checkNV12 validates a frame given to an encoder of the given size.
func checkNV12(f *codec.Frame, width, height int) error {
	switch {
	case f == nil:
		return fmt.Errorf("%w: nil frame", codec.ErrInvalidData)
	case f.Format != codec.NV12:
		return fmt.Errorf("%w: frame format %s, the encoder takes %s", codec.ErrInvalidData, f.Format, codec.NV12)
	case f.Width != width || f.Height != height:
		return fmt.Errorf("%w: frame is %dx%d, the encoder was opened for %dx%d", codec.ErrInvalidData, f.Width, f.Height, width, height)
	case len(f.Planes) < 2 || len(f.Strides) < 2:
		return fmt.Errorf("%w: NV12 frame with %d planes", codec.ErrInvalidData, len(f.Planes))
	}
	for i := 0; i < 2; i++ {
		rows, rowBytes := codec.NV12.PlaneLayout(i, width, height)
		if !planeFits(planeLayout{stride: f.Strides[i]}, rows, rowBytes, len(f.Planes[i])) {
			return fmt.Errorf("%w: plane %d holds %d bytes with stride %d, too little for %dx%d", codec.ErrInvalidData, i, len(f.Planes[i]), f.Strides[i], width, height)
		}
	}
	return nil
}

// packNV12 copies a validated NV12 frame into dst without row padding and
// returns the number of bytes written: the layout a VideoFrame is built
// from.
func packNV12(dst []byte, f *codec.Frame) int {
	n := 0
	for i := 0; i < 2; i++ {
		rows, rowBytes := codec.NV12.PlaneLayout(i, f.Width, f.Height)
		stride, p := f.Strides[i], f.Planes[i]
		if stride == rowBytes {
			n += copy(dst[n:], p[:rows*rowBytes])
			continue
		}
		for r := 0; r < rows; r++ {
			n += copy(dst[n:], p[r*stride:r*stride+rowBytes])
		}
	}
	return n
}
