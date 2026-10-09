package sys

import (
	"testing"
	"unsafe"
)

// The expected values were printed by a C program compiled against the
// libva headers (offsetof/sizeof); they are identical on x86-64 and arm64.
func TestStructLayouts(t *testing.T) {
	var pic PictureH264
	var pp PictureParameterBufferH264
	var iq IQMatrixBufferH264
	var sp SliceParameterBufferH264
	var ca ConfigAttrib
	var gv GenericValue
	var sa SurfaceAttrib
	var imf ImageFormat
	var im Image

	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"sizeof VAPictureH264", unsafe.Sizeof(pic), 36},
		{"VAPictureH264.frame_idx", unsafe.Offsetof(pic.FrameIdx), 4},
		{"VAPictureH264.flags", unsafe.Offsetof(pic.Flags), 8},
		{"VAPictureH264.TopFieldOrderCnt", unsafe.Offsetof(pic.TopFieldOrderCnt), 12},
		{"VAPictureH264.BottomFieldOrderCnt", unsafe.Offsetof(pic.BottomFieldOrderCnt), 16},

		{"sizeof VAPictureParameterBufferH264", unsafe.Sizeof(pp), 672},
		{"ReferenceFrames", unsafe.Offsetof(pp.ReferenceFrames), 36},
		{"picture_width_in_mbs_minus1", unsafe.Offsetof(pp.PictureWidthInMbsMinus1), 612},
		{"picture_height_in_mbs_minus1", unsafe.Offsetof(pp.PictureHeightInMbsMinus1), 614},
		{"bit_depth_luma_minus8", unsafe.Offsetof(pp.BitDepthLumaMinus8), 616},
		{"bit_depth_chroma_minus8", unsafe.Offsetof(pp.BitDepthChromaMinus8), 617},
		{"num_ref_frames", unsafe.Offsetof(pp.NumRefFrames), 618},
		{"seq_fields", unsafe.Offsetof(pp.SeqFields), 620},
		{"num_slice_groups_minus1", unsafe.Offsetof(pp.NumSliceGroupsMinus1), 624},
		{"slice_group_map_type", unsafe.Offsetof(pp.SliceGroupMapType), 625},
		{"slice_group_change_rate_minus1", unsafe.Offsetof(pp.SliceGroupChangeRateMinus1), 626},
		{"pic_init_qp_minus26", unsafe.Offsetof(pp.PicInitQpMinus26), 628},
		{"pic_init_qs_minus26", unsafe.Offsetof(pp.PicInitQsMinus26), 629},
		{"chroma_qp_index_offset", unsafe.Offsetof(pp.ChromaQpIndexOffset), 630},
		{"second_chroma_qp_index_offset", unsafe.Offsetof(pp.SecondChromaQpIndexOffset), 631},
		{"pic_fields", unsafe.Offsetof(pp.PicFields), 632},
		{"frame_num", unsafe.Offsetof(pp.FrameNum), 636},

		{"sizeof VAIQMatrixBufferH264", unsafe.Sizeof(iq), 240},
		{"ScalingList8x8", unsafe.Offsetof(iq.ScalingList8x8), 96},

		{"sizeof VASliceParameterBufferH264", unsafe.Sizeof(sp), 3128},
		{"slice_data_bit_offset", unsafe.Offsetof(sp.SliceDataBitOffset), 12},
		{"first_mb_in_slice", unsafe.Offsetof(sp.FirstMbInSlice), 14},
		{"slice_type", unsafe.Offsetof(sp.SliceType), 16},
		{"direct_spatial_mv_pred_flag", unsafe.Offsetof(sp.DirectSpatialMvPredFlag), 17},
		{"num_ref_idx_l0_active_minus1", unsafe.Offsetof(sp.NumRefIdxL0ActiveMinus1), 18},
		{"num_ref_idx_l1_active_minus1", unsafe.Offsetof(sp.NumRefIdxL1ActiveMinus1), 19},
		{"cabac_init_idc", unsafe.Offsetof(sp.CabacInitIdc), 20},
		{"slice_qp_delta", unsafe.Offsetof(sp.SliceQpDelta), 21},
		{"disable_deblocking_filter_idc", unsafe.Offsetof(sp.DisableDeblockingFilterIdc), 22},
		{"slice_alpha_c0_offset_div2", unsafe.Offsetof(sp.SliceAlphaC0OffsetDiv2), 23},
		{"slice_beta_offset_div2", unsafe.Offsetof(sp.SliceBetaOffsetDiv2), 24},
		{"RefPicList0", unsafe.Offsetof(sp.RefPicList0), 28},
		{"RefPicList1", unsafe.Offsetof(sp.RefPicList1), 1180},
		{"luma_log2_weight_denom", unsafe.Offsetof(sp.LumaLog2WeightDenom), 2332},
		{"chroma_log2_weight_denom", unsafe.Offsetof(sp.ChromaLog2WeightDenom), 2333},
		{"luma_weight_l0_flag", unsafe.Offsetof(sp.LumaWeightL0Flag), 2334},
		{"luma_weight_l0", unsafe.Offsetof(sp.LumaWeightL0), 2336},
		{"luma_offset_l0", unsafe.Offsetof(sp.LumaOffsetL0), 2400},
		{"chroma_weight_l0_flag", unsafe.Offsetof(sp.ChromaWeightL0Flag), 2464},
		{"chroma_weight_l0", unsafe.Offsetof(sp.ChromaWeightL0), 2466},
		{"chroma_offset_l0", unsafe.Offsetof(sp.ChromaOffsetL0), 2594},
		{"luma_weight_l1_flag", unsafe.Offsetof(sp.LumaWeightL1Flag), 2722},
		{"luma_weight_l1", unsafe.Offsetof(sp.LumaWeightL1), 2724},
		{"luma_offset_l1", unsafe.Offsetof(sp.LumaOffsetL1), 2788},
		{"chroma_weight_l1_flag", unsafe.Offsetof(sp.ChromaWeightL1Flag), 2852},
		{"chroma_weight_l1", unsafe.Offsetof(sp.ChromaWeightL1), 2854},
		{"chroma_offset_l1", unsafe.Offsetof(sp.ChromaOffsetL1), 2982},

		{"sizeof VAConfigAttrib", unsafe.Sizeof(ca), 8},
		{"VAConfigAttrib.value", unsafe.Offsetof(ca.Value), 4},
		{"sizeof VAGenericValue", unsafe.Sizeof(gv), 16},
		{"VAGenericValue.value", unsafe.Offsetof(gv.Value), 8},
		{"sizeof VASurfaceAttrib", unsafe.Sizeof(sa), 24},
		{"VASurfaceAttrib.flags", unsafe.Offsetof(sa.Flags), 4},
		{"VASurfaceAttrib.value", unsafe.Offsetof(sa.Value), 8},

		{"sizeof VAImageFormat", unsafe.Sizeof(imf), 48},
		{"sizeof VAImage", unsafe.Sizeof(im), 120},
		{"VAImage.format", unsafe.Offsetof(im.Format), 4},
		{"VAImage.buf", unsafe.Offsetof(im.Buf), 52},
		{"VAImage.width", unsafe.Offsetof(im.Width), 56},
		{"VAImage.height", unsafe.Offsetof(im.Height), 58},
		{"VAImage.data_size", unsafe.Offsetof(im.DataSize), 60},
		{"VAImage.num_planes", unsafe.Offsetof(im.NumPlanes), 64},
		{"VAImage.pitches", unsafe.Offsetof(im.Pitches), 68},
		{"VAImage.offsets", unsafe.Offsetof(im.Offsets), 80},
		{"VAImage.num_palette_entries", unsafe.Offsetof(im.NumPaletteEntries), 92},
		{"VAImage.entry_bytes", unsafe.Offsetof(im.EntryBytes), 96},
		{"VAImage.component_order", unsafe.Offsetof(im.ComponentOrder), 100},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d want %d", c.name, c.got, c.want)
		}
	}
}
