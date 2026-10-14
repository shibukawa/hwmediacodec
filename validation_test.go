package hwmediacodec_test

import (
	"context"
	"errors"
	"math"
	"testing"

	hw "github.com/shibukawa/hwmediacodec"
)

func TestEncoderRejectsUnrepresentableOptions(t *testing.T) {
	opts := []hw.EncoderOption{
		hw.WithQuality(math.NaN()), hw.WithQuality(math.Inf(1)),
		hw.WithFrameRate(math.NaN()), hw.WithFrameRate(math.Inf(1)), hw.WithFrameRate(math.SmallestNonzeroFloat64),
	}
	if math.MaxInt > math.MaxInt32 {
		opts = append(opts, hw.WithBitrate(math.MaxInt), hw.WithKeyframeInterval(math.MaxInt))
	}
	for _, opt := range opts {
		enc, err := hw.NewEncoder(context.Background(), hw.H264, 320, 240, opt)
		if enc != nil {
			enc.Close()
		}
		if err == nil || errors.Is(err, hw.ErrUnsupported) {
			t.Fatalf("invalid encoder option reached backend: %v", err)
		}
	}
	if math.MaxInt > math.MaxInt32 {
		if enc, err := hw.NewEncoder(context.Background(), hw.H264, math.MaxInt, 4); err == nil || errors.Is(err, hw.ErrUnsupported) {
			if enc != nil {
				enc.Close()
			}
			t.Fatalf("unrepresentable picture reached backend: %v", err)
		}
	}
}
