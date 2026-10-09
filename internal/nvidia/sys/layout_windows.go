//go:build amd64

package sys

import "unsafe"

// Compile-time layout checks for the two cuvid structures whose unsigned
// long fields are 32 bits wide on Windows. Each pair of array lengths is
// only valid when the Go value equals the size or offset measured with
// x86_64-w64-mingw32-gcc and clang --target=x86_64-pc-windows-msvc against
// nv-codec-headers n11.1.5.3, so a cross-build from any host verifies them.
// Every other structure has the same layout as on Linux and is covered by
// layout_test.go.
var (
	_ [24 - unsafe.Sizeof(SourceDataPacket{})]byte
	_ [unsafe.Sizeof(SourceDataPacket{}) - 24]byte
	_ [0 - unsafe.Offsetof(SourceDataPacket{}.Flags)]byte
	_ [unsafe.Offsetof(SourceDataPacket{}.Flags) - 0]byte
	_ [4 - unsafe.Offsetof(SourceDataPacket{}.PayloadSize)]byte
	_ [unsafe.Offsetof(SourceDataPacket{}.PayloadSize) - 4]byte
	_ [8 - unsafe.Offsetof(SourceDataPacket{}.Payload)]byte
	_ [unsafe.Offsetof(SourceDataPacket{}.Payload) - 8]byte
	_ [16 - unsafe.Offsetof(SourceDataPacket{}.Timestamp)]byte
	_ [unsafe.Offsetof(SourceDataPacket{}.Timestamp) - 16]byte
	_ [112 - unsafe.Sizeof(DecodeCreateInfo{})]byte
	_ [unsafe.Sizeof(DecodeCreateInfo{}) - 112]byte
	_ [0 - unsafe.Offsetof(DecodeCreateInfo{}.Width)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.Width) - 0]byte
	_ [4 - unsafe.Offsetof(DecodeCreateInfo{}.Height)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.Height) - 4]byte
	_ [8 - unsafe.Offsetof(DecodeCreateInfo{}.NumDecodeSurfaces)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.NumDecodeSurfaces) - 8]byte
	_ [12 - unsafe.Offsetof(DecodeCreateInfo{}.CodecType)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.CodecType) - 12]byte
	_ [16 - unsafe.Offsetof(DecodeCreateInfo{}.ChromaFormat)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.ChromaFormat) - 16]byte
	_ [20 - unsafe.Offsetof(DecodeCreateInfo{}.CreationFlags)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.CreationFlags) - 20]byte
	_ [24 - unsafe.Offsetof(DecodeCreateInfo{}.BitDepthMinus8)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.BitDepthMinus8) - 24]byte
	_ [28 - unsafe.Offsetof(DecodeCreateInfo{}.IntraDecodeOnly)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.IntraDecodeOnly) - 28]byte
	_ [32 - unsafe.Offsetof(DecodeCreateInfo{}.MaxWidth)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.MaxWidth) - 32]byte
	_ [36 - unsafe.Offsetof(DecodeCreateInfo{}.MaxHeight)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.MaxHeight) - 36]byte
	_ [40 - unsafe.Offsetof(DecodeCreateInfo{}.Reserved1)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.Reserved1) - 40]byte
	_ [44 - unsafe.Offsetof(DecodeCreateInfo{}.DisplayArea)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.DisplayArea) - 44]byte
	_ [52 - unsafe.Offsetof(DecodeCreateInfo{}.OutputFormat)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.OutputFormat) - 52]byte
	_ [56 - unsafe.Offsetof(DecodeCreateInfo{}.DeinterlaceMode)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.DeinterlaceMode) - 56]byte
	_ [60 - unsafe.Offsetof(DecodeCreateInfo{}.TargetWidth)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.TargetWidth) - 60]byte
	_ [64 - unsafe.Offsetof(DecodeCreateInfo{}.TargetHeight)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.TargetHeight) - 64]byte
	_ [68 - unsafe.Offsetof(DecodeCreateInfo{}.NumOutputSurfaces)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.NumOutputSurfaces) - 68]byte
	_ [72 - unsafe.Offsetof(DecodeCreateInfo{}.VidLock)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.VidLock) - 72]byte
	_ [80 - unsafe.Offsetof(DecodeCreateInfo{}.TargetRect)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.TargetRect) - 80]byte
	_ [88 - unsafe.Offsetof(DecodeCreateInfo{}.EnableHistogram)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.EnableHistogram) - 88]byte
	_ [92 - unsafe.Offsetof(DecodeCreateInfo{}.Reserved2)]byte
	_ [unsafe.Offsetof(DecodeCreateInfo{}.Reserved2) - 92]byte
)
