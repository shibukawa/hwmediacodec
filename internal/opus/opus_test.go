package opus

import (
	"bytes"
	"errors"
	"testing"
)

func TestPacketSamples(t *testing.T) {
	cases := []struct {
		packet []byte
		want   int
	}{
		{[]byte{0xFC, 0}, 960},           // CELT fullband 20 ms, one frame
		{[]byte{0xF4, 0}, 480},           // CELT fullband 10 ms
		{[]byte{0xE0, 0}, 120},           // CELT fullband 2.5 ms
		{[]byte{0x08, 0}, 960},           // SILK narrowband 20 ms
		{[]byte{0x18, 0}, 2880},          // SILK narrowband 60 ms
		{[]byte{0x60, 0}, 480},           // hybrid 10 ms
		{[]byte{0x68, 0}, 960},           // hybrid 20 ms
		{[]byte{0xFD, 0, 0}, 1920},       // two frames of equal size
		{[]byte{0xFE, 1, 0, 0}, 1920},    // two frames of different size
		{[]byte{0xFF, 3, 0, 0, 0}, 2880}, // three frames
		{[]byte{0xFF, 0x86, 0}, 5760},    // six frames (VBR flag set): 120 ms
	}
	for _, c := range cases {
		got, err := PacketSamples(c.packet)
		if err != nil || got != c.want {
			t.Errorf("% x: got %d samples (%v), want %d", c.packet, got, err, c.want)
		}
	}
	for _, bad := range [][]byte{nil, {0xFF}, {0xFF, 0}, {0xFF, 7} /* 140 ms */} {
		if _, err := PacketSamples(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("% x: got %v, want ErrInvalid", bad, err)
		}
	}
}

func TestHead(t *testing.T) {
	// The header libopus writes for 48 kHz stereo.
	raw := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0x38, 0x01, 0x80, 0xBB, 0, 0, 0, 0, 0}
	h, err := ParseHead(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h.Channels != 2 || h.PreSkip != 312 || h.InputSampleRate != 48000 || h.OutputGain != 0 || h.MappingFamily != 0 {
		t.Errorf("parsed %+v", h)
	}
	if got := h.Marshal(); !bytes.Equal(got, raw) {
		t.Errorf("marshalled % x, want % x", got, raw)
	}
	surround := Head{Channels: 6, PreSkip: 312, InputSampleRate: 48000, OutputGain: -256, MappingFamily: 1, Mapping: []byte{4, 2, 0, 4, 1, 2, 3, 5}}
	back, err := ParseHead(surround.Marshal())
	if err != nil || back.Channels != 6 || back.OutputGain != -256 || back.MappingFamily != 1 || !bytes.Equal(back.Mapping, surround.Mapping) {
		t.Errorf("surround header came back as %+v (%v)", back, err)
	}
	for name, bad := range map[string][]byte{
		"short":           raw[:18],
		"magic":           append([]byte("OpusTags"), raw[8:]...),
		"version":         append(append([]byte{}, raw[:8]...), append([]byte{0x10}, raw[9:]...)...),
		"no channels":     append(append([]byte{}, raw[:9]...), append([]byte{0}, raw[10:]...)...),
		"mapping missing": append(append([]byte{}, raw[:18]...), 1),
	} {
		if _, err := ParseHead(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
}
