//go:build linux && !android

package libavcodec

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Bound functions of libavcodec and of the libavutil it depends on. They
// are valid after load returns nil. Objects travel as unsafe.Pointer: they
// are C memory, which the garbage collector leaves alone.
var (
	avcodecVersion      func() uint32
	findDecoder         func(id int32) unsafe.Pointer
	allocContext        func(codec unsafe.Pointer) unsafe.Pointer
	freeContext         func(ctx *unsafe.Pointer)
	parametersAlloc     func() unsafe.Pointer
	parametersFree      func(par *unsafe.Pointer)
	parametersToContext func(ctx, par unsafe.Pointer) int32
	open2               func(ctx, codec unsafe.Pointer, options *unsafe.Pointer) int32
	packetAlloc         func() unsafe.Pointer
	packetFree          func(pkt *unsafe.Pointer)
	sendPacket          func(ctx, pkt unsafe.Pointer) int32
	receiveFrame        func(ctx, frame unsafe.Pointer) int32
	frameAlloc          func() unsafe.Pointer
	frameFree           func(frame *unsafe.Pointer)
	frameUnref          func(frame unsafe.Pointer)
	mallocz             func(size uintptr) unsafe.Pointer
	optGetInt           func(obj unsafe.Pointer, name *byte, searchFlags int32, out *int64) int32
	getBytesPerSample   func(format int32) int32
	sampleFmtIsPlanar   func(format int32) int32
	getPackedSampleFmt  func(format int32) int32
)

const (
	inputBufferPadding = 64  // AV_INPUT_BUFFER_PADDING_SIZE
	errAgain           = -11 // AVERROR(EAGAIN) on Linux
	mediaTypeAudio     = 1   // AVMEDIA_TYPE_AUDIO
	codecIDAAC         = 0x15002

	// AVSampleFormat, the packed ones.
	sampleFmtS16 = 1
	sampleFmtS32 = 2
	sampleFmtFLT = 3

	// The major versions load accepts: FFmpeg 4 to 8.
	firstMajor, lastMajor = 58, 62
)

// The heads of three structures are written and read directly. They have
// kept their layout from libavcodec 58 (FFmpeg 4) to 62 (FFmpeg 8), the
// versions load accepts; what follows the fields named here has not and is
// left alone.

// parameters is the head of AVCodecParameters.
type parameters struct {
	CodecType     int32
	CodecID       int32
	CodecTag      uint32
	_             uint32
	Extradata     unsafe.Pointer
	ExtradataSize int32
}

// packet is the head of AVPacket.
type packet struct {
	Buf  unsafe.Pointer
	PTS  int64
	DTS  int64
	Data unsafe.Pointer
	Size int32
}

// frame is the head of AVFrame.
type frame struct {
	Data         [8]unsafe.Pointer
	Linesize     [8]int32
	ExtendedData *unsafe.Pointer
	Width        int32
	Height       int32
	NbSamples    int32
	Format       int32
}

var (
	loadOnce sync.Once
	loadErr  error
)

// load opens libavcodec and binds its symbols. The result is cached.
func load() error {
	loadOnce.Do(func() { loadErr = open() })
	return loadErr
}

func open() error {
	var h uintptr
	var err error
	for v := lastMajor; v >= firstMajor && h == 0; v-- {
		h, err = purego.Dlopen(fmt.Sprintf("libavcodec.so.%d", v), purego.RTLD_NOW|purego.RTLD_LOCAL)
	}
	if err != nil {
		return fmt.Errorf("libavcodec (FFmpeg %d to %d) is not installed: %w", firstMajor-54, lastMajor-54, err)
	}
	var missing []string
	// A lookup through the handle searches the libraries libavcodec
	// depends on as well, which is where the libavutil functions are.
	bind := func(fptr any, name string) {
		addr, err := purego.Dlsym(h, name)
		if err != nil {
			missing = append(missing, name)
			return
		}
		purego.RegisterFunc(fptr, addr)
	}
	bind(&avcodecVersion, "avcodec_version")
	bind(&findDecoder, "avcodec_find_decoder")
	bind(&allocContext, "avcodec_alloc_context3")
	bind(&freeContext, "avcodec_free_context")
	bind(&parametersAlloc, "avcodec_parameters_alloc")
	bind(&parametersFree, "avcodec_parameters_free")
	bind(&parametersToContext, "avcodec_parameters_to_context")
	bind(&open2, "avcodec_open2")
	bind(&packetAlloc, "av_packet_alloc")
	bind(&packetFree, "av_packet_free")
	bind(&sendPacket, "avcodec_send_packet")
	bind(&receiveFrame, "avcodec_receive_frame")
	bind(&frameAlloc, "av_frame_alloc")
	bind(&frameFree, "av_frame_free")
	bind(&frameUnref, "av_frame_unref")
	bind(&mallocz, "av_mallocz")
	bind(&optGetInt, "av_opt_get_int")
	bind(&getBytesPerSample, "av_get_bytes_per_sample")
	bind(&sampleFmtIsPlanar, "av_sample_fmt_is_planar")
	bind(&getPackedSampleFmt, "av_get_packed_sample_fmt")
	if len(missing) > 0 {
		return errors.New("libavcodec lacks " + strings.Join(missing, ", "))
	}
	major := avcodecVersion() >> 16
	if major < firstMajor || major > lastMajor {
		return fmt.Errorf("libavcodec %d is not a version this library knows the structures of (%d to %d)", major, firstMajor, lastMajor)
	}
	return nil
}
