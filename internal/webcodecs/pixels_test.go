package webcodecs

import (
	"bytes"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// ramp fills b with distinct, position-dependent values.
func ramp(b []byte, seed int) {
	for i := range b {
		b[i] = byte(i*7 + seed)
	}
}

func TestI420ToNV12(t *testing.T) {
	for _, size := range [][2]int{{8, 6}, {7, 5}, {1, 1}, {16, 2}} {
		w, h := size[0], size[1]
		cw, ch := (w+1)/2, (h+1)/2
		// Padded rows and a gap in front of every plane, as a browser is
		// free to lay them out.
		layout := []planeLayout{
			{offset: 3, stride: w + 5},
			{stride: cw + 2},
			{stride: cw + 9},
			{stride: w}, // alpha, ignored
		}
		layout[1].offset = layout[0].offset + layout[0].stride*h + 11
		layout[2].offset = layout[1].offset + layout[1].stride*ch + 1
		layout[3].offset = layout[2].offset + layout[2].stride*ch
		src := make([]byte, layout[3].offset+w*h)
		ramp(src, w)

		dst := make([]byte, codec.NV12.FrameSize(w, h))
		if err := i420ToNV12(dst, src, layout, w, h); err != nil {
			t.Fatalf("%dx%d: %v", w, h, err)
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if got, want := dst[y*w+x], src[layout[0].offset+y*layout[0].stride+x]; got != want {
					t.Fatalf("%dx%d: luma (%d,%d) = %d, want %d", w, h, x, y, got, want)
				}
			}
		}
		uv := dst[w*h:]
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				u := src[layout[1].offset+y*layout[1].stride+x]
				v := src[layout[2].offset+y*layout[2].stride+x]
				if uv[y*2*cw+2*x] != u || uv[y*2*cw+2*x+1] != v {
					t.Fatalf("%dx%d: chroma (%d,%d) = %d,%d, want %d,%d", w, h, x, y, uv[y*2*cw+2*x], uv[y*2*cw+2*x+1], u, v)
				}
			}
		}

		// Planes that reach beyond the buffer are an error, not a panic.
		short := layout[2].offset + layout[2].stride*(ch-1) + cw - 1 // one byte into the last V row
		if err := i420ToNV12(dst, src[:short], layout, w, h); err == nil {
			t.Errorf("%dx%d: truncated source accepted", w, h)
		}
		if err := i420ToNV12(dst[:len(dst)-1], src, layout, w, h); err == nil {
			t.Errorf("%dx%d: short destination accepted", w, h)
		}
		if err := i420ToNV12(dst, src, layout[:2], w, h); err == nil {
			t.Errorf("%dx%d: two planes accepted", w, h)
		}
	}
}

func TestNV12Planes(t *testing.T) {
	const w, h = 7, 5
	layout := []planeLayout{{offset: 0, stride: 16}, {offset: 16 * h, stride: 16}}
	buf := make([]byte, 16*h+16*3)
	planes, strides, err := nv12Planes(buf, layout, w, h)
	if err != nil {
		t.Fatal(err)
	}
	if strides[0] != 16 || strides[1] != 16 {
		t.Errorf("strides %v", strides)
	}
	// The last row need not be padded.
	if len(planes[0]) != 16*(h-1)+w || len(planes[1]) != 16*2+8 {
		t.Errorf("plane sizes %d, %d", len(planes[0]), len(planes[1]))
	}
	if _, _, err := nv12Planes(buf[:16*h+16*2+7], layout, w, h); err == nil {
		t.Error("truncated chroma plane accepted")
	}
	if _, _, err := nv12Planes(buf, []planeLayout{{stride: w - 1}, layout[1]}, w, h); err == nil {
		t.Error("stride below the row size accepted")
	}
	if _, _, err := nv12Planes(buf, layout[:1], w, h); err == nil {
		t.Error("one plane accepted")
	}
}

func TestPackedRGB(t *testing.T) {
	const w, h = 3, 4
	tight := make([]byte, w*4*h)
	ramp(tight, 1)
	got, err := packedRGB(append([]byte(nil), tight...), []planeLayout{{stride: w * 4}}, w, h)
	if err != nil || !bytes.Equal(got, tight) {
		t.Fatalf("tight rows: %v", err)
	}

	// Rows padded to 16 bytes behind a 5-byte gap move together.
	padded := make([]byte, 5+16*h)
	for y := 0; y < h; y++ {
		copy(padded[5+16*y:], tight[w*4*y:w*4*(y+1)])
	}
	got, err = packedRGB(padded, []planeLayout{{offset: 5, stride: 16}}, w, h)
	if err != nil || !bytes.Equal(got, tight) {
		t.Fatalf("padded rows: %v\n got %v\nwant %v", err, got, tight)
	}
	if _, err := packedRGB(tight[:len(tight)-1], []planeLayout{{stride: w * 4}}, w, h); err == nil {
		t.Error("truncated plane accepted")
	}
}

func TestCheckAndPackNV12(t *testing.T) {
	const w, h = 6, 4
	yStride, uvStride := 10, 8
	f := &codec.Frame{
		Width: w, Height: h, Format: codec.NV12,
		Planes:  [][]byte{make([]byte, yStride*h), make([]byte, uvStride*h/2)},
		Strides: []int{yStride, uvStride},
	}
	ramp(f.Planes[0], 3)
	ramp(f.Planes[1], 9)
	if err := checkNV12(f, w, h); err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, codec.NV12.FrameSize(w, h))
	if n := packNV12(dst, f); n != len(dst) {
		t.Fatalf("packed %d bytes, want %d", n, len(dst))
	}
	for y := 0; y < h; y++ {
		if !bytes.Equal(dst[y*w:(y+1)*w], f.Planes[0][y*yStride:y*yStride+w]) {
			t.Fatalf("luma row %d differs", y)
		}
	}
	for y := 0; y < h/2; y++ {
		if !bytes.Equal(dst[w*h+y*w:w*h+(y+1)*w], f.Planes[1][y*uvStride:y*uvStride+w]) {
			t.Fatalf("chroma row %d differs", y)
		}
	}

	bad := []*codec.Frame{
		nil,
		{Width: w, Height: h, Format: codec.RGBA, Planes: f.Planes, Strides: f.Strides},
		{Width: w + 2, Height: h, Format: codec.NV12, Planes: f.Planes, Strides: f.Strides},
		{Width: w, Height: h, Format: codec.NV12, Planes: f.Planes[:1], Strides: f.Strides},
		{Width: w, Height: h, Format: codec.NV12, Planes: [][]byte{f.Planes[0], f.Planes[1][:10]}, Strides: f.Strides},
		{Width: w, Height: h, Format: codec.NV12, Planes: f.Planes, Strides: []int{w - 1, uvStride}},
	}
	for i, b := range bad {
		if err := checkNV12(b, w, h); !errors.Is(err, codec.ErrInvalidData) {
			t.Errorf("bad frame %d: got %v, want ErrInvalidData", i, err)
		}
	}
}
