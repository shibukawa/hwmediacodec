package mediafoundation

import "math"

// Media Foundation timestamps are in 100-nanosecond units.
const hnsPerSecond = 10_000_000

// toHNS converts a timestamp in timeScale units per second to 100 ns units
// (truncating). The arithmetic is split so that large values do not overflow.
func toHNS(pts int64, timeScale int32) int64 {
	ts := int64(timeScale)
	return pts/ts*hnsPerSecond + (pts%ts)*hnsPerSecond/ts
}

// fromHNS converts a 100 ns timestamp back to timeScale units per second,
// rounding to the nearest unit so that toHNS followed by fromHNS is exact
// for every time scale up to 10 MHz.
func fromHNS(hns int64, timeScale int32) int64 {
	ts := int64(timeScale)
	whole := hns / hnsPerSecond
	rem := hns % hnsPerSecond
	frac := rem * ts
	if frac >= 0 {
		frac = (frac + hnsPerSecond/2) / hnsPerSecond
	} else {
		frac = (frac - hnsPerSecond/2) / hnsPerSecond
	}
	return whole*ts + frac
}

// frameRateRatio converts a frame rate to the numerator/denominator pair
// Media Foundation wants in MF_MT_FRAME_RATE. Integer rates become n/1,
// NTSC-style rates such as 29.97 become 30000/1001, and anything else is
// rounded to a thousandth. A zero or negative rate yields 30/1, a nominal
// value for encoders that require a frame rate.
func frameRateRatio(fps float64) (num, den uint32) {
	if fps <= 0 || math.IsNaN(fps) || math.IsInf(fps, 0) {
		return 30, 1
	}
	if r := math.Round(fps); math.Abs(fps-r) < 1e-6 {
		return uint32(r), 1
	}
	if x := fps * 1001 / 1000; math.Abs(x-math.Round(x)) < 1e-3 {
		return uint32(math.Round(x)) * 1000, 1001
	}
	return uint32(math.Round(fps * 1000)), 1000
}

// frameDurationHNS returns the duration of one frame in 100 ns units.
func frameDurationHNS(num, den uint32) int64 {
	if num == 0 {
		return 0
	}
	return int64(math.Round(float64(hnsPerSecond) * float64(den) / float64(num)))
}
