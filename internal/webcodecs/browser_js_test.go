//go:build js && wasm

package webcodecs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/reorder"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
)

// These tests need a browser (scripts/browser_test.sh ./internal/webcodecs);
// under Node.js there is no WebCodecs and they skip.

func requireWebCodecs(t *testing.T) {
	t.Helper()
	if _, ok := class("VideoDecoder"); !ok {
		t.Skip(noWebCodecs)
	}
}

// software makes every configuration of the test ask for the browser's
// software codecs.
func software(t *testing.T) {
	t.Helper()
	accelerationOverride = "prefer-software"
	t.Cleanup(func() { accelerationOverride = "" })
}

func units(t *testing.T, c codec.Codec, stream []byte) [][]byte {
	t.Helper()
	var out [][]byte
	if c == codec.AV1 {
		r, err := ivf.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatal(err)
		}
		for {
			tu, _, err := r.Next()
			if err == io.EOF {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, tu)
		}
	}
	r := annexb.NewReader(bytes.NewReader(stream), c)
	for {
		au, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, au)
	}
}

// openDecoder opens a backend decoder the way the public API does: with
// the reorder layer where the backend asks for it.
func openDecoder(t *testing.T, c codec.Codec, format codec.PixelFormat) codec.Decoder {
	t.Helper()
	d, err := Backend{}.NewDecoder(context.Background(), codec.DecoderConfig{Codec: c, OutputFormat: format, TimeScale: 90000, DisplayOrder: true, AllowSoftware: true})
	if errors.Is(err, codec.ErrUnsupported) {
		t.Skipf("not available in this browser: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := d.(codec.DisplayOrderer); !ok || !o.OutputsDisplayOrder() {
		d = reorder.Wrap(d, c)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// decode runs the Send/Receive loop over the packets and returns the
// checksum of every frame.
func decode(t *testing.T, d codec.Decoder, packets [][]byte) [][32]byte {
	t.Helper()
	ctx := context.Background()
	var sums [][32]byte
	drain := func(end error) {
		for {
			f, err := d.Receive(ctx)
			if err == end {
				return
			}
			if errors.Is(err, codec.ErrUnsupported) {
				// A browser whose decoders hand over RGB has no NV12.
				t.Skipf("not available in this browser: %v", err)
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			sums = append(sums, testutil.FrameChecksum(f))
			f.Release()
		}
	}
	for i, p := range packets {
		for {
			err := d.Send(ctx, codec.Packet{Data: p, PTS: int64(i)})
			if errors.Is(err, codec.ErrAgain) {
				drain(codec.ErrAgain)
				continue
			}
			if err != nil {
				t.Fatalf("Send %d: %v", i, err)
			}
			break
		}
		drain(codec.ErrAgain)
	}
	if err := d.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	drain(io.EOF)
	return sums
}

func compare(t *testing.T, got, want [][32]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d frames, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d differs from ffmpeg's", i)
		}
	}
}

// TestSoftwareDecodeMatchesReference decodes with the browser's software
// decoders, whose frames are planar (I420) and are packed into NV12 in Go,
// and compares every frame with ffmpeg's decode. HEVC has no software
// decoder in the browsers and skips.
func TestSoftwareDecodeMatchesReference(t *testing.T) {
	requireWebCodecs(t)
	for _, c := range []codec.Codec{codec.H264, codec.HEVC, codec.AV1} {
		t.Run(c.String(), func(t *testing.T) {
			software(t)
			d := openDecoder(t, c, codec.NV12)
			s := testutil.GenerateStreamBFrames(t, c, 320, 240, 40, 2)
			want := testutil.ReferenceNV12(t, s.Path, c, s.Width, s.Height)
			compare(t, decode(t, d, units(t, c, testutil.ReadFile(t, s.Path))), want)
		})
	}
}

// TestSoftwareEncode encodes with the browser's software H.264 encoder and
// decodes the result with ffmpeg.
func TestSoftwareEncode(t *testing.T) {
	requireWebCodecs(t)
	software(t)
	const w, h, n = 320, 240, 20
	ctx := context.Background()
	e, err := Backend{}.NewEncoder(ctx, codec.EncoderConfig{
		Codec: codec.H264, Width: w, Height: h, InputFormat: codec.NV12, TimeScale: 90000,
		FrameRate: 30, Bitrate: 800_000, RateControl: codec.VBR, KeyframeInterval: 10, AllowSoftware: true,
	})
	if errors.Is(err, codec.ErrUnsupported) {
		t.Skipf("not available in this browser: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	raw := testutil.GenerateNV12Frames(t, w, h, n)
	var stream []byte
	keys, packets := 0, 0
	drain := func(end error) {
		for {
			p, err := e.Receive(ctx)
			if err == end {
				return
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			if p.Keyframe {
				keys++
				if info := newParamSets(codec.H264).inspect(p.Data); !info.kinds[annexb.ParamSPS] || !info.kinds[annexb.ParamPPS] {
					t.Errorf("keyframe packet %d carries no parameter sets", packets)
				}
			}
			if p.PTS != int64(packets)*3000 {
				t.Errorf("packet %d has PTS %d", packets, p.PTS)
			}
			packets++
			stream = append(stream, p.Data...)
		}
	}
	for i, pix := range raw {
		if err := e.Send(ctx, testutil.NV12Frame(pix, w, h, int64(i)*3000)); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		drain(codec.ErrAgain)
	}
	if err := e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	drain(io.EOF)
	if packets != n || keys != 2 {
		t.Fatalf("%d packets with %d keyframes, want %d and 2", packets, keys, n)
	}
	path := testutil.TempDir(t) + "/software.h264"
	testutil.WriteFile(t, path, stream)
	decoded := testutil.ReferenceNV12Frames(t, path, codec.H264, w, h)
	if len(decoded) != n {
		t.Fatalf("ffmpeg decodes %d frames, want %d", len(decoded), n)
	}
	worst := 100.0
	for i := range decoded {
		if psnr := testutil.PSNRY(decoded[i], raw[i], w, h); psnr < worst {
			worst = psnr
		}
	}
	t.Logf("worst luma PSNR %.2f dB", worst)
	if worst < 30 {
		t.Errorf("worst luma PSNR %.2f dB is below 30 dB", worst)
	}
}

// TestDecoderRecoversFromError breaks a stream so that the browser's
// decoder gives up, which closes a VideoDecoder for good, and checks that
// the error is reported and that the same Decoder then decodes a stream
// from its first keyframe.
func TestDecoderRecoversFromError(t *testing.T) {
	requireWebCodecs(t)
	d := openDecoder(t, codec.H264, codec.NV12)
	s := testutil.GenerateStream(t, codec.H264, 320, 240, 12)
	stream := testutil.ReadFile(t, s.Path)
	packets := units(t, codec.H264, stream)
	want := testutil.ReferenceNV12(t, s.Path, codec.H264, s.Width, s.Height)

	// The parameter sets of the stream in front of a keyframe slice that
	// is noise.
	var broken []byte
	for _, nal := range annexb.Split(packets[0]) {
		if annexb.IsParameterSet(codec.H264, annexb.NALUnitType(codec.H264, nal)) {
			broken = annexb.AppendUnit(broken, nal)
		}
	}
	noise := []byte{0x65}
	for i := 0; i < 400; i++ {
		noise = append(noise, byte(i*37+11)|1)
	}
	broken = annexb.AppendUnit(broken, noise)

	ctx := context.Background()
	err := d.Send(ctx, codec.Packet{Data: broken})
	if err == nil {
		err = d.Flush(ctx)
	}
	for err == nil {
		var f *codec.Frame
		if f, err = d.Receive(ctx); err == nil {
			f.Release()
		}
	}
	if err == io.EOF {
		t.Skip("this browser's decoder swallowed the broken keyframe")
	}
	t.Logf("broken keyframe: %v", err)
	if errors.Is(err, codec.ErrAgain) || errors.Is(err, codec.ErrClosed) {
		t.Fatalf("broken keyframe reported as %v", err)
	}
	compare(t, decode(t, d, packets), want)
}
