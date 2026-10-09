package av1

import "fmt"

// FrameType is the frame_type field of a frame header.
type FrameType uint8

// Frame types (Section 6.8.2).
const (
	KeyFrame FrameType = iota
	InterFrame
	IntraOnlyFrame
	SwitchFrame
)

func (t FrameType) String() string {
	switch t {
	case KeyFrame:
		return "key"
	case InterFrame:
		return "inter"
	case IntraOnlyFrame:
		return "intra-only"
	case SwitchFrame:
		return "switch"
	}
	return fmt.Sprintf("frametype(%d)", uint8(t))
}

// FrameHeader holds the leading fields of an uncompressed frame header
// (Section 5.9.2): enough to tell a shown key frame, where decoding can
// start, from everything else.
type FrameHeader struct {
	ShowExistingFrame bool
	FrameToShowMapIdx uint8 // valid when ShowExistingFrame
	FrameType         FrameType
	ShowFrame         bool
}

// ParseFrameHeader reads the leading fields of the uncompressed header that
// begins the payload of a FRAME_HEADER, REDUNDANT_FRAME_HEADER or FRAME
// OBU. reducedStillPictureHeader is the flag of the stream's sequence
// header; such streams have no header bits for these fields and every
// frame is a shown key frame.
func ParseFrameHeader(payload []byte, reducedStillPictureHeader bool) (FrameHeader, error) {
	if reducedStillPictureHeader {
		return FrameHeader{FrameType: KeyFrame, ShowFrame: true}, nil
	}
	if len(payload) == 0 {
		return FrameHeader{}, fmt.Errorf("%w: empty frame header", ErrInvalid)
	}
	b := payload[0]
	if b&0x80 != 0 {
		return FrameHeader{ShowExistingFrame: true, FrameToShowMapIdx: b >> 4 & 7}, nil
	}
	return FrameHeader{FrameType: FrameType(b >> 5 & 3), ShowFrame: b&0x10 != 0}, nil
}

// TemporalUnit describes the OBUs of one temporal unit.
type TemporalUnit struct {
	OBUs []OBU
	// SequenceHeader is the sequence header OBU in the unit, or nil, and
	// Sequence its parsed form.
	SequenceHeader *OBU
	Sequence       *SequenceHeader
	// HasFrame reports whether the unit carries a frame, a frame header or
	// tile data, and so produces a shown frame.
	HasFrame bool
	// Keyframe reports whether the unit contains a shown key frame (or a
	// reduced still picture), which is where decoding can start.
	Keyframe bool
	// ShowExistingFrame reports whether the unit shows a previously decoded
	// frame instead of carrying a new one.
	ShowExistingFrame bool
}

// ParseTemporalUnit splits data into OBUs and classifies the unit. prev is
// the stream's current sequence header (nil before the first one); a
// sequence header inside the unit takes its place for the classification.
func ParseTemporalUnit(data []byte, prev *SequenceHeader) (*TemporalUnit, error) {
	obus, err := Split(data)
	if err != nil {
		return nil, err
	}
	if len(obus) == 0 {
		return nil, fmt.Errorf("%w: empty temporal unit", ErrInvalid)
	}
	tu := &TemporalUnit{OBUs: obus}
	seq := prev
	for i := range obus {
		o := &obus[i]
		switch o.Type {
		case OBUSequenceHeader:
			sh, err := ParseSequenceHeader(o.Payload)
			if err != nil {
				return nil, err
			}
			tu.SequenceHeader, tu.Sequence, seq = o, sh, sh
		case OBUFrame, OBUFrameHeader:
			tu.HasFrame = true
			if seq == nil {
				continue
			}
			fh, err := ParseFrameHeader(o.Payload, seq.ReducedStillPictureHeader)
			if err != nil {
				return nil, err
			}
			switch {
			case fh.ShowExistingFrame:
				tu.ShowExistingFrame = true
			case fh.FrameType == KeyFrame && fh.ShowFrame:
				tu.Keyframe = true
			}
		case OBUTileGroup:
			tu.HasFrame = true
		}
	}
	return tu, nil
}

// StripTemporalDelimiters returns the OBUs of a temporal unit without its
// temporal delimiter OBUs, concatenated. It returns data itself when there
// is nothing to strip.
func StripTemporalDelimiters(data []byte, obus []OBU) []byte {
	n := 0
	for _, o := range obus {
		if o.Type == OBUTemporalDelimiter {
			n += len(o.Raw)
		}
	}
	if n == 0 {
		return data
	}
	out := make([]byte, 0, len(data)-n)
	for _, o := range obus {
		if o.Type != OBUTemporalDelimiter {
			out = append(out, o.Raw...)
		}
	}
	return out
}
