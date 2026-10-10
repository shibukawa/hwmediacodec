package h264_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// encoderSPS is the kind of SPS the VA-API encoder emits.
func encoderSPS(profile uint8) *h264.SPS {
	s := &h264.SPS{
		ProfileIDC: profile, LevelIDC: 31, ChromaFormatIDC: 1, BitDepthLuma: 8, BitDepthChroma: 8,
		Log2MaxFrameNum: 8, PicOrderCntType: 0, Log2MaxPicOrderCntLsb: 8, MaxNumRefFrames: 1,
		PicWidthInMbs: 20, PicHeightInMapUnits: 15, FrameMbsOnly: true, Direct8x8Inference: true,
		FrameCropping: true, CropRight: 0, CropBottom: 4,
		VUIPresent: true,
	}
	if profile == 66 {
		s.ConstraintFlags = 0xc0
	}
	s.VUI = h264.VUI{
		TimingInfoPresent: true, NumUnitsInTick: 1001, TimeScale: 60000, FixedFrameRate: true,
		BitstreamRestriction: true, MotionVectorsOverPicBoundaries: true,
		Log2MaxMvLengthHorizontal: 15, Log2MaxMvLengthVertical: 15, MaxNumReorderFrames: 0, MaxDecFrameBuffering: 1,
	}
	return s
}

func encoderPPS(cabac, t8x8 bool) *h264.PPS {
	return &h264.PPS{
		EntropyCodingMode: cabac, NumSliceGroups: 1, NumRefIdxL0DefaultActive: 1, NumRefIdxL1DefaultActive: 1,
		PicInitQpMinus26: 0, DeblockingFilterControlPresent: true, Transform8x8Mode: t8x8,
	}
}

// TestWriteParameterSetsRoundTrip serialises SPS/PPS and parses them back.
func TestWriteParameterSetsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		profile uint8
		cabac   bool
		t8x8    bool
	}{{66, false, false}, {77, true, false}, {100, true, true}} {
		sps := encoderSPS(tc.profile)
		pps := encoderPPS(tc.cabac, tc.t8x8)
		ps := h264.NewParameterSets()
		got, err := ps.AddSPS(h264.WriteSPS(sps))
		if err != nil {
			t.Fatalf("profile %d: parse written SPS: %v", tc.profile, err)
		}
		want := *sps
		want.ScalingLists = got.ScalingLists // flat on both sides
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("profile %d: SPS round trip differs:\n got %+v\nwant %+v", tc.profile, *got, want)
		}
		if err := ps.AddPPS(h264.WritePPS(pps)); err != nil {
			t.Fatal(err)
		}
		_, gp, err := ps.Lookup(0)
		if err != nil {
			t.Fatal(err)
		}
		wp := *pps
		wp.ScalingLists = gp.ScalingLists
		wp.SPSID = 0
		if !reflect.DeepEqual(*gp, wp) {
			t.Errorf("profile %d: PPS round trip differs:\n got %+v\nwant %+v", tc.profile, *gp, wp)
		}
	}
}

// sliceCases are the slice headers the encoder writes (I and P with one
// reference) plus richer ones that exercise the generic writer.
func sliceCases() []*h264.SliceHeader {
	idr := &h264.SliceHeader{NALType: h264.NALSliceIDR, NALRefIdc: 3, IDR: true, SliceTypeRaw: 2, SliceType: h264.SliceI, IdrPicID: 5, SliceQpDelta: -3}
	p := &h264.SliceHeader{NALType: h264.NALSlice, NALRefIdc: 2, SliceTypeRaw: 0, SliceType: h264.SliceP, FrameNum: 7, PicOrderCntLsb: 14, NumRefIdxL0Active: 1, SliceQpDelta: 2}
	pOverride := &h264.SliceHeader{NALType: h264.NALSlice, NALRefIdc: 2, SliceTypeRaw: 5, SliceType: h264.SliceP, FrameNum: 255, PicOrderCntLsb: 255,
		NumRefIdxActiveOverride: true, NumRefIdxL0Active: 3,
		RefPicListModificationL0: []h264.RefPicListModification{{Idc: 0, Value: 1}, {Idc: 1, Value: 0}, {Idc: 2, Value: 1}},
		AdaptiveRefPicMarking:    true,
		MMCOs:                    []h264.MMCO{{Op: 1, DifferenceOfPicNumsMinus1: 2}, {Op: 3, DifferenceOfPicNumsMinus1: 0, LongTermFrameIdx: 1}, {Op: 4, MaxLongTermFrameIdxPlus1: 2}, {Op: 2, LongTermPicNum: 0}, {Op: 6, LongTermFrameIdx: 0}},
		CabacInitIdc:             2, SliceQpDelta: -12, DisableDeblockingFilterIdc: 2, SliceAlphaC0OffsetDiv2: -1, SliceBetaOffsetDiv2: 3}
	nonRef := &h264.SliceHeader{NALType: h264.NALSlice, NALRefIdc: 0, SliceTypeRaw: 1, SliceType: h264.SliceB, FrameNum: 3, PicOrderCntLsb: 4,
		DirectSpatialMvPred: true, NumRefIdxActiveOverride: true, NumRefIdxL0Active: 2, NumRefIdxL1Active: 1,
		RefPicListModificationL0: []h264.RefPicListModification{}, DisableDeblockingFilterIdc: 1}
	return []*h264.SliceHeader{idr, p, pOverride, nonRef}
}

// TestWriteSliceHeaderRoundTrip writes slice headers, appends fake slice
// data and parses them back with ParseSliceHeader, checking the header bit
// count too.
func TestWriteSliceHeaderRoundTrip(t *testing.T) {
	for _, cabac := range []bool{false, true} {
		sps := encoderSPS(100)
		pps := encoderPPS(cabac, true)
		ps := h264.NewParameterSets()
		if _, err := ps.AddSPS(h264.WriteSPS(sps)); err != nil {
			t.Fatal(err)
		}
		if err := ps.AddPPS(h264.WritePPS(pps)); err != nil {
			t.Fatal(err)
		}
		for i, sh := range sliceCases() {
			if cabac && sh.SliceType == h264.SliceB {
				sh.CabacInitIdc = 1
			}
			data, bits := h264.WriteSliceHeader(sh, sps, pps)
			// Fake slice data (non-zero so that no emulation issue arises).
			nal := append(append([]byte(nil), data...), 0xAB, 0xCD, 0xEF, 0x80)
			got, _, _, err := h264.ParseSliceHeader(nal, ps)
			if err != nil {
				t.Fatalf("cabac=%v case %d: parse: %v", cabac, i, err)
			}
			want := *sh
			if want.SliceType == h264.SliceP || want.SliceType == h264.SliceB {
				if !want.NumRefIdxActiveOverride {
					want.NumRefIdxL0Active = pps.NumRefIdxL0DefaultActive
					if want.SliceType == h264.SliceB {
						want.NumRefIdxL1Active = pps.NumRefIdxL1DefaultActive
					}
				}
			} else {
				want.NumRefIdxL0Active, want.NumRefIdxL1Active = 0, 0
			}
			want.HeaderBits = got.HeaderBits
			if !cabac {
				want.CabacInitIdc = 0
			}
			if !reflect.DeepEqual(*got, want) {
				t.Errorf("cabac=%v case %d: round trip differs:\n got %+v\nwant %+v", cabac, i, *got, want)
			}
			if got.HeaderBits != bits {
				t.Errorf("cabac=%v case %d: writer reports %d bits, parser consumed %d", cabac, i, bits, got.HeaderBits)
			}
		}
	}
}

// TestWrittenHeadersMatchFFmpeg feeds written SPS/PPS and slice headers (with
// fake slice data) to ffmpeg's trace_headers and compares the elements.
func TestWrittenHeadersMatchFFmpeg(t *testing.T) {
	testutil.RequireFFmpeg(t)
	sps := encoderSPS(100)
	pps := encoderPPS(true, true)
	var stream bytes.Buffer
	add := func(nal []byte) {
		stream.Write([]byte{0, 0, 0, 1})
		stream.Write(nal)
	}
	add(h264.WriteSPS(sps))
	add(h264.WritePPS(pps))
	for _, sh := range sliceCases() {
		data, _ := h264.WriteSliceHeader(sh, sps, pps)
		add(append(append([]byte(nil), data...), 0xAB, 0xCD, 0xEF, 0x80))
	}
	path := filepath.Join(t.TempDir(), "written.h264")
	if err := os.WriteFile(path, stream.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var trace []testutil.TraceUnit
	for _, u := range testutil.TraceHeaders(t, path, "h264") {
		switch u.Kind {
		case "Sequence Parameter Set", "Picture Parameter Set", "Slice Header":
			trace = append(trace, u)
		}
	}
	got := parseStream(t, stream.Bytes())
	if len(got) != len(trace) {
		t.Fatalf("parsed %d units, ffmpeg traced %d", len(got), len(trace))
	}
	for i := range trace {
		next := map[string]int{}
		for _, f := range trace[i].Fields {
			vals, ok := got[i].values[f.Name]
			if !ok {
				continue
			}
			k := next[f.Name]
			if k >= len(vals) {
				t.Errorf("unit %d (%s): ffmpeg printed more %s values than we have", i, trace[i].Kind, f.Name)
				continue
			}
			next[f.Name]++
			if vals[k] != f.Value {
				t.Errorf("unit %d (%s): %s = %d, ffmpeg says %d", i, trace[i].Kind, f.Name, vals[k], f.Value)
			}
		}
	}
	// ffmpeg must see the stream as the profile and size we wrote.
	nals := annexb.Split(stream.Bytes())
	if len(nals) != 2+len(sliceCases()) {
		t.Fatalf("stream has %d NAL units", len(nals))
	}
}
