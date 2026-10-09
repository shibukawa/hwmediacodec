package mediafoundation

import "testing"

func TestHNSConversion(t *testing.T) {
	cases := []struct {
		pts   int64
		scale int32
		hns   int64
	}{
		{0, 90000, 0},
		{90000, 90000, hnsPerSecond},
		{3000, 90000, hnsPerSecond / 30},
		{1, 1000, 10000},
		{-90000, 90000, -hnsPerSecond},
	}
	for _, c := range cases {
		if got := toHNS(c.pts, c.scale); got != c.hns {
			t.Errorf("toHNS(%d, %d) = %d, want %d", c.pts, c.scale, got, c.hns)
		}
		if back := fromHNS(c.hns, c.scale); back != c.pts {
			t.Errorf("fromHNS(%d, %d) = %d, want %d", c.hns, c.scale, back, c.pts)
		}
	}
}

func TestHNSRoundTrip(t *testing.T) {
	// 90 kHz units do not divide 100 ns evenly; rounding in fromHNS keeps
	// the round trip exact, which is what frames need when the decoder
	// changes a timestamp and the exact-match lookup misses.
	for _, scale := range []int32{90000, 1000, 48000, 1_000_000, 10_000_000} {
		for _, pts := range []int64{0, 1, 2, 3003, 6006, 90000, 123456789, 1 << 40, -3003, -1} {
			if back := fromHNS(toHNS(pts, scale), scale); back != pts {
				t.Errorf("scale %d: round trip %d -> %d", scale, pts, back)
			}
		}
	}
}

func TestFrameRateRatio(t *testing.T) {
	cases := []struct {
		fps      float64
		num, den uint32
	}{
		{30, 30, 1},
		{60, 60, 1},
		{29.97, 30000, 1001},
		{59.94, 60000, 1001},
		{23.976, 24000, 1001},
		{12.5, 12500, 1000},
		{0, 30, 1},
		{-1, 30, 1},
	}
	for _, c := range cases {
		if n, d := frameRateRatio(c.fps); n != c.num || d != c.den {
			t.Errorf("frameRateRatio(%g) = %d/%d, want %d/%d", c.fps, n, d, c.num, c.den)
		}
	}
	if got := frameDurationHNS(30000, 1001); got != 333667 {
		t.Errorf("frameDurationHNS(30000/1001) = %d, want 333667", got)
	}
	if got := frameDurationHNS(30, 1); got != 333333 {
		t.Errorf("frameDurationHNS(30) = %d, want 333333", got)
	}
}
