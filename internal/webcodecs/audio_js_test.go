//go:build js && wasm

package webcodecs

import (
	"context"
	"errors"
	"io"
	"math"
	"syscall/js"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

func TestAudioConfigs(t *testing.T) {
	for _, c := range []struct {
		asc  []byte
		want string
	}{
		{[]byte{0x11, 0x90}, "mp4a.40.2"},
		{[]byte{0x13, 0x10, 0x56, 0xE5, 0x98}, "mp4a.40.5"},
		{[]byte{0x13, 0x08, 0x56, 0xE5, 0x9D, 0x48, 0x80}, "mp4a.40.29"},
	} {
		cfg, err := aac.ParseConfig(c.asc)
		if err != nil {
			t.Fatal(err)
		}
		if got := audioCodecString(codec.AAC, cfg); got != c.want {
			t.Errorf("% x: codec string %s, want %s", c.asc, got, c.want)
		}
	}
	// HE-AAC is configured at its output rate with the config as
	// description; Opus without one, whatever the format carries.
	config, rate, channels, frame, err := audioDecoderConfig(codec.AudioFormat{Codec: codec.AAC, Config: []byte{0x13, 0x10, 0x56, 0xE5, 0x98}})
	if err != nil || rate != 48000 || channels != 2 || frame != 2048 || config["codec"] != "mp4a.40.5" || config["description"] == nil {
		t.Errorf("HE-AAC: %v, %d Hz, %d channels, frames of %d (%v)", config["codec"], rate, channels, frame, err)
	}
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 1, 0x38, 0x01, 0x80, 0xBB, 0, 0, 0, 0, 0}
	config, rate, channels, frame, err = audioDecoderConfig(codec.AudioFormat{Codec: codec.Opus, Channels: 2, Config: head})
	if err != nil || rate != 48000 || channels != 1 || frame != 0 || config["description"] != nil {
		t.Errorf("Opus: %v, %d Hz, %d channels, frame %d (%v)", config, rate, channels, frame, err)
	}
	if _, _, _, _, err := audioDecoderConfig(codec.AudioFormat{Codec: codec.AAC, Config: []byte{0x11, 0xB8}}); err == nil {
		t.Error("an AAC stream with 8 channels was accepted")
	}
	for wav, order := range bitstreamOrder {
		seen := map[int]bool{}
		for _, from := range order {
			seen[from] = true
		}
		if len(order) != wav || len(seen) != wav {
			t.Errorf("channel order for %d channels is not a permutation: %v", wav, order)
		}
	}
	cbr := codec.AudioEncoderConfig{Codec: codec.Opus, SampleRate: 48000, Channels: 2, Bitrate: 64000, RateControl: codec.CBR}
	if c := audioEncoderConfig(&cbr); c["bitrateMode"] != "constant" || c["bitrate"] != 64000 || c["codec"] != "opus" {
		t.Errorf("Opus CBR config: %v", c)
	}
}

// A frame of a minute of PCM is handed to the browser in pieces: Send
// stops feeding when the packets it will produce reach maxAudioPending,
// Receive feeds the rest as it makes room, and Flush gets the last
// samples out. Nothing is lost and the queue never grows past the bound
// before Flush.
func TestAudioEncoderFeedsLongFrameInParts(t *testing.T) {
	if global.Get("AudioEncoder").Type() == js.TypeUndefined {
		t.Skip(noWebCodecs)
	}
	const seconds, rate, channels = 60, 48000, 2
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	enc, err := Backend{}.NewAudioEncoder(ctx, codec.AudioEncoderConfig{Codec: codec.Opus, SampleRate: rate, Channels: channels, SampleFormat: codec.S16, TimeScale: rate, RateControl: codec.VBR})
	if errors.Is(err, codec.ErrUnsupported) {
		t.Skipf("not available in this browser: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	e := enc.(*audioEncoder)
	samples := make([]int16, seconds*rate*channels)
	for i := range samples {
		samples[i] = int16(8000 * math.Sin(float64(i/channels)*2*math.Pi*440/rate))
	}
	if err := e.Send(ctx, codec.S16Frame(samples, rate, channels, 0)); err != nil {
		t.Fatal(err)
	}
	if len(e.input) == 0 {
		t.Fatal("the whole minute was handed to the browser by Send")
	}
	if n := len(e.pending); n > maxAudioPending {
		t.Fatalf("Send queued %d packets, the bound is %d", n, maxAudioPending)
	}
	var packets int
	var duration int64
	take := func(p codec.Packet) {
		packets++
		duration += p.Duration
	}
	// Receive feeds the rest while the page's event loop (a sleep yields
	// to it) brings the packets back.
	deadline := time.Now().Add(time.Minute)
	for len(e.input) > 0 || len(e.pending) > 0 {
		p, err := e.Receive(ctx)
		if errors.Is(err, codec.ErrAgain) {
			if time.Now().After(deadline) {
				t.Fatalf("the browser stopped returning packets with %d bytes of input left", len(e.input))
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(e.pending) > maxAudioPending {
			t.Fatalf("the queue grew to %d packets while receiving", len(e.pending))
		}
		take(p)
	}
	if packets <= maxAudioPending {
		t.Fatalf("Receive handed out %d packets and stopped: it did not feed what Send left", packets)
	}
	if err := e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		p, err := e.Receive(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		take(p)
	}
	if want := int64(seconds*rate + e.format.Delay); duration != want {
		t.Fatalf("the packets cover %d samples, want %d", duration, want)
	}
	if min := seconds * rate / e.format.FrameSamples; packets < min {
		t.Fatalf("%d packets for a minute, want at least %d", packets, min)
	}
}
