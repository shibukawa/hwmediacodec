package hwmediacodec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/pixconv"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// fakeEncoder stands in for a backend encoder.
type fakeEncoder struct{ codec.Encoder }

// fakeBackend accepts the listed input formats and records what it was
// asked for.
type fakeBackend struct {
	namedBackend
	accepts []codec.PixelFormat
	failure error // returned instead of ErrUnsupported for NV12 requests
	asked   []codec.EncoderConfig
	opened  *fakeEncoder
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

// nv12Only hides a backend's own RGB input, which leaves it where Media
// Foundation, VA-API and Intel VPL are: NV12 or nothing.
type nv12Only struct{ codec.Backend }

func (b nv12Only) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	if cfg.InputFormat != NV12 {
		return nil, &UnsupportedError{Backend: b.Name(), Codec: cfg.Codec, Direction: Encode, Reason: "input format " + cfg.InputFormat.String() + " is not supported; use NV12"}
	}
	return b.Backend.NewEncoder(ctx, cfg)
}

// TestEncodeConvertedRGB runs the conversion in front of the hardware
// encoder of this machine and compares ffmpeg's RGB decode of the result
// with the source, as TestEncodeRGBAInput does for a backend's own RGB
// input. The stream has to declare BT.709, the matrix of the conversion:
// decoded with BT.601 instead the same frames score about 28 dB.
func TestEncodeConvertedRGB(t *testing.T) {
	const (
		width, height = 320, 240
		fps           = 30
		minRGBPSNR    = 35.0
	)
	testutil.RequireFFmpeg(t)
	ctx := context.Background()
	for _, c := range []Codec{H264, HEVC} {
		for _, format := range []PixelFormat{RGBA, BGRA} {
			t.Run(c.String()+"-"+format.String(), func(t *testing.T) {
				var wrapped []codec.Backend
				for _, b := range backends() {
					wrapped = append(wrapped, nv12Only{b})
				}
				enc, err := newEncoder(ctx, wrapped, codec.EncoderConfig{
					Codec: c, Width: width, Height: height, InputFormat: format,
					TimeScale: DefaultTimeScale, RateControl: VBR, FrameRate: fps, Bitrate: 1_500_000,
				})
				if errors.Is(err, ErrUnsupported) {
					t.Skipf("no %s encoder on this machine: %v", c, err)
				}
				if err != nil {
					t.Fatalf("newEncoder: %v", err)
				}
				defer enc.Close()
				if _, ok := enc.(*pixconv.Encoder); !ok {
					t.Fatalf("got %T, want the conversion in front of the backend encoder", enc)
				}

				src := testutil.GenerateRawFrames(t, format, width, height, 30)
				var stream bytes.Buffer
				packets := 0
				drain := func(end error) {
					for {
						p, err := enc.Receive(ctx)
						if err == end {
							return
						}
						if err != nil {
							t.Fatalf("Receive: %v", err)
						}
						stream.Write(p.Data)
						packets++
					}
				}
				for i, raw := range src {
					f := testutil.RawFrame(raw, format, width, height, int64(i)*int64(DefaultTimeScale)/fps)
					if err := enc.Send(ctx, f); err != nil {
						t.Fatalf("Send frame %d: %v", i, err)
					}
					drain(ErrAgain)
				}
				if err := enc.Flush(ctx); err != nil {
					t.Fatal(err)
				}
				drain(io.EOF)
				if packets != len(src) {
					t.Fatalf("got %d packets for %d frames", packets, len(src))
				}

				path := filepath.Join(t.TempDir(), "converted."+c.String())
				if err := os.WriteFile(path, stream.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				if cs := testutil.ProbeStreamField(t, path, c, "color_space"); cs != "bt709" {
					t.Errorf("stream declares colour matrix %q, want bt709", cs)
				}
				if r := testutil.ProbeStreamField(t, path, c, "color_range"); r != "tv" && r != "unknown" {
					t.Errorf("stream declares colour range %q, want video range", r)
				}
				got := testutil.ReferenceFrames(t, path, c, format, width, height)
				if len(got) != len(src) {
					t.Fatalf("ffmpeg decoded %d frames, want %d", len(got), len(src))
				}
				worst := 1e9
				for i := range src {
					worst = min(worst, testutil.BlockPSNR(src[i], got[i], width, height, 8))
				}
				t.Logf("worst %s 8x8-block PSNR %.2f dB over %d frames", format, worst, len(src))
				if worst < minRGBPSNR {
					t.Fatalf("worst PSNR %.2f dB is below %.0f dB", worst, minRGBPSNR)
				}
			})
		}
	}
}
