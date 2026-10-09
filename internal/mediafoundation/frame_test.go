package mediafoundation

import (
	"bytes"
	"testing"
)

// makeNV12 builds a padded NV12 surface whose luma byte at (x, y) is
// 10+x+y*3 and whose chroma byte at (x, y) is 200-x-y.
func makeNV12(pitch, allocHeight int) nv12Image {
	data := make([]byte, pitch*allocHeight*3/2)
	for y := 0; y < allocHeight; y++ {
		for x := 0; x < pitch; x++ {
			data[y*pitch+x] = byte(10 + x + y*3)
		}
	}
	for y := 0; y < allocHeight/2; y++ {
		for x := 0; x < pitch; x++ {
			data[pitch*allocHeight+y*pitch+x] = byte(200 - x - y)
		}
	}
	return nv12Image{data: data, pitch: pitch, allocHeight: allocHeight}
}

func TestCopyNV12Crop(t *testing.T) {
	src := makeNV12(64, 48) // e.g. a 1080p-style padded surface in miniature
	r := rect{x: 0, y: 0, w: 50, h: 45}
	dst := make([]byte, nv12Size(r.w, r.h))
	planes, strides, err := copyNV12(dst, src, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(planes) != 2 || strides[0] != 50 || strides[1] != 50 {
		t.Fatalf("planes=%d strides=%v", len(planes), strides)
	}
	for y := 0; y < r.h; y++ {
		for x := 0; x < r.w; x++ {
			if got, want := planes[0][y*strides[0]+x], byte(10+x+y*3); got != want {
				t.Fatalf("luma (%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
	for y := 0; y < (r.h+1)/2; y++ {
		for x := 0; x < strides[1]; x++ {
			if got, want := planes[1][y*strides[1]+x], byte(200-x-y); got != want {
				t.Fatalf("chroma (%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
	if len(planes[1]) != 50*23 {
		t.Fatalf("chroma plane has %d bytes, want %d", len(planes[1]), 50*23)
	}
}

func TestCopyNV12Offset(t *testing.T) {
	src := makeNV12(32, 32)
	r := rect{x: 4, y: 6, w: 8, h: 8}
	dst := make([]byte, nv12Size(r.w, r.h))
	planes, _, err := copyNV12(dst, src, r)
	if err != nil {
		t.Fatal(err)
	}
	if planes[0][0] != byte(10+4+6*3) {
		t.Fatalf("luma origin = %d", planes[0][0])
	}
	// Chroma row 3 of the surface, columns 4..11.
	wantChroma := src.data[32*32+3*32+4 : 32*32+3*32+12]
	if !bytes.Equal(planes[1][:8], wantChroma) {
		t.Fatalf("chroma row 0 = %v want %v", planes[1][:8], wantChroma)
	}
}

func TestCopyNV12Errors(t *testing.T) {
	src := makeNV12(16, 16)
	cases := []rect{
		{0, 0, 0, 8},   // empty
		{0, 0, 17, 8},  // wider than pitch
		{0, 0, 16, 17}, // taller than surface
		{-1, 0, 4, 4},  // negative offset
	}
	for _, r := range cases {
		if _, _, err := copyNV12(make([]byte, 1024), src, r); err == nil {
			t.Errorf("rect %+v: expected an error", r)
		}
	}
	if _, _, err := copyNV12(make([]byte, 10), src, rect{0, 0, 16, 16}); err == nil {
		t.Error("small destination: expected an error")
	}
	short := nv12Image{data: src.data[:len(src.data)-1], pitch: 16, allocHeight: 16}
	if _, _, err := copyNV12(make([]byte, 1024), short, rect{0, 0, 16, 16}); err == nil {
		t.Error("short source: expected an error")
	}
}

func TestRectClampTo(t *testing.T) {
	r := rect{x: 0, y: 0, w: 1920, h: 1080}.clampTo(1920, 1088)
	if r != (rect{0, 0, 1920, 1080}) {
		t.Fatalf("got %+v", r)
	}
	r = rect{x: 8, y: 8, w: 1920, h: 1088}.clampTo(1920, 1088)
	if r != (rect{8, 8, 1912, 1080}) {
		t.Fatalf("got %+v", r)
	}
}
