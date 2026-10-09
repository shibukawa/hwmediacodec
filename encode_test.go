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

// newTestEncoder opens a hardware encoder. A control the backend on this
// machine does not offer (constant quality, CBR, B-frames, low latency, a
// profile) makes the test skip rather than fail, so a run reports what the
// driver supports.
func newTestEncoder(t *testing.T, c hwmediacodec.Codec, opts ...hwmediacodec.EncoderOption) hwmediacodec.Encoder {
	t.Helper()
	requireHardwareEncode(t, c)
	opts = append([]hwmediacodec.EncoderOption{hwmediacodec.WithFrameRate(testFPS)}, opts...)
	enc, err := hwmediacodec.NewEncoder(context.Background(), c, encodeWidth, encodeHeight, opts...)
	if errors.Is(err, hwmediacodec.ErrUnsupported) && len(opts) > 1 {
		t.Skipf("encoder control not supported on this backend: %v", err)
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

// TestTranscodeRoundTrip runs the end-to-end batch transcode path: hardware
// encode, then hardware decode, compared to the source.
func TestTranscodeRoundTrip(t *testing.T) {
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
	r := annexb.NewReader(bytes.NewReader(stream), hwmediacodec.H264)
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: au}); err != nil {
			t.Fatalf("decoder Send: %v", err)
		}
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				break
			}
			if err != nil {
				t.Fatalf("decoder Receive: %v", err)
			}
			decoded = append(decoded, testutil.FrameBytes(f))
			f.Release()
		}
	}
	// The hardware encoder writes no VUI reorder bound, so the display-order
	// decoder holds the DPB depth back until Flush.
	if err := dec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := dec.Receive(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, testutil.FrameBytes(f))
		f.Release()
	}
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

// TestEncodeRGBAInput feeds packed RGB frames (what ebiten.Image.ReadPixels
// produces) and compares ffmpeg's RGB decode of the result with the source.
// The stream must declare the matrix the hardware used for the conversion
// (BT.709 on VideoToolbox, BT.601 on NVENC), since the reference decode
// converts back with the declared one; decoding with the wrong matrix
// scores about 28 dB here, the right one about 39 dB (8x8-block PSNR, see
// TestDecodeRGBA).
func TestEncodeRGBAInput(t *testing.T) {
	const minRGBPSNR = 35.0
	for _, f := range []hwmediacodec.PixelFormat{hwmediacodec.RGBA, hwmediacodec.BGRA} {
		t.Run(f.String(), func(t *testing.T) {
			src := testutil.GenerateRawFrames(t, f, encodeWidth, encodeHeight, 30)
			requireHardwareEncode(t, hwmediacodec.H264)
			ctx := context.Background()
			enc, err := hwmediacodec.NewEncoder(ctx, hwmediacodec.H264, encodeWidth, encodeHeight,
				hwmediacodec.WithFrameRate(testFPS), hwmediacodec.WithBitrate(1_500_000), hwmediacodec.WithInputFormat(f))
			if errors.Is(err, hwmediacodec.ErrUnsupported) {
				t.Skipf("%s input not available on this backend: %v", f, err)
			}
			if err != nil {
				t.Fatalf("NewEncoder: %v", err)
			}
			defer enc.Close()
			var pkts []hwmediacodec.Packet
			for i, raw := range src {
				fr := testutil.RawFrame(raw, f, encodeWidth, encodeHeight, int64(i)*testPTSStep)
				if err := enc.Send(ctx, fr); err != nil {
					t.Fatalf("Send frame %d: %v", i, err)
				}
				for {
					p, err := enc.Receive(ctx)
					if errors.Is(err, hwmediacodec.ErrAgain) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					pkts = append(pkts, p)
				}
			}
			if err := enc.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			for {
				p, err := enc.Receive(ctx)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				pkts = append(pkts, p)
			}
			if len(pkts) != len(src) {
				t.Fatalf("got %d packets for %d frames", len(pkts), len(src))
			}
			path := writeStream(t, "rgb.h264", pkts)
			switch cs := testutil.ProbeStreamField(t, path, hwmediacodec.H264, "color_space"); cs {
			case "bt709", "bt470bg", "smpte170m":
			default:
				t.Errorf("stream declares colour matrix %q, want bt709 or a BT.601 matrix", cs)
			}
			got := testutil.ReferenceFrames(t, path, hwmediacodec.H264, f, encodeWidth, encodeHeight)
			if len(got) != len(src) {
				t.Fatalf("ffmpeg decoded %d frames, want %d", len(got), len(src))
			}
			worst := 1e9
			for i := range src {
				if psnr := testutil.BlockPSNR(src[i], got[i], encodeWidth, encodeHeight, 8); psnr < worst {
					worst = psnr
				}
			}
			t.Logf("worst %s 8x8-block PSNR %.2f dB over %d frames", f, worst, len(src))
			if worst < minRGBPSNR {
				t.Fatalf("worst PSNR %.2f dB is below %.0f dB", worst, minRGBPSNR)
			}
		})
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
	cbr := size(hwmediacodec.WithBitrate(1_000_000), hwmediacodec.WithRateControl(hwmediacodec.CBR))
	t.Logf("VBR 400k: %d bytes (%.0f kbit/s), VBR 2M: %d bytes (%.0f kbit/s), CBR 1M: %d bytes (%.0f kbit/s)",
		low, float64(low)*8/seconds/1000, high, float64(high)*8/seconds/1000, cbr, float64(cbr)*8/seconds/1000)
	if high < low*2 {
		t.Errorf("2 Mbit/s output (%d bytes) is not at least twice the 400 kbit/s output (%d bytes)", high, low)
	}
	if rate := float64(cbr) * 8 / seconds; rate < 600_000 || rate > 1_400_000 {
		t.Errorf("CBR 1 Mbit/s produced %.0f bit/s", rate)
	}
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
