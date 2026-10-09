package sys

import (
	"testing"
	"unsafe"
)

// Expected values measured with gcc against va_dec_av1.h (libva 2.22); they
// are identical on x86-64 and arm64.
func TestAV1StructLayouts(t *testing.T) {
	var seg SegmentationAV1
	var fg FilmGrainAV1
	var wm WarpedMotionParamsAV1
	var pp DecPictureParameterBufferAV1
	var sp SliceParameterBufferAV1

	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"sizeof VASegmentationStructAV1", unsafe.Sizeof(seg), 156},
		{"VASegmentationStructAV1.feature_data", unsafe.Offsetof(seg.FeatureData), 4},
		{"VASegmentationStructAV1.feature_mask", unsafe.Offsetof(seg.FeatureMask), 132},
		{"sizeof VAFilmGrainStructAV1", unsafe.Sizeof(fg), 176},
		{"VAFilmGrainStructAV1.grain_seed", unsafe.Offsetof(fg.GrainSeed), 4},
		{"VAFilmGrainStructAV1.num_y_points", unsafe.Offsetof(fg.NumYPoints), 6},
		{"VAFilmGrainStructAV1.point_y_scaling", unsafe.Offsetof(fg.PointYScaling), 21},
		{"VAFilmGrainStructAV1.num_cb_points", unsafe.Offsetof(fg.NumCbPoints), 35},
		{"VAFilmGrainStructAV1.num_cr_points", unsafe.Offsetof(fg.NumCrPoints), 56},
		{"VAFilmGrainStructAV1.ar_coeffs_y", unsafe.Offsetof(fg.ARCoeffsY), 77},
		{"VAFilmGrainStructAV1.ar_coeffs_cb", unsafe.Offsetof(fg.ARCoeffsCb), 101},
		{"VAFilmGrainStructAV1.ar_coeffs_cr", unsafe.Offsetof(fg.ARCoeffsCr), 126},
		{"VAFilmGrainStructAV1.cb_mult", unsafe.Offsetof(fg.CbMult), 151},
		{"VAFilmGrainStructAV1.cb_offset", unsafe.Offsetof(fg.CbOffset), 154},
		{"VAFilmGrainStructAV1.cr_mult", unsafe.Offsetof(fg.CrMult), 156},
		{"VAFilmGrainStructAV1.cr_offset", unsafe.Offsetof(fg.CrOffset), 158},
		{"sizeof VAWarpedMotionParamsAV1", unsafe.Sizeof(wm), 56},
		{"VAWarpedMotionParamsAV1.wmmat", unsafe.Offsetof(wm.WMMat), 4},
		{"VAWarpedMotionParamsAV1.invalid", unsafe.Offsetof(wm.Invalid), 36},
		{"sizeof VADecPictureParameterBufferAV1", unsafe.Sizeof(pp), 1160},
		{"VADecPictureParameterBufferAV1.matrix_coefficients", unsafe.Offsetof(pp.MatrixCoefficients), 3},
		{"VADecPictureParameterBufferAV1.seq_info_fields", unsafe.Offsetof(pp.SeqInfoFields), 4},
		{"VADecPictureParameterBufferAV1.current_frame", unsafe.Offsetof(pp.CurrentFrame), 8},
		{"VADecPictureParameterBufferAV1.current_display_picture", unsafe.Offsetof(pp.CurrentDisplayPicture), 12},
		{"VADecPictureParameterBufferAV1.anchor_frames_num", unsafe.Offsetof(pp.AnchorFramesNum), 16},
		{"VADecPictureParameterBufferAV1.anchor_frames_list", unsafe.Offsetof(pp.AnchorFramesList), 24},
		{"VADecPictureParameterBufferAV1.frame_width_minus1", unsafe.Offsetof(pp.FrameWidthMinus1), 32},
		{"VADecPictureParameterBufferAV1.output_frame_height_in_tiles_minus_1", unsafe.Offsetof(pp.OutputFrameHeightInTilesMinus1), 38},
		{"VADecPictureParameterBufferAV1.ref_frame_map", unsafe.Offsetof(pp.RefFrameMap), 40},
		{"VADecPictureParameterBufferAV1.ref_frame_idx", unsafe.Offsetof(pp.RefFrameIdx), 72},
		{"VADecPictureParameterBufferAV1.primary_ref_frame", unsafe.Offsetof(pp.PrimaryRefFrame), 79},
		{"VADecPictureParameterBufferAV1.order_hint", unsafe.Offsetof(pp.OrderHint), 80},
		{"VADecPictureParameterBufferAV1.seg_info", unsafe.Offsetof(pp.SegInfo), 84},
		{"VADecPictureParameterBufferAV1.film_grain_info", unsafe.Offsetof(pp.FilmGrainInfo), 240},
		{"VADecPictureParameterBufferAV1.tile_cols", unsafe.Offsetof(pp.TileCols), 416},
		{"VADecPictureParameterBufferAV1.tile_rows", unsafe.Offsetof(pp.TileRows), 417},
		{"VADecPictureParameterBufferAV1.width_in_sbs_minus_1", unsafe.Offsetof(pp.WidthInSbsMinus1), 418},
		{"VADecPictureParameterBufferAV1.height_in_sbs_minus_1", unsafe.Offsetof(pp.HeightInSbsMinus1), 544},
		{"VADecPictureParameterBufferAV1.tile_count_minus_1", unsafe.Offsetof(pp.TileCountMinus1), 670},
		{"VADecPictureParameterBufferAV1.context_update_tile_id", unsafe.Offsetof(pp.ContextUpdateTileID), 672},
		{"VADecPictureParameterBufferAV1.pic_info_fields", unsafe.Offsetof(pp.PicInfoFields), 676},
		{"VADecPictureParameterBufferAV1.superres_scale_denominator", unsafe.Offsetof(pp.SuperresScaleDenominator), 680},
		{"VADecPictureParameterBufferAV1.interp_filter", unsafe.Offsetof(pp.InterpFilter), 681},
		{"VADecPictureParameterBufferAV1.filter_level", unsafe.Offsetof(pp.FilterLevel), 682},
		{"VADecPictureParameterBufferAV1.filter_level_u", unsafe.Offsetof(pp.FilterLevelU), 684},
		{"VADecPictureParameterBufferAV1.loop_filter_info_fields", unsafe.Offsetof(pp.LoopFilterInfoFields), 686},
		{"VADecPictureParameterBufferAV1.ref_deltas", unsafe.Offsetof(pp.RefDeltas), 687},
		{"VADecPictureParameterBufferAV1.mode_deltas", unsafe.Offsetof(pp.ModeDeltas), 695},
		{"VADecPictureParameterBufferAV1.base_qindex", unsafe.Offsetof(pp.BaseQIndex), 697},
		{"VADecPictureParameterBufferAV1.y_dc_delta_q", unsafe.Offsetof(pp.YDcDeltaQ), 698},
		{"VADecPictureParameterBufferAV1.v_ac_delta_q", unsafe.Offsetof(pp.VAcDeltaQ), 702},
		{"VADecPictureParameterBufferAV1.qmatrix_fields", unsafe.Offsetof(pp.QMatrixFields), 704},
		{"VADecPictureParameterBufferAV1.mode_control_fields", unsafe.Offsetof(pp.ModeControlFields), 708},
		{"VADecPictureParameterBufferAV1.cdef_damping_minus_3", unsafe.Offsetof(pp.CDEFDampingMinus3), 712},
		{"VADecPictureParameterBufferAV1.cdef_y_strengths", unsafe.Offsetof(pp.CDEFYStrengths), 714},
		{"VADecPictureParameterBufferAV1.cdef_uv_strengths", unsafe.Offsetof(pp.CDEFUVStrengths), 722},
		{"VADecPictureParameterBufferAV1.loop_restoration_fields", unsafe.Offsetof(pp.LoopRestorationFields), 730},
		{"VADecPictureParameterBufferAV1.wm", unsafe.Offsetof(pp.WM), 732},
		{"sizeof VASliceParameterBufferAV1", unsafe.Sizeof(sp), 40},
		{"VASliceParameterBufferAV1.slice_data_flag", unsafe.Offsetof(sp.SliceDataFlag), 8},
		{"VASliceParameterBufferAV1.tile_row", unsafe.Offsetof(sp.TileRow), 12},
		{"VASliceParameterBufferAV1.tile_column", unsafe.Offsetof(sp.TileColumn), 14},
		{"VASliceParameterBufferAV1.tg_start", unsafe.Offsetof(sp.TgStart), 16},
		{"VASliceParameterBufferAV1.tg_end", unsafe.Offsetof(sp.TgEnd), 18},
		{"VASliceParameterBufferAV1.anchor_frame_idx", unsafe.Offsetof(sp.AnchorFrameIdx), 20},
		{"VASliceParameterBufferAV1.tile_idx_in_tile_list", unsafe.Offsetof(sp.TileIdxInTileList), 22},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, the C compiler says %d", c.name, c.got, c.want)
		}
	}
}
