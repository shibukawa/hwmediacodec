package hwmediacodec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/pixconv"
	"github.com/shibukawa/hwmediacodec/internal/reorder"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
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

// nv12Only hides a backend's own RGB conversion, which leaves it where
// Media Foundation, VA-API and Intel VPL are: NV12 or nothing.
type nv12Only struct{ codec.Backend }

func (b nv12Only) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	if cfg.OutputFormat != NV12 {
		return nil, &UnsupportedError{Backend: b.Name(), Codec: cfg.Codec, Direction: Decode, Reason: "output format " + cfg.OutputFormat.String() + " is not supported; use NV12"}
	}
	return b.Backend.NewDecoder(ctx, cfg)
}

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

// taggedStream encodes a short B-frame test stream whose VUI names the
// given colour matrix ("" for none).
func taggedStream(t *testing.T, c Codec, width, height, frames int, matrix string) []byte {
	t.Helper()
	ffmpeg := testutil.RequireFFmpeg(t)
	enc, format, key, params := "libx264", "h264", "-x264-params", "bframes=2:keyint=25"
	if c == HEVC {
		enc, format, key, params = "libx265", "hevc", "-x265-params", "bframes=2:keyint=25:open-gop=0:log-level=error"
	}
	if !testutil.HasEncoder(t, enc) {
		t.Skipf("ffmpeg has no %s encoder", enc)
	}
	if matrix != "" {
		params += ":colormatrix=" + matrix
	}
	path := filepath.Join(t.TempDir(), "tagged."+format)
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p", "-c:v", enc, "-preset", "veryfast", key, params, "-f", format, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg generate failed: %v\n%s", err, out)
	}
	return testutil.ReadFile(t, path)
}

type rgbFrame struct {
	pix []byte
	pts int64
}

// decodeRGB decodes an Annex-B stream and returns copies of the frames,
// which must be tightly packed pictures in format f.
func decodeRGB(t *testing.T, dec Decoder, c Codec, stream []byte, f PixelFormat, width, height int) []rgbFrame {
	t.Helper()
	ctx := context.Background()
	var out []rgbFrame
	drain := func(end error) {
		for {
			fr, err := dec.Receive(ctx)
			if err == end || (end == ErrAgain && errors.Is(err, ErrAgain)) {
				return
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			if fr.Format != f || fr.Width != width || fr.Height != height || len(fr.Planes) != 1 || fr.Strides[0] != 4*width || len(fr.Planes[0]) != 4*width*height {
				t.Fatalf("frame is %s %dx%d with %d planes, strides %v", fr.Format, fr.Width, fr.Height, len(fr.Planes), fr.Strides)
			}
			out = append(out, rgbFrame{pix: append([]byte{}, fr.Planes[0]...), pts: fr.PTS})
			fr.Release()
		}
	}
	r := annexb.NewReader(bytes.NewReader(stream), c)
	for i := 0; ; i++ {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := dec.Send(ctx, Packet{Data: au, PTS: int64(i) * 3000}); err != nil {
			t.Fatalf("Send packet %d: %v", i, err)
		}
		drain(ErrAgain)
	}
	if err := dec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	drain(io.EOF)
	return out
}

// TestDecodeConvertedRGB runs the conversion behind the hardware decoder of
// this machine and compares the frames with the backend's own RGB output
// and, where ffmpeg's conversion uses the same matrix, with ffmpeg's. The
// streams cover the three ways the matrix is chosen: named by the stream,
// and not named for a small and for a large picture. A wrong matrix scores
// about 24 dB here.
func TestDecodeConvertedRGB(t *testing.T) {
	const minPSNR = 30.0
	testutil.RequireFFmpeg(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		width, height int
		matrix        string
		ffmpeg        bool // ffmpeg converts with the same matrix
	}{
		{"untagged-sd", 320, 240, "", true},
		{"bt709-sd", 320, 240, "bt709", true},
		{"smpte170m-hd", 1280, 720, "smpte170m", true},
		{"untagged-hd", 1280, 720, "", false}, // ffmpeg assumes BT.601 at any size
	} {
		for _, c := range []Codec{H264, HEVC} {
			t.Run(c.String()+"-"+tc.name, func(t *testing.T) {
				const frames = 12
				stream := taggedStream(t, c, tc.width, tc.height, frames, tc.matrix)
				var wrapped []codec.Backend
				for _, b := range backends() {
					wrapped = append(wrapped, nv12Only{b})
				}
				decoded := map[PixelFormat][]rgbFrame{}
				for _, f := range []PixelFormat{RGBA, BGRA} {
					cfg := codec.DecoderConfig{Codec: c, OutputFormat: f, TimeScale: DefaultTimeScale, DisplayOrder: true}
					dec, err := newDecoder(ctx, wrapped, cfg)
					if errors.Is(err, ErrUnsupported) {
						t.Skipf("no %s decoder on this machine: %v", c, err)
					}
					if err != nil {
						t.Fatalf("newDecoder: %v", err)
					}
					if _, ok := dec.(*pixconv.Decoder); !ok {
						t.Fatalf("got %T, want the conversion behind the backend decoder", dec)
					}
					decoded[f] = decodeRGB(t, dec, c, stream, f, tc.width, tc.height)
					dec.Close()
					if len(decoded[f]) != frames {
						t.Fatalf("%s: decoded %d frames, want %d", f, len(decoded[f]), frames)
					}
				}
				rgba, bgra := decoded[RGBA], decoded[BGRA]
				for i := range rgba {
					a, b := rgba[i].pix, bgra[i].pix
					for j := 0; j < len(a); j += 4 {
						if a[j] != b[j+2] || a[j+1] != b[j+1] || a[j+2] != b[j] || a[j+3] != 255 || b[j+3] != 255 {
							t.Fatalf("frame %d pixel %d: RGBA %v is not the mirror of BGRA %v with alpha 255", i, j/4, a[j:j+4], b[j:j+4])
						}
					}
				}

				// The backend's own conversion, where it has one.
				native, err := newDecoder(ctx, backends(), codec.DecoderConfig{Codec: c, OutputFormat: RGBA, TimeScale: DefaultTimeScale, DisplayOrder: true})
				if err != nil {
					t.Fatalf("newDecoder: %v", err)
				}
				if _, converted := native.(*pixconv.Decoder); !converted {
					want := decodeRGB(t, native, c, stream, RGBA, tc.width, tc.height)
					worst := 1e9
					for i := range want {
						if rgba[i].pts != want[i].pts {
							t.Fatalf("frame %d has PTS %d, the backend's own RGB frame %d", i, rgba[i].pts, want[i].pts)
						}
						worst = min(worst, testutil.BlockPSNR(rgba[i].pix, want[i].pix, tc.width, tc.height, 8))
					}
					t.Logf("worst 8x8-block PSNR against the backend's own RGBA: %.2f dB", worst)
					if worst < minPSNR {
						t.Errorf("worst PSNR against the backend's own RGBA %.2f dB is below %.0f dB", worst, minPSNR)
					}
				}
				native.Close()

				if !tc.ffmpeg {
					return
				}
				path := filepath.Join(t.TempDir(), "stream."+c.String())
				if err := os.WriteFile(path, stream, 0o644); err != nil {
					t.Fatal(err)
				}
				want := testutil.ReferenceFrames(t, path, c, RGBA, tc.width, tc.height)
				if len(want) != frames {
					t.Fatalf("ffmpeg decoded %d frames, want %d", len(want), frames)
				}
				worst := 1e9
				for i := range want {
					worst = min(worst, testutil.BlockPSNR(rgba[i].pix, want[i], tc.width, tc.height, 8))
				}
				t.Logf("worst 8x8-block PSNR against ffmpeg: %.2f dB", worst)
				if worst < minPSNR {
					t.Errorf("worst PSNR against ffmpeg %.2f dB is below %.0f dB", worst, minPSNR)
				}
			})
		}
	}
}
