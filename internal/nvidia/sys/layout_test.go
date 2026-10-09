package sys

import (
	"testing"
	"unsafe"
)

// layoutCheck compares one size or field offset with the value a C compiler
// reports.
type layoutCheck struct {
	name string
	got  uintptr
	want uintptr
}

// The expected values were printed by a C program (offsetof/sizeof)
// compiled against nv-codec-headers n11.1.5.3 (dynlink_cuda.h,
// dynlink_cuviddec.h, dynlink_nvcuvid.h, nvEncodeAPI.h) and cross-checked
// with clang -fsyntax-only static assertions for x86_64-linux-gnu and
// aarch64-linux-gnu; both targets agree on every value. Windows x64
// (x86_64-w64-mingw32-gcc and clang --target=x86_64-pc-windows-msvc) agrees
// with them too, except for the two structures with unsigned long fields,
// whose expectations live in ulongLayoutChecks per ABI.
func TestStructLayouts(t *testing.T) {
	var mc Memcpy2D
	var vf VideoFormat
	var pp ParserParams
	var di ParserDispInfo
	var dc DecodeCaps
	var dp DecodePicParams
	var pr ProcParams
	var ds GetDecodeStatus

	var g GUID
	var cp CapsParam
	var cib CreateInputBuffer
	var cbb CreateBitstreamBuffer
	var qp QP
	var rc RCParams
	var vui VUIParameters
	var me MEHintCounts
	var h264 ConfigH264
	var hevc ConfigHEVC
	var cc CodecConfig
	var cfg Config
	var ip InitializeParams
	var pc PresetConfig
	var ph PicParamsH264
	var pv PicParamsHEVC
	var cpp CodecPicParams
	var ep PicParams
	var lb LockBitstream
	var li LockInputBuffer
	var spp SequenceParamPayload
	var os OpenEncodeSessionExParams
	var fl FunctionList

	checks := []layoutCheck{
		{"sizeof CUDA_MEMCPY2D", unsafe.Sizeof(mc), 128},
		{"CUDA_MEMCPY2D.srcMemoryType", unsafe.Offsetof(mc.SrcMemoryType), 16},
		{"CUDA_MEMCPY2D.srcHost", unsafe.Offsetof(mc.SrcHost), 24},
		{"CUDA_MEMCPY2D.srcDevice", unsafe.Offsetof(mc.SrcDevice), 32},
		{"CUDA_MEMCPY2D.srcPitch", unsafe.Offsetof(mc.SrcPitch), 48},
		{"CUDA_MEMCPY2D.dstXInBytes", unsafe.Offsetof(mc.DstXInBytes), 56},
		{"CUDA_MEMCPY2D.dstMemoryType", unsafe.Offsetof(mc.DstMemoryType), 72},
		{"CUDA_MEMCPY2D.dstHost", unsafe.Offsetof(mc.DstHost), 80},
		{"CUDA_MEMCPY2D.dstDevice", unsafe.Offsetof(mc.DstDevice), 88},
		{"CUDA_MEMCPY2D.dstPitch", unsafe.Offsetof(mc.DstPitch), 104},
		{"CUDA_MEMCPY2D.WidthInBytes", unsafe.Offsetof(mc.WidthInBytes), 112},
		{"CUDA_MEMCPY2D.Height", unsafe.Offsetof(mc.Height), 120},

		{"sizeof CUVIDEOFORMAT", unsafe.Sizeof(vf), 64},
		{"CUVIDEOFORMAT.frame_rate", unsafe.Offsetof(vf.FrameRateNumerator), 4},
		{"CUVIDEOFORMAT.progressive_sequence", unsafe.Offsetof(vf.ProgressiveSequence), 12},
		{"CUVIDEOFORMAT.min_num_decode_surfaces", unsafe.Offsetof(vf.MinNumDecodeSurfaces), 15},
		{"CUVIDEOFORMAT.coded_width", unsafe.Offsetof(vf.CodedWidth), 16},
		{"CUVIDEOFORMAT.coded_height", unsafe.Offsetof(vf.CodedHeight), 20},
		{"CUVIDEOFORMAT.display_area", unsafe.Offsetof(vf.DisplayArea), 24},
		{"CUVIDEOFORMAT.chroma_format", unsafe.Offsetof(vf.ChromaFormat), 40},
		{"CUVIDEOFORMAT.bitrate", unsafe.Offsetof(vf.Bitrate), 44},
		{"CUVIDEOFORMAT.display_aspect_ratio", unsafe.Offsetof(vf.DisplayAspectRatioX), 48},
		{"CUVIDEOFORMAT.video_signal_description", unsafe.Offsetof(vf.VideoSignal), 56},
		{"CUVIDEOFORMAT.seqhdr_data_length", unsafe.Offsetof(vf.SeqHdrDataLength), 60},

		{"sizeof CUVIDPARSERPARAMS", unsafe.Sizeof(pp), 136},
		{"CUVIDPARSERPARAMS.ulMaxDisplayDelay", unsafe.Offsetof(pp.MaxDisplayDelay), 16},
		{"CUVIDPARSERPARAMS.uReserved1", unsafe.Offsetof(pp.Reserved1), 24},
		{"CUVIDPARSERPARAMS.pUserData", unsafe.Offsetof(pp.UserData), 40},
		{"CUVIDPARSERPARAMS.pfnSequenceCallback", unsafe.Offsetof(pp.SequenceCallback), 48},
		{"CUVIDPARSERPARAMS.pfnDecodePicture", unsafe.Offsetof(pp.DecodePicture), 56},
		{"CUVIDPARSERPARAMS.pfnDisplayPicture", unsafe.Offsetof(pp.DisplayPicture), 64},
		{"CUVIDPARSERPARAMS.pfnGetOperatingPoint", unsafe.Offsetof(pp.GetOperatingPoint), 72},
		{"CUVIDPARSERPARAMS.pvReserved2", unsafe.Offsetof(pp.Reserved2), 80},
		{"CUVIDPARSERPARAMS.pExtVideoInfo", unsafe.Offsetof(pp.ExtVideoInfo), 128},

		{"sizeof CUVIDPARSERDISPINFO", unsafe.Sizeof(di), 24},
		{"CUVIDPARSERDISPINFO.timestamp", unsafe.Offsetof(di.Timestamp), 16},

		{"sizeof CUVIDDECODECAPS", unsafe.Sizeof(dc), 88},
		{"CUVIDDECODECAPS.reserved1", unsafe.Offsetof(dc.Reserved1), 12},
		{"CUVIDDECODECAPS.bIsSupported", unsafe.Offsetof(dc.IsSupported), 24},
		{"CUVIDDECODECAPS.nNumNVDECs", unsafe.Offsetof(dc.NumNVDECs), 25},
		{"CUVIDDECODECAPS.nOutputFormatMask", unsafe.Offsetof(dc.OutputFormatMask), 26},
		{"CUVIDDECODECAPS.nMaxWidth", unsafe.Offsetof(dc.MaxWidth), 28},
		{"CUVIDDECODECAPS.nMaxHeight", unsafe.Offsetof(dc.MaxHeight), 32},
		{"CUVIDDECODECAPS.nMaxMBCount", unsafe.Offsetof(dc.MaxMBCount), 36},
		{"CUVIDDECODECAPS.nMinWidth", unsafe.Offsetof(dc.MinWidth), 40},
		{"CUVIDDECODECAPS.nMinHeight", unsafe.Offsetof(dc.MinHeight), 42},
		{"CUVIDDECODECAPS.bIsHistogramSupported", unsafe.Offsetof(dc.IsHistogramSupported), 44},
		{"CUVIDDECODECAPS.nMaxHistogramBins", unsafe.Offsetof(dc.MaxHistogramBins), 46},
		{"CUVIDDECODECAPS.reserved3", unsafe.Offsetof(dc.Reserved3), 48},

		{"sizeof CUVIDPICPARAMS", unsafe.Sizeof(dp), 4280},
		{"CUVIDPICPARAMS.CurrPicIdx", unsafe.Offsetof(dp.CurrPicIdx), 8},
		{"CUVIDPICPARAMS.field_pic_flag", unsafe.Offsetof(dp.FieldPicFlag), 12},
		{"CUVIDPICPARAMS.second_field", unsafe.Offsetof(dp.SecondField), 20},
		{"CUVIDPICPARAMS.nBitstreamDataLen", unsafe.Offsetof(dp.BitstreamDataLen), 24},
		{"CUVIDPICPARAMS.pBitstreamData", unsafe.Offsetof(dp.BitstreamData), 32},
		{"CUVIDPICPARAMS.nNumSlices", unsafe.Offsetof(dp.NumSlices), 40},
		{"CUVIDPICPARAMS.pSliceDataOffsets", unsafe.Offsetof(dp.SliceDataOffsets), 48},
		{"CUVIDPICPARAMS.ref_pic_flag", unsafe.Offsetof(dp.RefPicFlag), 56},
		{"CUVIDPICPARAMS.intra_pic_flag", unsafe.Offsetof(dp.IntraPicFlag), 60},
		{"CUVIDPICPARAMS.Reserved", unsafe.Offsetof(dp.Reserved), 64},
		{"CUVIDPICPARAMS.CodecSpecific", unsafe.Offsetof(dp.CodecSpecific), 184},

		{"sizeof CUVIDPROCPARAMS", unsafe.Sizeof(pr), 264},
		{"CUVIDPROCPARAMS.raw_input_dptr", unsafe.Offsetof(pr.RawInputDptr), 24},
		{"CUVIDPROCPARAMS.raw_output_dptr", unsafe.Offsetof(pr.RawOutputDptr), 40},
		{"CUVIDPROCPARAMS.Reserved1", unsafe.Offsetof(pr.Reserved1), 52},
		{"CUVIDPROCPARAMS.output_stream", unsafe.Offsetof(pr.OutputStream), 56},
		{"CUVIDPROCPARAMS.Reserved", unsafe.Offsetof(pr.Reserved), 64},
		{"CUVIDPROCPARAMS.histogram_dptr", unsafe.Offsetof(pr.HistogramDptr), 248},
		{"CUVIDPROCPARAMS.Reserved2", unsafe.Offsetof(pr.Reserved2), 256},

		{"sizeof CUVIDGETDECODESTATUS", unsafe.Sizeof(ds), 192},

		{"sizeof GUID", unsafe.Sizeof(g), 16},
		{"GUID.Data2", unsafe.Offsetof(g.Data2), 4},
		{"GUID.Data3", unsafe.Offsetof(g.Data3), 6},
		{"GUID.Data4", unsafe.Offsetof(g.Data4), 8},

		{"sizeof NV_ENC_CAPS_PARAM", unsafe.Sizeof(cp), 256},
		{"NV_ENC_CAPS_PARAM.reserved", unsafe.Offsetof(cp.Reserved), 8},

		{"sizeof NV_ENC_CREATE_INPUT_BUFFER", unsafe.Sizeof(cib), 776},
		{"NV_ENC_CREATE_INPUT_BUFFER.bufferFmt", unsafe.Offsetof(cib.BufferFmt), 16},
		{"NV_ENC_CREATE_INPUT_BUFFER.inputBuffer", unsafe.Offsetof(cib.InputBuffer), 24},
		{"NV_ENC_CREATE_INPUT_BUFFER.pSysMemBuffer", unsafe.Offsetof(cib.SysMemBuffer), 32},
		{"NV_ENC_CREATE_INPUT_BUFFER.reserved1", unsafe.Offsetof(cib.Reserved1), 40},
		{"NV_ENC_CREATE_INPUT_BUFFER.reserved2", unsafe.Offsetof(cib.Reserved2), 272},

		{"sizeof NV_ENC_CREATE_BITSTREAM_BUFFER", unsafe.Sizeof(cbb), 776},
		{"NV_ENC_CREATE_BITSTREAM_BUFFER.bitstreamBuffer", unsafe.Offsetof(cbb.BitstreamBuffer), 16},
		{"NV_ENC_CREATE_BITSTREAM_BUFFER.bitstreamBufferPtr", unsafe.Offsetof(cbb.BitstreamBufferPtr), 24},
		{"NV_ENC_CREATE_BITSTREAM_BUFFER.reserved1", unsafe.Offsetof(cbb.Reserved1), 32},
		{"NV_ENC_CREATE_BITSTREAM_BUFFER.reserved2", unsafe.Offsetof(cbb.Reserved2), 264},

		{"sizeof NV_ENC_QP", unsafe.Sizeof(qp), 12},

		{"sizeof NV_ENC_RC_PARAMS", unsafe.Sizeof(rc), 128},
		{"NV_ENC_RC_PARAMS.constQP", unsafe.Offsetof(rc.ConstQP), 8},
		{"NV_ENC_RC_PARAMS.averageBitRate", unsafe.Offsetof(rc.AverageBitRate), 20},
		{"NV_ENC_RC_PARAMS.maxBitRate", unsafe.Offsetof(rc.MaxBitRate), 24},
		{"NV_ENC_RC_PARAMS.vbvBufferSize", unsafe.Offsetof(rc.VBVBufferSize), 28},
		{"NV_ENC_RC_PARAMS.vbvInitialDelay", unsafe.Offsetof(rc.VBVInitialDelay), 32},
		{"NV_ENC_RC_PARAMS.bitfields", unsafe.Offsetof(rc.Flags), 36},
		{"NV_ENC_RC_PARAMS.minQP", unsafe.Offsetof(rc.MinQP), 40},
		{"NV_ENC_RC_PARAMS.maxQP", unsafe.Offsetof(rc.MaxQP), 52},
		{"NV_ENC_RC_PARAMS.initialRCQP", unsafe.Offsetof(rc.InitialRCQP), 64},
		{"NV_ENC_RC_PARAMS.temporallayerIdxMask", unsafe.Offsetof(rc.TemporalLayerIdxMask), 76},
		{"NV_ENC_RC_PARAMS.temporalLayerQP", unsafe.Offsetof(rc.TemporalLayerQP), 80},
		{"NV_ENC_RC_PARAMS.targetQuality", unsafe.Offsetof(rc.TargetQuality), 88},
		{"NV_ENC_RC_PARAMS.targetQualityLSB", unsafe.Offsetof(rc.TargetQualityLSB), 89},
		{"NV_ENC_RC_PARAMS.lookaheadDepth", unsafe.Offsetof(rc.LookaheadDepth), 90},
		{"NV_ENC_RC_PARAMS.lowDelayKeyFrameScale", unsafe.Offsetof(rc.LowDelayKeyFrameScale), 92},
		{"NV_ENC_RC_PARAMS.qpMapMode", unsafe.Offsetof(rc.QPMapMode), 96},
		{"NV_ENC_RC_PARAMS.multiPass", unsafe.Offsetof(rc.MultiPass), 100},
		{"NV_ENC_RC_PARAMS.alphaLayerBitrateRatio", unsafe.Offsetof(rc.AlphaLayerBitrateRatio), 104},
		{"NV_ENC_RC_PARAMS.cbQPIndexOffset", unsafe.Offsetof(rc.CbQPIndexOffset), 108},
		{"NV_ENC_RC_PARAMS.crQPIndexOffset", unsafe.Offsetof(rc.CrQPIndexOffset), 109},
		{"NV_ENC_RC_PARAMS.reserved2", unsafe.Offsetof(rc.Reserved2), 110},
		{"NV_ENC_RC_PARAMS.reserved", unsafe.Offsetof(rc.Reserved), 112},

		{"sizeof NV_ENC_CONFIG_H264_VUI_PARAMETERS", unsafe.Sizeof(vui), 112},
		{"VUI.videoSignalTypePresentFlag", unsafe.Offsetof(vui.VideoSignalTypePresentFlag), 8},
		{"VUI.colourDescriptionPresentFlag", unsafe.Offsetof(vui.ColourDescriptionPresentFlag), 20},
		{"VUI.colourMatrix", unsafe.Offsetof(vui.ColourMatrix), 32},
		{"VUI.bitstreamRestrictionFlag", unsafe.Offsetof(vui.BitstreamRestrictionFlag), 48},
		{"VUI.reserved", unsafe.Offsetof(vui.Reserved), 52},
		{"sizeof NVENC_EXTERNAL_ME_HINT_COUNTS_PER_BLOCKTYPE", unsafe.Sizeof(me), 16},

		{"sizeof NV_ENC_CONFIG_H264", unsafe.Sizeof(h264), 1792},
		{"NV_ENC_CONFIG_H264.level", unsafe.Offsetof(h264.Level), 4},
		{"NV_ENC_CONFIG_H264.idrPeriod", unsafe.Offsetof(h264.IDRPeriod), 8},
		{"NV_ENC_CONFIG_H264.spsId", unsafe.Offsetof(h264.SPSID), 24},
		{"NV_ENC_CONFIG_H264.adaptiveTransformMode", unsafe.Offsetof(h264.AdaptiveTransformMode), 32},
		{"NV_ENC_CONFIG_H264.entropyCodingMode", unsafe.Offsetof(h264.EntropyCodingMode), 44},
		{"NV_ENC_CONFIG_H264.maxNumRefFrames", unsafe.Offsetof(h264.MaxNumRefFrames), 60},
		{"NV_ENC_CONFIG_H264.sliceMode", unsafe.Offsetof(h264.SliceMode), 64},
		{"NV_ENC_CONFIG_H264.h264VUIParameters", unsafe.Offsetof(h264.VUI), 72},
		{"NV_ENC_CONFIG_H264.ltrNumFrames", unsafe.Offsetof(h264.LTRNumFrames), 184},
		{"NV_ENC_CONFIG_H264.chromaFormatIDC", unsafe.Offsetof(h264.ChromaFormatIDC), 192},
		{"NV_ENC_CONFIG_H264.useBFramesAsRef", unsafe.Offsetof(h264.UseBFramesAsRef), 200},
		{"NV_ENC_CONFIG_H264.numRefL1", unsafe.Offsetof(h264.NumRefL1), 208},
		{"NV_ENC_CONFIG_H264.reserved1", unsafe.Offsetof(h264.Reserved1), 212},
		{"NV_ENC_CONFIG_H264.reserved2", unsafe.Offsetof(h264.Reserved2), 1280},

		{"sizeof NV_ENC_CONFIG_HEVC", unsafe.Sizeof(hevc), 1560},
		{"NV_ENC_CONFIG_HEVC.maxCUSize", unsafe.Offsetof(hevc.MaxCUSize), 12},
		{"NV_ENC_CONFIG_HEVC.bitfields", unsafe.Offsetof(hevc.Flags), 16},
		{"NV_ENC_CONFIG_HEVC.idrPeriod", unsafe.Offsetof(hevc.IDRPeriod), 20},
		{"NV_ENC_CONFIG_HEVC.maxNumRefFramesInDPB", unsafe.Offsetof(hevc.MaxNumRefFramesInDPB), 32},
		{"NV_ENC_CONFIG_HEVC.vpsId", unsafe.Offsetof(hevc.VPSID), 40},
		{"NV_ENC_CONFIG_HEVC.sliceMode", unsafe.Offsetof(hevc.SliceMode), 52},
		{"NV_ENC_CONFIG_HEVC.maxTemporalLayersMinus1", unsafe.Offsetof(hevc.MaxTemporalLayersMinus1), 60},
		{"NV_ENC_CONFIG_HEVC.hevcVUIParameters", unsafe.Offsetof(hevc.VUI), 64},
		{"NV_ENC_CONFIG_HEVC.ltrTrustMode", unsafe.Offsetof(hevc.LTRTrustMode), 176},
		{"NV_ENC_CONFIG_HEVC.numRefL1", unsafe.Offsetof(hevc.NumRefL1), 188},
		{"NV_ENC_CONFIG_HEVC.reserved1", unsafe.Offsetof(hevc.Reserved1), 192},
		{"NV_ENC_CONFIG_HEVC.reserved2", unsafe.Offsetof(hevc.Reserved2), 1048},

		{"sizeof NV_ENC_CODEC_CONFIG", unsafe.Sizeof(cc), 1792},
		{"alignof NV_ENC_CODEC_CONFIG", unsafe.Alignof(cc), 8},

		{"sizeof NV_ENC_CONFIG", unsafe.Sizeof(cfg), 3584},
		{"NV_ENC_CONFIG.profileGUID", unsafe.Offsetof(cfg.ProfileGUID), 4},
		{"NV_ENC_CONFIG.gopLength", unsafe.Offsetof(cfg.GOPLength), 20},
		{"NV_ENC_CONFIG.frameIntervalP", unsafe.Offsetof(cfg.FrameIntervalP), 24},
		{"NV_ENC_CONFIG.frameFieldMode", unsafe.Offsetof(cfg.FrameFieldMode), 32},
		{"NV_ENC_CONFIG.mvPrecision", unsafe.Offsetof(cfg.MVPrecision), 36},
		{"NV_ENC_CONFIG.rcParams", unsafe.Offsetof(cfg.RC), 40},
		{"NV_ENC_CONFIG.encodeCodecConfig", unsafe.Offsetof(cfg.CodecConfig), 168},
		{"NV_ENC_CONFIG.reserved", unsafe.Offsetof(cfg.Reserved), 1960},
		{"NV_ENC_CONFIG.reserved2", unsafe.Offsetof(cfg.Reserved2), 3072},

		{"sizeof NV_ENC_INITIALIZE_PARAMS", unsafe.Sizeof(ip), 1808},
		{"NV_ENC_INITIALIZE_PARAMS.presetGUID", unsafe.Offsetof(ip.PresetGUID), 20},
		{"NV_ENC_INITIALIZE_PARAMS.encodeWidth", unsafe.Offsetof(ip.EncodeWidth), 36},
		{"NV_ENC_INITIALIZE_PARAMS.frameRateNum", unsafe.Offsetof(ip.FrameRateNum), 52},
		{"NV_ENC_INITIALIZE_PARAMS.enablePTD", unsafe.Offsetof(ip.EnablePTD), 64},
		{"NV_ENC_INITIALIZE_PARAMS.bitfields", unsafe.Offsetof(ip.Flags), 68},
		{"NV_ENC_INITIALIZE_PARAMS.privDataSize", unsafe.Offsetof(ip.PrivDataSize), 72},
		{"NV_ENC_INITIALIZE_PARAMS.privData", unsafe.Offsetof(ip.PrivData), 80},
		{"NV_ENC_INITIALIZE_PARAMS.encodeConfig", unsafe.Offsetof(ip.EncodeConfig), 88},
		{"NV_ENC_INITIALIZE_PARAMS.maxEncodeWidth", unsafe.Offsetof(ip.MaxEncodeWidth), 96},
		{"NV_ENC_INITIALIZE_PARAMS.maxMEHintCountsPerBlock", unsafe.Offsetof(ip.MaxMEHintCountsPerBlock), 104},
		{"NV_ENC_INITIALIZE_PARAMS.tuningInfo", unsafe.Offsetof(ip.TuningInfo), 136},
		{"NV_ENC_INITIALIZE_PARAMS.bufferFormat", unsafe.Offsetof(ip.BufferFormat), 140},
		{"NV_ENC_INITIALIZE_PARAMS.reserved", unsafe.Offsetof(ip.Reserved), 144},
		{"NV_ENC_INITIALIZE_PARAMS.reserved2", unsafe.Offsetof(ip.Reserved2), 1296},

		{"sizeof NV_ENC_PRESET_CONFIG", unsafe.Sizeof(pc), 5128},
		{"NV_ENC_PRESET_CONFIG.presetCfg", unsafe.Offsetof(pc.PresetCfg), 8},
		{"NV_ENC_PRESET_CONFIG.reserved1", unsafe.Offsetof(pc.Reserved1), 3592},
		{"NV_ENC_PRESET_CONFIG.reserved2", unsafe.Offsetof(pc.Reserved2), 4616},

		{"sizeof NV_ENC_PIC_PARAMS_H264", unsafe.Sizeof(ph), 1536},
		{"NV_ENC_PIC_PARAMS_H264.bitfields", unsafe.Offsetof(ph.Flags), 20},
		{"NV_ENC_PIC_PARAMS_H264.sliceTypeData", unsafe.Offsetof(ph.SliceTypeData), 24},
		{"NV_ENC_PIC_PARAMS_H264.seiPayloadArray", unsafe.Offsetof(ph.SEIPayloadArray), 40},
		{"NV_ENC_PIC_PARAMS_H264.forceIntraSliceIdx", unsafe.Offsetof(ph.ForceIntraSliceIdx), 72},
		{"NV_ENC_PIC_PARAMS_H264.h264ExtPicParams", unsafe.Offsetof(ph.H264ExtPicParams), 80},
		{"NV_ENC_PIC_PARAMS_H264.reserved", unsafe.Offsetof(ph.Reserved), 208},
		{"NV_ENC_PIC_PARAMS_H264.reserved2", unsafe.Offsetof(ph.Reserved2), 1048},

		{"sizeof NV_ENC_PIC_PARAMS_HEVC", unsafe.Sizeof(pv), 1536},
		{"NV_ENC_PIC_PARAMS_HEVC.bitfields", unsafe.Offsetof(pv.Flags), 16},
		{"NV_ENC_PIC_PARAMS_HEVC.sliceTypeData", unsafe.Offsetof(pv.SliceTypeData), 24},
		{"NV_ENC_PIC_PARAMS_HEVC.seiPayloadArrayCnt", unsafe.Offsetof(pv.SEIPayloadArrayCnt), 56},
		{"NV_ENC_PIC_PARAMS_HEVC.seiPayloadArray", unsafe.Offsetof(pv.SEIPayloadArray), 64},
		{"NV_ENC_PIC_PARAMS_HEVC.reserved2", unsafe.Offsetof(pv.Reserved2), 72},
		{"NV_ENC_PIC_PARAMS_HEVC.reserved3", unsafe.Offsetof(pv.Reserved3), 1048},

		{"sizeof NV_ENC_CODEC_PIC_PARAMS", unsafe.Sizeof(cpp), 1536},

		{"sizeof NV_ENC_PIC_PARAMS", unsafe.Sizeof(ep), 3344},
		{"NV_ENC_PIC_PARAMS.encodePicFlags", unsafe.Offsetof(ep.EncodePicFlags), 16},
		{"NV_ENC_PIC_PARAMS.inputTimeStamp", unsafe.Offsetof(ep.InputTimeStamp), 24},
		{"NV_ENC_PIC_PARAMS.inputBuffer", unsafe.Offsetof(ep.InputBuffer), 40},
		{"NV_ENC_PIC_PARAMS.outputBitstream", unsafe.Offsetof(ep.OutputBitstream), 48},
		{"NV_ENC_PIC_PARAMS.bufferFmt", unsafe.Offsetof(ep.BufferFmt), 64},
		{"NV_ENC_PIC_PARAMS.pictureType", unsafe.Offsetof(ep.PictureType), 72},
		{"NV_ENC_PIC_PARAMS.codecPicParams", unsafe.Offsetof(ep.CodecPicParams), 80},
		{"NV_ENC_PIC_PARAMS.meHintCountsPerBlock", unsafe.Offsetof(ep.MEHintCountsPerBlock), 1616},
		{"NV_ENC_PIC_PARAMS.meExternalHints", unsafe.Offsetof(ep.MEExternalHints), 1648},
		{"NV_ENC_PIC_PARAMS.reserved2", unsafe.Offsetof(ep.Reserved2), 1680},
		{"NV_ENC_PIC_PARAMS.qpDeltaMap", unsafe.Offsetof(ep.QPDeltaMap), 1696},
		{"NV_ENC_PIC_PARAMS.meHintRefPicDist", unsafe.Offsetof(ep.MEHintRefPicDist), 1712},
		{"NV_ENC_PIC_PARAMS.alphaBuffer", unsafe.Offsetof(ep.AlphaBuffer), 1720},
		{"NV_ENC_PIC_PARAMS.reserved3", unsafe.Offsetof(ep.Reserved3), 1728},
		{"NV_ENC_PIC_PARAMS.reserved4", unsafe.Offsetof(ep.Reserved4), 2872},

		{"sizeof NV_ENC_LOCK_BITSTREAM", unsafe.Sizeof(lb), 1544},
		{"NV_ENC_LOCK_BITSTREAM.outputBitstream", unsafe.Offsetof(lb.OutputBitstream), 8},
		{"NV_ENC_LOCK_BITSTREAM.frameIdx", unsafe.Offsetof(lb.FrameIdx), 24},
		{"NV_ENC_LOCK_BITSTREAM.bitstreamSizeInBytes", unsafe.Offsetof(lb.BitstreamSizeInBytes), 36},
		{"NV_ENC_LOCK_BITSTREAM.outputTimeStamp", unsafe.Offsetof(lb.OutputTimeStamp), 40},
		{"NV_ENC_LOCK_BITSTREAM.bitstreamBufferPtr", unsafe.Offsetof(lb.BitstreamBufferPtr), 56},
		{"NV_ENC_LOCK_BITSTREAM.pictureType", unsafe.Offsetof(lb.PictureType), 64},
		{"NV_ENC_LOCK_BITSTREAM.temporalId", unsafe.Offsetof(lb.TemporalID), 88},
		{"NV_ENC_LOCK_BITSTREAM.intraMBCount", unsafe.Offsetof(lb.IntraMBCount), 140},
		{"NV_ENC_LOCK_BITSTREAM.alphaLayerSizeInBytes", unsafe.Offsetof(lb.AlphaLayerSizeInBytes), 156},
		{"NV_ENC_LOCK_BITSTREAM.reserved1", unsafe.Offsetof(lb.Reserved1), 160},
		{"NV_ENC_LOCK_BITSTREAM.reserved2", unsafe.Offsetof(lb.Reserved2), 1032},

		{"sizeof NV_ENC_LOCK_INPUT_BUFFER", unsafe.Sizeof(li), 1544},
		{"NV_ENC_LOCK_INPUT_BUFFER.inputBuffer", unsafe.Offsetof(li.InputBuffer), 8},
		{"NV_ENC_LOCK_INPUT_BUFFER.bufferDataPtr", unsafe.Offsetof(li.BufferDataPtr), 16},
		{"NV_ENC_LOCK_INPUT_BUFFER.pitch", unsafe.Offsetof(li.Pitch), 24},
		{"NV_ENC_LOCK_INPUT_BUFFER.reserved2", unsafe.Offsetof(li.Reserved2), 1032},

		{"sizeof NV_ENC_SEQUENCE_PARAM_PAYLOAD", unsafe.Sizeof(spp), 1544},
		{"NV_ENC_SEQUENCE_PARAM_PAYLOAD.spsppsBuffer", unsafe.Offsetof(spp.SPSPPSBuffer), 16},
		{"NV_ENC_SEQUENCE_PARAM_PAYLOAD.outSPSPPSPayloadSize", unsafe.Offsetof(spp.OutSPSPPSPayloadSize), 24},
		{"NV_ENC_SEQUENCE_PARAM_PAYLOAD.reserved2", unsafe.Offsetof(spp.Reserved2), 1032},

		{"sizeof NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS", unsafe.Sizeof(os), 1552},
		{"NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS.device", unsafe.Offsetof(os.Device), 8},
		{"NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS.apiVersion", unsafe.Offsetof(os.APIVersion), 24},
		{"NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS.reserved2", unsafe.Offsetof(os.Reserved2), 1040},

		{"sizeof NV_ENCODE_API_FUNCTION_LIST", unsafe.Sizeof(fl), 2552},
		{"FUNCTION_LIST.nvEncOpenEncodeSession", unsafe.Offsetof(fl.OpenEncodeSession), 8},
		{"FUNCTION_LIST.nvEncGetEncodeGUIDs", unsafe.Offsetof(fl.GetEncodeGUIDs), 40},
		{"FUNCTION_LIST.nvEncGetEncodeCaps", unsafe.Offsetof(fl.GetEncodeCaps), 64},
		{"FUNCTION_LIST.nvEncInitializeEncoder", unsafe.Offsetof(fl.InitializeEncoder), 96},
		{"FUNCTION_LIST.nvEncEncodePicture", unsafe.Offsetof(fl.EncodePicture), 136},
		{"FUNCTION_LIST.nvEncLockBitstream", unsafe.Offsetof(fl.LockBitstream), 144},
		{"FUNCTION_LIST.nvEncLockInputBuffer", unsafe.Offsetof(fl.LockInputBuffer), 160},
		{"FUNCTION_LIST.nvEncGetSequenceParams", unsafe.Offsetof(fl.GetSequenceParams), 184},
		{"FUNCTION_LIST.nvEncDestroyEncoder", unsafe.Offsetof(fl.DestroyEncoder), 224},
		{"FUNCTION_LIST.nvEncOpenEncodeSessionEx", unsafe.Offsetof(fl.OpenEncodeSessionEx), 240},
		{"FUNCTION_LIST.reserved1", unsafe.Offsetof(fl.Reserved1), 272},
		{"FUNCTION_LIST.nvEncGetLastErrorString", unsafe.Offsetof(fl.GetLastErrorString), 304},
		{"FUNCTION_LIST.nvEncGetEncodePresetConfigEx", unsafe.Offsetof(fl.GetEncodePresetConfigEx), 320},
		{"FUNCTION_LIST.nvEncGetSequenceParamEx", unsafe.Offsetof(fl.GetSequenceParamEx), 328},
		{"FUNCTION_LIST.reserved2", unsafe.Offsetof(fl.Reserved2), 336},
	}
	for _, c := range append(checks, ulongLayoutChecks()...) {
		if c.got != c.want {
			t.Errorf("%s: got %d want %d", c.name, c.got, c.want)
		}
	}
}

// TestVersionConstants checks the NVENCAPI_STRUCT_VERSION arithmetic against
// the values the C preprocessor produced for SDK 11.1.
func TestVersionConstants(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"NVENCAPI_VERSION", APIVersion, 16777227},
		{"NV_ENC_CAPS_PARAM_VER", CapsParamVer, 1895890955},
		{"NV_ENC_CREATE_INPUT_BUFFER_VER", CreateInputBufferVer, 1895890955},
		{"NV_ENC_CREATE_BITSTREAM_BUFFER_VER", CreateBitstreamBufferVer, 1895890955},
		{"NV_ENC_RC_PARAMS_VER", RCParamsVer, 1895890955},
		{"NV_ENC_CONFIG_VER", ConfigVer, 4043767819},
		{"NV_ENC_INITIALIZE_PARAMS_VER", InitializeParamsVer, 4043636747},
		{"NV_ENC_PRESET_CONFIG_VER", PresetConfigVer, 4043571211},
		{"NV_ENC_PIC_PARAMS_VER", PicParamsVer, 4043571211},
		{"NV_ENC_LOCK_BITSTREAM_VER", LockBitstreamVer, 1895890955},
		{"NV_ENC_LOCK_INPUT_BUFFER_VER", LockInputBufferVer, 1895890955},
		{"NV_ENC_SEQUENCE_PARAM_PAYLOAD_VER", SequenceParamPayloadVer, 1895890955},
		{"NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS_VER", OpenEncodeSessionExParamsVer, 1895890955},
		{"NV_ENCODE_API_FUNCTION_LIST_VER", FunctionListVer, 1895956491},
		{"max supported version floor", MaxSupportedVersionFloor, 177},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %d want %d", c.name, c.got, c.want)
		}
	}
	if s := StatusString(StatusErrNeedMoreInput); s != "NV_ENC_ERR_NEED_MORE_INPUT" {
		t.Errorf("StatusString(17) = %q", s)
	}
	if s := StatusString(99); s != "NVENCSTATUS(99)" {
		t.Errorf("StatusString(99) = %q", s)
	}
}
