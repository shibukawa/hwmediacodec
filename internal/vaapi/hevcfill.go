package vaapi

// This file translates parsed HEVC headers and the decoded picture buffer
// into VA-API parameter buffers. It does not call libva, so it builds and is
// tested on every platform.

import (
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// hevcMaxReferenceFrames is the size of
// VAPictureParameterBufferHEVC.ReferenceFrames.
const hevcMaxReferenceFrames = 15

// vaSurface is implemented by the picture handles the decoder stores in
// the DPB.
type vaSurface interface {
	// surfaceID returns the VASurfaceID that holds the picture.
	surfaceID() uint32
}

// vaPictureHEVC converts a DPB picture to a VAPictureHEVC entry.
func vaPictureHEVC(p *hevc.Picture, flags uint32) sys.PictureHEVC {
	if p == nil {
		return sys.InvalidPictureHEVC
	}
	s, ok := p.Handle.(vaSurface)
	if !ok {
		return sys.InvalidPictureHEVC
	}
	if p.LongTerm {
		flags |= sys.PictureHEVCLongTermReference
	}
	return sys.PictureHEVC{PictureID: s.surfaceID(), PicOrderCnt: p.POC, Flags: flags}
}

// fillHEVCPictureParameters builds VAPictureParameterBufferHEVC for the
// current picture. refs is DPB.Refs(): the pictures the current one may
// predict from in reference picture set order, then the ones kept for later
// pictures. ReferenceFrames keeps that order, so the driver's view of
// RefPicSetStCurrBefore, RefPicSetStCurrAfter and RefPicSetLtCurr has the
// order of the specification.
func fillHEVCPictureParameters(pp *sys.PictureParameterBufferHEVC, sps *hevc.SPS, pps *hevc.PPS, sh *hevc.SliceHeader, cur *hevc.Picture, dpb *hevc.DPB, refs []*hevc.Picture) {
	*pp = sys.PictureParameterBufferHEVC{}
	pp.CurrPic = vaPictureHEVC(cur, 0)
	pp.CurrPic.Flags &^= sys.PictureHEVCLongTermReference

	before, after, lt := dpb.RefPicSets()
	for i := range pp.ReferenceFrames {
		if i >= len(refs) {
			pp.ReferenceFrames[i] = sys.InvalidPictureHEVC
			continue
		}
		var flags uint32
		switch {
		case i < len(before):
			flags = sys.PictureHEVCRPSStCurrBefore
		case i < len(before)+len(after):
			flags = sys.PictureHEVCRPSStCurrAfter
		case i < len(before)+len(after)+len(lt):
			flags = sys.PictureHEVCRPSLtCurr
		}
		pp.ReferenceFrames[i] = vaPictureHEVC(refs[i], flags)
	}

	pp.PicWidthInLumaSamples = uint16(sps.Width)
	pp.PicHeightInLumaSamples = uint16(sps.Height)

	var pic uint32
	pic |= (sps.ChromaFormatIDC & 3) << sys.HEVCPicChromaFormatIDCShift
	pic |= b2u(sps.SeparateColourPlane) * sys.HEVCPicSeparateColourPlaneFlag
	pic |= b2u(sps.PCMEnabled) * sys.HEVCPicPcmEnabledFlag
	pic |= b2u(sps.ScalingListEnabled) * sys.HEVCPicScalingListEnabledFlag
	pic |= b2u(pps.TransformSkipEnabled) * sys.HEVCPicTransformSkipEnabledFlag
	pic |= b2u(sps.AMPEnabled) * sys.HEVCPicAmpEnabledFlag
	pic |= b2u(sps.StrongIntraSmoothingEnabled) * sys.HEVCPicStrongIntraSmoothingEnabledFlag
	pic |= b2u(pps.SignDataHidingEnabled) * sys.HEVCPicSignDataHidingEnabledFlag
	pic |= b2u(pps.ConstrainedIntraPred) * sys.HEVCPicConstrainedIntraPredFlag
	pic |= b2u(pps.CuQpDeltaEnabled) * sys.HEVCPicCuQpDeltaEnabledFlag
	pic |= b2u(pps.WeightedPred) * sys.HEVCPicWeightedPredFlag
	pic |= b2u(pps.WeightedBipred) * sys.HEVCPicWeightedBipredFlag
	pic |= b2u(pps.TransquantBypassEnabled) * sys.HEVCPicTransquantBypassEnabledFlag
	pic |= b2u(pps.TilesEnabled) * sys.HEVCPicTilesEnabledFlag
	pic |= b2u(pps.EntropyCodingSyncEnabled) * sys.HEVCPicEntropyCodingSyncEnabledFlag
	pic |= b2u(pps.LoopFilterAcrossSlicesEnabled) * sys.HEVCPicPpsLoopFilterAcrossSlicesEnabledFlag
	pic |= b2u(pps.LoopFilterAcrossTilesEnabled) * sys.HEVCPicLoopFilterAcrossTilesEnabledFlag
	pic |= b2u(sps.PCMLoopFilterDisabled) * sys.HEVCPicPcmLoopFilterDisabledFlag
	pp.PicFields = pic

	pp.SpsMaxDecPicBufferingMinus1 = uint8(sps.MaxDecPicBuffering() - 1)
	pp.BitDepthLumaMinus8 = uint8(sps.BitDepthLuma - 8)
	pp.BitDepthChromaMinus8 = uint8(sps.BitDepthChroma - 8)
	if sps.PCMEnabled {
		pp.PcmSampleBitDepthLumaMinus1 = uint8(sps.PCMSampleBitDepthLumaMinus1)
		pp.PcmSampleBitDepthChromaMinus1 = uint8(sps.PCMSampleBitDepthChromaMinus1)
		pp.Log2MinPcmLumaCodingBlockSizeMinus3 = uint8(sps.Log2MinPCMLumaCodingBlockSizeMinus3)
		pp.Log2DiffMaxMinPcmLumaCodingBlockSize = uint8(sps.Log2DiffMaxMinPCMLumaCodingBlockSize)
	}
	pp.Log2MinLumaCodingBlockSizeMinus3 = uint8(sps.Log2MinLumaCodingBlockSizeMinus3)
	pp.Log2DiffMaxMinLumaCodingBlockSize = uint8(sps.Log2DiffMaxMinLumaCodingBlockSize)
	pp.Log2MinTransformBlockSizeMinus2 = uint8(sps.Log2MinLumaTransformBlockSizeMinus2)
	pp.Log2DiffMaxMinTransformBlockSize = uint8(sps.Log2DiffMaxMinLumaTransformBlockSize)
	pp.MaxTransformHierarchyDepthIntra = uint8(sps.MaxTransformHierarchyDepthIntra)
	pp.MaxTransformHierarchyDepthInter = uint8(sps.MaxTransformHierarchyDepthInter)
	pp.InitQpMinus26 = int8(pps.InitQpMinus26)
	pp.DiffCuQpDeltaDepth = uint8(pps.DiffCuQpDeltaDepth)
	pp.PpsCbQpOffset = int8(pps.CbQpOffset)
	pp.PpsCrQpOffset = int8(pps.CrQpOffset)
	pp.Log2ParallelMergeLevelMinus2 = uint8(pps.Log2ParallelMergeLevelMinus2)
	if pps.TilesEnabled {
		pp.NumTileColumnsMinus1 = uint8(pps.NumTileColumns - 1)
		pp.NumTileRowsMinus1 = uint8(pps.NumTileRows - 1)
		for i, w := range pps.TileColumnWidths(sps) {
			if i < len(pp.ColumnWidthMinus1) {
				pp.ColumnWidthMinus1[i] = uint16(w - 1)
			}
		}
		for i, h := range pps.TileRowHeights(sps) {
			if i < len(pp.RowHeightMinus1) {
				pp.RowHeightMinus1[i] = uint16(h - 1)
			}
		}
	}

	irap := hevc.IsIRAP(sh.NALType)
	var sp uint32
	sp |= b2u(pps.ListsModificationPresent) * sys.HEVCSliceParsingListsModificationPresentFlag
	sp |= b2u(sps.LongTermRefPicsPresent) * sys.HEVCSliceParsingLongTermRefPicsPresentFlag
	sp |= b2u(sps.TemporalMVPEnabled) * sys.HEVCSliceParsingSpsTemporalMvpEnabledFlag
	sp |= b2u(pps.CabacInitPresent) * sys.HEVCSliceParsingCabacInitPresentFlag
	sp |= b2u(pps.OutputFlagPresent) * sys.HEVCSliceParsingOutputFlagPresentFlag
	sp |= b2u(pps.DependentSliceSegmentsEnabled) * sys.HEVCSliceParsingDependentSliceSegmentsEnabled
	sp |= b2u(pps.SliceChromaQpOffsetsPresent) * sys.HEVCSliceParsingPpsSliceChromaQpOffsetsPresent
	sp |= b2u(sps.SAOEnabled) * sys.HEVCSliceParsingSampleAdaptiveOffsetEnabled
	sp |= b2u(pps.DeblockingFilterOverrideEnabled) * sys.HEVCSliceParsingDeblockingFilterOverrideEnabled
	sp |= b2u(pps.DeblockingFilterDisabled) * sys.HEVCSliceParsingPpsDisableDeblockingFilter
	sp |= b2u(pps.SliceSegmentHeaderExtensionPresent) * sys.HEVCSliceParsingSliceSegmentHeaderExtPresent
	sp |= b2u(irap) * sys.HEVCSliceParsingRapPicFlag
	sp |= b2u(hevc.IsIDR(sh.NALType)) * sys.HEVCSliceParsingIdrPicFlag
	sp |= b2u(irap) * sys.HEVCSliceParsingIntraPicFlag
	pp.SliceParsingFields = sp

	pp.Log2MaxPicOrderCntLsbMinus4 = uint8(sps.Log2MaxPOCLsb - 4)
	pp.NumShortTermRefPicSets = uint8(len(sps.ShortTermRPS))
	pp.NumLongTermRefPicSps = uint8(len(sps.LtRefPicPOCLsbSPS))
	pp.NumRefIdxL0DefaultActiveMinus1 = uint8(pps.NumRefIdxL0DefaultActive - 1)
	pp.NumRefIdxL1DefaultActiveMinus1 = uint8(pps.NumRefIdxL1DefaultActive - 1)
	pp.PpsBetaOffsetDiv2 = int8(pps.BetaOffsetDiv2)
	pp.PpsTcOffsetDiv2 = int8(pps.TcOffsetDiv2)
	pp.NumExtraSliceHeaderBits = uint8(pps.NumExtraSliceHeaderBits)
	// Drivers that parse the slice header themselves (AMD) skip the set
	// coded in it by this size.
	if !sh.ShortTermRefPicSetSPSFlag {
		pp.StRpsBits = uint32(sh.ShortTermRPSBits)
	}
}

// fillHEVCIQMatrix copies the scaling lists in effect; both sides keep them
// in coded (up-right diagonal) order.
func fillHEVCIQMatrix(iq *sys.IQMatrixBufferHEVC, lists *hevc.ScalingList) {
	*iq = sys.IQMatrixBufferHEVC{}
	iq.ScalingList4x4 = lists.L4
	iq.ScalingList8x8 = lists.L8
	iq.ScalingList16x16 = lists.L16
	iq.ScalingList32x32 = lists.L32
	iq.ScalingListDC16x16 = lists.DC16
	iq.ScalingListDC32x32 = lists.DC32
}

// fillHEVCSliceParameters builds VASliceParameterBufferHEVC for one slice
// segment. refs is the order of ReferenceFrames in the picture parameters;
// the reference lists are given as indices into it.
func fillHEVCSliceParameters(sp *sys.SliceParameterBufferHEVC, sps *hevc.SPS, sh *hevc.SliceHeader, nalSize int, l0, l1, refs []*hevc.Picture, last bool) {
	*sp = sys.SliceParameterBufferHEVC{}
	sp.SliceDataSize = uint32(nalSize)
	sp.SliceDataOffset = 0
	sp.SliceDataFlag = sys.SliceDataFlagAll
	sp.SliceDataByteOffset = uint32(sh.DataByteOffset())
	sp.SliceSegmentAddress = sh.SliceSegmentAddress

	for list := range sp.RefPicList {
		for i := range sp.RefPicList[list] {
			sp.RefPicList[list][i] = sys.HEVCRefPicListUnused
		}
	}
	index := func(p *hevc.Picture) uint8 {
		for i, r := range refs {
			if r == p && i < hevcMaxReferenceFrames {
				return uint8(i)
			}
		}
		return sys.HEVCRefPicListUnused
	}
	for i, p := range l0 {
		if i < len(sp.RefPicList[0]) && p != nil {
			sp.RefPicList[0][i] = index(p)
		}
	}
	for i, p := range l1 {
		if i < len(sp.RefPicList[1]) && p != nil {
			sp.RefPicList[1][i] = index(p)
		}
	}

	chroma := sps.ChromaFormatIDC != 0 && !sps.SeparateColourPlane
	var flags uint32
	flags |= b2u(last) * sys.HEVCSliceLastSliceOfPic
	flags |= b2u(sh.DependentSliceSegment) * sys.HEVCSliceDependentSliceSegmentFlag
	flags |= (sh.SliceType & 3) << sys.HEVCSliceSliceTypeShift
	flags |= (sh.ColourPlaneID & 3) << sys.HEVCSliceColorPlaneIDShift
	flags |= b2u(sh.SAOLuma) * sys.HEVCSliceSaoLumaFlag
	flags |= b2u(sh.SAOChroma && chroma) * sys.HEVCSliceSaoChromaFlag
	flags |= b2u(sh.MvdL1Zero) * sys.HEVCSliceMvdL1ZeroFlag
	flags |= b2u(sh.CabacInit) * sys.HEVCSliceCabacInitFlag
	flags |= b2u(sh.SliceTemporalMVPEnabled) * sys.HEVCSliceTemporalMvpEnabledFlag
	flags |= b2u(sh.DeblockingFilterDisabled) * sys.HEVCSliceDeblockingFilterDisabledFlag
	flags |= b2u(sh.CollocatedFromL0) * sys.HEVCSliceCollocatedFromL0Flag
	flags |= b2u(sh.LoopFilterAcrossSlicesEnabled) * sys.HEVCSliceLoopFilterAcrossSlicesEnabledFlag
	sp.LongSliceFlags = flags

	sp.CollocatedRefIdx = 0xff
	if sh.SliceTemporalMVPEnabled {
		sp.CollocatedRefIdx = uint8(sh.CollocatedRefIdx)
	}
	if sh.NumRefIdxL0Active > 0 {
		sp.NumRefIdxL0ActiveMinus1 = uint8(sh.NumRefIdxL0Active - 1)
	}
	if sh.NumRefIdxL1Active > 0 {
		sp.NumRefIdxL1ActiveMinus1 = uint8(sh.NumRefIdxL1Active - 1)
	}
	sp.SliceQpDelta = int8(sh.SliceQpDelta)
	sp.SliceCbQpOffset = int8(sh.SliceCbQpOffset)
	sp.SliceCrQpOffset = int8(sh.SliceCrQpOffset)
	sp.SliceBetaOffsetDiv2 = int8(sh.BetaOffsetDiv2)
	sp.SliceTcOffsetDiv2 = int8(sh.TcOffsetDiv2)
	if sh.SliceType != hevc.SliceI {
		sp.FiveMinusMaxNumMergeCand = uint8(sh.FiveMinusMaxNumMergeCand)
	}
	// num_entry_point_offsets and entry_offset_to_subset_array stay zero:
	// they describe a VASubsetsParameterBufferHEVC, which is not sent.
	sp.SliceDataNumEmuPrevnBytes = uint16(sh.EmulationPreventionBytes)

	// Prediction weights are only coded (and only meaningful) when the PPS
	// enables weighted prediction for the slice type.
	if w := sh.PredWeights; w != nil {
		sp.LumaLog2WeightDenom = uint8(w.LumaLog2WeightDenom)
		if chroma {
			sp.DeltaChromaLog2WeightDenom = int8(w.DeltaChromaLog2WeightDenom)
		}
		for i := 0; i < int(sh.NumRefIdxL0Active) && i < len(w.L0); i++ {
			e := &w.L0[i]
			sp.DeltaLumaWeightL0[i] = int8(e.DeltaLumaWeight)
			sp.LumaOffsetL0[i] = int8(e.LumaOffset)
			for j := 0; j < 2; j++ {
				sp.DeltaChromaWeightL0[i][j] = int8(e.DeltaChromaWeight[j])
				sp.ChromaOffsetL0[i][j] = int8(e.ChromaOffset[j])
			}
		}
		if sh.SliceType == hevc.SliceB {
			for i := 0; i < int(sh.NumRefIdxL1Active) && i < len(w.L1); i++ {
				e := &w.L1[i]
				sp.DeltaLumaWeightL1[i] = int8(e.DeltaLumaWeight)
				sp.LumaOffsetL1[i] = int8(e.LumaOffset)
				for j := 0; j < 2; j++ {
					sp.DeltaChromaWeightL1[i][j] = int8(e.DeltaChromaWeight[j])
					sp.ChromaOffsetL1[i][j] = int8(e.ChromaOffset[j])
				}
			}
		}
	}
}
