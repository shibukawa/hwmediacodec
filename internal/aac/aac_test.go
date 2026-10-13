package aac

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseConfig(t *testing.T) {
	cases := []struct {
		name string
		asc  []byte
		want Config
		rate int // output
		ch   int // output
		out  int // output samples per access unit
	}{
		{"lc 48k stereo", []byte{0x11, 0x90}, Config{ObjectType: 2, SampleRate: 48000, ChannelConfig: 2, FrameSamples: 1024}, 48000, 2, 1024},
		{"lc 44.1k mono", []byte{0x12, 0x08}, Config{ObjectType: 2, SampleRate: 44100, ChannelConfig: 1, FrameSamples: 1024}, 44100, 1, 1024},
		{"lc 5.1", []byte{0x11, 0xB0}, Config{ObjectType: 2, SampleRate: 48000, ChannelConfig: 6, FrameSamples: 1024}, 48000, 6, 1024},
		{"lc 7.1", []byte{0x11, 0xB8}, Config{ObjectType: 2, SampleRate: 48000, ChannelConfig: 7, FrameSamples: 1024}, 48000, 8, 1024},
		{"lc 960", []byte{0x11, 0x94}, Config{ObjectType: 2, SampleRate: 48000, ChannelConfig: 2, FrameSamples: 960}, 48000, 2, 960},
		// As afconvert writes them: a sync extension after the AAC-LC config.
		{"he backward compatible", []byte{0x13, 0x10, 0x56, 0xE5, 0x98},
			Config{ObjectType: 2, SampleRate: 24000, ChannelConfig: 2, FrameSamples: 1024, SBR: true, ExtensionSampleRate: 48000}, 48000, 2, 2048},
		{"he v2 backward compatible", []byte{0x13, 0x08, 0x56, 0xE5, 0x9D, 0x48, 0x80},
			Config{ObjectType: 2, SampleRate: 24000, ChannelConfig: 1, FrameSamples: 1024, SBR: true, PS: true, ExtensionSampleRate: 48000}, 48000, 2, 2048},
		// Hierarchical: object type 5, the extension rate, then the core.
		{"he hierarchical", []byte{0x2B, 0x11, 0x88, 0x00},
			Config{ObjectType: 2, SampleRate: 24000, ChannelConfig: 2, FrameSamples: 1024, SBR: true, ExtensionSampleRate: 48000}, 48000, 2, 2048},
		// An explicit 24-bit rate.
		{"lc 12345 Hz", mustLC(t, 12345, 2), Config{ObjectType: 2, SampleRate: 12345, ChannelConfig: 2, FrameSamples: 1024}, 12345, 2, 1024},
	}
	for _, c := range cases {
		got, err := ParseConfig(c.asc)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got.OutputSampleRate() != c.rate || got.OutputChannels() != c.ch || got.OutputFrameSamples() != c.out {
			t.Errorf("%s: output %d Hz, %d channels, %d samples; want %d, %d, %d", c.name,
				got.OutputSampleRate(), got.OutputChannels(), got.OutputFrameSamples(), c.rate, c.ch, c.out)
		}
	}
	for _, bad := range [][]byte{nil, {0x11}, {0x16, 0x90} /* rate index 13 */} {
		if _, err := ParseConfig(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseConfig(% x): got %v, want ErrInvalid", bad, err)
		}
	}
}

func mustLC(t *testing.T, rate, channels int) []byte {
	t.Helper()
	b, err := LCConfig(rate, channels)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLCConfig(t *testing.T) {
	if got := mustLC(t, 48000, 2); !bytes.Equal(got, []byte{0x11, 0x90}) {
		t.Errorf("48 kHz stereo: % x", got)
	}
	if got := mustLC(t, 44100, 1); !bytes.Equal(got, []byte{0x12, 0x08}) {
		t.Errorf("44.1 kHz mono: % x", got)
	}
	for _, rate := range []int{8000, 22050, 32000, 96000, 11111} {
		for _, ch := range []int{1, 2, 6, 8} {
			c, err := ParseConfig(mustLC(t, rate, ch))
			if err != nil || c.ObjectType != ObjectTypeLC || c.SampleRate != rate || c.Channels() != ch {
				t.Errorf("%d Hz, %d channels: parsed back as %+v (%v)", rate, ch, c, err)
			}
		}
	}
	if _, err := LCConfig(48000, 7); err == nil {
		t.Error("7 channels have no channel configuration, yet LCConfig succeeded")
	}
	if _, err := LCConfig(0, 2); err == nil {
		t.Error("LCConfig accepted a sample rate of 0")
	}
}

func TestADTS(t *testing.T) {
	cfg := Config{ObjectType: ObjectTypeLC, SampleRate: 44100, ChannelConfig: 2, FrameSamples: 1024}
	hdr, err := AppendADTS(nil, cfg, 300)
	if err != nil {
		t.Fatal(err)
	}
	// The header ffmpeg writes for the same stream and size.
	if want := []byte{0xFF, 0xF1, 0x50, 0x80, 0x26, 0x7F, 0xFC}; !bytes.Equal(hdr, want) {
		t.Errorf("header % x, want % x", hdr, want)
	}
	h, err := ParseADTS(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if h.ObjectType != 2 || h.SampleRate != 44100 || h.ChannelConfig != 2 || h.HeaderSize != 7 || h.FrameSize != 307 || h.Blocks != 1 {
		t.Errorf("parsed %+v", h)
	}
	if !bytes.Equal(h.Config(), []byte{0x12, 0x10}) {
		t.Errorf("config % x", h.Config())
	}
	// With a CRC the header is two bytes longer.
	hdr[1] &^= 1
	if h, err := ParseADTS(hdr); err != nil || h.HeaderSize != 9 {
		t.Errorf("header with CRC: %+v, %v", h, err)
	}
	for name, bad := range map[string][]byte{
		"short":      {0xFF, 0xF1, 0x50},
		"no sync":    {0x00, 0xF1, 0x50, 0x80, 0x26, 0x7F, 0xFC},
		"layer":      {0xFF, 0xF3, 0x50, 0x80, 0x26, 0x7F, 0xFC},
		"tiny frame": {0xFF, 0xF1, 0x50, 0x80, 0x00, 0x7F, 0xFC},
		"rate index": {0xFF, 0xF1, 0x74, 0x80, 0x26, 0x7F, 0xFC},
	} {
		if _, err := ParseADTS(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	for name, c := range map[string]struct {
		cfg  Config
		size int
	}{
		"object type": {Config{ObjectType: 5, SampleRate: 44100, ChannelConfig: 2}, 100},
		"rate":        {Config{ObjectType: 2, SampleRate: 12345, ChannelConfig: 2}, 100},
		"channels":    {Config{ObjectType: 2, SampleRate: 44100, ChannelConfig: 0}, 100},
		"too large":   {cfg, 8192},
	} {
		if _, err := AppendADTS(nil, c.cfg, c.size); err == nil {
			t.Errorf("%s: AppendADTS succeeded", name)
		}
	}
}
