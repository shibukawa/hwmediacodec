//go:build android || mediacodec_typecheck

package mediacodec

import (
	"math"
	"testing"
)

func TestOutputRange(t *testing.T) {
	for _, tc := range []struct {
		size           uintptr
		offset, length int32
		want           bool
	}{{16, 4, 12, true}, {16, -1, 1, false}, {16, 1, -1, false}, {16, 8, 9, false}, {16, 17, 0, false}, {16, 16, 0, true}, {16, math.MaxInt32, math.MaxInt32, false}} {
		start, end, ok := outputRange(tc.size, tc.offset, tc.length)
		if ok != tc.want || ok && (start != int(tc.offset) || end-start != int(tc.length)) {
			t.Errorf("outputRange(%d, %d, %d) = %d, %d, %v", tc.size, tc.offset, tc.length, start, end, ok)
		}
	}
}

func TestTimestampRescaling(t *testing.T) {
	if got := toMicros(-45000, 90000); got != -500000 {
		t.Fatalf("negative timestamp became %d", got)
	}
	if got := fromMicros(20_000_000_000_000, 90000); got != 1_800_000_000_000 {
		t.Fatalf("large timestamp became %d", got)
	}
}
