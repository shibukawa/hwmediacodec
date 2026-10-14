//go:build darwin

package videotoolbox

import (
	"context"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

func TestVideoSendBoundsPendingAndInflight(t *testing.T) {
	ctx := context.Background()
	d := &decoder{inflight: make(map[uintptr]inflightFrame)}
	e := &encoder{cfg: codec.EncoderConfig{InputFormat: codec.NV12, Width: 2, Height: 2}, inflight: make(map[uintptr]int64)}
	for i := range maxPendingPackets {
		d.inflight[uintptr(i)] = inflightFrame{}
		e.inflight[uintptr(i)] = 0
	}
	f := &codec.Frame{Format: codec.NV12, Width: 2, Height: 2, Planes: [][]byte{make([]byte, 4), make([]byte, 2)}, Strides: []int{2, 2}}
	if err := d.Send(ctx, codec.Packet{Data: []byte{1}}); !errors.Is(err, codec.ErrAgain) {
		t.Fatalf("decoder Send: %v", err)
	}
	if err := e.Send(ctx, f); !errors.Is(err, codec.ErrAgain) {
		t.Fatalf("encoder Send: %v", err)
	}
}

func TestEncoderReservationsTrackRepeatedPTSAndFailures(t *testing.T) {
	if err := sys.Load(); err != nil {
		t.Fatal(err)
	}
	encode := sys.VTCompressionSessionEncodeFrame
	t.Cleanup(func() { sys.VTCompressionSessionEncodeFrame = encode })
	e := &encoder{inflight: map[uintptr]int64{}}
	encRegistryMu.Lock()
	e.handle = encNextHandle
	encNextHandle++
	encRegistry[e.handle] = e
	encRegistryMu.Unlock()
	defer e.Close()
	var refs []uintptr
	sys.VTCompressionSessionEncodeFrame = func(_, _ uintptr, _, _ sys.CMTime, _, ref uintptr, _ *uint32) int32 {
		refs = append(refs, ref)
		return 0
	}
	for range 2 {
		if st := e.submitFrame(0, sys.CMTime{Value: -7}, 0, nil); st != 0 {
			t.Fatal(st)
		}
	}
	if len(e.inflight) != 2 || refs[0] == refs[1] {
		t.Fatal("equal timestamps shared an output reservation")
	}
	for _, ref := range refs {
		if e.inflight[ref] != -7 {
			t.Fatal("timestamp was not preserved")
		}
		onEncodedFrame(e.handle, ref, 0, sys.EncodeInfoFrameDropped, 0)
	}
	if len(e.inflight) != 0 || e.dropped != 2 {
		t.Fatal("completed callbacks retained output reservations")
	}
	sys.VTCompressionSessionEncodeFrame = func(_, _ uintptr, _, _ sys.CMTime, _, _ uintptr, _ *uint32) int32 { return sys.StatusVTInvalidSession }
	if st := e.submitFrame(0, sys.CMTime{}, 0, nil); st != sys.StatusVTInvalidSession || len(e.inflight) != 0 {
		t.Fatalf("failed submission: status %d, outstanding %d", st, len(e.inflight))
	}
	for _, callback := range []bool{false, true} {
		sys.VTCompressionSessionEncodeFrame = func(_, _ uintptr, _, _ sys.CMTime, _, ref uintptr, flags *uint32) int32 {
			*flags = sys.EncodeInfoFrameDropped
			if callback {
				onEncodedFrame(e.handle, ref, 0, *flags, 0)
			}
			return 0
		}
		before := e.dropped
		var flags uint32
		if st := e.submitFrame(0, sys.CMTime{}, 0, &flags); st != 0 || len(e.inflight) != 0 || e.dropped != before+1 {
			t.Fatalf("synchronous drop (callback %v): status %d, outstanding %d, dropped %d", callback, st, len(e.inflight), e.dropped)
		}
	}
}
