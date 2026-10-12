package mediacodec

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

// mediaImage2 serialises a MediaImage2 for the parser.
func mediaImage2(width, height int, planes [3]plane) []byte {
	b := make([]byte, sys.MediaImage2Size)
	put := func(off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
	put(0, sys.MediaImageTypeYUV)
	put(4, 3)
	put(8, uint32(width))
	put(12, uint32(height))
	put(16, 8)
	put(20, 8)
	for i, p := range planes {
		off := 24 + i*20
		put(off, uint32(p.offset))
		put(off+4, uint32(int32(p.colInc)))
		put(off+8, uint32(int32(p.rowInc)))
		sub := uint32(2)
		if i == 0 {
			sub = 1
		}
		put(off+12, sub)
		put(off+16, sub)
	}
	return b
}

// picture is a synthetic NV12 picture whose samples encode their position.
func picture(width, height int) (y, uv []byte) {
	cw, ch := (width+1)/2, (height+1)/2
	y = make([]byte, width*height)
	uv = make([]byte, cw*2*ch)
	for r := 0; r < height; r++ {
		for x := 0; x < width; x++ {
			y[r*width+x] = byte(r*7 + x*3)
		}
	}
	for r := 0; r < ch; r++ {
		for x := 0; x < cw; x++ {
			uv[r*cw*2+2*x] = byte(100 + r*5 + x)
			uv[r*cw*2+2*x+1] = byte(200 - r*3 - x)
		}
	}
	return y, uv
}

func TestLayoutRoundTrip(t *testing.T) {
	const w, h = 37, 23 // odd sizes exercise the rounding
	y, uv := picture(w, h)
	cases := []struct {
		name string
		l    layout
	}{
		{"nv12 tight", layout{width: w, height: h, y: plane{0, 1, w}, u: plane{w * h, 2, (w + 1) / 2 * 2}, v: plane{w*h + 1, 2, (w + 1) / 2 * 2}}},
		{"nv12 strided", layout{width: w, height: h, y: plane{0, 1, 64}, u: plane{64 * 32, 2, 64}, v: plane{64*32 + 1, 2, 64}}},
		{"nv21", layout{width: w, height: h, y: plane{0, 1, 64}, u: plane{64*32 + 1, 2, 64}, v: plane{64 * 32, 2, 64}}},
		{"i420", layout{width: w, height: h, y: plane{0, 1, 48}, u: plane{48 * 24, 1, 24}, v: plane{48*24 + 24*12, 1, 24}}},
		{"yv12", layout{width: w, height: h, y: plane{0, 1, 48}, u: plane{48*24 + 24*12, 1, 24}, v: plane{48 * 24, 1, 24}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, tc.l.size()+16)
			for i := range buf {
				buf[i] = 0xEE
			}
			tc.l.unpackNV12(buf, y, w, uv, (w+1)/2*2, w, h)
			gotY := make([]byte, len(y))
			gotUV := make([]byte, len(uv))
			tc.l.packNV12(gotY, gotUV, buf, 0, 0, w, h)
			if !bytes.Equal(gotY, y) {
				t.Error("luma differs after the round trip")
			}
			if !bytes.Equal(gotUV, uv) {
				t.Error("chroma differs after the round trip")
			}
		})
	}
}

func TestPackNV12Crop(t *testing.T) {
	// A 64x48 buffer holding a 50x30 picture at (8, 4): what a decoder
	// reports through the crop rectangle.
	const bw, bh = 64, 48
	l := layout{width: bw, height: bh, y: plane{0, 1, bw}, u: plane{bw * bh, 2, bw}, v: plane{bw*bh + 1, 2, bw}}
	fy, fuv := picture(bw, bh)
	buf := make([]byte, l.size())
	l.unpackNV12(buf, fy, bw, fuv, bw, bw, bh)
	const left, top, w, h = 8, 4, 50, 30
	y := make([]byte, w*h)
	uv := make([]byte, w*((h+1)/2))
	l.packNV12(y, uv, buf, left, top, w, h)
	for r := 0; r < h; r++ {
		for x := 0; x < w; x++ {
			if y[r*w+x] != fy[(top+r)*bw+left+x] {
				t.Fatalf("luma (%d,%d) wrong", x, r)
			}
		}
	}
	for r := 0; r < (h+1)/2; r++ {
		for x := 0; x < w; x++ {
			if uv[r*w+x] != fuv[(top/2+r)*bw+left+x] {
				t.Fatalf("chroma (%d,%d) wrong", x, r)
			}
		}
	}
}

func TestParseMediaImage2(t *testing.T) {
	want := layout{width: 320, height: 240, y: plane{0, 1, 384}, u: plane{384 * 240, 2, 384}, v: plane{384*240 + 1, 2, 384}}
	b := mediaImage2(320, 240, [3]plane{want.y, want.u, want.v})
	got, err := parseMediaImage2(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if !got.isNV12() {
		t.Error("interleaved CbCr with Cb first must be recognised as NV12")
	}
	// size reaches the last chroma sample (a Cr byte, one past the last
	// Cb), not the end of its row.
	if got.size() != 384*240+1+119*384+159*2+1 {
		t.Errorf("size %d", got.size())
	}
	// Not YUV.
	bad := append([]byte(nil), b...)
	binary.LittleEndian.PutUint32(bad, 0x300)
	if _, err := parseMediaImage2(bad); err == nil {
		t.Error("an RGB image must be rejected")
	}
	// Too short.
	if _, err := parseMediaImage2(b[:50]); err == nil {
		t.Error("a short structure must be rejected")
	}
	// 4:2:2 chroma.
	bad = append([]byte(nil), b...)
	binary.LittleEndian.PutUint32(bad[24+20+16:], 1)
	if _, err := parseMediaImage2(bad); err == nil {
		t.Error("4:2:2 must be rejected")
	}
}

func TestLayoutFromColorFormat(t *testing.T) {
	l, err := layoutFromColorFormat(sys.ColorFormatYUV420SemiPlanar, 1920, 1080, 1920, 1088)
	if err != nil {
		t.Fatal(err)
	}
	// size covers the samples of the 1080 picture rows, not the padding
	// rows up to the slice height.
	if !l.isNV12() || l.u.offset != 1920*1088 || l.size() != 1920*1088+539*1920+1920 {
		t.Errorf("semi-planar layout %+v size %d", l, l.size())
	}
	l, err = layoutFromColorFormat(sys.ColorFormatYUV420Planar, 320, 240, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if l.isNV12() || l.u != (plane{320 * 240, 1, 160}) || l.v != (plane{320*240 + 160*120, 1, 160}) {
		t.Errorf("planar layout %+v", l)
	}
	if _, err := layoutFromColorFormat(sys.ColorFormatYUV420Flexible, 320, 240, 0, 0); err == nil {
		t.Error("the flexible format alone does not describe a layout")
	}
}
