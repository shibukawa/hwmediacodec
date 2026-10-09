package h264

import (
	"encoding/hex"
	"testing"
)

// Parameter sets and slice headers of a 320x240 libx264 stream with three
// B-frames (b-pyramid), taken from the ffmpeg test oracle.
var (
	x264SPS = mustHex("6764000dacd94141fb0110000003001000000303c0f1429960")
	x264PPS = mustHex("68ef8fcb")
	// First VCL NAL units in decode order: IDR, P, B, b, P, B, b, b.
	x264Slices = []string{
		"6588840057e9d7ad678e1baf",
		"419a23188affbc87dbdb1f72",
		"419e414214fffeeb656f56e6",
		"019e62442dfffeec782c2621",
		"419a67344c477fca0dc771db",
		"419e8545112c25ffb63bff16",
		"019ea4442dffb473964e8244",
		"019ea6442dffb6ba8ce57dc2",
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
	s, err := ParseSPS(x264SPS)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 0 || s.ProfileIDC != 100 || s.LevelIDC != 13 || s.ChromaFormatIDC != 1 {
		t.Errorf("id/profile/level/chroma = %d/%d/%d/%d", s.ID, s.ProfileIDC, s.LevelIDC, s.ChromaFormatIDC)
	}
	if s.POCType != 0 || s.Log2MaxPOCLsb < 4 {
		t.Errorf("poc type %d, log2 max lsb %d", s.POCType, s.Log2MaxPOCLsb)
	}
	if s.Width() != 320 || s.Height() != 240 || !s.FrameMbsOnly {
		t.Errorf("size %dx%d frame_mbs_only=%v", s.Width(), s.Height(), s.FrameMbsOnly)
	}
	if s.MaxNumRefFrames != 4 {
		t.Errorf("max_num_ref_frames = %d", s.MaxNumRefFrames)
	}
	if !s.HasBitstreamRestriction || s.MaxNumReorderFrames != 2 || s.MaxDecFrameBuffering != 4 {
		t.Errorf("bitstream restriction %v reorder=%d dpb=%d", s.HasBitstreamRestriction, s.MaxNumReorderFrames, s.MaxDecFrameBuffering)
	}
	if !s.HasTiming || s.NumUnitsInTick != 1 || s.TimeScale != 60 {
		t.Errorf("timing %v %d/%d", s.HasTiming, s.NumUnitsInTick, s.TimeScale)
	}
	if got := s.MaxReorderFrames(); got != 2 {
		t.Errorf("MaxReorderFrames = %d, want 2", got)
	}
}

func TestParsePPS(t *testing.T) {
	p, err := ParsePPS(x264PPS)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 0 || p.SPSID != 0 || p.BottomFieldPicOrderInFramePresent {
		t.Errorf("pps = %+v", p)
	}
}

func TestSliceHeadersAndPOC(t *testing.T) {
	sps, err := ParseSPS(x264SPS)
	if err != nil {
		t.Fatal(err)
	}
	pps, err := ParsePPS(x264PPS)
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
	for i, hx := range x264Slices {
		h, err := ParseSliceHeader(mustHex(hx), lookup)
		if err != nil {
			t.Fatalf("slice %d: %v", i, err)
		}
		if h.FirstMbInSlice != 0 || h.PPSID != 0 {
			t.Errorf("slice %d: first_mb=%d pps=%d", i, h.FirstMbInSlice, h.PPSID)
		}
		if (i == 0) != h.IDR {
			t.Errorf("slice %d: IDR=%v", i, h.IDR)
		}
		pocs = append(pocs, st.Next(sps, h))
	}
	// Decode order I P B b P B b b (b-pyramid: the middle B of each group
	// is a reference and is decoded first); POCs count two per frame.
	want := []int32{0, 6, 2, 4, 14, 10, 8, 12}
	for i := range want {
		if pocs[i] != want[i] {
			t.Fatalf("POCs = %v, want %v", pocs, want)
		}
	}
}

func TestSliceHeaderErrors(t *testing.T) {
	if _, err := ParseSliceHeader(mustHex("6764000d"), nil); err != ErrNotSlice {
		t.Errorf("SPS as slice: %v", err)
	}
	none := func(uint32) (*PPS, *SPS) { return nil, nil }
	if _, err := ParseSliceHeader(mustHex(x264Slices[0]), none); err == nil {
		t.Error("unknown PPS was accepted")
	}
	if _, err := ParseSPS([]byte{0x67, 0x64}); err == nil {
		t.Error("truncated SPS was accepted")
	}
}

func TestMaxReorderFramesWithoutVUI(t *testing.T) {
	base := SPS{ProfileIDC: 100, LevelIDC: 13, POCType: 0, MaxNumRefFrames: 1, PicWidthInMbs: 20, PicHeightInMapUnits: 15, FrameMbsOnly: true}
	cases := []struct {
		name string
		mod  func(*SPS)
		want int
	}{
		{"level 1.3 at 320x240", func(*SPS) {}, 7},
		{"level 4.0 at 1080p", func(s *SPS) { s.LevelIDC = 40; s.PicWidthInMbs, s.PicHeightInMapUnits = 120, 68 }, 4},
		{"level 5.1 at 320x240 caps at 16", func(s *SPS) { s.LevelIDC = 51 }, 16},
		{"poc type 2", func(s *SPS) { s.POCType = 2 }, 0},
		{"intra only", func(s *SPS) { s.MaxNumRefFrames = 0 }, 0},
		{"constrained high", func(s *SPS) { s.ConstraintFlags = 0x10 }, 0},
		{"baseline keeps dpb", func(s *SPS) { s.ProfileIDC = 66; s.ConstraintFlags = 0x10 }, 7},
		{"vui wins", func(s *SPS) { s.HasBitstreamRestriction = true; s.MaxNumReorderFrames = 1; s.MaxDecFrameBuffering = 3 }, 1},
		{"vui capped by dpb", func(s *SPS) { s.HasBitstreamRestriction = true; s.MaxNumReorderFrames = 5; s.MaxDecFrameBuffering = 3 }, 3},
	}
	for _, c := range cases {
		s := base
		c.mod(&s)
		if got := s.MaxReorderFrames(); got != c.want {
			t.Errorf("%s: MaxReorderFrames = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestPOCType2AndWrap(t *testing.T) {
	sps := &SPS{Log2MaxFrameNum: 4, POCType: 2}
	var st POCState
	var pocs []int32
	for i := 0; i < 20; i++ {
		h := &SliceHeader{NalRefIDC: 1, FrameNum: uint32(i % 16), IDR: i == 0}
		if i == 0 {
			h.NALType = NALSliceIDR
		}
		pocs = append(pocs, st.Next(sps, h))
	}
	for i := 1; i < len(pocs); i++ {
		if pocs[i] != pocs[i-1]+2 {
			t.Fatalf("POC type 2 sequence %v is not monotonic across the frame_num wrap", pocs)
		}
	}

	// POC type 0: the lsb wraps and the msb must follow.
	sps = &SPS{Log2MaxFrameNum: 4, POCType: 0, Log2MaxPOCLsb: 4}
	st.Reset()
	pocs = nil
	for i := 0; i < 12; i++ {
		h := &SliceHeader{NalRefIDC: 1, POCLsb: uint32((2 * i) % 16), IDR: i == 0}
		pocs = append(pocs, st.Next(sps, h))
	}
	for i := 1; i < len(pocs); i++ {
		if pocs[i] != pocs[i-1]+2 {
			t.Fatalf("POC type 0 sequence %v is not monotonic across the lsb wrap", pocs)
		}
	}
}
