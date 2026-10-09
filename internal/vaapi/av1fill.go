package vaapi

// This file translates a parsed AV1 frame header into VA-API parameter
// buffers. It does not call libva, so it builds and is tested on every
// platform.

import (
	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// fillAV1PictureParameters describes one frame to the driver. recon is the
// surface the frame is reconstructed into, the one later frames reference;
// display is the surface to show, which differs from it only when film
// grain is applied. refs holds the surface of every reference slot.
func fillAV1PictureParameters(pp *sys.DecPictureParameterBufferAV1, seq *av1.SequenceHeader, h *av1.Header, recon, display uint32, refs *[av1.NumRefFrames]uint32) {
	*pp = sys.DecPictureParameterBufferAV1{
		Profile:               seq.Profile,
		BitDepthIdx:           uint8((seq.BitDepth - 8) / 2),
		MatrixCoefficients:    seq.MatrixCoefficients,
		CurrentFrame:          recon,
		CurrentDisplayPicture: display,
		FrameWidthMinus1:      uint16(h.UpscaledWidth - 1),
		FrameHeightMinus1:     uint16(h.FrameHeight - 1),
		RefFrameIdx:           h.RefFrameIdx,
		PrimaryRefFrame:       h.PrimaryRefFrame,
		OrderHint:             h.OrderHint,
		TileCols:              uint8(h.TileCols),
		TileRows:              uint8(h.TileRows),
		ContextUpdateTileID:   h.ContextUpdateTileID,

		SuperresScaleDenominator: h.SuperresDenom,
		InterpFilter:             h.InterpolationFilter,
		FilterLevel:              [2]uint8{h.LoopFilterLevel[0], h.LoopFilterLevel[1]},
		FilterLevelU:             h.LoopFilterLevel[2],
		FilterLevelV:             h.LoopFilterLevel[3],
		RefDeltas:                h.LoopFilterRefDeltas,
		ModeDeltas:               h.LoopFilterModeDeltas,
		BaseQIndex:               h.BaseQIdx,
		YDcDeltaQ:                h.DeltaQYDc,
		UDcDeltaQ:                h.DeltaQUDc,
		UAcDeltaQ:                h.DeltaQUAc,
		VDcDeltaQ:                h.DeltaQVDc,
		VAcDeltaQ:                h.DeltaQVAc,
		CDEFDampingMinus3:        h.CDEFDampingMinus3,
		CDEFBits:                 h.CDEFBits,
	}
	if seq.OrderHintBits > 0 {
		pp.OrderHintBitsMinus1 = seq.OrderHintBits - 1
	}
	pp.SeqInfoFields = b2u(seq.StillPicture) |
		b2u(seq.Use128x128Superblock)<<1 |
		b2u(seq.EnableFilterIntra)<<2 |
		b2u(seq.EnableIntraEdgeFilter)<<3 |
		b2u(seq.EnableInterintraCompound)<<4 |
		b2u(seq.EnableMaskedCompound)<<5 |
		b2u(seq.EnableDualFilter)<<6 |
		b2u(seq.EnableOrderHint)<<7 |
		b2u(seq.EnableJntComp)<<8 |
		b2u(seq.EnableCDEF)<<9 |
		b2u(seq.MonoChrome)<<10 |
		b2u(seq.FullRange)<<11 |
		uint32(seq.SubsamplingX&1)<<12 |
		uint32(seq.SubsamplingY&1)<<13 |
		uint32(seq.ChromaSamplePosition&1)<<14 |
		b2u(seq.FilmGrainParamsPresent)<<15
	pp.PicInfoFields = uint32(h.FrameType)&3 |
		b2u(h.ShowFrame)<<2 |
		b2u(h.ShowableFrame)<<3 |
		b2u(h.ErrorResilientMode)<<4 |
		b2u(h.DisableCDFUpdate)<<5 |
		b2u(h.AllowScreenContent)<<6 |
		b2u(h.ForceIntegerMV)<<7 |
		b2u(h.AllowIntraBC)<<8 |
		b2u(h.UseSuperres)<<9 |
		b2u(h.AllowHighPrecMV)<<10 |
		b2u(h.MotionModeSwitch)<<11 |
		b2u(h.UseRefFrameMVs)<<12 |
		b2u(h.DisableFrameEndCDF)<<13 |
		b2u(h.UniformTileSpacing)<<14 |
		b2u(h.AllowWarpedMotion)<<15
	pp.LoopFilterInfoFields = h.LoopFilterSharpness&7 |
		b2u8(h.LoopFilterDeltaEnabled)<<3 |
		b2u8(h.LoopFilterDeltaUpdate)<<4
	pp.QMatrixFields = uint16(b2u(h.UsingQMatrix)) |
		uint16(h.QmY&15)<<1 |
		uint16(h.QmU&15)<<5 |
		uint16(h.QmV&15)<<9
	pp.ModeControlFields = b2u(h.DeltaQPresent) |
		uint32(h.DeltaQRes&3)<<1 |
		b2u(h.DeltaLFPresent)<<3 |
		uint32(h.DeltaLFRes&3)<<4 |
		b2u(h.DeltaLFMulti)<<6 |
		uint32(h.TxMode&3)<<7 |
		b2u(h.ReferenceSelect)<<9 |
		b2u(h.ReducedTxSet)<<10 |
		b2u(h.SkipModePresent)<<11
	pp.LoopRestorationFields = uint16(h.FrameRestorationType[0]&3) |
		uint16(h.FrameRestorationType[1]&3)<<2 |
		uint16(h.FrameRestorationType[2]&3)<<4 |
		uint16(h.LrUnitShift&3)<<6 |
		uint16(h.LrUVShift&1)<<8

	// A shown key frame starts from nothing; every other frame may name
	// any slot.
	for i := range pp.RefFrameMap {
		pp.RefFrameMap[i] = sys.InvalidSurface
		if !(h.FrameType == av1.KeyFrame && h.ShowFrame) {
			pp.RefFrameMap[i] = refs[i]
		}
	}
	for i := 0; i < 1<<h.CDEFBits; i++ {
		pp.CDEFYStrengths[i] = h.CDEFYPriStrength[i]<<2 | h.CDEFYSecStrength[i]
		pp.CDEFUVStrengths[i] = h.CDEFUVPriStrength[i]<<2 | h.CDEFUVSecStrength[i]
	}
	// The arrays have room for 63 tiles each way; a 64th column or row is
	// implied by the frame size.
	for i := 0; i < min(h.TileCols, len(pp.WidthInSbsMinus1)); i++ {
		pp.WidthInSbsMinus1[i] = h.WidthInSbsMinus1[i]
	}
	for i := 0; i < min(h.TileRows, len(pp.HeightInSbsMinus1)); i++ {
		pp.HeightInSbsMinus1[i] = h.HeightInSbsMinus1[i]
	}
	for ref := av1.RefLast; ref <= av1.RefAltref; ref++ {
		wm := &pp.WM[ref-av1.RefLast]
		wm.WMType = uint32(h.GmType[ref])
		wm.Invalid = b2u8(h.GmInvalid[ref])
		copy(wm.WMMat[:6], h.GmParams[ref][:])
	}

	seg := &pp.SegInfo
	seg.SegmentInfoFields = b2u(h.SegmentationEnabled) |
		b2u(h.SegmentationUpdateMap)<<1 |
		b2u(h.SegmentationTemporalUpdate)<<2 |
		b2u(h.SegmentationUpdateData)<<3
	seg.FeatureData = h.FeatureData
	for i := range h.FeatureEnabled {
		for j, on := range h.FeatureEnabled[i] {
			seg.FeatureMask[i] |= b2u8(on) << uint(j)
		}
	}

	g := &h.FilmGrain
	fg := &pp.FilmGrainInfo
	fg.FilmGrainInfoFields = b2u(g.ApplyGrain) |
		b2u(g.ChromaScalingFromLuma)<<1 |
		uint32(g.GrainScalingMinus8&3)<<2 |
		uint32(g.ARCoeffLag&3)<<4 |
		uint32(g.ARCoeffShiftMinus6&3)<<6 |
		uint32(g.GrainScaleShift&3)<<8 |
		b2u(g.OverlapFlag)<<10 |
		b2u(g.ClipToRestrictedRange)<<11
	fg.GrainSeed = g.GrainSeed
	fg.NumYPoints, fg.NumCbPoints, fg.NumCrPoints = g.NumYPoints, g.NumCbPoints, g.NumCrPoints
	fg.CbMult, fg.CbLumaMult, fg.CbOffset = g.CbMult, g.CbLumaMult, g.CbOffset
	fg.CrMult, fg.CrLumaMult, fg.CrOffset = g.CrMult, g.CrLumaMult, g.CrOffset
	if g.ApplyGrain {
		fg.PointYValue, fg.PointYScaling = g.PointYValue, g.PointYScaling
		fg.PointCbValue, fg.PointCbScaling = g.PointCbValue, g.PointCbScaling
		fg.PointCrValue, fg.PointCrScaling = g.PointCrValue, g.PointCrScaling
		for i, c := range g.ARCoeffsYPlus128 {
			fg.ARCoeffsY[i] = int8(int(c) - 128)
		}
		for i := range g.ARCoeffsCbPlus128 {
			fg.ARCoeffsCb[i] = int8(int(g.ARCoeffsCbPlus128[i]) - 128)
			fg.ARCoeffsCr[i] = int8(int(g.ARCoeffsCrPlus128[i]) - 128)
		}
	}
}

// appendAV1TileParameters appends one element per tile of a tile group.
// The offsets count into tg.Data, which is submitted as the slice data
// buffer that follows the parameters.
func appendAV1TileParameters(dst []sys.SliceParameterBufferAV1, tg *av1.TileGroup) []sys.SliceParameterBufferAV1 {
	for _, tile := range tg.Tiles {
		dst = append(dst, sys.SliceParameterBufferAV1{
			SliceDataSize:   uint32(tile.Size),
			SliceDataOffset: uint32(tile.Offset),
			SliceDataFlag:   sys.SliceDataFlagAll,
			TileRow:         uint16(tile.Row),
			TileColumn:      uint16(tile.Col),
			TgStart:         uint16(tg.Start),
			TgEnd:           uint16(tg.End),
		})
	}
	return dst
}
