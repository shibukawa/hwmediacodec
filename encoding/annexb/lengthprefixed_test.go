package annexb

import (
	"bytes"
	"testing"
)

func TestLengthPrefixedRoundTrip(t *testing.T) {
	nals := [][]byte{{0x67, 1, 2, 3}, {0x68, 4}, bytes.Repeat([]byte{0x65}, 300)}
	var sample, want []byte
	for _, n := range nals {
		sample = AppendLengthPrefixed(sample, n)
		want = AppendUnit(want, n)
	}
	if len(sample) != 4*3+4+2+300 || sample[3] != 4 || sample[4] != 0x67 {
		t.Fatalf("length-prefixed sample starts %x", sample[:8])
	}
	got, err := FromLengthPrefixed([]byte{0xAA}, sample, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 0xAA || !bytes.Equal(got[1:], want) {
		t.Error("FromLengthPrefixed does not append the Annex-B form")
	}
	if split := Split(got[1:]); len(split) != 3 || !bytes.Equal(split[2], nals[2]) {
		t.Errorf("Split sees %d units", len(split))
	}

	// Two-byte lengths, as an avcC record may ask for.
	short := []byte{0, 2, 0x67, 9, 0, 1, 0x68}
	got, err = FromLengthPrefixed(nil, short, 2)
	if err != nil || !bytes.Equal(got, []byte{0, 0, 0, 1, 0x67, 9, 0, 0, 0, 1, 0x68}) {
		t.Errorf("two-byte lengths: %x %v", got, err)
	}
}

func TestFromLengthPrefixedErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		sample []byte
		size   int
	}{
		"length beyond the sample": {[]byte{0, 0, 0, 9, 1, 2}, 4},
		"zero length":              {[]byte{0, 0, 0, 0, 1}, 4},
		"length size 0":            {[]byte{1, 2, 3}, 0},
		"length size 5":            {[]byte{1, 2, 3, 4, 5, 6}, 5},
	} {
		if _, err := FromLengthPrefixed(nil, tc.sample, tc.size); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Trailing bytes too short for a length are ignored.
	if got, err := FromLengthPrefixed(nil, []byte{0, 0, 0, 1, 0x65, 0, 0}, 4); err != nil || len(got) != 5 {
		t.Errorf("trailing bytes: %x %v", got, err)
	}
}
