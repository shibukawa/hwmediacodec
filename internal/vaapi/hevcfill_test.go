package vaapi

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec/bitstream/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// fakeSurface stands in for a VA surface in the DPB.
type fakeSurface uint32

func (f fakeSurface) surfaceID() uint32 { return uint32(f) }

// filledPicture is what the decoder would submit for one picture.
type filledPicture struct {
	pic    sys.PictureParameterBufferHEVC
	iq     *sys.IQMatrixBufferHEVC
	slices []sys.SliceParameterBufferHEVC
	sizes  []int // NAL unit sizes
}

// fillStream runs the decoder's bitstream side over an Annex-B stream and
// returns the VA parameter buffers of every picture.
func fillStream(t *testing.T, data []byte) []filledPicture {
	t.Helper()
	ps := hevc.NewParameterSets()
	dpb := hevc.NewDPB()
	dpb.MissingHandle = func() any { return fakeSurface(999) }
	r := annexb.NewReader(bytes.NewReader(data), codec.HEVC)
	var out []filledPicture
	for index := 0; ; index++ {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var slices [][]byte
		for _, nal := range annexb.Split(au) {
			switch typ := hevc.Type(nal); {
			case typ == hevc.NALSPS:
				if _, err := ps.AddSPS(nal); err != nil {
					t.Fatal(err)
				}
			case typ == hevc.NALPPS:
				if _, err := ps.AddPPS(nal); err != nil {
					t.Fatal(err)
				}
			case hevc.IsSlice(typ):
				slices = append(slices, nal)
			}
		}
		if len(slices) == 0 {
			continue
		}
		var headers []*hevc.SliceHeader
		var sps *hevc.SPS
		var pps *hevc.PPS
		var prev *hevc.SliceHeader
		for i, nal := range slices {
			sh, s, p, err := hevc.ParseSliceHeader(nal, ps, prev)
			if err != nil {
				t.Fatalf("access unit %d slice %d: %v", index, i, err)
			}
			headers = append(headers, sh)
			prev, sps, pps = sh, s, p
		}
		if err := checkHEVCStreamForTest(sps, pps); err != nil {
			t.Fatal(err)
		}
		cur, err := dpb.Start(sps, headers[0], fakeSurface(100+index))
		if err != nil {
			t.Fatalf("access unit %d: %v", index, err)
		}
		var f filledPicture
		refs := dpb.Refs()
		fillHEVCPictureParameters(&f.pic, sps, pps, headers[0], cur, dpb, refs)
		if sps.ScalingListEnabled {
			lists := &sps.ScalingList
			if pps.ScalingListDataPresent {
				lists = &pps.ScalingList
			}
			f.iq = &sys.IQMatrixBufferHEVC{}
			fillHEVCIQMatrix(f.iq, lists)
		}
		f.slices = make([]sys.SliceParameterBufferHEVC, len(slices))
		for i, nal := range slices {
			l0, l1 := dpb.RefPicLists(headers[i])
			fillHEVCSliceParameters(&f.slices[i], sps, headers[i], len(nal), l0, l1, refs, i == len(slices)-1)
			f.sizes = append(f.sizes, len(nal))
		}
		dpb.Finish()
		out = append(out, f)
	}
	return out
}

// checkHEVCStreamForTest mirrors the decoder's stream check without its
// linux-only error type.
func checkHEVCStreamForTest(sps *hevc.SPS, pps *hevc.PPS) error {
	if sps.ChromaFormatIDC != 1 || sps.BitDepthLuma != 8 || sps.BitDepthChroma != 8 || pps.RangeExtension {
		return fmt.Errorf("stream is not 8-bit 4:2:0 Main profile")
	}
	return nil
}

// traceSlice is one slice segment header as ffmpeg's trace_headers read it.
type traceSlice struct {
	values    map[string]int64
	headerEnd int // bits, including the NAL unit header and byte_alignment()
	rpsBits   int // size of a reference picture set coded in the header
}

func traceSlices(t *testing.T, path string) (slices []traceSlice, sps, pps map[string]int64) {
	t.Helper()
	isRPS := func(name string) bool {
		for _, p := range []string{"inter_ref_pic_set_prediction_flag", "delta_idx_minus1", "delta_rps_sign", "abs_delta_rps_minus1",
			"used_by_curr_pic_flag", "use_delta_flag", "num_negative_pics", "num_positive_pics",
			"delta_poc_s0_minus1", "used_by_curr_pic_s0_flag", "delta_poc_s1_minus1", "used_by_curr_pic_s1_flag"} {
			if name == p || strings.HasPrefix(name, p+"[") {
				return true
			}
		}
		return false
	}
	for _, u := range testutil.TraceHeaders(t, path, "hevc") {
		m := map[string]int64{}
		end, rpsStart, rpsEnd := 0, -1, 0
		for _, f := range u.Fields {
			m[f.Name] = f.Value
			end = max(end, f.Pos+f.Bits)
			if u.Kind == "Slice Segment Header" && isRPS(f.Name) {
				if rpsStart < 0 {
					rpsStart = f.Pos
				}
				rpsEnd = f.Pos + f.Bits
			}
		}
		switch u.Kind {
		case "Sequence Parameter Set":
			sps = m
		case "Picture Parameter Set":
			pps = m
		case "Slice Segment Header":
			ts := traceSlice{values: m, headerEnd: end}
			if rpsStart >= 0 {
				ts.rpsBits = rpsEnd - rpsStart
			}
			slices = append(slices, ts)
		}
	}
	return slices, sps, pps
}

var fillConfigs = []struct {
	name   string
	source func(w, h int) string
	args   []string
}{
	{"bpyramid-slices", nil, []string{"--bframes", "4", "--b-pyramid", "--ref", "5", "--keyint", "20", "--min-keyint", "20", "--no-open-gop", "--slices", "3"}},
	{"lowdelay", nil, []string{"--bframes", "0", "--ref", "6", "--keyint", "30", "--no-wpp"}},
	{"open-gop", nil, []string{"--bframes", "3", "--b-pyramid", "--ref", "4", "--keyint", "10", "--min-keyint", "10", "--open-gop", "--no-sao"}},
	{"weights", testutil.FadeSource, []string{"--bframes", "3", "--ref", "3", "--weightp", "--weightb", "--keyint", "48"}},
	{"scaling-list", nil, []string{"--bframes", "2", "--keyint", "12", "--min-keyint", "12", "--scaling-list", "default"}},
}

// TestHEVCDecodeBuffers checks the VA-API parameter buffers the decoder
// builds for x265 streams against two independent views of the same
// streams: the reference lists x265 says it used, and the syntax elements
// ffmpeg's trace_headers reads.
func TestHEVCDecodeBuffers(t *testing.T) {
	for _, c := range fillConfigs {
		t.Run(c.name, func(t *testing.T) {
			source := fmt.Sprintf("testsrc2=size=%dx%d:rate=30", 320, 240)
			if c.source != nil {
				source = c.source(320, 240)
			}
			path, frames := testutil.GenerateX265Source(t, source, 48, c.args...)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			pics := fillStream(t, data)
			trace, sps, pps := traceSlices(t, path)
			if len(pics) != len(frames) {
				t.Fatalf("filled %d pictures, x265 encoded %d", len(pics), len(frames))
			}

			seen := map[uint32]int32{} // surface -> POC of the picture it holds
			weights, tracePos := 0, 0
			for i, f := range pics {
				frame := frames[i]
				pp := &f.pic

				// Current picture.
				if pp.CurrPic.PicOrderCnt != int32(frame.POC) || pp.CurrPic.Flags != 0 || pp.CurrPic.PictureID != uint32(100+i) {
					t.Errorf("picture %d: CurrPic %+v, x265 POC %d", i, pp.CurrPic, frame.POC)
				}
				if hevc.IsIDR(int(traceNALType(trace[tracePos]))) {
					seen = map[uint32]int32{}
				}

				// ReferenceFrames: valid entries first, each naming the
				// surface that picture was decoded into, with exclusive
				// reference picture set flags in set order.
				stage, valid := 0, 0
				for j, ref := range pp.ReferenceFrames {
					if ref.Flags&sys.PictureHEVCInvalid != 0 {
						if ref.PictureID != sys.InvalidSurface {
							t.Errorf("picture %d: invalid entry %d has surface %d", i, j, ref.PictureID)
						}
						continue
					}
					if j != valid {
						t.Errorf("picture %d: valid entry %d follows an empty one", i, j)
					}
					valid++
					if poc, ok := seen[ref.PictureID]; !ok || poc != ref.PicOrderCnt {
						t.Errorf("picture %d: entry %d names surface %d with POC %d; that surface holds POC %d (decoded: %v)", i, j, ref.PictureID, ref.PicOrderCnt, poc, ok)
					}
					var s int
					switch ref.Flags {
					case sys.PictureHEVCRPSStCurrBefore:
						s = 0
						if ref.PicOrderCnt >= pp.CurrPic.PicOrderCnt {
							t.Errorf("picture %d: entry %d is ST_CURR_BEFORE with POC %d", i, j, ref.PicOrderCnt)
						}
					case sys.PictureHEVCRPSStCurrAfter:
						s = 1
						if ref.PicOrderCnt <= pp.CurrPic.PicOrderCnt {
							t.Errorf("picture %d: entry %d is ST_CURR_AFTER with POC %d", i, j, ref.PicOrderCnt)
						}
					case 0:
						s = 3
					default:
						t.Errorf("picture %d: entry %d has flags %#x", i, j, ref.Flags)
					}
					if s < stage {
						t.Errorf("picture %d: entry %d (flags %#x) is out of reference picture set order", i, j, ref.Flags)
					}
					stage = s
				}

				// Picture-level fields against the active parameter sets.
				checkField := func(name string, got uint32, trace map[string]int64) {
					if want, ok := trace[name]; ok && int64(got) != want {
						t.Errorf("picture %d: %s = %d, ffmpeg says %d", i, name, got, want)
					}
				}
				bit := func(v, mask uint32) uint32 { return b2u(v&mask != 0) }
				checkField("pic_width_in_luma_samples", uint32(pp.PicWidthInLumaSamples), sps)
				checkField("pic_height_in_luma_samples", uint32(pp.PicHeightInLumaSamples), sps)
				checkField("sps_max_dec_pic_buffering_minus1[0]", uint32(pp.SpsMaxDecPicBufferingMinus1), sps)
				checkField("log2_min_luma_coding_block_size_minus3", uint32(pp.Log2MinLumaCodingBlockSizeMinus3), sps)
				checkField("log2_diff_max_min_luma_coding_block_size", uint32(pp.Log2DiffMaxMinLumaCodingBlockSize), sps)
				checkField("log2_min_luma_transform_block_size_minus2", uint32(pp.Log2MinTransformBlockSizeMinus2), sps)
				checkField("log2_diff_max_min_luma_transform_block_size", uint32(pp.Log2DiffMaxMinTransformBlockSize), sps)
				checkField("max_transform_hierarchy_depth_inter", uint32(pp.MaxTransformHierarchyDepthInter), sps)
				checkField("max_transform_hierarchy_depth_intra", uint32(pp.MaxTransformHierarchyDepthIntra), sps)
				checkField("log2_max_pic_order_cnt_lsb_minus4", uint32(pp.Log2MaxPicOrderCntLsbMinus4), sps)
				checkField("num_short_term_ref_pic_sets", uint32(pp.NumShortTermRefPicSets), sps)
				checkField("chroma_format_idc", pp.PicFields&3, sps)
				checkField("scaling_list_enabled_flag", bit(pp.PicFields, sys.HEVCPicScalingListEnabledFlag), sps)
				checkField("amp_enabled_flag", bit(pp.PicFields, sys.HEVCPicAmpEnabledFlag), sps)
				checkField("strong_intra_smoothing_enabled_flag", bit(pp.PicFields, sys.HEVCPicStrongIntraSmoothingEnabledFlag), sps)
				checkField("sample_adaptive_offset_enabled_flag", bit(pp.SliceParsingFields, sys.HEVCSliceParsingSampleAdaptiveOffsetEnabled), sps)
				checkField("sps_temporal_mvp_enabled_flag", bit(pp.SliceParsingFields, sys.HEVCSliceParsingSpsTemporalMvpEnabledFlag), sps)
				checkField("long_term_ref_pics_present_flag", bit(pp.SliceParsingFields, sys.HEVCSliceParsingLongTermRefPicsPresentFlag), sps)
				checkField("sign_data_hiding_enabled_flag", bit(pp.PicFields, sys.HEVCPicSignDataHidingEnabledFlag), pps)
				checkField("cu_qp_delta_enabled_flag", bit(pp.PicFields, sys.HEVCPicCuQpDeltaEnabledFlag), pps)
				checkField("weighted_pred_flag", bit(pp.PicFields, sys.HEVCPicWeightedPredFlag), pps)
				checkField("weighted_bipred_flag", bit(pp.PicFields, sys.HEVCPicWeightedBipredFlag), pps)
				checkField("entropy_coding_sync_enabled_flag", bit(pp.PicFields, sys.HEVCPicEntropyCodingSyncEnabledFlag), pps)
				checkField("tiles_enabled_flag", bit(pp.PicFields, sys.HEVCPicTilesEnabledFlag), pps)
				checkField("pps_loop_filter_across_slices_enabled_flag", bit(pp.PicFields, sys.HEVCPicPpsLoopFilterAcrossSlicesEnabledFlag), pps)
				checkField("diff_cu_qp_delta_depth", uint32(pp.DiffCuQpDeltaDepth), pps)
				checkField("num_ref_idx_l0_default_active_minus1", uint32(pp.NumRefIdxL0DefaultActiveMinus1), pps)
				checkField("cabac_init_present_flag", bit(pp.SliceParsingFields, sys.HEVCSliceParsingCabacInitPresentFlag), pps)
				checkField("lists_modification_present_flag", bit(pp.SliceParsingFields, sys.HEVCSliceParsingListsModificationPresentFlag), pps)
				if got, want := int64(pp.InitQpMinus26), pps["init_qp_minus26"]; got != want {
					t.Errorf("picture %d: init_qp_minus26 %d, ffmpeg says %d", i, got, want)
				}
				if (f.iq != nil) != (sps["scaling_list_enabled_flag"] == 1) {
					t.Errorf("picture %d: IQ matrix sent = %v", i, f.iq != nil)
				}

				for k := range f.slices {
					sp := &f.slices[k]
					if tracePos >= len(trace) {
						t.Fatalf("picture %d slice %d: ffmpeg traced only %d slice segments", i, k, len(trace))
					}
					ts := trace[tracePos]
					tracePos++
					tv := ts.values
					if k == 0 {
						nal := int(traceNALType(ts))
						if got := bit(pp.SliceParsingFields, sys.HEVCSliceParsingIdrPicFlag); got != b2u(hevc.IsIDR(nal)) {
							t.Errorf("picture %d: IdrPicFlag %d for NAL type %d", i, got, nal)
						}
						if got := bit(pp.SliceParsingFields, sys.HEVCSliceParsingRapPicFlag); got != b2u(hevc.IsIRAP(nal)) {
							t.Errorf("picture %d: RapPicFlag %d for NAL type %d", i, got, nal)
						}
						if int(pp.StRpsBits) != ts.rpsBits {
							t.Errorf("picture %d: st_rps_bits %d, the set takes %d bits in ffmpeg's trace", i, pp.StRpsBits, ts.rpsBits)
						}
					}
					// Where slice data starts and how large the unit is.
					if int(sp.SliceDataByteOffset)*8 != ts.headerEnd {
						t.Errorf("picture %d slice %d: slice_data_byte_offset %d, ffmpeg's header ends at bit %d", i, k, sp.SliceDataByteOffset, ts.headerEnd)
					}
					if int(sp.SliceDataSize) != f.sizes[k] || sp.SliceDataOffset != 0 || sp.SliceDataFlag != sys.SliceDataFlagAll {
						t.Errorf("picture %d slice %d: data size %d offset %d flag %d for a %d byte NAL unit", i, k, sp.SliceDataSize, sp.SliceDataOffset, sp.SliceDataFlag, f.sizes[k])
					}
					last := k == len(f.slices)-1
					if bit(sp.LongSliceFlags, sys.HEVCSliceLastSliceOfPic) != b2u(last) {
						t.Errorf("picture %d slice %d: LastSliceOfPic is wrong", i, k)
					}
					sliceType := tv["slice_type"]
					if got := int64((sp.LongSliceFlags >> sys.HEVCSliceSliceTypeShift) & 3); got != sliceType {
						t.Errorf("picture %d slice %d: slice_type %d, ffmpeg says %d", i, k, got, sliceType)
					}
					if got, want := int64(sp.SliceSegmentAddress), tv["slice_segment_address"]; got != want {
						t.Errorf("picture %d slice %d: slice_segment_address %d, ffmpeg says %d", i, k, got, want)
					}
					if got, want := int64(sp.SliceQpDelta), tv["slice_qp_delta"]; got != want {
						t.Errorf("picture %d slice %d: slice_qp_delta %d, ffmpeg says %d", i, k, got, want)
					}
					for name, mask := range map[string]uint32{
						"slice_sao_luma_flag":             sys.HEVCSliceSaoLumaFlag,
						"slice_sao_chroma_flag":           sys.HEVCSliceSaoChromaFlag,
						"mvd_l1_zero_flag":                sys.HEVCSliceMvdL1ZeroFlag,
						"slice_temporal_mvp_enabled_flag": sys.HEVCSliceTemporalMvpEnabledFlag,
					} {
						if got, want := int64(bit(sp.LongSliceFlags, mask)), tv[name]; got != want {
							t.Errorf("picture %d slice %d: %s %d, ffmpeg says %d", i, k, name, got, want)
						}
					}
					if v, ok := tv["collocated_from_l0_flag"]; ok && int64(bit(sp.LongSliceFlags, sys.HEVCSliceCollocatedFromL0Flag)) != v {
						t.Errorf("picture %d slice %d: collocated_from_l0_flag differs from ffmpeg's %d", i, k, v)
					}
					wantColl := int64(0xff)
					if tv["slice_temporal_mvp_enabled_flag"] == 1 {
						wantColl = tv["collocated_ref_idx"]
					}
					if int64(sp.CollocatedRefIdx) != wantColl {
						t.Errorf("picture %d slice %d: collocated_ref_idx %d, want %d", i, k, sp.CollocatedRefIdx, wantColl)
					}
					if sliceType != hevc.SliceI {
						if got, want := int64(sp.FiveMinusMaxNumMergeCand), tv["five_minus_max_num_merge_cand"]; got != want {
							t.Errorf("picture %d slice %d: five_minus_max_num_merge_cand %d, ffmpeg says %d", i, k, got, want)
						}
					}

					// Reference lists: indices into ReferenceFrames that
					// resolve to the pictures x265 predicted from.
					lists := [2][]int{frame.List0, frame.List1}
					for l := 0; l < 2; l++ {
						var got []int
						for j, idx := range sp.RefPicList[l] {
							if idx == sys.HEVCRefPicListUnused {
								if j < len(lists[l]) {
									t.Errorf("picture %d slice %d: RefPicList%d[%d] is unused", i, k, l, j)
								}
								continue
							}
							if int(idx) >= valid {
								t.Fatalf("picture %d slice %d: RefPicList%d[%d] = %d points past the %d reference frames", i, k, l, j, idx, valid)
							}
							ref := pp.ReferenceFrames[idx]
							if ref.Flags&(sys.PictureHEVCRPSStCurrBefore|sys.PictureHEVCRPSStCurrAfter|sys.PictureHEVCRPSLtCurr) == 0 {
								t.Errorf("picture %d slice %d: RefPicList%d[%d] names a picture outside the current reference picture set", i, k, l, j)
							}
							got = append(got, int(ref.PicOrderCnt))
						}
						if !reflect.DeepEqual(got, lists[l]) {
							t.Errorf("picture %d slice %d (%s): RefPicList%d resolves to POCs %v, x265 used %v", i, k, frame.Type, l, got, lists[l])
						}
					}
					n0, n1 := len(frame.List0), len(frame.List1)
					if int(sp.NumRefIdxL0ActiveMinus1) != max(n0-1, 0) || int(sp.NumRefIdxL1ActiveMinus1) != max(n1-1, 0) {
						t.Errorf("picture %d slice %d: num_ref_idx minus1 %d/%d for lists of %d/%d", i, k, sp.NumRefIdxL0ActiveMinus1, sp.NumRefIdxL1ActiveMinus1, n0, n1)
					}

					weights += checkWeights(t, fmt.Sprintf("picture %d slice %d", i, k), sp, tv, n0, n1)
				}
				seen[pp.CurrPic.PictureID] = pp.CurrPic.PicOrderCnt
			}
			if tracePos != len(trace) {
				t.Errorf("compared %d slice segments, ffmpeg traced %d", tracePos, len(trace))
			}
			if c.name == "weights" && weights == 0 {
				t.Error("the fade produced no prediction weights to compare")
			}
			t.Logf("%d pictures, %d slice segments, %d weight entries", len(pics), tracePos, weights)
		})
	}
}

func traceNALType(ts traceSlice) int64 { return ts.values["nal_unit_type"] }

// checkWeights compares the prediction weight tables of a slice with
// ffmpeg's trace and returns the number of weighted entries. The chroma
// offset VA-API wants is the derived one (7-56), computed here from the
// traced syntax elements.
func checkWeights(t *testing.T, where string, sp *sys.SliceParameterBufferHEVC, tv map[string]int64, n0, n1 int) int {
	t.Helper()
	denom, coded := tv["luma_log2_weight_denom"]
	if !coded {
		var zero sys.SliceParameterBufferHEVC
		if sp.LumaLog2WeightDenom != 0 || sp.DeltaChromaLog2WeightDenom != 0 || sp.DeltaLumaWeightL0 != zero.DeltaLumaWeightL0 ||
			sp.LumaOffsetL0 != zero.LumaOffsetL0 || sp.DeltaChromaWeightL0 != zero.DeltaChromaWeightL0 || sp.ChromaOffsetL0 != zero.ChromaOffsetL0 ||
			sp.DeltaLumaWeightL1 != zero.DeltaLumaWeightL1 || sp.ChromaOffsetL1 != zero.ChromaOffsetL1 {
			t.Errorf("%s: weights sent without a prediction weight table", where)
		}
		return 0
	}
	deltaChroma := tv["delta_chroma_log2_weight_denom"]
	if int64(sp.LumaLog2WeightDenom) != denom || int64(sp.DeltaChromaLog2WeightDenom) != deltaChroma {
		t.Errorf("%s: weight denominators %d/%d, ffmpeg says %d/%d", where, sp.LumaLog2WeightDenom, sp.DeltaChromaLog2WeightDenom, denom, deltaChroma)
	}
	chromaDenom := uint(denom + deltaChroma)
	count := 0
	list := func(l string, n int, dlw, lo []int8, dcw, co [][2]int8) {
		for i := 0; i < n; i++ {
			var wantDLW, wantLO int64
			if tv[fmt.Sprintf("luma_weight_%s_flag[%d]", l, i)] == 1 {
				wantDLW = tv[fmt.Sprintf("delta_luma_weight_%s[%d]", l, i)]
				wantLO = tv[fmt.Sprintf("luma_offset_%s[%d]", l, i)]
				count++
			}
			if int64(dlw[i]) != wantDLW || int64(lo[i]) != wantLO {
				t.Errorf("%s: luma weight %s[%d] = %d/%d, ffmpeg says %d/%d", where, l, i, dlw[i], lo[i], wantDLW, wantLO)
			}
			for j := 0; j < 2; j++ {
				var wantDCW, wantCO int64
				if tv[fmt.Sprintf("chroma_weight_%s_flag[%d]", l, i)] == 1 {
					wantDCW = tv[fmt.Sprintf("delta_chroma_weight_%s[%d][%d]", l, i, j)]
					delta := tv[fmt.Sprintf("chroma_offset_%s[%d][%d]", l, i, j)]
					weight := (int64(1) << chromaDenom) + wantDCW
					wantCO = min(max(128+delta-((128*weight)>>chromaDenom), -128), 127)
					count++
				}
				if int64(dcw[i][j]) != wantDCW || int64(co[i][j]) != wantCO {
					t.Errorf("%s: chroma weight %s[%d][%d] = %d/%d, want %d/%d", where, l, i, j, dcw[i][j], co[i][j], wantDCW, wantCO)
				}
			}
		}
	}
	list("l0", n0, sp.DeltaLumaWeightL0[:], sp.LumaOffsetL0[:], sp.DeltaChromaWeightL0[:], sp.ChromaOffsetL0[:])
	list("l1", n1, sp.DeltaLumaWeightL1[:], sp.LumaOffsetL1[:], sp.DeltaChromaWeightL1[:], sp.ChromaOffsetL1[:])
	return count
}

// TestHEVCIQMatrix checks that scaling lists reach the driver in the order
// they are coded, for coded lists and for the defaults.
func TestHEVCIQMatrix(t *testing.T) {
	lists := hevc.DefaultScalingList()
	for i := range lists.L4[2] {
		lists.L4[2][i] = uint8(i + 1)
	}
	for i := range lists.L32[1] {
		lists.L32[1][i] = uint8(200 - i)
	}
	lists.DC16[4], lists.DC32[1] = 33, 44
	var iq sys.IQMatrixBufferHEVC
	fillHEVCIQMatrix(&iq, &lists)
	if iq.ScalingList4x4[2][0] != 1 || iq.ScalingList4x4[2][15] != 16 || iq.ScalingList4x4[0][7] != 16 {
		t.Errorf("4x4 lists: %v", iq.ScalingList4x4)
	}
	// Coded order: the eleventh coefficient of the default intra list is
	// the first 17 (position x=0, y=4 of the matrix).
	if iq.ScalingList8x8[0][9] != 16 || iq.ScalingList8x8[0][10] != 17 || iq.ScalingList16x16[5][63] != 91 || iq.ScalingList8x8[1][63] != 115 {
		t.Errorf("default lists are not in coded order")
	}
	if iq.ScalingList32x32[0][63] != 115 || iq.ScalingList32x32[1][0] != 200 || iq.ScalingList32x32[1][63] != 137 {
		t.Errorf("32x32 lists: %v", iq.ScalingList32x32)
	}
	if iq.ScalingListDC16x16 != [6]uint8{16, 16, 16, 16, 33, 16} || iq.ScalingListDC32x32 != [2]uint8{16, 44} {
		t.Errorf("DC coefficients: %v %v", iq.ScalingListDC16x16, iq.ScalingListDC32x32)
	}
}
