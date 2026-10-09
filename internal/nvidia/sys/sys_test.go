//go:build linux || (windows && amd64)

package sys

import (
	"errors"
	"testing"
)

// TestLoadCUDA checks that the driver entry points bind when the CUDA
// driver library is installed. Without the NVIDIA driver the test is skipped.
func TestLoadCUDA(t *testing.T) {
	err := LoadCUDA()
	if errors.Is(err, ErrNotAvailable) {
		t.Skipf("CUDA driver library not installed: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if CuInit == nil || CuMemcpy2D == nil || CuDevicePrimaryCtxRetain == nil {
		t.Fatal("symbols not bound")
	}
	t.Logf("cuGetErrorString(100) = %q", CUDAErrorString(CUDAErrorNoDevice))
}

// TestLoadCuvid checks the NVDEC and parser bindings when the NVDEC
// library is installed.
func TestLoadCuvid(t *testing.T) {
	err := LoadCuvid()
	if errors.Is(err, ErrNotAvailable) {
		t.Skipf("NVDEC library not installed: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if CuvidCreateVideoParser == nil || CuvidMapVideoFrame == nil || CuvidGetDecoderCaps == nil {
		t.Fatal("symbols not bound")
	}
}

// TestLoadNVENC checks the NVENC function table when the NVENC library is
// installed and the driver supports API 11.1.
func TestLoadNVENC(t *testing.T) {
	api, err := LoadNVENC()
	if errors.Is(err, ErrNotAvailable) {
		t.Skipf("NVENC library not installed: %v", err)
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
