package av1_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// headerStreams are the encoder settings the frame header parser is checked
// with. Between them they reach every branch of the header syntax that
// libaom and SVT-AV1 can be made to write.
var headerStreams = []struct {
	name    string
	encoder string
	w, h    int
	frames  int
	args    []string
}{
	{"svt", "libsvtav1", 320, 240, 40, []string{"-preset", "10", "-g", "25"}},
	{"svt-film-grain", "libsvtav1", 320, 240, 30, []string{"-preset", "10", "-g", "25", "-svtav1-params", "film-grain=8"}},
	{"svt-10bit", "libsvtav1", 320, 240, 20, []string{"-preset", "10", "-pix_fmt", "yuv420p10le"}},
	{"aom", "libaom-av1", 320, 240, 40, []string{"-cpu-used", "6", "-g", "20", "-lag-in-frames", "16", "-b:v", "300k"}},
	{"aom-global-motion", "libaom-av1", 320, 240, 24, []string{"-vf", "rotate=a=0.03*n,scale=iw*1.0:ih", "-cpu-used", "2", "-g", "24", "-lag-in-frames", "12", "-b:v", "400k"}},
	{"aom-tiles", "libaom-av1", 640, 480, 20, []string{"-cpu-used", "8", "-g", "10", "-tiles", "3x2", "-b:v", "500k"}},
	{"aom-tiles-uniform", "libaom-av1", 640, 480, 20, []string{"-cpu-used", "8", "-tile-columns", "2", "-tile-rows", "1", "-b:v", "500k"}},
	{"aom-film-grain", "libaom-av1", 320, 240, 30, []string{"-cpu-used", "8", "-g", "15", "-lag-in-frames", "8", "-aom-params", "film-grain-test=3", "-b:v", "300k"}},
	{"aom-film-grain-table", "libaom-av1", 320, 240, 30, []string{"-cpu-used", "8", "-g", "15", "-denoise-noise-level", "30", "-b:v", "300k"}},
	{"aom-error-resilient", "libaom-av1", 320, 240, 30, []string{"-cpu-used", "8", "-g", "15", "-lag-in-frames", "8", "-error-resilience", "default", "-b:v", "300k"}},
	{"aom-screen", "libaom-av1", 320, 240, 20, []string{"-cpu-used", "6", "-g", "10", "-aom-params", "tune-content=screen", "-b:v", "300k"}},
	{"aom-lossless", "libaom-av1", 160, 120, 10, []string{"-cpu-used", "8", "-aom-params", "lossless=1"}},
	{"aom-segmentation", "libaom-av1", 320, 240, 30, []string{"-cpu-used", "8", "-g", "15", "-aq-mode", "1", "-b:v", "200k"}},
	{"aom-deltaq", "libaom-av1", 320, 240, 20, []string{"-cpu-used", "6", "-g", "10", "-aom-params", "deltaq-mode=1:delta-lf-mode=1:enable-tpl-model=1", "-b:v", "200k"}},
	{"aom-realtime", "libaom-av1", 320, 240, 40, []string{"-usage", "realtime", "-cpu-used", "8", "-g", "20", "-aq-mode", "3", "-lag-in-frames", "0", "-b:v", "200k"}},
	{"aom-qmatrix", "libaom-av1", 320, 240, 20, []string{"-cpu-used", "8", "-g", "10", "-aom-params", "enable-qm=1", "-b:v", "300k"}},
	{"aom-no-order-hint", "libaom-av1", 320, 240, 20, []string{"-cpu-used", "8", "-g", "10", "-aom-params", "enable-order-hint=0", "-b:v", "300k"}},
	{"aom-128sb-restoration", "libaom-av1", 640, 480, 12, []string{"-cpu-used", "3", "-g", "12", "-aom-params", "sb-size=128:enable-restoration=1", "-b:v", "600k"}},
	{"aom-gray", "libaom-av1", 320, 240, 16, []string{"-cpu-used", "8", "-pix_fmt", "gray", "-b:v", "200k"}},
	{"aom-444", "libaom-av1", 320, 240, 16, []string{"-cpu-used", "8", "-pix_fmt", "yuv444p", "-aom-params", "tune-content=screen", "-b:v", "300k"}},
	{"aom-still", "libaom-av1", 320, 240, 1, []string{"-cpu-used", "8", "-still-picture", "1"}},
	{"rav1e", "librav1e", 320, 240, 20, []string{"-speed", "10", "-g", "10", "-b:v", "300k"}},
	{"aomenc-superres", "aomenc", 320, 240, 24, []string{"--cpu-used=6", "--kf-max-dist=12", "--superres-mode=1", "--superres-denominator=12", "--superres-kf-denominator=11", "--target-bitrate=300"}},
	{"aomenc-superres-random", "aomenc", 320, 240, 24, []string{"--cpu-used=6", "--kf-max-dist=12", "--superres-mode=2", "--target-bitrate=300"}},
	{"aomenc-resize", "aomenc", 320, 240, 24, []string{"--cpu-used=6", "--kf-max-dist=12", "--resize-mode=1", "--resize-denominator=12", "--resize-kf-denominator=10", "--target-bitrate=300"}},
	{"aomenc-resize-random", "aomenc", 320, 240, 40, []string{"--cpu-used=6", "--kf-max-dist=40", "--resize-mode=2", "--target-bitrate=300"}},
	{"aomenc-resize-superres-random", "aomenc", 320, 240, 40, []string{"--cpu-used=6", "--kf-max-dist=40", "--lag-in-frames=0", "--resize-mode=2", "--superres-mode=2", "--target-bitrate=300"}},
	{"aomenc-sframes", "aomenc", 320, 240, 40, []string{"--cpu-used=6", "--kf-max-dist=40", "--lag-in-frames=0", "--sframe-dist=8", "--sframe-mode=1", "--error-resilient=1", "--target-bitrate=300"}},
	{"aomenc-tile-groups", "aomenc", 640, 480, 12, []string{"--cpu-used=6", "--tile-columns=2", "--tile-rows=1", "--num-tile-groups=3", "--target-bitrate=500"}},
	{"aomenc-forward-keyframes", "aomenc", 320, 240, 48, []string{"--cpu-used=6", "--lag-in-frames=19", "--enable-fwd-kf=1", "--fwd-kf-dist=16", "--kf-max-dist=16", "--kf-min-dist=16", "--target-bitrate=300"}},
	{"aomenc-frame-parallel", "aomenc", 320, 240, 24, []string{"--cpu-used=6", "--frame-parallel=1", "--reduced-reference-set=1", "--target-bitrate=300"}},
	{"aomenc-realtime-resize", "aomenc", 320, 240, 60, []string{"--rt", "--cpu-used=9", "--lag-in-frames=0", "--end-usage=cbr", "--resize-mode=3", "--target-bitrate=20", "--kf-max-dist=1000", "--buf-sz=200", "--buf-initial-sz=100", "--buf-optimal-sz=100", "--undershoot-pct=50", "--overshoot-pct=50"}},
}

// headerValues lists what the trace of a frame header must show for every
// syntax element the parser models, by the element's name in ffmpeg's
// trace. Elements that the stream did not carry are simply absent from the
// trace.
func headerValues(h *av1.Header, seq *av1.SequenceHeader) map[string]int64 {
	m := map[string]int64{
		"show_existing_frame":             b2i(h.ShowExistingFrame),
		"frame_to_show_map_idx":           int64(h.FrameToShowMapIdx),
		"frame_type":                      int64(h.FrameType),
		"show_frame":                      b2i(h.ShowFrame),
		"showable_frame":                  b2i(h.ShowableFrame),
		"error_resilient_mode":            b2i(h.ErrorResilientMode),
		"disable_cdf_update":              b2i(h.DisableCDFUpdate),
		"allow_screen_content_tools":      b2i(h.AllowScreenContent),
		"force_integer_mv":                b2i(h.ForceIntegerMV),
		"current_frame_id":                int64(h.CurrentFrameID),
		"frame_size_override_flag":        b2i(h.FrameSizeOverride),
		"order_hint":                      int64(h.OrderHint),
		"primary_ref_frame":               int64(h.PrimaryRefFrame),
		"refresh_frame_flags":             int64(h.RefreshFrameFlags),
		"frame_width_minus_1":             int64(h.UpscaledWidth - 1),
		"frame_height_minus_1":            int64(h.FrameHeight - 1),
		"use_superres":                    b2i(h.UseSuperres),
		"coded_denom":                     int64(h.SuperresDenom) - 9,
		"render_and_frame_size_different": b2i(h.RenderWidth != h.UpscaledWidth || h.RenderHeight != h.FrameHeight),
		"render_width_minus_1":            int64(h.RenderWidth - 1),
		"render_height_minus_1":           int64(h.RenderHeight - 1),
		"allow_intrabc":                   b2i(h.AllowIntraBC),
		"frame_refs_short_signaling":      b2i(h.FrameRefsShort),
		"allow_high_precision_mv":         b2i(h.AllowHighPrecMV),
		"is_filter_switchable":            b2i(h.InterpolationFilter == av1.InterpolationFilterSwitchable),
		"interpolation_filter":            int64(h.InterpolationFilter),
		"is_motion_mode_switchable":       b2i(h.MotionModeSwitch),
		"use_ref_frame_mvs":               b2i(h.UseRefFrameMVs),
		"disable_frame_end_update_cdf":    b2i(h.DisableFrameEndCDF),
		"uniform_tile_spacing_flag":       b2i(h.UniformTileSpacing),
		"context_update_tile_id":          int64(h.ContextUpdateTileID),
		"tile_size_bytes_minus1":          int64(h.TileSizeBytes - 1),
		"base_q_idx":                      int64(h.BaseQIdx),
		"using_qmatrix":                   b2i(h.UsingQMatrix),
		"qm_y":                            int64(h.QmY),
		"qm_u":                            int64(h.QmU),
		"qm_v":                            int64(h.QmV),
		"segmentation_enabled":            b2i(h.SegmentationEnabled),
		"segmentation_update_map":         b2i(h.SegmentationUpdateMap),
		"segmentation_temporal_update":    b2i(h.SegmentationTemporalUpdate),
		"segmentation_update_data":        b2i(h.SegmentationUpdateData),
		"delta_q_present":                 b2i(h.DeltaQPresent),
		"delta_q_res":                     int64(h.DeltaQRes),
		"delta_lf_present":                b2i(h.DeltaLFPresent),
		"delta_lf_res":                    int64(h.DeltaLFRes),
		"delta_lf_multi":                  b2i(h.DeltaLFMulti),
		"loop_filter_sharpness":           int64(h.LoopFilterSharpness),
		"loop_filter_delta_enabled":       b2i(h.LoopFilterDeltaEnabled),
		"loop_filter_delta_update":        b2i(h.LoopFilterDeltaUpdate),
		"cdef_damping_minus_3":            int64(h.CDEFDampingMinus3),
		"cdef_bits":                       int64(h.CDEFBits),
		"lr_unit_shift":                   int64(h.LrUnitShift),
		"lr_uv_shift":                     int64(h.LrUVShift),
		"tx_mode":                         int64(h.TxMode),
		"reference_select":                b2i(h.ReferenceSelect),
		"skip_mode_present":               b2i(h.SkipModePresent),
		"allow_warped_motion":             b2i(h.AllowWarpedMotion),
		"reduced_tx_set":                  b2i(h.ReducedTxSet),
		"apply_grain":                     b2i(h.FilmGrain.ApplyGrain),
		"grain_seed":                      int64(h.FilmGrain.GrainSeed),
		"update_grain":                    b2i(h.FilmGrain.UpdateGrain),
		"num_y_points":                    int64(h.FilmGrain.NumYPoints),
		"chroma_scaling_from_luma":        b2i(h.FilmGrain.ChromaScalingFromLuma),
		"num_cb_points":                   int64(h.FilmGrain.NumCbPoints),
		"num_cr_points":                   int64(h.FilmGrain.NumCrPoints),
		"grain_scaling_minus_8":           int64(h.FilmGrain.GrainScalingMinus8),
		"ar_coeff_lag":                    int64(h.FilmGrain.ARCoeffLag),
		"ar_coeff_shift_minus_6":          int64(h.FilmGrain.ARCoeffShiftMinus6),
		"grain_scale_shift":               int64(h.FilmGrain.GrainScaleShift),
		"cb_mult":                         int64(h.FilmGrain.CbMult),
		"cb_luma_mult":                    int64(h.FilmGrain.CbLumaMult),
		"cb_offset":                       int64(h.FilmGrain.CbOffset),
		"cr_mult":                         int64(h.FilmGrain.CrMult),
		"cr_luma_mult":                    int64(h.FilmGrain.CrLumaMult),
		"cr_offset":                       int64(h.FilmGrain.CrOffset),
		"overlap_flag":                    b2i(h.FilmGrain.OverlapFlag),
		"clip_to_restricted_range":        b2i(h.FilmGrain.ClipToRestrictedRange),
		"delta_q_y_dc.delta_q":            int64(h.DeltaQYDc),
		"delta_q_u_dc.delta_q":            int64(h.DeltaQUDc),
		"delta_q_u_ac.delta_q":            int64(h.DeltaQUAc),
		"delta_q_v_dc.delta_q":            int64(h.DeltaQVDc),
		"delta_q_v_ac.delta_q":            int64(h.DeltaQVAc),
		"delta_q_y_dc.delta_coded":        b2i(h.DeltaQYDc != 0),
		"delta_q_u_dc.delta_coded":        b2i(h.DeltaQUDc != 0),
		"delta_q_u_ac.delta_coded":        b2i(h.DeltaQUAc != 0),
		"delta_q_v_dc.delta_coded":        b2i(h.DeltaQVDc != 0),
		"delta_q_v_ac.delta_coded":        b2i(h.DeltaQVAc != 0),
		"diff_uv_delta":                   b2i(h.DeltaQUDc != h.DeltaQVDc || h.DeltaQUAc != h.DeltaQVAc),
	}
	// lr_type is the coded value; the header holds what it maps to.
	lrType := [4]int64{av1.RestoreNone: 0, av1.RestoreSwitchable: 1, av1.RestoreWiener: 2, av1.RestoreSgrproj: 3}
	for i := 0; i < 3; i++ {
		m[fmt.Sprintf("lr_type[%d]", i)] = lrType[h.FrameRestorationType[i]]
	}
	for i := 0; i < 4; i++ {
		m[fmt.Sprintf("loop_filter_level[%d]", i)] = int64(h.LoopFilterLevel[i])
	}
	for i := 0; i < av1.RefsPerFrame; i++ {
		m[fmt.Sprintf("ref_frame_idx[%d]", i)] = int64(h.RefFrameIdx[i])
	}
	for i := 0; i < av1.TotalRefsPerFrame; i++ {
		m[fmt.Sprintf("loop_filter_ref_deltas[%d]", i)] = int64(h.LoopFilterRefDeltas[i])
		m[fmt.Sprintf("is_global[%d]", i)] = b2i(h.GmType[i] != av1.WarpIdentity)
		m[fmt.Sprintf("is_rot_zoom[%d]", i)] = b2i(h.GmType[i] == av1.WarpRotzoom)
		m[fmt.Sprintf("is_translation[%d]", i)] = b2i(h.GmType[i] == av1.WarpTranslation)
	}
	for i := 0; i < 2; i++ {
		m[fmt.Sprintf("loop_filter_mode_deltas[%d]", i)] = int64(h.LoopFilterModeDeltas[i])
	}
	for i := 0; i < 8; i++ {
		m[fmt.Sprintf("cdef_y_pri_strength[%d]", i)] = int64(h.CDEFYPriStrength[i])
		m[fmt.Sprintf("cdef_y_sec_strength[%d]", i)] = int64(h.CDEFYSecStrength[i])
		m[fmt.Sprintf("cdef_uv_pri_strength[%d]", i)] = int64(h.CDEFUVPriStrength[i])
		m[fmt.Sprintf("cdef_uv_sec_strength[%d]", i)] = int64(h.CDEFUVSecStrength[i])
	}
	for i := 0; i < h.TileCols; i++ {
		m[fmt.Sprintf("width_in_sbs_minus_1[%d]", i)] = int64(h.WidthInSbsMinus1[i])
	}
	for i := 0; i < h.TileRows; i++ {
		m[fmt.Sprintf("height_in_sbs_minus_1[%d]", i)] = int64(h.HeightInSbsMinus1[i])
	}
	m["tile_cols_log2"] = int64(h.TileColsLog2)
	m["tile_rows_log2"] = int64(h.TileRowsLog2)
	for i := 0; i < av1.MaxSegments; i++ {
		for j := 0; j < av1.SegLvlMax; j++ {
			m[fmt.Sprintf("feature_enabled[%d][%d]", i, j)] = b2i(h.FeatureEnabled[i][j])
			m[fmt.Sprintf("feature_value[%d][%d]", i, j)] = int64(h.FeatureData[i][j])
		}
	}
	g := &h.FilmGrain
	for i := 0; i < 14; i++ {
		m[fmt.Sprintf("point_y_value[%d]", i)] = int64(g.PointYValue[i])
		m[fmt.Sprintf("point_y_scaling[%d]", i)] = int64(g.PointYScaling[i])
	}
	for i := 0; i < 10; i++ {
		m[fmt.Sprintf("point_cb_value[%d]", i)] = int64(g.PointCbValue[i])
		m[fmt.Sprintf("point_cb_scaling[%d]", i)] = int64(g.PointCbScaling[i])
		m[fmt.Sprintf("point_cr_value[%d]", i)] = int64(g.PointCrValue[i])
		m[fmt.Sprintf("point_cr_scaling[%d]", i)] = int64(g.PointCrScaling[i])
	}
	for i := 0; i < 24; i++ {
		m[fmt.Sprintf("ar_coeffs_y_plus_128[%d]", i)] = int64(g.ARCoeffsYPlus128[i])
	}
	for i := 0; i < 25; i++ {
		m[fmt.Sprintf("ar_coeffs_cb_plus_128[%d]", i)] = int64(g.ARCoeffsCbPlus128[i])
		m[fmt.Sprintf("ar_coeffs_cr_plus_128[%d]", i)] = int64(g.ARCoeffsCrPlus128[i])
	}
	return m
}

// unmodelled names syntax elements of the trace that the Header does not
// keep as such: their effect is checked through the header length and
// through the elements that follow them.
func unmodelled(name string) bool {
	base, _, _ := strings.Cut(name, "[")
	switch base {
	case "frame_presentation_time", "display_frame_id", "buffer_removal_time_present_flag", "buffer_removal_time",
		"ref_order_hint", "last_frame_idx", "golden_frame_idx", "delta_frame_id_minus1", "found_ref",
		"update_ref_delta", "update_mode_delta", "gm_params", "film_grain_params_ref_idx",
		"increment_tile_cols_log2", "increment_tile_rows_log2", "subexp_more_bits", "subexp_bits", "subexp_final_bits":
		return true
	}
	return false
}

// TestFrameHeadersMatchFFmpegTrace parses every frame header of streams
// written with a range of encoder settings and compares each syntax element
// and the length of each header with ffmpeg's trace_headers bitstream
// filter. The headers are parsed against the reference state the parser
// maintains itself, so values that depend on earlier frames (frame sizes
// taken from references, order hints, segmentation data) are covered too.
func TestFrameHeadersMatchFFmpegTrace(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range headerStreams {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			if tc.encoder == "aomenc" {
				path = testutil.GenerateAV1Aomenc(t, tc.w, tc.h, tc.frames, tc.args...)
			} else {
				path = testutil.GenerateAV1With(t, tc.encoder, tc.w, tc.h, tc.frames, tc.args...)
			}
			var traces []testutil.TraceUnit
			for _, u := range testutil.TraceHeaders(t, path, "ivf") {
				if u.Kind == "Frame Header" && len(u.Fields) > 0 {
					traces = append(traces, u)
				}
			}
			state := &av1.State{}
			n, kinds := 0, map[string]int{}
			for ti, data := range readIVF(t, path) {
				obus, err := av1.Split(data)
				if err != nil {
					t.Fatal(err)
				}
				var cur *av1.Header // the frame whose tile groups are still to come
				tiles := 0
				for _, o := range obus {
					switch o.Type {
					case av1.OBUSequenceHeader:
						seq, err := av1.ParseSequenceHeader(o.Payload)
						if err != nil {
							t.Fatal(err)
						}
						state.Seq = seq
					case av1.OBUFrameHeader, av1.OBUFrame:
						if cur != nil {
							continue // a repeated frame header
						}
						if n >= len(traces) {
							t.Fatalf("temporal unit %d: more frame headers than the %d ffmpeg traced", ti, len(traces))
						}
						h, err := state.ParseHeader(o.Payload, o.TemporalID, o.SpatialID)
						if err != nil {
							t.Fatalf("temporal unit %d, frame header %d: %v", ti, n, err)
						}
						u := traces[n]
						want := headerValues(h, state.Seq)
						end := 0
						for _, f := range u.Fields {
							if f.Name == "zero_bit" || strings.HasPrefix(f.Name, "trailing") {
								continue
							}
							end = f.Pos + f.Bits
							seen[strings.Split(f.Name, "[")[0]] = true
							v, ok := want[f.Name]
							if !ok {
								if !unmodelled(f.Name) {
									t.Errorf("frame header %d: the trace has %s = %d, which the test does not know", n, f.Name, f.Value)
								}
								continue
							}
							if f.Name == "force_integer_mv" && h.IsIntra() {
								// The trace shows the coded bit; the header
								// holds 1 for every intra frame.
								continue
							}
							if v != f.Value {
								t.Errorf("frame header %d (temporal unit %d, %s): %s = %d, ffmpeg says %d", n, ti, h.FrameType, f.Name, v, f.Value)
							}
						}
						if got := h.HeaderBits + 8*(len(o.Raw)-len(o.Payload)); got != end {
							t.Fatalf("frame header %d (temporal unit %d, %s): ends at bit %d, ffmpeg's last element ends at %d", n, ti, h.FrameType, got, end)
						}
						n++
						switch {
						case h.ShowExistingFrame:
							kinds["show-existing"]++
							state.Update(h)
						case o.Type == av1.OBUFrame:
							tg, err := h.ParseTileGroup(o.Payload[(h.HeaderBits+7)/8:])
							if err != nil {
								t.Fatalf("frame header %d: tile group: %v", n-1, err)
							}
							tiles += len(tg.Tiles)
							cur = h
						default:
							cur = h
						}
						if !h.ShowExistingFrame {
							kinds[h.FrameType.String()]++
						}
					case av1.OBUTileGroup:
						if cur == nil {
							t.Fatalf("temporal unit %d: tile group without a frame header", ti)
						}
						tg, err := cur.ParseTileGroup(o.Payload)
						if err != nil {
							t.Fatalf("temporal unit %d: tile group: %v", ti, err)
						}
						tiles += len(tg.Tiles)
					}
					if cur != nil && tiles == cur.NumTiles() {
						state.Update(cur)
						cur, tiles = nil, 0
					}
				}
				if cur != nil {
					t.Fatalf("temporal unit %d ends with %d of %d tiles", ti, tiles, cur.NumTiles())
				}
			}
			if n != len(traces) {
				t.Fatalf("parsed %d frame headers, ffmpeg traced %d", n, len(traces))
			}
			t.Logf("%d frame headers agree with ffmpeg: %v", n, kinds)
		})
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	t.Logf("syntax elements seen: %s", strings.Join(names, " "))
}
