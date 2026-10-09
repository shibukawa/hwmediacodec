//go:build windows && (amd64 || arm64)

package hwmediacodec_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// requireHardware skips the test when Probe does not report a Media
// Foundation hardware decoder for c (no GPU, missing codec package).
func requireHardware(t *testing.T, c hwmediacodec.Codec) {
	t.Helper()
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, cap := range caps {
		if cap.Backend == "mediafoundation" && cap.Codec == c && cap.Direction == hwmediacodec.Decode && cap.Hardware {
			return
		}
	}
	t.Skipf("no Media Foundation hardware decoder for %s on this machine", c)
}

func TestDecodeH264MatchesReference(t *testing.T) {
	requireHardware(t, hwmediacodec.H264)
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

func TestDecodeHEVCMatchesReference(t *testing.T) {
	requireHardware(t, hwmediacodec.HEVC)
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

// TestDecodeBFramesDisplayOrder checks streams with B-frames. The Media
// Foundation decoders reorder output into display order, so the comparison
// is order-sensitive; a set comparison is reported separately when the order
// differs so that a reorder bug is distinguishable from a decode bug.
func TestDecodeBFramesDisplayOrder(t *testing.T) {
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		t.Run(c.String(), func(t *testing.T) {
			requireHardware(t, c)
			s := testutil.GenerateStreamBFrames(t, c, 320, 240, 60, 3)
			want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)
			dec, err := hwmediacodec.NewDecoder(context.Background(), c)
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			defer dec.Close()
			got, _ := decodeAll(t, dec, c, testutil.ReadFile(t, s.Path))
			if len(got) != len(want) {
				t.Fatalf("frame count: got %d want %d", len(got), len(want))
			}
			ordered := true
			for i := range want {
				if got[i] != want[i] {
					ordered = false
					break
				}
			}
			if !ordered {
				t.Errorf("frames are not in display order")
				sortChecksums(got)
				sortChecksums(want)
			}
			compareChecksums(t, got, want)
		})
	}
}

func TestDecodeParameterSetChange(t *testing.T) {
	requireHardware(t, hwmediacodec.H264)
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
	requireHardware(t, hwmediacodec.H264)
	s := testutil.GenerateStream(t, hwmediacodec.H264, 160, 120, 20)
	aus := splitAccessUnits(t, hwmediacodec.H264, testutil.ReadFile(t, s.Path))
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
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: aus[i], PTS: int64(i) * 3000}); err != nil {
			t.Fatal(err)
		}
		count()
	}
	if err := dec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	count()
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
	// The decoder may hold the picture for reordering, so drain it.
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
}

func TestInvalidData(t *testing.T) {
	requireHardware(t, hwmediacodec.H264)
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
