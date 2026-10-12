package webcodecs

import (
	"bytes"
	"reflect"
	"regexp"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
)

func TestHEVCCodecStrings(t *testing.T) {
	cases := []struct {
		name string
		ptl  hevc.ProfileTierLevel
		want string
	}{
		// The examples of ISO/IEC 14496-15 Annex E.
		{"main", hevc.ProfileTierLevel{ProfileIDC: 1, CompatibilityFlags: 0x60000000, ProgressiveSource: true, NonPackedConstraint: true, FrameOnlyConstraint: true, LevelIDC: 93}, "1.6.L93.B0"},
		{"main10 high tier", hevc.ProfileTierLevel{ProfileIDC: 2, CompatibilityFlags: 0x20000000, Tier: true, ProgressiveSource: true, FrameOnlyConstraint: true, LevelIDC: 153}, "2.4.H153.90"},
		{"profile space", hevc.ProfileTierLevel{ProfileSpace: 1, ProfileIDC: 1, CompatibilityFlags: 0x60000000, LevelIDC: 120}, "A1.6.L120"},
		// Range extension: constraint flags beyond the first byte, and
		// zero bytes in the middle that must stay.
		{"rext", hevc.ProfileTierLevel{ProfileIDC: 4, CompatibilityFlags: 0x08000000, ProgressiveSource: true, FrameOnlyConstraint: true, ConstraintBits: 0x0_00_00_08_00_00, LevelIDC: 123}, "4.10.L123.90.0.0.8"},
	}
	for _, c := range cases {
		got := hevcCodecStrings(&c.ptl)
		want := []string{"hev1." + c.want, "hvc1." + c.want}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", c.name, got, want)
		}
	}
}

func TestLevels(t *testing.T) {
	for _, c := range []struct {
		w, h       int
		fps        float64
		h264, hevc uint8
	}{
		{320, 240, 30, 31, 93},
		{1280, 720, 30, 31, 93},
		{1280, 720, 60, 32, 120},
		{1920, 1080, 30, 40, 120},
		{1920, 1080, 0, 40, 120}, // unknown rate counts as 30
		{1920, 1080, 60, 42, 123},
		{3840, 2160, 30, 51, 150},
		{3840, 2160, 60, 52, 153},
		{7680, 4320, 30, 60, 180},
	} {
		if got := h264Level(c.w, c.h, c.fps); got != c.h264 {
			t.Errorf("h264Level(%dx%d@%g) = %d, want %d", c.w, c.h, c.fps, got, c.h264)
		}
		if got := hevcLevel(c.w, c.h, c.fps); got != c.hevc {
			t.Errorf("hevcLevel(%dx%d@%g) = %d, want %d", c.w, c.h, c.fps, got, c.hevc)
		}
	}
}

func TestEncodeCodecStrings(t *testing.T) {
	for _, c := range []struct {
		codec   codec.Codec
		profile codec.Profile
		want    []string
	}{
		{codec.H264, codec.ProfileDefault, []string{"avc1.640028", "avc1.4D0028", "avc1.42E028", "avc1.420028"}},
		{codec.H264, codec.ProfileHigh, []string{"avc1.640028"}},
		{codec.H264, codec.ProfileMain, []string{"avc1.4D0028"}},
		{codec.H264, codec.ProfileBaseline, []string{"avc1.42E028", "avc1.420028"}},
		{codec.HEVC, codec.ProfileDefault, []string{"hvc1.1.6.L120.B0", "hev1.1.6.L120.B0"}},
		{codec.HEVC, codec.ProfileMain, []string{"hvc1.1.6.L120.B0", "hev1.1.6.L120.B0"}},
		{codec.HEVC, codec.ProfileHigh, nil},
		{codec.HEVC, codec.ProfileBaseline, nil},
		{codec.AV1, codec.ProfileDefault, nil},
	} {
		got, ok := encodeCodecStrings(c.codec, c.profile, 1920, 1080, 30)
		if ok != (c.want != nil) || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %s: got %v %v, want %v", c.codec, c.profile, got, ok, c.want)
		}
	}
}

func TestQuantizer(t *testing.T) {
	for q, want := range map[float64]int{1: 6, 0.5: 29, 0.01: 51, 0.8: 15} {
		if got := quantizer(q); got != want {
			t.Errorf("quantizer(%g) = %d, want %d", q, got, want)
		}
	}
}

func TestToMicros(t *testing.T) {
	for _, c := range []struct {
		ts    int64
		scale int32
		want  int64
	}{
		{0, 90000, 0}, {90000, 90000, 1_000_000}, {3000, 90000, 33333}, {1, 30, 33333}, {2, 30, 66667}, {-3000, 90000, -33333},
		{123456, 1_000_000, 123456},
	} {
		if got := toMicros(c.ts, c.scale); got != c.want {
			t.Errorf("toMicros(%d, %d) = %d, want %d", c.ts, c.scale, got, c.want)
		}
	}
}

var dashCodecsRe = regexp.MustCompile(`codecs="([^"]+)"`)

// dashCodecs asks ffmpeg's DASH muxer for the codec string of a stream.
func dashCodecs(t *testing.T, path string) string {
	t.Helper()
	mpd := testutil.TempDir(t) + "/manifest.mpd"
	cmd := testutil.Command(testutil.RequireFFmpeg(t), "-hide_banner", "-loglevel", "error", "-y",
		"-fflags", "+genpts", "-r", "30", "-i", path, "-c", "copy", "-f", "dash", mpd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg dash failed: %v\n%s", err, out)
	}
	m := dashCodecsRe.FindSubmatch(testutil.ReadFile(t, mpd))
	if m == nil {
		t.Skip("ffmpeg's DASH muxer wrote no codec string")
	}
	return string(m[1])
}

// TestStreamCodecStringsMatchFFmpeg compares the codec strings derived
// from the parameter sets with the ones ffmpeg writes into a DASH manifest
// for the same stream. (ffmpeg writes none for HEVC there; the HEVC
// strings are proven by the browser accepting them, see
// scripts/browser_test.sh.)
func TestStreamCodecStringsMatchFFmpeg(t *testing.T) {
	t.Run("h264", func(t *testing.T) {
		for _, extra := range [][]string{nil, {"-profile:v", "main"}, {"-profile:v", "baseline"}} {
			path := testutil.GenerateH264(t, 640, 360, 5, extra...)
			var got string
			for _, nal := range annexb.Split(testutil.ReadFile(t, path)) {
				if annexb.NALUnitType(codec.H264, nal) == annexb.H264NALSPS {
					sps, err := h264.ParseSPS(nal)
					if err != nil {
						t.Fatal(err)
					}
					got = h264StreamCodecString(sps)
				}
			}
			if want := dashCodecs(t, path); !bytes.EqualFold([]byte(got), []byte(want)) {
				t.Errorf("%v: got %s, ffmpeg writes %s", extra, got, want)
			}
		}
	})
	t.Run("av1", func(t *testing.T) {
		for _, extra := range [][]string{nil, {"-pix_fmt", "yuv420p10le"}} {
			path := testutil.GenerateAV1(t, 640, 360, 5, extra...)
			r, err := ivf.NewReader(bytes.NewReader(testutil.ReadFile(t, path)))
			if err != nil {
				t.Fatal(err)
			}
			unit, _, err := r.Next()
			if err != nil {
				t.Fatal(err)
			}
			tu, err := av1.ParseTemporalUnit(unit, nil)
			if err != nil || tu.Sequence == nil {
				t.Fatalf("first temporal unit: %v", err)
			}
			got := av1CodecString(tu.Sequence)
			if want := dashCodecs(t, path); got != want {
				t.Errorf("%v: got %s, ffmpeg writes %s", extra, got, want)
			}
		}
	})
}
