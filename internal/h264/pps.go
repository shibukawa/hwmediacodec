package h264

import (
	"errors"
	"math/bits"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// ErrMissingSPS is returned when a PPS or slice refers to a sequence
// parameter set that has not been received.
var ErrMissingSPS = errors.New("h264: referenced SPS not available")

// ErrMissingPPS is returned when a slice refers to a picture parameter set
// that has not been received.
var ErrMissingPPS = errors.New("h264: referenced PPS not available")

// PPS is a parsed picture parameter set (7.3.2.2).
type PPS struct {
	ID    uint32
	SPSID uint32

	EntropyCodingMode                 bool // CABAC
	BottomFieldPicOrderInFramePresent bool

	NumSliceGroups             uint32
	SliceGroupMapType          uint32
	SliceGroupChangeRate       uint32 // slice_group_change_rate_minus1 + 1
	SliceGroupChangeDirection  bool
	PicSizeInMapUnitsForGroups uint32 // pic_size_in_map_units_minus1 + 1 (map type 6)

	NumRefIdxL0DefaultActive uint32
	NumRefIdxL1DefaultActive uint32
	WeightedPred             bool
	WeightedBipredIdc        uint32
	PicInitQpMinus26         int32
	PicInitQsMinus26         int32
	ChromaQpIndexOffset      int32

	DeblockingFilterControlPresent bool
	ConstrainedIntraPred           bool
	RedundantPicCntPresent         bool

	Transform8x8Mode          bool
	ScalingMatrixPresent      bool
	ScalingLists              ScalingLists // resolved against the SPS
	SecondChromaQpIndexOffset int32
}

// ParsePPS parses a picture parameter set NAL unit (including the NAL header
// byte). The SPS it refers to is needed to parse the scaling lists; lookup
// returns nil when the id is unknown, in which case ErrMissingSPS is
// returned.
func ParsePPS(nal []byte, lookup func(spsID uint32) *SPS) (*PPS, error) {
	if _, typ, ok := NALHeader(nal); !ok || typ != NALPPS {
		return nil, syntaxErr("not a PPS NAL unit")
	}
	if len(nal) < 2 {
		return nil, syntaxErr("PPS too short")
	}
	r := bitstream.NewRBSP(nal[1:])
	p := &PPS{}
	var err error
	if p.ID, err = r.ReadUE(); err != nil {
		return nil, err
	}
	if p.ID > maxPPSID {
		return nil, syntaxErr("pic_parameter_set_id %d out of range", p.ID)
	}
	if p.SPSID, err = r.ReadUE(); err != nil {
		return nil, err
	}
	if p.SPSID > maxSPSID {
		return nil, syntaxErr("seq_parameter_set_id %d out of range", p.SPSID)
	}
	sps := lookup(p.SPSID)
	if sps == nil {
		return nil, ErrMissingSPS
	}
	if p.EntropyCodingMode, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if p.BottomFieldPicOrderInFramePresent, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if p.NumSliceGroups, err = r.ReadUE(); err != nil {
		return nil, err
	}
	p.NumSliceGroups++
	if p.NumSliceGroups > 8 {
		return nil, syntaxErr("num_slice_groups_minus1 %d out of range", p.NumSliceGroups-1)
	}
	if p.NumSliceGroups > 1 {
		if err := parseSliceGroups(r, p); err != nil {
			return nil, err
		}
	}
	if p.NumRefIdxL0DefaultActive, err = r.ReadUE(); err != nil {
		return nil, err
	}
	if p.NumRefIdxL1DefaultActive, err = r.ReadUE(); err != nil {
		return nil, err
	}
	p.NumRefIdxL0DefaultActive++
	p.NumRefIdxL1DefaultActive++
	if p.NumRefIdxL0DefaultActive > maxRefIdxActive || p.NumRefIdxL1DefaultActive > maxRefIdxActive {
		return nil, syntaxErr("num_ref_idx_default_active out of range")
	}
	if p.WeightedPred, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	v, err := r.ReadBits(2)
	if err != nil {
		return nil, err
	}
	p.WeightedBipredIdc = uint32(v)
	if p.PicInitQpMinus26, err = r.ReadSE(); err != nil {
		return nil, err
	}
	if p.PicInitQsMinus26, err = r.ReadSE(); err != nil {
		return nil, err
	}
	if p.ChromaQpIndexOffset, err = r.ReadSE(); err != nil {
		return nil, err
	}
	if p.DeblockingFilterControlPresent, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if p.ConstrainedIntraPred, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if p.RedundantPicCntPresent, err = r.ReadFlag(); err != nil {
		return nil, err
	}

	p.ScalingLists = sps.ScalingLists
	p.SecondChromaQpIndexOffset = p.ChromaQpIndexOffset
	if r.MoreRBSPData() {
		if p.Transform8x8Mode, err = r.ReadFlag(); err != nil {
			return nil, err
		}
		if p.ScalingMatrixPresent, err = r.ReadFlag(); err != nil {
			return nil, err
		}
		if p.ScalingMatrixPresent {
			count := 6
			if p.Transform8x8Mode {
				if sps.ChromaFormatIDC == 3 {
					count += 6
				} else {
					count += 2
				}
			}
			var fallback *ScalingLists
			if sps.ScalingMatrixPresent {
				fallback = &sps.ScalingLists
			}
			if p.ScalingLists, err = parseScalingMatrix(r, count, fallback); err != nil {
				return nil, err
			}
		}
		if p.SecondChromaQpIndexOffset, err = r.ReadSE(); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// parseSliceGroups consumes the FMO syntax so that the remaining fields can
// be read. Hardware decoders do not support FMO; the backend rejects
// NumSliceGroups > 1.
func parseSliceGroups(r *bitstream.Reader, p *PPS) error {
	var err error
	if p.SliceGroupMapType, err = r.ReadUE(); err != nil {
		return err
	}
	switch p.SliceGroupMapType {
	case 0:
		for i := uint32(0); i < p.NumSliceGroups; i++ {
			if _, err := r.ReadUE(); err != nil { // run_length_minus1
				return err
			}
		}
	case 2:
		for i := uint32(0); i+1 < p.NumSliceGroups; i++ {
			if _, err := r.ReadUE(); err != nil { // top_left
				return err
			}
			if _, err := r.ReadUE(); err != nil { // bottom_right
				return err
			}
		}
	case 3, 4, 5:
		if p.SliceGroupChangeDirection, err = r.ReadFlag(); err != nil {
			return err
		}
		if p.SliceGroupChangeRate, err = r.ReadUE(); err != nil {
			return err
		}
		p.SliceGroupChangeRate++
	case 6:
		if p.PicSizeInMapUnitsForGroups, err = r.ReadUE(); err != nil {
			return err
		}
		p.PicSizeInMapUnitsForGroups++
		n := bits.Len32(p.NumSliceGroups - 1) // Ceil(Log2(num_slice_groups))
		for i := uint32(0); i < p.PicSizeInMapUnitsForGroups; i++ {
			if _, err := r.ReadBits(n); err != nil { // slice_group_id
				return err
			}
		}
	case 1:
	default:
		return syntaxErr("slice_group_map_type %d out of range", p.SliceGroupMapType)
	}
	return nil
}
