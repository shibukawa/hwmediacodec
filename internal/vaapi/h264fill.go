//go:build linux

package vaapi

import (
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// vaPicture converts a DPB picture to a VAPictureH264 entry.
func vaPicture(p *h264.Picture) sys.PictureH264 {
	if p == nil {
		return sys.InvalidPictureH264
	}
	s, ok := p.Handle.(*surface)
	if !ok {
		return sys.InvalidPictureH264
	}
	out := sys.PictureH264{PictureID: s.id, TopFieldOrderCnt: int32(p.TopPOC), BottomFieldOrderCnt: int32(p.BottomPOC)}
	switch {
	case p.LongTerm:
		out.FrameIdx = uint32(p.LongTermFrameIdx)
		out.Flags |= sys.PictureH264LongTermReference
	case p.Reference:
		out.FrameIdx = uint32(p.FrameNum)
		out.Flags |= sys.PictureH264ShortTermReference
	default:
		out.FrameIdx = uint32(p.FrameNum)
	}
	if p.NonExisting {
		out.Flags |= sys.PictureH264NonExisting
	}
	return out
}

// fillPictureParameters builds VAPictureParameterBufferH264 for the current
// picture.
func fillPictureParameters(pp *sys.PictureParameterBufferH264, sps *h264.SPS, pps *h264.PPS, sh *h264.SliceHeader, cur *h264.Picture, refs []*h264.Picture) {
	*pp = sys.PictureParameterBufferH264{}
	pp.CurrPic = vaPicture(cur)
	pp.CurrPic.Flags &^= sys.PictureH264LongTermReference
	if cur.Reference {
		pp.CurrPic.Flags |= sys.PictureH264ShortTermReference
	}
	n := 0
	for _, r := range refs {
		if n == len(pp.ReferenceFrames) {
			break
		}
		if !r.Reference {
			continue
		}
		pp.ReferenceFrames[n] = vaPicture(r)
		n++
	}
	for ; n < len(pp.ReferenceFrames); n++ {
		pp.ReferenceFrames[n] = sys.InvalidPictureH264
	}

	pp.PictureWidthInMbsMinus1 = uint16(sps.PicWidthInMbs - 1)
	pp.PictureHeightInMbsMinus1 = uint16(sps.FrameHeightInMbs() - 1)
	pp.BitDepthLumaMinus8 = uint8(sps.BitDepthLuma - 8)
	pp.BitDepthChromaMinus8 = uint8(sps.BitDepthChroma - 8)
	pp.NumRefFrames = uint8(sps.MaxNumRefFrames)

	var seq uint32
	seq |= (sps.ChromaFormatIDC & 3) << sys.SeqChromaFormatIDCShift
	seq |= b2u(sps.SeparateColourPlane) * sys.SeqResidualColourTransformFlag
	seq |= b2u(sps.GapsInFrameNumAllowed) * sys.SeqGapsInFrameNumValueAllowedFlag
	seq |= b2u(sps.FrameMbsOnly) * sys.SeqFrameMbsOnlyFlag
	seq |= b2u(sps.MbAdaptiveFrameField) * sys.SeqMbAdaptiveFrameFieldFlag
	seq |= b2u(sps.Direct8x8Inference) * sys.SeqDirect8x8InferenceFlag
	seq |= b2u(sps.LevelIDC >= 31) * sys.SeqMinLumaBiPredSize8x8 // A.3.3.2
	seq |= ((sps.Log2MaxFrameNum - 4) & 0xf) << sys.SeqLog2MaxFrameNumMinus4Shift
	seq |= (sps.PicOrderCntType & 3) << sys.SeqPicOrderCntTypeShift
	if sps.PicOrderCntType == 0 {
		seq |= ((sps.Log2MaxPicOrderCntLsb - 4) & 0xf) << sys.SeqLog2MaxPicOrderCntLsbMinus4Shift
	}
	seq |= b2u(sps.DeltaPicOrderAlwaysZero) * sys.SeqDeltaPicOrderAlwaysZeroFlag
	pp.SeqFields = seq

	pp.NumSliceGroupsMinus1 = uint8(pps.NumSliceGroups - 1)
	pp.SliceGroupMapType = uint8(pps.SliceGroupMapType)
	if pps.SliceGroupChangeRate > 0 {
		pp.SliceGroupChangeRateMinus1 = uint16(pps.SliceGroupChangeRate - 1)
	}
	pp.PicInitQpMinus26 = int8(pps.PicInitQpMinus26)
	pp.PicInitQsMinus26 = int8(pps.PicInitQsMinus26)
	pp.ChromaQpIndexOffset = int8(pps.ChromaQpIndexOffset)
	pp.SecondChromaQpIndexOffset = int8(pps.SecondChromaQpIndexOffset)

	var pic uint32
	pic |= b2u(pps.EntropyCodingMode) * sys.PicEntropyCodingModeFlag
	pic |= b2u(pps.WeightedPred) * sys.PicWeightedPredFlag
	pic |= (pps.WeightedBipredIdc & 3) << sys.PicWeightedBipredIdcShift
	pic |= b2u(pps.Transform8x8Mode) * sys.PicTransform8x8ModeFlag
	pic |= b2u(sh.FieldPic) * sys.PicFieldPicFlag
	pic |= b2u(pps.ConstrainedIntraPred) * sys.PicConstrainedIntraPredFlag
	pic |= b2u(pps.BottomFieldPicOrderInFramePresent) * sys.PicPicOrderPresentFlag
	pic |= b2u(pps.DeblockingFilterControlPresent) * sys.PicDeblockingFilterControlPresentFlag
	pic |= b2u(pps.RedundantPicCntPresent) * sys.PicRedundantPicCntPresentFlag
	pic |= b2u(sh.NALRefIdc != 0) * sys.PicReferencePicFlag
	pp.PicFields = pic
	pp.FrameNum = uint16(sh.FrameNum)
}

// fillIQMatrix copies the scaling lists in effect (raster order).
func fillIQMatrix(iq *sys.IQMatrixBufferH264, lists *h264.ScalingLists) {
	*iq = sys.IQMatrixBufferH264{}
	iq.ScalingList4x4 = lists.L4
	iq.ScalingList8x8[0] = lists.L8[0]
	iq.ScalingList8x8[1] = lists.L8[1]
}

// fillSliceParameters builds VASliceParameterBufferH264 for one slice.
func fillSliceParameters(sp *sys.SliceParameterBufferH264, sh *h264.SliceHeader, nalSize int, l0, l1 []*h264.Picture) {
	*sp = sys.SliceParameterBufferH264{}
	sp.SliceDataSize = uint32(nalSize)
	sp.SliceDataOffset = 0
	sp.SliceDataFlag = sys.SliceDataFlagAll
	sp.SliceDataBitOffset = uint16(sh.HeaderBits)
	sp.FirstMbInSlice = uint16(sh.FirstMbInSlice)
	sp.SliceType = uint8(sh.SliceType)
	sp.DirectSpatialMvPredFlag = uint8(b2u(sh.DirectSpatialMvPred))
	if sh.NumRefIdxL0Active > 0 {
		sp.NumRefIdxL0ActiveMinus1 = uint8(sh.NumRefIdxL0Active - 1)
	}
	if sh.NumRefIdxL1Active > 0 {
		sp.NumRefIdxL1ActiveMinus1 = uint8(sh.NumRefIdxL1Active - 1)
	}
	sp.CabacInitIdc = uint8(sh.CabacInitIdc)
	sp.SliceQpDelta = int8(sh.SliceQpDelta)
	sp.DisableDeblockingFilterIdc = uint8(sh.DisableDeblockingFilterIdc)
	sp.SliceAlphaC0OffsetDiv2 = int8(sh.SliceAlphaC0OffsetDiv2)
	sp.SliceBetaOffsetDiv2 = int8(sh.SliceBetaOffsetDiv2)

	for i := range sp.RefPicList0 {
		sp.RefPicList0[i] = sys.InvalidPictureH264
		sp.RefPicList1[i] = sys.InvalidPictureH264
	}
	for i, p := range l0 {
		if i < len(sp.RefPicList0) {
			sp.RefPicList0[i] = vaPicture(p)
		}
	}
	for i, p := range l1 {
		if i < len(sp.RefPicList1) {
			sp.RefPicList1[i] = vaPicture(p)
		}
	}

	// Prediction weights: the driver wants the inferred defaults too.
	w := sh.PredWeights
	if w == nil {
		w = &h264.PredWeightTable{}
		for i := range w.L0 {
			w.L0[i].LumaWeight, w.L0[i].ChromaWeight = 1, [2]int16{1, 1}
			w.L1[i].LumaWeight, w.L1[i].ChromaWeight = 1, [2]int16{1, 1}
		}
	}
	sp.LumaLog2WeightDenom = uint8(w.LumaLog2WeightDenom)
	sp.ChromaLog2WeightDenom = uint8(w.ChromaLog2WeightDenom)
	sp.LumaWeightL0Flag = uint8(b2u(w.AnyLumaL0))
	sp.ChromaWeightL0Flag = uint8(b2u(w.AnyChromaL0))
	sp.LumaWeightL1Flag = uint8(b2u(w.AnyLumaL1))
	sp.ChromaWeightL1Flag = uint8(b2u(w.AnyChromaL1))
	for i := 0; i < int(sh.NumRefIdxL0Active) && i < len(w.L0); i++ {
		e := &w.L0[i]
		sp.LumaWeightL0[i], sp.LumaOffsetL0[i] = e.LumaWeight, e.LumaOffset
		sp.ChromaWeightL0[i], sp.ChromaOffsetL0[i] = e.ChromaWeight, e.ChromaOffset
	}
	for i := 0; i < int(sh.NumRefIdxL1Active) && i < len(w.L1); i++ {
		e := &w.L1[i]
		sp.LumaWeightL1[i], sp.LumaOffsetL1[i] = e.LumaWeight, e.LumaOffset
		sp.ChromaWeightL1[i], sp.ChromaOffsetL1[i] = e.ChromaWeight, e.ChromaOffset
	}
}
