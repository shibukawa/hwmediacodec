package reorder

import (
	"context"
	"io"

	"github.com/shibukawa/hwmediacodec/bitstream/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Decoder wraps a decode-order backend decoder and returns its frames in
// display order. It implements codec.Decoder and codec.DisplayOrderer.
type Decoder struct {
	inner   codec.Decoder
	tracker *Tracker
	buf     buffer
	// flushing is set by Flush and cleared by Send; while set, buffered
	// frames are released in order without waiting for the reorder bound.
	flushing bool
	closed   bool
}

// Wrap returns a display-order decoder on top of d for codec c.
func Wrap(d codec.Decoder, c codec.Codec) *Decoder {
	return &Decoder{inner: d, tracker: NewTracker(c)}
}

// OutputsDisplayOrder implements codec.DisplayOrderer.
func (d *Decoder) OutputsDisplayOrder() bool { return true }

// Unwrap returns the backend decoder.
func (d *Decoder) Unwrap() codec.Decoder { return d.inner }

// Send implements codec.Decoder.
func (d *Decoder) Send(ctx context.Context, p codec.Packet) error {
	if d.closed {
		return codec.ErrClosed
	}
	d.flushing = false
	nals := annexb.Split(p.Data)
	if len(nals) == 0 {
		return d.inner.Send(ctx, p) // the backend reports ErrInvalidData
	}
	order, forward := d.tracker.Classify(nals)
	if !forward {
		return nil
	}
	codec.SetPacketOrder(&p, order)
	return d.inner.Send(ctx, p)
}

// Receive implements codec.Decoder.
func (d *Decoder) Receive(ctx context.Context) (*codec.Frame, error) {
	if d.closed {
		return nil, codec.ErrClosed
	}
	for {
		if f := d.buf.pop(d.flushing); f != nil {
			return f, nil
		}
		f, err := d.inner.Receive(ctx)
		if err != nil {
			if err == io.EOF && d.buf.len() > 0 {
				return d.buf.pop(true), nil
			}
			return nil, err
		}
		d.buf.push(f)
	}
}

// Flush implements codec.Decoder. After it, Receive returns every buffered
// frame in display order and then io.EOF.
func (d *Decoder) Flush(ctx context.Context) error {
	if d.closed {
		return codec.ErrClosed
	}
	err := d.inner.Flush(ctx)
	d.flushing = true
	d.tracker.Reset()
	return err
}

// Close implements codec.Decoder.
func (d *Decoder) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	d.buf.release()
	return d.inner.Close()
}
