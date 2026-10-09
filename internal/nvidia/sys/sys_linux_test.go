//go:build linux

package sys

import (
	"errors"
	"testing"
)

// TestLoadCUDA checks that the driver entry points bind when libcuda is
// installed. Without the NVIDIA driver the test is skipped.
func TestLoadCUDA(t *testing.T) {
	err := LoadCUDA()
	if errors.Is(err, ErrNotAvailable) {
		t.Skipf("libcuda not installed: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if CuInit == nil || CuMemcpy2D == nil || CuDevicePrimaryCtxRetain == nil {
		t.Fatal("symbols not bound")
	}
	t.Logf("cuGetErrorString(100) = %q", CUDAErrorString(CUDAErrorNoDevice))
}

// TestLoadCuvid checks the NVDEC and parser bindings when libnvcuvid is
// installed.
func TestLoadCuvid(t *testing.T) {
	err := LoadCuvid()
	if errors.Is(err, ErrNotAvailable) {
		t.Skipf("libnvcuvid not installed: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if CuvidCreateVideoParser == nil || CuvidMapVideoFrame == nil || CuvidGetDecoderCaps == nil {
		t.Fatal("symbols not bound")
	}
}

// TestLoadNVENC checks the NVENC function table when libnvidia-encode is
// installed and the driver supports API 11.1.
func TestLoadNVENC(t *testing.T) {
	api, err := LoadNVENC()
	if errors.Is(err, ErrNotAvailable) {
		t.Skipf("libnvidia-encode not installed: %v", err)
	}
	if errors.Is(err, ErrNVENCVersion) {
		t.Skipf("driver too old: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if api.OpenEncodeSessionEx == nil || api.EncodePicture == nil || api.LockBitstream == nil {
		t.Fatal("function table not bound")
	}
	t.Logf("NVENC API max version %d.%d", NVENCMaxVersion>>4, NVENCMaxVersion&0xf)
}
