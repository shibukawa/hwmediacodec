package hwmediacodec_test

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/shibukawa/hwmediacodec"
)

func TestProbe(t *testing.T) {
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if caps == nil {
		t.Fatal("Probe returned a nil slice; want an empty slice when nothing is available")
	}
	for _, c := range caps {
		t.Logf("%s %s %s hardware=%v", c.Backend, c.Codec, c.Direction, c.Hardware)
	}
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		type key struct {
			codec hwmediacodec.Codec
			dir   hwmediacodec.Direction
		}
		want := map[key]bool{}
		for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
			want[key{c, hwmediacodec.Decode}] = false
			want[key{c, hwmediacodec.Encode}] = false
		}
		for _, c := range caps {
			if c.Backend == "videotoolbox" && c.Hardware {
				want[key{c.Codec, c.Direction}] = true
			}
		}
		for k, found := range want {
			if !found {
				t.Errorf("expected videotoolbox hardware %s for %s on Apple Silicon", k.dir, k.codec)
			}
		}
	case runtime.GOOS == "linux":
		// With no GPU, no libva, no libvpl and no NVIDIA driver the list
		// is empty; with one, every entry comes from a Linux backend.
		for _, c := range caps {
			if (c.Backend != "vaapi" && c.Backend != "nvidia" && c.Backend != "vpl") || !c.Hardware {
				t.Errorf("unexpected capability on linux: %+v", c)
			}
		}
	case runtime.GOOS == "windows" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"):
		// Hardware depends on the machine; only the backend identity is
		// fixed. The NVIDIA backend exists on amd64 only.
		for _, c := range caps {
			nvidia := c.Backend == "nvidia" && runtime.GOARCH == "amd64"
			if (c.Backend != "mediafoundation" && !nvidia) || !c.Hardware {
				t.Errorf("unexpected capability on windows: %+v", c)
			}
		}
	case runtime.GOOS != "darwin":
		if len(caps) != 0 {
			t.Errorf("no backend exists for %s yet, but Probe returned %d capabilities", runtime.GOOS, len(caps))
		}
	}
}

func TestUnsupportedCodec(t *testing.T) {
	ctx := context.Background()
	// An unknown codec value is unsupported everywhere; AV1 decoding is
	// unsupported wherever Probe lists no hardware for it (an Apple chip
	// older than the M3, a GPU without an AV1 decoder), and AV1 encoding
	// everywhere.
	unknown := hwmediacodec.Codec(200)
	_, err := hwmediacodec.NewDecoder(ctx, unknown)
	if err == nil {
		t.Fatal("expected an error for decoding an unknown codec")
	}
	if !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("error does not match ErrUnsupported: %v", err)
	}
	var ue *hwmediacodec.UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("error is not *UnsupportedError: %T %v", err, err)
	}
	if ue.Codec != unknown || ue.Direction != hwmediacodec.Decode || ue.Reason == "" {
		t.Fatalf("unexpected UnsupportedError contents: %+v", ue)
	}
	t.Log(err)

	if !hasHardware(t, hwmediacodec.AV1, hwmediacodec.Decode) {
		_, err = hwmediacodec.NewDecoder(ctx, hwmediacodec.AV1)
		if !errors.As(err, &ue) || !errors.Is(err, hwmediacodec.ErrUnsupported) || ue.Codec != hwmediacodec.AV1 || ue.Direction != hwmediacodec.Decode {
			t.Fatalf("AV1 decode without hardware: unexpected error %v", err)
		}
		t.Log(err)
	}

	_, err = hwmediacodec.NewEncoder(ctx, hwmediacodec.AV1, 640, 480)
	if !errors.As(err, &ue) {
		t.Fatalf("AV1 encode: error is not *UnsupportedError: %T %v", err, err)
	}
	if !errors.Is(err, hwmediacodec.ErrUnsupported) || ue.Codec != hwmediacodec.AV1 || ue.Direction != hwmediacodec.Encode {
		t.Fatalf("AV1 encode: unexpected error %v", err)
	}
	t.Log(err)
}

func TestUnsupportedOutputFormat(t *testing.T) {
	_, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.H264, hwmediacodec.WithOutputFormat(hwmediacodec.PixelFormat(200)))
	if !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for an unknown output format, got %v", err)
	}
}

func TestUnsupportedInputFormat(t *testing.T) {
	_, err := hwmediacodec.NewEncoder(context.Background(), hwmediacodec.H264, 640, 480, hwmediacodec.WithInputFormat(hwmediacodec.PixelFormat(200)))
	if !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for an unknown input format, got %v", err)
	}
}

// TestEncoderOptionValidation checks the option checks that run before any
// backend is consulted, so they behave the same on every platform.
func TestEncoderOptionValidation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		width, height int
		opts          []hwmediacodec.EncoderOption
	}{
		{"zero size", 0, 480, nil},
		{"negative bitrate", 640, 480, []hwmediacodec.EncoderOption{hwmediacodec.WithBitrate(-1)}},
		{"quality out of range", 640, 480, []hwmediacodec.EncoderOption{hwmediacodec.WithQuality(1.5)}},
		{"quality and bitrate", 640, 480, []hwmediacodec.EncoderOption{hwmediacodec.WithQuality(0.5), hwmediacodec.WithBitrate(1000)}},
		{"negative keyframe interval", 640, 480, []hwmediacodec.EncoderOption{hwmediacodec.WithKeyframeInterval(-1)}},
		{"bad time scale", 640, 480, []hwmediacodec.EncoderOption{hwmediacodec.WithTimeScale(0)}},
		{"bad rate control", 640, 480, []hwmediacodec.EncoderOption{hwmediacodec.WithRateControl(hwmediacodec.RateControl(9))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := hwmediacodec.NewEncoder(ctx, hwmediacodec.H264, tc.width, tc.height, tc.opts...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, hwmediacodec.ErrUnsupported) {
				t.Fatalf("option validation should not report ErrUnsupported: %v", err)
			}
			t.Log(err)
		})
	}
}

// TestSharedOptions checks that the options accepted by both constructors
// satisfy both option interfaces.
func TestSharedOptions(t *testing.T) {
	var _ hwmediacodec.DecoderOption = hwmediacodec.WithSoftwareFallback()
	var _ hwmediacodec.EncoderOption = hwmediacodec.WithSoftwareFallback()
	var _ hwmediacodec.DecoderOption = hwmediacodec.WithTimeScale(1000)
	var _ hwmediacodec.EncoderOption = hwmediacodec.WithTimeScale(1000)
}

func TestPixelFormatLayout(t *testing.T) {
	cases := []struct {
		f      hwmediacodec.PixelFormat
		planes int
		size   int
	}{
		{hwmediacodec.NV12, 2, 321*241 + 322*121},
		{hwmediacodec.RGBA, 1, 321 * 241 * 4},
		{hwmediacodec.BGRA, 1, 321 * 241 * 4},
	}
	for _, c := range cases {
		if got := c.f.PlaneCount(); got != c.planes {
			t.Errorf("%s: PlaneCount = %d, want %d", c.f, got, c.planes)
		}
		if got := c.f.FrameSize(321, 241); got != c.size {
			t.Errorf("%s: FrameSize = %d, want %d", c.f, got, c.size)
		}
		if rows, rowBytes := c.f.PlaneLayout(c.planes, 321, 241); rows != 0 || rowBytes != 0 {
			t.Errorf("%s: plane %d should not exist", c.f, c.planes)
		}
	}
	if rows, rowBytes := hwmediacodec.NV12.PlaneLayout(1, 321, 241); rows != 121 || rowBytes != 322 {
		t.Errorf("NV12 chroma plane = %dx%d", rowBytes, rows)
	}
	if s := hwmediacodec.PixelFormat(200).String(); s != "pixelformat(200)" {
		t.Errorf("unknown format string %q", s)
	}
}
