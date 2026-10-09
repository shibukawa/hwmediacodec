//go:build linux

package vaapi

import (
	"fmt"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// hevcEncoder is the HEVC side of an encoder: the coding block geometry
// and coding tools the driver offers, the parameter sets built from them
// and the VA parameter buffers.
type hevcEncoder struct {
	tools hevcTools

	vps *hevc.VPS
	sps *hevc.SPS
	pps *hevc.PPS

	seqParam   sys.EncSequenceParameterBufferHEVC
	picParam   sys.EncPictureParameterBufferHEVC
	sliceParam sys.EncSliceParameterBufferHEVC

	// outputChecked is set once the driver's own SPS was compared with the
	// requested picture size.
	outputChecked bool
}

// initHEVC reads the driver's HEVC capabilities and sets the coded size.
// The rate control mode is not known yet; buildHEVCParameterSets finishes
// the configuration.
func (e *encoder) initHEVC() error {
	dpy := e.dpy
	// Some Intel encoders have no P-frames and need them replaced by
	// B-frames with two identical lists; this encoder writes P-frames.
	if dir, ok := dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribPredictionDirection); ok && dir&sys.PredictionDirectionBiNotEmpty != 0 {
		return unsupportedEncode(e.cfg.Codec, "this VA-API HEVC encoder ("+dpy.vendor+") has no P-frames (it needs generalised B-frames), which the vaapi backend does not write")
	}
	e.hevc = &hevcEncoder{}
	return nil
}

// buildHEVCParameterSets derives the VPS, SPS and PPS the stream will
// carry.
func (e *encoder) buildHEVCParameterSets() {
	cfg := &e.cfg
	h := e.hevc
	blocks, hasBlocks := e.dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribEncHEVCBlockSizes)
	features, hasFeatures := e.dpy.configAttrib(e.profile, e.entrypoint, sys.ConfigAttribEncHEVCFeatures)
	h.tools = hevcToolsFor(blocks, hasBlocks, features, hasFeatures, e.rcMode != sys.RCCQP)
	num, den := frameRateFraction(cfg.FrameRate)
	h.vps, h.sps, h.pps = hevcParameterSets(cfg.Width, cfg.Height, num, den, int(e.peakBits/1000), e.qp, h.tools)
	if cfg.BT709 {
		h.sps.VUI.VideoSignalTypePresent = true
		h.sps.VUI.VideoFormat = videoFormatUnspecified
		h.sps.VUI.ColourDescriptionPresent = true
		h.sps.VUI.ColourPrimaries = colourBT709
		h.sps.VUI.TransferCharacteristics = colourBT709
		h.sps.VUI.MatrixCoeffs = colourBT709
	}
	e.alignedW, e.alignedH = h.sps.Width, h.sps.Height

	e.paramSets = e.paramSets[:0]
	for _, nal := range [][]byte{hevc.WriteVPS(h.vps), hevc.WriteSPS(h.sps), hevc.WritePPS(h.pps)} {
		e.paramSets = append(append(e.paramSets, 0, 0, 0, 1), nal...)
	}
}

func (e *encoder) fillHEVCSequenceParameters() {
	h := e.hevc
	sps, t := h.sps, &h.tools
	s := &h.seqParam
	*s = sys.EncSequenceParameterBufferHEVC{}
	s.GeneralProfileIDC = sps.PTL.ProfileIDC
	s.GeneralLevelIDC = sps.PTL.LevelIDC
	s.GeneralTierFlag = uint8(b2u(sps.PTL.Tier))
	s.IntraPeriod = uint32(e.gop)
	s.IntraIDRPeriod = uint32(e.gop)
	s.IPPeriod = 1
	s.BitsPerSecond = e.peakBits
	s.PicWidthInLumaSamples = uint16(sps.Width)
	s.PicHeightInLumaSamples = uint16(sps.Height)
	var seq uint32
	seq |= 1 << sys.HEVCEncSeqChromaFormatIDCShift
	seq |= b2u(t.strongIntraSmoothing) * sys.HEVCEncSeqStrongIntraSmoothingEnabledFlag
	seq |= b2u(t.amp) * sys.HEVCEncSeqAmpEnabledFlag
	seq |= b2u(t.sao) * sys.HEVCEncSeqSampleAdaptiveOffsetEnabledFlag
	s.SeqFields = seq
	s.Log2MinLumaCodingBlockSizeMinus3 = uint8(sps.Log2MinLumaCodingBlockSizeMinus3)
	s.Log2DiffMaxMinLumaCodingBlockSize = uint8(sps.Log2DiffMaxMinLumaCodingBlockSize)
	s.Log2MinTransformBlockSizeMinus2 = uint8(sps.Log2MinLumaTransformBlockSizeMinus2)
	s.Log2DiffMaxMinTransformBlockSize = uint8(sps.Log2DiffMaxMinLumaTransformBlockSize)
	s.MaxTransformHierarchyDepthInter = uint8(sps.MaxTransformHierarchyDepthInter)
	s.MaxTransformHierarchyDepthIntra = uint8(sps.MaxTransformHierarchyDepthIntra)

	s.VUIParametersPresentFlag = 1
	vui := uint32(sys.HEVCEncVUIBitstreamRestrictionFlag | sys.HEVCEncVUIMotionVectorsOverPicBoundaries | sys.HEVCEncVUIRestrictedRefPicListsFlag)
	vui |= sps.VUI.Log2MaxMvLengthHorizontal << sys.HEVCEncVUILog2MaxMvLengthHorizontalShift
	vui |= sps.VUI.Log2MaxMvLengthVertical << sys.HEVCEncVUILog2MaxMvLengthVerticalShift
	if sps.VUI.TimingInfoPresent {
		vui |= sys.HEVCEncVUITimingInfoPresentFlag
		s.VUINumUnitsInTick = sps.VUI.NumUnitsInTick
		s.VUITimeScale = sps.VUI.TimeScale
	}
	s.VUIFields = vui
}

func (e *encoder) hevcRefPicture(ref uint32) sys.PictureHEVC {
	return sys.PictureHEVC{PictureID: ref, PicOrderCnt: int32(e.refPOC), Flags: sys.PictureHEVCRPSStCurrBefore}
}

func (e *encoder) fillHEVCPictureParameters(idr bool, recon, ref uint32, poc int) {
	h := e.hevc
	pps := h.pps
	p := &h.picParam
	*p = sys.EncPictureParameterBufferHEVC{}
	p.DecodedCurrPic = sys.PictureHEVC{PictureID: recon, PicOrderCnt: int32(poc)}
	for i := range p.ReferenceFrames {
		p.ReferenceFrames[i] = sys.InvalidPictureHEVC
	}
	if !idr && e.haveRef {
		p.ReferenceFrames[0] = e.hevcRefPicture(ref)
	}
	p.CodedBuf = e.codedBuf
	p.CollocatedRefPicIndex = 0xff // no temporal motion vector prediction
	p.PicInitQp = uint8(26 + pps.InitQpMinus26)
	p.DiffCuQpDeltaDepth = uint8(pps.DiffCuQpDeltaDepth)
	p.NumRefIdxL0DefaultActiveMinus1 = uint8(pps.NumRefIdxL0DefaultActive - 1)
	p.NumRefIdxL1DefaultActiveMinus1 = uint8(pps.NumRefIdxL1DefaultActive - 1)
	p.SlicePicParameterSetID = uint8(pps.ID)

	var fields uint32
	if idr {
		p.NalUnitType = hevc.NALIDRWRADL
		fields |= sys.HEVCEncPicIdrPicFlag
		fields |= 1 << sys.HEVCEncPicCodingTypeShift // I
	} else {
		p.NalUnitType = hevc.NALTrailR
		fields |= 2 << sys.HEVCEncPicCodingTypeShift // P
	}
	fields |= sys.HEVCEncPicReferencePicFlag
	fields |= b2u(pps.SignDataHidingEnabled) * sys.HEVCEncPicSignDataHidingEnabledFlag
	fields |= b2u(pps.ConstrainedIntraPred) * sys.HEVCEncPicConstrainedIntraPredFlag
	fields |= b2u(pps.TransformSkipEnabled) * sys.HEVCEncPicTransformSkipEnabledFlag
	fields |= b2u(pps.CuQpDeltaEnabled) * sys.HEVCEncPicCuQpDeltaEnabledFlag
	fields |= b2u(pps.TransquantBypassEnabled) * sys.HEVCEncPicTransquantBypassEnabledFlag
	fields |= b2u(pps.LoopFilterAcrossSlicesEnabled) * sys.HEVCEncPicPpsLoopFilterAcrossSlicesEnabledFlag
	p.PicFields = fields
}

func (e *encoder) fillHEVCSliceParameters(sh *hevc.SliceHeader, idr bool) {
	h := e.hevc
	s := &h.sliceParam
	*s = sys.EncSliceParameterBufferHEVC{}
	s.NumCtuInSlice = uint32(h.sps.PicSizeInCtbs())
	s.SliceType = uint8(sh.SliceType)
	s.SlicePicParameterSetID = uint8(sh.PPSID)
	for i := range s.RefPicList0 {
		s.RefPicList0[i] = sys.InvalidPictureHEVC
		s.RefPicList1[i] = sys.InvalidPictureHEVC
	}
	if !idr && e.haveRef {
		s.RefPicList0[0] = h.picParam.ReferenceFrames[0]
	}
	s.MaxNumMergeCand = uint8(5 - sh.FiveMinusMaxNumMergeCand)
	s.SliceQpDelta = int8(sh.SliceQpDelta)
	var fields uint32 = sys.HEVCEncSliceLastSliceOfPicFlag
	fields |= b2u(sh.SAOLuma) * sys.HEVCEncSliceSaoLumaFlag
	fields |= b2u(sh.SAOChroma) * sys.HEVCEncSliceSaoChromaFlag
	fields |= b2u(sh.LoopFilterAcrossSlicesEnabled) * sys.HEVCEncSliceLoopFilterAcrossSlicesEnabledFlag
	fields |= b2u(sh.CollocatedFromL0) * sys.HEVCEncSliceCollocatedFromL0Flag
	s.SliceFields = fields
}

// checkHEVCOutput looks at the SPS in the first keyframe the driver
// produced. A driver that writes its own parameter sets (AMD) decides the
// coded size and the conformance window itself; if the picture it describes
// is not the one that was asked for, the stream would decode to the wrong
// size, so that is reported instead of passed on.
func (e *encoder) checkHEVCOutput(data []byte) error {
	h := e.hevc
	if h.outputChecked {
		return nil
	}
	for _, nal := range annexb.Split(data) {
		if hevc.Type(nal) != hevc.NALSPS {
			continue
		}
		h.outputChecked = true
		sps, err := hevc.ParseSPS(nal)
		if err != nil {
			return nil // not ours to judge; a decoder will
		}
		if _, _, w, ht := sps.Crop(); w != e.cfg.Width || ht != e.cfg.Height {
			return &codec.BackendError{Backend: Name, Op: "encode", Message: fmt.Sprintf(
				"the driver (%s) encoded a %dx%d picture for the requested %dx%d; use dimensions that are multiples of %d",
				e.dpy.vendor, w, ht, e.cfg.Width, e.cfg.Height, 1<<uint(h.tools.minCbLog2))}
		}
		return nil
	}
	return nil
}
