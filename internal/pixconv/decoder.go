package pixconv

import (
	"bytes"
	"context"
	"sync"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
)

// matrixUnspecified is matrix_coeffs 2: the stream does not name a matrix.
const matrixUnspecified = 2

// Decoder turns the NV12 frames of a backend decoder into packed RGB
// frames. It implements codec.Decoder.
//
// The matrix and range of the conversion follow the video signal fields of
// the newest sequence parameter set sent to the decoder (see ColorFor).
// Frames still inside the decoder when a stream switches to parameter sets
// with another colour description are converted with the new one.
type Decoder struct {
	inner  codec.Decoder
	codec  codec.Codec
	format codec.PixelFormat

	sps       []byte // the last SPS NAL unit seen
	matrix    uint8
	fullRange bool

	// pool holds the pixel buffers of released frames. Frames may be
	// released from any goroutine, also after Close.
	pool   sync.Pool
	closed bool
}

// WrapDecoder returns a decoder that delivers frames in format (RGBA or
// BGRA) on top of d, which must have been opened for NV12 output of codec c.
func WrapDecoder(d codec.Decoder, c codec.Codec, format codec.PixelFormat) *Decoder {
	return &Decoder{inner: d, codec: c, format: format, matrix: matrixUnspecified}
}

// Unwrap returns the decoder underneath.
func (d *Decoder) Unwrap() codec.Decoder { return d.inner }

// OutputsDisplayOrder implements codec.DisplayOrderer for the decoder
// underneath.
func (d *Decoder) OutputsDisplayOrder() bool {
	o, ok := d.inner.(codec.DisplayOrderer)
	return ok && o.OutputsDisplayOrder()
}

// Send implements codec.Decoder.
func (d *Decoder) Send(ctx context.Context, p codec.Packet) error {
	if d.closed {
		return codec.ErrClosed
	}
	d.readColor(p.Data)
	return d.inner.Send(ctx, p)
}

// readColor picks up the colour description of the sequence parameter sets
// in an access unit. A parameter set that does not parse leaves the
// description as it was; the backend reports the broken stream.
func (d *Decoder) readColor(au []byte) {
	if d.codec != codec.H264 && d.codec != codec.HEVC {
		return
	}
	for _, nal := range annexb.Split(au) {
		if !d.isSPS(nal) || bytes.Equal(nal, d.sps) {
			continue
		}
		switch d.codec {
		case codec.H264:
			s, err := h264.ParseSPS(nal)
			if err != nil {
				continue
			}
			d.matrix, d.fullRange = signal(s.VUIPresent && s.VUI.VideoSignalTypePresent, s.VUI.ColourDescriptionPresent, s.VUI.MatrixCoefficients, s.VUI.VideoFullRange)
		case codec.HEVC:
			s, err := hevc.ParseSPS(nal)
			if err != nil {
				continue
			}
			d.matrix, d.fullRange = signal(s.VUIPresent && s.VUI.VideoSignalTypePresent, s.VUI.ColourDescriptionPresent, s.VUI.MatrixCoeffs, s.VUI.VideoFullRange)
		}
		d.sps = append(d.sps[:0], nal...)
	}
}

func (d *Decoder) isSPS(nal []byte) bool {
	if d.codec == codec.H264 {
		_, typ, ok := h264.NALHeader(nal)
		return ok && typ == h264.NALSPS
	}
	return hevc.Type(nal) == hevc.NALSPS
}

// signal reduces the video signal fields of a VUI to the matrix and range.
func signal(present, described bool, matrix uint8, fullRange bool) (uint8, bool) {
	if !present {
		return matrixUnspecified, false
	}
	if !described {
		matrix = matrixUnspecified
	}
	return matrix, fullRange
}

// Receive implements codec.Decoder.
func (d *Decoder) Receive(ctx context.Context) (*codec.Frame, error) {
	if d.closed {
		return nil, codec.ErrClosed
	}
	f, err := d.inner.Receive(ctx)
	if err != nil {
		return nil, err
	}
	defer f.Release()
	return d.convert(f), nil
}

// convert returns the picture of an NV12 frame as a packed RGB frame with
// rows of exactly 4*Width bytes.
func (d *Decoder) convert(f *codec.Frame) *codec.Frame {
	stride := 4 * f.Width
	size := stride * f.Height
	var pix []byte
	if b, ok := d.pool.Get().(*[]byte); ok && cap(*b) >= size {
		pix = (*b)[:size]
	} else {
		pix = make([]byte, size)
	}
	NV12ToRGB(pix, stride, f.Planes[0], f.Strides[0], f.Planes[1], f.Strides[1], f.Width, f.Height,
		ColorFor(d.matrix, d.fullRange, f.Width, f.Height), d.format == codec.BGRA)
	out := &codec.Frame{
		Width: f.Width, Height: f.Height, Format: d.format,
		Planes: [][]byte{pix}, Strides: []int{stride}, PTS: f.PTS,
	}
	codec.SetFrameOrder(out, codec.FrameOrder(f))
	codec.SetRelease(out, func() { d.pool.Put(&pix) })
	return out
}

// Flush implements codec.Decoder.
func (d *Decoder) Flush(ctx context.Context) error {
	if d.closed {
		return codec.ErrClosed
	}
	return d.inner.Flush(ctx)
}

// Close implements codec.Decoder.
func (d *Decoder) Close() error {
	d.closed = true
	return d.inner.Close()
}
