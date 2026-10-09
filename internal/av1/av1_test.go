package av1_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// The sequence header payloads SVT-AV1 4.2 wrote for a 320x240 8-bit and a
// 10-bit testsrc2 stream (ffmpeg -c:v libsvtav1 -preset 10 -g 25), and the
// av1C record ffmpeg wrote for the 8-bit stream when remuxing it into MP4.
var (
	svtSeqHeader8  = []byte{0x02, 0x00, 0x00, 0x05, 0x21, 0xe7, 0xfd, 0xe2, 0x57, 0xc8, 0x02}
	svtSeqHeader10 = []byte{0x02, 0x00, 0x00, 0x05, 0x21, 0xe7, 0xfd, 0xe2, 0x57, 0xca, 0x02}
	svtAV1C8       = []byte{0x81, 0x00, 0x0c, 0x00, 0x0a, 0x0b, 0x02, 0x00, 0x00, 0x05, 0x21, 0xe7, 0xfd, 0xe2, 0x57, 0xc8, 0x02}
)

func TestParseSequenceHeaderSVT(t *testing.T) {
	for _, tc := range []struct {
		name     string
		payload  []byte
		bitDepth int
	}{{"8bit", svtSeqHeader8, 8}, {"10bit", svtSeqHeader10, 10}} {
		t.Run(tc.name, func(t *testing.T) {
			sh, err := av1.ParseSequenceHeader(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			want := &av1.SequenceHeader{
				Profile: 0, OperatingPoints: 1, LevelIdx: 0, Tier: 0,
				InitialDisplayDelayPresent: true, InitialDisplayDelay: 5,
				FrameWidthBits: 9, FrameHeightBits: 8, MaxFrameWidth: 320, MaxFrameHeight: 240,
				EnableIntraEdgeFilter: true, EnableWarpedMotion: true, EnableOrderHint: true, EnableRefFrameMVs: true,
				SeqForceScreenContentTools: av1.SelectScreenContentTools, SeqForceIntegerMV: av1.SelectIntegerMV,
				OrderHintBits: 7, EnableCDEF: true,
				BitDepth: tc.bitDepth, ColorPrimaries: 2, TransferCharacteristics: 2, MatrixCoefficients: 2,
				SubsamplingX: 1, SubsamplingY: 1,
				OperatingPointIdcs: []uint16{0}, DecoderModelPresentForOp: []bool{false},
			}
			if !reflect.DeepEqual(sh, want) {
				t.Fatalf("parsed\n%+v\nwant\n%+v", *sh, *want)
			}
		})
	}
}

// writeUVLC appends v as uvlc().
func writeUVLC(w *bitstream.Writer, v uint32) {
	lz := 0
	for (uint64(v)+1)>>uint(lz+1) != 0 {
		lz++
	}
	for i := 0; i < lz; i++ {
		w.WriteBit(0)
	}
	w.WriteBit(1)
	w.WriteBits(uint64(v)+1-1<<uint(lz), lz)
}

// TestParseSequenceHeaderSynthetic builds headers bit by bit to cover the
// branches SVT-AV1 never takes: timing and decoder model info, several
// operating points with tiers and display delays, frame id numbers,
// explicit screen content tools, Professional profile 12-bit 4:2:2 with a
// colour description, High profile sRGB 4:4:4 as a reduced still picture,
// and monochrome.
func TestParseSequenceHeaderSynthetic(t *testing.T) {
	t.Run("professional-12bit-422", func(t *testing.T) {
		w := bitstream.NewWriter()
		w.WriteBits(2, 3)  // seq_profile
		w.WriteFlag(true)  // still_picture
		w.WriteFlag(false) // reduced_still_picture_header
		w.WriteFlag(true)  // timing_info_present_flag
		w.WriteBits(1001, 32)
		w.WriteBits(60000, 32)
		w.WriteFlag(true) // equal_picture_interval
		writeUVLC(w, 5)   // num_ticks_per_picture_minus_1
		w.WriteFlag(true) // decoder_model_info_present_flag
		w.WriteBits(9, 5) // buffer_delay_length_minus_1
		w.WriteBits(1, 32)
		w.WriteBits(4, 5)
		w.WriteBits(4, 5)
		w.WriteFlag(true) // initial_display_delay_present_flag
		w.WriteBits(1, 5) // operating_points_cnt_minus_1
		// operating point 0
		w.WriteBits(0x123, 12)
		w.WriteBits(13, 5)   // seq_level_idx > 7
		w.WriteFlag(true)    // seq_tier
		w.WriteFlag(true)    // decoder_model_present_for_this_op
		w.WriteBits(100, 10) // decoder_buffer_delay
		w.WriteBits(200, 10) // encoder_buffer_delay
		w.WriteFlag(true)    // low_delay_mode_flag
		w.WriteFlag(true)    // initial_display_delay_present_for_this_op
		w.WriteBits(3, 4)    // initial_display_delay_minus_1
		// operating point 1
		w.WriteBits(0x45, 12)
		w.WriteBits(5, 5)
		w.WriteFlag(false) // decoder_model_present_for_this_op
		w.WriteFlag(false) // initial_display_delay_present_for_this_op
		w.WriteBits(11, 4) // frame_width_bits_minus_1
		w.WriteBits(10, 4) // frame_height_bits_minus_1
		w.WriteBits(1919, 12)
		w.WriteBits(1079, 11)
		w.WriteFlag(true) // frame_id_numbers_present_flag
		w.WriteBits(5, 4) // delta_frame_id_length_minus_2
		w.WriteBits(3, 3) // additional_frame_id_length_minus_1
		w.WriteFlag(true) // use_128x128_superblock
		w.WriteFlag(true) // enable_filter_intra
		w.WriteFlag(false)
		w.WriteFlag(true)  // enable_interintra_compound
		w.WriteFlag(false) // enable_masked_compound
		w.WriteFlag(true)  // enable_warped_motion
		w.WriteFlag(true)  // enable_dual_filter
		w.WriteFlag(true)  // enable_order_hint
		w.WriteFlag(true)  // enable_jnt_comp
		w.WriteFlag(false) // enable_ref_frame_mvs
		w.WriteFlag(false) // seq_choose_screen_content_tools
		w.WriteFlag(true)  // seq_force_screen_content_tools
		w.WriteFlag(false) // seq_choose_integer_mv
		w.WriteFlag(false) // seq_force_integer_mv
		w.WriteBits(4, 3)  // order_hint_bits_minus_1
		w.WriteFlag(true)  // enable_superres
		w.WriteFlag(false) // enable_cdef
		w.WriteFlag(true)  // enable_restoration
		w.WriteFlag(true)  // high_bitdepth
		w.WriteFlag(true)  // twelve_bit
		w.WriteFlag(false) // mono_chrome
		w.WriteFlag(true)  // color_description_present_flag
		w.WriteBits(9, 8)
		w.WriteBits(16, 8)
		w.WriteBits(9, 8)
		w.WriteFlag(true)  // color_range
		w.WriteFlag(true)  // subsampling_x
		w.WriteFlag(false) // subsampling_y
		w.WriteFlag(true)  // separate_uv_delta_q
		w.WriteFlag(true)  // film_grain_params_present
		w.Trailing()
		sh, err := av1.ParseSequenceHeader(w.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		want := &av1.SequenceHeader{
			Profile: 2, StillPicture: true, OperatingPoints: 2, OperatingPointIdc: 0x123, LevelIdx: 13, Tier: 1,
			TimingInfoPresent: true, NumUnitsInDisplayTick: 1001, TimeScale: 60000, EqualPictureInterval: true, NumTicksPerPictureMinus1: 5,
			DecoderModelInfoPresent: true, InitialDisplayDelayPresent: true, InitialDisplayDelay: 4,
			FrameWidthBits: 12, FrameHeightBits: 11, MaxFrameWidth: 1920, MaxFrameHeight: 1080,
			FrameIDNumbersPresent: true, DeltaFrameIDLength: 7, AdditionalFrameIDLength: 4,
			Use128x128Superblock: true, EnableFilterIntra: true, EnableInterintraCompound: true, EnableWarpedMotion: true,
			EnableDualFilter: true, EnableOrderHint: true, EnableJntComp: true,
			SeqForceScreenContentTools: 1, SeqForceIntegerMV: 0, OrderHintBits: 5,
			EnableSuperres: true, EnableRestoration: true,
			BitDepth: 12, ColorDescriptionPresent: true, ColorPrimaries: 9, TransferCharacteristics: 16, MatrixCoefficients: 9,
			FullRange: true, SubsamplingX: 1, SubsamplingY: 0, SeparateUVDeltaQ: true, FilmGrainParamsPresent: true,
			BufferRemovalTimeLength: 5, FramePresentationTimeLength: 5,
			OperatingPointIdcs: []uint16{0x123, 0x45}, DecoderModelPresentForOp: []bool{true, false},
		}
		if !reflect.DeepEqual(sh, want) {
			t.Fatalf("parsed\n%+v\nwant\n%+v", *sh, *want)
		}
	})
	t.Run("high-srgb-444-reduced", func(t *testing.T) {
		w := bitstream.NewWriter()
		w.WriteBits(1, 3)  // seq_profile
		w.WriteFlag(true)  // still_picture
		w.WriteFlag(true)  // reduced_still_picture_header
		w.WriteBits(8, 5)  // seq_level_idx[0] (no tier bit in this branch)
		w.WriteBits(7, 4)  // frame_width_bits_minus_1
		w.WriteBits(7, 4)  // frame_height_bits_minus_1
		w.WriteBits(99, 8) // max_frame_width_minus_1
		w.WriteBits(49, 8) // max_frame_height_minus_1
		w.WriteFlag(false) // use_128x128_superblock
		w.WriteFlag(false) // enable_filter_intra
		w.WriteFlag(true)  // enable_intra_edge_filter
		w.WriteFlag(false) // enable_superres
		w.WriteFlag(true)  // enable_cdef
		w.WriteFlag(false) // enable_restoration
		w.WriteFlag(false) // high_bitdepth (no mono_chrome bit for profile 1)
		w.WriteFlag(true)  // color_description_present_flag
		w.WriteBits(1, 8)  // BT.709
		w.WriteBits(13, 8) // sRGB
		w.WriteBits(0, 8)  // identity
		w.WriteFlag(false) // separate_uv_delta_q
		w.WriteFlag(false) // film_grain_params_present
		w.Trailing()
		sh, err := av1.ParseSequenceHeader(w.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		want := &av1.SequenceHeader{
			Profile: 1, StillPicture: true, ReducedStillPictureHeader: true, OperatingPoints: 1, LevelIdx: 8,
			FrameWidthBits: 8, FrameHeightBits: 8, MaxFrameWidth: 100, MaxFrameHeight: 50,
			EnableIntraEdgeFilter: true, SeqForceScreenContentTools: av1.SelectScreenContentTools, SeqForceIntegerMV: av1.SelectIntegerMV,
			EnableCDEF: true, BitDepth: 8, ColorDescriptionPresent: true, ColorPrimaries: 1, TransferCharacteristics: 13,
			FullRange:          true,
			OperatingPointIdcs: []uint16{0}, DecoderModelPresentForOp: []bool{false},
		}
		if !reflect.DeepEqual(sh, want) {
			t.Fatalf("parsed\n%+v\nwant\n%+v", *sh, *want)
		}
	})
	t.Run("monochrome", func(t *testing.T) {
		w := bitstream.NewWriter()
		w.WriteBits(0, 3)  // seq_profile
		w.WriteFlag(false) // still_picture
		w.WriteFlag(false) // reduced_still_picture_header
		w.WriteFlag(false) // timing_info_present_flag
		w.WriteFlag(false) // initial_display_delay_present_flag
		w.WriteBits(0, 5)  // operating_points_cnt_minus_1
		w.WriteBits(0, 12)
		w.WriteBits(1, 5) // seq_level_idx[0]
		w.WriteBits(3, 4) // frame_width_bits_minus_1
		w.WriteBits(3, 4)
		w.WriteBits(15, 4) // max_frame_width_minus_1
		w.WriteBits(15, 4)
		w.WriteFlag(false) // frame_id_numbers_present_flag
		w.WriteFlag(false) // use_128x128_superblock
		w.WriteFlag(false) // enable_filter_intra
		w.WriteFlag(false) // enable_intra_edge_filter
		w.WriteFlag(false) // enable_interintra_compound
		w.WriteFlag(false) // enable_masked_compound
		w.WriteFlag(false) // enable_warped_motion
		w.WriteFlag(false) // enable_dual_filter
		w.WriteFlag(false) // enable_order_hint
		w.WriteFlag(true)  // seq_choose_screen_content_tools
		w.WriteFlag(true)  // seq_choose_integer_mv
		w.WriteFlag(false) // enable_superres
		w.WriteFlag(false) // enable_cdef
		w.WriteFlag(false) // enable_restoration
		w.WriteFlag(false) // high_bitdepth
		w.WriteFlag(true)  // mono_chrome
		w.WriteFlag(false) // color_description_present_flag
		w.WriteFlag(true)  // color_range
		w.WriteFlag(true)  // film_grain_params_present
		w.Trailing()
		sh, err := av1.ParseSequenceHeader(w.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		want := &av1.SequenceHeader{
			OperatingPoints: 1, LevelIdx: 1, FrameWidthBits: 4, FrameHeightBits: 4, MaxFrameWidth: 16, MaxFrameHeight: 16,
			SeqForceScreenContentTools: av1.SelectScreenContentTools, SeqForceIntegerMV: av1.SelectIntegerMV,
			BitDepth: 8, MonoChrome: true, ColorPrimaries: 2, TransferCharacteristics: 2, MatrixCoefficients: 2,
			FullRange: true, SubsamplingX: 1, SubsamplingY: 1, FilmGrainParamsPresent: true,
			OperatingPointIdcs: []uint16{0}, DecoderModelPresentForOp: []bool{false},
		}
		if !reflect.DeepEqual(sh, want) {
			t.Fatalf("parsed\n%+v\nwant\n%+v", *sh, *want)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		if _, err := av1.ParseSequenceHeader(svtSeqHeader8[:5]); !errors.Is(err, av1.ErrInvalid) {
			t.Fatalf("got %v, want ErrInvalid", err)
		}
	})
}

func TestLEB128(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 16383, 16384, 1 << 35, 1<<56 - 1} {
		b := av1.AppendLEB128(nil, v)
		got, n, ok := av1.ReadLEB128(append(b, 0xff))
		if !ok || got != v || n != len(b) {
			t.Errorf("%d: encoded % x, read %d (%d bytes, ok=%v)", v, b, got, n, ok)
		}
	}
	if _, _, ok := av1.ReadLEB128(nil); ok {
		t.Error("empty input decoded")
	}
	if _, _, ok := av1.ReadLEB128([]byte{0x80, 0x80}); ok {
		t.Error("truncated value decoded")
	}
	if _, _, ok := av1.ReadLEB128(bytes.Repeat([]byte{0x80}, 9)); ok {
		t.Error("nine-byte value decoded")
	}
}

func TestSplit(t *testing.T) {
	frame := []byte{0x10, 0xaa, 0xbb}
	var tu []byte
	tu = av1.AppendOBU(tu, av1.OBUTemporalDelimiter, nil)
	tu = av1.AppendOBU(tu, av1.OBUSequenceHeader, svtSeqHeader8)
	// A frame OBU with an extension header (temporal id 2, spatial id 1).
	tu = append(tu, byte(av1.OBUFrame)<<3|0x06, 2<<5|1<<3)
	tu = av1.AppendLEB128(tu, uint64(len(frame)))
	tu = append(tu, frame...)
	// A last OBU without a size field runs to the end.
	tu = append(tu, byte(av1.OBUPadding)<<3)
	tu = append(tu, 0, 0, 0)

	obus, err := av1.Split(tu)
	if err != nil {
		t.Fatal(err)
	}
	if len(obus) != 4 {
		t.Fatalf("got %d OBUs, want 4", len(obus))
	}
	if obus[0].Type != av1.OBUTemporalDelimiter || len(obus[0].Payload) != 0 || !bytes.Equal(obus[0].Raw, []byte{0x12, 0x00}) {
		t.Errorf("temporal delimiter: %+v", obus[0])
	}
	if obus[1].Type != av1.OBUSequenceHeader || !bytes.Equal(obus[1].Payload, svtSeqHeader8) || len(obus[1].Raw) != 2+len(svtSeqHeader8) {
		t.Errorf("sequence header: %+v", obus[1])
	}
	if o := obus[2]; o.Type != av1.OBUFrame || !o.HasExtension || o.TemporalID != 2 || o.SpatialID != 1 || !bytes.Equal(o.Payload, frame) || len(o.Raw) != 3+len(frame) {
		t.Errorf("frame: %+v", o)
	}
	if o := obus[3]; o.Type != av1.OBUPadding || len(o.Payload) != 3 || len(o.Raw) != 4 {
		t.Errorf("padding: %+v", o)
	}
	if &obus[2].Payload[0] != &tu[2+len(svtSeqHeader8)+2+3] {
		t.Error("payload does not alias the input")
	}

	for name, bad := range map[string][]byte{
		"forbidden bit":  {0x92, 0x00},
		"size too large": {0x0a, 0x20, 0x01},
		"truncated size": {0x0a, 0x80},
		"truncated ext":  {0x36},
	} {
		if _, err := av1.Split(bad); !errors.Is(err, av1.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if obus, err := av1.Split(nil); err != nil || len(obus) != 0 {
		t.Errorf("empty input: %v %v", obus, err)
	}
}

func TestParseFrameHeader(t *testing.T) {
	cases := []struct {
		b    byte
		want av1.FrameHeader
	}{
		{0x10, av1.FrameHeader{FrameType: av1.KeyFrame, ShowFrame: true}},
		{0x00, av1.FrameHeader{FrameType: av1.KeyFrame, ShowFrame: false}},
		{0x30, av1.FrameHeader{FrameType: av1.InterFrame, ShowFrame: true}},
		{0x2f, av1.FrameHeader{FrameType: av1.InterFrame, ShowFrame: false}},
		{0x50, av1.FrameHeader{FrameType: av1.IntraOnlyFrame, ShowFrame: true}},
		{0x70, av1.FrameHeader{FrameType: av1.SwitchFrame, ShowFrame: true}},
		{0xe0, av1.FrameHeader{ShowExistingFrame: true, FrameToShowMapIdx: 6}},
		{0x8f, av1.FrameHeader{ShowExistingFrame: true, FrameToShowMapIdx: 0}},
	}
	for _, c := range cases {
		got, err := av1.ParseFrameHeader([]byte{c.b, 0xff}, false)
		if err != nil || got != c.want {
			t.Errorf("%#x: got %+v (%v), want %+v", c.b, got, err, c.want)
		}
	}
	if got, err := av1.ParseFrameHeader([]byte{0x2f}, true); err != nil || got != (av1.FrameHeader{FrameType: av1.KeyFrame, ShowFrame: true}) {
		t.Errorf("reduced still picture: %+v %v", got, err)
	}
	if _, err := av1.ParseFrameHeader(nil, false); !errors.Is(err, av1.ErrInvalid) {
		t.Errorf("empty: %v", err)
	}
}

func TestParseTemporalUnit(t *testing.T) {
	key := av1.AppendOBU(av1.AppendOBU(av1.AppendOBU(nil, av1.OBUTemporalDelimiter, nil), av1.OBUSequenceHeader, svtSeqHeader8), av1.OBUFrame, []byte{0x10, 0xaa})
	tu, err := av1.ParseTemporalUnit(key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tu.SequenceHeader == nil || tu.Sequence == nil || tu.Sequence.MaxFrameWidth != 320 || !tu.HasFrame || !tu.Keyframe || tu.ShowExistingFrame {
		t.Fatalf("key unit: %+v", tu)
	}
	seq := tu.Sequence

	hidden := av1.AppendOBU(av1.AppendOBU(av1.AppendOBU(nil, av1.OBUTemporalDelimiter, nil), av1.OBUFrame, []byte{0x20, 0xaa}), av1.OBUFrame, []byte{0x30, 0xbb})
	tu, err = av1.ParseTemporalUnit(hidden, seq)
	if err != nil {
		t.Fatal(err)
	}
	if tu.SequenceHeader != nil || !tu.HasFrame || tu.Keyframe || tu.ShowExistingFrame {
		t.Fatalf("hidden+inter unit: %+v", tu)
	}

	existing := av1.AppendOBU(av1.AppendOBU(nil, av1.OBUTemporalDelimiter, nil), av1.OBUFrameHeader, []byte{0xe0})
	tu, err = av1.ParseTemporalUnit(existing, seq)
	if err != nil {
		t.Fatal(err)
	}
	if !tu.HasFrame || tu.Keyframe || !tu.ShowExistingFrame {
		t.Fatalf("show_existing_frame unit: %+v", tu)
	}
	// Without any sequence header the frame cannot be classified.
	tu, err = av1.ParseTemporalUnit(existing, nil)
	if err != nil || !tu.HasFrame || tu.Keyframe || tu.ShowExistingFrame {
		t.Fatalf("unit without sequence header: %+v %v", tu, err)
	}

	td := av1.AppendOBU(nil, av1.OBUTemporalDelimiter, nil)
	tu, err = av1.ParseTemporalUnit(td, seq)
	if err != nil || tu.HasFrame {
		t.Fatalf("delimiter-only unit: %+v %v", tu, err)
	}
	if _, err := av1.ParseTemporalUnit(nil, seq); !errors.Is(err, av1.ErrInvalid) {
		t.Fatalf("empty unit: %v", err)
	}
	if _, err := av1.ParseTemporalUnit([]byte("not a bitstream"), seq); !errors.Is(err, av1.ErrInvalid) {
		t.Fatalf("garbage: %v", err)
	}

	stripped := av1.StripTemporalDelimiters(key, tu0(t, key))
	if !bytes.Equal(stripped, key[2:]) {
		t.Fatalf("stripped % x", stripped)
	}
	if got := av1.StripTemporalDelimiters(key[2:], tu0(t, key[2:])); &got[0] != &key[2] {
		t.Fatal("stripping nothing should return the input")
	}
}

func tu0(t *testing.T, data []byte) []av1.OBU {
	t.Helper()
	obus, err := av1.Split(data)
	if err != nil {
		t.Fatal(err)
	}
	return obus
}

func TestCodecConfigurationRecord(t *testing.T) {
	sh, err := av1.ParseSequenceHeader(svtSeqHeader8)
	if err != nil {
		t.Fatal(err)
	}
	if got := av1.CodecConfigurationRecord(sh, svtSeqHeader8); !bytes.Equal(got, svtAV1C8) {
		t.Fatalf("av1C\n% x\nwant (ffmpeg's)\n% x", got, svtAV1C8)
	}
	synthetic := &av1.SequenceHeader{Profile: 2, LevelIdx: 13, Tier: 1, BitDepth: 12, MonoChrome: true, SubsamplingX: 1, SubsamplingY: 1, ChromaSamplePosition: 2}
	if got := av1.CodecConfigurationRecord(synthetic, []byte{1, 2}); !bytes.Equal(got, []byte{0x81, 0x4d, 0xfe, 0x00, 0x0a, 0x02, 1, 2}) {
		t.Fatalf("synthetic av1C % x", got)
	}
}
