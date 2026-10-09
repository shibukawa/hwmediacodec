package sys

import (
	"testing"
	"unsafe"
)

// The expected values were printed by a C program (sizeof/offsetof and the
// enumerators) compiled with gcc 12 for x86_64-linux-gnu against the VPL
// headers of Debian's libvpl-dev 2023.1.1 (API 2.8). The GPU runtime only
// exists for x86-64; the Go layouts are the same on every 64-bit target, so
// the test runs on any development machine.
func TestStructLayouts(t *testing.T) {
	var ver Version
	var eb ExtBuffer
	var fi FrameInfo
	var im InfoMFX
	var vp VideoParam
	var bs Bitstream
	var fd FrameData
	var fs FrameSurface
	var ar FrameAllocRequest
	var ec EncodeCtrl
	var co ExtCodingOption
	var co2 ExtCodingOption2
	var co3 ExtCodingOption3
	var sp ExtCodingOptionSPSPPS
	var vps ExtCodingOptionVPS

	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"sizeof mfxVersion", unsafe.Sizeof(ver), 4},

		{"sizeof mfxExtBuffer", unsafe.Sizeof(eb), 8},

		{"sizeof mfxFrameInfo", unsafe.Sizeof(fi), 68},
		{"mfxFrameInfo.ChannelId", unsafe.Offsetof(fi.ChannelID), 16},
		{"mfxFrameInfo.BitDepthLuma", unsafe.Offsetof(fi.BitDepthLuma), 18},
		{"mfxFrameInfo.BitDepthChroma", unsafe.Offsetof(fi.BitDepthChroma), 20},
		{"mfxFrameInfo.Shift", unsafe.Offsetof(fi.Shift), 22},
		{"mfxFrameInfo.FrameId", unsafe.Offsetof(fi.FrameID), 24},
		{"mfxFrameInfo.FourCC", unsafe.Offsetof(fi.FourCC), 32},
		{"mfxFrameInfo.Width", unsafe.Offsetof(fi.Width), 36},
		{"mfxFrameInfo.Height", unsafe.Offsetof(fi.Height), 38},
		{"mfxFrameInfo.CropX", unsafe.Offsetof(fi.CropX), 40},
		{"mfxFrameInfo.CropY", unsafe.Offsetof(fi.CropY), 42},
		{"mfxFrameInfo.CropW", unsafe.Offsetof(fi.CropW), 44},
		{"mfxFrameInfo.CropH", unsafe.Offsetof(fi.CropH), 46},
		{"mfxFrameInfo.FrameRateExtN", unsafe.Offsetof(fi.FrameRateExtN), 48},
		{"mfxFrameInfo.FrameRateExtD", unsafe.Offsetof(fi.FrameRateExtD), 52},
		{"mfxFrameInfo.AspectRatioW", unsafe.Offsetof(fi.AspectRatioW), 58},
		{"mfxFrameInfo.AspectRatioH", unsafe.Offsetof(fi.AspectRatioH), 60},
		{"mfxFrameInfo.PicStruct", unsafe.Offsetof(fi.PicStruct), 62},
		{"mfxFrameInfo.ChromaFormat", unsafe.Offsetof(fi.ChromaFormat), 64},

		{"sizeof mfxInfoMFX", unsafe.Sizeof(im), 136},
		{"mfxInfoMFX.LowPower", unsafe.Offsetof(im.LowPower), 28},
		{"mfxInfoMFX.BRCParamMultiplier", unsafe.Offsetof(im.BRCParamMultiplier), 30},
		{"mfxInfoMFX.FrameInfo", unsafe.Offsetof(im.FrameInfo), 32},
		{"mfxInfoMFX.CodecId", unsafe.Offsetof(im.CodecID), 100},
		{"mfxInfoMFX.CodecProfile", unsafe.Offsetof(im.CodecProfile), 104},
		{"mfxInfoMFX.CodecLevel", unsafe.Offsetof(im.CodecLevel), 106},
		{"mfxInfoMFX.NumThread", unsafe.Offsetof(im.NumThread), 108},
		{"mfxInfoMFX.TargetUsage", unsafe.Offsetof(im.TargetUsage), 110},
		{"mfxInfoMFX.GopPicSize", unsafe.Offsetof(im.GopPicSize), 112},
		{"mfxInfoMFX.GopRefDist", unsafe.Offsetof(im.GopRefDist), 114},
		{"mfxInfoMFX.GopOptFlag", unsafe.Offsetof(im.GopOptFlag), 116},
		{"mfxInfoMFX.IdrInterval", unsafe.Offsetof(im.IdrInterval), 118},
		{"mfxInfoMFX.RateControlMethod", unsafe.Offsetof(im.RateControlMethod), 120},
		{"mfxInfoMFX.InitialDelayInKB", unsafe.Offsetof(im.InitialDelayInKB), 122},
		{"mfxInfoMFX.QPI", unsafe.Offsetof(im.InitialDelayInKB), 122},
		{"mfxInfoMFX.BufferSizeInKB", unsafe.Offsetof(im.BufferSizeInKB), 124},
		{"mfxInfoMFX.TargetKbps", unsafe.Offsetof(im.TargetKbps), 126},
		{"mfxInfoMFX.QPP", unsafe.Offsetof(im.TargetKbps), 126},
		{"mfxInfoMFX.MaxKbps", unsafe.Offsetof(im.MaxKbps), 128},
		{"mfxInfoMFX.QPB", unsafe.Offsetof(im.MaxKbps), 128},
		{"mfxInfoMFX.NumSlice", unsafe.Offsetof(im.NumSlice), 130},
		{"mfxInfoMFX.NumRefFrame", unsafe.Offsetof(im.NumRefFrame), 132},
		{"mfxInfoMFX.EncodedOrder", unsafe.Offsetof(im.EncodedOrder), 134},
		{"mfxInfoMFX.DecodedOrder", unsafe.Offsetof(im.TargetUsage), 110},
		{"mfxInfoMFX.MaxDecFrameBuffering", unsafe.Offsetof(im.IdrInterval), 118},
		{"sizeof mfxInfoVPP (the mfxVideoParam union)", unsafe.Offsetof(vp.Protected) - unsafe.Offsetof(vp.MFX), 168},

		{"sizeof mfxVideoParam", unsafe.Sizeof(vp), 208},
		{"mfxVideoParam.AllocId", unsafe.Offsetof(vp.AllocID), 0},
		{"mfxVideoParam.AsyncDepth", unsafe.Offsetof(vp.AsyncDepth), 14},
		{"mfxVideoParam.mfx", unsafe.Offsetof(vp.MFX), 16},
		{"mfxVideoParam.Protected", unsafe.Offsetof(vp.Protected), 184},
		{"mfxVideoParam.IOPattern", unsafe.Offsetof(vp.IOPattern), 186},
		{"mfxVideoParam.ExtParam", unsafe.Offsetof(vp.ExtParam), 192},
		{"mfxVideoParam.NumExtParam", unsafe.Offsetof(vp.NumExtParam), 200},

		{"sizeof mfxBitstream", unsafe.Sizeof(bs), 72},
		{"mfxBitstream.EncryptedData", unsafe.Offsetof(bs.EncryptedData), 0},
		{"mfxBitstream.ExtParam", unsafe.Offsetof(bs.ExtParam), 8},
		{"mfxBitstream.NumExtParam", unsafe.Offsetof(bs.NumExtParam), 16},
		{"mfxBitstream.CodecId", unsafe.Offsetof(bs.CodecID), 20},
		{"mfxBitstream.DecodeTimeStamp", unsafe.Offsetof(bs.DecodeTimeStamp), 24},
		{"mfxBitstream.TimeStamp", unsafe.Offsetof(bs.TimeStamp), 32},
		{"mfxBitstream.Data", unsafe.Offsetof(bs.Data), 40},
		{"mfxBitstream.DataOffset", unsafe.Offsetof(bs.DataOffset), 48},
		{"mfxBitstream.DataLength", unsafe.Offsetof(bs.DataLength), 52},
		{"mfxBitstream.MaxLength", unsafe.Offsetof(bs.MaxLength), 56},
		{"mfxBitstream.PicStruct", unsafe.Offsetof(bs.PicStruct), 60},
		{"mfxBitstream.FrameType", unsafe.Offsetof(bs.FrameType), 62},
		{"mfxBitstream.DataFlag", unsafe.Offsetof(bs.DataFlag), 64},

		{"sizeof mfxFrameData", unsafe.Sizeof(fd), 96},
		{"mfxFrameData.ExtParam", unsafe.Offsetof(fd.ExtParam), 0},
		{"mfxFrameData.NumExtParam", unsafe.Offsetof(fd.NumExtParam), 8},
		{"mfxFrameData.MemType", unsafe.Offsetof(fd.MemType), 28},
		{"mfxFrameData.PitchHigh", unsafe.Offsetof(fd.PitchHigh), 30},
		{"mfxFrameData.TimeStamp", unsafe.Offsetof(fd.TimeStamp), 32},
		{"mfxFrameData.FrameOrder", unsafe.Offsetof(fd.FrameOrder), 40},
		{"mfxFrameData.Locked", unsafe.Offsetof(fd.Locked), 44},
		{"mfxFrameData.Pitch", unsafe.Offsetof(fd.Pitch), 46},
		{"mfxFrameData.Y", unsafe.Offsetof(fd.Y), 48},
		{"mfxFrameData.UV", unsafe.Offsetof(fd.UV), 56},
		{"mfxFrameData.V", unsafe.Offsetof(fd.V), 64},
		{"mfxFrameData.A", unsafe.Offsetof(fd.A), 72},
		{"mfxFrameData.MemId", unsafe.Offsetof(fd.MemID), 80},
		{"mfxFrameData.Corrupted", unsafe.Offsetof(fd.Corrupted), 88},
		{"mfxFrameData.DataFlag", unsafe.Offsetof(fd.DataFlag), 90},

		{"sizeof mfxFrameSurface1", unsafe.Sizeof(fs), 184},
		{"mfxFrameSurface1.FrameInterface", unsafe.Offsetof(fs.FrameInterface), 0},
		{"mfxFrameSurface1.Version", unsafe.Offsetof(fs.Version), 8},
		{"mfxFrameSurface1.Info", unsafe.Offsetof(fs.Info), 16},
		{"mfxFrameSurface1.Data", unsafe.Offsetof(fs.Data), 88},

		{"sizeof mfxFrameAllocRequest", unsafe.Sizeof(ar), 92},
		{"mfxFrameAllocRequest.AllocId", unsafe.Offsetof(ar.AllocID), 0},
		{"mfxFrameAllocRequest.Info", unsafe.Offsetof(ar.Info), 16},
		{"mfxFrameAllocRequest.Type", unsafe.Offsetof(ar.Type), 84},
		{"mfxFrameAllocRequest.NumFrameMin", unsafe.Offsetof(ar.NumFrameMin), 86},
		{"mfxFrameAllocRequest.NumFrameSuggested", unsafe.Offsetof(ar.NumFrameSuggested), 88},

		{"sizeof mfxEncodeCtrl", unsafe.Sizeof(ec), 56},
		{"mfxEncodeCtrl.Header", unsafe.Offsetof(ec.Header), 0},
		{"mfxEncodeCtrl.MfxNalUnitType", unsafe.Offsetof(ec.MfxNalUnitType), 26},
		{"mfxEncodeCtrl.SkipFrame", unsafe.Offsetof(ec.SkipFrame), 28},
		{"mfxEncodeCtrl.QP", unsafe.Offsetof(ec.QP), 30},
		{"mfxEncodeCtrl.FrameType", unsafe.Offsetof(ec.FrameType), 32},
		{"mfxEncodeCtrl.NumExtParam", unsafe.Offsetof(ec.NumExtParam), 34},
		{"mfxEncodeCtrl.NumPayload", unsafe.Offsetof(ec.NumPayload), 36},
		{"mfxEncodeCtrl.ExtParam", unsafe.Offsetof(ec.ExtParam), 40},
		{"mfxEncodeCtrl.Payload", unsafe.Offsetof(ec.Payload), 48},

		{"sizeof mfxExtCodingOption", unsafe.Sizeof(co), 64},
		{"mfxExtCodingOption.RateDistortionOpt", unsafe.Offsetof(co.RateDistortionOpt), 10},
		{"mfxExtCodingOption.MVSearchWindow", unsafe.Offsetof(co.MVSearchWindow), 16},
		{"mfxExtCodingOption.FramePicture", unsafe.Offsetof(co.FramePicture), 22},
		{"mfxExtCodingOption.CAVLC", unsafe.Offsetof(co.CAVLC), 24},
		{"mfxExtCodingOption.RecoveryPointSEI", unsafe.Offsetof(co.RecoveryPointSEI), 30},
		{"mfxExtCodingOption.NalHrdConformance", unsafe.Offsetof(co.NalHrdConformance), 34},
		{"mfxExtCodingOption.SingleSeiNalUnit", unsafe.Offsetof(co.SingleSeiNalUnit), 36},
		{"mfxExtCodingOption.VuiVclHrdParameters", unsafe.Offsetof(co.VuiVclHrdParameters), 38},
		{"mfxExtCodingOption.RefPicMarkRep", unsafe.Offsetof(co.RefPicMarkRep), 44},
		{"mfxExtCodingOption.MaxDecFrameBuffering", unsafe.Offsetof(co.MaxDecFrameBuffering), 54},
		{"mfxExtCodingOption.AUDelimiter", unsafe.Offsetof(co.AUDelimiter), 56},
		{"mfxExtCodingOption.PicTimingSEI", unsafe.Offsetof(co.PicTimingSEI), 60},
		{"mfxExtCodingOption.VuiNalHrdParameters", unsafe.Offsetof(co.VuiNalHrdParameters), 62},

		{"sizeof mfxExtCodingOption2", unsafe.Sizeof(co2), 68},
		{"mfxExtCodingOption2.IntRefType", unsafe.Offsetof(co2.IntRefType), 8},
		{"mfxExtCodingOption2.IntRefQPDelta", unsafe.Offsetof(co2.IntRefQPDelta), 12},
		{"mfxExtCodingOption2.MaxFrameSize", unsafe.Offsetof(co2.MaxFrameSize), 16},
		{"mfxExtCodingOption2.MaxSliceSize", unsafe.Offsetof(co2.MaxSliceSize), 20},
		{"mfxExtCodingOption2.BitrateLimit", unsafe.Offsetof(co2.BitrateLimit), 24},
		{"mfxExtCodingOption2.MBBRC", unsafe.Offsetof(co2.MBBRC), 26},
		{"mfxExtCodingOption2.ExtBRC", unsafe.Offsetof(co2.ExtBRC), 28},
		{"mfxExtCodingOption2.LookAheadDepth", unsafe.Offsetof(co2.LookAheadDepth), 30},
		{"mfxExtCodingOption2.Trellis", unsafe.Offsetof(co2.Trellis), 32},
		{"mfxExtCodingOption2.RepeatPPS", unsafe.Offsetof(co2.RepeatPPS), 34},
		{"mfxExtCodingOption2.BRefType", unsafe.Offsetof(co2.BRefType), 36},
		{"mfxExtCodingOption2.AdaptiveI", unsafe.Offsetof(co2.AdaptiveI), 38},
		{"mfxExtCodingOption2.AdaptiveB", unsafe.Offsetof(co2.AdaptiveB), 40},
		{"mfxExtCodingOption2.LookAheadDS", unsafe.Offsetof(co2.LookAheadDS), 42},
		{"mfxExtCodingOption2.NumMbPerSlice", unsafe.Offsetof(co2.NumMbPerSlice), 44},
		{"mfxExtCodingOption2.SkipFrame", unsafe.Offsetof(co2.SkipFrame), 46},
		{"mfxExtCodingOption2.MinQPI", unsafe.Offsetof(co2.MinQPI), 48},
		{"mfxExtCodingOption2.MaxQPB", unsafe.Offsetof(co2.MaxQPB), 53},
		{"mfxExtCodingOption2.FixedFrameRate", unsafe.Offsetof(co2.FixedFrameRate), 54},
		{"mfxExtCodingOption2.DisableDeblockingIdc", unsafe.Offsetof(co2.DisableDeblockingIdc), 56},
		{"mfxExtCodingOption2.DisableVUI", unsafe.Offsetof(co2.DisableVUI), 58},
		{"mfxExtCodingOption2.BufferingPeriodSEI", unsafe.Offsetof(co2.BufferingPeriodSEI), 60},
		{"mfxExtCodingOption2.EnableMAD", unsafe.Offsetof(co2.EnableMAD), 62},
		{"mfxExtCodingOption2.UseRawRef", unsafe.Offsetof(co2.UseRawRef), 64},

		{"sizeof mfxExtCodingOption3", unsafe.Sizeof(co3), 512},
		{"mfxExtCodingOption3.GPB", unsafe.Offsetof(co3.GPB), 66},

		{"sizeof mfxExtCodingOptionSPSPPS", unsafe.Sizeof(sp), 32},
		{"mfxExtCodingOptionSPSPPS.SPSBuffer", unsafe.Offsetof(sp.SPSBuffer), 8},
		{"mfxExtCodingOptionSPSPPS.PPSBuffer", unsafe.Offsetof(sp.PPSBuffer), 16},
		{"mfxExtCodingOptionSPSPPS.SPSBufSize", unsafe.Offsetof(sp.SPSBufSize), 24},
		{"mfxExtCodingOptionSPSPPS.PPSBufSize", unsafe.Offsetof(sp.PPSBufSize), 26},
		{"mfxExtCodingOptionSPSPPS.SPSId", unsafe.Offsetof(sp.SPSID), 28},
		{"mfxExtCodingOptionSPSPPS.PPSId", unsafe.Offsetof(sp.PPSID), 30},

		{"sizeof mfxExtCodingOptionVPS", unsafe.Sizeof(vps), 32},
		{"mfxExtCodingOptionVPS.VPSBuffer", unsafe.Offsetof(vps.VPSBuffer), 8},
		{"mfxExtCodingOptionVPS.VPSBufSize", unsafe.Offsetof(vps.VPSBufSize), 16},
		{"mfxExtCodingOptionVPS.VPSId", unsafe.Offsetof(vps.VPSID), 18},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestConstants checks the FourCC-style identifiers and the values that the
// dispatcher filter is built from against the same C program.
func TestConstants(t *testing.T) {
	checks := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"MFX_CODEC_AVC", uint64(CodecAVC), 541283905},
		{"MFX_CODEC_HEVC", uint64(CodecHEVC), 1129727304},
		{"MFX_CODEC_AV1", uint64(CodecAV1), 540104257},
		{"MFX_PROFILE_AV1_MAIN", uint64(ProfileAV1Main), 1},
		{"MFX_FOURCC_NV12", uint64(FourCCNV12), 842094158},
		{"MFX_EXTBUFF_CODING_OPTION", uint64(ExtBuffCodingOption), 1347372099},
		{"MFX_EXTBUFF_CODING_OPTION2", uint64(ExtBuffCodingOption2), 844055619},
		{"MFX_EXTBUFF_CODING_OPTION3", uint64(ExtBuffCodingOption3), 860832835},
		{"MFX_EXTBUFF_CODING_OPTION_SPSPPS", uint64(ExtBuffCodingOptionSPSPPS), 1347637059},
		{"MFX_EXTBUFF_CODING_OPTION_VPS", uint64(ExtBuffCodingOptionVPS), 1347833667},
		{"MFX_PROFILE_AVC_CONSTRAINED_BASELINE", uint64(ProfileAVCConstrainedBaseline), 578},
		{"MFX_VARIANT_TYPE_U32", uint64(VariantTypeU32), 5},
		{"MFX_VARIANT_VERSION", uint64(VariantVersion), 256},
		{"MFX_HANDLE_VA_DISPLAY", uint64(HandleVADisplay), 4},
		{"MFX_INFINITE", uint64(Infinite), 4294967295},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestVariantWords checks the two register words that carry an mfxVariant
// by value: Version at byte 0, Type at byte 4, Data at byte 8.
func TestVariantWords(t *testing.T) {
	lo, hi := VariantU32(ImplTypeHardware)
	if lo != 0x0000_0005_0000_0100 || hi != 2 {
		t.Errorf("VariantU32(2) = %#x, %#x", lo, hi)
	}
}
