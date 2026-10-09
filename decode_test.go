package hwmediacodec_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"sort"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// Helpers shared by the platform-specific decoder conformance tests.

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

func sortChecksums(s [][32]byte) {
	sort.Slice(s, func(i, j int) bool { return bytes.Compare(s[i][:], s[j][:]) < 0 })
}

// splitAccessUnits returns every access unit of an Annex-B stream.
func splitAccessUnits(t *testing.T, c hwmediacodec.Codec, data []byte) [][]byte {
	t.Helper()
	r := annexb.NewReader(bytes.NewReader(data), c)
	var aus [][]byte
	for {
		au, err := r.Next()
		if err == io.EOF {
			return aus
		}
		if err != nil {
			t.Fatal(err)
		}
		aus = append(aus, au)
	}
}
