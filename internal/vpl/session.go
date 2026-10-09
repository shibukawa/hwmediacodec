//go:build linux

package vpl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	vasys "github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
	"github.com/shibukawa/hwmediacodec/internal/vpl/sys"
)

// DeviceEnv names the environment variable that selects the DRM render node
// of the Intel GPU (for example /dev/dri/renderD129). When it is unset the
// first render node of an Intel device that libva can initialise is used.
const DeviceEnv = "HWMEDIACODEC_VPL_DEVICE"

const (
	// syncTimeoutMS bounds MFXVideoCORE_SyncOperation, the only call that
	// waits for the GPU.
	syncTimeoutMS = 60_000
	// busyRetries bounds the retries after MFX_WRN_DEVICE_BUSY, one
	// millisecond apart.
	busyRetries = 5000
	// maxSurfaces bounds the system-memory surfaces created on demand.
	maxSurfaces = 64
)

// errNoDevice means the libraries exist but no usable Intel GPU or GPU
// runtime does; it is reported as "unsupported" rather than as a failure.
var errNoDevice = errors.New("vpl: no usable Intel GPU")

// session is one VPL session on an Intel GPU. The GPU runtime works on top
// of VA-API and is handed the VADisplay of the render node, so the session
// owns the DRM file descriptor and the display as well as the dispatcher
// loader.
type session struct {
	path    string
	fd      int
	dpy     uintptr
	loader  uintptr
	ses     uintptr
	version sys.Version
	vendor  string
}

// notAvailable reports whether err means "no Intel VPL here", which Probe
// turns into an empty list and the constructors into ErrUnsupported.
func notAvailable(err error) bool {
	return errors.Is(err, sys.ErrNotAvailable) || errors.Is(err, vasys.ErrNotAvailable) || errors.Is(err, errNoDevice)
}

func candidateDevices() (paths []string, explicit bool) {
	if p := os.Getenv(DeviceEnv); p != "" {
		return []string{p}, true
	}
	for i := 128; i < 128+16; i++ {
		paths = append(paths, fmt.Sprintf("/dev/dri/renderD%d", i))
	}
	return paths, false
}

// intelVendorID is the PCI vendor id sysfs reports for Intel devices.
const intelVendorID = "0x8086"

// pciVendor returns the PCI vendor id of a render node as sysfs prints it
// ("0x8086"), or "" when it cannot be read.
func pciVendor(path string) string {
	b, err := os.ReadFile(filepath.Join("/sys/class/drm", filepath.Base(path), "device/vendor"))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}

// openSession loads the libraries, initialises VA-API on the Intel render
// node and creates a hardware session bound to it.
func openSession() (*session, error) {
	if err := sys.Load(); err != nil {
		return nil, err
	}
	if err := vasys.Load(); err != nil {
		return nil, err
	}
	paths, explicit := candidateDevices()
	var lastErr error
	for _, path := range paths {
		if !explicit {
			// Skip other vendors' GPUs without initialising their VA
			// driver. An unreadable id (no sysfs) is settled by the
			// driver's vendor string instead.
			if v := pciVendor(path); v != "" && v != intelVendorID {
				continue
			}
		}
		s, err := openDevice(path, explicit)
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", errNoDevice, lastErr)
	}
	return nil, errNoDevice
}

func openDevice(path string, explicit bool) (*session, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	s := &session{path: path, fd: fd}
	s.dpy = vasys.GetDisplayDRM(int32(fd))
	if s.dpy == 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("vpl: vaGetDisplayDRM failed for %s", path)
	}
	vasys.InstallMessageCallbacks(s.dpy)
	var major, minor int32
	if st := vasys.Initialize(s.dpy, &major, &minor); st != vasys.StatusSuccess {
		syscall.Close(fd)
		return nil, fmt.Errorf("vpl: vaInitialize on %s: %s", path, vasys.StatusString(st))
	}
	s.vendor = vasys.QueryVendorString(s.dpy)
	if !explicit && pciVendor(path) == "" && !strings.Contains(s.vendor, "Intel") {
		s.close()
		return nil, fmt.Errorf("vpl: %s is not an Intel GPU (%s)", path, s.vendor)
	}
	if err := s.createSession(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// createSession asks the dispatcher for a hardware implementation and binds
// it to the VA display.
func (s *session) createSession() error {
	s.loader = sys.MFXLoad()
	if s.loader == 0 {
		return errors.New("vpl: MFXLoad failed")
	}
	cfg := sys.MFXCreateConfig(s.loader)
	if cfg == 0 {
		return errors.New("vpl: MFXCreateConfig failed")
	}
	name := []byte("mfxImplDescription.Impl\x00")
	lo, hi := sys.VariantU32(sys.ImplTypeHardware)
	if st := sys.MFXSetConfigFilterProperty(cfg, &name[0], lo, hi); st != sys.ErrNone {
		return mfxError("MFXSetConfigFilterProperty", st)
	}
	if st := sys.MFXCreateSession(s.loader, 0, &s.ses); st != sys.ErrNone {
		s.ses = 0
		// No runtime for this GPU generation, or no runtime installed.
		return fmt.Errorf("%w: no hardware implementation for %s (%s): %s", errNoDevice, s.path, s.vendor, sys.StatusString(st))
	}
	// The runtime renders through the application's VADisplay. A runtime
	// that already opened its own display reports undefined behaviour,
	// which is harmless.
	if st := sys.CoreSetHandle(s.ses, sys.HandleVADisplay, s.dpy); st < 0 && st != sys.ErrUndefinedBehavior {
		return mfxError("MFXVideoCORE_SetHandle", st)
	}
	sys.MFXQueryVersion(s.ses, &s.version)
	return nil
}

func (s *session) close() {
	if s.ses != 0 {
		sys.MFXClose(s.ses)
		s.ses = 0
	}
	if s.loader != 0 {
		sys.MFXUnload(s.loader)
		s.loader = 0
	}
	if s.dpy != 0 {
		vasys.Terminate(s.dpy)
		s.dpy = 0
	}
	if s.fd >= 0 {
		syscall.Close(s.fd)
		s.fd = -1
	}
}

// describe names the runtime for error messages.
func (s *session) describe() string {
	return fmt.Sprintf("%s, VPL API %d.%d", s.vendor, s.version.Major, s.version.Minor)
}

// sync waits for an asynchronous operation to finish.
func (s *session) sync(op string, syncPoint uintptr) error {
	st := sys.CoreSyncOperation(s.ses, syncPoint, syncTimeoutMS)
	switch {
	case st == sys.WrnInExecution:
		return &codec.BackendError{Backend: Name, Op: op, Status: int64(st), Message: fmt.Sprintf("the GPU did not finish within %d s", syncTimeoutMS/1000)}
	case st < 0:
		return mfxError(op, st)
	}
	return nil
}

// busyWait sleeps after MFX_WRN_DEVICE_BUSY; it reports false once the
// retry budget is spent.
func busyWait(n *int) bool {
	if *n >= busyRetries {
		return false
	}
	*n++
	time.Sleep(time.Millisecond)
	return true
}

// mfxError wraps a failed VPL call.
func mfxError(op string, st int32) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(st), Message: sys.StatusString(st)}
}
