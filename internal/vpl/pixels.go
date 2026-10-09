package vpl

import (
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/vpl/sys"
)

// surfaceAlign is the alignment of the coded picture size VPL expects for
// progressive 4:2:0 pictures.
const surfaceAlign = 16

func alignUp(v, a int) int { return (v + a - 1) / a * a }

// surface is one NV12 picture in system memory that is shared with the
// runtime. The runtime keeps the address of s and of the pixel buffer for
// as long as s.Data.Locked is non-zero; both live on the Go heap, which
// does not move, and stay referenced from here.
type surface struct {
	s      *sys.FrameSurface
	buf    []byte
	width  int // allocated luma width (info.Width)
	height int // allocated luma rows
	pitch  int
}

// newSurface allocates the pixel buffer for a picture of the coded size in
// info and describes it in an mfxFrameSurface1.
func newSurface(info sys.FrameInfo) *surface {
	w, h := int(info.Width), alignUp(int(info.Height), 2)
	pitch := alignUp(w, 64)
	buf := make([]byte, pitch*h*3/2)
	s := &sys.FrameSurface{Info: info}
	base := uintptr(unsafe.Pointer(&buf[0]))
	s.Data.Y = base
	s.Data.UV = base + uintptr(pitch*h)
	s.Data.Pitch = uint16(pitch)
	s.Data.PitchHigh = uint16(pitch >> 16)
	return &surface{s: s, buf: buf, width: w, height: h, pitch: pitch}
}

// locked reports whether the runtime still uses the surface. The counter is
// written by the runtime's threads; a stale read only delays the reuse.
func (s *surface) locked() bool { return s.s.Data.Locked != 0 }

func (s *surface) addr() uintptr { return uintptr(unsafe.Pointer(s.s)) }

// cropRect returns the visible rectangle the runtime reported for the
// picture in the surface, clamped to the allocation and with an even
// origin, as 4:2:0 chroma requires.
func (s *surface) cropRect() (x, y, w, h int) {
	info := &s.s.Info
	x, y, w, h = int(info.CropX), int(info.CropY), int(info.CropW), int(info.CropH)
	if w <= 0 || h <= 0 || x+w > s.width || y+h > s.height {
		return 0, 0, s.width, s.height
	}
	return x &^ 1, y &^ 1, w, h
}

// copyOut copies the rectangle into tightly packed NV12 planes: luma rows of
// lumaBytes bytes and chroma rows of chromaBytes bytes.
func (s *surface) copyOut(luma, chroma []byte, x, y, lumaBytes, lumaRows, chromaBytes, chromaRows int) {
	src := s.buf[y*s.pitch+x:]
	for r := 0; r < lumaRows; r++ {
		copy(luma[r*lumaBytes:(r+1)*lumaBytes], src[r*s.pitch:])
	}
	src = s.buf[s.pitch*s.height+(y/2)*s.pitch+x:]
	for r := 0; r < chromaRows; r++ {
		copy(chroma[r*chromaBytes:(r+1)*chromaBytes], src[r*s.pitch:])
	}
}

// upload copies an NV12 picture of width x height into the surface and
// replicates its right and bottom edges into the alignment padding, so the
// encoder does not spend bits on a hard edge that is cropped away later.
// width and height are even.
func (s *surface) upload(luma []byte, lumaStride int, chroma []byte, chromaStride int, width, height int) {
	for r := 0; r < s.height; r++ {
		sr := min(r, height-1)
		row := s.buf[r*s.pitch : r*s.pitch+s.width]
		copy(row, luma[sr*lumaStride:sr*lumaStride+width])
		for x := width; x < s.width; x++ {
			row[x] = row[width-1]
		}
	}
	base := s.pitch * s.height
	for r := 0; r < s.height/2; r++ {
		sr := min(r, height/2-1)
		row := s.buf[base+r*s.pitch : base+r*s.pitch+s.width]
		copy(row, chroma[sr*chromaStride:sr*chromaStride+width])
		for x := width; x+1 < s.width; x += 2 {
			row[x], row[x+1] = row[width-2], row[width-1]
		}
	}
}
