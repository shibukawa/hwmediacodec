package av1_test

import (
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/av1"
)

func FuzzParseSequenceAndFrame(f *testing.F) {
	f.Add(svtSeqHeader8, []byte{0, 0, 0, 0})
	f.Add(svtSeqHeader10, []byte{0x80})
	f.Fuzz(func(t *testing.T, seqPayload, framePayload []byte) {
		seq, err := av1.ParseSequenceHeader(seqPayload)
		if err != nil {
			return
		}
		state := &av1.State{Seq: seq}
		header, err := state.ParseHeader(framePayload, 0, 0)
		if err != nil {
			return
		}
		start := (header.HeaderBits + 7) / 8
		if start > len(framePayload) {
			t.Fatalf("header consumed %d bytes of %d", start, len(framePayload))
		}
		if !header.ShowExistingFrame {
			_, _ = header.ParseTileGroup(framePayload[start:])
		}
		state.Update(header)
	})
}
