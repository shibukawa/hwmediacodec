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
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		want := map[hwmediacodec.Codec]bool{hwmediacodec.H264: false, hwmediacodec.HEVC: false}
		for _, c := range caps {
			if c.Backend == "videotoolbox" && c.Direction == hwmediacodec.Decode && c.Hardware {
				want[c.Codec] = true
			}
		}
		for codec, found := range want {
			if !found {
				t.Errorf("expected videotoolbox hardware decode for %s on Apple Silicon", codec)
			}
		}
	} else if runtime.GOOS == "linux" {
		// With no GPU or no libva the list is empty; with one, every entry
		// comes from the VA-API backend.
		for _, c := range caps {
			if c.Backend != "vaapi" || !c.Hardware {
				t.Errorf("unexpected capability on linux: %+v", c)
			}
		}
	} else if runtime.GOOS != "darwin" && len(caps) != 0 {
		t.Errorf("no backend exists for %s yet, but Probe returned %d capabilities", runtime.GOOS, len(caps))
	}
}

func TestUnsupportedCodec(t *testing.T) {
	ctx := context.Background()
	_, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.AV1)
	if err == nil {
		t.Fatal("expected an error for AV1 decoding")
	}
	if !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("error does not match ErrUnsupported: %v", err)
	}
	var ue *hwmediacodec.UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("error is not *UnsupportedError: %T %v", err, err)
	}
	if ue.Codec != hwmediacodec.AV1 || ue.Direction != hwmediacodec.Decode || ue.Reason == "" {
		t.Fatalf("unexpected UnsupportedError contents: %+v", ue)
	}
	t.Log(err)
}

func TestUnsupportedOutputFormat(t *testing.T) {
	_, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.H264, hwmediacodec.WithOutputFormat(hwmediacodec.PixelFormat(200)))
	if !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for an unknown output format, got %v", err)
	}
}
