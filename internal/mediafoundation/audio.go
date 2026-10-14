//go:build windows && (amd64 || arm64)

package mediafoundation

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediafoundation/sys"
)

// The audio codecs of Windows are Media Foundation transforms like the
// video ones, but plain software: the AAC decoder and encoder that every
// edition with Media Foundation has, and an Opus decoder on the systems
// that carry one (there is no Opus encoder). They are synchronous and
// work on system memory, so none of the Direct3D machinery is involved.

// maxAudioPending bounds the frames and packets held for Receive; Send
// reports ErrAgain beyond it.
const maxAudioPending = 256

func unsupportedAudio(c codec.Codec, dir codec.Direction, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: dir, Reason: reason}
}

// audioSubtype returns the Media Foundation subtype of an audio codec.
func audioSubtype(c codec.Codec) (*sys.GUID, bool) {
	switch c {
	case codec.AAC:
		return &sys.MFAudioFormat_AAC, true
	case codec.Opus:
		return &sys.MFAudioFormat_Opus, true
	}
	return nil, false
}

// enumerateAudio lists the synchronous transforms of a category that take
// in and make out (either may be nil). The caller must releaseCandidates
// the result.
func enumerateAudio(category sys.GUID, in, out *sys.MFT_REGISTER_TYPE_INFO) ([]candidate, error) {
	flags := sys.MFT_ENUM_FLAG_SYNCMFT | sys.MFT_ENUM_FLAG_LOCALMFT | sys.MFT_ENUM_FLAG_SORTANDFILTER
	cands, err := enumerate(category, flags, in, out)
	if err != nil || len(cands) > 0 {
		return cands, err
	}
	// Codecs installed from the Store (the Opus decoder of the Web Media
	// Extensions) may only be listed with this flag, which older releases
	// do not know: a failure here is not an error.
	if more, err := enumerate(category, flags|sys.MFT_ENUM_FLAG_UNTRUSTED_STOREMFT, in, out); err == nil {
		return more, nil
	}
	return cands, nil
}

func audioDecoders(subtype *sys.GUID) ([]candidate, error) {
	in := sys.MFT_REGISTER_TYPE_INFO{MajorType: sys.MFMediaType_Audio, Subtype: *subtype}
	return enumerateAudio(sys.MFT_CATEGORY_AUDIO_DECODER, &in, nil)
}

func audioEncoders(subtype *sys.GUID) ([]candidate, error) {
	out := sys.MFT_REGISTER_TYPE_INFO{MajorType: sys.MFMediaType_Audio, Subtype: *subtype}
	return enumerateAudio(sys.MFT_CATEGORY_AUDIO_ENCODER, nil, &out)
}

// ProbeAudio implements codec.AudioBackend.
func (Backend) ProbeAudio(ctx context.Context) ([]codec.Capability, error) {
	if err := sys.Load(); err != nil {
		return nil, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := sys.CoInitializeMTA(); err != nil {
		return nil, err
	}
	var caps []codec.Capability
	add := func(c codec.Codec, dir codec.Direction, list func(*sys.GUID) ([]candidate, error)) error {
		subtype, _ := audioSubtype(c)
		cands, err := list(subtype)
		if err != nil {
			return err
		}
		usable := false
		for _, cand := range cands {
			usable = usable || cand.usable()
		}
		releaseCandidates(cands)
		if usable {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: dir})
		}
		return ctx.Err()
	}
	if err := add(codec.AAC, codec.Decode, audioDecoders); err != nil {
		return caps, err
	}
	if err := add(codec.AAC, codec.Encode, audioEncoders); err != nil {
		return caps, err
	}
	if err := add(codec.Opus, codec.Decode, audioDecoders); err != nil {
		return caps, err
	}
	return caps, nil
}

// audioTransform drives one synchronous audio transform: samples of
// system memory in, samples of system memory out.
type audioTransform struct {
	t        *sys.IMFTransform
	name     string
	in, out  uint32
	outSize  uint32
	provides bool // the transform allocates its output samples
	// streaming is set once the transform was told that data follows, and
	// started while a stream is under way.
	streaming bool
	started   bool
}

// openAudioTransform activates the first usable transform of the list.
// Without one it returns nil and what stood in the way.
func openAudioTransform(cands []candidate) (*audioTransform, string) {
	var reasons []string
	for _, cand := range cands {
		if !cand.usable() {
			reasons = append(reasons, cand.name+": not a synchronous software transform")
			continue
		}
		var t *sys.IMFTransform
		if hr := cand.activate.ActivateObject(&sys.IID_IMFTransform, unsafe.Pointer(&t)); hr.Failed() || t == nil {
			reasons = append(reasons, fmt.Sprintf("%s: activation failed: %s", cand.name, hr))
			continue
		}
		x := &audioTransform{t: t, name: cand.name}
		var in, out uint32
		// E_NOTIMPL means the stream identifiers are 0.
		if hr := t.GetStreamIDs(1, &in, 1, &out); !hr.Failed() {
			x.in, x.out = in, out
		}
		return x, ""
	}
	if len(reasons) == 0 {
		return nil, "no transform is registered"
	}
	return nil, strings.Join(reasons, "; ")
}

// newAudioType makes a media type of the audio major type.
func newAudioType(subtype *sys.GUID, sampleRate, channels int) (*sys.IMFMediaType, error) {
	var mt *sys.IMFMediaType
	if hr := sys.MFCreateMediaType(&mt); hr.Failed() {
		return nil, backendErr("MFCreateMediaType", hr)
	}
	a := mt.Attributes()
	a.SetGUID(&sys.MF_MT_MAJOR_TYPE, &sys.MFMediaType_Audio)
	a.SetGUID(&sys.MF_MT_SUBTYPE, subtype)
	a.SetUINT32(&sys.MF_MT_AUDIO_SAMPLES_PER_SECOND, uint32(sampleRate))
	a.SetUINT32(&sys.MF_MT_AUDIO_NUM_CHANNELS, uint32(channels))
	return mt, nil
}

// readOutputInfo records how the transform wants its output buffers,
// which may change with the output type.
func (x *audioTransform) readOutputInfo() error {
	var info sys.MFT_OUTPUT_STREAM_INFO
	if hr := x.t.GetOutputStreamInfo(x.out, &info); hr.Failed() {
		return backendErr("IMFTransform::GetOutputStreamInfo", hr)
	}
	x.provides = info.Flags&(sys.MFT_OUTPUT_STREAM_PROVIDES_SAMPLES|sys.MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES) != 0
	// A second of 5.1 in float at 96 kHz is more than any codec returns at
	// once; transforms that do not say what they need get that.
	x.outSize = max(info.Size, 1<<16)
	return nil
}

// notify sends a notification, which a transform may answer with
// E_NOTIMPL.
func (x *audioTransform) notify(message uint32, name string) error {
	if hr := x.t.ProcessMessage(message, 0); hr.Failed() && hr != sys.E_NOTIMPL {
		return backendErr("IMFTransform::ProcessMessage("+name+")", hr)
	}
	return nil
}

// begin tells the transform that a stream starts, if it has not been
// told.
func (x *audioTransform) begin() error {
	if !x.streaming {
		if err := x.notify(sys.MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, "BEGIN_STREAMING"); err != nil {
			return err
		}
		x.streaming = true
	}
	if !x.started {
		if err := x.notify(sys.MFT_MESSAGE_NOTIFY_START_OF_STREAM, "START_OF_STREAM"); err != nil {
			return err
		}
		x.started = true
	}
	return nil
}

// input hands the transform one buffer with its time and duration in
// 100 ns units. MF_E_NOTACCEPTING comes back as it is: the caller fetches
// output and tries again.
func (x *audioTransform) input(data []byte, hns, duration int64) sys.HRESULT {
	sample, err := newInputSample(data, hns)
	if err != nil {
		return sys.E_OUTOFMEMORY
	}
	defer sample.Release()
	sample.SetSampleDuration(duration)
	return x.t.ProcessInput(x.in, sample, 0)
}

// What output found.
const (
	outputData   = iota // a buffer
	outputEmpty         // the transform needs more input
	outputChange        // the output type has to be chosen again
)

// output asks the transform for one buffer of output.
func (x *audioTransform) output() (data []byte, state int, err error) {
	out := sys.MFT_OUTPUT_DATA_BUFFER{StreamID: x.out}
	var own *sys.IMFSample
	if !x.provides {
		var buf *sys.IMFMediaBuffer
		if hr := sys.MFCreateMemoryBuffer(x.outSize, &buf); hr.Failed() {
			return nil, 0, backendErr("MFCreateMemoryBuffer", hr)
		}
		hr := sys.MFCreateSample(&own)
		if !hr.Failed() {
			hr = own.AddBuffer(buf)
		}
		buf.Release()
		if hr.Failed() {
			if own != nil {
				own.Release()
			}
			return nil, 0, backendErr("MFCreateSample", hr)
		}
		out.Sample = own
	}
	var status uint32
	hr := x.t.ProcessOutput(0, 1, &out, &status)
	if out.Events != nil {
		out.Events.Release()
	}
	sample := out.Sample
	defer func() {
		if sample != nil {
			sample.Release()
		}
		if own != nil && own != sample {
			own.Release()
		}
	}()
	switch {
	case hr == sys.MF_E_TRANSFORM_STREAM_CHANGE,
		hr == sys.S_OK && sample == nil && out.Status&sys.MFT_OUTPUT_DATA_BUFFER_FORMAT_CHANGE != 0:
		return nil, outputChange, nil
	case hr == sys.MF_E_TRANSFORM_NEED_MORE_INPUT:
		return nil, outputEmpty, nil
	case hr.Failed():
		return nil, 0, backendErr("IMFTransform::ProcessOutput", hr)
	case sample == nil:
		return nil, 0, &codec.BackendError{Backend: Name, Op: "IMFTransform::ProcessOutput", Message: "no sample returned"}
	}
	var buf *sys.IMFMediaBuffer
	if hr := sample.ConvertToContiguousBuffer(&buf); hr.Failed() || buf == nil {
		return nil, 0, backendErr("IMFSample::ConvertToContiguousBuffer", hr)
	}
	defer buf.Release()
	var p *byte
	var maxLen, curLen uint32
	if hr := buf.Lock(&p, &maxLen, &curLen); hr.Failed() {
		return nil, 0, backendErr("IMFMediaBuffer::Lock", hr)
	}
	if curLen > 0 && p != nil {
		data = append([]byte(nil), unsafe.Slice(p, curLen)...)
	}
	buf.Unlock()
	return data, outputData, nil
}

// drain asks the transform for everything it holds back. The output is
// fetched by the caller.
func (x *audioTransform) drain() error {
	if err := x.notify(sys.MFT_MESSAGE_NOTIFY_END_OF_STREAM, "END_OF_STREAM"); err != nil {
		return err
	}
	if hr := x.t.ProcessMessage(sys.MFT_MESSAGE_COMMAND_DRAIN, 0); hr.Failed() {
		return backendErr("IMFTransform::ProcessMessage(DRAIN)", hr)
	}
	return nil
}

// reset makes the transform forget the stream so far; the next input
// starts another.
func (x *audioTransform) reset() {
	x.t.ProcessMessage(sys.MFT_MESSAGE_COMMAND_FLUSH, 0)
	x.started = false
}

func (x *audioTransform) release() {
	if x.t == nil {
		return
	}
	if x.streaming {
		x.t.ProcessMessage(sys.MFT_MESSAGE_NOTIFY_END_STREAMING, 0)
	}
	x.t.Release()
	x.t = nil
}

// samplesHNS converts a count of samples to 100 ns units.
func samplesHNS(samples int64, sampleRate int) int64 {
	return codec.Rescale(samples, int64(sampleRate), hnsPerSecond)
}

// comThread joins the calling thread to COM for the time a transform is
// set up; the returned function gives the thread back.
func comThread() (func(), error) {
	runtime.LockOSThread()
	if err := sys.CoInitializeMTA(); err != nil {
		runtime.UnlockOSThread()
		return nil, &codec.BackendError{Backend: Name, Op: "CoInitializeEx", Message: err.Error()}
	}
	return runtime.UnlockOSThread, nil
}
