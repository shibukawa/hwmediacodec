package hevc

import "github.com/shibukawa/hwmediacodec/internal/bitstream"

// NALUnit wraps an RBSP into a NAL unit of layer 0: the two-byte header
// followed by the payload with emulation prevention bytes inserted.
func NALUnit(typ, temporalID int, rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/64+8)
	out = append(out, byte(typ<<1), byte(temporalID+1))
	return append(out, bitstream.Escape(rbsp)...)
}

// VPS is the part of a video parameter set (7.3.2.1) a single-layer,
// single-sub-layer encoder writes.
type VPS struct {
	ID                       uint32
	TemporalIDNesting        bool
	PTL                      ProfileTierLevel
	MaxDecPicBufferingMinus1 uint32
	MaxNumReorderPics        uint32
	MaxLatencyIncreasePlus1  uint32
	TimingInfoPresent        bool
	NumUnitsInTick           uint32
	TimeScale                uint32
}

// WriteVPS serialises a video parameter set as a complete NAL unit: one
// layer, one sub-layer, no HRD parameters and no extension. With a single
// sub-layer the specification requires TemporalIDNesting to be set, here
// and in the SPS.
func WriteVPS(v *VPS) []byte {
	w := bitstream.NewWriter()
	w.WriteBits(uint64(v.ID), 4)
	w.WriteBits(3, 2) // vps_base_layer_internal_flag, vps_base_layer_available_flag
	w.WriteBits(0, 6) // vps_max_layers_minus1
	w.WriteBits(0, 3) // vps_max_sub_layers_minus1
	w.WriteFlag(v.TemporalIDNesting)
	w.WriteBits(0xffff, 16) // vps_reserved_0xffff_16bits
	v.PTL.write(w)
	w.WriteFlag(true) // vps_sub_layer_ordering_info_present_flag
	w.WriteUE(v.MaxDecPicBufferingMinus1)
	w.WriteUE(v.MaxNumReorderPics)
	w.WriteUE(v.MaxLatencyIncreasePlus1)
	w.WriteBits(0, 6) // vps_max_layer_id
	w.WriteUE(0)      // vps_num_layer_sets_minus1
	w.WriteFlag(v.TimingInfoPresent)
	if v.TimingInfoPresent {
		w.WriteBits(uint64(v.NumUnitsInTick), 32)
		w.WriteBits(uint64(v.TimeScale), 32)
		w.WriteFlag(false) // vps_poc_proportional_to_timing_flag
		w.WriteUE(0)       // vps_num_hrd_parameters
	}
	w.WriteFlag(false) // vps_extension_flag
	w.Trailing()
	return NALUnit(NALVPS, 0, w.Bytes())
}

// WriteSPS serialises a sequence parameter set as a complete NAL unit. It
// writes a single sub-layer, the short-term reference picture sets without
// inter prediction, and the VUI without HRD parameters. Scaling list data
// and the extensions are not written; their present flags are coded as 0.
func WriteSPS(s *SPS) []byte {
	w := bitstream.NewWriter()
	w.WriteBits(uint64(s.VPSID), 4)
	w.WriteBits(0, 3) // sps_max_sub_layers_minus1
	w.WriteFlag(s.TemporalIDNesting)
	s.PTL.write(w)
	w.WriteUE(s.ID)
	w.WriteUE(s.ChromaFormatIDC)
	if s.ChromaFormatIDC == 3 {
		w.WriteFlag(s.SeparateColourPlane)
	}
	w.WriteUE(uint32(s.Width))
	w.WriteUE(uint32(s.Height))
	w.WriteFlag(s.ConformanceWindow)
	if s.ConformanceWindow {
		w.WriteUE(s.ConfWinLeft)
		w.WriteUE(s.ConfWinRight)
		w.WriteUE(s.ConfWinTop)
		w.WriteUE(s.ConfWinBottom)
	}
	w.WriteUE(s.BitDepthLuma - 8)
	w.WriteUE(s.BitDepthChroma - 8)
	w.WriteUE(uint32(s.Log2MaxPOCLsb - 4))
	w.WriteFlag(s.SubLayerOrderingInfoPresent)
	w.WriteUE(s.MaxDecPicBufferingMinus1[0])
	w.WriteUE(s.MaxNumReorderPics[0])
	w.WriteUE(s.MaxLatencyIncreasePlus1[0])
	w.WriteUE(s.Log2MinLumaCodingBlockSizeMinus3)
	w.WriteUE(s.Log2DiffMaxMinLumaCodingBlockSize)
	w.WriteUE(s.Log2MinLumaTransformBlockSizeMinus2)
	w.WriteUE(s.Log2DiffMaxMinLumaTransformBlockSize)
	w.WriteUE(s.MaxTransformHierarchyDepthInter)
	w.WriteUE(s.MaxTransformHierarchyDepthIntra)
	w.WriteFlag(s.ScalingListEnabled)
	if s.ScalingListEnabled {
		w.WriteFlag(false) // sps_scaling_list_data_present_flag
	}
	w.WriteFlag(s.AMPEnabled)
	w.WriteFlag(s.SAOEnabled)
	w.WriteFlag(s.PCMEnabled)
	if s.PCMEnabled {
		w.WriteBits(uint64(s.PCMSampleBitDepthLumaMinus1), 4)
		w.WriteBits(uint64(s.PCMSampleBitDepthChromaMinus1), 4)
		w.WriteUE(s.Log2MinPCMLumaCodingBlockSizeMinus3)
		w.WriteUE(s.Log2DiffMaxMinPCMLumaCodingBlockSize)
		w.WriteFlag(s.PCMLoopFilterDisabled)
	}
	w.WriteUE(uint32(len(s.ShortTermRPS)))
	for i := range s.ShortTermRPS {
		s.ShortTermRPS[i].write(w, i)
	}
	w.WriteFlag(s.LongTermRefPicsPresent)
	if s.LongTermRefPicsPresent {
		w.WriteUE(uint32(len(s.LtRefPicPOCLsbSPS)))
		for i, lsb := range s.LtRefPicPOCLsbSPS {
			w.WriteBits(uint64(lsb), s.Log2MaxPOCLsb)
			w.WriteFlag(s.UsedByCurrPicLtSPS[i])
		}
	}
	w.WriteFlag(s.TemporalMVPEnabled)
	w.WriteFlag(s.StrongIntraSmoothingEnabled)
	w.WriteFlag(s.VUIPresent)
	if s.VUIPresent {
		writeVUI(w, &s.VUI)
	}
	w.WriteFlag(false) // sps_extension_present_flag
	w.Trailing()
	return NALUnit(NALSPS, 0, w.Bytes())
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
			w.WriteBits(uint64(v.MatrixCoeffs), 8)
		}
	}
	w.WriteFlag(v.ChromaLocInfoPresent)
	if v.ChromaLocInfoPresent {
		w.WriteUE(v.ChromaSampleLocTypeTopField)
		w.WriteUE(v.ChromaSampleLocTypeBottomField)
	}
	w.WriteFlag(v.NeutralChromaIndication)
	w.WriteFlag(v.FieldSeq)
	w.WriteFlag(v.FrameFieldInfoPresent)
	w.WriteFlag(v.DefaultDisplayWindow)
	if v.DefaultDisplayWindow {
		w.WriteUE(v.DefDispWinLeft)
		w.WriteUE(v.DefDispWinRight)
		w.WriteUE(v.DefDispWinTop)
		w.WriteUE(v.DefDispWinBottom)
	}
	w.WriteFlag(v.TimingInfoPresent)
	if v.TimingInfoPresent {
		w.WriteBits(uint64(v.NumUnitsInTick), 32)
		w.WriteBits(uint64(v.TimeScale), 32)
		w.WriteFlag(v.POCProportionalToTiming)
		if v.POCProportionalToTiming {
			w.WriteUE(v.NumTicksPOCDiffOneMinus1)
		}
		w.WriteFlag(false) // vui_hrd_parameters_present_flag
	}
	w.WriteFlag(v.BitstreamRestriction)
	if v.BitstreamRestriction {
		w.WriteFlag(v.TilesFixedStructure)
		w.WriteFlag(v.MotionVectorsOverPicBoundaries)
		w.WriteFlag(v.RestrictedRefPicLists)
		w.WriteUE(v.MinSpatialSegmentationIDC)
		w.WriteUE(v.MaxBytesPerPicDenom)
		w.WriteUE(v.MaxBitsPerMinCuDenom)
		w.WriteUE(v.Log2MaxMvLengthHorizontal)
		w.WriteUE(v.Log2MaxMvLengthVertical)
	}
}

// WritePPS serialises a picture parameter set as a complete NAL unit. Tiles
// are written with the grid of the PPS; scaling list data and the
// extensions are not written.
func WritePPS(p *PPS) []byte {
	w := bitstream.NewWriter()
	w.WriteUE(p.ID)
	w.WriteUE(p.SPSID)
	w.WriteFlag(p.DependentSliceSegmentsEnabled)
	w.WriteFlag(p.OutputFlagPresent)
	w.WriteBits(uint64(p.NumExtraSliceHeaderBits), 3)
	w.WriteFlag(p.SignDataHidingEnabled)
	w.WriteFlag(p.CabacInitPresent)
	w.WriteUE(p.NumRefIdxL0DefaultActive - 1)
	w.WriteUE(p.NumRefIdxL1DefaultActive - 1)
	w.WriteSE(p.InitQpMinus26)
	w.WriteFlag(p.ConstrainedIntraPred)
	w.WriteFlag(p.TransformSkipEnabled)
	w.WriteFlag(p.CuQpDeltaEnabled)
	if p.CuQpDeltaEnabled {
		w.WriteUE(p.DiffCuQpDeltaDepth)
	}
	w.WriteSE(p.CbQpOffset)
	w.WriteSE(p.CrQpOffset)
	w.WriteFlag(p.SliceChromaQpOffsetsPresent)
	w.WriteFlag(p.WeightedPred)
	w.WriteFlag(p.WeightedBipred)
	w.WriteFlag(p.TransquantBypassEnabled)
	w.WriteFlag(p.TilesEnabled)
	w.WriteFlag(p.EntropyCodingSyncEnabled)
	if p.TilesEnabled {
		w.WriteUE(uint32(p.NumTileColumns - 1))
		w.WriteUE(uint32(p.NumTileRows - 1))
		w.WriteFlag(p.UniformSpacing)
		if !p.UniformSpacing {
			for _, v := range p.ColumnWidthMinus1 {
				w.WriteUE(v)
			}
			for _, v := range p.RowHeightMinus1 {
				w.WriteUE(v)
			}
		}
		w.WriteFlag(p.LoopFilterAcrossTilesEnabled)
	}
	w.WriteFlag(p.LoopFilterAcrossSlicesEnabled)
	w.WriteFlag(p.DeblockingFilterControlPresent)
	if p.DeblockingFilterControlPresent {
		w.WriteFlag(p.DeblockingFilterOverrideEnabled)
		w.WriteFlag(p.DeblockingFilterDisabled)
		if !p.DeblockingFilterDisabled {
			w.WriteSE(p.BetaOffsetDiv2)
			w.WriteSE(p.TcOffsetDiv2)
		}
	}
	w.WriteFlag(false) // pps_scaling_list_data_present_flag
	w.WriteFlag(p.ListsModificationPresent)
	w.WriteUE(p.Log2ParallelMergeLevelMinus2)
	w.WriteFlag(p.SliceSegmentHeaderExtensionPresent)
	w.WriteFlag(false) // pps_extension_present_flag
	w.Trailing()
	return NALUnit(NALPPS, 0, w.Bytes())
}

// WriteSliceHeader serialises the slice segment header of an independent
// slice segment as a NAL unit that ends after byte_alignment(); the caller
// (or the hardware encoder) appends slice_segment_data().
//
// Long-term reference pictures, reference list modification, weighted
// prediction tables and entry points are not written: the header must not
// use them. A short-term reference picture set coded in the header is
// written without inter prediction.
func WriteSliceHeader(h *SliceHeader, sps *SPS, pps *PPS) []byte {
	w := bitstream.NewWriter()
	w.WriteFlag(h.FirstSliceSegmentInPic)
	if IsIRAP(h.NALType) {
		w.WriteFlag(h.NoOutputOfPriorPics)
	}
	w.WriteUE(h.PPSID)
	if !h.FirstSliceSegmentInPic {
		if pps.DependentSliceSegmentsEnabled {
			w.WriteFlag(false) // dependent_slice_segment_flag
		}
		w.WriteBits(uint64(h.SliceSegmentAddress), ceilLog2(sps.PicSizeInCtbs()))
	}
	w.WriteBits(0, pps.NumExtraSliceHeaderBits) // slice_reserved_flag
	w.WriteUE(h.SliceType)
	if pps.OutputFlagPresent {
		w.WriteFlag(h.PicOutput)
	}
	if sps.SeparateColourPlane {
		w.WriteBits(uint64(h.ColourPlaneID), 2)
	}
	chroma := sps.ChromaFormatIDC != 0 && !sps.SeparateColourPlane
	if !IsIDR(h.NALType) {
		w.WriteBits(uint64(h.POCLsb), sps.Log2MaxPOCLsb)
		w.WriteFlag(h.ShortTermRefPicSetSPSFlag)
		if !h.ShortTermRefPicSetSPSFlag {
			rps := h.ShortTermRPS
			if rps == nil {
				rps = &ShortTermRPS{}
			}
			explicit := *rps
			explicit.InterRPSPrediction = false
			explicit.write(w, len(sps.ShortTermRPS))
		} else if n := len(sps.ShortTermRPS); n > 1 {
			w.WriteBits(uint64(h.ShortTermRefPicSetIdx), ceilLog2(n))
		}
		if sps.LongTermRefPicsPresent {
			if len(sps.LtRefPicPOCLsbSPS) > 0 {
				w.WriteUE(0) // num_long_term_sps
			}
			w.WriteUE(0) // num_long_term_pics
		}
		if sps.TemporalMVPEnabled {
			w.WriteFlag(h.SliceTemporalMVPEnabled)
		}
	}
	if sps.SAOEnabled {
		w.WriteFlag(h.SAOLuma)
		if chroma {
			w.WriteFlag(h.SAOChroma)
		}
	}
	if h.SliceType == SliceP || h.SliceType == SliceB {
		w.WriteFlag(h.NumRefIdxActiveOverride)
		if h.NumRefIdxActiveOverride {
			w.WriteUE(h.NumRefIdxL0Active - 1)
			if h.SliceType == SliceB {
				w.WriteUE(h.NumRefIdxL1Active - 1)
			}
		}
		if pps.ListsModificationPresent && h.NumPicTotalCurr > 1 {
			w.WriteFlag(false) // ref_pic_list_modification_flag_l0
			if h.SliceType == SliceB {
				w.WriteFlag(false) // ref_pic_list_modification_flag_l1
			}
		}
		if h.SliceType == SliceB {
			w.WriteFlag(h.MvdL1Zero)
		}
		if pps.CabacInitPresent {
			w.WriteFlag(h.CabacInit)
		}
		if h.SliceTemporalMVPEnabled {
			if h.SliceType == SliceB {
				w.WriteFlag(h.CollocatedFromL0)
			}
			l0, l1 := h.NumRefIdxL0Active, h.NumRefIdxL1Active
			if !h.NumRefIdxActiveOverride {
				l0, l1 = pps.NumRefIdxL0DefaultActive, pps.NumRefIdxL1DefaultActive
			}
			if (h.CollocatedFromL0 && l0 > 1) || (!h.CollocatedFromL0 && l1 > 1) {
				w.WriteUE(h.CollocatedRefIdx)
			}
		}
		w.WriteUE(h.FiveMinusMaxNumMergeCand)
	}
	w.WriteSE(h.SliceQpDelta)
	if pps.SliceChromaQpOffsetsPresent {
		w.WriteSE(h.SliceCbQpOffset)
		w.WriteSE(h.SliceCrQpOffset)
	}
	if pps.DeblockingFilterOverrideEnabled {
		w.WriteFlag(h.DeblockingFilterOverride)
	}
	disabled := pps.DeblockingFilterDisabled
	if pps.DeblockingFilterOverrideEnabled && h.DeblockingFilterOverride {
		disabled = h.DeblockingFilterDisabled
		w.WriteFlag(h.DeblockingFilterDisabled)
		if !h.DeblockingFilterDisabled {
			w.WriteSE(h.BetaOffsetDiv2)
			w.WriteSE(h.TcOffsetDiv2)
		}
	}
	if pps.LoopFilterAcrossSlicesEnabled && (h.SAOLuma || h.SAOChroma || !disabled) {
		w.WriteFlag(h.LoopFilterAcrossSlicesEnabled)
	}
	if pps.TilesEnabled || pps.EntropyCodingSyncEnabled {
		w.WriteUE(0) // num_entry_point_offsets
	}
	if pps.SliceSegmentHeaderExtensionPresent {
		w.WriteUE(0) // slice_segment_header_extension_length
	}
	w.Trailing() // byte_alignment() has the syntax of rbsp_trailing_bits()
	return NALUnit(h.NALType, h.TemporalID, w.Bytes())
}
