package testutil

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// LongFrame describes an audio encoder under the long-frame check: the
// encoder and the views into it that the check needs.
type LongFrame struct {
	Encoder    codec.AudioEncoder
	SampleRate int
	Channels   int
	// Pending reports how many packets wait in the encoder's queue, and
	// Bound how many it may hold before Flush; Slack is what the queue may
	// pass Bound by while receiving (packets a codec had in flight).
	Pending func() int
	Bound   int
	Slack   int
	// HasInput reports whether samples Send accepted wait to be encoded.
	HasInput func() bool
}

// CheckLongFrame sends one frame of a minute of 16-bit PCM and checks that
// it is encoded in parts: Send stops at the queue's bound, a second Send is
// refused with ErrAgain, Receive encodes the rest as it makes room without
// the queue growing past the bound, and after Flush the packets cover
// every sample plus the encoder's delay.
func CheckLongFrame(t testing.TB, c LongFrame) {
	t.Helper()
	const seconds = 60
	ctx := context.Background()
	e, rate, channels := c.Encoder, c.SampleRate, c.Channels
	samples := make([]int16, seconds*rate*channels)
	for i := range samples {
		samples[i] = int16(8000 * math.Sin(float64(i/channels)*2*math.Pi*440/float64(rate)))
	}
	if err := e.Send(ctx, codec.S16Frame(samples, rate, channels, 0)); err != nil {
		t.Fatal(err)
	}
	if n := c.Pending(); n > c.Bound+c.Slack {
		t.Fatalf("Send queued %d packets, the bound is %d", n, c.Bound)
	}
	if !c.HasInput() {
		t.Fatal("the whole minute was encoded by Send")
	}
	if err := e.Send(ctx, codec.S16Frame(samples[:rate*channels], rate, channels, 0)); !errors.Is(err, codec.ErrAgain) {
		t.Fatalf("Send with a full queue = %v, want ErrAgain", err)
	}
	var packets int
	var duration int64
	drain := func(bounded bool) {
		t.Helper()
		for {
			p, err := e.Receive(ctx)
			if errors.Is(err, codec.ErrAgain) || err == io.EOF {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if n := c.Pending(); bounded && n > c.Bound+c.Slack {
				t.Fatalf("the queue grew to %d packets while receiving", n)
			}
			packets++
			duration += p.Duration
		}
	}
	drain(true)
	if packets <= c.Bound {
		t.Fatalf("Receive handed out %d packets and stopped: it did not encode what Send left", packets)
	}
	if err := e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	drain(false)
	if _, err := e.Receive(ctx); err != io.EOF {
		t.Fatalf("after the drain Receive = %v, want io.EOF", err)
	}
	// The durations count in the encoder's time scale, which the callers
	// open at the sample rate.
	f := e.Format()
	if want := int64(seconds*rate + f.Delay); duration != want {
		t.Fatalf("the packets cover %d samples, want %d", duration, want)
	}
	if min := seconds * rate / f.FrameSamples; packets < min {
		t.Fatalf("%d packets for a minute, want at least %d", packets, min)
	}
}
