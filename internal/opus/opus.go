// Package opus reads and writes the headers around Opus packets: the TOC
// byte that gives a packet's duration (RFC 6716, section 3.1) and the
// identification header "OpusHead" (RFC 7845, section 5.1). It contains
// no codec.
package opus

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// SampleRate is the rate Opus decodes to and counts its timestamps in.
const SampleRate = 48000

// MaxPacketSamples is the longest packet: 120 ms.
const MaxPacketSamples = 5760

// ErrInvalid reports a packet or header that cannot be parsed.
var ErrInvalid = errors.New("opus: invalid data")

// FrameSamples returns the number of samples at 48 kHz of one frame of the
// packet whose first byte is toc.
func FrameSamples(toc byte) int {
	config := toc >> 3
	switch {
	case config < 12: // SILK: 10, 20, 40, 60 ms
		return [...]int{480, 960, 1920, 2880}[config&3]
	case config < 16: // hybrid: 10, 20 ms
		return [...]int{480, 960}[config&1]
	}
	return 120 << (config & 3) // CELT: 2.5, 5, 10, 20 ms
}

// PacketFrames returns the number of frames in the packet.
func PacketFrames(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, fmt.Errorf("%w: empty packet", ErrInvalid)
	}
	switch p[0] & 3 {
	case 0:
		return 1, nil
	case 1, 2:
		return 2, nil
	}
	if len(p) < 2 {
		return 0, fmt.Errorf("%w: packet has no frame count byte", ErrInvalid)
	}
	n := int(p[1] & 0x3F)
	if n == 0 {
		return 0, fmt.Errorf("%w: packet with zero frames", ErrInvalid)
	}
	return n, nil
}

// PacketSamples returns the number of samples per channel at 48 kHz the
// packet decodes to.
func PacketSamples(p []byte) (int, error) {
	frames, err := PacketFrames(p)
	if err != nil {
		return 0, err
	}
	n := frames * FrameSamples(p[0])
	if n > MaxPacketSamples {
		return 0, fmt.Errorf("%w: packet of %d samples is longer than 120 ms", ErrInvalid, n)
	}
	return n, nil
}

// Head is the identification header of an Opus stream.
type Head struct {
	Channels int
	// PreSkip is the number of samples at 48 kHz to drop from the start of
	// the decoded stream.
	PreSkip int
	// InputSampleRate is the rate of the original recording; it does not
	// change how the stream decodes.
	InputSampleRate int
	// OutputGain is a gain to apply when decoding, in 1/256 dB.
	OutputGain int16
	// MappingFamily is 0 for mono and stereo. For other families Mapping
	// holds the channel mapping table (stream count, coupled count, one
	// index per channel).
	MappingFamily uint8
	Mapping       []byte
}

const headMagic = "OpusHead"

// ParseHead parses an identification header.
func ParseHead(b []byte) (Head, error) {
	var h Head
	if len(b) < 19 || string(b[:8]) != headMagic {
		return h, fmt.Errorf("%w: not an OpusHead header", ErrInvalid)
	}
	if b[8]>>4 != 0 {
		return h, fmt.Errorf("%w: OpusHead version %d is not supported", ErrInvalid, b[8])
	}
	h.Channels = int(b[9])
	h.PreSkip = int(binary.LittleEndian.Uint16(b[10:]))
	h.InputSampleRate = int(binary.LittleEndian.Uint32(b[12:]))
	h.OutputGain = int16(binary.LittleEndian.Uint16(b[16:]))
	h.MappingFamily = b[18]
	if h.Channels == 0 {
		return h, fmt.Errorf("%w: OpusHead with zero channels", ErrInvalid)
	}
	if h.MappingFamily != 0 {
		if len(b) < 21+h.Channels {
			return h, fmt.Errorf("%w: OpusHead channel mapping is truncated", ErrInvalid)
		}
		h.Mapping = append([]byte(nil), b[19:21+h.Channels]...)
	}
	return h, nil
}

// Marshal builds the identification header.
func (h Head) Marshal() []byte {
	b := make([]byte, 19, 19+len(h.Mapping))
	copy(b, headMagic)
	b[8] = 1
	b[9] = byte(h.Channels)
	binary.LittleEndian.PutUint16(b[10:], uint16(h.PreSkip))
	binary.LittleEndian.PutUint32(b[12:], uint32(h.InputSampleRate))
	binary.LittleEndian.PutUint16(b[16:], uint16(h.OutputGain))
	b[18] = h.MappingFamily
	if h.MappingFamily != 0 {
		b = append(b, h.Mapping...)
	}
	return b
}
