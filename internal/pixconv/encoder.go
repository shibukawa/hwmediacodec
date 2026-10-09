package pixconv

import (
	"context"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Encoder takes packed RGB frames, converts them to NV12 and feeds an NV12
// backend encoder. It implements codec.Encoder.
type Encoder struct {
	inner  codec.Encoder
	format codec.PixelFormat
	width  int
	height int
	// nv12 is the converted picture; the backend copies it before Send
	// returns, so one buffer serves every frame.
	nv12   codec.Frame
	closed bool
}

// WrapEncoder returns an encoder for frames in format (RGBA or BGRA) of the
// given size on top of e, which must have been opened for NV12 input of
// the same size.
func WrapEncoder(e codec.Encoder, format codec.PixelFormat, width, height int) *Encoder {
	yRows, yBytes := codec.NV12.PlaneLayout(0, width, height)
	cRows, cBytes := codec.NV12.PlaneLayout(1, width, height)
	buf := make([]byte, yRows*yBytes+cRows*cBytes)
	return &Encoder{
		inner: e, format: format, width: width, height: height,
		nv12: codec.Frame{
			Width: width, Height: height, Format: codec.NV12,
			Planes:  [][]byte{buf[:yRows*yBytes], buf[yRows*yBytes:]},
			Strides: []int{yBytes, cBytes},
		},
	}
}

// Unwrap returns the backend encoder.
func (e *Encoder) Unwrap() codec.Encoder { return e.inner }

// Send implements codec.Encoder.
func (e *Encoder) Send(ctx context.Context, f *codec.Frame) error {
	if e.closed {
		return codec.ErrClosed
	}
	if err := e.check(f); err != nil {
		return err
	}
	RGBToNV12(e.nv12.Planes[0], e.nv12.Strides[0], e.nv12.Planes[1], e.nv12.Strides[1],
		f.Planes[0], f.Strides[0], e.width, e.height, e.format == codec.BGRA)
	e.nv12.PTS = f.PTS
	e.nv12.ForceKeyframe = f.ForceKeyframe
	return e.inner.Send(ctx, &e.nv12)
}

// check validates a frame against the encoder configuration.
func (e *Encoder) check(f *codec.Frame) error {
	switch {
	case f == nil:
		return fmt.Errorf("%w: nil frame", codec.ErrInvalidData)
	case f.Format != e.format:
		return fmt.Errorf("%w: frame format %s, encoder expects %s", codec.ErrInvalidData, f.Format, e.format)
	case f.Width != e.width || f.Height != e.height:
		return fmt.Errorf("%w: frame size %dx%d, encoder expects %dx%d", codec.ErrInvalidData, f.Width, f.Height, e.width, e.height)
	case len(f.Planes) != 1 || len(f.Strides) != 1:
		return fmt.Errorf("%w: %s frame needs 1 plane and 1 stride, got %d and %d", codec.ErrInvalidData, e.format, len(f.Planes), len(f.Strides))
	}
	rowBytes := 4 * e.width
	if f.Strides[0] < rowBytes {
		return fmt.Errorf("%w: plane 0 stride %d is smaller than its row of %d bytes", codec.ErrInvalidData, f.Strides[0], rowBytes)
	}
	if need := (e.height-1)*f.Strides[0] + rowBytes; len(f.Planes[0]) < need {
		return fmt.Errorf("%w: plane 0 holds %d bytes, need %d", codec.ErrInvalidData, len(f.Planes[0]), need)
	}
	return nil
}

// Receive implements codec.Encoder.
func (e *Encoder) Receive(ctx context.Context) (codec.Packet, error) {
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	return e.inner.Receive(ctx)
}

// Flush implements codec.Encoder.
func (e *Encoder) Flush(ctx context.Context) error {
	if e.closed {
		return codec.ErrClosed
	}
	return e.inner.Flush(ctx)
}

// Close implements codec.Encoder.
func (e *Encoder) Close() error {
	e.closed = true
	return e.inner.Close()
}
