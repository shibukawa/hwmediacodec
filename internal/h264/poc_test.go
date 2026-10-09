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

func TestParseX264SPS(t *testing.T) {
	s, err := ParseSPS(x264SPS)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 0 || s.ProfileIDC != 100 || s.LevelIDC != 13 || s.ChromaFormatIDC != 1 {
		t.Errorf("id/profile/level/chroma = %d/%d/%d/%d", s.ID, s.ProfileIDC, s.LevelIDC, s.ChromaFormatIDC)
	}
	if s.PicOrderCntType != 0 || s.Log2MaxPicOrderCntLsb < 4 {
		t.Errorf("poc type %d, log2 max lsb %d", s.PicOrderCntType, s.Log2MaxPicOrderCntLsb)
	}
	if s.Width() != 320 || s.Height() != 240 || !s.FrameMbsOnly {
		t.Errorf("size %dx%d frame_mbs_only=%v", s.Width(), s.Height(), s.FrameMbsOnly)
	}
	if s.MaxNumRefFrames != 4 {
		t.Errorf("max_num_ref_frames = %d", s.MaxNumRefFrames)
	}
	if !s.VUI.BitstreamRestriction || s.VUI.MaxNumReorderFrames != 2 || s.VUI.MaxDecFrameBuffering != 4 {
		t.Errorf("bitstream restriction %v reorder=%d dpb=%d", s.VUI.BitstreamRestriction, s.VUI.MaxNumReorderFrames, s.VUI.MaxDecFrameBuffering)
	}
	if !s.VUI.TimingInfoPresent || s.VUI.NumUnitsInTick != 1 || s.VUI.TimeScale != 60 {
		t.Errorf("timing %v %d/%d", s.VUI.TimingInfoPresent, s.VUI.NumUnitsInTick, s.VUI.TimeScale)
	}
	if got := s.MaxReorderFrames(); got != 2 {
		t.Errorf("MaxReorderFrames = %d, want 2", got)
	}
}

func TestSliceHeadersAndPOC(t *testing.T) {
	ps := NewParameterSets()
	if _, err := ps.AddSPS(x264SPS); err != nil {
		t.Fatal(err)
	}
	if err := ps.AddPPS(x264PPS); err != nil {
		t.Fatal(err)
	}
	var st POCState
	var pocs []int32
	for i, hx := range x264Slices {
		h, sps, _, err := ParseSliceHeader(mustHex(hx), ps)
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
	ps := NewParameterSets()
	if _, _, _, err := ParseSliceHeader(mustHex("6764000d"), ps); err == nil {
		t.Error("SPS was accepted as a slice")
	}
	if _, _, _, err := ParseSliceHeader(mustHex(x264Slices[0]), ps); err != ErrMissingPPS {
		t.Errorf("unknown PPS: %v", err)
	}
	if _, err := ParseSPS([]byte{0x67, 0x64}); err == nil {
		t.Error("truncated SPS was accepted")
	}
}

func TestMaxReorderFramesWithoutVUI(t *testing.T) {
	base := SPS{ProfileIDC: 100, LevelIDC: 13, PicOrderCntType: 0, MaxNumRefFrames: 1, PicWidthInMbs: 20, PicHeightInMapUnits: 15, FrameMbsOnly: true}
	cases := []struct {
		name string
		mod  func(*SPS)
		want int
	}{
		{"level 1.3 at 320x240", func(*SPS) {}, 7},
		{"level 4.0 at 1080p", func(s *SPS) { s.LevelIDC = 40; s.PicWidthInMbs, s.PicHeightInMapUnits = 120, 68 }, 4},
		{"level 5.1 at 320x240 caps at 16", func(s *SPS) { s.LevelIDC = 51 }, 16},
		{"poc type 2", func(s *SPS) { s.PicOrderCntType = 2 }, 0},
		{"intra only", func(s *SPS) { s.MaxNumRefFrames = 0 }, 0},
		{"constrained high", func(s *SPS) { s.ConstraintFlags = 0x10 }, 0},
		{"baseline keeps dpb", func(s *SPS) { s.ProfileIDC = 66; s.ConstraintFlags = 0x10 }, 7},
		{"vui wins", func(s *SPS) {
			s.VUIPresent, s.VUI.BitstreamRestriction = true, true
			s.VUI.MaxNumReorderFrames, s.VUI.MaxDecFrameBuffering = 1, 3
		}, 1},
		{"vui capped by dpb", func(s *SPS) {
			s.VUIPresent, s.VUI.BitstreamRestriction = true, true
			s.VUI.MaxNumReorderFrames, s.VUI.MaxDecFrameBuffering = 5, 3
		}, 3},
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
	sps := &SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2}
	var st POCState
	var pocs []int32
	for i := 0; i < 20; i++ {
		h := &SliceHeader{NALRefIdc: 1, FrameNum: uint32(i % 16), IDR: i == 0}
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
	sps = &SPS{Log2MaxFrameNum: 4, PicOrderCntType: 0, Log2MaxPicOrderCntLsb: 4}
	st.Reset()
	pocs = nil
	for i := 0; i < 12; i++ {
		h := &SliceHeader{NALRefIdc: 1, PicOrderCntLsb: uint32((2 * i) % 16), IDR: i == 0}
		pocs = append(pocs, st.Next(sps, h))
	}
	for i := 1; i < len(pocs); i++ {
		if pocs[i] != pocs[i-1]+2 {
			t.Fatalf("POC type 0 sequence %v is not monotonic across the lsb wrap", pocs)
		}
	}
}

// TestPOCStateMatchesDPB checks the lightweight POC state against the full
// DPB implementation on synthetic sequences, including an MMCO 5 reset.
func TestPOCStateMatchesDPB(t *testing.T) {
	for _, pocType := range []uint32{0, 1, 2} {
		sps := &SPS{Log2MaxFrameNum: 4, PicOrderCntType: pocType, Log2MaxPicOrderCntLsb: 5, MaxNumRefFrames: 4,
			OffsetForRefFrame: []int32{2}, OffsetForNonRefPic: -1}
		seq := []struct {
			idr, ref bool
			fn, lsb  uint32
			mmco5    bool
		}{
			{idr: true, ref: true},
			{ref: true, fn: 1, lsb: 4},
			{ref: false, fn: 2, lsb: 2},
			{ref: true, fn: 2, lsb: 8},
			{ref: true, fn: 3, lsb: 12, mmco5: true},
			{ref: true, fn: 1, lsb: 2},
			{ref: false, fn: 2, lsb: 1},
			{ref: true, fn: 2, lsb: 4},
		}
		d := NewDPB(nil)
		var st POCState
		for i, p := range seq {
			h := &SliceHeader{IDR: p.idr, FrameNum: p.fn, PicOrderCntLsb: p.lsb, SliceType: SliceP, NumRefIdxL0Active: 1}
			if p.idr {
				h.NALType, h.SliceType = NALSliceIDR, SliceI
			} else {
				h.NALType = NALSlice
			}
			if p.ref {
				h.NALRefIdc = 1
			}
			if p.mmco5 {
				h.AdaptiveRefPicMarking = true
				h.MMCOs = []MMCO{{Op: 5}}
			}
			pic, err := d.Start(sps, h, nil)
			if err != nil {
				t.Fatalf("poc type %d picture %d: %v", pocType, i, err)
			}
			if err := d.Finish(h); err != nil {
				t.Fatal(err)
			}
			if got, want := st.Next(sps, h), int32(pic.POC()); got != want {
				t.Fatalf("poc type %d picture %d: POCState %d, DPB %d", pocType, i, got, want)
			}
		}
	}
}
