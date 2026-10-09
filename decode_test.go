package hwmediacodec_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// Helpers shared by the platform-specific conformance tests.

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
