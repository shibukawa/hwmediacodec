// Package av1 parses the parts of an AV1 bitstream that a hardware decoder
// backend needs in order to describe a stream to the operating system: the
// OBU framing of a temporal unit (Section 5.3 of the AV1 specification),
// the sequence header (5.5), the leading fields of a frame header (5.9) and
// the ISOBMFF AV1CodecConfigurationRecord ("av1C"). It decodes nothing.
package av1

import (
	"errors"
	"fmt"
)

// OBUType is the obu_type field of an OBU header.
type OBUType uint8

// OBU types (Section 6.2.2).
const (
	OBUSequenceHeader       OBUType = 1
	OBUTemporalDelimiter    OBUType = 2
	OBUFrameHeader          OBUType = 3
	OBUTileGroup            OBUType = 4
	OBUMetadata             OBUType = 5
	OBUFrame                OBUType = 6
	OBURedundantFrameHeader OBUType = 7
	OBUTileList             OBUType = 8
	OBUPadding              OBUType = 15
)

func (t OBUType) String() string {
	switch t {
	case OBUSequenceHeader:
		return "sequence header"
	case OBUTemporalDelimiter:
		return "temporal delimiter"
	case OBUFrameHeader:
		return "frame header"
	case OBUTileGroup:
		return "tile group"
	case OBUMetadata:
		return "metadata"
	case OBUFrame:
		return "frame"
	case OBURedundantFrameHeader:
		return "redundant frame header"
	case OBUTileList:
		return "tile list"
	case OBUPadding:
		return "padding"
	}
	return fmt.Sprintf("obu(%d)", uint8(t))
}

// OBU is one open bitstream unit of a temporal unit.
type OBU struct {
	Type OBUType
	// HasExtension reports whether the header carried an extension byte,
	// from which TemporalID and SpatialID come.
	HasExtension bool
	TemporalID   uint8
	SpatialID    uint8
	// Raw is the complete OBU as it appears in the stream (header, optional
	// extension byte, size field and payload); Payload is the payload alone.
	// Both alias the data given to Split.
	Raw     []byte
	Payload []byte
}

// ErrInvalid is wrapped by every error this package returns for malformed
// input.
var ErrInvalid = errors.New("av1: invalid bitstream")

// Split parses the OBUs of one temporal unit in the low-overhead bitstream
// format (Section 5.2), the format of IVF frames and ISOBMFF samples: every
// OBU carries obu_has_size_field=1, except that the last OBU may omit the
// size field and run to the end of data. The returned OBUs alias data.
func Split(data []byte) ([]OBU, error) {
	var out []OBU
	for off := 0; off < len(data); {
		start := off
		h := data[off]
		off++
		if h&0x80 != 0 {
			return nil, fmt.Errorf("%w: obu_forbidden_bit set at byte %d", ErrInvalid, start)
		}
		o := OBU{Type: OBUType(h >> 3 & 0xf), HasExtension: h&0x04 != 0}
		hasSize := h&0x02 != 0
		if o.HasExtension {
			if off >= len(data) {
				return nil, fmt.Errorf("%w: truncated OBU extension header at byte %d", ErrInvalid, start)
			}
			e := data[off]
			off++
			o.TemporalID = e >> 5
			o.SpatialID = e >> 3 & 0x3
		}
		size := len(data) - off
		if hasSize {
			v, n, ok := ReadLEB128(data[off:])
			if !ok {
				return nil, fmt.Errorf("%w: bad obu_size at byte %d", ErrInvalid, off)
			}
			off += n
			if v > uint64(len(data)-off) {
				return nil, fmt.Errorf("%w: obu_size %d exceeds the %d remaining bytes", ErrInvalid, v, len(data)-off)
			}
			size = int(v)
		}
		o.Payload = data[off : off+size]
		off += size
		o.Raw = data[start:off]
		out = append(out, o)
	}
	return out, nil
}

// ReadLEB128 decodes a leb128 value (Section 4.10.5) from the start of b and
// returns it with the number of bytes it occupied. ok is false when b ends
// inside the value or the value is longer than the eight bytes the format
// allows.
func ReadLEB128(b []byte) (value uint64, n int, ok bool) {
	for i := 0; i < 8 && i < len(b); i++ {
		value |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i]&0x80 == 0 {
			return value, i + 1, true
		}
	}
	return 0, 0, false
}

// AppendLEB128 appends v in leb128 form.
func AppendLEB128(dst []byte, v uint64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(dst, b)
		}
		dst = append(dst, b|0x80)
	}
}

// AppendOBU appends an OBU of type t with the given payload, without an
// extension header and with obu_has_size_field=1.
func AppendOBU(dst []byte, t OBUType, payload []byte) []byte {
	dst = append(dst, byte(t)<<3|0x02)
	dst = AppendLEB128(dst, uint64(len(payload)))
	return append(dst, payload...)
}
