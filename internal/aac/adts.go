package aac

import "fmt"

// ADTSHeaderSize is the size of an ADTS header without CRC; MaxADTSHeaderSize
// includes the CRC.
const (
	ADTSHeaderSize    = 7
	MaxADTSHeaderSize = 9
	// maxADTSFrame is the largest frame the 13-bit length field can express.
	maxADTSFrame = 1<<13 - 1
)

// ADTSHeader is a parsed ADTS frame header.
type ADTSHeader struct {
	ObjectType    int
	SampleRate    int
	ChannelConfig int
	// HeaderSize is 7, or 9 when the frame carries a CRC.
	HeaderSize int
	// FrameSize is the size of the whole frame, header included.
	FrameSize int
	// Blocks is the number of raw data blocks (access units) in the frame;
	// almost every stream has one per frame.
	Blocks int
}

// Config returns the AudioSpecificConfig describing the stream of the frame.
func (h ADTSHeader) Config() []byte {
	idx := 0
	for i, r := range sampleRates {
		if r == h.SampleRate {
			idx = i
		}
	}
	v := uint16(h.ObjectType)<<11 | uint16(idx)<<7 | uint16(h.ChannelConfig)<<3
	return []byte{byte(v >> 8), byte(v)}
}

// ParseADTS parses the header at the start of b, which must hold at least
// ADTSHeaderSize bytes.
func ParseADTS(b []byte) (ADTSHeader, error) {
	var h ADTSHeader
	if len(b) < ADTSHeaderSize {
		return h, fmt.Errorf("%w: ADTS header is truncated", ErrInvalid)
	}
	if b[0] != 0xFF || b[1]&0xF6 != 0xF0 {
		return h, fmt.Errorf("%w: no ADTS sync word", ErrInvalid)
	}
	h.HeaderSize = ADTSHeaderSize
	if b[1]&1 == 0 {
		h.HeaderSize = MaxADTSHeaderSize
	}
	h.ObjectType = int(b[2]>>6) + 1
	idx := int(b[2] >> 2 & 0xF)
	if idx >= len(sampleRates) {
		return h, fmt.Errorf("%w: reserved sampling frequency index %d in ADTS header", ErrInvalid, idx)
	}
	h.SampleRate = sampleRates[idx]
	h.ChannelConfig = int(b[2]&1)<<2 | int(b[3]>>6)
	h.FrameSize = int(b[3]&3)<<11 | int(b[4])<<3 | int(b[5]>>5)
	h.Blocks = int(b[6]&3) + 1
	if h.FrameSize < h.HeaderSize {
		return h, fmt.Errorf("%w: ADTS frame of %d bytes is shorter than its header", ErrInvalid, h.FrameSize)
	}
	return h, nil
}

// AppendADTS appends the 7-byte ADTS header for one access unit of
// payloadSize bytes of the stream c describes.
func AppendADTS(dst []byte, c Config, payloadSize int) ([]byte, error) {
	if c.ObjectType < 1 || c.ObjectType > 4 {
		return dst, fmt.Errorf("aac: ADTS cannot carry audio object type %d", c.ObjectType)
	}
	idx := -1
	for i, r := range sampleRates {
		if r == c.SampleRate {
			idx = i
		}
	}
	if idx < 0 {
		return dst, fmt.Errorf("aac: ADTS cannot carry the sample rate %d", c.SampleRate)
	}
	if c.ChannelConfig < 1 || c.ChannelConfig > 7 {
		return dst, fmt.Errorf("aac: ADTS cannot carry channel configuration %d", c.ChannelConfig)
	}
	if payloadSize < 0 || payloadSize > maxADTSFrame-ADTSHeaderSize {
		return dst, fmt.Errorf("aac: an access unit of %d bytes does not fit an ADTS frame", payloadSize)
	}
	size := payloadSize + ADTSHeaderSize
	return append(dst,
		0xFF,
		0xF1, // MPEG-4, layer 0, no CRC
		byte(c.ObjectType-1)<<6|byte(idx)<<2|byte(c.ChannelConfig>>2),
		byte(c.ChannelConfig&3)<<6|byte(size>>11),
		byte(size>>3),
		byte(size&7)<<5|0x1F, // buffer fullness 0x7FF: variable bitrate
		0xFC,
	), nil
}
