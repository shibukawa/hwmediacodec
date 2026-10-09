// Package mediafoundation implements the Windows backend on top of Media
// Foundation decoder transforms accelerated through Direct3D 11 (DXVA). The
// platform-specific parts are in the windows-tagged files; this file and
// time.go are plain Go so that they can be unit tested anywhere.
package mediafoundation

import (
	"encoding/binary"
	"fmt"
)

// nv12Image describes an NV12 picture in memory: luma row r starts at
// data[r*pitch] and the interleaved CbCr plane starts at row allocHeight,
// with the same pitch. allocHeight is the allocated (possibly padded) height
// of the surface, not the visible height.
type nv12Image struct {
	data        []byte
	pitch       int
	allocHeight int
}

// rect is the visible picture area inside an nv12Image.
type rect struct{ x, y, w, h int }

func (r rect) empty() bool { return r.w <= 0 || r.h <= 0 }

// clampTo limits r to a surface of the given size.
func (r rect) clampTo(width, height int) rect {
	if r.x < 0 {
		r.x = 0
	}
	if r.y < 0 {
		r.y = 0
	}
	if r.x+r.w > width {
		r.w = width - r.x
	}
	if r.y+r.h > height {
		r.h = height - r.y
	}
	return r
}

// nv12Size is the number of bytes of a tightly packed NV12 frame.
func nv12Size(w, h int) int {
	return w*h + ((w+1)/2*2)*((h+1)/2)
}

// copyNV12 copies the visible area r of src into dst as a tightly packed
// NV12 frame (Y plane, then CbCr) and returns the two planes and their
// strides. dst must hold at least nv12Size(r.w, r.h) bytes. Odd offsets are
// rounded down to the chroma grid.
func copyNV12(dst []byte, src nv12Image, r rect) (planes [][]byte, strides []int, err error) {
	if r.empty() || r.x < 0 || r.y < 0 {
		return nil, nil, fmt.Errorf("mediafoundation: invalid picture area %+v", r)
	}
	if src.pitch <= 0 || r.x+r.w > src.pitch || r.y+r.h > src.allocHeight {
		return nil, nil, fmt.Errorf("mediafoundation: picture area %+v exceeds surface (pitch %d, %d rows)", r, src.pitch, src.allocHeight)
	}
	chromaRows := (r.h + 1) / 2
	chromaX := r.x &^ 1
	chromaRowBytes := (r.w + 1) / 2 * 2
	chromaBase := src.pitch*src.allocHeight + (r.y/2)*src.pitch
	need := chromaBase + (chromaRows-1)*src.pitch + chromaX + chromaRowBytes
	if need > len(src.data) {
		return nil, nil, fmt.Errorf("mediafoundation: surface too small: need %d bytes, have %d", need, len(src.data))
	}
	ySize := r.w * r.h
	cSize := chromaRowBytes * chromaRows
	if len(dst) < ySize+cSize {
		return nil, nil, fmt.Errorf("mediafoundation: destination too small: need %d bytes, have %d", ySize+cSize, len(dst))
	}
	y := dst[:ySize]
	for row := 0; row < r.h; row++ {
		off := (r.y+row)*src.pitch + r.x
		copy(y[row*r.w:(row+1)*r.w], src.data[off:off+r.w])
	}
	c := dst[ySize : ySize+cSize]
	for row := 0; row < chromaRows; row++ {
		off := chromaBase + row*src.pitch + chromaX
		copy(c[row*chromaRowBytes:(row+1)*chromaRowBytes], src.data[off:off+chromaRowBytes])
	}
	return [][]byte{y, c}, []int{r.w, chromaRowBytes}, nil
}

// decodeVideoArea parses an MFVideoArea blob (two MFOffset values followed
// by a SIZE) as stored in the aperture attributes of a video media type. The
// fractional parts of the offsets are ignored.
func decodeVideoArea(b []byte) (rect, bool) {
	if len(b) < 16 {
		return rect{}, false
	}
	return rect{
		x: int(int16(binary.LittleEndian.Uint16(b[2:]))),
		y: int(int16(binary.LittleEndian.Uint16(b[6:]))),
		w: int(int32(binary.LittleEndian.Uint32(b[8:]))),
		h: int(int32(binary.LittleEndian.Uint32(b[12:]))),
	}, true
}

// packNV12 copies the two planes of an NV12 frame into dst tightly packed
// (stride = width), which is the layout the encoder's input type declares.
// The frame must already have been validated.
func packNV12(dst []byte, f *codecFrame) {
	rows := [2]int{f.Height, (f.Height + 1) / 2}
	rowBytes := [2]int{f.Width, (f.Width + 1) / 2 * 2}
	off := 0
	for i := 0; i < 2; i++ {
		src := f.Planes[i]
		stride := f.Strides[i]
		for r := 0; r < rows[i]; r++ {
			copy(dst[off:off+rowBytes[i]], src[r*stride:r*stride+rowBytes[i]])
			off += rowBytes[i]
		}
	}
}
