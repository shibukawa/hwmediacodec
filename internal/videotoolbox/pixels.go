//go:build darwin

package videotoolbox

import (
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

// vtPixelFormat maps a public pixel format to the Core Video pixel format
// type used for it.
func vtPixelFormat(f codec.PixelFormat) (uint32, bool) {
	switch f {
	case codec.NV12:
		return sys.PixelFormat420YpCbCr8BiPlanarVideoRange, true
	case codec.BGRA:
		return sys.PixelFormat32BGRA, true
	case codec.RGBA:
		return sys.PixelFormat32RGBA, true
	}
	return 0, false
}

// isFormat reports whether a pixel buffer's type matches the public format,
// accepting the full-range NV12 variant.
func isFormat(vt uint32, f codec.PixelFormat) bool {
	switch f {
	case codec.NV12:
		return vt == sys.PixelFormat420YpCbCr8BiPlanarVideoRange || vt == sys.PixelFormat420YpCbCr8BiPlanarFullRange
	case codec.BGRA:
		return vt == sys.PixelFormat32BGRA
	case codec.RGBA:
		return vt == sys.PixelFormat32RGBA
	}
	return false
}

// bufferPlane describes one plane of a locked CVPixelBuffer.
type bufferPlane struct {
	base   *byte
	stride int
	rows   int
	width  int // meaningful bytes per row
}

// bufferPlanes returns the planes of a locked pixel buffer, in the layout of
// format f. It returns false when the buffer does not match f.
func bufferPlanes(pb uintptr, f codec.PixelFormat) ([]bufferPlane, bool) {
	if !isFormat(sys.CVPixelBufferGetPixelFormatType(pb), f) {
		return nil, false
	}
	width := int(sys.CVPixelBufferGetWidth(pb))
	height := int(sys.CVPixelBufferGetHeight(pb))
	n := f.PlaneCount()
	planes := make([]bufferPlane, n)
	if f == codec.NV12 {
		if int(sys.CVPixelBufferGetPlaneCount(pb)) != n {
			return nil, false
		}
		for i := range planes {
			rowBytes := int(sys.CVPixelBufferGetWidthOfPlane(pb, uintptr(i)))
			if i == 1 {
				rowBytes *= 2
			}
			planes[i] = bufferPlane{
				base:   sys.CVPixelBufferGetBaseAddressOfPlane(pb, uintptr(i)),
				stride: int(sys.CVPixelBufferGetBytesPerRowOfPlane(pb, uintptr(i))),
				rows:   int(sys.CVPixelBufferGetHeightOfPlane(pb, uintptr(i))),
				width:  rowBytes,
			}
		}
	} else {
		planes[0] = bufferPlane{
			base:   sys.CVPixelBufferGetBaseAddress(pb),
			stride: int(sys.CVPixelBufferGetBytesPerRow(pb)),
			rows:   height,
			width:  width * 4,
		}
	}
	for _, pl := range planes {
		if pl.base == nil || pl.stride < pl.width || pl.rows <= 0 {
			return nil, false
		}
	}
	return planes, true
}

func (pl bufferPlane) bytes() []byte {
	return unsafe.Slice(pl.base, pl.stride*pl.rows)
}

// copyRows copies rows of rowBytes from src (stride sstride) to dst (stride
// dstride). When swizzle is set the first and third byte of every pixel
// are exchanged, which converts between BGRA and RGBA.
func copyRows(dst []byte, dstride int, src []byte, sstride, rows, rowBytes int, swizzle bool) {
	for r := 0; r < rows; r++ {
		d := dst[r*dstride : r*dstride+rowBytes]
		s := src[r*sstride : r*sstride+rowBytes]
		if swizzle {
			swapRB(d, s)
		} else {
			copy(d, s)
		}
	}
}

// swapRB copies 4-byte pixels from src to dst exchanging bytes 0 and 2.
func swapRB(dst, src []byte) {
	n := len(src) &^ 3
	dst = dst[:n]
	src = src[:n]
	for i := 0; i < n; i += 4 {
		s := src[i : i+4 : i+4]
		d := dst[i : i+4 : i+4]
		d[0], d[1], d[2], d[3] = s[2], s[1], s[0], s[3]
	}
}
