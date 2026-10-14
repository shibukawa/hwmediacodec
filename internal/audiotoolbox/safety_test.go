//go:build darwin

package audiotoolbox

import (
	"context"
	"errors"
	"testing"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/audiotoolbox/sys"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

func TestInputZeroRequestDoesNotConsumePCM(t *testing.T) {
	in := &input{unit: 4, chunks: [][]byte{make([]byte, 8)}}
	h := registerInput(in)
	defer unregisterInput(h)
	n := uint32(0)
	var list sys.AudioBufferList
	if st := onInput(0, &n, &list, nil, h); st != 0 {
		t.Fatalf("callback status %d", st)
	}
	if len(in.chunks) != 1 || len(in.chunks[0]) != 8 || list.Buffer.Data != nil {
		t.Fatal("zero request consumed input")
	}
	n = ^uint32(0)
	if st := onInput(0, &n, &list, nil, h); st != 0 || n != 2 || list.Buffer.DataByteSize != 8 {
		t.Fatalf("large request: status %d, packets %d, bytes %d", st, n, list.Buffer.DataByteSize)
	}
}

func TestCanceledAudioMethodsPreserveState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &encoder{pending: []codec.Packet{{Data: []byte{1}}}}
	d := &decoder{out: []*codec.AudioFrame{{Data: []byte{1}}}}
	if _, err := e.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("encoder Receive: %v", err)
	}
	if _, err := d.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("decoder Receive: %v", err)
	}
	if err := e.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("encoder Flush: %v", err)
	}
	if err := d.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("decoder Flush: %v", err)
	}
	if len(e.pending) != 1 || len(d.out) != 1 || e.flushed || d.flushed {
		t.Fatal("canceled method changed queued output")
	}
}

func TestDecoderErrorDropsBufferedTimestamps(t *testing.T) {
	fill, reset := sys.AudioConverterFillComplexBuffer, sys.AudioConverterReset
	t.Cleanup(func() { sys.AudioConverterFillComplexBuffer, sys.AudioConverterReset = fill, reset })
	sys.AudioConverterFillComplexBuffer = func(_ uintptr, _ uintptr, _ uintptr, n *uint32, _ *sys.AudioBufferList, _ *sys.AudioStreamPacketDescription) int32 {
		*n = 0
		return sys.StatusInvalidInputSize
	}
	sys.AudioConverterReset = func(uintptr) int32 { return 0 }
	d := &decoder{cfg: codec.AudioDecoderConfig{Format: codec.AudioFormat{Codec: codec.AAC}, SampleFormat: codec.S16}, in: &input{}, frame: 2, channels: 1, conv: 1, pts: []int64{99}, pcm: []byte{1, 2}}
	if err := d.Send(context.Background(), codec.Packet{Data: []byte{1}, PTS: 100}); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("Send: %v", err)
	}
	if len(d.pts) != 0 || len(d.pcm) != 0 {
		t.Fatal("converter reset retained output from the old state")
	}
}

func TestFailedConverterConfigurationDisposesConverter(t *testing.T) {
	newFn, dispose, set := sys.AudioConverterNew, sys.AudioConverterDispose, sys.AudioConverterSetProperty
	t.Cleanup(func() {
		sys.AudioConverterNew, sys.AudioConverterDispose, sys.AudioConverterSetProperty = newFn, dispose, set
	})
	sys.AudioConverterNew = func(_, _ *sys.AudioStreamBasicDescription, out *uintptr) int32 { *out = 42; return 0 }
	disposed := uintptr(0)
	sys.AudioConverterDispose = func(c uintptr) int32 { disposed = c; return 0 }
	sys.AudioConverterSetProperty = func(uintptr, uint32, uint32, unsafe.Pointer) int32 { return sys.StatusParam }
	d := &decoder{cookie: []byte{1}, cfg: codec.AudioDecoderConfig{SampleFormat: codec.S16}, sampleRate: 48000, channels: 1}
	if err := d.createConverter(); err == nil {
		t.Fatal("configuration failure was ignored")
	}
	if disposed != 42 || d.conv != 0 {
		t.Fatalf("disposed %d, live converter %d", disposed, d.conv)
	}
}

func TestDecoderRejectsOversizedNativeOutput(t *testing.T) {
	fill := sys.AudioConverterFillComplexBuffer
	t.Cleanup(func() { sys.AudioConverterFillComplexBuffer = fill })
	sys.AudioConverterFillComplexBuffer = func(_ uintptr, _ uintptr, _ uintptr, n *uint32, _ *sys.AudioBufferList, _ *sys.AudioStreamPacketDescription) int32 {
		*n = 3
		return 0
	}
	d := &decoder{frame: 2, channels: 1, cfg: codec.AudioDecoderConfig{SampleFormat: codec.S16}}
	if n, st := d.fill(make([]byte, 4)); n != 0 || st != sys.StatusInvalidOutputSize {
		t.Fatalf("fill returned %d samples, status %d", n, st)
	}
}
