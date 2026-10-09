//go:build darwin

package hwmediacodec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

func requireAppleSilicon(t *testing.T) {
	t.Helper()
	if runtime.GOARCH != "arm64" {
		t.Skip("hardware decode tests target Apple Silicon (Intel Macs are out of scope)")
	}
}

func TestDecodeH264MatchesReference(t *testing.T) {
	requireAppleSilicon(t)
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
	requireAppleSilicon(t)
	cases := []struct {
		codec   hwmediacodec.Codec
		bframes int
	}{
		{hwmediacodec.H264, 3}, {hwmediacodec.H264, 8},
		{hwmediacodec.HEVC, 3}, {hwmediacodec.HEVC, 6},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s-bf%d", c.codec, c.bframes), func(t *testing.T) {
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
	requireAppleSilicon(t)
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
	requireAppleSilicon(t)
	const minPSNR = 30.0
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		s := testutil.GenerateStreamBFrames(t, c, 320, 240, 20, 2)
		stream := testutil.ReadFile(t, s.Path)
		decoded := map[hwmediacodec.PixelFormat][]decodedFrame{}
		for _, f := range []hwmediacodec.PixelFormat{hwmediacodec.RGBA, hwmediacodec.BGRA} {
			t.Run(c.String()+"-"+f.String(), func(t *testing.T) {
				want := testutil.ReferenceFrames(t, s.Path, c, f, s.Width, s.Height)
				dec, err := hwmediacodec.NewDecoder(context.Background(), c, hwmediacodec.WithOutputFormat(f))
				if err != nil {
					t.Fatalf("NewDecoder: %v", err)
				}
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

func TestDecodeHEVCMatchesReference(t *testing.T) {
	requireAppleSilicon(t)
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
	requireAppleSilicon(t)
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
	requireAppleSilicon(t)
	s := testutil.GenerateStream(t, hwmediacodec.H264, 160, 120, 20)
	data := testutil.ReadFile(t, s.Path)
	aus := splitAccessUnits(t, hwmediacodec.H264, data)
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
	requireAppleSilicon(t)
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
