package hevc

import (
	"encoding/hex"
	"testing"
)

// Parameter sets and slice segment headers of a 320x240 libx265 stream with
// three B-frames, taken from the ffmpeg test oracle.
var (
	x265SPS = mustHex("42010101600000030090000003000003003ca00a080f16595964932bc05a020000030002000003003c10")
	x265PPS = mustHex("4401c172b46240")
	// First slice segments in decode order: IDR_N_LP, TRAIL_R, TRAIL_R,
	// TRAIL_N, TRAIL_N, TRAIL_R, TRAIL_R, TRAIL_N.
	x265Slices = []string{
		"2801af08482f002d07c163fc",
		"0201d02149e10c2042415c69",
		"0201e044957820484137209e",
		"0001e024fd7e89029208af05",
		"0001e066b5fd42050410fe11",
		"0201d0389755f510c0810105",
		"0201e0c22555fd4204a412f6",
		"0001e0a6f5ff489028208931",
	}
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestParseSPS(t *testing.T) {
	s, err := ParseSPS(x265SPS)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 0 || s.VPSID != 0 || s.MaxSubLayersMinus1 != 0 || s.ChromaFormatIDC != 1 {
		t.Errorf("sps = %+v", s)
	}
	if s.Width != 320 || s.Height != 240 {
		t.Errorf("size %dx%d", s.Width, s.Height)
	}
	if s.Log2MaxPOCLsb != 8 {
		t.Errorf("log2_max_pic_order_cnt_lsb = %d", s.Log2MaxPOCLsb)
	}
	if len(s.MaxNumReorderPics) != 1 || s.MaxNumReorderPics[0] != 2 || s.MaxDecPicBufferingMinus1[0] != 4 {
		t.Errorf("reorder %v dpb %v", s.MaxNumReorderPics, s.MaxDecPicBufferingMinus1)
	}
	if got := s.MaxReorderFrames(); got != 2 {
		t.Errorf("MaxReorderFrames = %d", got)
	}
}

func TestParsePPS(t *testing.T) {
	p, err := ParsePPS(x265PPS)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 0 || p.SPSID != 0 || p.DependentSliceSegmentsEnabled || p.OutputFlagPresent || p.NumExtraSliceHeaderBits != 0 {
		t.Errorf("pps = %+v", p)
	}
}

func TestSliceHeadersAndPOC(t *testing.T) {
	sps, err := ParseSPS(x265SPS)
	if err != nil {
		t.Fatal(err)
	}
	pps, err := ParsePPS(x265PPS)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(id uint32) (*PPS, *SPS) {
		if id != 0 {
			return nil, nil
		}
		return pps, sps
	}
	var st POCState
	var pocs []int32
	wantTypes := []int{NALIDRNLP, 1, 1, 0, 0, 1, 1, 0}
	for i, hx := range x265Slices {
		nal := mustHex(hx)
		if ty := Type(nal); ty != wantTypes[i] {
			t.Errorf("slice %d: type %d, want %d", i, ty, wantTypes[i])
		}
		if tid := TemporalID(nal); tid != 0 {
			t.Errorf("slice %d: temporal id %d", i, tid)
		}
		h, err := ParseSliceHeader(nal, lookup)
		if err != nil {
			t.Fatalf("slice %d: %v", i, err)
		}
		if h.PPSID != 0 || !h.PicOutput {
			t.Errorf("slice %d: header %+v", i, h)
		}
		pocs = append(pocs, st.Next(sps, h, i == 0))
	}
	// Decode order I P B b b P B b; the second P is the last frame of the
	// eight-frame stream.
	want := []int32{0, 4, 2, 1, 3, 7, 6, 5}
	for i := range want {
		if pocs[i] != want[i] {
			t.Fatalf("POCs = %v, want %v", pocs, want)
		}
	}
}

func TestSliceHeaderErrors(t *testing.T) {
	if _, err := ParseSliceHeader(x265SPS, nil); err != ErrNotSlice {
		t.Errorf("SPS as slice: %v", err)
	}
	none := func(uint32) (*PPS, *SPS) { return nil, nil }
	if _, err := ParseSliceHeader(mustHex(x265Slices[0]), none); err == nil {
		t.Error("unknown PPS was accepted")
	}
	// A dependent slice segment (first_slice_segment_in_pic_flag = 0).
	if _, err := ParseSliceHeader([]byte{0x02, 0x01, 0x40, 0x00}, none); err != ErrNotFirstSegment {
		t.Errorf("second segment: %v", err)
	}
	if _, err := ParseSPS(x265SPS[:6]); err == nil {
		t.Error("truncated SPS was accepted")
	}
}

func TestPOCWrapAndCRA(t *testing.T) {
	sps := &SPS{Log2MaxPOCLsb: 4, MaxNumReorderPics: []uint32{0}}
	var st POCState
	var pocs []int32
	for i := 0; i < 24; i++ {
		h := &SliceHeader{NALType: 1, POCLsb: uint32(i % 16)}
		if i == 0 {
			h.NALType = NALIDRWRADL
			h.POCLsb = 0
		}
		pocs = append(pocs, st.Next(sps, h, i == 0))
	}
	for i := 1; i < len(pocs); i++ {
		if pocs[i] != pocs[i-1]+1 {
			t.Fatalf("POC sequence %v is not monotonic across the lsb wrap", pocs)
		}
	}
	// A CRA in the middle of the stream (NoRaslOutputFlag = 0) continues
	// the count; the same CRA at the start of decoding restarts the msb.
	cra := &SliceHeader{NALType: NALCRA, POCLsb: 8}
	if got := st.Next(sps, cra, false); got != 24 {
		t.Errorf("mid-stream CRA POC = %d, want 24", got)
	}
	st.Reset()
	if got := st.Next(sps, cra, true); got != 8 {
		t.Errorf("CRA after reset POC = %d, want 8", got)
	}
}
