package hwmediacodec_test

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func requireHardwareDecode(t *testing.T, c hwmediacodec.Codec) {
	t.Helper()
	requireHardware(t, c, hwmediacodec.Decode)
}

func requireHardwareEncode(t *testing.T, c hwmediacodec.Codec) {
	t.Helper()
	requireHardware(t, c, hwmediacodec.Encode)
}

// decodeAll feeds every access unit of an Annex-B stream and returns the
// decoded frames' checksums and sizes in output order.
func decodeAll(t *testing.T, dec hwmediacodec.Decoder, c hwmediacodec.Codec, stream []byte) (sums [][32]byte, sizes [][2]int) {
	t.Helper()
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
			if f.Format != hwmediacodec.NV12 || len(f.Planes) != 2 {
				t.Fatalf("unexpected frame layout: %s planes=%d", f.Format, len(f.Planes))
			}
			sums = append(sums, testutil.FrameChecksum(f))
			sizes = append(sizes, [2]int{f.Width, f.Height})
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
	return sums, sizes
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

// TestDecodeBFramesMatchesReference checks that streams with B-frames decode
// correctly. Without real presentation timestamps the frames come back in
// decode order, so the comparison is order-insensitive: every reference
// frame must appear exactly once.
func TestDecodeBFramesMatchesReference(t *testing.T) {
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		t.Run(c.String(), func(t *testing.T) {
			requireHardwareDecode(t, c)
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
			sortChecksums(got)
			sortChecksums(want)
			compareChecksums(t, got, want)
		})
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
