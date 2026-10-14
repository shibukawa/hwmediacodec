package playback

import (
	"bytes"
	"math"
	"testing"

	"github.com/shibukawa/hwmediacodec"
)

func TestNewStreamRejectsUnrepresentableFramePeriod(t *testing.T) {
	for _, fps := range []float64{0, -1, math.NaN(), math.Inf(1), math.SmallestNonzeroFloat64, math.MaxFloat64} {
		if _, err := NewStream(bytes.NewReader(nil), hwmediacodec.H264, fps); err == nil {
			t.Errorf("frame rate %g accepted", fps)
		}
	}
}
