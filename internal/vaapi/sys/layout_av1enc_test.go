package sys

import (
	"testing"
	"unsafe"
)

// Expected values measured with gcc against va_enc_av1.h (libva 2.23) on
// x86-64; the structs hold no pointers, so arm64 is the same.
func TestAV1EncodeStructLayouts(t *testing.T) {
	var seq EncSequenceParameterBufferAV1
	var pic EncPictureParameterBufferAV1
	var seg EncSegParamAV1
	var wm EncWarpedMotionParamsAV1
	var tg EncTileGroupBufferAV1

	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"sizeof VAEncSequenceParameterBufferAV1", unsafe.Sizeof(seq), 88},
		{"intra_period", unsafe.Offsetof(seq.IntraPeriod), 4},
		{"bits_per_second", unsafe.Offsetof(seq.BitsPerSecond), 12},
		{"seq_fields", unsafe.Offsetof(seq.SeqFields), 16},
		{"order_hint_bits_minus_1", unsafe.Offsetof(seq.OrderHintBitsMinus1), 20},
		{"sizeof VAEncSegParamAV1", unsafe.Sizeof(seg), 156},
		{"VAEncSegParamAV1.feature_data", unsafe.Offsetof(seg.FeatureData), 2},
		{"VAEncSegParamAV1.feature_mask", unsafe.Offsetof(seg.FeatureMask), 130},
		{"sizeof VAEncWarpedMotionParamsAV1", unsafe.Sizeof(wm), 56},
		{"sizeof VAEncTileGroupBufferAV1", unsafe.Sizeof(tg), 20},
		{"sizeof VAEncPictureParameterBufferAV1", unsafe.Sizeof(pic), 1032},
		{"reconstructed_frame", unsafe.Offsetof(pic.ReconstructedFrame), 4},
		{"coded_buf", unsafe.Offsetof(pic.CodedBuf), 8},
		{"reference_frames", unsafe.Offsetof(pic.ReferenceFrames), 12},
		{"ref_frame_idx", unsafe.Offsetof(pic.RefFrameIdx), 44},
		{"refresh_frame_flags", unsafe.Offsetof(pic.RefreshFrameFlags), 54},
		{"ref_frame_ctrl_l0", unsafe.Offsetof(pic.RefFrameCtrlL0), 56},
		{"picture_flags", unsafe.Offsetof(pic.PictureFlags), 64},
		{"seg_id_block_size", unsafe.Offsetof(pic.SegIDBlockSize), 68},
		{"filter_level", unsafe.Offsetof(pic.FilterLevel), 71},
		{"loop_filter_flags", unsafe.Offsetof(pic.LoopFilterFlags), 75},
		{"ref_deltas", unsafe.Offsetof(pic.RefDeltas), 78},
		{"base_qindex", unsafe.Offsetof(pic.BaseQIndex), 88},
		{"max_base_qindex", unsafe.Offsetof(pic.MaxBaseQIndex), 95},
		{"qmatrix_flags", unsafe.Offsetof(pic.QMatrixFlags), 96},
		{"mode_control_flags", unsafe.Offsetof(pic.ModeControlFlags), 100},
		{"segments", unsafe.Offsetof(pic.Segments), 104},
		{"tile_cols", unsafe.Offsetof(pic.TileCols), 260},
		{"width_in_sbs_minus_1", unsafe.Offsetof(pic.WidthInSbsMinus1), 264},
		{"height_in_sbs_minus_1", unsafe.Offsetof(pic.HeightInSbsMinus1), 390},
		{"context_update_tile_id", unsafe.Offsetof(pic.ContextUpdateTileID), 516},
		{"cdef_damping_minus_3", unsafe.Offsetof(pic.CDEFDampingMinus3), 518},
		{"cdef_y_strengths", unsafe.Offsetof(pic.CDEFYStrengths), 520},
		{"loop_restoration_flags", unsafe.Offsetof(pic.LoopRestorationFlags), 536},
		{"wm", unsafe.Offsetof(pic.WM), 540},
		{"bit_offset_qindex", unsafe.Offsetof(pic.BitOffsetQIndex), 932},
		{"size_in_bits_frame_hdr_obu", unsafe.Offsetof(pic.SizeInBitsFrameHdrOBU), 956},
		{"tile_group_obu_hdr_info", unsafe.Offsetof(pic.TileGroupOBUHdrInfo), 960},
		{"skip_frames_reduced_size", unsafe.Offsetof(pic.SkipFramesReducedSize), 964},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
