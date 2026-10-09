//go:build linux || (windows && amd64)

package sys

import "sync"

// Bound NVDEC entry points (libnvcuvid.so.1 on Linux, nvcuvid.dll on
// Windows). They are valid after LoadCuvid returns nil.
var (
	CuvidGetDecoderCaps     func(caps *DecodeCaps) int32
	CuvidCreateDecoder      func(decoder *uintptr, info *DecodeCreateInfo) int32
	CuvidDestroyDecoder     func(decoder uintptr) int32
	CuvidDecodePicture      func(decoder uintptr, pic *DecodePicParams) int32
	CuvidGetDecodeStatus    func(decoder uintptr, picIdx int32, status *GetDecodeStatus) int32
	CuvidMapVideoFrame      func(decoder uintptr, picIdx int32, devPtr *uint64, pitch *uint32, params *ProcParams) int32
	CuvidUnmapVideoFrame    func(decoder uintptr, devPtr uint64) int32
	CuvidCreateVideoParser  func(parser *uintptr, params *ParserParams) int32
	CuvidParseVideoData     func(parser uintptr, packet *SourceDataPacket) int32
	CuvidDestroyVideoParser func(parser uintptr) int32
)

var (
	cuvidOnce sync.Once
	cuvidErr  error
)

// LoadCuvid opens the NVDEC library (and the CUDA driver) and binds the
// decoder and parser entry points. The result is cached.
func LoadCuvid() error {
	cuvidOnce.Do(func() { cuvidErr = loadCuvid() })
	return cuvidErr
}

func loadCuvid() error {
	if err := LoadCUDA(); err != nil {
		return err
	}
	b, err := open(cuvidLibraries)
	if err != nil {
		return err
	}
	b.fn(&CuvidGetDecoderCaps, "cuvidGetDecoderCaps")
	b.fn(&CuvidCreateDecoder, "cuvidCreateDecoder")
	b.fn(&CuvidDestroyDecoder, "cuvidDestroyDecoder")
	b.fn(&CuvidDecodePicture, "cuvidDecodePicture")
	b.fn(&CuvidGetDecodeStatus, "cuvidGetDecodeStatus")
	b.fn(&CuvidMapVideoFrame, "cuvidMapVideoFrame64")
	b.fn(&CuvidUnmapVideoFrame, "cuvidUnmapVideoFrame64")
	b.fn(&CuvidCreateVideoParser, "cuvidCreateVideoParser")
	b.fn(&CuvidParseVideoData, "cuvidParseVideoData")
	b.fn(&CuvidDestroyVideoParser, "cuvidDestroyVideoParser")
	return b.missing()
}
