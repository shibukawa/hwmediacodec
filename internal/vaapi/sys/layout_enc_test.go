package sys

import (
	"testing"
	"unsafe"
)

// Expected values measured with clang against va.h / va_enc_h264.h; the misc
// parameter structs carry the 4-byte VAEncMiscParameterBuffer header.
func TestEncodeStructLayouts(t *testing.T) {
	var seq EncSequenceParameterBufferH264
	var pic EncPictureParameterBufferH264
	var sl EncSliceParameterBufferH264
	var rc EncMiscParameterRateControl
	var fr EncMiscParameterFrameRate
	var hrd EncMiscParameterHRD
	var ql EncMiscParameterQualityLevel
	var ph EncPackedHeaderParameterBuffer
	var cs CodedBufferSegment

	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"sizeof VAEncSequenceParameterBufferH264", unsafe.Sizeof(seq), 1132},
		{"intra_period", unsafe.Offsetof(seq.IntraPeriod), 4},
		{"bits_per_second", unsafe.Offsetof(seq.BitsPerSecond), 16},
		{"max_num_ref_frames", unsafe.Offsetof(seq.MaxNumRefFrames), 20},
		{"picture_width_in_mbs", unsafe.Offsetof(seq.PictureWidthInMbs), 24},
		{"picture_height_in_mbs", unsafe.Offsetof(seq.PictureHeightInMbs), 26},
		{"seq_fields", unsafe.Offsetof(seq.SeqFields), 28},
		{"bit_depth_luma_minus8", unsafe.Offsetof(seq.BitDepthLumaMinus8), 32},
		{"num_ref_frames_in_pic_order_cnt_cycle", unsafe.Offsetof(seq.NumRefFramesInPicOrderCntCycle), 34},
		{"offset_for_non_ref_pic", unsafe.Offsetof(seq.OffsetForNonRefPic), 36},
		{"offset_for_ref_frame", unsafe.Offsetof(seq.OffsetForRefFrame), 44},
		{"frame_cropping_flag", unsafe.Offsetof(seq.FrameCroppingFlag), 1068},
		{"frame_crop_left_offset", unsafe.Offsetof(seq.FrameCropLeftOffset), 1072},
		{"frame_crop_bottom_offset", unsafe.Offsetof(seq.FrameCropBottomOffset), 1084},
		{"vui_parameters_present_flag", unsafe.Offsetof(seq.VUIParametersPresentFlag), 1088},
		{"vui_fields", unsafe.Offsetof(seq.VUIFields), 1092},
		{"aspect_ratio_idc", unsafe.Offsetof(seq.AspectRatioIDC), 1096},
		{"sar_width", unsafe.Offsetof(seq.SarWidth), 1100},
		{"num_units_in_tick", unsafe.Offsetof(seq.NumUnitsInTick), 1108},
		{"time_scale", unsafe.Offsetof(seq.TimeScale), 1112},

		{"sizeof VAEncPictureParameterBufferH264", unsafe.Sizeof(pic), 648},
		{"coded_buf", unsafe.Offsetof(pic.CodedBuf), 612},
		{"pic_parameter_set_id", unsafe.Offsetof(pic.PicParameterSetID), 616},
		{"last_picture", unsafe.Offsetof(pic.LastPicture), 618},
		{"frame_num", unsafe.Offsetof(pic.FrameNum), 620},
		{"pic_init_qp", unsafe.Offsetof(pic.PicInitQp), 622},
		{"num_ref_idx_l0_active_minus1", unsafe.Offsetof(pic.NumRefIdxL0ActiveMinus1), 623},
		{"second_chroma_qp_index_offset", unsafe.Offsetof(pic.SecondChromaQpIndexOffset), 626},
		{"pic_fields", unsafe.Offsetof(pic.PicFields), 628},

		{"sizeof VAEncSliceParameterBufferH264", unsafe.Sizeof(sl), 3140},
		{"macroblock_info", unsafe.Offsetof(sl.MacroblockInfo), 8},
		{"slice_type", unsafe.Offsetof(sl.SliceType), 12},
		{"idr_pic_id", unsafe.Offsetof(sl.IdrPicID), 14},
		{"pic_order_cnt_lsb", unsafe.Offsetof(sl.PicOrderCntLsb), 16},
		{"delta_pic_order_cnt_bottom", unsafe.Offsetof(sl.DeltaPicOrderCntBottom), 20},
		{"delta_pic_order_cnt", unsafe.Offsetof(sl.DeltaPicOrderCnt), 24},
		{"direct_spatial_mv_pred_flag", unsafe.Offsetof(sl.DirectSpatialMvPredFlag), 32},
		{"RefPicList0", unsafe.Offsetof(sl.RefPicList0), 36},
		{"RefPicList1", unsafe.Offsetof(sl.RefPicList1), 1188},
		{"luma_log2_weight_denom", unsafe.Offsetof(sl.LumaLog2WeightDenom), 2340},
		{"luma_weight_l0", unsafe.Offsetof(sl.LumaWeightL0), 2344},
		{"chroma_weight_l0", unsafe.Offsetof(sl.ChromaWeightL0), 2474},
		{"luma_weight_l1_flag", unsafe.Offsetof(sl.LumaWeightL1Flag), 2730},
		{"chroma_offset_l1", unsafe.Offsetof(sl.ChromaOffsetL1), 2990},
		{"cabac_init_idc", unsafe.Offsetof(sl.CabacInitIdc), 3118},
		{"slice_qp_delta", unsafe.Offsetof(sl.SliceQpDelta), 3119},
		{"slice_beta_offset_div2", unsafe.Offsetof(sl.SliceBetaOffsetDiv2), 3122},

		{"sizeof misc+VAEncMiscParameterRateControl", unsafe.Sizeof(rc), 4 + 60},
		{"rc.bits_per_second", unsafe.Offsetof(rc.BitsPerSecond), 4},
		{"rc.rc_flags", unsafe.Offsetof(rc.RcFlags), 4 + 24},
		{"rc.target_frame_size", unsafe.Offsetof(rc.TargetFrameSize), 4 + 40},
		{"sizeof misc+VAEncMiscParameterFrameRate", unsafe.Sizeof(fr), 4 + 24},
		{"sizeof misc+VAEncMiscParameterHRD", unsafe.Sizeof(hrd), 4 + 24},
		{"hrd.buffer_size", unsafe.Offsetof(hrd.BufferSize), 4 + 4},
		{"sizeof misc+VAEncMiscParameterBufferQualityLevel", unsafe.Sizeof(ql), 4 + 20},
		{"sizeof VAEncPackedHeaderParameterBuffer", unsafe.Sizeof(ph), 28},
		{"packed.bit_length", unsafe.Offsetof(ph.BitLength), 4},
		{"packed.has_emulation_bytes", unsafe.Offsetof(ph.HasEmulationBytes), 8},
		{"sizeof VACodedBufferSegment", unsafe.Sizeof(cs), 48},
		{"segment.status", unsafe.Offsetof(cs.Status), 8},
		{"segment.buf", unsafe.Offsetof(cs.Buf), 16},
		{"segment.next", unsafe.Offsetof(cs.Next), 24},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d want %d", c.name, c.got, c.want)
		}
	}
}
