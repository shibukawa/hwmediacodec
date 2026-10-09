package hwmediacodec_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"sort"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// requireHardware skips the test unless Probe reports a hardware engine for
// c in the given direction on this machine. Intel Macs are out of scope.
func requireHardware(t *testing.T, c hwmediacodec.Codec, dir hwmediacodec.Direction) {
	t.Helper()
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		t.Skip("hardware tests target Apple Silicon (Intel Macs are out of scope)")
	}
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, cap := range caps {
		if cap.Codec == c && cap.Direction == dir && cap.Hardware {
			return
		}
	}
	t.Skipf("no hardware %s %s on this machine (%s/%s)", c, dir, runtime.GOOS, runtime.GOARCH)
}

// hasHardware reports whether Probe lists a hardware engine for c.
func hasHardware(t *testing.T, c hwmediacodec.Codec, dir hwmediacodec.Direction) bool {
	t.Helper()
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		return false
	}
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, cap := range caps {
		if cap.Codec == c && cap.Direction == dir && cap.Hardware {
			return true
		}
	}
	return false
}

func requireHardwareDecode(t *testing.T, c hwmediacodec.Codec) {
	t.Helper()
	requireHardware(t, c, hwmediacodec.Decode)
}

func requireHardwareEncode(t *testing.T, c hwmediacodec.Codec) {
	t.Helper()
	requireHardware(t, c, hwmediacodec.Encode)
}

// newDecoderOrSkip opens a decoder and skips the test when the backend on
// this machine does not offer the requested configuration (for example an
// output format a backend does not convert to).
func newDecoderOrSkip(t *testing.T, c hwmediacodec.Codec, opts ...hwmediacodec.DecoderOption) hwmediacodec.Decoder {
	t.Helper()
	dec, err := hwmediacodec.NewDecoder(context.Background(), c, opts...)
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Skipf("configuration not available on this backend: %v", err)
	}
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	return dec
}

// decodeAll feeds every access unit of an Annex-B stream and returns the
// decoded frames' checksums and sizes in output order.
func decodeAll(t *testing.T, dec hwmediacodec.Decoder, c hwmediacodec.Codec, stream []byte) (sums [][32]byte, sizes [][2]int) {
	t.Helper()
	frames := decodeFrames(t, dec, c, stream, hwmediacodec.NV12)
	for _, f := range frames {
		sums = append(sums, sha256.Sum256(f.pix))
		sizes = append(sizes, [2]int{f.width, f.height})
	}
	return sums, sizes
}

// decodedFrame is a tightly packed copy of a decoded frame.
type decodedFrame struct {
	pix           []byte
	width, height int
	pts           int64
}

// decodeFrames feeds every access unit of an Annex-B stream, checks that
// the frames come back in format f with tight strides, and returns copies
// in output order.
func decodeFrames(t *testing.T, dec hwmediacodec.Decoder, c hwmediacodec.Codec, stream []byte, format hwmediacodec.PixelFormat) []decodedFrame {
	t.Helper()
	var out []decodedFrame
	ctx := context.Background()
	r := annexb.NewReader(bytes.NewReader(stream), c)
	var pts int64
	drain := func(final bool) {
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				if final {
					t.Fatal("Receive returned ErrAgain after Flush")
				}
				return
			}
			if err == io.EOF {
				if !final {
					t.Fatal("Receive returned io.EOF before Flush")
				}
				return
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			if f.Format != format || len(f.Planes) != format.PlaneCount() {
				t.Fatalf("unexpected frame layout: %s planes=%d", f.Format, len(f.Planes))
			}
			for i, stride := range f.Strides {
				if _, rowBytes := format.PlaneLayout(i, f.Width, f.Height); stride != rowBytes {
					t.Fatalf("plane %d stride %d, want tight rows of %d bytes", i, stride, rowBytes)
				}
			}
			out = append(out, decodedFrame{pix: testutil.FrameBytes(f), width: f.Width, height: f.Height, pts: f.PTS})
			f.Release()
		}
	}
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("annexb: %v", err)
		}
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: au, PTS: pts}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		pts += 3000
		drain(false)
	}
	if err := dec.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	drain(true)
	return out
}

func compareChecksums(t *testing.T, got, want [][32]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("frame count: got %d want %d", len(got), len(want))
	}
	mismatches := 0
	for i := range want {
		if got[i] != want[i] {
			mismatches++
			if mismatches <= 5 {
				t.Errorf("frame %d checksum differs: got %x want %x", i, got[i][:8], want[i][:8])
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d frames differ from the ffmpeg reference", mismatches, len(want))
	}
}

func TestDecodeH264MatchesReference(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.H264)
	s := testutil.GenerateStream(t, hwmediacodec.H264, 320, 240, 60)
	want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)

	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.H264)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	got, sizes := decodeAll(t, dec, hwmediacodec.H264, testutil.ReadFile(t, s.Path))
	for i, sz := range sizes {
		if sz != [2]int{320, 240} {
			t.Fatalf("frame %d size %v", i, sz)
		}
	}
	compareChecksums(t, got, want)
}

// TestDecodeBFramesMatchesReference checks that streams with B-frames come
// back in display order: the frame sequence must equal ffmpeg's output
// frame for frame.
func TestDecodeBFramesMatchesReference(t *testing.T) {
	cases := []struct {
		codec   hwmediacodec.Codec
		bframes int
	}{
		{hwmediacodec.H264, 3}, {hwmediacodec.H264, 8},
		{hwmediacodec.HEVC, 3}, {hwmediacodec.HEVC, 6},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s-bf%d", c.codec, c.bframes), func(t *testing.T) {
			requireHardwareDecode(t, c.codec)
			s := testutil.GenerateStreamBFrames(t, c.codec, 320, 240, 60, c.bframes)
			want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)
			dec, err := hwmediacodec.NewDecoder(context.Background(), c.codec)
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			defer dec.Close()
			got, _ := decodeAll(t, dec, c.codec, testutil.ReadFile(t, s.Path))
			compareChecksums(t, got, want)
		})
	}
}

// TestDecodeOrderOption checks that WithDecodeOrder returns the frames as
// the hardware produces them: the same set of frames, not in display order.
func TestDecodeOrderOption(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.H264)
	s := testutil.GenerateStreamBFrames(t, hwmediacodec.H264, 320, 240, 60, 3)
	want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)
	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.H264, hwmediacodec.WithDecodeOrder())
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	got, _ := decodeAll(t, dec, hwmediacodec.H264, testutil.ReadFile(t, s.Path))
	if len(got) != len(want) {
		t.Fatalf("frame count: got %d want %d", len(got), len(want))
	}
	inOrder := true
	for i := range want {
		if got[i] != want[i] {
			inOrder = false
		}
	}
	if inOrder {
		t.Error("decode order equals display order for a B-frame stream")
	}
	sortChecksums(got)
	sortChecksums(want)
	compareChecksums(t, got, want)
}

// TestDecodeRGBA checks the packed RGB output formats against ffmpeg's
// conversion of the same frames. The two converters interpolate chroma
// differently around sharp edges (about 28 dB plain PSNR on testsrc2), so
// the comparison averages 8x8 blocks first: a wrong matrix, range or
// channel order still fails by a wide margin (BT.709 scores 25 dB, swapped
// channels 4 dB). RGBA and BGRA must be exact mirrors of each other.
func TestDecodeRGBA(t *testing.T) {
	const minPSNR = 30.0
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		if !hasHardware(t, c, hwmediacodec.Decode) {
			continue
		}
		s := testutil.GenerateStreamBFrames(t, c, 320, 240, 20, 2)
		stream := testutil.ReadFile(t, s.Path)
		decoded := map[hwmediacodec.PixelFormat][]decodedFrame{}
		for _, f := range []hwmediacodec.PixelFormat{hwmediacodec.RGBA, hwmediacodec.BGRA} {
			t.Run(c.String()+"-"+f.String(), func(t *testing.T) {
				want := testutil.ReferenceFrames(t, s.Path, c, f, s.Width, s.Height)
				dec := newDecoderOrSkip(t, c, hwmediacodec.WithOutputFormat(f))
				defer dec.Close()
				got := decodeFrames(t, dec, c, stream, f)
				if len(got) != len(want) {
					t.Fatalf("frame count: got %d want %d", len(got), len(want))
				}
				worst := math.Inf(1)
				for i := range got {
					if len(got[i].pix) != f.FrameSize(320, 240) {
						t.Fatalf("frame %d has %d bytes", i, len(got[i].pix))
					}
					for j := 3; j < len(got[i].pix); j += 4 {
						if got[i].pix[j] != 255 {
							t.Fatalf("frame %d pixel %d alpha = %d, want 255", i, j/4, got[i].pix[j])
						}
					}
					if psnr := testutil.BlockPSNR(got[i].pix, want[i], 320, 240, 8); psnr < worst {
						worst = psnr
					}
				}
				t.Logf("worst 8x8-block PSNR against ffmpeg %s: %.2f dB", f, worst)
				if worst < minPSNR {
					t.Fatalf("worst PSNR %.2f dB is below %.0f dB", worst, minPSNR)
				}
				decoded[f] = got
			})
		}
		rgba, bgra := decoded[hwmediacodec.RGBA], decoded[hwmediacodec.BGRA]
		if len(rgba) == 0 || len(rgba) != len(bgra) {
			continue
		}
		for i := range rgba {
			a, b := rgba[i].pix, bgra[i].pix
			for j := 0; j < len(a); j += 4 {
				if a[j] != b[j+2] || a[j+1] != b[j+1] || a[j+2] != b[j] || a[j+3] != b[j+3] {
					t.Fatalf("%s frame %d pixel %d: RGBA %v is not the mirror of BGRA %v", c, i, j/4, a[j:j+4], b[j:j+4])
				}
			}
		}
	}
}

func sortChecksums(s [][32]byte) {
	sort.Slice(s, func(i, j int) bool { return bytes.Compare(s[i][:], s[j][:]) < 0 })
}

func TestDecodeHEVCMatchesReference(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.HEVC)
	s := testutil.GenerateStream(t, hwmediacodec.HEVC, 320, 240, 60)
	want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)

	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.HEVC)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	got, _ := decodeAll(t, dec, hwmediacodec.HEVC, testutil.ReadFile(t, s.Path))
	compareChecksums(t, got, want)
}

func TestDecodeParameterSetChange(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.H264)
	s1 := testutil.GenerateStream(t, hwmediacodec.H264, 320, 240, 30)
	s2 := testutil.GenerateStream(t, hwmediacodec.H264, 160, 120, 30)
	want := append(testutil.ReferenceNV12(t, s1.Path, s1.Codec, s1.Width, s1.Height),
		testutil.ReferenceNV12(t, s2.Path, s2.Codec, s2.Width, s2.Height)...)
	stream := append(testutil.ReadFile(t, s1.Path), testutil.ReadFile(t, s2.Path)...)

	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.H264)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	got, sizes := decodeAll(t, dec, hwmediacodec.H264, stream)
	if len(sizes) != 60 {
		t.Fatalf("frame count: got %d want 60", len(sizes))
	}
	for i, sz := range sizes {
		want := [2]int{320, 240}
		if i >= 30 {
			want = [2]int{160, 120}
		}
		if sz != want {
			t.Fatalf("frame %d size %v want %v", i, sz, want)
		}
	}
	compareChecksums(t, got, want)
}

func TestFlushResumesAtKeyframe(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.H264)
	s := testutil.GenerateStream(t, hwmediacodec.H264, 160, 120, 20)
	data := testutil.ReadFile(t, s.Path)
	r := annexb.NewReader(bytes.NewReader(data), hwmediacodec.H264)
	var aus [][]byte
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		aus = append(aus, au)
	}
	if len(aus) != 20 {
		t.Fatalf("got %d access units, want 20", len(aus))
	}

	ctx := context.Background()
	dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	count := func() int {
		n := 0
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return n
			}
			if err != nil {
				t.Fatal(err)
			}
			f.Release()
			n++
		}
	}

	// Decode the first five pictures, then flush (seek).
	for i := 0; i < 5; i++ {
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: aus[i]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 5 {
		t.Fatalf("got %d frames before flush, want 5", n)
	}
	if _, err := dec.Receive(ctx); err != io.EOF {
		t.Fatalf("after flush Receive = %v, want io.EOF", err)
	}

	// A non-keyframe after the flush must be skipped without error.
	if err := dec.Send(ctx, hwmediacodec.Packet{Data: aus[7]}); err != nil {
		t.Fatalf("Send non-keyframe after flush: %v", err)
	}
	if n := count(); n != 0 {
		t.Fatalf("got %d frames from a non-keyframe after flush, want 0", n)
	}
	// Decoding resumes at the next keyframe (the stream starts with an IDR).
	if err := dec.Send(ctx, hwmediacodec.Packet{Data: aus[0]}); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("got %d frames from the keyframe after flush, want 1", n)
	}
}

func TestInvalidData(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.H264)
	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.H264)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	err = dec.Send(context.Background(), hwmediacodec.Packet{Data: []byte("not a bitstream")})
	if !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Fatalf("got %v want ErrInvalidData", err)
	}
	if err := dec.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dec.Receive(context.Background()); !errors.Is(err, hwmediacodec.ErrClosed) {
		t.Fatalf("Receive after Close = %v, want ErrClosed", err)
	}
}
