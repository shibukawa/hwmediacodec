//go:build darwin

package audiotoolbox

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// A frame of a minute of PCM is encoded in parts: Send stops at maxPending
// packets, Receive encodes the rest as it makes room, and Flush gets the
// last samples out. Nothing is lost and the queue never grows past the
// bound before Flush.
func TestEncoderEncodesLongFrameInParts(t *testing.T) {
	const seconds, rate, channels = 60, 48000, 2
	ctx := context.Background()
	enc, err := Backend{}.NewAudioEncoder(ctx, codec.AudioEncoderConfig{Codec: codec.AAC, SampleRate: rate, Channels: channels, SampleFormat: codec.S16, TimeScale: rate, RateControl: codec.VBR})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	e := enc.(*encoder)
	samples := make([]int16, seconds*rate*channels)
	for i := range samples {
		samples[i] = int16(8000 * math.Sin(float64(i/channels)*2*math.Pi*440/rate))
	}
	if err := e.Send(ctx, codec.S16Frame(samples, rate, channels, 0)); err != nil {
		t.Fatal(err)
	}
	if n := len(e.pending); n > maxPending {
		t.Fatalf("Send queued %d packets, the bound is %d", n, maxPending)
	}
	if !e.hasInput() {
		t.Fatal("the whole minute was encoded by Send")
	}
	// More input is refused while the queue is full.
	if err := e.Send(ctx, codec.S16Frame(samples[:rate], rate, channels, 0)); !errors.Is(err, codec.ErrAgain) {
		t.Fatalf("Send with a full queue = %v, want ErrAgain", err)
	}
	var packets int
	var duration int64
	drain := func(bounded bool) {
		for {
			p, err := e.Receive(ctx)
			if errors.Is(err, codec.ErrAgain) || err == io.EOF {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bounded && len(e.pending) > maxPending {
				t.Fatalf("the queue grew to %d packets while receiving", len(e.pending))
			}
			packets++
			duration += p.Duration
		}
	}
	drain(true)
	if packets <= maxPending {
		t.Fatalf("Receive handed out %d packets and stopped: it did not encode what Send left", packets)
	}
	if err := e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	drain(false)
	if _, err := e.Receive(ctx); err != io.EOF {
		t.Fatalf("after the drain Receive = %v, want io.EOF", err)
	}
	// Every sample came out: the durations cover the signal and the
	// encoder's delay in front of it.
	if want := int64(seconds*rate + e.format.Delay); duration != want {
		t.Fatalf("the packets cover %d samples, want %d", duration, want)
	}
	if min := seconds * rate / e.format.FrameSamples; packets < min {
		t.Fatalf("%d packets for a minute, want at least %d", packets, min)
	}
}
