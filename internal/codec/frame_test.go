package codec

import (
	"errors"
	"math"
	"testing"
)

func TestFrameValidate(t *testing.T) {
	cases := []struct {
		name string
		f    *Frame
		ok   bool
	}{
		{"nil", nil, false},
		{"native", &Frame{Format: Native, Width: 1, Height: 1}, false},
		{"negative", &Frame{Format: RGBA, Width: -1, Height: 1}, false},
		{"zero", &Frame{Format: RGBA, Width: 1, Height: 0}, false},
		{"overflow", &Frame{Format: RGBA, Width: math.MaxInt, Height: 4}, false},
		{"missing plane", &Frame{Format: RGBA, Width: 1, Height: 1}, false},
		{"negative stride", &Frame{Format: RGBA, Width: 1, Height: 2, Planes: [][]byte{make([]byte, 8)}, Strides: []int{-4}}, false},
		{"overlapping rows", &Frame{Format: RGBA, Width: 2, Height: 2, Planes: [][]byte{make([]byte, 16)}, Strides: []int{4}}, false},
		{"overflow stride", &Frame{Format: RGBA, Width: 1, Height: 4, Planes: [][]byte{make([]byte, 16)}, Strides: []int{math.MaxInt / 2}}, false},
		{"last row without padding", &Frame{Format: RGBA, Width: 1, Height: 2, Planes: [][]byte{make([]byte, 12)}, Strides: []int{8}}, true},
		{"single row large stride", &Frame{Format: BGRA, Width: 1, Height: 1, Planes: [][]byte{make([]byte, 4)}, Strides: []int{math.MaxInt}}, true},
		{"odd NV12", &Frame{Format: NV12, Width: 3, Height: 3, Planes: [][]byte{make([]byte, 9), make([]byte, 8)}, Strides: []int{3, 4}}, true},
		{"short NV12 chroma", &Frame{Format: NV12, Width: 3, Height: 3, Planes: [][]byte{make([]byte, 9), make([]byte, 7)}, Strides: []int{3, 4}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.f.Validate()
			if tc.ok && err != nil || !tc.ok && !errors.Is(err, ErrInvalidData) {
				t.Fatalf("Validate() = %v, want valid=%v", err, tc.ok)
			}
			if !tc.ok {
				if _, err := tc.f.RGBAImage(); !errors.Is(err, ErrInvalidData) {
					t.Fatalf("RGBAImage() = %v, want ErrInvalidData", err)
				}
			}
		})
	}
}

func TestFrameSizeOverflow(t *testing.T) {
	for _, format := range []PixelFormat{NV12, RGBA, BGRA} {
		for _, dims := range [][2]int{{0, 1}, {1, -1}, {math.MaxInt, 3}, {3, math.MaxInt}} {
			if n := format.FrameSize(dims[0], dims[1]); n != 0 {
				t.Errorf("%s FrameSize(%d, %d) = %d", format, dims[0], dims[1], n)
			}
		}
	}
	if n := NV12.FrameSize(3, 3); n != 17 {
		t.Errorf("odd NV12 frame size = %d, want 17", n)
	}
	if RGBAFrame(nil, 0) != nil {
		t.Error("RGBAFrame(nil) must return nil")
	}
}
