package hevc

import "github.com/shibukawa/hwmediacodec/internal/bitstream"

// Profile identifiers (general_profile_idc).
const (
	ProfileMain   = 1
	ProfileMain10 = 2
	ProfileRExt   = 4
)

// ProfileTierLevel holds the general_* fields of profile_tier_level()
// (7.3.3). Sub-layer profiles and levels are skipped.
type ProfileTierLevel struct {
	ProfileSpace uint8
	Tier         bool
	ProfileIDC   uint8
	// CompatibilityFlags holds general_profile_compatibility_flag[j] in bit
	// 31-j, as the 32 flags appear in the bitstream.
	CompatibilityFlags  uint32
	ProgressiveSource   bool
	InterlacedSource    bool
	NonPackedConstraint bool
	FrameOnlyConstraint bool
	// ConstraintBits holds the 44 bits between
	// general_frame_only_constraint_flag and general_level_idc (profile
	// specific constraint flags, reserved bits and general_inbld_flag).
	ConstraintBits uint64
	LevelIDC       uint8
}

// Compatible reports general_profile_compatibility_flag[j].
func (p *ProfileTierLevel) Compatible(j int) bool {
	return j >= 0 && j < 32 && p.CompatibilityFlags&(1<<uint(31-j)) != 0
}

// profileTierLevel reads profile_tier_level(1, maxSubLayersMinus1).
func (r *reader) profileTierLevel(maxSubLayersMinus1 int) ProfileTierLevel {
	var p ProfileTierLevel
	p.ProfileSpace = uint8(r.u(2))
	p.Tier = r.flag()
	p.ProfileIDC = uint8(r.u(5))
	p.CompatibilityFlags = r.u(32)
	p.ProgressiveSource = r.flag()
	p.InterlacedSource = r.flag()
	p.NonPackedConstraint = r.flag()
	p.FrameOnlyConstraint = r.flag()
	p.ConstraintBits = r.u64(44)
	p.LevelIDC = uint8(r.u(8))

	var profilePresent, levelPresent [8]bool
	for i := 0; i < maxSubLayersMinus1; i++ {
		profilePresent[i] = r.flag()
		levelPresent[i] = r.flag()
	}
	if maxSubLayersMinus1 > 0 {
		r.skip(2 * (8 - maxSubLayersMinus1)) // reserved_zero_2bits
	}
	for i := 0; i < maxSubLayersMinus1; i++ {
		if profilePresent[i] {
			r.skip(88)
		}
		if levelPresent[i] {
			r.skip(8)
		}
	}
	return p
}

// write emits profile_tier_level(1, 0).
func (p *ProfileTierLevel) write(w *bitstream.Writer) {
	w.WriteBits(uint64(p.ProfileSpace), 2)
	w.WriteFlag(p.Tier)
	w.WriteBits(uint64(p.ProfileIDC), 5)
	w.WriteBits(uint64(p.CompatibilityFlags), 32)
	w.WriteFlag(p.ProgressiveSource)
	w.WriteFlag(p.InterlacedSource)
	w.WriteFlag(p.NonPackedConstraint)
	w.WriteFlag(p.FrameOnlyConstraint)
	w.WriteBits(p.ConstraintBits, 44)
	w.WriteBits(uint64(p.LevelIDC), 8)
}
