package h264_test

import (
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// Test streams: x264 settings chosen to exercise CAVLC and CABAC, POC types
// 0 and 2, B-pyramids, several references, weighted prediction, explicit
// scaling matrices, multiple slices per picture, reference list
// modification and MMCO, and several IDR periods.
var oracleStreams = []struct {
	name string
	w, h int
	n    int
	args []string
}{
	{"baseline-cavlc", 160, 120, 20, []string{"-profile:v", "baseline", "-g", "10", "-x264-params", "scenecut=0"}},
	{"main-noB", 160, 120, 20, []string{"-profile:v", "main", "-bf", "0", "-refs", "3", "-g", "12", "-x264-params", "scenecut=0:weightp=2"}},
	{"high-bpyramid", 320, 240, 40, []string{"-preset", "slow", "-bf", "3", "-b-pyramid", "normal", "-refs", "6", "-g", "20", "-x264-params", "weightp=2:weightb=1:cqm=jvt:slices=3:scenecut=0"}},
	{"high-many-refs", 176, 144, 40, []string{"-preset", "medium", "-bf", "8", "-b-pyramid", "normal", "-refs", "16", "-g", "40", "-x264-params", "weightp=2:scenecut=0"}},
	{"short-gops", 160, 120, 30, []string{"-bf", "2", "-refs", "2", "-x264-params", "keyint=8:min-keyint=8:scenecut=0:open-gop=0"}},
	{"open-gop", 160, 120, 30, []string{"-bf", "2", "-refs", "2", "-x264-params", "keyint=10:min-keyint=10:scenecut=0:open-gop=1"}},
}

// parsedUnit is what our parsers produced for one NAL unit.
type parsedUnit struct {
	kind   string
	values map[string][]int64 // trace field name -> values in syntax order
	bits   int                // header size in bits, slices only
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

type valueMap map[string][]int64

func (m valueMap) add(name string, v int64) { m[name] = append(m[name], v) }

func spsValues(s *h264.SPS) valueMap {
	m := valueMap{}
	m.add("profile_idc", int64(s.ProfileIDC))
	for i := 0; i < 6; i++ {
		m.add(fmt.Sprintf("constraint_set%d_flag", i), b2i(s.ConstraintSet(i)))
	}
	m.add("level_idc", int64(s.LevelIDC))
	m.add("seq_parameter_set_id", int64(s.ID))
	m.add("chroma_format_idc", int64(s.ChromaFormatIDC))
	m.add("separate_colour_plane_flag", b2i(s.SeparateColourPlane))
	m.add("bit_depth_luma_minus8", int64(s.BitDepthLuma-8))
	m.add("bit_depth_chroma_minus8", int64(s.BitDepthChroma-8))
	m.add("qpprime_y_zero_transform_bypass_flag", b2i(s.QpprimeYZeroTransformBypass))
	m.add("seq_scaling_matrix_present_flag", b2i(s.ScalingMatrixPresent))
	m.add("log2_max_frame_num_minus4", int64(s.Log2MaxFrameNum-4))
	m.add("pic_order_cnt_type", int64(s.PicOrderCntType))
	if s.PicOrderCntType == 0 {
		m.add("log2_max_pic_order_cnt_lsb_minus4", int64(s.Log2MaxPicOrderCntLsb-4))
	}
	m.add("delta_pic_order_always_zero_flag", b2i(s.DeltaPicOrderAlwaysZero))
	m.add("offset_for_non_ref_pic", int64(s.OffsetForNonRefPic))
	m.add("offset_for_top_to_bottom_field", int64(s.OffsetForTopToBottomField))
	m.add("num_ref_frames_in_pic_order_cnt_cycle", int64(len(s.OffsetForRefFrame)))
	for i, o := range s.OffsetForRefFrame {
		m.add(fmt.Sprintf("offset_for_ref_frame[%d]", i), int64(o))
	}
	m.add("max_num_ref_frames", int64(s.MaxNumRefFrames))
	m.add("gaps_in_frame_num_allowed_flag", b2i(s.GapsInFrameNumAllowed))
	m.add("pic_width_in_mbs_minus1", int64(s.PicWidthInMbs-1))
	m.add("pic_height_in_map_units_minus1", int64(s.PicHeightInMapUnits-1))
	m.add("frame_mbs_only_flag", b2i(s.FrameMbsOnly))
	m.add("mb_adaptive_frame_field_flag", b2i(s.MbAdaptiveFrameField))
	m.add("direct_8x8_inference_flag", b2i(s.Direct8x8Inference))
	m.add("frame_cropping_flag", b2i(s.FrameCropping))
	m.add("frame_crop_left_offset", int64(s.CropLeft))
	m.add("frame_crop_right_offset", int64(s.CropRight))
	m.add("frame_crop_top_offset", int64(s.CropTop))
	m.add("frame_crop_bottom_offset", int64(s.CropBottom))
	m.add("vui_parameters_present_flag", b2i(s.VUIPresent))
	v := &s.VUI
	m.add("aspect_ratio_info_present_flag", b2i(v.AspectRatioInfoPresent))
	m.add("aspect_ratio_idc", int64(v.AspectRatioIDC))
	m.add("sar_width", int64(v.SarWidth))
	m.add("sar_height", int64(v.SarHeight))
	m.add("overscan_info_present_flag", b2i(v.OverscanInfoPresent))
	m.add("overscan_appropriate_flag", b2i(v.OverscanAppropriate))
	m.add("video_signal_type_present_flag", b2i(v.VideoSignalTypePresent))
	m.add("video_format", int64(v.VideoFormat))
	m.add("video_full_range_flag", b2i(v.VideoFullRange))
	m.add("colour_description_present_flag", b2i(v.ColourDescriptionPresent))
	m.add("colour_primaries", int64(v.ColourPrimaries))
	m.add("transfer_characteristics", int64(v.TransferCharacteristics))
	m.add("matrix_coefficients", int64(v.MatrixCoefficients))
	m.add("chroma_loc_info_present_flag", b2i(v.ChromaLocInfoPresent))
	m.add("chroma_sample_loc_type_top_field", int64(v.ChromaSampleLocTypeTop))
	m.add("chroma_sample_loc_type_bottom_field", int64(v.ChromaSampleLocTypeBottom))
	m.add("timing_info_present_flag", b2i(v.TimingInfoPresent))
	m.add("num_units_in_tick", int64(v.NumUnitsInTick))
	m.add("time_scale", int64(v.TimeScale))
	m.add("fixed_frame_rate_flag", b2i(v.FixedFrameRate))
	m.add("nal_hrd_parameters_present_flag", b2i(v.NalHRDPresent))
	m.add("vcl_hrd_parameters_present_flag", b2i(v.VclHRDPresent))
	m.add("low_delay_hrd_flag", b2i(v.LowDelayHRD))
	m.add("pic_struct_present_flag", b2i(v.PicStructPresent))
	m.add("bitstream_restriction_flag", b2i(v.BitstreamRestriction))
	m.add("motion_vectors_over_pic_boundaries_flag", b2i(v.MotionVectorsOverPicBoundaries))
	m.add("max_bytes_per_pic_denom", int64(v.MaxBytesPerPicDenom))
	m.add("max_bits_per_mb_denom", int64(v.MaxBitsPerMbDenom))
	m.add("log2_max_mv_length_horizontal", int64(v.Log2MaxMvLengthHorizontal))
	m.add("log2_max_mv_length_vertical", int64(v.Log2MaxMvLengthVertical))
	m.add("max_num_reorder_frames", int64(v.MaxNumReorderFrames))
	m.add("max_dec_frame_buffering", int64(v.MaxDecFrameBuffering))
	return m
}

func ppsValues(p *h264.PPS) valueMap {
	m := valueMap{}
	m.add("pic_parameter_set_id", int64(p.ID))
	m.add("seq_parameter_set_id", int64(p.SPSID))
	m.add("entropy_coding_mode_flag", b2i(p.EntropyCodingMode))
	m.add("bottom_field_pic_order_in_frame_present_flag", b2i(p.BottomFieldPicOrderInFramePresent))
	m.add("num_slice_groups_minus1", int64(p.NumSliceGroups-1))
	m.add("num_ref_idx_l0_default_active_minus1", int64(p.NumRefIdxL0DefaultActive-1))
	m.add("num_ref_idx_l1_default_active_minus1", int64(p.NumRefIdxL1DefaultActive-1))
	m.add("weighted_pred_flag", b2i(p.WeightedPred))
	m.add("weighted_bipred_idc", int64(p.WeightedBipredIdc))
	m.add("pic_init_qp_minus26", int64(p.PicInitQpMinus26))
	m.add("pic_init_qs_minus26", int64(p.PicInitQsMinus26))
	m.add("chroma_qp_index_offset", int64(p.ChromaQpIndexOffset))
	m.add("deblocking_filter_control_present_flag", b2i(p.DeblockingFilterControlPresent))
	m.add("constrained_intra_pred_flag", b2i(p.ConstrainedIntraPred))
	m.add("redundant_pic_cnt_present_flag", b2i(p.RedundantPicCntPresent))
	m.add("transform_8x8_mode_flag", b2i(p.Transform8x8Mode))
	m.add("pic_scaling_matrix_present_flag", b2i(p.ScalingMatrixPresent))
	m.add("second_chroma_qp_index_offset", int64(p.SecondChromaQpIndexOffset))
	return m
}

func sliceValues(h *h264.SliceHeader) valueMap {
	m := valueMap{}
	m.add("nal_ref_idc", int64(h.NALRefIdc))
	m.add("nal_unit_type", int64(h.NALType))
	m.add("first_mb_in_slice", int64(h.FirstMbInSlice))
	m.add("slice_type", int64(h.SliceTypeRaw))
	m.add("pic_parameter_set_id", int64(h.PPSID))
	m.add("colour_plane_id", int64(h.ColourPlaneID))
	m.add("frame_num", int64(h.FrameNum))
	m.add("field_pic_flag", b2i(h.FieldPic))
	m.add("bottom_field_flag", b2i(h.BottomField))
	m.add("idr_pic_id", int64(h.IdrPicID))
	m.add("pic_order_cnt_lsb", int64(h.PicOrderCntLsb))
	m.add("delta_pic_order_cnt_bottom", int64(h.DeltaPicOrderCntBottom))
	m.add("delta_pic_order_cnt[0]", int64(h.DeltaPicOrderCnt[0]))
	m.add("delta_pic_order_cnt[1]", int64(h.DeltaPicOrderCnt[1]))
	m.add("redundant_pic_cnt", int64(h.RedundantPicCnt))
	m.add("direct_spatial_mv_pred_flag", b2i(h.DirectSpatialMvPred))
	m.add("num_ref_idx_active_override_flag", b2i(h.NumRefIdxActiveOverride))
	if h.NumRefIdxActiveOverride {
		m.add("num_ref_idx_l0_active_minus1", int64(h.NumRefIdxL0Active-1))
		if h.SliceType == h264.SliceB {
			m.add("num_ref_idx_l1_active_minus1", int64(h.NumRefIdxL1Active-1))
		}
	}
	addMods := func(flagName string, mods []h264.RefPicListModification) {
		m.add(flagName, b2i(mods != nil))
		if mods == nil {
			return
		}
		for _, mod := range mods {
			m.add("modification_of_pic_nums_idc", int64(mod.Idc))
			if mod.Idc == 2 {
				m.add("long_term_pic_num", int64(mod.Value))
			} else {
				m.add("abs_diff_pic_num_minus1", int64(mod.Value))
			}
		}
		m.add("modification_of_pic_nums_idc", 3)
	}
	if !h.SliceType.IsIntra() {
		addMods("ref_pic_list_modification_flag_l0", h.RefPicListModificationL0)
	}
	if h.SliceType == h264.SliceB {
		addMods("ref_pic_list_modification_flag_l1", h.RefPicListModificationL1)
	}
	if w := h.PredWeights; w != nil {
		m.add("luma_log2_weight_denom", int64(w.LumaLog2WeightDenom))
		m.add("chroma_log2_weight_denom", int64(w.ChromaLog2WeightDenom))
		addList := func(l string, list []h264.WeightEntry) {
			for i, e := range list {
				m.add(fmt.Sprintf("luma_weight_%s_flag[%d]", l, i), b2i(e.LumaWeightFlag))
				if e.LumaWeightFlag {
					m.add(fmt.Sprintf("luma_weight_%s[%d]", l, i), int64(e.LumaWeight))
					m.add(fmt.Sprintf("luma_offset_%s[%d]", l, i), int64(e.LumaOffset))
				}
				m.add(fmt.Sprintf("chroma_weight_%s_flag[%d]", l, i), b2i(e.ChromaWeightFlag))
				if e.ChromaWeightFlag {
					for j := 0; j < 2; j++ {
						m.add(fmt.Sprintf("chroma_weight_%s[%d][%d]", l, i, j), int64(e.ChromaWeight[j]))
						m.add(fmt.Sprintf("chroma_offset_%s[%d][%d]", l, i, j), int64(e.ChromaOffset[j]))
					}
				}
			}
		}
		addList("l0", w.L0[:h.NumRefIdxL0Active])
		if h.SliceType == h264.SliceB {
			addList("l1", w.L1[:h.NumRefIdxL1Active])
		}
	}
	if h.NALRefIdc != 0 {
		if h.IDR {
			m.add("no_output_of_prior_pics_flag", b2i(h.NoOutputOfPriorPics))
			m.add("long_term_reference_flag", b2i(h.LongTermReference))
		} else {
			m.add("adaptive_ref_pic_marking_mode_flag", b2i(h.AdaptiveRefPicMarking))
			if h.AdaptiveRefPicMarking {
				for _, op := range h.MMCOs {
					m.add("memory_management_control_operation", int64(op.Op))
					if op.Op == 1 || op.Op == 3 {
						m.add("difference_of_pic_nums_minus1", int64(op.DifferenceOfPicNumsMinus1))
					}
					if op.Op == 2 {
						m.add("long_term_pic_num", int64(op.LongTermPicNum))
					}
					if op.Op == 3 || op.Op == 6 {
						m.add("long_term_frame_idx", int64(op.LongTermFrameIdx))
					}
					if op.Op == 4 {
						m.add("max_long_term_frame_idx_plus1", int64(op.MaxLongTermFrameIdxPlus1))
					}
				}
				m.add("memory_management_control_operation", 0)
			}
		}
	}
	m.add("cabac_init_idc", int64(h.CabacInitIdc))
	m.add("slice_qp_delta", int64(h.SliceQpDelta))
	m.add("sp_for_switch_flag", b2i(h.SpForSwitch))
	m.add("slice_qs_delta", int64(h.SliceQsDelta))
	m.add("disable_deblocking_filter_idc", int64(h.DisableDeblockingFilterIdc))
	m.add("slice_alpha_c0_offset_div2", int64(h.SliceAlphaC0OffsetDiv2))
	m.add("slice_beta_offset_div2", int64(h.SliceBetaOffsetDiv2))
	m.add("slice_group_change_cycle", int64(h.SliceGroupChangeCycle))
	return m
}

// parseStream runs our parsers over every SPS, PPS and slice NAL unit.
func parseStream(t *testing.T, data []byte) []parsedUnit {
	t.Helper()
	ps := h264.NewParameterSets()
	var out []parsedUnit
	for i, nal := range annexb.Split(data) {
		_, typ, ok := h264.NALHeader(nal)
		if !ok {
			t.Fatalf("nal %d: bad header", i)
		}
		switch typ {
		case h264.NALSPS:
			s, err := ps.AddSPS(nal)
			if err != nil {
				t.Fatalf("nal %d: SPS: %v", i, err)
			}
			out = append(out, parsedUnit{kind: "Sequence Parameter Set", values: spsValues(s)})
		case h264.NALPPS:
			if err := ps.AddPPS(nal); err != nil {
				t.Fatalf("nal %d: PPS: %v", i, err)
			}
			_, p, err := ps.Lookup(ppsID(t, nal))
			if err != nil {
				t.Fatalf("nal %d: PPS lookup: %v", i, err)
			}
			out = append(out, parsedUnit{kind: "Picture Parameter Set", values: ppsValues(p)})
		case h264.NALSlice, h264.NALSliceIDR:
			h, _, _, err := h264.ParseSliceHeader(nal, ps)
			if err != nil {
				t.Fatalf("nal %d: slice header: %v", i, err)
			}
			out = append(out, parsedUnit{kind: "Slice Header", values: sliceValues(h), bits: h.HeaderBits})
		}
	}
	return out
}

func ppsID(t *testing.T, nal []byte) uint32 {
	t.Helper()
	p, err := h264.ParsePPS(nal, func(uint32) *h264.SPS { return &h264.SPS{ChromaFormatIDC: 1} })
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// TestHeadersMatchFFmpegTrace compares every syntax element our parsers
// expose against ffmpeg's trace_headers output for a range of x264 streams.
func TestHeadersMatchFFmpegTrace(t *testing.T) {
	for _, s := range oracleStreams {
		t.Run(s.name, func(t *testing.T) {
			path := testutil.GenerateH264(t, s.w, s.h, s.n, s.args...)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var trace []testutil.TraceUnit
			for _, u := range testutil.TraceHeaders(t, path, "h264") {
				switch u.Kind {
				case "Sequence Parameter Set", "Picture Parameter Set", "Slice Header":
					trace = append(trace, u)
				}
			}
			got := parseStream(t, data)
			if len(got) != len(trace) {
				t.Fatalf("parsed %d units, ffmpeg traced %d", len(got), len(trace))
			}
			compared := map[string]int{}
			skipped := map[string]int{}
			slices := 0
			for i := range trace {
				tu, gu := trace[i], got[i]
				if tu.Kind != gu.kind {
					t.Fatalf("unit %d: kind %q vs ours %q", i, tu.Kind, gu.kind)
				}
				next := map[string]int{}
				headerEnd := 0
				for _, f := range tu.Fields {
					if f.Name == "cabac_alignment_one_bit" {
						continue
					}
					if end := f.Pos + f.Bits; end > headerEnd {
						headerEnd = end
					}
					vals, ok := gu.values[f.Name]
					if !ok {
						skipped[f.Name]++
						continue
					}
					k := next[f.Name]
					if k >= len(vals) {
						t.Errorf("unit %d (%s): ffmpeg has more %s values than we parsed (%d)", i, tu.Kind, f.Name, len(vals))
						continue
					}
					next[f.Name]++
					compared[f.Name]++
					if vals[k] != f.Value {
						t.Errorf("unit %d (%s): %s[%d] = %d, ffmpeg says %d", i, tu.Kind, f.Name, k, vals[k], f.Value)
					}
				}
				for name, vals := range gu.values {
					if n := next[name]; n > 0 && n != len(vals) {
						t.Errorf("unit %d (%s): we parsed %d %s values, ffmpeg printed %d", i, tu.Kind, len(vals), name, n)
					}
				}
				if gu.kind == "Slice Header" {
					slices++
					if gu.bits != headerEnd {
						t.Errorf("unit %d: slice header ends at bit %d, ffmpeg says %d", i, gu.bits, headerEnd)
					}
				}
			}
			if slices == 0 {
				t.Fatal("no slices compared")
			}
			var names []string
			for n := range skipped {
				names = append(names, n)
			}
			sort.Strings(names)
			t.Logf("%d slices; compared %d distinct elements; not modelled: %v", slices, len(compared), names)
		})
	}
}

// TestScalingListsJVT checks that x264's "jvt" matrices (the H.264 default
// tables) are parsed and resolved into raster order.
func TestScalingListsJVT(t *testing.T) {
	path := testutil.GenerateH264(t, 160, 120, 2, "-x264-params", "cqm=jvt:8x8dct=1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ps := h264.NewParameterSets()
	var pps *h264.PPS
	for _, nal := range annexb.Split(data) {
		_, typ, _ := h264.NALHeader(nal)
		switch typ {
		case h264.NALSPS:
			if _, err := ps.AddSPS(nal); err != nil {
				t.Fatal(err)
			}
		case h264.NALPPS:
			if err := ps.AddPPS(nal); err != nil {
				t.Fatal(err)
			}
			if _, pps, err = ps.Lookup(ppsID(t, nal)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if pps == nil {
		t.Fatal("no PPS")
	}
	// Default_4x4_Intra and Default_8x8_Intra in raster order (Tables 7-3
	// and 7-4 are listed in zigzag order).
	want4Intra := [16]uint8{6, 13, 20, 28, 13, 20, 28, 32, 20, 28, 32, 37, 28, 32, 37, 42}
	want4Inter := [16]uint8{10, 14, 20, 24, 14, 20, 24, 27, 20, 24, 27, 30, 24, 27, 30, 34}
	want8Intra := [64]uint8{
		6, 10, 13, 16, 18, 23, 25, 27,
		10, 11, 16, 18, 23, 25, 27, 29,
		13, 16, 18, 23, 25, 27, 29, 31,
		16, 18, 23, 25, 27, 29, 31, 33,
		18, 23, 25, 27, 29, 31, 33, 36,
		23, 25, 27, 29, 31, 33, 36, 38,
		25, 27, 29, 31, 33, 36, 38, 40,
		27, 29, 31, 33, 36, 38, 40, 42,
	}
	want8Inter := [64]uint8{
		9, 13, 15, 17, 19, 21, 22, 24,
		13, 13, 17, 19, 21, 22, 24, 25,
		15, 17, 19, 21, 22, 24, 25, 27,
		17, 19, 21, 22, 24, 25, 27, 28,
		19, 21, 22, 24, 25, 27, 28, 30,
		21, 22, 24, 25, 27, 28, 30, 32,
		22, 24, 25, 27, 28, 30, 32, 33,
		24, 25, 27, 28, 30, 32, 33, 35,
	}
	for i := 0; i < 3; i++ {
		if pps.ScalingLists.L4[i] != want4Intra {
			t.Errorf("L4[%d] = %v, want Default_4x4_Intra", i, pps.ScalingLists.L4[i])
		}
		if pps.ScalingLists.L4[i+3] != want4Inter {
			t.Errorf("L4[%d] = %v, want Default_4x4_Inter", i+3, pps.ScalingLists.L4[i+3])
		}
	}
	if pps.ScalingLists.L8[0] != want8Intra {
		t.Errorf("L8[0] = %v, want Default_8x8_Intra", pps.ScalingLists.L8[0])
	}
	if pps.ScalingLists.L8[1] != want8Inter {
		t.Errorf("L8[1] = %v, want Default_8x8_Inter", pps.ScalingLists.L8[1])
	}
}

func TestFlatScalingListsByDefault(t *testing.T) {
	path := testutil.GenerateH264(t, 160, 120, 2)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ps := h264.NewParameterSets()
	for _, nal := range annexb.Split(data) {
		_, typ, _ := h264.NALHeader(nal)
		switch typ {
		case h264.NALSPS:
			if _, err := ps.AddSPS(nal); err != nil {
				t.Fatal(err)
			}
		case h264.NALPPS:
			if err := ps.AddPPS(nal); err != nil {
				t.Fatal(err)
			}
			_, pps, err := ps.Lookup(ppsID(t, nal))
			if err != nil {
				t.Fatal(err)
			}
			for i := range pps.ScalingLists.L4 {
				for _, v := range pps.ScalingLists.L4[i] {
					if v != 16 {
						t.Fatalf("L4[%d] not flat: %v", i, pps.ScalingLists.L4[i])
					}
				}
			}
			for i := range pps.ScalingLists.L8 {
				for _, v := range pps.ScalingLists.L8[i] {
					if v != 16 {
						t.Fatalf("L8[%d] not flat", i)
					}
				}
			}
		}
	}
}

func TestParameterSetOrder(t *testing.T) {
	path := testutil.GenerateH264(t, 160, 120, 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sps, pps []byte
	for _, nal := range annexb.Split(data) {
		_, typ, _ := h264.NALHeader(nal)
		switch typ {
		case h264.NALSPS:
			sps = nal
		case h264.NALPPS:
			pps = nal
		}
	}
	ps := h264.NewParameterSets()
	if err := ps.AddPPS(pps); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ps.Lookup(0); err != h264.ErrMissingSPS {
		t.Fatalf("Lookup before SPS = %v, want ErrMissingSPS", err)
	}
	if _, err := ps.AddSPS(sps); err != nil {
		t.Fatal(err)
	}
	s, p, err := ps.Lookup(0)
	if err != nil || s == nil || p == nil {
		t.Fatalf("Lookup after SPS = %v, %v, %v", s, p, err)
	}
	if _, _, err := ps.Lookup(7); err != h264.ErrMissingPPS {
		t.Fatalf("Lookup unknown PPS = %v, want ErrMissingPPS", err)
	}
}
