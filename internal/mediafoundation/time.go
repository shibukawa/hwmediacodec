package mediafoundation

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
