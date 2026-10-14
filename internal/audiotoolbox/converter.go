//go:build darwin

package audiotoolbox

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/shibukawa/hwmediacodec/internal/audiotoolbox/sys"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// statusNoData is what the input callback answers when it has nothing to
// give right now but the stream has not ended ('hwnd'). AudioConverter
// passes a callback's error through as the result of
// AudioConverterFillComplexBuffer and keeps its state, which is how a
// converter is driven with input that arrives piece by piece.
const statusNoData int32 = 0x68776E64

// maxPending bounds the frames or packets held for Receive; Send reports
// ErrAgain beyond it.
const maxPending = 256

// input is the queue a converter's input callback serves from. It is only
// touched by the goroutine that is inside AudioConverterFillComplexBuffer
// (the callback runs on the calling thread) and by the same goroutine
// between calls, under the owner's mutex.
type input struct {
	// chunks is the queued input, oldest first.
	chunks [][]byte
	// unit is the size of one input packet for PCM (the bytes of one
	// sample of every channel). It is 0 for compressed input, where every
	// chunk is one packet.
	unit int
	// eof makes an empty queue mean the end of the stream.
	eof bool
	// held is the chunk handed out last. AudioToolbox reads it until the
	// callback is asked again, so it must stay reachable until then.
	held []byte
	desc sys.AudioStreamPacketDescription
}

// Inputs are reached from the callback through a small integer handle,
// never through a Go pointer.
var (
	inputsMu     sync.Mutex
	inputs               = map[uintptr]*input{}
	nextHandle   uintptr = 1
	callbackOnce sync.Once
	callbackPtr  uintptr
)

func registerInput(in *input) uintptr {
	inputsMu.Lock()
	defer inputsMu.Unlock()
	h := nextHandle
	nextHandle++
	inputs[h] = in
	return h
}

func unregisterInput(h uintptr) {
	inputsMu.Lock()
	delete(inputs, h)
	inputsMu.Unlock()
}

func inputCallback() uintptr {
	callbackOnce.Do(func() {
		callbackPtr = purego.NewCallback(onInput)
	})
	return callbackPtr
}

// onInput is the AudioConverterComplexInputDataProc.
func onInput(converter uintptr, ioPackets *uint32, ioData *sys.AudioBufferList, outDesc **sys.AudioStreamPacketDescription, user uintptr) uintptr {
	inputsMu.Lock()
	in := inputs[user]
	inputsMu.Unlock()
	if *ioPackets == 0 {
		ioData.Buffer.Data = nil
		ioData.Buffer.DataByteSize = 0
		return 0
	}
	if in == nil || len(in.chunks) == 0 {
		*ioPackets = 0
		ioData.Buffer.Data = nil
		ioData.Buffer.DataByteSize = 0
		if in != nil && in.eof {
			return 0
		}
		return uintptr(statusNoData)
	}
	chunk := in.chunks[0]
	if in.unit > 0 {
		packets := min(uint64(*ioPackets), uint64(len(chunk)/in.unit))
		if want := int(packets) * in.unit; want < len(chunk) {
			in.chunks[0] = chunk[want:]
			chunk = chunk[:want]
		} else {
			in.chunks = in.chunks[1:]
		}
		*ioPackets = uint32(len(chunk) / in.unit)
	} else {
		in.chunks = in.chunks[1:]
		*ioPackets = 1
		in.desc = sys.AudioStreamPacketDescription{DataByteSize: uint32(len(chunk))}
		if outDesc != nil {
			*outDesc = &in.desc
		}
	}
	in.held = chunk
	ioData.NumberBuffers = 1
	ioData.Buffer.Data = unsafe.Pointer(unsafe.SliceData(chunk))
	ioData.Buffer.DataByteSize = uint32(len(chunk))
	return 0
}

// pcmDescription describes interleaved PCM in f.
func pcmDescription(f codec.SampleFormat, sampleRate, channels int) sys.AudioStreamBasicDescription {
	flags := sys.FlagIsSignedInteger | sys.FlagIsPacked
	if f == codec.F32 {
		flags = sys.FlagIsFloat | sys.FlagIsPacked
	}
	bytes := uint32(f.BytesPerSample() * channels)
	return sys.AudioStreamBasicDescription{
		SampleRate:       float64(sampleRate),
		FormatID:         sys.FormatLinearPCM,
		FormatFlags:      flags,
		BytesPerPacket:   bytes,
		FramesPerPacket:  1,
		BytesPerFrame:    bytes,
		ChannelsPerFrame: uint32(channels),
		BitsPerChannel:   uint32(f.BytesPerSample() * 8),
	}
}

func backendError(op string, status int32) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(status), Message: sys.StatusString(status)}
}
