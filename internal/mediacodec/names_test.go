package mediacodec

import (
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

func TestIsSoftwareCodec(t *testing.T) {
	cases := map[string]bool{
		"c2.android.avc.decoder":     true,
		"OMX.google.h264.decoder":    true,
		"c2.android.av1.decoder":     true,
		"OMX.SEC.avc.sw.dec":         true,
		"c2.qti.avc.decoder":         false,
		"c2.exynos.h264.decoder":     false,
		"c2.mtk.avc.decoder":         false,
		"OMX.qcom.video.decoder.avc": false,
		"c2.google.av1.decoder":      false, // Pixel's hardware AV1 goes by this name
	}
	for name, want := range cases {
		if got := isSoftwareCodec(name); got != want {
			t.Errorf("isSoftwareCodec(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestProfileLevel(t *testing.T) {
	if p, l, ok := profileLevel(codec.H264, codec.ProfileHigh, 1920, 1080); !ok || p != sys.AVCProfileHigh || l != sys.AVCLevel41 {
		t.Errorf("H.264 High 1080p = %d %#x %v", p, l, ok)
	}
	if _, l, ok := profileLevel(codec.H264, codec.ProfileMain, 3840, 2160); !ok || l != sys.AVCLevel51 {
		t.Errorf("H.264 Main 4K level = %#x %v", l, ok)
	}
	if p, l, ok := profileLevel(codec.HEVC, codec.ProfileMain, 3840, 2160); !ok || p != sys.HEVCProfileMain || l != sys.HEVCMainTierLevel51 {
		t.Errorf("HEVC Main 4K = %d %#x %v", p, l, ok)
	}
	if _, _, ok := profileLevel(codec.HEVC, codec.ProfileHigh, 640, 480); ok {
		t.Error("HEVC has no High profile")
	}
	if _, _, ok := profileLevel(codec.AV1, codec.ProfileMain, 640, 480); ok {
		t.Error("AV1 profiles are not mapped")
	}
}
