package hwmediacodec_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// Helpers shared by the platform-specific encoder conformance tests.

const (
	testFPS      = 30
	testPTSStep  = int64(hwmediacodec.DefaultTimeScale) / testFPS
	minPSNR      = 30.0 // dB; the hardware encoder at the test bitrates stays well above this
	encodeWidth  = 320
	encodeHeight = 240
)

// encodeAll feeds every raw frame to enc and returns the packets in output
// order. forceAt lists frame indexes that request a keyframe.
func encodeAll(t *testing.T, enc hwmediacodec.Encoder, frames [][]byte, width, height int, forceAt ...int) []hwmediacodec.Packet {
	t.Helper()
	ctx := context.Background()
	var pkts []hwmediacodec.Packet
	drain := func(final bool) {
		for {
			p, err := enc.Receive(ctx)
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
			if len(p.Data) == 0 {
				t.Fatal("Receive returned an empty packet")
			}
			pkts = append(pkts, p)
		}
	}
	for i, raw := range frames {
		f := testutil.NV12Frame(raw, width, height, int64(i)*testPTSStep)
		for _, idx := range forceAt {
			if idx == i {
				f.ForceKeyframe = true
			}
		}
		if err := enc.Send(ctx, f); err != nil {
			t.Fatalf("Send frame %d: %v", i, err)
		}
		drain(false)
	}
	if err := enc.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	drain(true)
	return pkts
}

func writeStream(t *testing.T, name string, pkts []hwmediacodec.Packet) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	var buf bytes.Buffer
	for _, p := range pkts {
		buf.Write(p.Data)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// nalTypes returns the NAL unit types of an access unit in order.
func nalTypes(c hwmediacodec.Codec, au []byte) []int {
	var out []int
	for _, nal := range annexb.Split(au) {
		out = append(out, annexb.NALUnitType(c, nal))
	}
	return out
}

func hasNAL(types []int, want int) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

// checkParameterSets verifies that every keyframe carries in-band parameter
// sets and that no other packet is marked as a keyframe.
func checkParameterSets(t *testing.T, c hwmediacodec.Codec, pkts []hwmediacodec.Packet) {
	t.Helper()
	for i, p := range pkts {
		types := nalTypes(c, p.Data)
		keyNAL := false
		for _, ty := range types {
			if annexb.IsVCL(c, ty) && annexb.IsKeyframe(c, ty) {
				keyNAL = true
			}
		}
		if keyNAL != p.Keyframe {
			t.Errorf("packet %d: Keyframe=%v but NAL types %v", i, p.Keyframe, types)
		}
		if !p.Keyframe {
			continue
		}
		switch c {
		case hwmediacodec.H264:
			if !hasNAL(types, annexb.H264NALSPS) || !hasNAL(types, annexb.H264NALPPS) {
				t.Errorf("packet %d is a keyframe without SPS/PPS: %v", i, types)
			}
		case hwmediacodec.HEVC:
			if !hasNAL(types, annexb.HEVCNALVPS) || !hasNAL(types, annexb.HEVCNALSPS) || !hasNAL(types, annexb.HEVCNALPPS) {
				t.Errorf("packet %d is a keyframe without VPS/SPS/PPS: %v", i, types)
			}
		}
	}
}

// checkTimestamps verifies that PTS values are the ones sent and, without
// B-frames, that packets arrive in presentation order with DTS == PTS.
func checkTimestamps(t *testing.T, pkts []hwmediacodec.Packet, bframes bool) {
	t.Helper()
	seen := map[int64]bool{}
	for i, p := range pkts {
		if p.PTS%testPTSStep != 0 || seen[p.PTS] {
			t.Errorf("packet %d: unexpected or duplicate PTS %d", i, p.PTS)
		}
		seen[p.PTS] = true
		if !bframes {
			if p.PTS != int64(i)*testPTSStep {
				t.Errorf("packet %d: PTS %d, want %d (presentation order without B-frames)", i, p.PTS, int64(i)*testPTSStep)
			}
			if p.DTS != p.PTS {
				t.Errorf("packet %d: DTS %d != PTS %d without B-frames", i, p.DTS, p.PTS)
			}
		} else if i > 0 && p.DTS < pkts[i-1].DTS {
			t.Errorf("packet %d: DTS %d goes backwards from %d", i, p.DTS, pkts[i-1].DTS)
		}
	}
}

// checkQuality decodes the stream with ffmpeg and compares every frame to the
// source by luma PSNR.
func checkQuality(t *testing.T, path string, c hwmediacodec.Codec, src [][]byte, width, height int) {
	t.Helper()
	got := testutil.ReferenceNV12Frames(t, path, c, width, height)
	if len(got) != len(src) {
		t.Fatalf("ffmpeg decoded %d frames, want %d", len(got), len(src))
	}
	worst := 1e9
	for i := range src {
		if psnr := testutil.PSNRY(src[i], got[i], width, height); psnr < worst {
			worst = psnr
		}
	}
	t.Logf("worst luma PSNR %.2f dB over %d frames", worst, len(src))
	if worst < minPSNR {
		t.Fatalf("worst luma PSNR %.2f dB is below %.0f dB", worst, minPSNR)
	}
}
