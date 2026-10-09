package mediafoundation

import (
	"bytes"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

func TestPackNV12(t *testing.T) {
	w, h := 6, 4
	// Source planes with padding in the stride.
	y := make([]byte, 8*h)
	c := make([]byte, 8*(h/2))
	for i := range y {
		y[i] = byte(i)
	}
	for i := range c {
		c[i] = byte(100 + i)
	}
	f := &codec.Frame{Width: w, Height: h, Format: codec.NV12, Planes: [][]byte{y, c}, Strides: []int{8, 8}}
	dst := make([]byte, nv12Size(w, h))
	packNV12(dst, f)
	want := []byte{0, 1, 2, 3, 4, 5, 8, 9, 10, 11, 12, 13, 16, 17, 18, 19, 20, 21, 24, 25, 26, 27, 28, 29,
		100, 101, 102, 103, 104, 105, 108, 109, 110, 111, 112, 113}
	if !bytes.Equal(dst, want) {
		t.Fatalf("packed %v\nwant   %v", dst, want)
	}
}

func TestCheckFrame(t *testing.T) {
	cfg := &codec.EncoderConfig{Codec: codec.H264, Width: 4, Height: 2, InputFormat: codec.NV12}
	good := &codec.Frame{Width: 4, Height: 2, Format: codec.NV12, Planes: [][]byte{make([]byte, 8), make([]byte, 4)}, Strides: []int{4, 4}}
	if err := checkFrame(cfg, good); err != nil {
		t.Fatal(err)
	}
	bad := []*codec.Frame{
		nil,
		{Width: 4, Height: 2, Format: codec.PixelFormat(9), Planes: good.Planes, Strides: good.Strides},
		{Width: 2, Height: 2, Format: codec.NV12, Planes: good.Planes, Strides: good.Strides},
		{Width: 4, Height: 2, Format: codec.NV12, Planes: good.Planes[:1], Strides: good.Strides},
		{Width: 4, Height: 2, Format: codec.NV12, Planes: good.Planes, Strides: []int{2, 4}},
		{Width: 4, Height: 2, Format: codec.NV12, Planes: [][]byte{make([]byte, 7), make([]byte, 4)}, Strides: good.Strides},
	}
	for i, f := range bad {
		if err := checkFrame(cfg, f); !errors.Is(err, codec.ErrInvalidData) {
			t.Errorf("case %d: got %v, want ErrInvalidData", i, err)
		}
	}
}

func TestFinishPacket(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x1e, 0x80}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x84, 0x00}
	p := []byte{0x41, 0x9a, 0x00}
	sc := []byte{0, 0, 0, 1}
	au := func(nals ...[]byte) []byte {
		var out []byte
		for _, n := range nals {
			out = append(out, sc...)
			out = append(out, n...)
		}
		return out
	}
	params := newParamSets(codec.H264)

	// First keyframe carries its parameter sets: stored, unchanged.
	pkt := finishPacket(codec.H264, params, au(sps, pps, idr), 10, 10)
	if !pkt.Keyframe || !bytes.Equal(pkt.Data, au(sps, pps, idr)) || pkt.PTS != 10 {
		t.Fatalf("first keyframe: %+v", pkt)
	}
	// A P-frame is not a keyframe and is passed through.
	pkt = finishPacket(codec.H264, params, au(p), 20, 20)
	if pkt.Keyframe || !bytes.Equal(pkt.Data, au(p)) {
		t.Fatalf("p-frame: %+v", pkt)
	}
	// A bare IDR gets the stored sets prepended.
	pkt = finishPacket(codec.H264, params, au(idr), 30, 30)
	if !pkt.Keyframe || !bytes.Equal(pkt.Data, au(sps, pps, idr)) {
		t.Fatalf("bare keyframe: %x", pkt.Data)
	}
	// Without any stored set nothing can be prepended.
	fresh := newParamSets(codec.H264)
	pkt = finishPacket(codec.H264, fresh, au(idr), 40, 40)
	if !pkt.Keyframe || !bytes.Equal(pkt.Data, au(idr)) {
		t.Fatalf("keyframe without stored sets: %x", pkt.Data)
	}
}

func TestDefaultBitrate(t *testing.T) {
	if got := defaultBitrate(1920, 1080, 30); got != 6_220_800 {
		t.Errorf("1080p30 = %d", got)
	}
	if got := defaultBitrate(160, 120, 30); got != 200_000 {
		t.Errorf("tiny = %d, want the floor", got)
	}
	if got := defaultBitrate(1280, 720, 0); got != defaultBitrate(1280, 720, 30) {
		t.Errorf("unknown rate should assume 30 fps")
	}
}
