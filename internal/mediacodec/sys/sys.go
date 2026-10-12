//go:build android || mediacodec_typecheck

package sys

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Bound functions. They are valid after Load returns nil. Pointers to the
// opaque NDK objects (AMediaCodec, AMediaFormat) travel as uintptr.
var (
	AMediaCodec_createDecoderByType func(mime string) uintptr
	AMediaCodec_createEncoderByType func(mime string) uintptr
	AMediaCodec_delete              func(codec uintptr) int32
	AMediaCodec_getName             func(codec uintptr, name **byte) int32
	AMediaCodec_releaseName         func(codec uintptr, name *byte)
	AMediaCodec_configure           func(codec, format, window, crypto uintptr, flags uint32) int32
	AMediaCodec_start               func(codec uintptr) int32
	AMediaCodec_stop                func(codec uintptr) int32
	AMediaCodec_flush               func(codec uintptr) int32
	AMediaCodec_dequeueInputBuffer  func(codec uintptr, timeoutUs int64) int
	AMediaCodec_getInputBuffer      func(codec uintptr, idx uintptr, size *uintptr) *byte
	AMediaCodec_queueInputBuffer    func(codec uintptr, idx uintptr, offset int64, size uintptr, timeUs uint64, flags uint32) int32
	AMediaCodec_dequeueOutputBuffer func(codec uintptr, info *BufferInfo, timeoutUs int64) int
	AMediaCodec_getOutputBuffer     func(codec uintptr, idx uintptr, size *uintptr) *byte
	AMediaCodec_releaseOutputBuffer func(codec uintptr, idx uintptr, render bool) int32
	AMediaCodec_getOutputFormat     func(codec uintptr) uintptr
	AMediaCodec_getInputFormat      func(codec uintptr) uintptr
	AMediaCodec_setParameters       func(codec, params uintptr) int32

	AMediaFormat_new       func() uintptr
	AMediaFormat_delete    func(format uintptr) int32
	AMediaFormat_toString  func(format uintptr) *byte
	AMediaFormat_getInt32  func(format uintptr, name string, out *int32) bool
	AMediaFormat_getInt64  func(format uintptr, name string, out *int64) bool
	AMediaFormat_getFloat  func(format uintptr, name string, out *float32) bool
	AMediaFormat_getBuffer func(format uintptr, name string, data *unsafe.Pointer, size *uintptr) bool
	AMediaFormat_getString func(format uintptr, name string, out **byte) bool
	AMediaFormat_getRect   func(format uintptr, name string, left, top, right, bottom *int32) bool
	AMediaFormat_setInt32  func(format uintptr, name string, value int32)
	AMediaFormat_setInt64  func(format uintptr, name string, value int64)
	AMediaFormat_setFloat  func(format uintptr, name string, value float32)
	AMediaFormat_setString func(format uintptr, name string, value string)
	AMediaFormat_setBuffer func(format uintptr, name string, data *byte, size uintptr)
)

var (
	loadOnce sync.Once
	loadErr  error
)

// Load opens libmediandk.so and binds every symbol. It is safe to call
// repeatedly; the result is cached. The newest symbols used
// (AMediaCodec_getName, AMediaCodec_getInputFormat, AMediaFormat_getRect)
// exist from API 28 (Android 9); the backend documents Android 14 as its
// minimum, and on older systems Load fails and the backend is absent.
func Load() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

type binder struct {
	handle uintptr
	errs   []string
}

func (b *binder) fn(fptr any, name string) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		b.errs = append(b.errs, name)
		return
	}
	purego.RegisterFunc(fptr, addr)
}

func load() error {
	h, err := purego.Dlopen("libmediandk.so", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("mediacodec: dlopen libmediandk.so: %w", err)
	}
	b := &binder{handle: h}
	b.fn(&AMediaCodec_createDecoderByType, "AMediaCodec_createDecoderByType")
	b.fn(&AMediaCodec_createEncoderByType, "AMediaCodec_createEncoderByType")
	b.fn(&AMediaCodec_delete, "AMediaCodec_delete")
	b.fn(&AMediaCodec_getName, "AMediaCodec_getName")
	b.fn(&AMediaCodec_releaseName, "AMediaCodec_releaseName")
	b.fn(&AMediaCodec_configure, "AMediaCodec_configure")
	b.fn(&AMediaCodec_start, "AMediaCodec_start")
	b.fn(&AMediaCodec_stop, "AMediaCodec_stop")
	b.fn(&AMediaCodec_flush, "AMediaCodec_flush")
	b.fn(&AMediaCodec_dequeueInputBuffer, "AMediaCodec_dequeueInputBuffer")
	b.fn(&AMediaCodec_getInputBuffer, "AMediaCodec_getInputBuffer")
	b.fn(&AMediaCodec_queueInputBuffer, "AMediaCodec_queueInputBuffer")
	b.fn(&AMediaCodec_dequeueOutputBuffer, "AMediaCodec_dequeueOutputBuffer")
	b.fn(&AMediaCodec_getOutputBuffer, "AMediaCodec_getOutputBuffer")
	b.fn(&AMediaCodec_releaseOutputBuffer, "AMediaCodec_releaseOutputBuffer")
	b.fn(&AMediaCodec_getOutputFormat, "AMediaCodec_getOutputFormat")
	b.fn(&AMediaCodec_getInputFormat, "AMediaCodec_getInputFormat")
	b.fn(&AMediaCodec_setParameters, "AMediaCodec_setParameters")

	b.fn(&AMediaFormat_new, "AMediaFormat_new")
	b.fn(&AMediaFormat_delete, "AMediaFormat_delete")
	b.fn(&AMediaFormat_toString, "AMediaFormat_toString")
	b.fn(&AMediaFormat_getInt32, "AMediaFormat_getInt32")
	b.fn(&AMediaFormat_getInt64, "AMediaFormat_getInt64")
	b.fn(&AMediaFormat_getFloat, "AMediaFormat_getFloat")
	b.fn(&AMediaFormat_getBuffer, "AMediaFormat_getBuffer")
	b.fn(&AMediaFormat_getString, "AMediaFormat_getString")
	b.fn(&AMediaFormat_getRect, "AMediaFormat_getRect")
	b.fn(&AMediaFormat_setInt32, "AMediaFormat_setInt32")
	b.fn(&AMediaFormat_setInt64, "AMediaFormat_setInt64")
	b.fn(&AMediaFormat_setFloat, "AMediaFormat_setFloat")
	b.fn(&AMediaFormat_setString, "AMediaFormat_setString")
	b.fn(&AMediaFormat_setBuffer, "AMediaFormat_setBuffer")
	if len(b.errs) > 0 {
		return errors.New("mediacodec: libmediandk.so lacks " + strings.Join(b.errs, ", ") + " (Android 9 / API 28 or later is required)")
	}
	return nil
}

// GoString copies a NUL-terminated C string.
func GoString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

// CodecName returns the name of the codec (for example "c2.qti.avc.decoder").
func CodecName(codec uintptr) string {
	var p *byte
	if AMediaCodec_getName(codec, &p) != OK || p == nil {
		return ""
	}
	s := GoString(p)
	AMediaCodec_releaseName(codec, p)
	return s
}

// FormatString returns the AMediaFormat_toString of a format, for messages.
func FormatString(format uintptr) string {
	if format == 0 {
		return ""
	}
	return GoString(AMediaFormat_toString(format))
}

// FormatBuffer copies a buffer entry of a format, or returns nil.
func FormatBuffer(format uintptr, name string) []byte {
	var data unsafe.Pointer
	var size uintptr
	if !AMediaFormat_getBuffer(format, name, &data, &size) || data == nil || size == 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice((*byte)(data), size)...)
}

// FormatInt32 returns an int32 entry of a format and whether it exists.
func FormatInt32(format uintptr, name string) (int32, bool) {
	var v int32
	ok := AMediaFormat_getInt32(format, name, &v)
	return v, ok
}

// SetBuffer stores a copy of b under name.
func SetBuffer(format uintptr, name string, b []byte) {
	if len(b) == 0 {
		return
	}
	AMediaFormat_setBuffer(format, name, &b[0], uintptr(len(b)))
}

// StatusString names a media_status_t value.
func StatusString(st int32) string {
	switch st {
	case OK:
		return "ok"
	case ErrorUnknown:
		return "AMEDIA_ERROR_UNKNOWN"
	case ErrorMalformed:
		return "AMEDIA_ERROR_MALFORMED"
	case ErrorUnsupported:
		return "AMEDIA_ERROR_UNSUPPORTED"
	case ErrorInvalidObject:
		return "AMEDIA_ERROR_INVALID_OBJECT"
	case ErrorInvalidParameter:
		return "AMEDIA_ERROR_INVALID_PARAMETER"
	case ErrorInvalidOperation:
		return "AMEDIA_ERROR_INVALID_OPERATION"
	case ErrorEndOfStream:
		return "AMEDIA_ERROR_END_OF_STREAM"
	case ErrorIO:
		return "AMEDIA_ERROR_IO"
	case ErrorWouldBlock:
		return "AMEDIA_ERROR_WOULD_BLOCK"
	}
	return fmt.Sprintf("media_status_t %d", st)
}
