//go:build linux

package vpl

import (
	"context"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// TestWithoutDevice checks the behaviour on a machine without an Intel GPU
// (or without the libraries): no capabilities, no error, and ErrUnsupported
// from the constructors. On a machine with VPL hardware it only logs the
// capabilities; the conformance tests of the root package cover the rest.
func TestWithoutDevice(t *testing.T) {
	ctx := context.Background()
	caps, err := Backend{}.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(caps) > 0 {
		for _, c := range caps {
			t.Logf("%s %s %s", c.Backend, c.Codec, c.Direction)
		}
		return
	}
	_, err = Backend{}.NewDecoder(ctx, codec.DecoderConfig{Codec: codec.H264, OutputFormat: codec.NV12, TimeScale: 90000})
	if !errors.Is(err, codec.ErrUnsupported) {
		t.Errorf("NewDecoder without a device: %v, want ErrUnsupported", err)
	}
	t.Log(err)
	_, err = Backend{}.NewEncoder(ctx, codec.EncoderConfig{Codec: codec.H264, Width: 640, Height: 480, InputFormat: codec.NV12, TimeScale: 90000})
	if !errors.Is(err, codec.ErrUnsupported) {
		t.Errorf("NewEncoder without a device: %v, want ErrUnsupported", err)
	}
	t.Log(err)
}
