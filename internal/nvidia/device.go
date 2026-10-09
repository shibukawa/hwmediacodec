//go:build linux

package nvidia

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/nvidia/sys"
)

// DeviceEnv names the environment variable that selects the CUDA device by
// ordinal (the index nvidia-smi prints) on machines with several GPUs. The
// default is device 0.
const DeviceEnv = "HWMEDIACODEC_NVIDIA_DEVICE"

// errNoDevice means the driver library exists but no usable GPU does; it is
// reported as "unsupported" rather than as a failure.
var errNoDevice = errors.New("nvidia: no usable CUDA device")

var (
	initOnce sync.Once
	initErr  error
)

// initCUDA loads libcuda and initialises the driver once per process. Any
// cuInit failure (no GPU, kernel module missing or mismatched) counts as
// "no device": the GPU cannot be used either way.
func initCUDA() error {
	initOnce.Do(func() {
		if err := sys.LoadCUDA(); err != nil {
			initErr = err
			return
		}
		if st := sys.CuInit(0); st != sys.CUDASuccess {
			initErr = fmt.Errorf("%w: cuInit: %s", errNoDevice, sys.CUDAErrorString(st))
		}
	})
	return initErr
}

// device is one CUDA device with its primary context retained. The primary
// context is shared with every other CUDA user in the process, so opening
// several decoders and encoders costs one context, not one each.
type device struct {
	ordinal int32
	dev     int32
	ctx     uintptr
	name    string
}

func openDevice() (*device, error) {
	if err := initCUDA(); err != nil {
		return nil, err
	}
	ordinal := int32(0)
	if s := os.Getenv(DeviceEnv); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("nvidia: %s=%q is not a device ordinal", DeviceEnv, s)
		}
		ordinal = int32(n)
	}
	var count int32
	if st := sys.CuDeviceGetCount(&count); st != sys.CUDASuccess {
		return nil, fmt.Errorf("%w: cuDeviceGetCount: %s", errNoDevice, sys.CUDAErrorString(st))
	}
	if count == 0 {
		return nil, errNoDevice
	}
	if ordinal >= count {
		return nil, fmt.Errorf("%w: %s=%d but only %d device(s) exist", errNoDevice, DeviceEnv, ordinal, count)
	}
	d := &device{ordinal: ordinal}
	if st := sys.CuDeviceGet(&d.dev, ordinal); st != sys.CUDASuccess {
		return nil, cuError("cuDeviceGet", st)
	}
	var name [256]byte
	if st := sys.CuDeviceGetName(&name[0], int32(len(name)), d.dev); st == sys.CUDASuccess {
		d.name = cString(name[:])
	}
	if st := sys.CuDevicePrimaryCtxRetain(&d.ctx, d.dev); st != sys.CUDASuccess {
		return nil, cuError("cuDevicePrimaryCtxRetain", st)
	}
	return d, nil
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func (d *device) close() {
	if d.ctx != 0 {
		sys.CuDevicePrimaryCtxRelease(d.dev)
		d.ctx = 0
	}
}

// run makes the device's context current on the calling thread for the
// duration of fn. CUDA contexts are bound per thread and goroutines migrate
// between threads, so every sequence of driver, NVDEC or NVENC calls goes
// through here.
func (d *device) run(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if st := sys.CuCtxPushCurrent(d.ctx); st != sys.CUDASuccess {
		return cuError("cuCtxPushCurrent", st)
	}
	err := fn()
	var popped uintptr
	if st := sys.CuCtxPopCurrent(&popped); st != sys.CUDASuccess && err == nil {
		err = cuError("cuCtxPopCurrent", st)
	}
	return err
}

// cuError wraps a failed CUDA driver or NVDEC call.
func cuError(op string, st int32) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(st), Message: sys.CUDAErrorString(st)}
}

// copy2D copies a rectangle from device memory into host memory with
// cuMemcpy2D.
func copy2D(src uint64, srcPitch, srcX, srcY int, dst []byte, dstPitch, widthBytes, rows int) error {
	if rows == 0 || widthBytes == 0 {
		return nil
	}
	m := sys.Memcpy2D{
		SrcMemoryType: sys.MemoryTypeDevice,
		SrcDevice:     src,
		SrcPitch:      uintptr(srcPitch),
		SrcXInBytes:   uintptr(srcX),
		SrcY:          uintptr(srcY),
		DstMemoryType: sys.MemoryTypeHost,
		DstHost:       unsafe.Pointer(&dst[0]),
		DstPitch:      uintptr(dstPitch),
		WidthInBytes:  uintptr(widthBytes),
		Height:        uintptr(rows),
	}
	if st := sys.CuMemcpy2D(&m); st != sys.CUDASuccess {
		return cuError("cuMemcpy2D", st)
	}
	return nil
}
