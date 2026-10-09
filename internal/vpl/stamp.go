package vpl

import "github.com/shibukawa/hwmediacodec/internal/vpl/sys"

// stampBias is added to a PTS before it travels through the runtime as an
// mfxU64 time stamp. The runtime reserves the all-ones value for "no time
// stamp" and its decoders carry time stamps as seconds in a double, so a
// negative PTS reinterpreted as a huge unsigned number would be replaced or
// lose its low bits. With the bias every PTS above -2^40 is a small positive
// number that survives exactly, and the order of the values is preserved.
const stampBias = 1 << 40

func toStamp(pts int64) uint64 { return uint64(pts) + stampBias }

// fromStamp is the inverse of toStamp. ok is false for a picture the
// runtime gave no time stamp.
func fromStamp(ts uint64) (pts int64, ok bool) {
	if ts == sys.TimestampUnknown {
		return 0, false
	}
	return int64(ts - stampBias), true
}
