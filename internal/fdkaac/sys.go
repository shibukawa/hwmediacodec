//go:build linux && !android

package fdkaac

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Bound functions of libfdk-aac (aacdecoder_lib.h, aacenc_lib.h). They
// are valid after load returns nil. Handles travel as uintptr.
var (
	decOpen      func(transport, layers uint32) uintptr
	decClose     func(h uintptr)
	decConfigRaw func(h uintptr, conf *unsafe.Pointer, length *uint32) int32
	decSetParam  func(h uintptr, param, value int32) int32
	decFill      func(h uintptr, buffer *unsafe.Pointer, size *uint32, valid *uint32) int32
	decFrame     func(h uintptr, pcm *int16, size int32, flags uint32) int32
	decInfo      func(h uintptr) *streamInfo
	decLibInfo   func(info *libInfo) int32

	encOpen     func(h *uintptr, modules, maxChannels uint32) int32
	encClose    func(h *uintptr) int32
	encSetParam func(h uintptr, param, value uint32) int32
	encEncode   func(h uintptr, in, out *bufDesc, inArgs *encInArgs, outArgs *encOutArgs) int32
)

// streamInfo is the head of CStreamInfo, which is the same in every
// release of the library; what follows it is not read.
type streamInfo struct {
	SampleRate  int32
	FrameSize   int32
	NumChannels int32
}

// libInfo mirrors LIB_INFO, the description of one module of the
// library. aacDecoder_GetLibInfo fills in an array of them, one for each
// module the decoder was built with.
type libInfo struct {
	Title, BuildDate, BuildTime *byte
	ModuleID                    int32
	Version                     int32
	Flags                       uint32
	VersionStr                  [32]byte
	_                           [4]byte
}

// libInfoModules is more than the library has module identifiers
// (FDK_MODULE_LAST is 39 in its second version), so that it finds a free
// entry for each module it reports.
const libInfoModules = 64

const moduleSBRDecoder = 5 // FDK_SBRDEC

var (
	sbrOnce sync.Once
	sbr     bool
)

// hasSBR reports whether the library decodes spectral band replication.
// The builds some distributions ship (fdk-aac-free) leave it out, along
// with everything else beyond AAC-LC, and decode the core of an HE-AAC
// stream alone.
func hasSBR() bool {
	sbrOnce.Do(func() {
		var modules [libInfoModules]libInfo
		decLibInfo(&modules[0])
		for i := range modules {
			if modules[i].ModuleID == moduleSBRDecoder {
				sbr = true
			}
		}
	})
	return sbr
}

// bufDesc mirrors AACENC_BufDesc.
type bufDesc struct {
	NumBufs     int32
	_           int32
	Bufs        *unsafe.Pointer
	Identifiers *int32
	Sizes       *int32
	ElSizes     *int32
}

// encInArgs and encOutArgs mirror AACENC_InArgs and AACENC_OutArgs.
type encInArgs struct {
	NumInSamples int32
	NumAncBytes  int32
}

type encOutArgs struct {
	NumOutBytes  int32
	NumInSamples int32
	NumAncBytes  int32
	BitResState  int32
}

const (
	transportRaw = 0 // TT_MP4_RAW

	// AACDEC_PARAM.
	decParamChannelMapping = 0x0003 // AAC_PCM_OUTPUT_CHANNEL_MAPPING: 1 is the order of a WAV file
	decParamLimiterEnable  = 0x0004 // AAC_PCM_LIMITER_ENABLE
	decParamConcealMethod  = 0x0100 // AAC_CONCEAL_METHOD: 1 is noise substitution

	decFlagFlush = 2 // AACDEC_FLUSH: return what the decoder holds back

	decOK            = 0
	decNotEnoughBits = 0x1002

	// AACENC_PARAM.
	encParamAOT          = 0x0100
	encParamBitrate      = 0x0101
	encParamBitrateMode  = 0x0102
	encParamSampleRate   = 0x0103
	encParamChannelMode  = 0x0106
	encParamChannelOrder = 0x0107
	encParamAfterburner  = 0x0200
	encParamTransmux     = 0x0300

	encOK = 0

	// Buffer identifiers of AACENC_BufDesc.
	bufInAudio      = 0
	bufOutBitstream = 3
)

var (
	loadOnce sync.Once
	loadErr  error
)

// load opens libfdk-aac and binds its symbols. The result is cached.
func load() error {
	loadOnce.Do(func() { loadErr = open() })
	return loadErr
}

func open() error {
	var h uintptr
	var err error
	for _, name := range []string{"libfdk-aac.so.2", "libfdk-aac.so.1", "libfdk-aac.so"} {
		if h, err = purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("libfdk-aac is not installed: %w", err)
	}
	var missing []string
	bind := func(fptr any, name string) {
		addr, err := purego.Dlsym(h, name)
		if err != nil {
			missing = append(missing, name)
			return
		}
		purego.RegisterFunc(fptr, addr)
	}
	bind(&decOpen, "aacDecoder_Open")
	bind(&decClose, "aacDecoder_Close")
	bind(&decConfigRaw, "aacDecoder_ConfigRaw")
	bind(&decSetParam, "aacDecoder_SetParam")
	bind(&decFill, "aacDecoder_Fill")
	bind(&decFrame, "aacDecoder_DecodeFrame")
	bind(&decInfo, "aacDecoder_GetStreamInfo")
	bind(&decLibInfo, "aacDecoder_GetLibInfo")
	bind(&encOpen, "aacEncOpen")
	bind(&encClose, "aacEncClose")
	bind(&encSetParam, "aacEncoder_SetParam")
	bind(&encEncode, "aacEncEncode")
	if len(missing) > 0 {
		return errors.New("libfdk-aac lacks " + strings.Join(missing, ", "))
	}
	return nil
}
