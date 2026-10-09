//go:build linux

package vaapi

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// DeviceEnv names the environment variable that selects the DRM render node
// (for example /dev/dri/renderD129 on a machine with several GPUs). When it
// is unset the first render node that libva can initialise is used.
const DeviceEnv = "HWMEDIACODEC_VAAPI_DEVICE"

// errNoDevice means no usable VA-API device was found; it is reported as
// "unsupported" rather than as a failure.
var errNoDevice = errors.New("vaapi: no usable DRM render node")

// display is one initialised VADisplay on a DRM render node.
type display struct {
	path   string
	fd     int
	dpy    uintptr
	major  int32
	minor  int32
	vendor string
	// profiles with a VLD (decode) entrypoint, as reported by the driver.
	decodeProfiles map[int32]bool
}

func candidateDevices() []string {
	if p := os.Getenv(DeviceEnv); p != "" {
		return []string{p}
	}
	var out []string
	for i := 128; i < 128+16; i++ {
		out = append(out, fmt.Sprintf("/dev/dri/renderD%d", i))
	}
	return out
}

// openDisplay loads libva and initialises a display on the first usable
// render node. Errors other than errNoDevice indicate a broken installation.
func openDisplay() (*display, error) {
	if err := sys.Load(); err != nil {
		return nil, err
	}
	var lastErr error
	for _, path := range candidateDevices() {
		d, err := openDevice(path)
		if err == nil {
			return d, nil
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

func openDevice(path string) (*display, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	d := &display{path: path, fd: fd}
	d.dpy = sys.GetDisplayDRM(int32(fd))
	if d.dpy == 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("vaapi: vaGetDisplayDRM failed for %s", path)
	}
	sys.InstallMessageCallbacks(d.dpy)
	if st := sys.Initialize(d.dpy, &d.major, &d.minor); st != sys.StatusSuccess {
		syscall.Close(fd)
		return nil, fmt.Errorf("vaapi: vaInitialize on %s: %s", path, statusMessage(st))
	}
	d.vendor = sys.QueryVendorString(d.dpy)
	if err := d.queryProfiles(); err != nil {
		d.close()
		return nil, err
	}
	return d, nil
}

func (d *display) queryProfiles() error {
	n := sys.MaxNumProfiles(d.dpy)
	if n <= 0 {
		return fmt.Errorf("vaapi: vaMaxNumProfiles returned %d", n)
	}
	profiles := make([]int32, n)
	var count int32
	if st := sys.QueryConfigProfiles(d.dpy, &profiles[0], &count); st != sys.StatusSuccess {
		return vaError("vaQueryConfigProfiles", st)
	}
	profiles = profiles[:count]
	maxEntry := sys.MaxNumEntrypoints(d.dpy)
	if maxEntry <= 0 {
		return fmt.Errorf("vaapi: vaMaxNumEntrypoints returned %d", maxEntry)
	}
	entrypoints := make([]int32, maxEntry)
	d.decodeProfiles = map[int32]bool{}
	for _, p := range profiles {
		var ne int32
		if st := sys.QueryConfigEntrypoints(d.dpy, p, &entrypoints[0], &ne); st != sys.StatusSuccess {
			continue
		}
		for _, e := range entrypoints[:ne] {
			if e == sys.EntrypointVLD {
				d.decodeProfiles[p] = true
			}
		}
	}
	return nil
}

// maxPictureSize reports the largest decode surface the driver accepts for
// the profile, or zeros when it does not say.
func (d *display) maxPictureSize(profile int32) (w, h int) {
	attr := sys.ConfigAttrib{Type: sys.ConfigAttribRTFormat, Value: sys.RTFormatYUV420}
	var config uint32
	if st := sys.CreateConfig(d.dpy, profile, sys.EntrypointVLD, &attr, 1, &config); st != sys.StatusSuccess {
		return 0, 0
	}
	defer sys.DestroyConfig(d.dpy, config)
	var n uint32
	if st := sys.QuerySurfaceAttributes(d.dpy, config, nil, &n); st != sys.StatusSuccess || n == 0 {
		return 0, 0
	}
	attrs := make([]sys.SurfaceAttrib, n)
	if st := sys.QuerySurfaceAttributes(d.dpy, config, &attrs[0], &n); st != sys.StatusSuccess {
		return 0, 0
	}
	for _, a := range attrs[:n] {
		switch a.Type {
		case sys.SurfaceAttribMaxWidth:
			w = int(a.Value.Int())
		case sys.SurfaceAttribMaxHeight:
			h = int(a.Value.Int())
		}
	}
	return w, h
}

func (d *display) close() {
	if d.dpy != 0 {
		sys.Terminate(d.dpy)
		d.dpy = 0
	}
	if d.fd >= 0 {
		syscall.Close(d.fd)
		d.fd = -1
	}
}

func statusMessage(st int32) string {
	msg := sys.StatusString(st)
	if m := sys.LastErrorMessage(); m != "" {
		msg += " (" + m + ")"
	}
	return msg
}

// vaError wraps a failed libva call.
func vaError(op string, st int32) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(st), Message: statusMessage(st)}
}
