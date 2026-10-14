package h264_test

import (
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/h264"
)

func TestParseSPSRejectsBitDepthOverflow(t *testing.T) {
	for _, depth := range []uint32{15, 0, 1, 6} {
		sps := encoderSPS(100)
		sps.BitDepthLuma = depth
		if _, err := h264.ParseSPS(h264.WriteSPS(sps)); !errors.Is(err, h264.ErrSyntax) {
			t.Fatalf("accepted bit depth %d: %v", depth, err)
		}
	}
}
