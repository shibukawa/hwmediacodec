package aac

import (
	"bytes"
	"testing"
)

func TestAppendADTSSizeOverflow(t *testing.T) {
	cfg := Config{ObjectType: ObjectTypeLC, SampleRate: 48000, ChannelConfig: 2}
	for _, size := range []int{-1, maxADTSFrame - ADTSHeaderSize + 1, int(^uint(0) >> 1)} {
		if _, err := AppendADTS(nil, cfg, size); err == nil {
			t.Fatalf("accepted payload size %d", size)
		}
	}
	if _, err := AppendADTS(nil, cfg, maxADTSFrame-ADTSHeaderSize); err != nil {
		t.Fatalf("maximum payload: %v", err)
	}
}

func TestESDescriptorBoundaries(t *testing.T) {
	asc := []byte{0x11, 0x90}
	dsi := append([]byte{5, byte(len(asc))}, asc...)
	dcBody := append(make([]byte, 13), dsi...)
	dc := append([]byte{4, byte(len(dcBody))}, dcBody...)
	for _, flags := range []byte{0, 0x20, 0x40, 0x80, 0xe0} {
		body := []byte{0, 1, flags}
		if flags&0x80 != 0 {
			body = append(body, 0, 2)
		}
		if flags&0x40 != 0 {
			body = append(body, 3, 'u', 'r', 'l')
		}
		if flags&0x20 != 0 {
			body = append(body, 0, 3)
		}
		body = append(body, dc...)
		b := append([]byte{3, byte(len(body))}, body...)
		if got := ConfigFromESDescriptor(b); !bytes.Equal(got, asc) {
			t.Fatalf("flags %#x: config %x, want %x", flags, got, asc)
		}
	}
	for _, bad := range [][]byte{
		{5, 3, 0x11, 0x90},                                               // truncated declared payload
		{5, 0x80, 0x80, 0x80, 0x82, 0x11, 0x90},                          // unterminated size
		{3, 3, 0, 1, 0, 5, 2, 0x11, 0x90},                                // DSI outside the ES descriptor
		{4, 13, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5, 2, 0x11, 0x90}, // DSI outside decoder config
		{3, 3, 0, 1, 0x40, 0, 5, 2, 0x11, 0x90},                          // URL length outside the descriptor
	} {
		if got := ConfigFromESDescriptor(bad); got != nil {
			t.Fatalf("accepted malformed descriptor %x as %x", bad, got)
		}
	}
}
