package av1

import (
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// Values of seq_force_screen_content_tools and seq_force_integer_mv that
// leave the choice to each frame.
const (
	SelectScreenContentTools = 2
	SelectIntegerMV          = 2
)

// Colour description codes (ISO/IEC 23091-4) the parser interprets.
const (
	ColorPrimariesBT709       = 1
	ColorPrimariesUnspecified = 2
	TransferUnspecified       = 2
	TransferSRGB              = 13
	MatrixIdentity            = 0
	MatrixUnspecified         = 2
)

// SequenceHeader holds the fields of a sequence header OBU (Section 5.5).
// Level and tier are those of operating point 0, which is what a decoder
// of the whole stream uses.
type SequenceHeader struct {
	Profile                   uint8 // seq_profile: 0 Main, 1 High, 2 Professional
	StillPicture              bool
	ReducedStillPictureHeader bool

	OperatingPoints   int    // operating_points_cnt_minus_1 + 1
	OperatingPointIdc uint16 // operating_point_idc[0]
	LevelIdx          uint8  // seq_level_idx[0]; 31 means unconstrained
	Tier              uint8  // seq_tier[0]

	TimingInfoPresent          bool
	NumUnitsInDisplayTick      uint32
	TimeScale                  uint32
	EqualPictureInterval       bool
	NumTicksPerPictureMinus1   uint32
	DecoderModelInfoPresent    bool
	InitialDisplayDelayPresent bool
	// InitialDisplayDelay is initial_display_delay_minus_1[0] + 1, or 0
	// when the stream does not signal it for operating point 0.
	InitialDisplayDelay int

	FrameWidthBits  uint8 // frame_width_bits_minus_1 + 1
	FrameHeightBits uint8
	MaxFrameWidth   int // max_frame_width_minus_1 + 1
	MaxFrameHeight  int

	FrameIDNumbersPresent   bool
	DeltaFrameIDLength      uint8 // delta_frame_id_length_minus_2 + 2
	AdditionalFrameIDLength uint8 // additional_frame_id_length_minus_1 + 1

	Use128x128Superblock     bool
	EnableFilterIntra        bool
	EnableIntraEdgeFilter    bool
	EnableInterintraCompound bool
	EnableMaskedCompound     bool
	EnableWarpedMotion       bool
	EnableDualFilter         bool
	EnableOrderHint          bool
	EnableJntComp            bool
	EnableRefFrameMVs        bool
	// SeqForceScreenContentTools and SeqForceIntegerMV are 0, 1 or the
	// Select* value that defers the choice to each frame.
	SeqForceScreenContentTools uint8
	SeqForceIntegerMV          uint8
	OrderHintBits              uint8
	EnableSuperres             bool
	EnableCDEF                 bool
	EnableRestoration          bool

	// color_config (Section 5.5.2).
	BitDepth                int // 8, 10 or 12
	MonoChrome              bool
	ColorDescriptionPresent bool
	ColorPrimaries          uint8 // ISO/IEC 23091-4 code; 2 when unspecified
	TransferCharacteristics uint8
	MatrixCoefficients      uint8
	FullRange               bool // color_range
	SubsamplingX            uint8
	SubsamplingY            uint8
	ChromaSamplePosition    uint8 // 0 unknown, 1 vertical, 2 colocated
	SeparateUVDeltaQ        bool

	FilmGrainParamsPresent bool
}

// ParseSequenceHeader parses the payload of a sequence header OBU.
func ParseSequenceHeader(payload []byte) (*SequenceHeader, error) {
	r := bitstream.New(payload)
	sh := &SequenceHeader{}
	var err error
	u := func(n int) uint64 {
		if err != nil {
			return 0
		}
		v, e := r.ReadBits(n)
		if e != nil {
			err = e
		}
		return v
	}
	f := func() bool { return u(1) == 1 }

	sh.Profile = uint8(u(3))
	sh.StillPicture = f()
	sh.ReducedStillPictureHeader = f()
	if sh.ReducedStillPictureHeader {
		sh.OperatingPoints = 1
		sh.LevelIdx = uint8(u(5))
	} else {
		sh.TimingInfoPresent = f()
		bufferDelayLength := 0
		if sh.TimingInfoPresent {
			sh.NumUnitsInDisplayTick = uint32(u(32))
			sh.TimeScale = uint32(u(32))
			sh.EqualPictureInterval = f()
			if sh.EqualPictureInterval && err == nil {
				sh.NumTicksPerPictureMinus1, err = readUVLC(r)
			}
			sh.DecoderModelInfoPresent = f()
			if sh.DecoderModelInfoPresent {
				bufferDelayLength = int(u(5)) + 1
				u(32) // num_units_in_decoding_tick
				u(5)  // buffer_removal_time_length_minus_1
				u(5)  // frame_presentation_time_length_minus_1
			}
		}
		sh.InitialDisplayDelayPresent = f()
		sh.OperatingPoints = int(u(5)) + 1
		for i := 0; i < sh.OperatingPoints; i++ {
			idc := uint16(u(12))
			level := uint8(u(5))
			var tier uint8
			if level > 7 {
				tier = uint8(u(1))
			}
			if sh.DecoderModelInfoPresent && f() { // decoder_model_present_for_this_op
				u(bufferDelayLength) // decoder_buffer_delay
				u(bufferDelayLength) // encoder_buffer_delay
				u(1)                 // low_delay_mode_flag
			}
			delay := 0
			if sh.InitialDisplayDelayPresent && f() {
				delay = int(u(4)) + 1
			}
			if i == 0 {
				sh.OperatingPointIdc, sh.LevelIdx, sh.Tier, sh.InitialDisplayDelay = idc, level, tier, delay
			}
		}
	}

	sh.FrameWidthBits = uint8(u(4)) + 1
	sh.FrameHeightBits = uint8(u(4)) + 1
	sh.MaxFrameWidth = int(u(int(sh.FrameWidthBits))) + 1
	sh.MaxFrameHeight = int(u(int(sh.FrameHeightBits))) + 1
	if !sh.ReducedStillPictureHeader {
		sh.FrameIDNumbersPresent = f()
	}
	if sh.FrameIDNumbersPresent {
		sh.DeltaFrameIDLength = uint8(u(4)) + 2
		sh.AdditionalFrameIDLength = uint8(u(3)) + 1
	}
	sh.Use128x128Superblock = f()
	sh.EnableFilterIntra = f()
	sh.EnableIntraEdgeFilter = f()
	if sh.ReducedStillPictureHeader {
		sh.SeqForceScreenContentTools = SelectScreenContentTools
		sh.SeqForceIntegerMV = SelectIntegerMV
	} else {
		sh.EnableInterintraCompound = f()
		sh.EnableMaskedCompound = f()
		sh.EnableWarpedMotion = f()
		sh.EnableDualFilter = f()
		sh.EnableOrderHint = f()
		if sh.EnableOrderHint {
			sh.EnableJntComp = f()
			sh.EnableRefFrameMVs = f()
		}
		if f() { // seq_choose_screen_content_tools
			sh.SeqForceScreenContentTools = SelectScreenContentTools
		} else {
			sh.SeqForceScreenContentTools = uint8(u(1))
		}
		sh.SeqForceIntegerMV = SelectIntegerMV
		if sh.SeqForceScreenContentTools > 0 && !f() { // seq_choose_integer_mv
			sh.SeqForceIntegerMV = uint8(u(1))
		}
		if sh.EnableOrderHint {
			sh.OrderHintBits = uint8(u(3)) + 1
		}
	}
	sh.EnableSuperres = f()
	sh.EnableCDEF = f()
	sh.EnableRestoration = f()

	// color_config()
	highBitdepth := f()
	switch {
	case sh.Profile == 2 && highBitdepth:
		if f() { // twelve_bit
			sh.BitDepth = 12
		} else {
			sh.BitDepth = 10
		}
	case highBitdepth:
		sh.BitDepth = 10
	default:
		sh.BitDepth = 8
	}
	if sh.Profile != 1 {
		sh.MonoChrome = f()
	}
	sh.ColorDescriptionPresent = f()
	if sh.ColorDescriptionPresent {
		sh.ColorPrimaries = uint8(u(8))
		sh.TransferCharacteristics = uint8(u(8))
		sh.MatrixCoefficients = uint8(u(8))
	} else {
		sh.ColorPrimaries = ColorPrimariesUnspecified
		sh.TransferCharacteristics = TransferUnspecified
		sh.MatrixCoefficients = MatrixUnspecified
	}
	switch {
	case sh.MonoChrome:
		sh.FullRange = f()
		sh.SubsamplingX, sh.SubsamplingY = 1, 1
	case sh.ColorPrimaries == ColorPrimariesBT709 && sh.TransferCharacteristics == TransferSRGB && sh.MatrixCoefficients == MatrixIdentity:
		sh.FullRange = true
		sh.SubsamplingX, sh.SubsamplingY = 0, 0
		sh.SeparateUVDeltaQ = f()
	default:
		sh.FullRange = f()
		switch sh.Profile {
		case 0:
			sh.SubsamplingX, sh.SubsamplingY = 1, 1
		case 1:
			sh.SubsamplingX, sh.SubsamplingY = 0, 0
		default:
			if sh.BitDepth == 12 {
				sh.SubsamplingX = uint8(u(1))
				if sh.SubsamplingX == 1 {
					sh.SubsamplingY = uint8(u(1))
				}
			} else {
				sh.SubsamplingX, sh.SubsamplingY = 1, 0
			}
		}
		if sh.SubsamplingX == 1 && sh.SubsamplingY == 1 {
			sh.ChromaSamplePosition = uint8(u(2))
		}
		sh.SeparateUVDeltaQ = f()
	}
	sh.FilmGrainParamsPresent = f()
	if err != nil {
		return nil, fmt.Errorf("%w: sequence header: %v", ErrInvalid, err)
	}
	if sh.Profile > 2 {
		return nil, fmt.Errorf("%w: reserved seq_profile %d", ErrInvalid, sh.Profile)
	}
	return sh, nil
}

// readUVLC reads a uvlc() value (Section 4.10.3).
func readUVLC(r *bitstream.Reader) (uint32, error) {
	leadingZeros := 0
	for {
		b, err := r.ReadBit()
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		leadingZeros++
		if leadingZeros >= 32 {
			return 1<<32 - 1, nil
		}
	}
	v, err := r.ReadBits(leadingZeros)
	if err != nil {
		return 0, err
	}
	return uint32(v + 1<<uint(leadingZeros) - 1), nil
}
