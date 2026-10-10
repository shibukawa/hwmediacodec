package hwmediacodec

import (
	"context"
	"errors"
	"io"
)

// FrameReader is a source of raw frames: a decoder fed from a PacketReader
// (DecodeReader), or anything that produces or transforms pictures.
type FrameReader interface {
	// ReadFrame returns the next frame, or io.EOF at the end. The caller
	// owns the frame and must Release it.
	ReadFrame(ctx context.Context) (*Frame, error)
}

// FrameWriter consumes raw frames: an encoder drained into a PacketWriter
// (EncodeWriter), or anything that shows or stores pictures.
type FrameWriter interface {
	// WriteFrame consumes f before it returns; the frame stays the
	// caller's, who may release or reuse it afterwards.
	WriteFrame(ctx context.Context, f *Frame) error
}

// DecodeReader couples a PacketReader to a Decoder and hands out the
// decoded frames one at a time: the Send/Receive loop of the Decoder
// documentation, written once. It does not own the decoder; Close that
// when done.
//
// Open the decoder with WithTimeScale(src.TimeScale()) so that Frame.PTS is
// in the reader's units.
type DecodeReader struct {
	dec     Decoder
	src     PacketReader
	pending *Packet // read from src, not yet accepted by the decoder
	flushed bool    // src reached io.EOF and the decoder was flushed
	done    bool
}

// NewDecodeReader returns a reader of the frames dec decodes from src.
func NewDecodeReader(dec Decoder, src PacketReader) *DecodeReader {
	return &DecodeReader{dec: dec, src: src}
}

// ReadFrame implements FrameReader. Frames come in the decoder's output
// order (display order unless it was opened with WithDecodeOrder).
func (r *DecodeReader) ReadFrame(ctx context.Context) (*Frame, error) {
	if r.done {
		return nil, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := r.dec.Receive(ctx)
		switch {
		case err == nil:
			return f, nil
		case err != io.EOF && !errors.Is(err, ErrAgain):
			return nil, err
		case r.flushed:
			r.done = true
			return nil, io.EOF
		}
		// The decoder wants input. (It also answers io.EOF here right
		// after a Reset, until the next packet arrives.)
		if r.pending == nil {
			p, err := r.src.ReadPacket()
			if err == io.EOF {
				if err := r.dec.Flush(ctx); err != nil {
					return nil, err
				}
				r.flushed = true
				continue
			}
			if err != nil {
				return nil, err
			}
			r.pending = &p
		}
		err = r.dec.Send(ctx, *r.pending)
		if errors.Is(err, ErrAgain) {
			continue // its output queue is full: Receive first
		}
		if err != nil {
			return nil, err
		}
		r.pending = nil
	}
}

// Reset drops what the decoder holds and makes the reader start afresh
// from the PacketReader's current position, which must be a keyframe: call
// it after PacketSeeker.SeekKeyframe.
func (r *DecodeReader) Reset(ctx context.Context) error {
	r.pending, r.flushed, r.done = nil, false, false
	if err := r.dec.Flush(ctx); err != nil {
		return err
	}
	for {
		f, err := r.dec.Receive(ctx)
		if errors.Is(err, ErrAgain) || err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		f.Release()
	}
}

// EncodeWriter couples an Encoder to a PacketWriter: every frame written
// is encoded and the packets that become ready go to the writer, the
// Send/Receive loop of the Encoder documentation written once. It owns
// neither side; Flush it at the end, then close the encoder and the
// writer.
type EncodeWriter struct {
	enc     Encoder
	dst     PacketWriter
	packets int64
}

// NewEncodeWriter returns a frame writer that encodes with enc into dst.
func NewEncodeWriter(enc Encoder, dst PacketWriter) *EncodeWriter {
	return &EncodeWriter{enc: enc, dst: dst}
}

// WriteFrame implements FrameWriter. The pixel data is copied before it
// returns (see Encoder.Send).
func (w *EncodeWriter) WriteFrame(ctx context.Context, f *Frame) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := w.enc.Send(ctx, f)
		if errors.Is(err, ErrAgain) {
			// Too many packets are waiting: make room.
			if err := w.drain(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		return w.drain(ctx)
	}
}

// Flush makes the encoder emit the frames it is holding and writes their
// packets. The encoder can be reused afterwards; the next frame is a
// keyframe.
func (w *EncodeWriter) Flush(ctx context.Context) error {
	if err := w.enc.Flush(ctx); err != nil {
		return err
	}
	return w.drain(ctx)
}

// Packets is the number of packets written so far.
func (w *EncodeWriter) Packets() int64 { return w.packets }

func (w *EncodeWriter) drain(ctx context.Context) error {
	for {
		p, err := w.enc.Receive(ctx)
		if errors.Is(err, ErrAgain) || err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := w.dst.WritePacket(p); err != nil {
			return err
		}
		w.packets++
	}
}

// CopyFrames writes every frame of src to dst and releases it, until src
// reports io.EOF, and returns the number of frames copied. With a
// DecodeReader and an EncodeWriter this is a transcoder; Flush the
// EncodeWriter afterwards.
func CopyFrames(ctx context.Context, dst FrameWriter, src FrameReader) (int64, error) {
	var n int64
	for {
		f, err := src.ReadFrame(ctx)
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		err = dst.WriteFrame(ctx, f)
		f.Release()
		if err != nil {
			return n, err
		}
		n++
	}
}
