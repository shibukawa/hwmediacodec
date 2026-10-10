package hwmediacodec

import (
	"context"
	"io"
	"time"
)

// PacketWriter receives encoded packets in decode order: a file, a
// segmenter, a network track. It is what encoders are drained into (see
// EncodeWriter and package capture).
type PacketWriter interface {
	WritePacket(p Packet) error
}

// PacketWriteCloser is a PacketWriter that is finished with Close (the
// sample tables of a file are written, the viewers disconnected).
type PacketWriteCloser interface {
	PacketWriter
	io.Closer
}

// PacketWriterFunc adapts a function to a PacketWriteCloser whose Close
// does nothing.
type PacketWriterFunc func(p Packet) error

// WritePacket calls f.
func (f PacketWriterFunc) WritePacket(p Packet) error { return f(p) }

// Close does nothing.
func (f PacketWriterFunc) Close() error { return nil }

// PacketReader hands out the access units of a stream in decode order: a
// demuxed track, a raw elementary stream. It is what decoders are fed from
// (see DecodeReader).
type PacketReader interface {
	// Codec of the pictures.
	Codec() Codec
	// TimeScale is the number of units per second of the packets' PTS and
	// DTS. Open the decoder with WithTimeScale(TimeScale()) so that the
	// frames carry the same clock.
	TimeScale() int32
	// ReadPacket returns the next access unit, whose PTS counts from the
	// start of the presentation. It returns io.EOF at the end of the
	// stream.
	ReadPacket() (Packet, error)
}

// PacketSeeker is a PacketReader that can be repositioned, which is what
// seeking and looping need.
type PacketSeeker interface {
	PacketReader
	// SeekKeyframe repositions the reader at the last keyframe whose
	// presentation time is not after t (at the first keyframe when t lies
	// before it) and returns that keyframe's presentation time. The next
	// ReadPacket returns the keyframe.
	SeekKeyframe(t time.Duration) (time.Duration, error)
	// Length returns the stream duration, or 0 when it is not known.
	Length() time.Duration
}

// HasHardware reports whether Probe lists a hardware engine for codec c in
// the given direction on this machine, that is, whether NewDecoder or
// NewEncoder can succeed without WithSoftwareFallback. Use it to choose
// another path (a software codec, another format) before trying.
func HasHardware(ctx context.Context, c Codec, dir Direction) bool {
	caps, err := Probe(ctx)
	if err != nil {
		return false
	}
	for _, cap := range caps {
		if cap.Codec == c && cap.Direction == dir && cap.Hardware {
			return true
		}
	}
	return false
}
