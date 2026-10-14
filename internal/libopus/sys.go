//go:build linux && !android

package libopus

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Bound functions of libopus (opus.h). They are valid after load returns
// nil. Encoder and decoder states travel as uintptr.
var (
	decoderCreate  func(sampleRate, channels int32, err *int32) uintptr
	decoderDestroy func(st uintptr)
	decode         func(st uintptr, data *byte, size int32, pcm *int16, frameSize, fec int32) int32
	decodeFloat    func(st uintptr, data *byte, size int32, pcm *float32, frameSize, fec int32) int32

	encoderCreate  func(sampleRate, channels, application int32, err *int32) uintptr
	encoderDestroy func(st uintptr)
	encode         func(st uintptr, pcm *int16, frameSize int32, data *byte, maxBytes int32) int32
	encodeFloat    func(st uintptr, pcm *float32, frameSize int32, data *byte, maxBytes int32) int32
	// encoderCtl is opus_encoder_ctl, which is variadic in C. Every
	// request used here takes one integer or pointer argument, which the
	// calling conventions of linux/amd64 and linux/arm64 pass like a
	// fixed one.
	encoderCtl func(st uintptr, request int32, arg uintptr) int32
	strerror   func(code int32) *byte
)

// opus_defines.h.
const (
	applicationAudio = 2049

	ctlSetBitrate   = 4002
	ctlSetVBR       = 4006
	ctlGetLookahead = 4027

	errBadArg        = -1
	errInvalidPacket = -4
)

var (
	loadOnce sync.Once
	loadErr  error
)

// load opens libopus and binds its symbols. The result is cached.
func load() error {
	loadOnce.Do(func() { loadErr = open() })
	return loadErr
}

func open() error {
	var h uintptr
	var err error
	for _, name := range []string{"libopus.so.0", "libopus.so"} {
		if h, err = purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("libopus is not installed: %w", err)
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
	bind(&decoderCreate, "opus_decoder_create")
	bind(&decoderDestroy, "opus_decoder_destroy")
	bind(&decode, "opus_decode")
	bind(&decodeFloat, "opus_decode_float")
	bind(&encoderCreate, "opus_encoder_create")
	bind(&encoderDestroy, "opus_encoder_destroy")
	bind(&encode, "opus_encode")
	bind(&encodeFloat, "opus_encode_float")
	bind(&encoderCtl, "opus_encoder_ctl")
	bind(&strerror, "opus_strerror")
	if len(missing) > 0 {
		return errors.New("libopus lacks " + strings.Join(missing, ", "))
	}
	return nil
}

// errorText describes a negative libopus result.
func errorText(code int32) string {
	if p := strerror(code); p != nil {
		n := 0
		for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 && n < 256 {
			n++
		}
		return string(unsafe.Slice(p, n))
	}
	return fmt.Sprintf("error %d", code)
}
