package sys

import "unsafe"

// ulongLayoutChecks lists the layout of the cuvid structures that contain
// unsigned long fields, as measured for Windows x64 (LLP64) with
// x86_64-w64-mingw32-gcc and with clang --target=x86_64-pc-windows-msvc;
// both agree on every value.
func ulongLayoutChecks() []layoutCheck {
	var sp SourceDataPacket
	var ci DecodeCreateInfo
	return []layoutCheck{
		{"sizeof CUVIDSOURCEDATAPACKET", unsafe.Sizeof(sp), 24},
		{"CUVIDSOURCEDATAPACKET.flags", unsafe.Offsetof(sp.Flags), 0},
		{"CUVIDSOURCEDATAPACKET.payload_size", unsafe.Offsetof(sp.PayloadSize), 4},
		{"CUVIDSOURCEDATAPACKET.payload", unsafe.Offsetof(sp.Payload), 8},
		{"CUVIDSOURCEDATAPACKET.timestamp", unsafe.Offsetof(sp.Timestamp), 16},
		{"sizeof CUVIDDECODECREATEINFO", unsafe.Sizeof(ci), 112},
		{"CUVIDDECODECREATEINFO.ulWidth", unsafe.Offsetof(ci.Width), 0},
		{"CUVIDDECODECREATEINFO.ulHeight", unsafe.Offsetof(ci.Height), 4},
		{"CUVIDDECODECREATEINFO.ulNumDecodeSurfaces", unsafe.Offsetof(ci.NumDecodeSurfaces), 8},
		{"CUVIDDECODECREATEINFO.CodecType", unsafe.Offsetof(ci.CodecType), 12},
		{"CUVIDDECODECREATEINFO.ChromaFormat", unsafe.Offsetof(ci.ChromaFormat), 16},
		{"CUVIDDECODECREATEINFO.ulCreationFlags", unsafe.Offsetof(ci.CreationFlags), 20},
		{"CUVIDDECODECREATEINFO.bitDepthMinus8", unsafe.Offsetof(ci.BitDepthMinus8), 24},
		{"CUVIDDECODECREATEINFO.ulIntraDecodeOnly", unsafe.Offsetof(ci.IntraDecodeOnly), 28},
		{"CUVIDDECODECREATEINFO.ulMaxWidth", unsafe.Offsetof(ci.MaxWidth), 32},
		{"CUVIDDECODECREATEINFO.ulMaxHeight", unsafe.Offsetof(ci.MaxHeight), 36},
		{"CUVIDDECODECREATEINFO.Reserved1", unsafe.Offsetof(ci.Reserved1), 40},
		{"CUVIDDECODECREATEINFO.display_area", unsafe.Offsetof(ci.DisplayArea), 44},
		{"CUVIDDECODECREATEINFO.OutputFormat", unsafe.Offsetof(ci.OutputFormat), 52},
		{"CUVIDDECODECREATEINFO.DeinterlaceMode", unsafe.Offsetof(ci.DeinterlaceMode), 56},
		{"CUVIDDECODECREATEINFO.ulTargetWidth", unsafe.Offsetof(ci.TargetWidth), 60},
		{"CUVIDDECODECREATEINFO.ulTargetHeight", unsafe.Offsetof(ci.TargetHeight), 64},
		{"CUVIDDECODECREATEINFO.ulNumOutputSurfaces", unsafe.Offsetof(ci.NumOutputSurfaces), 68},
		{"CUVIDDECODECREATEINFO.vidLock", unsafe.Offsetof(ci.VidLock), 72},
		{"CUVIDDECODECREATEINFO.target_rect", unsafe.Offsetof(ci.TargetRect), 80},
		{"CUVIDDECODECREATEINFO.enableHistogram", unsafe.Offsetof(ci.EnableHistogram), 88},
		{"CUVIDDECODECREATEINFO.Reserved2", unsafe.Offsetof(ci.Reserved2), 92},
	}
}
