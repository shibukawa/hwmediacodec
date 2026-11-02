//go:build linux && !android

package vaapi

import (
	"math/bits"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// AV1 limits of Annex A and Section 5.9.15 used to lay out the tiles.
const (
	av1MaxTileWidth = 4096
	av1MaxTileArea  = 4096 * 2304
	av1MaxTileCols  = 64
	av1MaxTileRows  = 64
	// av1SbSizeLog2 is the 64x64 superblock this encoder uses.
	av1SbSizeLog2 = 6
	// av1OrderHintBits is the width of order_hint in the frame headers.
	av1OrderHintBits = 8
	// av1PrimaryRefNone is PRIMARY_REF_NONE.
	av1PrimaryRefNone = 7
	// tx_mode values of the frame header (Section 6.8.21).
	av1TxModeLargest = 1
	av1TxModeSelect  = 2
)

// av1Stream holds the sequence-level choices of an AV1 stream: an 8-bit
// 4:2:0 Main profile stream with one operating point, order hints and,
// optionally, CDEF; every other tool of the sequence header is off.
type av1Stream struct {
	width, height int
	level         uint8 // seq_level_idx; 31 when no level fits
	bt709         bool
	enableCDEF    bool
	txMode        uint8
	// obuSizeBytes is the length the size field of the frame header OBU
	// is written in, as the driver asks for (VAConfigAttribEncAV1Ext2).
	obuSizeBytes  int
	tileSizeBytes int

	sbCols, sbRows             int
	tileColsLog2, tileRowsLog2 int
	maxLog2TileCols            int
	maxLog2TileRows            int
	tileCols, tileRows         int
	tileWidthSb, tileHeightSb  int
}

// av1TileLog2 is tile_log2 of Section 5.9.15: the smallest k with
// blkSize << k >= target.
func av1TileLog2(blkSize, target int) int {
	k := 0
	for blkSize<<k < target {
		k++
	}
	return k
}

// newAV1Stream lays out a stream of the given picture size. Tiles are
// uniformly spaced and as few as the limits of Section 5.9.15 allow.
func newAV1Stream(width, height int, level uint8) *av1Stream {
	s := &av1Stream{width: width, height: height, level: level, txMode: av1TxModeSelect, obuSizeBytes: 1, tileSizeBytes: 4}
	miCols := 2 * ((width + 7) >> 3)
	miRows := 2 * ((height + 7) >> 3)
	s.sbCols = (miCols + 15) >> 4
	s.sbRows = (miRows + 15) >> 4
	maxTileWidthSb := av1MaxTileWidth >> av1SbSizeLog2
	maxTileAreaSb := av1MaxTileArea >> (2 * av1SbSizeLog2)
	minLog2TileCols := av1TileLog2(maxTileWidthSb, s.sbCols)
	s.maxLog2TileCols = av1TileLog2(1, min(s.sbCols, av1MaxTileCols))
	s.maxLog2TileRows = av1TileLog2(1, min(s.sbRows, av1MaxTileRows))
	minLog2Tiles := max(minLog2TileCols, av1TileLog2(maxTileAreaSb, s.sbRows*s.sbCols))

	s.tileColsLog2 = minLog2TileCols
	s.tileWidthSb = (s.sbCols + (1 << s.tileColsLog2) - 1) >> s.tileColsLog2
	s.tileCols = (s.sbCols + s.tileWidthSb - 1) / s.tileWidthSb
	s.tileRowsLog2 = max(minLog2Tiles-s.tileColsLog2, 0)
	s.tileHeightSb = (s.sbRows + (1 << s.tileRowsLog2) - 1) >> s.tileRowsLog2
	s.tileRows = (s.sbRows + s.tileHeightSb - 1) / s.tileHeightSb
	return s
}

// numTiles returns the number of tiles of every frame.
func (s *av1Stream) numTiles() int { return s.tileCols * s.tileRows }

// tileSizes returns width_in_sbs_minus_1 and height_in_sbs_minus_1 of the
// tile layout.
func (s *av1Stream) tileSizes() (cols, rows []uint16) {
	for i := range s.tileCols {
		w := s.tileWidthSb
		if i == s.tileCols-1 {
			w = s.sbCols - i*s.tileWidthSb
		}
		cols = append(cols, uint16(w-1))
	}
	for i := range s.tileRows {
		h := s.tileHeightSb
		if i == s.tileRows-1 {
			h = s.sbRows - i*s.tileHeightSb
		}
		rows = append(rows, uint16(h-1))
	}
	return cols, rows
}

// av1Level describes one level of Table A.1 (main tier).
type av1Level struct {
	idx         uint8
	maxPicSize  int
	maxH, maxV  int
	maxDispRate int64 // luma samples per second
	maxBitrate  int64 // bits per second, main tier
}

var av1Levels = []av1Level{
	{0, 147456, 2048, 1152, 4423680, 1_500_000},
	{1, 278784, 2816, 1584, 8363520, 3_000_000},
	{4, 665856, 4352, 2448, 19975680, 6_000_000},
	{5, 1065024, 5504, 3096, 31950720, 10_000_000},
	{8, 2359296, 6144, 3456, 70778880, 12_000_000},
	{9, 2359296, 6144, 3456, 141557760, 20_000_000},
	{12, 8912896, 8192, 4352, 267386880, 30_000_000},
	{13, 8912896, 8192, 4352, 534773760, 40_000_000},
	{14, 8912896, 8192, 4352, 1069547520, 60_000_000},
	{16, 35651584, 16384, 8704, 1069547520, 60_000_000},
	{17, 35651584, 16384, 8704, 2139095040, 100_000_000},
	{18, 35651584, 16384, 8704, 4278190080, 160_000_000},
}

// av1LevelIdx picks the lowest main-tier level that fits the picture
// size, the sample rate (when fps is known) and the bitrate (when one is
// set); 31, the unconstrained level, when none does.
func av1LevelIdx(width, height int, fps float64, bitrate int64) uint8 {
	for _, l := range av1Levels {
		if width*height > l.maxPicSize || width > l.maxH || height > l.maxV {
			continue
		}
		if fps > 0 && int64(float64(width*height)*fps) > l.maxDispRate {
			continue
		}
		if bitrate > l.maxBitrate {
			continue
		}
		return l.idx
	}
	return 31
}

// appendAV1OBU appends an OBU header with obu_has_size_field set and a
// size field of sizeBytes bytes (a padded leb128 when sizeBytes is larger
// than the value needs; 0, or a length too short for the value, means the
// shortest form).
func appendAV1OBU(dst []byte, t av1.OBUType, payload []byte, sizeBytes int) []byte {
	dst = append(dst, byte(t)<<3|0x02)
	if sizeBytes == 0 || len(payload) >= 1<<(7*sizeBytes) {
		dst = av1.AppendLEB128(dst, uint64(len(payload)))
	} else {
		v := len(payload)
		for i := range sizeBytes {
			b := byte(v & 0x7f)
			v >>= 7
			if i < sizeBytes-1 {
				b |= 0x80
			}
			dst = append(dst, b)
		}
	}
	return append(dst, payload...)
}

// sequenceHeaderOBU returns the sequence header OBU of the stream.
func (s *av1Stream) sequenceHeaderOBU() []byte {
	w := bitstream.NewWriter()
	w.WriteBits(0, 3) // seq_profile: Main
	w.WriteBit(0)     // still_picture
	w.WriteBit(0)     // reduced_still_picture_header
	w.WriteBit(0)     // timing_info_present_flag
	w.WriteBit(0)     // initial_display_delay_present_flag
	w.WriteBits(0, 5) // operating_points_cnt_minus_1
	w.WriteBits(0, 12)
	w.WriteBits(uint64(s.level), 5)
	if s.level > 7 {
		w.WriteBit(0) // seq_tier: main
	}
	wBits := max(bits.Len(uint(s.width-1)), 1)
	hBits := max(bits.Len(uint(s.height-1)), 1)
	w.WriteBits(uint64(wBits-1), 4)
	w.WriteBits(uint64(hBits-1), 4)
	w.WriteBits(uint64(s.width-1), wBits)
	w.WriteBits(uint64(s.height-1), hBits)
	w.WriteBit(0) // frame_id_numbers_present_flag
	w.WriteBit(0) // use_128x128_superblock
	w.WriteBit(0) // enable_filter_intra
	w.WriteBit(0) // enable_intra_edge_filter
	w.WriteBit(0) // enable_interintra_compound
	w.WriteBit(0) // enable_masked_compound
	w.WriteBit(0) // enable_warped_motion
	w.WriteBit(0) // enable_dual_filter
	w.WriteBit(1) // enable_order_hint
	w.WriteBit(0) // enable_jnt_comp
	w.WriteBit(0) // enable_ref_frame_mvs
	w.WriteBit(0) // seq_choose_screen_content_tools
	w.WriteBit(0) // seq_force_screen_content_tools
	w.WriteBits(av1OrderHintBits-1, 3)
	w.WriteBit(0) // enable_superres
	w.WriteFlag(s.enableCDEF)
	w.WriteBit(0) // enable_restoration
	// color_config
	w.WriteBit(0) // high_bitdepth
	w.WriteBit(0) // mono_chrome
	w.WriteFlag(s.bt709)
	if s.bt709 {
		w.WriteBits(colourBT709, 8) // color_primaries
		w.WriteBits(colourBT709, 8) // transfer_characteristics
		w.WriteBits(colourBT709, 8) // matrix_coefficients
	}
	w.WriteBit(0)     // color_range: limited
	w.WriteBits(0, 2) // chroma_sample_position: unknown
	w.WriteBit(0)     // separate_uv_delta_q
	w.WriteBit(0)     // film_grain_params_present
	w.Trailing()
	return appendAV1OBU(nil, av1.OBUSequenceHeader, w.Bytes(), 0)
}

// av1Frame describes one frame: a shown key frame or a shown inter frame
// that predicts from one reference slot only.
type av1Frame struct {
	key          bool
	orderHint    uint8
	refreshFlags uint8
	refSlot      uint8 // the slot every ref_frame_idx names (inter frames)
	qindex       uint8
}

// av1FrameHeaderBits holds the positions in a frame header OBU that a
// driver rewrites under rate control, in bits from the start of the OBU.
type av1FrameHeaderBits struct {
	qindex, segmentation, loopFilter int
	cdef, cdefSize                   int
	total                            int
}

// frameHeaderOBU returns the frame header OBU of a frame and the bit
// positions of its rate-controlled fields.
func (s *av1Stream) frameHeaderOBU(f *av1Frame) ([]byte, av1FrameHeaderBits) {
	var pos av1FrameHeaderBits
	w := bitstream.NewWriter()
	w.WriteBit(0) // show_existing_frame
	if f.key {
		w.WriteBits(uint64(av1.KeyFrame), 2)
	} else {
		w.WriteBits(uint64(av1.InterFrame), 2)
	}
	w.WriteBit(1) // show_frame
	if !f.key {
		w.WriteBit(0) // error_resilient_mode (implied 1 for shown key frames)
	}
	w.WriteBit(0) // disable_cdf_update
	w.WriteBit(0) // frame_size_override_flag
	w.WriteBits(uint64(f.orderHint), av1OrderHintBits)
	if !f.key {
		w.WriteBits(0, 3) // primary_ref_frame: LAST_FRAME's slot
		w.WriteBits(uint64(f.refreshFlags), 8)
		w.WriteBit(0) // frame_refs_short_signaling
		for range av1.RefsPerFrame {
			w.WriteBits(uint64(f.refSlot), 3)
		}
	}
	w.WriteBit(0) // render_and_frame_size_different
	if !f.key {
		w.WriteBit(0)     // allow_high_precision_mv
		w.WriteBit(0)     // is_filter_switchable
		w.WriteBits(0, 2) // interpolation_filter: EIGHTTAP
		w.WriteBit(0)     // is_motion_mode_switchable
	}
	w.WriteBit(0) // disable_frame_end_update_cdf

	// tile_info
	w.WriteBit(1) // uniform_tile_spacing_flag
	if s.tileColsLog2 < s.maxLog2TileCols {
		w.WriteBit(0) // increment_tile_cols_log2
	}
	if s.tileRowsLog2 < s.maxLog2TileRows {
		w.WriteBit(0) // increment_tile_rows_log2
	}
	if s.tileColsLog2 > 0 || s.tileRowsLog2 > 0 {
		w.WriteBits(0, s.tileColsLog2+s.tileRowsLog2) // context_update_tile_id
		w.WriteBits(uint64(s.tileSizeBytes-1), 2)
	}

	// quantization_params
	pos.qindex = w.Len()
	w.WriteBits(uint64(f.qindex), 8)
	w.WriteBit(0) // DeltaQYDc: delta_coded
	w.WriteBit(0) // DeltaQUDc
	w.WriteBit(0) // DeltaQUAc
	w.WriteBit(0) // using_qmatrix

	pos.segmentation = w.Len()
	w.WriteBit(0) // segmentation_enabled
	if f.qindex > 0 {
		w.WriteBit(0) // delta_q_present
	}

	// loop_filter_params; base_q_idx > 0 keeps the frame lossy.
	pos.loopFilter = w.Len()
	w.WriteBits(0, 6) // loop_filter_level[0]
	w.WriteBits(0, 6) // loop_filter_level[1]
	w.WriteBits(0, 3) // loop_filter_sharpness
	w.WriteBit(0)     // loop_filter_delta_enabled

	pos.cdef = w.Len()
	if s.enableCDEF {
		w.WriteBits(0, 2) // cdef_damping_minus_3
		w.WriteBits(0, 2) // cdef_bits
		w.WriteBits(0, 4) // cdef_y_pri_strength[0]
		w.WriteBits(0, 2) // cdef_y_sec_strength[0]
		w.WriteBits(0, 4) // cdef_uv_pri_strength[0]
		w.WriteBits(0, 2) // cdef_uv_sec_strength[0]
	}
	pos.cdefSize = w.Len() - pos.cdef

	w.WriteFlag(s.txMode == av1TxModeSelect) // tx_mode_select
	if !f.key {
		w.WriteBit(0) // reference_select
	}
	w.WriteBit(0) // reduced_tx_set
	if !f.key {
		for range av1.RefsPerFrame {
			w.WriteBit(0) // is_global
		}
	}
	w.Trailing()

	payload := w.Bytes()
	obu := appendAV1OBU(nil, av1.OBUFrameHeader, payload, s.obuSizeBytes)
	head := 8 * (len(obu) - len(payload))
	pos.qindex += head
	pos.segmentation += head
	pos.loopFilter += head
	pos.cdef += head
	pos.total = 8 * len(obu)
	return obu, pos
}
