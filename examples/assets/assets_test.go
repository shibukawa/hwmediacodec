package assets_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/assets"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// clipCodecs names the codec of every entry of assets.Clips, in order.
var clipCodecs = []hwmediacodec.Codec{hwmediacodec.HEVC, hwmediacodec.H264}

func video(t *testing.T, clip assets.Clip) *mp4.VideoTrack {
	t.Helper()
	d, err := mp4.NewDemuxer(clip.Open())
	if err != nil {
		t.Fatal(err)
	}
	v := d.Video()
	if v == nil {
		t.Fatal("no video track")
	}
	if len(d.Others()) != 1 || d.Others()[0].Handler != "soun" {
		t.Errorf("other tracks: %d", len(d.Others()))
	}
	return v
}

// TestBundledClips checks the embedded sample video the way the players
// see it, in both encodings; no ffmpeg or hardware needed. The two files
// are the same clip: same picture size, frame count, timing and audio.
func TestBundledClips(t *testing.T) {
	clips := assets.Clips()
	if len(clips) != len(clipCodecs) {
		t.Fatalf("%d clips", len(clips))
	}
	if clips[0].Name != assets.WaterfallName || clips[1].Name != assets.WaterfallH264Name {
		t.Fatalf("clips %s, %s", clips[0].Name, clips[1].Name)
	}
	for i, clip := range clips {
		t.Run(clip.Name, func(t *testing.T) {
			v := video(t, clip)
			// The codec is read from the file's sample entry.
			if v.Codec != clipCodecs[i] || v.Width != 720 || v.Height != 1280 || v.SampleCount() != 293 {
				t.Fatalf("track %s %dx%d %d samples", v.Codec, v.Width, v.Height, v.SampleCount())
			}
			if fps := v.FrameRate(); fps < 29.6 || fps > 29.7 {
				t.Errorf("frame rate %.3f", fps)
			}
			ps := v.PacketSource()
			if l := ps.Length(); l < 9800*time.Millisecond || l > 9900*time.Millisecond {
				t.Errorf("length %v", l)
			}
			if kf := v.Keyframes(); len(kf) < 1 || kf[0] != 0 {
				t.Errorf("keyframes %v", kf)
			}
			// Every access unit converts (parameter sets in front of the keyframes).
			for i := range v.SampleCount() {
				au, err := v.AccessUnit(i)
				if err != nil {
					t.Fatal(err)
				}
				if len(au.Data) < 5 || au.Data[0] != 0 || au.Data[3] != 1 {
					t.Fatalf("sample %d is not Annex-B", i)
				}
			}
		})
	}
}

// TestAV1Clip checks that the AV1 encoding is the same clip as the others.
func TestAV1Clip(t *testing.T) {
	v := video(t, assets.AV1())
	if v.Codec != hwmediacodec.AV1 || v.Width != 720 || v.Height != 1280 || v.SampleCount() != 293 {
		t.Fatalf("track %s %dx%d %d samples", v.Codec, v.Width, v.Height, v.SampleCount())
	}
	if fps := v.FrameRate(); fps < 29.6 || fps > 29.7 {
		t.Errorf("frame rate %.3f", fps)
	}
	if kf := v.Keyframes(); len(kf) < 2 || kf[0] != 0 {
		t.Errorf("keyframes %v", kf)
	}
	for i := range v.SampleCount() {
		if au, err := v.AccessUnit(i); err != nil || len(au.Data) == 0 {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
}

// TestBundledClipsDecode decodes the first second of every encoding with
// the hardware decoder, to RGBA as the players do, and skips the ones the
// machine has no decoder for. It runs natively and, through
// scripts/browser_test.sh, in a browser: that is where the two encodings
// part ways (Firefox decodes the H.264 one only).
func TestBundledClipsDecode(t *testing.T) {
	ctx := context.Background()
	decoded := 0
	for i, clip := range assets.Clips() {
		t.Run(clip.Name, func(t *testing.T) {
			src := video(t, clip).PacketSource()
			dec, err := hwmediacodec.NewDecoder(ctx, clipCodecs[i],
				hwmediacodec.WithOutputFormat(hwmediacodec.RGBA), hwmediacodec.WithTimeScale(src.TimeScale()))
			if errors.Is(err, hwmediacodec.ErrUnsupported) {
				t.Skipf("no decoder here: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer dec.Close()
			frames := hwmediacodec.NewDecodeReader(dec, src)
			const want = 30
			last := int64(-1)
			for n := 0; n < want; n++ {
				f, err := frames.ReadFrame(ctx)
				if err == io.EOF {
					t.Fatalf("stream ended after %d frames", n)
				}
				if errors.Is(err, hwmediacodec.ErrUnsupported) {
					t.Skipf("no decoder here: %v", err)
				}
				if err != nil {
					t.Fatalf("frame %d: %v", n, err)
				}
				if f.Width != 720 || f.Height != 1280 || f.Format != hwmediacodec.RGBA || len(f.Planes[0]) != 720*1280*4 {
					t.Fatalf("frame %d is %dx%d %s with %d bytes", n, f.Width, f.Height, f.Format, len(f.Planes[0]))
				}
				if f.PTS <= last {
					t.Fatalf("frame %d has PTS %d after %d: not in display order", n, f.PTS, last)
				}
				last = f.PTS
				if n == 0 {
					// A waterfall in daylight is neither black nor flat.
					lo, hi := byte(255), byte(0)
					for _, v := range f.Planes[0][:720*4*200] {
						lo, hi = min(lo, v), max(hi, v)
					}
					if hi-lo < 64 {
						t.Errorf("first frame looks flat: samples %d..%d", lo, hi)
					}
				}
				f.Release()
			}
			decoded++
		})
	}
	if decoded == 0 {
		t.Skip("no hardware decoder for either encoding on this machine")
	}
}
