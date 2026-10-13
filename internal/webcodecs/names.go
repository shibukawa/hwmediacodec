package webcodecs

import (
	"fmt"
	"math"
	"math/bits"
	"strings"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "webcodecs"

// The codec strings WebCodecs configurations carry are the ones of RFC 6381
// and the ISOBMFF bindings of each codec.

// h264CodecString returns "avc1.PPCCLL": profile_idc, the constraint flags
// byte and level_idc in hexadecimal.
func h264CodecString(profileIDC, constraints, levelIDC uint8) string {
	return fmt.Sprintf("avc1.%02X%02X%02X", profileIDC, constraints, levelIDC)
}

// h264StreamCodecString is the codec string of a stream with the given SPS.
func h264StreamCodecString(sps *h264.SPS) string {
	return h264CodecString(sps.ProfileIDC, sps.ConstraintFlags, sps.LevelIDC)
}

// hevcCodecStrings returns the codec strings of a stream with the given
// profile_tier_level, as ISO/IEC 14496-15 Annex E writes them:
// the profile space letter and profile, the compatibility flags in
// reversed bit order, the tier and level, and the constraint bytes without
// trailing zero bytes. "hev1" (parameter sets in-band, which is how the
// packets arrive) comes first, "hvc1" second for browsers that only know
// that sample entry name.
func hevcCodecStrings(p *hevc.ProfileTierLevel) []string {
	var b strings.Builder
	if p.ProfileSpace > 0 && p.ProfileSpace < 4 {
		b.WriteByte('A' + p.ProfileSpace - 1)
	}
	fmt.Fprintf(&b, "%d.%X.", p.ProfileIDC, bits.Reverse32(p.CompatibilityFlags))
	if p.Tier {
		b.WriteByte('H')
	} else {
		b.WriteByte('L')
	}
	fmt.Fprintf(&b, "%d", p.LevelIDC)

	// The 48 bits from general_progressive_source_flag on.
	var flags uint64
	for i, set := range []bool{p.ProgressiveSource, p.InterlacedSource, p.NonPackedConstraint, p.FrameOnlyConstraint} {
		if set {
			flags |= 1 << uint(47-i)
		}
	}
	flags |= p.ConstraintBits & (1<<44 - 1)
	var constraint [6]byte
	for i := range constraint {
		constraint[i] = byte(flags >> uint(40-8*i))
	}
	n := len(constraint)
	for n > 0 && constraint[n-1] == 0 {
		n--
	}
	for _, c := range constraint[:n] {
		fmt.Fprintf(&b, ".%X", c)
	}
	return []string{"hev1." + b.String(), "hvc1." + b.String()}
}

// av1CodecString returns "av01.P.LLT.DD": profile, level index, tier and
// bit depth of the sequence header.
func av1CodecString(sh *av1.SequenceHeader) string {
	tier := 'M'
	if sh.Tier != 0 {
		tier = 'H'
	}
	return fmt.Sprintf("av01.%d.%02d%c.%02d", sh.Profile, sh.LevelIdx, tier, sh.BitDepth)
}

// probeDecodeCodecs lists codec strings that stand for "this codec" before
// a stream has been seen: Probe and NewDecoder accept the codec when the
// browser supports any of them. 1080p-class levels of the profiles
// hardware decoders implement.
func probeDecodeCodecs(c codec.Codec) []string {
	switch c {
	case codec.H264:
		return []string{"avc1.640028", "avc1.4D4028", "avc1.42E01F"}
	case codec.HEVC:
		return []string{"hev1.1.6.L120.90", "hvc1.1.6.L120.90"}
	case codec.AV1:
		return []string{"av01.0.08M.08"}
	}
	return nil
}

// h264Levels is Table A-1 of the H.264 specification, reduced to what the
// choice of a level needs: level_idc, the maximum frame size in macroblocks
// and the maximum macroblock rate. Levels below 3.1 are left out; no
// hardware encoder minds a small picture at level 3.1.
var h264Levels = []struct {
	idc     uint8
	maxFS   int
	maxMBPS int
}{
	{31, 3600, 108000},
	{32, 5120, 216000},
	{40, 8192, 245760},
	{42, 8704, 522240},
	{50, 22080, 589824},
	{51, 36864, 983040},
	{52, 36864, 2073600},
	{60, 139264, 4177920},
	{61, 139264, 8355840},
	{62, 139264, 16711680},
}

// h264Level returns the lowest level_idc (3.1 or above) that holds
// pictures of the given size at the given frame rate. A frame rate of 0
// counts as 30.
func h264Level(width, height int, fps float64) uint8 {
	if fps <= 0 {
		fps = 30
	}
	mbs := ((width + 15) / 16) * ((height + 15) / 16)
	rate := float64(mbs) * fps
	for _, l := range h264Levels {
		if mbs <= l.maxFS && rate <= float64(l.maxMBPS) {
			return l.idc
		}
	}
	return h264Levels[len(h264Levels)-1].idc
}

// hevcLevels is Table A.8 of the HEVC specification from level 3.1 on:
// general_level_idc, the maximum luma picture size and the maximum luma
// sample rate.
var hevcLevels = []struct {
	idc       uint8
	maxLumaPs int
	maxLumaSr float64
}{
	{93, 983040, 33177600},
	{120, 2228224, 66846720},
	{123, 2228224, 133693440},
	{150, 8912896, 267386880},
	{153, 8912896, 534773760},
	{156, 8912896, 1069547520},
	{180, 35651584, 1069547520},
	{183, 35651584, 2139095040},
	{186, 35651584, 4278190080},
}

// hevcLevel returns the lowest general_level_idc (3.1 or above) that holds
// pictures of the given size at the given frame rate.
func hevcLevel(width, height int, fps float64) uint8 {
	if fps <= 0 {
		fps = 30
	}
	ps := width * height
	sr := float64(ps) * fps
	for _, l := range hevcLevels {
		// A picture may also be at most sqrt(8*MaxLumaPs) wide or high.
		if ps <= l.maxLumaPs && width*width <= 8*l.maxLumaPs && height*height <= 8*l.maxLumaPs && sr <= l.maxLumaSr {
			return l.idc
		}
	}
	return hevcLevels[len(hevcLevels)-1].idc
}

// encodeCodecStrings lists the codec strings to offer the browser's
// encoder for a requested profile, most wanted first; ok is false when the
// codec does not define the profile. Without a requested profile H.264
// tries High, Main and Constrained Baseline in that order, since browsers
// differ in what their hardware encoders take.
func encodeCodecStrings(c codec.Codec, p codec.Profile, width, height int, fps float64) (candidates []string, ok bool) {
	switch c {
	case codec.H264:
		level := h264Level(width, height, fps)
		var (
			high     = h264CodecString(100, 0x00, level)
			main     = h264CodecString(77, 0x00, level)
			baseline = []string{h264CodecString(66, 0xE0, level), h264CodecString(66, 0x00, level)}
		)
		switch p {
		case codec.ProfileDefault:
			return append([]string{high, main}, baseline...), true
		case codec.ProfileHigh:
			return []string{high}, true
		case codec.ProfileMain:
			return []string{main}, true
		case codec.ProfileBaseline:
			return baseline, true
		}
	case codec.HEVC:
		if p != codec.ProfileDefault && p != codec.ProfileMain {
			return nil, false
		}
		// Main profile, main tier; B0 says progressive, non-packed,
		// frame-only, which is what an encoder fed whole frames makes.
		s := fmt.Sprintf("1.6.L%d.B0", hevcLevel(width, height, fps))
		return []string{"hvc1." + s, "hev1." + s}, true
	}
	return nil, false
}

// quantizer maps the public quality scale (0, 1] to the H.264/HEVC
// quantisation parameter the "quantizer" bitrate mode takes per frame, the
// same line the VA-API and NVENC backends use.
func quantizer(quality float64) int {
	return min(max(int(math.Round(51-45*quality)), 1), 51)
}

// The timestamps of the chunks given to a decoder are a counter in steps
// of timestampStep microseconds. The step keeps them distinct through the
// coarser clocks a browser may convert them to on the way (milliseconds,
// 90 kHz).
const timestampStep = 10_000

// toMicros converts a timestamp in TimeScale units to microseconds.
func toMicros(ts int64, timeScale int32) int64 {
	scale := int64(timeScale)
	if ts < 0 {
		return -((-ts*1_000_000 + scale/2) / scale)
	}
	return (ts*1_000_000 + scale/2) / scale
}
