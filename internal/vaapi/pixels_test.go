package vaapi

import (
	"math"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

func TestFillImageValidatesLayout(t *testing.T) {
	f := &codec.Frame{Format: codec.NV12, Width: 4, Height: 4, Planes: [][]byte{make([]byte, 16), make([]byte, 8)}, Strides: []int{4, 4}}
	img := sys.Image{NumPlanes: 2, Pitches: [3]uint32{8, 8}, Offsets: [3]uint32{0, 64}}
	dst := make([]byte, 96)
	if err := fillImage(dst, &img, f, 8, 8); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{4, 2}, {2, 4}, {8, math.MaxInt}} {
		if err := fillImage(dst, &img, f, size[0], size[1]); err == nil {
			t.Errorf("image %v: expected layout error", size)
		}
	}
	img.Pitches[1] = math.MaxUint32
	if err := fillImage(dst, &img, f, 8, 8); err == nil {
		t.Error("overflowing plane pitch: expected layout error")
	}
}

func TestImageRegionRejectsOverflow(t *testing.T) {
	img := sys.Image{NumPlanes: 2, Pitches: [3]uint32{8, 8}, Offsets: [3]uint32{0, 64}}
	data := make([]byte, 96)
	for _, r := range [][4]int{{math.MaxInt, 0, 1, 1}, {0, math.MaxInt, 2, 4}, {0, 0, math.MaxInt, 4}, {0, 0, 1, math.MaxInt}, {0, -1, 1, 4}, {0, 0, 0, 4}, {7, 0, 1, 2}} {
		if _, _, err := imageRegion(data, &img, 0, r[0], r[1], r[2], r[3]); err == nil {
			t.Errorf("region %v: expected layout error", r)
		}
	}
	region, pitch, err := imageRegion(data, &img, 1, 2, 1, 2, 4)
	if err != nil || pitch != 8 || len(region) != 22 {
		t.Fatalf("valid crop: len=%d pitch=%d err=%v", len(region), pitch, err)
	}
}
