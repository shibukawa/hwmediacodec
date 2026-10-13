//go:build darwin

// Package sys binds the AudioToolbox entry points used by the AudioToolbox
// backend (AudioConverter and AudioFormat) through purego, so that no cgo is
// required.
package sys

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	rtldLazy   = 0x1
	rtldGlobal = 0x8
)

// Audio format identifiers (four-character codes).
const (
	FormatLinearPCM uint32 = 0x6C70636D // 'lpcm'
	FormatMPEG4AAC  uint32 = 0x61616320 // 'aac '
	FormatOpus      uint32 = 0x6F707573 // 'opus'
	// FormatMPEG4AACHE and FormatMPEG4AACHEV2 are AAC with spectral band
	// replication and with parametric stereo on top. Asked for plain AAC,
	// a decoder returns the AAC-LC core of such a stream only.
	FormatMPEG4AACHE   uint32 = 0x61616368 // 'aach'
	FormatMPEG4AACHEV2 uint32 = 0x61616370 // 'aacp'
)

// Linear PCM format flags.
const (
	FlagIsFloat         uint32 = 1 << 0
	FlagIsSignedInteger uint32 = 1 << 2
	FlagIsPacked        uint32 = 1 << 3
)

// AudioConverter property identifiers.
const (
	PropMaximumOutputPacketSize  uint32 = 0x786F7073 // 'xops'
	PropDecompressionMagicCookie uint32 = 0x646D6763 // 'dmgc'
	PropCompressionMagicCookie   uint32 = 0x636D6763 // 'cmgc'
	PropEncodeBitRate            uint32 = 0x62726174 // 'brat'
	PropPrimeInfo                uint32 = 0x7072696D // 'prim'
	PropCurrentOutputStreamDesc  uint32 = 0x61636F64 // 'acod'
	PropBitRateControlMode       uint32 = 0x61636266 // 'acbf'
	PropApplicableEncodeBitRates uint32 = 0x61656272 // 'aebr'
	PropOutputChannelLayout      uint32 = 0x6F636C20 // 'ocl '
)

// BitRateControlModeConstant is the constant bitrate value of
// PropBitRateControlMode. The encoders start in a mode of their own choice
// (long-term average for AAC, variable for Opus).
const BitRateControlModeConstant uint32 = 0

// AudioFormat property identifiers.
const (
	FormatPropFormatInfo      uint32 = 0x666D7469 // 'fmti'
	FormatPropEncodeFormatIDs uint32 = 0x61636F66 // 'acof'
	FormatPropDecodeFormatIDs uint32 = 0x61636966 // 'acif'
)

// Channel layout tags (kAudioChannelLayoutTag_*): the layout number in the
// high 16 bits, the channel count in the low 16.
const (
	LayoutMPEG_3_0A uint32 = 113<<16 | 3 // L R C
	LayoutMPEG_4_0A uint32 = 115<<16 | 4 // L R C Cs
	LayoutMPEG_5_0A uint32 = 117<<16 | 5 // L R C Ls Rs
	LayoutMPEG_5_1A uint32 = 121<<16 | 6 // L R C LFE Ls Rs
	LayoutMPEG_7_1C uint32 = 128<<16 | 8 // L R C LFE Ls Rs Rls Rrs
)

// AudioStreamBasicDescription mirrors the CoreAudio struct (40 bytes).
type AudioStreamBasicDescription struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

// AudioBuffer mirrors the CoreAudio struct (16 bytes).
type AudioBuffer struct {
	NumberChannels uint32
	DataByteSize   uint32
	Data           unsafe.Pointer
}

// AudioBufferList mirrors the CoreAudio struct with one buffer, which is
// all interleaved PCM and compressed packets need (24 bytes).
type AudioBufferList struct {
	NumberBuffers uint32
	_             uint32
	Buffer        AudioBuffer
}

// AudioStreamPacketDescription mirrors the CoreAudio struct (16 bytes).
type AudioStreamPacketDescription struct {
	StartOffset            int64
	VariableFramesInPacket uint32
	DataByteSize           uint32
}

// AudioConverterPrimeInfo mirrors the AudioToolbox struct.
type AudioConverterPrimeInfo struct {
	LeadingFrames  uint32
	TrailingFrames uint32
}

// AudioChannelLayout mirrors the CoreAudio struct without channel
// descriptions, which is enough to name a layout by its tag (12 bytes).
type AudioChannelLayout struct {
	Tag                  uint32
	Bitmap               uint32
	NumberOfDescriptions uint32
}

// Bound functions. They are valid after Load returns nil.
var (
	AudioConverterNew     func(source, destination *AudioStreamBasicDescription, out *uintptr) int32
	AudioConverterDispose func(converter uintptr) int32
	AudioConverterReset   func(converter uintptr) int32
	// AudioConverterFillComplexBuffer pulls input through inputProc, a
	// callback made with purego.NewCallback, on the calling thread.
	AudioConverterFillComplexBuffer func(converter uintptr, inputProc uintptr, userData uintptr, ioOutputPackets *uint32, out *AudioBufferList, packetDescriptions *AudioStreamPacketDescription) int32
	AudioConverterSetProperty       func(converter uintptr, id uint32, size uint32, data unsafe.Pointer) int32
	AudioConverterGetProperty       func(converter uintptr, id uint32, ioSize *uint32, data unsafe.Pointer) int32
	AudioConverterGetPropertyInfo   func(converter uintptr, id uint32, outSize *uint32, outWritable *uint8) int32
	AudioFormatGetProperty          func(id uint32, specifierSize uint32, specifier unsafe.Pointer, ioSize *uint32, data unsafe.Pointer) int32
	AudioFormatGetPropertyInfo      func(id uint32, specifierSize uint32, specifier unsafe.Pointer, outSize *uint32) int32
)

var (
	loadOnce sync.Once
	loadErr  error
)

// Load opens AudioToolbox and binds every symbol. It is safe to call
// repeatedly; the result is cached.
func Load() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

func load() error {
	const path = "/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox"
	h, err := purego.Dlopen(path, rtldLazy|rtldGlobal)
	if err != nil {
		return fmt.Errorf("audiotoolbox: dlopen %s: %w", path, err)
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
	bind(&AudioConverterNew, "AudioConverterNew")
	bind(&AudioConverterDispose, "AudioConverterDispose")
	bind(&AudioConverterReset, "AudioConverterReset")
	bind(&AudioConverterFillComplexBuffer, "AudioConverterFillComplexBuffer")
	bind(&AudioConverterSetProperty, "AudioConverterSetProperty")
	bind(&AudioConverterGetProperty, "AudioConverterGetProperty")
	bind(&AudioConverterGetPropertyInfo, "AudioConverterGetPropertyInfo")
	bind(&AudioFormatGetProperty, "AudioFormatGetProperty")
	bind(&AudioFormatGetPropertyInfo, "AudioFormatGetPropertyInfo")
	if len(missing) > 0 {
		return errors.New("audiotoolbox: missing symbols: " + strings.Join(missing, ", "))
	}
	return nil
}

// SetUint32 sets a UInt32 converter property.
func SetUint32(converter uintptr, id uint32, v uint32) int32 {
	return AudioConverterSetProperty(converter, id, 4, unsafe.Pointer(&v))
}

// GetUint32 reads a UInt32 converter property.
func GetUint32(converter uintptr, id uint32) (uint32, int32) {
	var v uint32
	size := uint32(4)
	st := AudioConverterGetProperty(converter, id, &size, unsafe.Pointer(&v))
	return v, st
}

// GetBytes reads a variable-size converter property such as a magic cookie.
// It returns nil when the converter has no value for it.
func GetBytes(converter uintptr, id uint32) []byte {
	var size uint32
	if AudioConverterGetPropertyInfo(converter, id, &size, nil) != 0 || size == 0 {
		return nil
	}
	buf := make([]byte, size)
	if AudioConverterGetProperty(converter, id, &size, unsafe.Pointer(&buf[0])) != 0 {
		return nil
	}
	return buf[:size]
}

// SetBytes sets a variable-size converter property.
func SetBytes(converter uintptr, id uint32, b []byte) int32 {
	if len(b) == 0 {
		return 0
	}
	return AudioConverterSetProperty(converter, id, uint32(len(b)), unsafe.Pointer(&b[0]))
}

// FormatIDs lists the format identifiers AudioToolbox can encode (when
// encoders is true) or decode on this system.
func FormatIDs(encoders bool) []uint32 {
	id := FormatPropDecodeFormatIDs
	if encoders {
		id = FormatPropEncodeFormatIDs
	}
	var size uint32
	if AudioFormatGetPropertyInfo(id, 0, nil, &size) != 0 || size < 4 {
		return nil
	}
	ids := make([]uint32, size/4)
	if AudioFormatGetProperty(id, 0, nil, &size, unsafe.Pointer(&ids[0])) != 0 {
		return nil
	}
	return ids[:size/4]
}

// OSStatus codes from AudioConverter.h, AudioFormat.h and AudioCodec.h.
const (
	StatusFormatNotSupported       int32 = 0x666D743F // 'fmt?'
	StatusOperationNotSupported    int32 = 0x6F703F3F // 'op??'
	StatusPropertyNotSupported     int32 = 0x70726F70 // 'prop'
	StatusInvalidInputSize         int32 = 0x696E737A // 'insz'
	StatusInvalidOutputSize        int32 = 0x6F74737A // 'otsz'
	StatusUnspecifiedError         int32 = 0x77686174 // 'what'
	StatusBadPropertySize          int32 = 0x2173697A // '!siz'
	StatusRequiresPacketDescr      int32 = 0x21706B64 // '!pkd'
	StatusInputSampleRateOutOfRng  int32 = 0x21697372 // '!isr'
	StatusOutputSampleRateOutOfRng int32 = 0x216F7372 // '!osr'
	StatusUnsupportedFormat        int32 = 0x21646174 // '!dat'
	StatusUnknownFormat            int32 = 0x21666D74 // '!fmt'
	StatusBadSpecifierSize         int32 = 0x21737063 // '!spc'
	StatusParam                    int32 = -50
)

var statusNames = map[int32]string{
	StatusFormatNotSupported:       "format not supported",
	StatusOperationNotSupported:    "operation not supported",
	StatusPropertyNotSupported:     "property not supported",
	StatusInvalidInputSize:         "invalid input size",
	StatusInvalidOutputSize:        "invalid output size",
	StatusUnspecifiedError:         "unspecified error",
	StatusBadPropertySize:          "bad property size",
	StatusRequiresPacketDescr:      "packet descriptions required",
	StatusInputSampleRateOutOfRng:  "input sample rate out of range",
	StatusOutputSampleRateOutOfRng: "output sample rate out of range",
	StatusUnsupportedFormat:        "unsupported data format",
	StatusUnknownFormat:            "unknown format",
	StatusBadSpecifierSize:         "bad specifier size",
	StatusParam:                    "invalid parameter",
}

// StatusString describes an OSStatus value: a known name, the
// four-character code when the value is one, or the number.
func StatusString(status int32) string {
	if s, ok := statusNames[status]; ok {
		return s
	}
	b := [4]byte{byte(status >> 24), byte(status >> 16), byte(status >> 8), byte(status)}
	for _, c := range b {
		if c < 0x20 || c > 0x7E {
			return fmt.Sprintf("OSStatus %d", status)
		}
	}
	return fmt.Sprintf("OSStatus '%s'", b[:])
}
