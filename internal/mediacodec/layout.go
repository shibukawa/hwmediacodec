package mediacodec

import (
	"encoding/binary"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

// plane describes one plane of a YUV buffer: where it starts in the buffer
// and how its samples are spaced.
type plane struct {
	offset int
	colInc int // bytes between horizontally adjacent samples
	rowInc int // bytes between rows
}

// layout describes a 4:2:0 8-bit YUV byte buffer: the three planes and the
// size of the buffer's picture (before cropping).
type layout struct {
	width, height int
	y, u, v       plane
}

// parseMediaImage2 reads a MediaImage2 structure (the "image-data" entry of
// a MediaCodec format) into a layout. It accepts 8-bit 4:2:0 YUV only.
func parseMediaImage2(b []byte) (layout, error) {
	var l layout
	if len(b) < sys.MediaImage2Size {
		return l, fmt.Errorf("image-data is %d bytes, want %d", len(b), sys.MediaImage2Size)
	}
	u32 := func(off int) int { return int(binary.LittleEndian.Uint32(b[off:])) }
	i32 := func(off int) int { return int(int32(binary.LittleEndian.Uint32(b[off:]))) }
	typ := uint32(u32(0))
	if typ != sys.MediaImageTypeYUV {
		return l, fmt.Errorf("image-data type %#x is not YUV", typ)
	}
	if n := u32(4); n != 3 {
		return l, fmt.Errorf("image-data has %d planes, want 3", n)
	}
	l.width, l.height = u32(8), u32(12)
	if d, a := u32(16), u32(20); d != 8 || a != 8 {
		return l, fmt.Errorf("image-data bit depth %d/%d, want 8", d, a)
	}
	for i := 0; i < 3; i++ {
		off := 24 + i*20
		p := plane{offset: u32(off), colInc: i32(off + 4), rowInc: i32(off + 8)}
		hs, vs := u32(off+12), u32(off+16)
		if i == 0 && (hs != 1 || vs != 1) || i > 0 && (hs != 2 || vs != 2) {
			return l, fmt.Errorf("image-data plane %d subsampling %dx%d is not 4:2:0", i, hs, vs)
		}
		if p.colInc <= 0 || p.rowInc <= 0 {
			return l, fmt.Errorf("image-data plane %d increments %d/%d", i, p.colInc, p.rowInc)
		}
		switch i {
		case 0:
			l.y = p
		case 1:
			l.u = p
		case 2:
			l.v = p
		}
	}
	if l.width <= 0 || l.height <= 0 {
		return l, fmt.Errorf("image-data size %dx%d", l.width, l.height)
	}
	return l, nil
}

// layoutFromColorFormat builds the layout of a planar (19) or semi-planar
// (21) buffer from the format's stride and slice height; stride and
// sliceHeight of 0 default to the picture size.
func layoutFromColorFormat(colorFormat int32, width, height, stride, sliceHeight int) (layout, error) {
	var l layout
	if width <= 0 || height <= 0 {
		return l, fmt.Errorf("picture size %dx%d", width, height)
	}
	if stride < width {
		stride = width
	}
	if sliceHeight < height {
		sliceHeight = height
	}
	l.width, l.height = width, height
	l.y = plane{offset: 0, colInc: 1, rowInc: stride}
	cw := (width + 1) / 2
	ch := (sliceHeight + 1) / 2
	switch colorFormat {
	case sys.ColorFormatYUV420SemiPlanar:
		l.u = plane{offset: stride * sliceHeight, colInc: 2, rowInc: stride}
		l.v = plane{offset: stride*sliceHeight + 1, colInc: 2, rowInc: stride}
	case sys.ColorFormatYUV420Planar:
		cs := (stride + 1) / 2
		l.u = plane{offset: stride * sliceHeight, colInc: 1, rowInc: cs}
		l.v = plane{offset: stride*sliceHeight + cs*ch, colInc: 1, rowInc: cs}
	default:
		_ = cw
		return l, fmt.Errorf("colour format %d (%#x) is neither YUV420Planar nor YUV420SemiPlanar", colorFormat, colorFormat)
	}
	return l, nil
}

// size is the number of bytes the layout needs for a picture of its size.
func (l layout) size() int {
	cw, ch := (l.width+1)/2, (l.height+1)/2
	end := func(p plane, w, h int) int {
		if w == 0 || h == 0 {
			return p.offset
		}
		return p.offset + (h-1)*p.rowInc + (w-1)*p.colInc + 1
	}
	n := end(l.y, l.width, l.height)
	if e := end(l.u, cw, ch); e > n {
		n = e
	}
	if e := end(l.v, cw, ch); e > n {
		n = e
	}
	return n
}

// isNV12 reports whether the chroma planes are interleaved CbCr with Cb
// first, so that chroma rows can be copied as they are.
func (l layout) isNV12() bool {
	return l.u.colInc == 2 && l.v.colInc == 2 && l.v.offset == l.u.offset+1 && l.u.rowInc == l.v.rowInc
}

// packNV12 copies the width x height picture at (left, top) of the buffer
// into tightly packed NV12 planes (y of width*height bytes, uv of
// ((width+1)/2*2)*((height+1)/2) bytes). The buffer must hold the layout.
func (l layout) packNV12(y, uv, buf []byte, left, top, width, height int) {
	cw, ch := (width+1)/2, (height+1)/2
	for r := 0; r < height; r++ {
		src := l.y.offset + (top+r)*l.y.rowInc + left*l.y.colInc
		dst := y[r*width : (r+1)*width]
		if l.y.colInc == 1 {
			copy(dst, buf[src:src+width])
		} else {
			for x := range dst {
				dst[x] = buf[src+x*l.y.colInc]
			}
		}
	}
	cl, ct := left/2, top/2
	rowBytes := cw * 2
	if l.isNV12() {
		for r := 0; r < ch; r++ {
			src := l.u.offset + (ct+r)*l.u.rowInc + cl*2
			copy(uv[r*rowBytes:(r+1)*rowBytes], buf[src:src+rowBytes])
		}
		return
	}
	for r := 0; r < ch; r++ {
		su := l.u.offset + (ct+r)*l.u.rowInc + cl*l.u.colInc
		sv := l.v.offset + (ct+r)*l.v.rowInc + cl*l.v.colInc
		dst := uv[r*rowBytes : (r+1)*rowBytes]
		for x := 0; x < cw; x++ {
			dst[2*x] = buf[su+x*l.u.colInc]
			dst[2*x+1] = buf[sv+x*l.v.colInc]
		}
	}
}

// unpackNV12 writes an NV12 picture (planes with their strides) into the
// buffer according to the layout, which must be at least as large as the
// picture. Rows the layout has beyond the picture are left untouched.
func (l layout) unpackNV12(buf []byte, y []byte, yStride int, uv []byte, uvStride int, width, height int) {
	cw, ch := (width+1)/2, (height+1)/2
	for r := 0; r < height; r++ {
		dst := l.y.offset + r*l.y.rowInc
		src := y[r*yStride : r*yStride+width]
		if l.y.colInc == 1 {
			copy(buf[dst:dst+width], src)
		} else {
			for x, v := range src {
				buf[dst+x*l.y.colInc] = v
			}
		}
	}
	rowBytes := cw * 2
	if l.isNV12() {
		for r := 0; r < ch; r++ {
			dst := l.u.offset + r*l.u.rowInc
			copy(buf[dst:dst+rowBytes], uv[r*uvStride:r*uvStride+rowBytes])
		}
		return
	}
	for r := 0; r < ch; r++ {
		du := l.u.offset + r*l.u.rowInc
		dv := l.v.offset + r*l.v.rowInc
		src := uv[r*uvStride : r*uvStride+rowBytes]
		for x := 0; x < cw; x++ {
			buf[du+x*l.u.colInc] = src[2*x]
			buf[dv+x*l.v.colInc] = src[2*x+1]
		}
	}
}
