package hevc_test

import (
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// Test streams: x265 settings chosen to exercise B-pyramids, several
// references, weighted prediction, default scaling lists, multiple slices,
// wavefront entry points, open GOPs (CRA and RASL pictures), HRD
// parameters, temporal sub-layers, a conformance window, 10-bit video and
// field coding.
var oracleStreams = []struct {
	name   string
	w, h   int
	n      int
	params string
	extra  []string
}{
	{"default", 320, 240, 30, "keyint=12:min-keyint=12", nil},
	{"bpyramid-weights", 320, 240, 40, "bframes=4:b-pyramid=1:ref=5:keyint=20:min-keyint=20:weightp=1:weightb=1:slices=3:scaling-list=default:hrd=1:vbv-bufsize=1000:vbv-maxrate=1000", []string{"-preset", "slow"}},
	{"lowdelay-refs", 176, 144, 30, "bframes=0:ref=6:keyint=30:weightp=1:no-wpp=1:rect=1:amp=1:tskip=1:signhide=0:cbqpoffs=2:crqpoffs=-3:deblock=2,-1", []string{"-preset", "slower"}},
	{"open-gop", 320, 240, 40, "bframes=3:keyint=10:min-keyint=10:open-gop=1:no-sao=1:constrained-intra=1:strong-intra-smoothing=0", nil},
	{"temporal-layers", 320, 240, 30, "bframes=3:b-pyramid=1:temporal-layers=3:keyint=15:min-keyint=15", nil},
	{"cropped", 322, 242, 12, "bframes=2:keyint=6:min-keyint=6:no-deblock=1:cu-lossless=1", nil},
	{"main10", 160, 120, 12, "bframes=2:keyint=6:min-keyint=6", []string{"-pix_fmt", "yuv420p10le"}},
	{"interlaced", 320, 240, 20, "bframes=2:keyint=8:min-keyint=8:interlace=tff", nil},
	// A fade makes x265 code prediction weights for luma and chroma.
	{"fade-weights", 320, 240, 48, "bframes=3:ref=3:weightp=1:weightb=1:keyint=48", []string{"-vf", "fade=t=in:st=0:d=0.8,fade=t=out:st=1.0:d=0.6"}},
}

// parsedUnit is what our parsers produced for one NAL unit.
type parsedUnit struct {
	kind   string
	values valueMap // trace field name -> values in syntax order
	bits   int      // header size in bits, slices only
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

type valueMap map[string][]int64

func (m valueMap) add(name string, v int64) { m[name] = append(m[name], v) }

func (m valueMap) ptl(p *hevc.ProfileTierLevel) {
	m.add("general_profile_space", int64(p.ProfileSpace))
	m.add("general_tier_flag", b2i(p.Tier))
	m.add("general_profile_idc", int64(p.ProfileIDC))
	for j := 0; j < 32; j++ {
		m.add(fmt.Sprintf("general_profile_compatibility_flag[%d]", j), b2i(p.Compatible(j)))
	}
	m.add("general_progressive_source_flag", b2i(p.ProgressiveSource))
	m.add("general_interlaced_source_flag", b2i(p.InterlacedSource))
	m.add("general_non_packed_constraint_flag", b2i(p.NonPackedConstraint))
	m.add("general_frame_only_constraint_flag", b2i(p.FrameOnlyConstraint))
	m.add("general_inbld_flag", int64(p.ConstraintBits&1))
	m.add("general_level_idc", int64(p.LevelIDC))
}

// rps adds the syntax elements of a short-term reference picture set.
func (m valueMap) rps(s *hevc.ShortTermRPS, idx int) {
	if idx != 0 {
		m.add("inter_ref_pic_set_prediction_flag", b2i(s.InterRPSPrediction))
	}
	if s.InterRPSPrediction {
		m.add("delta_idx_minus1", int64(s.DeltaIdxMinus1))
		m.add("delta_rps_sign", b2i(s.DeltaRPSSign))
		m.add("abs_delta_rps_minus1", int64(s.AbsDeltaRPSMinus1))
		for j := range s.UsedByCurrPicFlag {
			m.add(fmt.Sprintf("used_by_curr_pic_flag[%d]", j), b2i(s.UsedByCurrPicFlag[j]))
			if !s.UsedByCurrPicFlag[j] {
				m.add(fmt.Sprintf("use_delta_flag[%d]", j), b2i(s.UseDeltaFlag[j]))
			}
		}
		return
	}
	m.add("num_negative_pics", int64(s.NumNegativePics))
	m.add("num_positive_pics", int64(s.NumPositivePics))
	prev := int32(0)
	for i := 0; i < s.NumNegativePics; i++ {
		m.add(fmt.Sprintf("delta_poc_s0_minus1[%d]", i), int64(prev-s.DeltaPocS0[i]-1))
		m.add(fmt.Sprintf("used_by_curr_pic_s0_flag[%d]", i), b2i(s.UsedByCurrPicS0[i]))
		prev = s.DeltaPocS0[i]
	}
	prev = 0
	for i := 0; i < s.NumPositivePics; i++ {
		m.add(fmt.Sprintf("delta_poc_s1_minus1[%d]", i), int64(s.DeltaPocS1[i]-prev-1))
		m.add(fmt.Sprintf("used_by_curr_pic_s1_flag[%d]", i), b2i(s.UsedByCurrPicS1[i]))
		prev = s.DeltaPocS1[i]
	}
}

func spsValues(s *hevc.SPS) valueMap {
	m := valueMap{}
	m.add("sps_video_parameter_set_id", int64(s.VPSID))
	m.add("sps_max_sub_layers_minus1", int64(s.MaxSubLayersMinus1))
	m.add("sps_temporal_id_nesting_flag", b2i(s.TemporalIDNesting))
	m.ptl(&s.PTL)
	m.add("sps_seq_parameter_set_id", int64(s.ID))
	m.add("chroma_format_idc", int64(s.ChromaFormatIDC))
	m.add("separate_colour_plane_flag", b2i(s.SeparateColourPlane))
	m.add("pic_width_in_luma_samples", int64(s.Width))
	m.add("pic_height_in_luma_samples", int64(s.Height))
	m.add("conformance_window_flag", b2i(s.ConformanceWindow))
	m.add("conf_win_left_offset", int64(s.ConfWinLeft))
	m.add("conf_win_right_offset", int64(s.ConfWinRight))
	m.add("conf_win_top_offset", int64(s.ConfWinTop))
	m.add("conf_win_bottom_offset", int64(s.ConfWinBottom))
	m.add("bit_depth_luma_minus8", int64(s.BitDepthLuma-8))
	m.add("bit_depth_chroma_minus8", int64(s.BitDepthChroma-8))
	m.add("log2_max_pic_order_cnt_lsb_minus4", int64(s.Log2MaxPOCLsb-4))
	m.add("sps_sub_layer_ordering_info_present_flag", b2i(s.SubLayerOrderingInfoPresent))
	first := s.MaxSubLayersMinus1
	if s.SubLayerOrderingInfoPresent {
		first = 0
	}
	for i := first; i <= s.MaxSubLayersMinus1; i++ {
		m.add(fmt.Sprintf("sps_max_dec_pic_buffering_minus1[%d]", i), int64(s.MaxDecPicBufferingMinus1[i]))
		m.add(fmt.Sprintf("sps_max_num_reorder_pics[%d]", i), int64(s.MaxNumReorderPics[i]))
		m.add(fmt.Sprintf("sps_max_latency_increase_plus1[%d]", i), int64(s.MaxLatencyIncreasePlus1[i]))
	}
	m.add("log2_min_luma_coding_block_size_minus3", int64(s.Log2MinLumaCodingBlockSizeMinus3))
	m.add("log2_diff_max_min_luma_coding_block_size", int64(s.Log2DiffMaxMinLumaCodingBlockSize))
	m.add("log2_min_luma_transform_block_size_minus2", int64(s.Log2MinLumaTransformBlockSizeMinus2))
	m.add("log2_diff_max_min_luma_transform_block_size", int64(s.Log2DiffMaxMinLumaTransformBlockSize))
	m.add("max_transform_hierarchy_depth_inter", int64(s.MaxTransformHierarchyDepthInter))
	m.add("max_transform_hierarchy_depth_intra", int64(s.MaxTransformHierarchyDepthIntra))
	m.add("scaling_list_enabled_flag", b2i(s.ScalingListEnabled))
	m.add("sps_scaling_list_data_present_flag", b2i(s.ScalingListDataPresent))
	m.add("amp_enabled_flag", b2i(s.AMPEnabled))
	m.add("sample_adaptive_offset_enabled_flag", b2i(s.SAOEnabled))
	m.add("pcm_enabled_flag", b2i(s.PCMEnabled))
	m.add("num_short_term_ref_pic_sets", int64(len(s.ShortTermRPS)))
	for i := range s.ShortTermRPS {
		m.rps(&s.ShortTermRPS[i], i)
	}
	m.add("long_term_ref_pics_present_flag", b2i(s.LongTermRefPicsPresent))
	if s.LongTermRefPicsPresent {
		m.add("num_long_term_ref_pics_sps", int64(len(s.LtRefPicPOCLsbSPS)))
	}
	m.add("sps_temporal_mvp_enabled_flag", b2i(s.TemporalMVPEnabled))
	m.add("strong_intra_smoothing_enabled_flag", b2i(s.StrongIntraSmoothingEnabled))
	m.add("vui_parameters_present_flag", b2i(s.VUIPresent))
	if s.VUIPresent {
		v := &s.VUI
		m.add("aspect_ratio_info_present_flag", b2i(v.AspectRatioInfoPresent))
		if v.AspectRatioInfoPresent {
			m.add("aspect_ratio_idc", int64(v.AspectRatioIDC))
		}
		m.add("overscan_info_present_flag", b2i(v.OverscanInfoPresent))
		m.add("video_signal_type_present_flag", b2i(v.VideoSignalTypePresent))
		if v.VideoSignalTypePresent {
			m.add("video_format", int64(v.VideoFormat))
			m.add("video_full_range_flag", b2i(v.VideoFullRange))
			m.add("colour_description_present_flag", b2i(v.ColourDescriptionPresent))
		}
		m.add("chroma_loc_info_present_flag", b2i(v.ChromaLocInfoPresent))
		m.add("neutral_chroma_indication_flag", b2i(v.NeutralChromaIndication))
		m.add("field_seq_flag", b2i(v.FieldSeq))
		m.add("frame_field_info_present_flag", b2i(v.FrameFieldInfoPresent))
		m.add("default_display_window_flag", b2i(v.DefaultDisplayWindow))
		m.add("vui_timing_info_present_flag", b2i(v.TimingInfoPresent))
		if v.TimingInfoPresent {
			m.add("vui_num_units_in_tick", int64(v.NumUnitsInTick))
			m.add("vui_time_scale", int64(v.TimeScale))
			m.add("vui_poc_proportional_to_timing_flag", b2i(v.POCProportionalToTiming))
			m.add("vui_hrd_parameters_present_flag", b2i(v.HRDParametersPresent))
		}
		m.add("bitstream_restriction_flag", b2i(v.BitstreamRestriction))
		if v.BitstreamRestriction {
			m.add("tiles_fixed_structure_flag", b2i(v.TilesFixedStructure))
			m.add("motion_vectors_over_pic_boundaries_flag", b2i(v.MotionVectorsOverPicBoundaries))
			m.add("restricted_ref_pic_lists_flag", b2i(v.RestrictedRefPicLists))
			m.add("min_spatial_segmentation_idc", int64(v.MinSpatialSegmentationIDC))
			m.add("max_bytes_per_pic_denom", int64(v.MaxBytesPerPicDenom))
			m.add("max_bits_per_min_cu_denom", int64(v.MaxBitsPerMinCuDenom))
			m.add("log2_max_mv_length_horizontal", int64(v.Log2MaxMvLengthHorizontal))
			m.add("log2_max_mv_length_vertical", int64(v.Log2MaxMvLengthVertical))
		}
	}
	m.add("sps_extension_present_flag", b2i(s.ExtensionPresent))
	return m
}

func ppsValues(p *hevc.PPS) valueMap {
	m := valueMap{}
	m.add("pps_pic_parameter_set_id", int64(p.ID))
	m.add("pps_seq_parameter_set_id", int64(p.SPSID))
	m.add("dependent_slice_segments_enabled_flag", b2i(p.DependentSliceSegmentsEnabled))
	m.add("output_flag_present_flag", b2i(p.OutputFlagPresent))
	m.add("num_extra_slice_header_bits", int64(p.NumExtraSliceHeaderBits))
	m.add("sign_data_hiding_enabled_flag", b2i(p.SignDataHidingEnabled))
	m.add("cabac_init_present_flag", b2i(p.CabacInitPresent))
	m.add("num_ref_idx_l0_default_active_minus1", int64(p.NumRefIdxL0DefaultActive-1))
	m.add("num_ref_idx_l1_default_active_minus1", int64(p.NumRefIdxL1DefaultActive-1))
	m.add("init_qp_minus26", int64(p.InitQpMinus26))
	m.add("constrained_intra_pred_flag", b2i(p.ConstrainedIntraPred))
	m.add("transform_skip_enabled_flag", b2i(p.TransformSkipEnabled))
	m.add("cu_qp_delta_enabled_flag", b2i(p.CuQpDeltaEnabled))
	if p.CuQpDeltaEnabled {
		m.add("diff_cu_qp_delta_depth", int64(p.DiffCuQpDeltaDepth))
	}
	m.add("pps_cb_qp_offset", int64(p.CbQpOffset))
	m.add("pps_cr_qp_offset", int64(p.CrQpOffset))
	m.add("pps_slice_chroma_qp_offsets_present_flag", b2i(p.SliceChromaQpOffsetsPresent))
	m.add("weighted_pred_flag", b2i(p.WeightedPred))
	m.add("weighted_bipred_flag", b2i(p.WeightedBipred))
	m.add("transquant_bypass_enabled_flag", b2i(p.TransquantBypassEnabled))
	m.add("tiles_enabled_flag", b2i(p.TilesEnabled))
	m.add("entropy_coding_sync_enabled_flag", b2i(p.EntropyCodingSyncEnabled))
	if p.TilesEnabled {
		m.add("num_tile_columns_minus1", int64(p.NumTileColumns-1))
		m.add("num_tile_rows_minus1", int64(p.NumTileRows-1))
		m.add("uniform_spacing_flag", b2i(p.UniformSpacing))
		m.add("loop_filter_across_tiles_enabled_flag", b2i(p.LoopFilterAcrossTilesEnabled))
	}
	m.add("pps_loop_filter_across_slices_enabled_flag", b2i(p.LoopFilterAcrossSlicesEnabled))
	m.add("deblocking_filter_control_present_flag", b2i(p.DeblockingFilterControlPresent))
	if p.DeblockingFilterControlPresent {
		m.add("deblocking_filter_override_enabled_flag", b2i(p.DeblockingFilterOverrideEnabled))
		m.add("pps_deblocking_filter_disabled_flag", b2i(p.DeblockingFilterDisabled))
		if !p.DeblockingFilterDisabled {
			m.add("pps_beta_offset_div2", int64(p.BetaOffsetDiv2))
			m.add("pps_tc_offset_div2", int64(p.TcOffsetDiv2))
		}
	}
	m.add("pps_scaling_list_data_present_flag", b2i(p.ScalingListDataPresent))
	m.add("lists_modification_present_flag", b2i(p.ListsModificationPresent))
	m.add("log2_parallel_merge_level_minus2", int64(p.Log2ParallelMergeLevelMinus2))
	m.add("slice_segment_header_extension_present_flag", b2i(p.SliceSegmentHeaderExtensionPresent))
	m.add("pps_extension_present_flag", b2i(p.ExtensionPresent))
	return m
}

func sliceValues(h *hevc.SliceHeader, sps *hevc.SPS, pps *hevc.PPS) valueMap {
	m := valueMap{}
	m.add("nal_unit_type", int64(h.NALType))
	m.add("nuh_temporal_id_plus1", int64(h.TemporalID+1))
	m.add("first_slice_segment_in_pic_flag", b2i(h.FirstSliceSegmentInPic))
	if hevc.IsIRAP(h.NALType) {
		m.add("no_output_of_prior_pics_flag", b2i(h.NoOutputOfPriorPics))
	}
	m.add("slice_pic_parameter_set_id", int64(h.PPSID))
	if !h.FirstSliceSegmentInPic {
		if pps.DependentSliceSegmentsEnabled {
			m.add("dependent_slice_segment_flag", b2i(h.DependentSliceSegment))
		}
		m.add("slice_segment_address", int64(h.SliceSegmentAddress))
	}
	if !h.DependentSliceSegment {
		m.add("slice_type", int64(h.SliceType))
		if pps.OutputFlagPresent {
			m.add("pic_output_flag", b2i(h.PicOutput))
		}
		if !hevc.IsIDR(h.NALType) {
			m.add("slice_pic_order_cnt_lsb", int64(h.POCLsb))
			m.add("short_term_ref_pic_set_sps_flag", b2i(h.ShortTermRefPicSetSPSFlag))
			if !h.ShortTermRefPicSetSPSFlag {
				m.rps(h.ShortTermRPS, len(sps.ShortTermRPS))
			} else if len(sps.ShortTermRPS) > 1 {
				m.add("short_term_ref_pic_set_idx", int64(h.ShortTermRefPicSetIdx))
			}
			if sps.LongTermRefPicsPresent {
				if len(sps.LtRefPicPOCLsbSPS) > 0 {
					m.add("num_long_term_sps", int64(h.NumLongTermSPS))
				}
				m.add("num_long_term_pics", int64(h.NumLongTermPics))
			}
			if sps.TemporalMVPEnabled {
				m.add("slice_temporal_mvp_enabled_flag", b2i(h.SliceTemporalMVPEnabled))
			}
		}
		if sps.SAOEnabled {
			m.add("slice_sao_luma_flag", b2i(h.SAOLuma))
			m.add("slice_sao_chroma_flag", b2i(h.SAOChroma))
		}
		if h.SliceType != hevc.SliceI {
			m.add("num_ref_idx_active_override_flag", b2i(h.NumRefIdxActiveOverride))
			if h.NumRefIdxActiveOverride {
				m.add("num_ref_idx_l0_active_minus1", int64(h.NumRefIdxL0Active-1))
				if h.SliceType == hevc.SliceB {
					m.add("num_ref_idx_l1_active_minus1", int64(h.NumRefIdxL1Active-1))
				}
			}
			if h.SliceType == hevc.SliceB {
				m.add("mvd_l1_zero_flag", b2i(h.MvdL1Zero))
			}
			if pps.CabacInitPresent {
				m.add("cabac_init_flag", b2i(h.CabacInit))
			}
			if h.SliceTemporalMVPEnabled {
				if h.SliceType == hevc.SliceB {
					m.add("collocated_from_l0_flag", b2i(h.CollocatedFromL0))
				}
				if (h.CollocatedFromL0 && h.NumRefIdxL0Active > 1) || (!h.CollocatedFromL0 && h.NumRefIdxL1Active > 1) {
					m.add("collocated_ref_idx", int64(h.CollocatedRefIdx))
				}
			}
			if w := h.PredWeights; w != nil {
				m.add("luma_log2_weight_denom", int64(w.LumaLog2WeightDenom))
				m.add("delta_chroma_log2_weight_denom", int64(w.DeltaChromaLog2WeightDenom))
				list := func(l string, entries []hevc.PredWeight, n int) {
					for i := 0; i < n; i++ {
						e := &entries[i]
						m.add(fmt.Sprintf("luma_weight_%s_flag[%d]", l, i), b2i(e.LumaFlag))
						m.add(fmt.Sprintf("chroma_weight_%s_flag[%d]", l, i), b2i(e.ChromaFlag))
						if e.LumaFlag {
							m.add(fmt.Sprintf("delta_luma_weight_%s[%d]", l, i), int64(e.DeltaLumaWeight))
							m.add(fmt.Sprintf("luma_offset_%s[%d]", l, i), int64(e.LumaOffset))
						}
						if e.ChromaFlag {
							for j := 0; j < 2; j++ {
								m.add(fmt.Sprintf("delta_chroma_weight_%s[%d][%d]", l, i, j), int64(e.DeltaChromaWeight[j]))
								m.add(fmt.Sprintf("chroma_offset_%s[%d][%d]", l, i, j), int64(e.DeltaChromaOffset[j]))
							}
						}
					}
				}
				list("l0", w.L0[:], int(h.NumRefIdxL0Active))
				if h.SliceType == hevc.SliceB {
					list("l1", w.L1[:], int(h.NumRefIdxL1Active))
				}
			}
			m.add("five_minus_max_num_merge_cand", int64(h.FiveMinusMaxNumMergeCand))
		}
		m.add("slice_qp_delta", int64(h.SliceQpDelta))
		if pps.SliceChromaQpOffsetsPresent {
			m.add("slice_cb_qp_offset", int64(h.SliceCbQpOffset))
			m.add("slice_cr_qp_offset", int64(h.SliceCrQpOffset))
		}
		if pps.DeblockingFilterOverrideEnabled {
			m.add("deblocking_filter_override_flag", b2i(h.DeblockingFilterOverride))
		}
		if h.DeblockingFilterOverride {
			m.add("slice_deblocking_filter_disabled_flag", b2i(h.DeblockingFilterDisabled))
			if !h.DeblockingFilterDisabled {
				m.add("slice_beta_offset_div2", int64(h.BetaOffsetDiv2))
				m.add("slice_tc_offset_div2", int64(h.TcOffsetDiv2))
			}
		}
		if pps.LoopFilterAcrossSlicesEnabled && (h.SAOLuma || h.SAOChroma || !h.DeblockingFilterDisabled) {
			m.add("slice_loop_filter_across_slices_enabled_flag", b2i(h.LoopFilterAcrossSlicesEnabled))
		}
	}
	if pps.TilesEnabled || pps.EntropyCodingSyncEnabled {
		m.add("num_entry_point_offsets", int64(h.NumEntryPointOffsets))
		if h.NumEntryPointOffsets > 0 {
			m.add("offset_len_minus1", int64(h.OffsetLenMinus1))
			for i, v := range h.EntryPointOffsetMinus1 {
				m.add(fmt.Sprintf("entry_point_offset_minus1[%d]", i), int64(v))
			}
		}
	}
	return m
}

// parseStream runs our parsers over every SPS, PPS and slice segment NAL
// unit.
func parseStream(t *testing.T, data []byte) []parsedUnit {
	t.Helper()
	ps := hevc.NewParameterSets()
	var out []parsedUnit
	var prev *hevc.SliceHeader
	for i, nal := range annexb.Split(data) {
		typ := hevc.Type(nal)
		switch {
		case typ == hevc.NALSPS:
			s, err := ps.AddSPS(nal)
			if err != nil {
				t.Fatalf("nal %d: SPS: %v", i, err)
			}
			out = append(out, parsedUnit{kind: "Sequence Parameter Set", values: spsValues(s)})
		case typ == hevc.NALPPS:
			p, err := ps.AddPPS(nal)
			if err != nil {
				t.Fatalf("nal %d: PPS: %v", i, err)
			}
			out = append(out, parsedUnit{kind: "Picture Parameter Set", values: ppsValues(p)})
		case hevc.IsSlice(typ):
			h, sps, pps, err := hevc.ParseSliceHeader(nal, ps, prev)
			if err != nil {
				t.Fatalf("nal %d: slice segment header: %v", i, err)
			}
			prev = h
			out = append(out, parsedUnit{kind: "Slice Segment Header", values: sliceValues(h, sps, pps), bits: h.HeaderBits})
		}
	}
	return out
}

// compareWithTrace checks every element of got against ffmpeg's
// trace_headers output for the same stream and returns the number of slice
// segments compared and the element names ffmpeg printed that got does not
// model.
func compareWithTrace(t *testing.T, path string, got []parsedUnit) (slices int, notModelled []string) {
	t.Helper()
	var trace []testutil.TraceUnit
	for _, u := range testutil.TraceHeaders(t, path, "hevc") {
		switch u.Kind {
		case "Sequence Parameter Set", "Picture Parameter Set", "Slice Segment Header":
			trace = append(trace, u)
		}
	}
	if len(got) != len(trace) {
		t.Fatalf("parsed %d units, ffmpeg traced %d", len(got), len(trace))
	}
	skipped := map[string]int{}
	for i := range trace {
		tu, gu := trace[i], got[i]
		if tu.Kind != gu.kind {
			t.Fatalf("unit %d: kind %q vs ours %q", i, tu.Kind, gu.kind)
		}
		next := map[string]int{}
		headerEnd := 0
		for _, f := range tu.Fields {
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
			if vals[k] != f.Value {
				t.Errorf("unit %d (%s): %s[%d] = %d, ffmpeg says %d", i, tu.Kind, f.Name, k, vals[k], f.Value)
			}
		}
		for name, vals := range gu.values {
			if n := next[name]; n > 0 && n != len(vals) {
				t.Errorf("unit %d (%s): we parsed %d %s values, ffmpeg printed %d", i, tu.Kind, len(vals), name, n)
			}
		}
		if gu.kind == "Slice Segment Header" {
			slices++
			// ffmpeg's trace ends with the byte_alignment() bits, so the
			// last position is where slice_segment_data() begins.
			if gu.bits != headerEnd {
				t.Errorf("unit %d: slice segment header ends at bit %d, ffmpeg says %d", i, gu.bits, headerEnd)
			}
		}
	}
	for n := range skipped {
		notModelled = append(notModelled, n)
	}
	sort.Strings(notModelled)
	return slices, notModelled
}

// TestHeadersMatchFFmpegTrace compares every syntax element our parsers
// expose against ffmpeg's trace_headers output for a range of x265 streams.
func TestHeadersMatchFFmpegTrace(t *testing.T) {
	for _, s := range oracleStreams {
		t.Run(s.name, func(t *testing.T) {
			path := testutil.GenerateHEVC(t, s.w, s.h, s.n, s.params, s.extra...)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			units := parseStream(t, data)
			slices, notModelled := compareWithTrace(t, path, units)
			if slices == 0 {
				t.Fatal("no slice segments compared")
			}
			if s.name == "fade-weights" {
				weights := 0
				for _, u := range units {
					weights += len(u.values["delta_luma_weight_l0[0]"]) + len(u.values["delta_chroma_weight_l0[0][0]"])
				}
				if weights == 0 {
					t.Error("the fade produced no prediction weights to compare")
				}
			}
			t.Logf("%d slice segments; not modelled: %v", slices, notModelled)
		})
	}
}
