package codec

import (
	"errors"
	"image"
	"image/color"
	"testing"
)

func TestRGBAImage(t *testing.T) {
	// A 3x2 frame with a stride wider than the picture: the padding must
	// not reach the image.
	const w, h, stride = 3, 2, 16
	pix := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			copy(pix[y*stride+4*x:], []byte{byte(10 * x), byte(100 + y), 7, 255})
		}
		for i := 4 * w; i < stride; i++ {
			pix[y*stride+i] = 0xEE
		}
	}
	for _, f := range []PixelFormat{RGBA, BGRA} {
		fr := &Frame{Width: w, Height: h, Format: f, Planes: [][]byte{pix}, Strides: []int{stride}}
		img, err := fr.RGBAImage()
		if err != nil {
			t.Fatal(err)
		}
		if img.Rect != image.Rect(0, 0, w, h) || img.Stride != 4*w {
			t.Fatalf("%s: image %v stride %d", f, img.Rect, img.Stride)
		}
		want := color.RGBA{R: 20, G: 101, B: 7, A: 255}
		if f == BGRA {
			want.R, want.B = want.B, want.R
		}
		if got := img.RGBAAt(2, 1); got != want {
			t.Errorf("%s: pixel (2,1) %v, want %v", f, got, want)
		}
		// The image owns its pixels.
		pix[0] ^= 0xFF
		if img.Pix[0] == pix[0] {
			t.Errorf("%s: the image shares the frame's memory", f)
		}
		pix[0] ^= 0xFF
	}
	nv12 := &Frame{Width: 2, Height: 2, Format: NV12, Planes: [][]byte{make([]byte, 4), make([]byte, 2)}, Strides: []int{2, 2}}
	if _, err := nv12.RGBAImage(); !errors.Is(err, ErrInvalidData) {
		t.Errorf("an NV12 frame: %v, want ErrInvalidData", err)
	}
	released := &Frame{Width: 2, Height: 2, Format: RGBA}
	if _, err := released.RGBAImage(); !errors.Is(err, ErrInvalidData) {
		t.Errorf("a frame without planes: %v, want ErrInvalidData", err)
	}
}

func TestRGBAFrame(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.SetRGBA(3, 2, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	f := RGBAFrame(img, 42)
	if f.Width != 8 || f.Height != 6 || f.Format != RGBA || f.PTS != 42 || f.Strides[0] != img.Stride {
		t.Fatalf("frame %+v", f)
	}
	if &f.Planes[0][0] != &img.Pix[0] {
		t.Error("the frame does not share the image's pixels")
	}
	// A sub-image starts inside the parent's buffer and keeps its stride.
	sub := img.SubImage(image.Rect(3, 2, 7, 5)).(*image.RGBA)
	sf := RGBAFrame(sub, 0)
	if sf.Width != 4 || sf.Height != 3 || sf.Strides[0] != img.Stride {
		t.Fatalf("sub-image frame %dx%d stride %d", sf.Width, sf.Height, sf.Strides[0])
	}
	if got := sf.Planes[0][:4]; got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("the sub-image frame starts at %v, want the pixel at (3,2)", got)
	}
	back, err := sf.RGBAImage()
	if err != nil || back.RGBAAt(0, 0) != (color.RGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Errorf("round trip: %v %v", back.RGBAAt(0, 0), err)
	}
}
