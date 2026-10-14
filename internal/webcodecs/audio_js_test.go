//go:build js && wasm

package webcodecs

import (
	"testing"

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
