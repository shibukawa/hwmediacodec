package sys

// SegmentationAV1 mirrors VASegmentationStructAV1.
type SegmentationAV1 struct {
	// SegmentInfoFields packs enabled (bit 0), update_map (1),
	// temporal_update (2) and update_data (3).
	SegmentInfoFields uint32
	FeatureData       [8][8]int16
	FeatureMask       [8]uint8
	_                 [4]uint32
}

// FilmGrainAV1 mirrors VAFilmGrainStructAV1.
type FilmGrainAV1 struct {
	// FilmGrainInfoFields packs apply_grain (bit 0),
	// chroma_scaling_from_luma (1), grain_scaling_minus_8 (2-3),
	// ar_coeff_lag (4-5), ar_coeff_shift_minus_6 (6-7), grain_scale_shift
	// (8-9), overlap_flag (10) and clip_to_restricted_range (11).
	FilmGrainInfoFields uint32
	GrainSeed           uint16
	NumYPoints          uint8
	PointYValue         [14]uint8
	PointYScaling       [14]uint8
	NumCbPoints         uint8
	PointCbValue        [10]uint8
	PointCbScaling      [10]uint8
	NumCrPoints         uint8
	PointCrValue        [10]uint8
	PointCrScaling      [10]uint8
	ARCoeffsY           [24]int8
	ARCoeffsCb          [25]int8
	ARCoeffsCr          [25]int8
	CbMult              uint8
	CbLumaMult          uint8
	CbOffset            uint16
	CrMult              uint8
	CrLumaMult          uint8
	CrOffset            uint16
	_                   [4]uint32
}

// WarpedMotionParamsAV1 mirrors VAWarpedMotionParamsAV1. WMType is a
// VAAV1TransformationType, whose values are those of the AV1 specification
// (0 identity, 1 translation, 2 rotzoom, 3 affine).
type WarpedMotionParamsAV1 struct {
	WMType  uint32
	WMMat   [8]int32
	Invalid uint8
	_       [4]uint32
}

// DecPictureParameterBufferAV1 mirrors VADecPictureParameterBufferAV1.
type DecPictureParameterBufferAV1 struct {
	Profile             uint8
	OrderHintBitsMinus1 uint8
	BitDepthIdx         uint8 // 0: 8-bit, 1: 10-bit, 2: 12-bit
	MatrixCoefficients  uint8
	// SeqInfoFields packs still_picture (bit 0), use_128x128_superblock
	// (1), enable_filter_intra (2), enable_intra_edge_filter (3),
	// enable_interintra_compound (4), enable_masked_compound (5),
	// enable_dual_filter (6), enable_order_hint (7), enable_jnt_comp (8),
	// enable_cdef (9), mono_chrome (10), color_range (11), subsampling_x
	// (12), subsampling_y (13), chroma_sample_position (14, deprecated) and
	// film_grain_params_present (15).
	SeqInfoFields uint32
	// CurrentFrame receives the reconstructed picture that later frames
	// reference; CurrentDisplayPicture the picture to show, which differs
	// from it only when film grain is applied.
	CurrentFrame          uint32
	CurrentDisplayPicture uint32
	AnchorFramesNum       uint8
	// AnchorFramesList is a VASurfaceID pointer used for large-scale tile
	// streams only; it stays nil.
	AnchorFramesList               uintptr
	FrameWidthMinus1               uint16 // of the upscaled frame
	FrameHeightMinus1              uint16
	OutputFrameWidthInTilesMinus1  uint16
	OutputFrameHeightInTilesMinus1 uint16
	RefFrameMap                    [8]uint32
	RefFrameIdx                    [7]uint8
	PrimaryRefFrame                uint8
	OrderHint                      uint8
	SegInfo                        SegmentationAV1
	FilmGrainInfo                  FilmGrainAV1
	TileCols                       uint8
	TileRows                       uint8
	WidthInSbsMinus1               [63]uint16
	HeightInSbsMinus1              [63]uint16
	TileCountMinus1                uint16
	ContextUpdateTileID            uint16
	// PicInfoFields packs frame_type (bits 0-1), show_frame (2),
	// showable_frame (3), error_resilient_mode (4), disable_cdf_update (5),
	// allow_screen_content_tools (6), force_integer_mv (7), allow_intrabc
	// (8), use_superres (9), allow_high_precision_mv (10),
	// is_motion_mode_switchable (11), use_ref_frame_mvs (12),
	// disable_frame_end_update_cdf (13), uniform_tile_spacing_flag (14),
	// allow_warped_motion (15) and large_scale_tile (16).
	PicInfoFields            uint32
	SuperresScaleDenominator uint8
	InterpFilter             uint8
	FilterLevel              [2]uint8
	FilterLevelU             uint8
	FilterLevelV             uint8
	// LoopFilterInfoFields packs sharpness_level (bits 0-2),
	// mode_ref_delta_enabled (3) and mode_ref_delta_update (4).
	LoopFilterInfoFields uint8
	RefDeltas            [8]int8
	ModeDeltas           [2]int8
	BaseQIndex           uint8
	YDcDeltaQ            int8
	UDcDeltaQ            int8
	UAcDeltaQ            int8
	VDcDeltaQ            int8
	VAcDeltaQ            int8
	// QMatrixFields packs using_qmatrix (bit 0), qm_y (1-4), qm_u (5-8)
	// and qm_v (9-12).
	QMatrixFields uint16
	// ModeControlFields packs delta_q_present_flag (bit 0),
	// log2_delta_q_res (1-2), delta_lf_present_flag (3), log2_delta_lf_res
	// (4-5), delta_lf_multi (6), tx_mode (7-8), reference_select (9),
	// reduced_tx_set_used (10) and skip_mode_present (11).
	ModeControlFields uint32
	CDEFDampingMinus3 uint8
	CDEFBits          uint8
	// The CDEF strengths are (primary << 2) | secondary, with the secondary
	// strength as coded (two bits).
	CDEFYStrengths  [8]uint8
	CDEFUVStrengths [8]uint8
	// LoopRestorationFields packs yframe_restoration_type (bits 0-1),
	// cbframe_restoration_type (2-3), crframe_restoration_type (4-5),
	// lr_unit_shift (6-7) and lr_uv_shift (8).
	LoopRestorationFields uint16
	// WM holds the global motion parameters of LAST_FRAME to ALTREF_FRAME.
	WM [7]WarpedMotionParamsAV1
	_  [8]uint32
}

// SliceParameterBufferAV1 mirrors VASliceParameterBufferAV1: one tile. A
// tile group is submitted as one buffer holding an element per tile,
// followed by one slice data buffer that SliceDataOffset counts into.
type SliceParameterBufferAV1 struct {
	SliceDataSize     uint32
	SliceDataOffset   uint32
	SliceDataFlag     uint32
	TileRow           uint16
	TileColumn        uint16
	TgStart           uint16 // deprecated in libva, still read by drivers
	TgEnd             uint16
	AnchorFrameIdx    uint8
	TileIdxInTileList uint16
	_                 [4]uint32
}
