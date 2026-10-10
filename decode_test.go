package hwmediacodec_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
)

// requireHardware skips the test unless Probe reports a hardware engine for
// c in the given direction on this machine. Intel Macs are out of scope.
func requireHardware(t *testing.T, c hwmediacodec.Codec, dir hwmediacodec.Direction) {
	t.Helper()
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		t.Skip("hardware tests target Apple Silicon (Intel Macs are out of scope)")
	}
	if !hwmediacodec.HasHardware(context.Background(), c, dir) {
		t.Skipf("no hardware %s %s on this machine (%s/%s)", c, dir, runtime.GOOS, runtime.GOARCH)
	}
}

// hasHardware reports whether Probe lists a hardware engine for c.
func hasHardware(t *testing.T, c hwmediacodec.Codec, dir hwmediacodec.Direction) bool {
	t.Helper()
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		return false
	}
	return hwmediacodec.HasHardware(context.Background(), c, dir)
}

// decodeBackend names the backend that serves hardware decoding of c: the
// first one Probe lists, which is the one NewDecoder tries first.
func decodeBackend(t *testing.T, c hwmediacodec.Codec) string {
	t.Helper()
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, cap := range caps {
		if cap.Codec == c && cap.Direction == hwmediacodec.Decode && cap.Hardware {
			return cap.Backend
		}
	}
	return ""
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

// splitStream splits an elementary stream into the packets a decoder
// consumes: Annex-B access units for H.264 and HEVC, IVF frames (temporal
// units) for AV1.
func splitStream(t *testing.T, c hwmediacodec.Codec, stream []byte) [][]byte {
	t.Helper()
	var out [][]byte
	if c == hwmediacodec.AV1 {
		r, err := ivf.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatalf("ivf: %v", err)
		}
		for {
			tu, _, err := r.Next()
			if err == io.EOF {
				return out
			}
			if err != nil {
				t.Fatalf("ivf: %v", err)
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
			t.Fatalf("annexb: %v", err)
		}
		out = append(out, au)
	}
}

// concatStreams joins two elementary streams of the same codec into one:
// by appending for Annex-B, by rewriting the frames of both IVF files into
// one IVF file for AV1.
func concatStreams(t *testing.T, c hwmediacodec.Codec, a, b []byte) []byte {
	t.Helper()
	if c != hwmediacodec.AV1 {
		return append(append([]byte(nil), a...), b...)
	}
	var buf bytes.Buffer
	w := ivf.NewWriter(&buf, "AV01", 0, 0, 1, 30)
	var pts uint64
	for _, s := range [][]byte{a, b} {
		for _, tu := range splitStream(t, c, s) {
			if err := w.WriteFrame(tu, pts); err != nil {
				t.Fatal(err)
			}
			pts++
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// decodeFrames feeds every packet of an elementary stream (see splitStream),
// checks that the frames come back in format f with tight strides, and
// returns copies in output order.
func decodeFrames(t *testing.T, dec hwmediacodec.Decoder, c hwmediacodec.Codec, stream []byte, format hwmediacodec.PixelFormat) []decodedFrame {
	t.Helper()
	return decodePackets(t, dec, splitStream(t, c, stream), format)
}

// decodePackets is decodeFrames for packets already split. Packet i is sent
// with PTS 3000*i.
func decodePackets(t *testing.T, dec hwmediacodec.Decoder, packets [][]byte, format hwmediacodec.PixelFormat) []decodedFrame {
	t.Helper()
	var out []decodedFrame
	ctx := context.Background()
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
	for _, p := range packets {
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: p, PTS: pts}); err != nil {
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
	switch decodeBackend(t, hwmediacodec.H264) {
	case "mediafoundation":
		t.Skip("the Media Foundation decoders reorder natively; WithDecodeOrder has no effect on that backend")
	case "vpl":
		t.Skip("the Intel VPL decoder reorders natively; WithDecodeOrder has no effect on that backend")
	}
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
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC, hwmediacodec.AV1} {
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

// TestDecodeParameterSetChange decodes two streams of different sizes
// joined into one: the parameter sets (or AV1 sequence header) change in
// the middle and the decoder has to start a new session.
func TestDecodeParameterSetChange(t *testing.T) {
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC, hwmediacodec.AV1} {
		t.Run(c.String(), func(t *testing.T) {
			requireHardwareDecode(t, c)
			s1 := testutil.GenerateStream(t, c, 320, 240, 30)
			s2 := testutil.GenerateStream(t, c, 160, 120, 30)
			want := append(testutil.ReferenceNV12(t, s1.Path, s1.Codec, s1.Width, s1.Height),
				testutil.ReferenceNV12(t, s2.Path, s2.Codec, s2.Width, s2.Height)...)
			stream := concatStreams(t, c, testutil.ReadFile(t, s1.Path), testutil.ReadFile(t, s2.Path))

			dec, err := hwmediacodec.NewDecoder(context.Background(), c)
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			defer dec.Close()
			got, sizes := decodeAll(t, dec, c, stream)
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
		})
	}
}

func TestFlushResumesAtKeyframe(t *testing.T) {
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC, hwmediacodec.AV1} {
		t.Run(c.String(), func(t *testing.T) {
			requireHardwareDecode(t, c)
			s := testutil.GenerateStream(t, c, 160, 120, 20)
			aus := splitStream(t, c, testutil.ReadFile(t, s.Path))
			if len(aus) != 20 {
				t.Fatalf("got %d access units, want 20", len(aus))
			}

			ctx := context.Background()
			dec, err := hwmediacodec.NewDecoder(ctx, c)
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
			// A backend may hold the picture until more input or a flush arrives, so
			// drain before counting.
			if err := dec.Send(ctx, hwmediacodec.Packet{Data: aus[0]}); err != nil {
				t.Fatal(err)
			}
			n := count()
			if err := dec.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if n += count(); n != 1 {
				t.Fatalf("got %d frames from the keyframe after flush, want 1", n)
			}
		})
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

// TestDecodeHEVCCodingTools decodes streams that use the parts of HEVC a
// slice-level backend has to describe to the driver itself: several
// references and B-pyramids (reference picture sets and lists), weighted
// prediction tables, several slices and wavefront entry points, scaling
// lists, and a conformance window.
func TestDecodeHEVCCodingTools(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.HEVC)
	fade := []string{"-vf", "fade=t=in:st=0:d=0.8,fade=t=out:st=1.0:d=0.6"}
	for _, tc := range []struct {
		name   string
		w, h   int
		params string
		extra  []string
	}{
		{"bpyramid-refs", 320, 240, "bframes=4:b-pyramid=1:ref=5:keyint=20:min-keyint=20:open-gop=0", nil},
		{"lowdelay-refs", 320, 240, "bframes=0:ref=6:keyint=30", nil},
		{"weights", 320, 240, "bframes=3:ref=3:weightp=1:weightb=1:keyint=48:open-gop=0", fade},
		{"slices", 320, 240, "bframes=2:slices=3:keyint=15:min-keyint=15:open-gop=0", nil},
		{"no-wavefront", 320, 240, "bframes=2:no-wpp=1:keyint=15:min-keyint=15:open-gop=0", nil},
		{"scaling-list", 320, 240, "bframes=2:scaling-list=default:keyint=15:min-keyint=15:open-gop=0", nil},
		{"tools", 320, 240, "bframes=2:rect=1:amp=1:tskip=1:no-sao=1:constrained-intra=1:cbqpoffs=2:crqpoffs=-3:deblock=2,-1:keyint=15:open-gop=0", nil},
		{"cropped", 322, 242, "bframes=2:keyint=15:min-keyint=15:open-gop=0", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := testutil.GenerateHEVC(t, tc.w, tc.h, 48, tc.params, tc.extra...)
			want := testutil.ReferenceNV12(t, path, hwmediacodec.HEVC, tc.w, tc.h)
			dec := newDecoderOrSkip(t, hwmediacodec.HEVC)
			defer dec.Close()
			got, sizes := decodeAll(t, dec, hwmediacodec.HEVC, testutil.ReadFile(t, path))
			for i, sz := range sizes {
				if sz != [2]int{tc.w, tc.h} {
					t.Fatalf("frame %d size %v, want %dx%d", i, sz, tc.w, tc.h)
				}
			}
			compareChecksums(t, got, want)
		})
	}
}

// TestDecodeHEVCOpenGOP decodes a stream whose later keyframes are CRA
// pictures followed by RASL pictures. From the start every picture is
// decodable; started at a CRA (a seek), the RASL pictures that follow it
// refer to pictures before it and must be dropped, exactly as the reference
// decoder does.
func TestDecodeHEVCOpenGOP(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.HEVC)
	const w, h = 320, 240
	path := testutil.GenerateHEVC(t, w, h, 40, "bframes=3:b-pyramid=1:keyint=10:min-keyint=10:open-gop=1")
	data := testutil.ReadFile(t, path)

	dec := newDecoderOrSkip(t, hwmediacodec.HEVC)
	got, _ := decodeAll(t, dec, hwmediacodec.HEVC, data)
	dec.Close()
	compareChecksums(t, got, testutil.ReferenceNV12(t, path, hwmediacodec.HEVC, w, h))

	// Find the first CRA picture after the start and cut the stream there.
	r := annexb.NewReader(bytes.NewReader(data), hwmediacodec.HEVC)
	var tail []byte
	for index := 0; ; index++ {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if tail == nil {
			cra := false
			for _, nal := range annexb.Split(au) {
				if annexb.NALUnitType(hwmediacodec.HEVC, nal) == 21 { // CRA_NUT
					cra = true
				}
			}
			if !cra || index == 0 {
				continue
			}
			// The cut needs the parameter sets, which the first access
			// unit carries.
			first, err := annexb.NewReader(bytes.NewReader(data), hwmediacodec.HEVC).Next()
			if err != nil {
				t.Fatal(err)
			}
			for _, nal := range annexb.Split(first) {
				if annexb.IsParameterSet(hwmediacodec.HEVC, annexb.NALUnitType(hwmediacodec.HEVC, nal)) {
					tail = append(append(tail, 0, 0, 0, 1), nal...)
				}
			}
		}
		tail = append(tail, au...)
	}
	if tail == nil {
		t.Skip("the encoder produced no CRA picture after the start")
	}
	cut := filepath.Join(t.TempDir(), "tail.hevc")
	if err := os.WriteFile(cut, tail, 0o644); err != nil {
		t.Fatal(err)
	}
	want := testutil.ReferenceNV12(t, cut, hwmediacodec.HEVC, w, h)
	dec = newDecoderOrSkip(t, hwmediacodec.HEVC)
	defer dec.Close()
	got, _ = decodeAll(t, dec, hwmediacodec.HEVC, tail)
	if len(got) >= len(data) || len(want) == 0 {
		t.Fatalf("decoded %d frames after the cut, reference has %d", len(got), len(want))
	}
	compareChecksums(t, got, want)
}
