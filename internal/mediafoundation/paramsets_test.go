package mediafoundation

import (
	"bytes"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

func TestParamSets(t *testing.T) {
	s := newParamSets(codec.H264)
	sps1 := []byte{0x67, 0x42, 0x00, 0x1e, 0x40} // id 1
	sps0 := []byte{0x67, 0x42, 0x00, 0x1e, 0x80} // id 0
	pps0 := []byte{0x68, 0xce, 0x38, 0x80}       // id 0
	for _, nal := range [][]byte{sps1, sps0, pps0} {
		if _, ok := s.add(annexb.NALUnitType(codec.H264, nal), nal); !ok {
			t.Fatalf("add %x failed", nal)
		}
	}
	if _, ok := s.add(5, []byte{0x65, 0x88}); ok {
		t.Fatal("a slice must not be stored")
	}
	want := bytes.Join([][]byte{{0, 0, 0, 1}, sps0, {0, 0, 0, 1}, sps1, {0, 0, 0, 1}, pps0}, nil)
	if got := s.annexB(); !bytes.Equal(got, want) {
		t.Fatalf("annexB = %x, want %x", got, want)
	}
	// Replacing a set keeps one entry per id.
	sps0b := []byte{0x67, 0x42, 0x00, 0x1f, 0x80}
	s.add(annexb.NALUnitType(codec.H264, sps0b), sps0b)
	if got := s.annexB(); bytes.Contains(got, sps0) || !bytes.Contains(got, sps0b) {
		t.Fatalf("replacement failed: %x", got)
	}
	if s.covers([3]bool{false, true, false}) {
		t.Fatal("SPS alone must not count as complete for H.264")
	}
	if !s.covers([3]bool{false, true, true}) {
		t.Fatal("SPS+PPS must count as complete for H.264")
	}
	h := newParamSets(codec.HEVC)
	if h.covers([3]bool{false, true, true}) {
		t.Fatal("HEVC also needs a VPS")
	}
	if !h.covers([3]bool{true, true, true}) {
		t.Fatal("VPS+SPS+PPS must count as complete for HEVC")
	}
}

func TestDecodeVideoArea(t *testing.T) {
	// MFVideoArea{OffsetX{0,8}, OffsetY{0,4}, {1920,1080}} little endian.
	b := []byte{0, 0, 8, 0, 0, 0, 4, 0, 0x80, 0x07, 0, 0, 0x38, 0x04, 0, 0}
	r, ok := decodeVideoArea(b)
	if !ok || r != (rect{8, 4, 1920, 1080}) {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
	if _, ok := decodeVideoArea(b[:15]); ok {
		t.Fatal("short blob must be rejected")
	}
}
