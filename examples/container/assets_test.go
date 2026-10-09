package container_test

import (
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/examples/assets"
	"github.com/shibukawa/hwmediacodec/examples/container"
)

// TestBundledClip checks the embedded sample video the way the players
// see it; no ffmpeg or hardware needed.
func TestBundledClip(t *testing.T) {
	d, err := container.NewDemuxer(assets.Waterfall())
	if err != nil {
		t.Fatal(err)
	}
	v := d.Video()
	if v == nil {
		t.Fatal("no video track")
	}
	if v.Codec != hwmediacodec.HEVC || v.Width != 720 || v.Height != 1280 || v.SampleCount() != 293 {
		t.Fatalf("track %s %dx%d %d samples", v.Codec, v.Width, v.Height, v.SampleCount())
	}
	if fps := v.FrameRate(); fps < 29.6 || fps > 29.7 {
		t.Errorf("frame rate %.3f", fps)
	}
	if len(d.Others()) != 1 || d.Others()[0].Handler != "soun" {
		t.Errorf("other tracks: %d", len(d.Others()))
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
}
