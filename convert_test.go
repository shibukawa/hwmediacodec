package hwmediacodec

import (
	"context"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/pixconv"
	"github.com/shibukawa/hwmediacodec/internal/reorder"
)

// fakeEncoder stands in for a backend encoder.
type fakeEncoder struct{ codec.Encoder }

// fakeDecoder stands in for a backend decoder; ordered tells whether it
// returns frames in display order by itself.
type fakeDecoder struct {
	codec.Decoder
	ordered bool
}

func (d *fakeDecoder) OutputsDisplayOrder() bool { return d.ordered }

// fakeBackend accepts the listed pixel formats and records what it was
// asked for.
type fakeBackend struct {
	namedBackend
	accepts []codec.PixelFormat
	failure error // returned instead of ErrUnsupported for NV12 requests
	ordered bool  // its decoders reorder natively
	asked   []codec.EncoderConfig
	opened  *fakeEncoder

	askedDec  []codec.DecoderConfig
	openedDec *fakeDecoder
}

func (b *fakeBackend) NewDecoder(_ context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	b.askedDec = append(b.askedDec, cfg)
	for _, f := range b.accepts {
		if f == cfg.OutputFormat {
			b.openedDec = &fakeDecoder{ordered: b.ordered}
			return b.openedDec, nil
		}
	}
	if b.failure != nil && cfg.OutputFormat == NV12 {
		return nil, b.failure
	}
	return nil, &UnsupportedError{Backend: b.Name(), Codec: cfg.Codec, Direction: Decode, Reason: "output format " + cfg.OutputFormat.String()}
}

func (b *fakeBackend) NewEncoder(_ context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	b.asked = append(b.asked, cfg)
	for _, f := range b.accepts {
		if f == cfg.InputFormat {
			b.opened = &fakeEncoder{}
			return b.opened, nil
		}
	}
	if b.failure != nil && cfg.InputFormat == NV12 {
		return nil, b.failure
	}
	return nil, &UnsupportedError{Backend: b.Name(), Codec: cfg.Codec, Direction: Encode, Reason: "input format " + cfg.InputFormat.String()}
}

// TestNewEncoderConvertsRGB checks where the conversion to NV12 goes: in
// front of a backend that takes NV12 only, and nowhere else.
func TestNewEncoderConvertsRGB(t *testing.T) {
	ctx := context.Background()
	cfg := codec.EncoderConfig{Codec: H264, Width: 64, Height: 48, InputFormat: RGBA, TimeScale: DefaultTimeScale, RateControl: VBR}

	t.Run("nv12 only backend", func(t *testing.T) {
		for _, format := range []PixelFormat{RGBA, BGRA} {
			b := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}}
			c := cfg
			c.InputFormat = format
			enc, err := newEncoder(ctx, []codec.Backend{b}, c)
			if err != nil {
				t.Fatal(err)
			}
			conv, ok := enc.(*pixconv.Encoder)
			if !ok || conv.Unwrap() != codec.Encoder(b.opened) {
				t.Fatalf("%s: got %T, want the conversion in front of the backend encoder", format, enc)
			}
			if len(b.asked) != 2 || b.asked[0].InputFormat != format || b.asked[0].BT709 {
				t.Fatalf("%s: backend was asked %+v, want the caller's format first", format, b.asked)
			}
			if got := b.asked[1]; got.InputFormat != NV12 || !got.BT709 || got.Width != 64 || got.Height != 48 {
				t.Errorf("%s: second request %+v, want NV12 declared as BT.709", format, got)
			}
		}
	})

	t.Run("native rgb backend", func(t *testing.T) {
		b := &fakeBackend{namedBackend: "native", accepts: []PixelFormat{NV12, RGBA}}
		enc, err := newEncoder(ctx, []codec.Backend{b}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if enc != Encoder(b.opened) || len(b.asked) != 1 || b.asked[0].BT709 {
			t.Errorf("got %T after %d requests, want the backend encoder as is", enc, len(b.asked))
		}
	})

	t.Run("nv12 input is passed through", func(t *testing.T) {
		b := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}}
		c := cfg
		c.InputFormat = NV12
		enc, err := newEncoder(ctx, []codec.Backend{b}, c)
		if err != nil {
			t.Fatal(err)
		}
		if enc != Encoder(b.opened) || len(b.asked) != 1 || b.asked[0].BT709 {
			t.Errorf("got %T after %d requests, want the backend encoder as is", enc, len(b.asked))
		}
	})

	t.Run("backend order wins", func(t *testing.T) {
		first := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}}
		second := &fakeBackend{namedBackend: "native", accepts: []PixelFormat{NV12, RGBA}}
		enc, err := newEncoder(ctx, []codec.Backend{first, second}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if conv, ok := enc.(*pixconv.Encoder); !ok || conv.Unwrap() != codec.Encoder(first.opened) || len(second.asked) != 0 {
			t.Errorf("got %T, second backend asked %d times; want the first backend with the conversion", enc, len(second.asked))
		}
	})

	t.Run("unavailable backend is skipped", func(t *testing.T) {
		first := &fakeBackend{namedBackend: "absent"}
		second := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}}
		enc, err := newEncoder(ctx, []codec.Backend{first, second}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if conv, ok := enc.(*pixconv.Encoder); !ok || conv.Unwrap() != codec.Encoder(second.opened) {
			t.Errorf("got %T, want the second backend with the conversion", enc)
		}
	})

	t.Run("no backend", func(t *testing.T) {
		b := &fakeBackend{namedBackend: "absent"}
		_, err := newEncoder(ctx, []codec.Backend{b}, cfg)
		var u *UnsupportedError
		if !errors.As(err, &u) || u.Backend != "absent" || u.Reason != "input format nv12" {
			t.Errorf("err = %v, want the backend's reason for refusing NV12", err)
		}
		if _, err := newEncoder(ctx, nil, cfg); !errors.Is(err, ErrUnsupported) {
			t.Errorf("err = %v, want ErrUnsupported without backends", err)
		}
	})

	t.Run("backend failure", func(t *testing.T) {
		boom := &BackendError{Backend: "broken", Op: "open", Status: -1}
		first := &fakeBackend{namedBackend: "broken", failure: boom}
		second := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}}
		if _, err := newEncoder(ctx, []codec.Backend{first, second}, cfg); err != error(boom) {
			t.Errorf("err = %v, want the first backend's failure", err)
		}
		if len(second.asked) != 0 {
			t.Error("a backend failure must not fall through to the next backend")
		}
	})
}

// TestNewDecoderConvertsRGB checks where the conversion from NV12 goes:
// behind a backend that returns NV12 only, outside the reorder layer, and
// nowhere else.
func TestNewDecoderConvertsRGB(t *testing.T) {
	ctx := context.Background()
	cfg := codec.DecoderConfig{Codec: H264, OutputFormat: RGBA, TimeScale: DefaultTimeScale, DisplayOrder: true}

	t.Run("nv12 only backend", func(t *testing.T) {
		for _, format := range []PixelFormat{RGBA, BGRA} {
			b := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}}
			c := cfg
			c.OutputFormat = format
			dec, err := newDecoder(ctx, []codec.Backend{b}, c)
			if err != nil {
				t.Fatal(err)
			}
			conv, ok := dec.(*pixconv.Decoder)
			if !ok {
				t.Fatalf("%s: got %T, want the conversion on top", format, dec)
			}
			ordered, ok := conv.Unwrap().(*reorder.Decoder)
			if !ok || ordered.Unwrap() != codec.Decoder(b.openedDec) {
				t.Fatalf("%s: the conversion sits on %T, want the reorder layer on the backend decoder", format, conv.Unwrap())
			}
			if len(b.askedDec) != 2 || b.askedDec[0].OutputFormat != format || b.askedDec[1].OutputFormat != NV12 || !b.askedDec[1].DisplayOrder {
				t.Errorf("%s: backend was asked %+v, want the caller's format, then NV12", format, b.askedDec)
			}
		}
	})

	t.Run("decode order", func(t *testing.T) {
		b := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}}
		c := cfg
		c.DisplayOrder = false
		dec, err := newDecoder(ctx, []codec.Backend{b}, c)
		if err != nil {
			t.Fatal(err)
		}
		if conv, ok := dec.(*pixconv.Decoder); !ok || conv.Unwrap() != codec.Decoder(b.openedDec) {
			t.Errorf("got %T, want the conversion directly on the backend decoder", dec)
		}
	})

	t.Run("backend that reorders", func(t *testing.T) {
		b := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}, ordered: true}
		dec, err := newDecoder(ctx, []codec.Backend{b}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		conv, ok := dec.(*pixconv.Decoder)
		if !ok || conv.Unwrap() != codec.Decoder(b.openedDec) || !conv.OutputsDisplayOrder() {
			t.Errorf("got %T, want the conversion directly on the backend decoder", dec)
		}
	})

	t.Run("native rgb backend", func(t *testing.T) {
		b := &fakeBackend{namedBackend: "native", accepts: []PixelFormat{NV12, RGBA}, ordered: true}
		dec, err := newDecoder(ctx, []codec.Backend{b}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if dec != Decoder(b.openedDec) || len(b.askedDec) != 1 {
			t.Errorf("got %T after %d requests, want the backend decoder as is", dec, len(b.askedDec))
		}
	})

	t.Run("nv12 output is passed through", func(t *testing.T) {
		b := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}, ordered: true}
		c := cfg
		c.OutputFormat = NV12
		dec, err := newDecoder(ctx, []codec.Backend{b}, c)
		if err != nil {
			t.Fatal(err)
		}
		if dec != Decoder(b.openedDec) || len(b.askedDec) != 1 {
			t.Errorf("got %T after %d requests, want the backend decoder as is", dec, len(b.askedDec))
		}
	})

	t.Run("backend order wins", func(t *testing.T) {
		first := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}, ordered: true}
		second := &fakeBackend{namedBackend: "native", accepts: []PixelFormat{NV12, RGBA}}
		dec, err := newDecoder(ctx, []codec.Backend{first, second}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if conv, ok := dec.(*pixconv.Decoder); !ok || conv.Unwrap() != codec.Decoder(first.openedDec) || len(second.askedDec) != 0 {
			t.Errorf("got %T, second backend asked %d times; want the first backend with the conversion", dec, len(second.askedDec))
		}
	})

	t.Run("no backend", func(t *testing.T) {
		absent := &fakeBackend{namedBackend: "absent"}
		nv12 := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}, ordered: true}
		dec, err := newDecoder(ctx, []codec.Backend{absent, nv12}, cfg)
		if conv, ok := dec.(*pixconv.Decoder); err != nil || !ok || conv.Unwrap() != codec.Decoder(nv12.openedDec) {
			t.Errorf("got %T, %v; want the second backend with the conversion", dec, err)
		}
		_, err = newDecoder(ctx, []codec.Backend{absent}, cfg)
		var u *UnsupportedError
		if !errors.As(err, &u) || u.Backend != "absent" || u.Reason != "output format nv12" {
			t.Errorf("err = %v, want the backend's reason for refusing NV12", err)
		}
		if _, err := newDecoder(ctx, nil, cfg); !errors.Is(err, ErrUnsupported) {
			t.Errorf("err = %v, want ErrUnsupported without backends", err)
		}
	})

	t.Run("backend failure", func(t *testing.T) {
		boom := &BackendError{Backend: "broken", Op: "open", Status: -1}
		first := &fakeBackend{namedBackend: "broken", failure: boom}
		second := &fakeBackend{namedBackend: "nv12only", accepts: []PixelFormat{NV12}}
		if _, err := newDecoder(ctx, []codec.Backend{first, second}, cfg); err != error(boom) {
			t.Errorf("err = %v, want the first backend's failure", err)
		}
		if len(second.askedDec) != 0 {
			t.Error("a backend failure must not fall through to the next backend")
		}
	})
}
