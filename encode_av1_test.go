package hwmediacodec_test

import (
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// TestEncodeAV1RoundTrip encodes with each rate control mode, checks the
// temporal units (a sequence header in front of every key frame and only
// there, no temporal delimiters, Keyframe set on exactly the key frames)
// and decodes them back with any backend. The size is a multiple of 64x16,
// which every AV1 encoder codes as it is.
func TestEncodeAV1RoundTrip(t *testing.T) {
	const w, h = 320, 240
	for _, tc := range []struct {
		name string
		opts []hwmediacodec.EncoderOption
	}{
		{"default", nil},
		{"bitrate", []hwmediacodec.EncoderOption{hwmediacodec.WithBitrate(1_000_000)}},
		{"cbr", []hwmediacodec.EncoderOption{hwmediacodec.WithBitrate(1_000_000), hwmediacodec.WithRateControl(hwmediacodec.CBR)}},
		{"quality", []hwmediacodec.EncoderOption{hwmediacodec.WithQuality(0.8)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireHardwareEncode(t, hwmediacodec.AV1)
			opts := append([]hwmediacodec.EncoderOption{hwmediacodec.WithFrameRate(testFPS), hwmediacodec.WithKeyframeInterval(10)}, tc.opts...)
			enc, err := hwmediacodec.NewEncoder(t.Context(), hwmediacodec.AV1, w, h, opts...)
			if err != nil {
				t.Fatalf("NewEncoder: %v", err)
			}
			defer enc.Close()
			src := gradientNV12(w, h, 30)
			pkts := encodeAll(t, enc, src, w, h, 15)
			if len(pkts) != len(src) {
				t.Fatalf("got %d packets for %d frames", len(pkts), len(src))
			}
			checkTimestamps(t, pkts, false)

			var seq *av1.SequenceHeader
			units := make([][]byte, len(pkts))
			for i, p := range pkts {
				units[i] = p.Data
				tu, err := av1.ParseTemporalUnit(p.Data, seq)
				if err != nil {
					t.Fatalf("packet %d: %v", i, err)
				}
				if tu.Sequence != nil {
					seq = tu.Sequence
				}
				wantKey := i%10 == 0 || i == 15
				if p.Keyframe != wantKey || tu.Keyframe != wantKey {
					t.Errorf("packet %d: Keyframe %v, key frame in the stream %v, want %v", i, p.Keyframe, tu.Keyframe, wantKey)
				}
				if (tu.SequenceHeader != nil) != wantKey {
					t.Errorf("packet %d: sequence header present %v, want %v", i, tu.SequenceHeader != nil, wantKey)
				}
				for _, o := range tu.OBUs {
					if o.Type == av1.OBUTemporalDelimiter {
						t.Errorf("packet %d carries a temporal delimiter", i)
					}
				}
			}
			if seq == nil || seq.MaxFrameWidth != w || seq.MaxFrameHeight != h || seq.BitDepth != 8 {
				t.Fatalf("sequence header %+v", seq)
			}

			t.Setenv("HWMEDIACODEC_BACKENDS", "")
			dec := newDecoderOrSkip(t, hwmediacodec.AV1)
			defer dec.Close()
			frames := decodePackets(t, dec, units, hwmediacodec.NV12)
			if len(frames) != len(src) {
				t.Fatalf("decoded %d frames, want %d", len(frames), len(src))
			}
			worst := 1e9
			for i, f := range frames {
				if f.width != w || f.height != h {
					t.Fatalf("frame %d decoded at %dx%d, want %dx%d", i, f.width, f.height, w, h)
				}
				worst = min(worst, testutil.PSNRY(src[i], f.pix, w, h))
			}
			t.Logf("worst luma PSNR %.2f dB", worst)
			if worst < minPSNR {
				t.Errorf("worst luma PSNR %.1f dB, want at least %.0f", worst, minPSNR)
			}
		})
	}
}
