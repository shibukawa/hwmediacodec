//go:build !windows

package sys

import "unsafe"

// ulongLayoutChecks lists the layout of the cuvid structures that contain
// unsigned long fields, as measured for LP64 targets (x86_64-linux-gnu and
// aarch64-linux-gnu).
func ulongLayoutChecks() []layoutCheck {
	var sp SourceDataPacket
	var ci DecodeCreateInfo
	return []layoutCheck{
		{"sizeof CUVIDSOURCEDATAPACKET", unsafe.Sizeof(sp), 32},
		{"CUVIDSOURCEDATAPACKET.flags", unsafe.Offsetof(sp.Flags), 0},
		{"CUVIDSOURCEDATAPACKET.payload_size", unsafe.Offsetof(sp.PayloadSize), 8},
		{"CUVIDSOURCEDATAPACKET.payload", unsafe.Offsetof(sp.Payload), 16},
		{"CUVIDSOURCEDATAPACKET.timestamp", unsafe.Offsetof(sp.Timestamp), 24},
		{"sizeof CUVIDDECODECREATEINFO", unsafe.Sizeof(ci), 176},
		{"CUVIDDECODECREATEINFO.ulWidth", unsafe.Offsetof(ci.Width), 0},
		{"CUVIDDECODECREATEINFO.ulHeight", unsafe.Offsetof(ci.Height), 8},
		{"CUVIDDECODECREATEINFO.ulNumDecodeSurfaces", unsafe.Offsetof(ci.NumDecodeSurfaces), 16},
		{"CUVIDDECODECREATEINFO.CodecType", unsafe.Offsetof(ci.CodecType), 24},
		{"CUVIDDECODECREATEINFO.ChromaFormat", unsafe.Offsetof(ci.ChromaFormat), 28},
		{"CUVIDDECODECREATEINFO.ulCreationFlags", unsafe.Offsetof(ci.CreationFlags), 32},
		{"CUVIDDECODECREATEINFO.bitDepthMinus8", unsafe.Offsetof(ci.BitDepthMinus8), 40},
		{"CUVIDDECODECREATEINFO.ulIntraDecodeOnly", unsafe.Offsetof(ci.IntraDecodeOnly), 48},
		{"CUVIDDECODECREATEINFO.ulMaxWidth", unsafe.Offsetof(ci.MaxWidth), 56},
		{"CUVIDDECODECREATEINFO.ulMaxHeight", unsafe.Offsetof(ci.MaxHeight), 64},
		{"CUVIDDECODECREATEINFO.Reserved1", unsafe.Offsetof(ci.Reserved1), 72},
		{"CUVIDDECODECREATEINFO.display_area", unsafe.Offsetof(ci.DisplayArea), 80},
		{"CUVIDDECODECREATEINFO.OutputFormat", unsafe.Offsetof(ci.OutputFormat), 88},
		{"CUVIDDECODECREATEINFO.DeinterlaceMode", unsafe.Offsetof(ci.DeinterlaceMode), 92},
		{"CUVIDDECODECREATEINFO.ulTargetWidth", unsafe.Offsetof(ci.TargetWidth), 96},
		{"CUVIDDECODECREATEINFO.ulTargetHeight", unsafe.Offsetof(ci.TargetHeight), 104},
		{"CUVIDDECODECREATEINFO.ulNumOutputSurfaces", unsafe.Offsetof(ci.NumOutputSurfaces), 112},
		{"CUVIDDECODECREATEINFO.vidLock", unsafe.Offsetof(ci.VidLock), 120},
		{"CUVIDDECODECREATEINFO.target_rect", unsafe.Offsetof(ci.TargetRect), 128},
		{"CUVIDDECODECREATEINFO.enableHistogram", unsafe.Offsetof(ci.EnableHistogram), 136},
		{"CUVIDDECODECREATEINFO.Reserved2", unsafe.Offsetof(ci.Reserved2), 144},
	}
}
