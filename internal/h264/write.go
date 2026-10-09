package h264

import "github.com/shibukawa/hwmediacodec/internal/bitstream"

// NALUnit wraps an RBSP into a NAL unit: header byte followed by the payload
// with emulation prevention bytes inserted.
func NALUnit(refIdc, typ int, rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/64+8)
	out = append(out, byte(refIdc<<5|typ))
	return append(out, bitstream.Escape(rbsp)...)
}

// WriteSPS serialises a sequence parameter set as a complete NAL unit
// (nal_ref_idc 3). Scaling matrices and HRD parameters are not written; the
// corresponding present flags are coded as 0.
func WriteSPS(s *SPS) []byte {
	w := bitstream.NewWriter()
	w.WriteBits(uint64(s.ProfileIDC), 8)
	w.WriteBits(uint64(s.ConstraintFlags&0xfc), 8)
	w.WriteBits(uint64(s.LevelIDC), 8)
	w.WriteUE(s.ID)
	if hasChromaInfo(s.ProfileIDC) {
		w.WriteUE(s.ChromaFormatIDC)
		if s.ChromaFormatIDC == 3 {
			w.WriteFlag(s.SeparateColourPlane)
		}
		w.WriteUE(s.BitDepthLuma - 8)
		w.WriteUE(s.BitDepthChroma - 8)
		w.WriteFlag(s.QpprimeYZeroTransformBypass)
		w.WriteFlag(false) // seq_scaling_matrix_present_flag
	}
	w.WriteUE(s.Log2MaxFrameNum - 4)
	w.WriteUE(s.PicOrderCntType)
	switch s.PicOrderCntType {
	case 0:
		w.WriteUE(s.Log2MaxPicOrderCntLsb - 4)
	case 1:
		w.WriteFlag(s.DeltaPicOrderAlwaysZero)
		w.WriteSE(s.OffsetForNonRefPic)
		w.WriteSE(s.OffsetForTopToBottomField)
		w.WriteUE(uint32(len(s.OffsetForRefFrame)))
		for _, o := range s.OffsetForRefFrame {
			w.WriteSE(o)
		}
	}
	w.WriteUE(s.MaxNumRefFrames)
	w.WriteFlag(s.GapsInFrameNumAllowed)
	w.WriteUE(s.PicWidthInMbs - 1)
	w.WriteUE(s.PicHeightInMapUnits - 1)
	w.WriteFlag(s.FrameMbsOnly)
	if !s.FrameMbsOnly {
		w.WriteFlag(s.MbAdaptiveFrameField)
	}
	w.WriteFlag(s.Direct8x8Inference)
	w.WriteFlag(s.FrameCropping)
	if s.FrameCropping {
		w.WriteUE(s.CropLeft)
		w.WriteUE(s.CropRight)
		w.WriteUE(s.CropTop)
		w.WriteUE(s.CropBottom)
	}
	w.WriteFlag(s.VUIPresent)
	if s.VUIPresent {
		writeVUI(w, &s.VUI)
	}
	w.Trailing()
	return NALUnit(3, NALSPS, w.Bytes())
}

func writeVUI(w *bitstream.Writer, v *VUI) {
	w.WriteFlag(v.AspectRatioInfoPresent)
	if v.AspectRatioInfoPresent {
		w.WriteBits(uint64(v.AspectRatioIDC), 8)
		if v.AspectRatioIDC == 255 {
			w.WriteBits(uint64(v.SarWidth), 16)
			w.WriteBits(uint64(v.SarHeight), 16)
		}
	}
	w.WriteFlag(v.OverscanInfoPresent)
	if v.OverscanInfoPresent {
		w.WriteFlag(v.OverscanAppropriate)
	}
	w.WriteFlag(v.VideoSignalTypePresent)
	if v.VideoSignalTypePresent {
		w.WriteBits(uint64(v.VideoFormat), 3)
		w.WriteFlag(v.VideoFullRange)
		w.WriteFlag(v.ColourDescriptionPresent)
		if v.ColourDescriptionPresent {
			w.WriteBits(uint64(v.ColourPrimaries), 8)
			w.WriteBits(uint64(v.TransferCharacteristics), 8)
			w.WriteBits(uint64(v.MatrixCoefficients), 8)
		}
	}
	w.WriteFlag(v.ChromaLocInfoPresent)
	if v.ChromaLocInfoPresent {
		w.WriteUE(v.ChromaSampleLocTypeTop)
		w.WriteUE(v.ChromaSampleLocTypeBottom)
	}
	w.WriteFlag(v.TimingInfoPresent)
	if v.TimingInfoPresent {
		w.WriteBits(uint64(v.NumUnitsInTick), 32)
		w.WriteBits(uint64(v.TimeScale), 32)
		w.WriteFlag(v.FixedFrameRate)
	}
	w.WriteFlag(false) // nal_hrd_parameters_present_flag
	w.WriteFlag(false) // vcl_hrd_parameters_present_flag
	w.WriteFlag(v.PicStructPresent)
	w.WriteFlag(v.BitstreamRestriction)
	if v.BitstreamRestriction {
		w.WriteFlag(v.MotionVectorsOverPicBoundaries)
		w.WriteUE(v.MaxBytesPerPicDenom)
		w.WriteUE(v.MaxBitsPerMbDenom)
		w.WriteUE(v.Log2MaxMvLengthHorizontal)
		w.WriteUE(v.Log2MaxMvLengthVertical)
		w.WriteUE(v.MaxNumReorderFrames)
		w.WriteUE(v.MaxDecFrameBuffering)
	}
}

// WritePPS serialises a picture parameter set as a complete NAL unit
// (nal_ref_idc 3). Slice groups and scaling matrices are not written.
func WritePPS(p *PPS) []byte {
	w := bitstream.NewWriter()
	w.WriteUE(p.ID)
	w.WriteUE(p.SPSID)
	w.WriteFlag(p.EntropyCodingMode)
	w.WriteFlag(p.BottomFieldPicOrderInFramePresent)
	w.WriteUE(0) // num_slice_groups_minus1
	w.WriteUE(p.NumRefIdxL0DefaultActive - 1)
	w.WriteUE(p.NumRefIdxL1DefaultActive - 1)
	w.WriteFlag(p.WeightedPred)
	w.WriteBits(uint64(p.WeightedBipredIdc), 2)
	w.WriteSE(p.PicInitQpMinus26)
	w.WriteSE(p.PicInitQsMinus26)
	w.WriteSE(p.ChromaQpIndexOffset)
	w.WriteFlag(p.DeblockingFilterControlPresent)
	w.WriteFlag(p.ConstrainedIntraPred)
	w.WriteFlag(p.RedundantPicCntPresent)
	if p.Transform8x8Mode || p.SecondChromaQpIndexOffset != p.ChromaQpIndexOffset {
		w.WriteFlag(p.Transform8x8Mode)
		w.WriteFlag(false) // pic_scaling_matrix_present_flag
		w.WriteSE(p.SecondChromaQpIndexOffset)
	}
	w.Trailing()
	return NALUnit(3, NALPPS, w.Bytes())
}

// WriteSliceHeader serialises the NAL header and slice_header() of h. It
// returns the escaped bytes (the last byte zero padded) and the exact number
// of bits they hold, which is what packed-header encode interfaces take.
// Slice groups are not supported.
func WriteSliceHeader(h *SliceHeader, sps *SPS, pps *PPS) (data []byte, bits int) {
	w := bitstream.NewWriter()
	w.WriteUE(h.FirstMbInSlice)
	w.WriteUE(h.SliceTypeRaw)
	w.WriteUE(h.PPSID)
	if sps.SeparateColourPlane {
		w.WriteBits(uint64(h.ColourPlaneID), 2)
	}
	w.WriteBits(uint64(h.FrameNum), int(sps.Log2MaxFrameNum))
	if !sps.FrameMbsOnly {
		w.WriteFlag(h.FieldPic)
		if h.FieldPic {
			w.WriteFlag(h.BottomField)
		}
	}
	if h.IDR {
		w.WriteUE(h.IdrPicID)
	}
	switch sps.PicOrderCntType {
	case 0:
		w.WriteBits(uint64(h.PicOrderCntLsb), int(sps.Log2MaxPicOrderCntLsb))
		if pps.BottomFieldPicOrderInFramePresent && !h.FieldPic {
			w.WriteSE(h.DeltaPicOrderCntBottom)
		}
	case 1:
		if !sps.DeltaPicOrderAlwaysZero {
			w.WriteSE(h.DeltaPicOrderCnt[0])
			if pps.BottomFieldPicOrderInFramePresent && !h.FieldPic {
				w.WriteSE(h.DeltaPicOrderCnt[1])
			}
		}
	}
	if pps.RedundantPicCntPresent {
		w.WriteUE(h.RedundantPicCnt)
	}
	if h.SliceType == SliceB {
		w.WriteFlag(h.DirectSpatialMvPred)
	}
	if h.SliceType == SliceP || h.SliceType == SliceSP || h.SliceType == SliceB {
		w.WriteFlag(h.NumRefIdxActiveOverride)
		if h.NumRefIdxActiveOverride {
			w.WriteUE(h.NumRefIdxL0Active - 1)
			if h.SliceType == SliceB {
				w.WriteUE(h.NumRefIdxL1Active - 1)
			}
		}
	}
	writeMods := func(mods []RefPicListModification) {
		w.WriteFlag(mods != nil)
		if mods == nil {
			return
		}
		for _, m := range mods {
			w.WriteUE(m.Idc)
			w.WriteUE(m.Value)
		}
		w.WriteUE(3)
	}
	if !h.SliceType.IsIntra() {
		writeMods(h.RefPicListModificationL0)
	}
	if h.SliceType == SliceB {
		writeMods(h.RefPicListModificationL1)
	}
	if (pps.WeightedPred && (h.SliceType == SliceP || h.SliceType == SliceSP)) ||
		(pps.WeightedBipredIdc == 1 && h.SliceType == SliceB) {
		writePredWeightTable(w, h, sps.ChromaArrayType())
	}
	if h.NALRefIdc != 0 {
		if h.IDR {
			w.WriteFlag(h.NoOutputOfPriorPics)
			w.WriteFlag(h.LongTermReference)
		} else {
			w.WriteFlag(h.AdaptiveRefPicMarking)
			if h.AdaptiveRefPicMarking {
				for _, m := range h.MMCOs {
					w.WriteUE(m.Op)
					if m.Op == 1 || m.Op == 3 {
						w.WriteUE(m.DifferenceOfPicNumsMinus1)
					}
					if m.Op == 2 {
						w.WriteUE(m.LongTermPicNum)
					}
					if m.Op == 3 || m.Op == 6 {
						w.WriteUE(m.LongTermFrameIdx)
					}
					if m.Op == 4 {
						w.WriteUE(m.MaxLongTermFrameIdxPlus1)
					}
				}
				w.WriteUE(0)
			}
		}
	}
	if pps.EntropyCodingMode && !h.SliceType.IsIntra() {
		w.WriteUE(h.CabacInitIdc)
	}
	w.WriteSE(h.SliceQpDelta)
	if h.SliceType == SliceSP || h.SliceType == SliceSI {
		if h.SliceType == SliceSP {
			w.WriteFlag(h.SpForSwitch)
		}
		w.WriteSE(h.SliceQsDelta)
	}
	if pps.DeblockingFilterControlPresent {
		w.WriteUE(h.DisableDeblockingFilterIdc)
		if h.DisableDeblockingFilterIdc != 1 {
			w.WriteSE(h.SliceAlphaC0OffsetDiv2)
			w.WriteSE(h.SliceBetaOffsetDiv2)
		}
	}
	rbspBits := w.Len()
	raw := NALUnit(h.NALRefIdc, h.NALType, w.Bytes())
	// Emulation prevention only ever inserts whole bytes in front of a
	// payload byte, so the padding of the last byte is unchanged.
	pad := len(w.Bytes())*8 - rbspBits
	return raw, len(raw)*8 - pad
}

func writePredWeightTable(w *bitstream.Writer, h *SliceHeader, chromaArrayType uint32) {
	pw := h.PredWeights
	if pw == nil {
		pw = &PredWeightTable{}
	}
	w.WriteUE(pw.LumaLog2WeightDenom)
	if chromaArrayType != 0 {
		w.WriteUE(pw.ChromaLog2WeightDenom)
	}
	writeList := func(list *[maxRefIdxActive]WeightEntry, n uint32) {
		for i := uint32(0); i < n; i++ {
			e := &list[i]
			w.WriteFlag(e.LumaWeightFlag)
			if e.LumaWeightFlag {
				w.WriteSE(int32(e.LumaWeight))
				w.WriteSE(int32(e.LumaOffset))
			}
			if chromaArrayType != 0 {
				w.WriteFlag(e.ChromaWeightFlag)
				if e.ChromaWeightFlag {
					for j := 0; j < 2; j++ {
						w.WriteSE(int32(e.ChromaWeight[j]))
						w.WriteSE(int32(e.ChromaOffset[j]))
					}
				}
			}
		}
	}
	writeList(&pw.L0, h.NumRefIdxL0Active)
	if h.SliceType == SliceB {
		writeList(&pw.L1, h.NumRefIdxL1Active)
	}
}
