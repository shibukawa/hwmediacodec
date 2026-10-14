//go:build android || mediacodec_typecheck

package mediacodec

import (
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

func TestEncoderDiscardsCompletedTimestampHistory(t *testing.T) {
	get, release := sys.AMediaCodec_getOutputBuffer, sys.AMediaCodec_releaseOutputBuffer
	t.Cleanup(func() { sys.AMediaCodec_getOutputBuffer, sys.AMediaCodec_releaseOutputBuffer = get, release })
	data := []byte{0, 0, 0, 1, 0x65, 0x80}
	sys.AMediaCodec_getOutputBuffer = func(_, _ uintptr, size *uintptr) *byte {
		*size = uintptr(len(data))
		return &data[0]
	}
	released := 0
	sys.AMediaCodec_releaseOutputBuffer = func(_, _ uintptr, _ bool) int32 { released++; return sys.OK }
	e := &encoder{
		cfg: codec.EncoderConfig{Codec: codec.H264, BFrames: true}, ps: newParamSets(codec.H264),
		inflight: map[int64]int64{1: 200, 2: 100}, sent: []int64{100, 200}, frameDur: 100,
	}
	for i, wantDTS := range []int64{0, 100} {
		info := sys.BufferInfo{Size: int32(len(data)), PresentationTimeUs: int64(i + 1)}
		if err := e.takeOutput(0, &info); err != nil {
			t.Fatal(err)
		}
		if len(e.sent) != 1-i || e.pending[i].DTS != wantDTS {
			t.Fatalf("output %d: retained %d timestamps, DTS %d, want %d", i, len(e.sent), e.pending[i].DTS, wantDTS)
		}
	}
	if err := e.takeOutput(0, &sys.BufferInfo{Offset: -1, Size: 1}); err == nil {
		t.Fatal("negative output offset accepted")
	}
	if released != 3 {
		t.Fatalf("released %d native output buffers, want 3", released)
	}
}
