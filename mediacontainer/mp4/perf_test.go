package mp4_test

import (
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

func openVideo(tb testing.TB) *mp4.VideoTrack {
	tb.Helper()
	src := mediatest.GenerateMP4(tb, tb.TempDir(), mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 30, BFrames: 2, GOP: 10})
	d, err := mp4.Open(src)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { d.Close() })
	return d.Video()
}

// TestAccessUnitAllocations pins the one allocation per access unit: the
// sample is read into the buffer that is returned and the length prefixes
// become start codes in place. (It was two, with a copy, before.)
func TestAccessUnitAllocations(t *testing.T) {
	v := openVideo(t)
	for _, i := range []int{0, 1} {
		allocs := testing.AllocsPerRun(50, func() {
			if _, err := v.AccessUnit(i); err != nil {
				t.Fatal(err)
			}
		})
		t.Logf("sample %d (keyframe=%v): %.1f allocations per AccessUnit", i, i == 0, allocs)
		if allocs > 1 {
			t.Errorf("sample %d: %.1f allocations per AccessUnit, want 1", i, allocs)
		}
	}
}

func BenchmarkAccessUnit(b *testing.B) {
	v := openVideo(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := v.AccessUnit(i % v.SampleCount()); err != nil {
			b.Fatal(err)
		}
	}
}
