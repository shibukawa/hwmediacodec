//go:build linux

package sys

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Bound libva entry points. They are valid after Load returns nil.
var (
	GetDisplayDRM func(fd int32) uintptr

	Initialize        func(dpy uintptr, major, minor *int32) int32
	Terminate         func(dpy uintptr) int32
	ErrorStr          func(status int32) string
	QueryVendorString func(dpy uintptr) string
	// SetErrorCallback and SetInfoCallback are nil on libva builds that
	// do not export them (before 2.0).
	SetErrorCallback func(dpy uintptr, callback uintptr, userContext uintptr) uintptr
	SetInfoCallback  func(dpy uintptr, callback uintptr, userContext uintptr) uintptr

	MaxNumProfiles         func(dpy uintptr) int32
	MaxNumEntrypoints      func(dpy uintptr) int32
	QueryConfigProfiles    func(dpy uintptr, profiles *int32, num *int32) int32
	QueryConfigEntrypoints func(dpy uintptr, profile int32, entrypoints *int32, num *int32) int32
	GetConfigAttributes    func(dpy uintptr, profile, entrypoint int32, attribs *ConfigAttrib, num int32) int32
	CreateConfig           func(dpy uintptr, profile, entrypoint int32, attribs *ConfigAttrib, num int32, config *uint32) int32
	DestroyConfig          func(dpy uintptr, config uint32) int32
	QuerySurfaceAttributes func(dpy uintptr, config uint32, attribs *SurfaceAttrib, num *uint32) int32

	CreateSurfaces  func(dpy uintptr, format uint32, width, height uint32, surfaces *uint32, num uint32, attribs *SurfaceAttrib, numAttribs uint32) int32
	DestroySurfaces func(dpy uintptr, surfaces *uint32, num int32) int32
	CreateContext   func(dpy uintptr, config uint32, width, height int32, flag int32, renderTargets *uint32, num int32, context *uint32) int32
	DestroyContext  func(dpy uintptr, context uint32) int32

	CreateBuffer  func(dpy uintptr, context uint32, typ int32, size uint32, numElements uint32, data unsafe.Pointer, buf *uint32) int32
	DestroyBuffer func(dpy uintptr, buf uint32) int32
	MapBuffer     func(dpy uintptr, buf uint32, pbuf **byte) int32
	UnmapBuffer   func(dpy uintptr, buf uint32) int32

	BeginPicture  func(dpy uintptr, context uint32, surface uint32) int32
	RenderPicture func(dpy uintptr, context uint32, buffers *uint32, num int32) int32
	EndPicture    func(dpy uintptr, context uint32) int32
	SyncSurface   func(dpy uintptr, surface uint32) int32

	DeriveImage  func(dpy uintptr, surface uint32, image *Image) int32
	CreateImage  func(dpy uintptr, format *ImageFormat, width, height int32, image *Image) int32
	GetImage     func(dpy uintptr, surface uint32, x, y int32, width, height uint32, image uint32) int32
	DestroyImage func(dpy uintptr, image uint32) int32
)

var (
	loadOnce sync.Once
	loadErr  error
)

// Load opens libva and libva-drm and binds every symbol. It is safe to call
// repeatedly; the result is cached. A missing library is reported as an
// error wrapping ErrNotAvailable.
func Load() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

// ErrNotAvailable is wrapped by Load when libva is not installed.
var ErrNotAvailable = errors.New("vaapi: libva is not available")

type binder struct {
	handle uintptr
	lib    string
	errs   []string
}

func (b *binder) fn(fptr any, name string) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		b.errs = append(b.errs, fmt.Sprintf("%s: %s", b.lib, name))
		return
	}
	purego.RegisterFunc(fptr, addr)
}

func (b *binder) optional(fptr any, name string) {
	if addr, err := purego.Dlsym(b.handle, name); err == nil {
		purego.RegisterFunc(fptr, addr)
	}
}

func open(names ...string) (*binder, error) {
	var errs []string
	for _, n := range names {
		h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			return &binder{handle: h, lib: n}, nil
		}
		errs = append(errs, err.Error())
	}
	return nil, fmt.Errorf("%w: %s", ErrNotAvailable, strings.Join(errs, "; "))
}

func load() error {
	va, err := open("libva.so.2", "libva.so")
	if err != nil {
		return err
	}
	drm, err := open("libva-drm.so.2", "libva-drm.so")
	if err != nil {
		return err
	}

	drm.fn(&GetDisplayDRM, "vaGetDisplayDRM")

	va.fn(&Initialize, "vaInitialize")
	va.fn(&Terminate, "vaTerminate")
	va.fn(&ErrorStr, "vaErrorStr")
	va.fn(&QueryVendorString, "vaQueryVendorString")
	va.optional(&SetErrorCallback, "vaSetErrorCallback")
	va.optional(&SetInfoCallback, "vaSetInfoCallback")
	va.fn(&MaxNumProfiles, "vaMaxNumProfiles")
	va.fn(&MaxNumEntrypoints, "vaMaxNumEntrypoints")
	va.fn(&QueryConfigProfiles, "vaQueryConfigProfiles")
	va.fn(&QueryConfigEntrypoints, "vaQueryConfigEntrypoints")
	va.fn(&GetConfigAttributes, "vaGetConfigAttributes")
	va.fn(&CreateConfig, "vaCreateConfig")
	va.fn(&DestroyConfig, "vaDestroyConfig")
	va.fn(&QuerySurfaceAttributes, "vaQuerySurfaceAttributes")
	va.fn(&CreateSurfaces, "vaCreateSurfaces")
	va.fn(&DestroySurfaces, "vaDestroySurfaces")
	va.fn(&CreateContext, "vaCreateContext")
	va.fn(&DestroyContext, "vaDestroyContext")
	va.fn(&CreateBuffer, "vaCreateBuffer")
	va.fn(&DestroyBuffer, "vaDestroyBuffer")
	va.fn(&MapBuffer, "vaMapBuffer")
	va.fn(&UnmapBuffer, "vaUnmapBuffer")
	va.fn(&BeginPicture, "vaBeginPicture")
	va.fn(&RenderPicture, "vaRenderPicture")
	va.fn(&EndPicture, "vaEndPicture")
	va.fn(&SyncSurface, "vaSyncSurface")
	va.fn(&DeriveImage, "vaDeriveImage")
	va.fn(&CreateImage, "vaCreateImage")
	va.fn(&GetImage, "vaGetImage")
	va.fn(&DestroyImage, "vaDestroyImage")

	var missing []string
	for _, b := range []*binder{va, drm} {
		missing = append(missing, b.errs...)
	}
	if len(missing) > 0 {
		return errors.New("vaapi: missing symbols: " + strings.Join(missing, ", "))
	}
	return nil
}

// Message callbacks. libva prints informational messages to stderr unless
// callbacks are installed; a library must not do that, so the info channel
// is silenced and the last error message is kept for diagnostics.
var (
	callbackOnce sync.Once
	infoCallback uintptr
	errCallback  uintptr

	msgMu   sync.Mutex
	lastErr string
)

func onInfo(userContext uintptr, msg *byte) uintptr { return 0 }

func onError(userContext uintptr, msg *byte) uintptr {
	s := strings.TrimSpace(cString(msg))
	if s == "" {
		return 0
	}
	msgMu.Lock()
	lastErr = s
	msgMu.Unlock()
	return 0
}

func cString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
		if n > 4096 {
			break
		}
	}
	return string(unsafe.Slice(p, n))
}

// InstallMessageCallbacks silences libva's stderr output for dpy and
// records error messages for LastErrorMessage.
func InstallMessageCallbacks(dpy uintptr) {
	if SetErrorCallback == nil || SetInfoCallback == nil {
		return
	}
	callbackOnce.Do(func() {
		infoCallback = purego.NewCallback(onInfo)
		errCallback = purego.NewCallback(onError)
	})
	SetErrorCallback(dpy, errCallback, 0)
	SetInfoCallback(dpy, infoCallback, 0)
}

// LastErrorMessage returns the most recent error message libva reported
// through the callback, and clears it.
func LastErrorMessage() string {
	msgMu.Lock()
	defer msgMu.Unlock()
	s := lastErr
	lastErr = ""
	return s
}

// StatusString describes a VAStatus.
func StatusString(status int32) string {
	if ErrorStr != nil {
		if s := ErrorStr(status); s != "" {
			return s
		}
	}
	return fmt.Sprintf("VAStatus %#x", uint32(status))
}
