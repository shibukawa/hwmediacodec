package annexb

import (
	"github.com/shibukawa/hwmediacodec/internal/bitstream"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Parameter-set kinds returned by ParameterSetID.
const (
	ParamVPS = 0
	ParamSPS = 1
	ParamPPS = 2
)

// ParameterSetID returns the kind (ParamVPS, ParamSPS or ParamPPS) and the id
// of a parameter-set NAL unit given without its start code. ok is false when
// the NAL unit type is not a parameter set or the header cannot be parsed.
//
// Backends use it to keep the latest parameter sets by id so that a decoder
// session can be rebuilt (VideoToolbox) or re-primed after a flush (Media
// Foundation).
func ParameterSetID(c codec.Codec, nalType int, nal []byte) (kind int, id uint32, ok bool) {
	switch c {
	case codec.H264:
		if len(nal) < 2 {
			return 0, 0, false
		}
		r := bitstream.NewRBSP(nal[1:])
		switch nalType {
		case H264NALSPS:
			if err := r.SkipBits(24); err != nil { // profile_idc, constraint flags, level_idc
				return 0, 0, false
			}
			id, err := r.ReadUE()
			if err != nil {
				return 0, 0, false
			}
			return ParamSPS, id, true
		case H264NALPPS:
			id, err := r.ReadUE()
			if err != nil {
				return 0, 0, false
			}
			return ParamPPS, id, true
		}
	case codec.HEVC:
		if len(nal) < 3 {
			return 0, 0, false
		}
		r := bitstream.NewRBSP(nal[2:])
		switch nalType {
		case HEVCNALVPS:
			id, err := r.ReadBits(4)
			if err != nil {
				return 0, 0, false
			}
			return ParamVPS, uint32(id), true
		case HEVCNALSPS:
			if err := r.SkipBits(4); err != nil { // sps_video_parameter_set_id
				return 0, 0, false
			}
			maxSubLayersMinus1, err := r.ReadBits(3)
			if err != nil {
				return 0, 0, false
			}
			if err := r.SkipBits(1); err != nil { // temporal_id_nesting_flag
				return 0, 0, false
			}
			if err := skipProfileTierLevel(r, int(maxSubLayersMinus1)); err != nil {
				return 0, 0, false
			}
			id, err := r.ReadUE()
			if err != nil {
				return 0, 0, false
			}
			return ParamSPS, id, true
		case HEVCNALPPS:
			id, err := r.ReadUE()
			if err != nil {
				return 0, 0, false
			}
			return ParamPPS, id, true
		}
	}
	return 0, 0, false
}

// skipProfileTierLevel skips profile_tier_level(1, maxSubLayersMinus1).
func skipProfileTierLevel(r *bitstream.Reader, maxSubLayersMinus1 int) error {
	// general_profile_space(2) tier(1) idc(5) compat(32) flags(48) = 88 bits,
	// then general_level_idc(8).
	if err := r.SkipBits(88 + 8); err != nil {
		return err
	}
	profilePresent := make([]bool, maxSubLayersMinus1)
	levelPresent := make([]bool, maxSubLayersMinus1)
	for i := 0; i < maxSubLayersMinus1; i++ {
		p, err := r.ReadBit()
		if err != nil {
			return err
		}
		l, err := r.ReadBit()
		if err != nil {
			return err
		}
		profilePresent[i] = p == 1
		levelPresent[i] = l == 1
	}
	if maxSubLayersMinus1 > 0 {
		if err := r.SkipBits(2 * (8 - maxSubLayersMinus1)); err != nil {
			return err
		}
	}
	for i := 0; i < maxSubLayersMinus1; i++ {
		if profilePresent[i] {
			if err := r.SkipBits(88); err != nil {
				return err
			}
		}
		if levelPresent[i] {
			if err := r.SkipBits(8); err != nil {
				return err
			}
		}
	}
	return nil
}
