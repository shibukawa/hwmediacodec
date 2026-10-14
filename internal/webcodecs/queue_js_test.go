//go:build js && wasm

package webcodecs

import (
	"context"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

func TestSendBoundsOutstandingBrowserWork(t *testing.T) {
	ctx := context.Background()
	e := &encoder{cfg: codec.EncoderConfig{Width: 2, Height: 2}, inflight: map[int64]int64{}}
	d := &decoder{cfg: codec.DecoderConfig{OutputFormat: codec.Native}, inflight: map[int64]inflightFrame{}}
	for i := range maxPending {
		e.inflight[int64(i)] = int64(i)
		d.inflight[int64(i)] = inflightFrame{}
	}
	f := &codec.Frame{Format: codec.NV12, Width: 2, Height: 2, Planes: [][]byte{make([]byte, 4), make([]byte, 2)}, Strides: []int{2, 2}}
	if err := e.Send(ctx, f); !errors.Is(err, codec.ErrAgain) {
		t.Fatalf("encoder Send: %v", err)
	}
	if err := d.Send(ctx, codec.Packet{Data: []byte{1}}); !errors.Is(err, codec.ErrAgain) {
		t.Fatalf("decoder Send: %v", err)
	}
	ad := &audioDecoder{waiting: make([]audioPacket, maxAudioPending)}
	if err := ad.Send(ctx, codec.Packet{Data: []byte{1}}); !errors.Is(err, codec.ErrAgain) {
		t.Fatalf("audio decoder Send: %v", err)
	}
}
