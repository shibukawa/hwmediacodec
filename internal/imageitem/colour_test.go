package imageitem

import (
	"image/color"
	"testing"

	"github.com/shibukawa/hwmediacodec"
)

// TestNV12Image converts flat pictures whose Y'CbCr values are known
// encodings of known colours, in both ranges and matrices.
func TestNV12Image(t *testing.T) {
	for _, tc := range []struct {
		name      string
		c         nclx
		y, cb, cr uint8
		want      color.RGBA
	}{
		// JFIF (full-range BT.601): red is Y 76, Cb 85, Cr 255.
		{"full 601 red", nclx{matrix: 6, fullRange: true}, 76, 85, 255, color.RGBA{R: 254, G: 0, B: 0, A: 255}},
		{"full 601 grey", nclx{matrix: 5, fullRange: true}, 128, 128, 128, color.RGBA{R: 128, G: 128, B: 128, A: 255}},
		{"full 601 white", nclx{matrix: 6, fullRange: true}, 255, 128, 128, color.RGBA{R: 255, G: 255, B: 255, A: 255}},
		// Video-range BT.709: white is Y 235, black Y 16, red is 63/102/240.
		{"video 709 white", nclx{matrix: 1}, 235, 128, 128, color.RGBA{R: 255, G: 255, B: 255, A: 255}},
		{"video 709 black", nclx{matrix: 1}, 16, 128, 128, color.RGBA{R: 0, G: 0, B: 0, A: 255}},
		{"video 709 red", nclx{matrix: 1}, 63, 102, 240, color.RGBA{R: 255, G: 0, B: 0, A: 255}},
		// The same samples read as full range are a different, duller colour:
		// this is the mistake the conversion exists to avoid.
		{"full 709 of video white", nclx{matrix: 1, fullRange: true}, 235, 128, 128, color.RGBA{R: 235, G: 235, B: 235, A: 255}},
	} {
		const w, h = 6, 4
		fr := &hwmediacodec.Frame{Width: w, Height: h, Format: hwmediacodec.NV12,
			Planes: [][]byte{make([]byte, 8*h), make([]byte, 8*h/2)}, Strides: []int{8, 8}}
		for i := range fr.Planes[0] {
			fr.Planes[0][i] = tc.y
		}
		for i := 0; i < len(fr.Planes[1]); i += 2 {
			fr.Planes[1][i], fr.Planes[1][i+1] = tc.cb, tc.cr
		}
		img := nv12Image(fr, tc.c)
		if img.Rect.Dx() != w || img.Rect.Dy() != h {
			t.Fatalf("%s: %v", tc.name, img.Rect)
		}
		for _, pt := range [][2]int{{0, 0}, {w - 1, h - 1}} {
			got := img.RGBAAt(pt[0], pt[1])
			if d := maxDiff(got, tc.want); d > 1 {
				t.Errorf("%s at %v: %v, want %v", tc.name, pt, got, tc.want)
			}
		}
	}
}

func maxDiff(a, b color.RGBA) int {
	d := 0
	for _, v := range []int{int(a.R) - int(b.R), int(a.G) - int(b.G), int(a.B) - int(b.B), int(a.A) - int(b.A)} {
		if v < 0 {
			v = -v
		}
		if v > d {
			d = v
		}
	}
	return d
}

func TestItemNCLX(t *testing.T) {
	it := &item{props: []property{
		{typ: "colr", data: append([]byte("prof"), make([]byte, 16)...)},
		{typ: "colr", data: []byte{'n', 'c', 'l', 'x', 0, 1, 0, 13, 0, 6, 0x80}},
	}}
	c, ok := it.nclx()
	if !ok || c != (nclx{primaries: 1, transfer: 13, matrix: 6, fullRange: true}) {
		t.Errorf("nclx %+v %v", c, ok)
	}
	if _, ok := (&item{}).nclx(); ok {
		t.Error("an item without colr reports nclx")
	}
}
