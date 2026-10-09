//go:build windows && (amd64 || arm64)

package hwmediacodec_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// newTestEncoder opens a hardware encoder. Controls the vendor MFT does not
// offer (constant quality, CBR, B-frames, low latency, a profile) make the
// test skip rather than fail, so a run reports what the driver supports.
func newTestEncoder(t *testing.T, c hwmediacodec.Codec, opts ...hwmediacodec.EncoderOption) hwmediacodec.Encoder {
	t.Helper()
	requireHardware(t, c, hwmediacodec.Encode)
	opts = append([]hwmediacodec.EncoderOption{hwmediacodec.WithFrameRate(testFPS)}, opts...)
	enc, err := hwmediacodec.NewEncoder(context.Background(), c, encodeWidth, encodeHeight, opts...)
	if errors.Is(err, hwmediacodec.ErrUnsupported) && len(opts) > 1 {
		t.Skipf("encoder control not supported here: %v", err)
	}
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	t.Cleanup(func() { enc.Close() })
	return enc
}

func TestEncodeMatchesSource(t *testing.T) {
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		t.Run(c.String(), func(t *testing.T) {
			src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 60)
			enc := newTestEncoder(t, c, hwmediacodec.WithBitrate(1_000_000))
			pkts := encodeAll(t, enc, src, encodeWidth, encodeHeight)
			if len(pkts) != len(src) {
				t.Fatalf("got %d packets for %d frames", len(pkts), len(src))
			}
			if !pkts[0].Keyframe {
				t.Error("first packet is not a keyframe")
			}
			checkParameterSets(t, c, pkts)
			checkTimestamps(t, pkts, false)
			path := writeStream(t, "out."+c.String(), pkts)
			checkQuality(t, path, c, src, encodeWidth, encodeHeight)
			infos := testutil.ProbeFrames(t, path, c)
			if len(infos) != len(pkts) {
				t.Fatalf("ffprobe saw %d frames, want %d", len(infos), len(pkts))
			}
			for i, info := range infos {
				if info.Keyframe != pkts[i].Keyframe {
					t.Errorf("packet %d: ffprobe key_frame=%v, Packet.Keyframe=%v", i, info.Keyframe, pkts[i].Keyframe)
				}
				if info.PictType == "B" {
					t.Errorf("packet %d is a B-frame although B-frames are off", i)
				}
			}
		})
	}
}

// TestTranscodeRoundTrip runs the batch transcode path on Windows: hardware
// encode, then hardware decode, compared to the source.
func TestTranscodeRoundTrip(t *testing.T) {
	requireHardware(t, hwmediacodec.H264, hwmediacodec.Decode)
	src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 30)
	enc := newTestEncoder(t, hwmediacodec.H264, hwmediacodec.WithBitrate(1_500_000))
	pkts := encodeAll(t, enc, src, encodeWidth, encodeHeight)

	ctx := context.Background()
	dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var stream []byte
	for _, p := range pkts {
		stream = append(stream, p.Data...)
	}
	var decoded [][]byte
	collect := func() {
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return
			}
			if err != nil {
				t.Fatalf("decoder Receive: %v", err)
			}
			buf := make([]byte, 0, testutil.NV12FrameSize(f.Width, f.Height))
			rows := []int{f.Height, (f.Height + 1) / 2}
			rowBytes := []int{f.Width, (f.Width + 1) / 2 * 2}
			for i, p := range f.Planes {
				for r := 0; r < rows[i]; r++ {
					buf = append(buf, p[r*f.Strides[i]:r*f.Strides[i]+rowBytes[i]]...)
				}
			}
			f.Release()
			decoded = append(decoded, buf)
		}
	}
	for _, au := range splitAccessUnits(t, hwmediacodec.H264, stream) {
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: au}); err != nil {
			t.Fatalf("decoder Send: %v", err)
		}
		collect()
	}
	if err := dec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	collect()
	if len(decoded) != len(src) {
		t.Fatalf("decoded %d frames, want %d", len(decoded), len(src))
	}
	worst := 1e9
	for i := range src {
		if psnr := testutil.PSNRY(src[i], decoded[i], encodeWidth, encodeHeight); psnr < worst {
			worst = psnr
		}
	}
	t.Logf("worst luma PSNR %.2f dB", worst)
	if worst < minPSNR {
		t.Fatalf("worst luma PSNR %.2f dB is below %.0f dB", worst, minPSNR)
	}
}

func TestEncodeKeyframeInterval(t *testing.T) {
	src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 45)
	enc := newTestEncoder(t, hwmediacodec.H264, hwmediacodec.WithBitrate(800_000), hwmediacodec.WithKeyframeInterval(10))
	pkts := encodeAll(t, enc, src, encodeWidth, encodeHeight)
	checkParameterSets(t, hwmediacodec.H264, pkts)
	for i, p := range pkts {
		if i%10 == 0 && !p.Keyframe {
			t.Errorf("packet %d should be a keyframe", i)
		}
	}
	keys := 0
	for _, p := range pkts {
		if p.Keyframe {
			keys++
		}
	}
	t.Logf("%d keyframes in %d packets", keys, len(pkts))
	if keys > 10 {
		t.Errorf("too many keyframes: %d", keys)
	}
}

func TestEncodeForceKeyframe(t *testing.T) {
	src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 20)
	enc := newTestEncoder(t, hwmediacodec.H264, hwmediacodec.WithBitrate(800_000))
	pkts := encodeAll(t, enc, src, encodeWidth, encodeHeight, 7, 13)
	checkParameterSets(t, hwmediacodec.H264, pkts)
	for _, i := range []int{0, 7, 13} {
		if !pkts[i].Keyframe {
			t.Errorf("packet %d should be a forced keyframe", i)
		}
	}
	for _, i := range []int{1, 6, 8, 12, 14, 19} {
		if pkts[i].Keyframe {
			t.Errorf("packet %d should not be a keyframe", i)
		}
	}
}

func TestEncodeBFrames(t *testing.T) {
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		t.Run(c.String(), func(t *testing.T) {
			src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 60)
			enc := newTestEncoder(t, c, hwmediacodec.WithBitrate(1_000_000), hwmediacodec.WithBFrames())
			pkts := encodeAll(t, enc, src, encodeWidth, encodeHeight)
			if len(pkts) != len(src) {
				t.Fatalf("got %d packets for %d frames", len(pkts), len(src))
			}
			checkParameterSets(t, c, pkts)
			checkTimestamps(t, pkts, true)
			path := writeStream(t, "out."+c.String(), pkts)
			checkQuality(t, path, c, src, encodeWidth, encodeHeight)
			bframes, reordered := 0, 0
			for _, info := range testutil.ProbeFrames(t, path, c) {
				if info.PictType == "B" {
					bframes++
				}
			}
			for i := 1; i < len(pkts); i++ {
				if pkts[i].PTS < pkts[i-1].PTS {
					reordered++
				}
			}
			t.Logf("%d B-frames, %d reordered packets", bframes, reordered)
			if bframes > 0 && reordered == 0 {
				t.Error("B-frames present but packets are not in decode order")
			}
		})
	}
}

func TestEncodeBitrateControl(t *testing.T) {
	src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 90)
	size := func(opts ...hwmediacodec.EncoderOption) int {
		enc := newTestEncoder(t, hwmediacodec.H264, opts...)
		n := 0
		for _, p := range encodeAll(t, enc, src, encodeWidth, encodeHeight) {
			n += len(p.Data)
		}
		return n
	}
	seconds := float64(len(src)) / testFPS
	low := size(hwmediacodec.WithBitrate(400_000))
	high := size(hwmediacodec.WithBitrate(2_000_000))
	t.Logf("VBR 400k: %d bytes (%.0f kbit/s), VBR 2M: %d bytes (%.0f kbit/s)",
		low, float64(low)*8/seconds/1000, high, float64(high)*8/seconds/1000)
	if high < low*2 {
		t.Errorf("2 Mbit/s output (%d bytes) is not at least twice the 400 kbit/s output (%d bytes)", high, low)
	}
	t.Run("cbr", func(t *testing.T) {
		cbr := size(hwmediacodec.WithBitrate(1_000_000), hwmediacodec.WithRateControl(hwmediacodec.CBR))
		rate := float64(cbr) * 8 / seconds
		t.Logf("CBR 1M: %d bytes (%.0f kbit/s)", cbr, rate/1000)
		if rate < 600_000 || rate > 1_400_000 {
			t.Errorf("CBR 1 Mbit/s produced %.0f bit/s", rate)
		}
	})
}

func TestEncodeQuality(t *testing.T) {
	src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 30)
	size := func(q float64) int {
		enc := newTestEncoder(t, hwmediacodec.H264, hwmediacodec.WithQuality(q))
		pkts := encodeAll(t, enc, src, encodeWidth, encodeHeight)
		path := writeStream(t, "out.h264", pkts)
		checkQuality(t, path, hwmediacodec.H264, src, encodeWidth, encodeHeight)
		n := 0
		for _, p := range pkts {
			n += len(p.Data)
		}
		return n
	}
	lo, hi := size(0.5), size(0.9)
	t.Logf("quality 0.5: %d bytes, quality 0.9: %d bytes", lo, hi)
	if hi <= lo {
		t.Errorf("higher quality did not produce a larger stream (%d <= %d)", hi, lo)
	}
}

func TestEncodeLowLatency(t *testing.T) {
	src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 45)
	enc := newTestEncoder(t, hwmediacodec.H264, hwmediacodec.WithLowLatency(), hwmediacodec.WithBitrate(1_000_000), hwmediacodec.WithKeyframeInterval(15))
	pkts := encodeAll(t, enc, src, encodeWidth, encodeHeight)
	if len(pkts) != len(src) {
		t.Fatalf("got %d packets for %d frames", len(pkts), len(src))
	}
	checkParameterSets(t, hwmediacodec.H264, pkts)
	checkTimestamps(t, pkts, false)
	for i, p := range pkts {
		if i%15 == 0 && !p.Keyframe {
			t.Errorf("packet %d should be a keyframe", i)
		}
	}
	path := writeStream(t, "out.h264", pkts)
	checkQuality(t, path, hwmediacodec.H264, src, encodeWidth, encodeHeight)
}

func TestEncodeProfiles(t *testing.T) {
	src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 10)
	cases := []struct {
		codec   hwmediacodec.Codec
		profile hwmediacodec.Profile
		want    string
	}{
		{hwmediacodec.H264, hwmediacodec.ProfileBaseline, "Baseline"},
		{hwmediacodec.H264, hwmediacodec.ProfileMain, "Main"},
		{hwmediacodec.H264, hwmediacodec.ProfileHigh, "High"},
		{hwmediacodec.HEVC, hwmediacodec.ProfileMain, "Main"},
	}
	for _, tc := range cases {
		t.Run(tc.codec.String()+"/"+tc.profile.String(), func(t *testing.T) {
			enc := newTestEncoder(t, tc.codec, hwmediacodec.WithProfile(tc.profile), hwmediacodec.WithBitrate(800_000))
			pkts := encodeAll(t, enc, src, encodeWidth, encodeHeight)
			path := writeStream(t, "out."+tc.codec.String(), pkts)
			if got := testutil.ProbeProfile(t, path, tc.codec); !bytes.Contains([]byte(got), []byte(tc.want)) {
				t.Errorf("ffprobe profile %q, want it to contain %q", got, tc.want)
			}
		})
	}
	requireHardware(t, hwmediacodec.HEVC, hwmediacodec.Encode)
	_, err := hwmediacodec.NewEncoder(context.Background(), hwmediacodec.HEVC, encodeWidth, encodeHeight, hwmediacodec.WithProfile(hwmediacodec.ProfileBaseline))
	if !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Errorf("HEVC baseline: got %v, want ErrUnsupported", err)
	}
}

func TestEncodeFlushAndReuse(t *testing.T) {
	src := testutil.GenerateNV12Frames(t, encodeWidth, encodeHeight, 20)
	enc := newTestEncoder(t, hwmediacodec.H264, hwmediacodec.WithBitrate(800_000))
	first := encodeAll(t, enc, src[:10], encodeWidth, encodeHeight)
	if len(first) != 10 {
		t.Fatalf("first run: %d packets, want 10", len(first))
	}
	ctx := context.Background()
	if _, err := enc.Receive(ctx); err != io.EOF {
		t.Fatalf("Receive after Flush = %v, want io.EOF", err)
	}
	second := encodeAll(t, enc, src[10:], encodeWidth, encodeHeight)
	if len(second) != 10 {
		t.Fatalf("second run: %d packets, want 10", len(second))
	}
	if !second[0].Keyframe {
		t.Error("the first packet after Flush is not a keyframe")
	}
	checkParameterSets(t, hwmediacodec.H264, second)
}

func TestEncodeInvalidInput(t *testing.T) {
	enc := newTestEncoder(t, hwmediacodec.H264)
	ctx := context.Background()
	raw := make([]byte, testutil.NV12FrameSize(160, 120))
	err := enc.Send(ctx, testutil.NV12Frame(raw, 160, 120, 0))
	if !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Fatalf("wrong size: got %v, want ErrInvalidData", err)
	}
	short := testutil.NV12Frame(make([]byte, testutil.NV12FrameSize(encodeWidth, encodeHeight)), encodeWidth, encodeHeight, 0)
	short.Planes[1] = short.Planes[1][:10]
	if err := enc.Send(ctx, short); !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Fatalf("short plane: got %v, want ErrInvalidData", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Receive(ctx); !errors.Is(err, hwmediacodec.ErrClosed) {
		t.Fatalf("Receive after Close = %v, want ErrClosed", err)
	}
	if err := enc.Send(ctx, short); !errors.Is(err, hwmediacodec.ErrClosed) {
		t.Fatalf("Send after Close = %v, want ErrClosed", err)
	}
}
