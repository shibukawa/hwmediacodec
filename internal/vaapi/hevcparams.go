package vaapi

// This file holds the parts of the HEVC encoder that do not touch libva, so
// that they build and are tested on every platform.

import (
	"math"

	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// encLog2MaxHEVCPOCLsb sizes slice_pic_order_cnt_lsb in the HEVC streams
// this encoder writes.
const encLog2MaxHEVCPOCLsb = 12

// hevcTools is the coding structure an HEVC encoder is configured with.
type hevcTools struct {
	ctbLog2, minCbLog2     int
	minTbLog2, maxTbLog2   int
	depthInter, depthIntra int

	amp, sao, strongIntraSmoothing bool
	signDataHiding, transformSkip  bool
	constrainedIntraPred           bool
	transquantBypass               bool
	cuQpDelta                      bool
}

// hevcToolsFor derives the coding structure from the driver's
// VAConfigAttribEncHEVCBlockSizes and VAConfigAttribEncHEVCFeatures values
// (hasBlocks and hasFeatures tell whether the driver reports them). Without
// them it falls back to the structure every known driver accepts: 32x32
// coding tree blocks, 16x16 minimum coding blocks, asymmetric partitions,
// no SAO.
func hevcToolsFor(blocks uint32, hasBlocks bool, features uint32, hasFeatures bool, rateControlled bool) hevcTools {
	t := hevcTools{ctbLog2: 5, minCbLog2: 4, minTbLog2: 2, maxTbLog2: 5, depthInter: 3, depthIntra: 3, amp: true}
	if hasBlocks {
		field := func(shift uint) int { return int((blocks >> shift) & 3) }
		t.ctbLog2 = field(sys.HEVCBlockLog2MaxCodingTreeBlockSizeMinus3Shift) + 3
		t.minCbLog2 = field(sys.HEVCBlockLog2MinLumaCodingBlockSizeMinus3Shift) + 3
		t.minTbLog2 = field(sys.HEVCBlockLog2MinLumaTransformBlockSizeMinus2Shift) + 2
		t.maxTbLog2 = field(sys.HEVCBlockLog2MaxLumaTransformBlockSizeMinus2Shift) + 2
		t.depthInter = field(sys.HEVCBlockMaxMaxTransformHierarchyDepthInterShift)
		t.depthIntra = field(sys.HEVCBlockMaxMaxTransformHierarchyDepthIntraShift)
	}
	// Keep the structure inside the limits of the specification whatever
	// the driver reports (7.4.3.2.1).
	t.ctbLog2 = min(max(t.ctbLog2, 4), 6)
	t.minCbLog2 = min(max(t.minCbLog2, 3), t.ctbLog2)
	t.maxTbLog2 = min(max(t.maxTbLog2, 2), min(t.ctbLog2, 5))
	t.minTbLog2 = min(max(t.minTbLog2, 2), min(t.maxTbLog2, t.minCbLog2-1))
	t.depthInter = min(t.depthInter, t.ctbLog2-t.minTbLog2)
	t.depthIntra = min(t.depthIntra, t.ctbLog2-t.minTbLog2)

	t.cuQpDelta = rateControlled
	if hasFeatures {
		supported := func(shift uint) bool { return sys.EncHEVCFeature(features, shift) != 0 }
		required := func(shift uint) bool { return sys.EncHEVCFeature(features, shift) == 2 }
		t.amp = supported(sys.HEVCFeatureAmpShift)
		t.sao = supported(sys.HEVCFeatureSaoShift)
		t.strongIntraSmoothing = required(sys.HEVCFeatureStrongIntraSmoothingShift)
		t.signDataHiding = required(sys.HEVCFeatureSignDataHidingShift)
		t.transformSkip = required(sys.HEVCFeatureTransformSkipShift)
		t.constrainedIntraPred = required(sys.HEVCFeatureConstrainedIntraPredShift)
		t.transquantBypass = required(sys.HEVCFeatureTransquantBypassShift)
		t.cuQpDelta = rateControlled || required(sys.HEVCFeatureCuQpDeltaShift)
	}
	// Asymmetric partitions need a coding block larger than the minimum.
	if t.minCbLog2 == t.ctbLog2 {
		t.amp = false
	}
	return t
}

// hevcParameterSets builds the parameter sets of a Main profile stream of
// I and P pictures in which every P picture references the picture before
// it: one short-term reference picture set {-1} in the SPS, no reordering.
// fpsNum/fpsDen is the frame rate, or 0/0 when it is unknown.
func hevcParameterSets(width, height int, fpsNum, fpsDen uint32, bitrateKbps, qp int, t hevcTools) (*hevc.VPS, *hevc.SPS, *hevc.PPS) {
	minCb := 1 << uint(t.minCbLog2)
	codedW := (width + minCb - 1) / minCb * minCb
	codedH := (height + minCb - 1) / minCb * minCb

	ptl := hevc.ProfileTierLevel{
		ProfileIDC: hevc.ProfileMain,
		// Main streams are also Main 10 streams.
		CompatibilityFlags:  1<<(31-hevc.ProfileMain) | 1<<(31-hevc.ProfileMain10),
		ProgressiveSource:   true,
		NonPackedConstraint: true,
		FrameOnlyConstraint: true,
		LevelIDC:            hevcLevelIDC(codedW, codedH, fpsNum, fpsDen, bitrateKbps),
	}
	num, den := fpsNum, fpsDen

	vps := &hevc.VPS{TemporalIDNesting: true, PTL: ptl, MaxDecPicBufferingMinus1: 1}
	sps := &hevc.SPS{
		TemporalIDNesting:                    true,
		PTL:                                  ptl,
		ChromaFormatIDC:                      1,
		Width:                                codedW,
		Height:                               codedH,
		BitDepthLuma:                         8,
		BitDepthChroma:                       8,
		Log2MaxPOCLsb:                        encLog2MaxHEVCPOCLsb,
		MaxDecPicBufferingMinus1:             []uint32{1},
		MaxNumReorderPics:                    []uint32{0},
		MaxLatencyIncreasePlus1:              []uint32{0},
		Log2MinLumaCodingBlockSizeMinus3:     uint32(t.minCbLog2 - 3),
		Log2DiffMaxMinLumaCodingBlockSize:    uint32(t.ctbLog2 - t.minCbLog2),
		Log2MinLumaTransformBlockSizeMinus2:  uint32(t.minTbLog2 - 2),
		Log2DiffMaxMinLumaTransformBlockSize: uint32(t.maxTbLog2 - t.minTbLog2),
		MaxTransformHierarchyDepthInter:      uint32(t.depthInter),
		MaxTransformHierarchyDepthIntra:      uint32(t.depthIntra),
		AMPEnabled:                           t.amp,
		SAOEnabled:                           t.sao,
		StrongIntraSmoothingEnabled:          t.strongIntraSmoothing,
		VUIPresent:                           true,
	}
	prev := hevc.ShortTermRPS{NumNegativePics: 1}
	prev.DeltaPocS0[0] = -1
	prev.UsedByCurrPicS0[0] = true
	sps.ShortTermRPS = []hevc.ShortTermRPS{prev}
	if codedW != width || codedH != height {
		// Conformance window offsets count chroma samples (4:2:0).
		sps.ConformanceWindow = true
		sps.ConfWinRight = uint32(codedW-width) / 2
		sps.ConfWinBottom = uint32(codedH-height) / 2
	}
	sps.VUI = hevc.VUI{
		BitstreamRestriction:           true,
		MotionVectorsOverPicBoundaries: true,
		RestrictedRefPicLists:          true,
		Log2MaxMvLengthHorizontal:      15,
		Log2MaxMvLengthVertical:        15,
	}
	if num > 0 {
		sps.VUI.TimingInfoPresent = true
		sps.VUI.NumUnitsInTick = den
		sps.VUI.TimeScale = num
		vps.TimingInfoPresent = true
		vps.NumUnitsInTick = den
		vps.TimeScale = num
	}
	pps := &hevc.PPS{
		SignDataHidingEnabled:         t.signDataHiding,
		NumRefIdxL0DefaultActive:      1,
		NumRefIdxL1DefaultActive:      1,
		InitQpMinus26:                 int32(qp - 26),
		ConstrainedIntraPred:          t.constrainedIntraPred,
		TransformSkipEnabled:          t.transformSkip,
		CuQpDeltaEnabled:              t.cuQpDelta,
		TransquantBypassEnabled:       t.transquantBypass,
		NumTileColumns:                1,
		NumTileRows:                   1,
		UniformSpacing:                true,
		LoopFilterAcrossTilesEnabled:  true,
		LoopFilterAcrossSlicesEnabled: true,
	}
	return vps, sps, pps
}

// hevcLevelIDC picks the lowest level of Table A.8 (Main tier) that fits
// the picture size, the luma sample rate and the bitrate.
func hevcLevelIDC(width, height int, fpsNum, fpsDen uint32, bitrateKbps int) uint8 {
	levels := []struct {
		idc        uint8
		maxLumaPs  int
		maxLumaSr  int64
		maxBitrate int // kbit/s, Main tier
	}{
		{30, 36864, 552960, 128}, {60, 122880, 3686400, 1500}, {63, 245760, 7372800, 3000},
		{90, 552960, 16588800, 6000}, {93, 983040, 33177600, 10000},
		{120, 2228224, 66846720, 12000}, {123, 2228224, 133693440, 20000},
		{150, 8912896, 267386880, 25000}, {153, 8912896, 534773760, 40000}, {156, 8912896, 1069547520, 60000},
		{180, 35651584, 1069547520, 60000}, {183, 35651584, 2139095040, 120000}, {186, 35651584, 4278190080, 240000},
	}
	lumaPs := width * height
	var lumaSr int64
	if fpsNum > 0 && fpsDen > 0 {
		lumaSr = (int64(lumaPs)*int64(fpsNum) + int64(fpsDen) - 1) / int64(fpsDen)
	}
	for _, l := range levels {
		// A.4.1: neither dimension may exceed Sqrt(MaxLumaPs * 8).
		side := int(math.Sqrt(float64(l.maxLumaPs) * 8))
		if lumaPs <= l.maxLumaPs && width <= side && height <= side && lumaSr <= l.maxLumaSr && bitrateKbps <= l.maxBitrate {
			return l.idc
		}
	}
	return 186
}

// hevcSliceHeader describes the single slice segment of the picture with
// picture order count poc: an IDR picture, or a P picture that references
// the picture before it. It drives both the VA slice parameters and the
// packed slice header.
func hevcSliceHeader(sps *hevc.SPS, pps *hevc.PPS, idr bool, poc int) *hevc.SliceHeader {
	sh := &hevc.SliceHeader{
		FirstSliceSegmentInPic:        true,
		PPSID:                         pps.ID,
		PicOutput:                     true,
		SAOLuma:                       sps.SAOEnabled,
		SAOChroma:                     sps.SAOEnabled,
		CollocatedFromL0:              true,
		LoopFilterAcrossSlicesEnabled: pps.LoopFilterAcrossSlicesEnabled,
	}
	if idr {
		sh.NALType, sh.SliceType = hevc.NALIDRWRADL, hevc.SliceI
		return sh
	}
	sh.NALType, sh.SliceType = hevc.NALTrailR, hevc.SliceP
	sh.POCLsb = uint32(poc % (1 << encLog2MaxHEVCPOCLsb))
	sh.ShortTermRefPicSetSPSFlag = true
	sh.ShortTermRPS = &sps.ShortTermRPS[0]
	sh.NumRefIdxL0Active = 1
	sh.NumPicTotalCurr = 1
	return sh
}
