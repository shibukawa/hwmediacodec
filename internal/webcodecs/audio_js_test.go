//go:build js && wasm

package webcodecs

import (
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// TestTrialMeasurement checks the arithmetic of the encoder trial on
// made-up decoder output: where the signal starts and how much of its
// start is missing.
func TestTrialMeasurement(t *testing.T) {
	signal := trialSignal()
	for _, v := range signal {
		if v < -0.25 || v > 0.25 {
			t.Fatalf("trial signal sample %g outside [-0.25, 0.25]", v)
		}
	}
	shifted := func(delay, lost int) []float32 {
		out := make([]float32, delay+len(signal)+trialTail)
		copy(out[delay:], signal)
		clear(out[delay : delay+lost])
		// A codec's output is not the input: add a little of something else.
		for i := range out {
			out[i] += 0.01 * float32((i*7919)%13-6) / 6
		}
		return out
	}
	for _, c := range []struct{ delay, lost, wantLead int }{
		{2112, 0, 0},
		{312, 0, 0},
		{0, 0, 0},
		{2112, 1024, 1024},
		{1600, 1000, 1024},
	} {
		decoded := shifted(c.delay, c.lost)
		delay, ok := signalDelay(signal, decoded, 1024)
		if !ok || delay != c.delay {
			t.Errorf("delay %d with %d lost: measured %d (found %v)", c.delay, c.lost, delay, ok)
			continue
		}
		if lead := lostLead(signal, decoded, delay); lead != c.wantLead {
			t.Errorf("delay %d with %d lost: lead %d, want %d", c.delay, c.lost, lead, c.wantLead)
		}
	}
	// Output that has nothing to do with the signal gives no delay.
	unrelated := make([]float32, len(signal)+trialTail)
	for i := range unrelated {
		unrelated[i] = float32((i*31)%17-8) / 64
	}
	if delay, ok := signalDelay(signal, unrelated, 1024); ok {
		t.Errorf("a delay of %d was found in unrelated samples", delay)
	}
}

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
