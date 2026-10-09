//go:build linux

package sys

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
)

// Bound dispatcher entry points (libvpl.so.2). They are valid after Load
// returns nil. Sessions, loaders, configs and sync points are opaque
// pointers and travel as uintptr.
var (
	MFXLoad         func() uintptr
	MFXUnload       func(loader uintptr)
	MFXCreateConfig func(loader uintptr) uintptr
	// MFXSetConfigFilterProperty takes an mfxVariant by value; see
	// VariantU32 for the two words.
	MFXSetConfigFilterProperty func(config uintptr, name *byte, variantLo, variantHi uint64) int32
	MFXCreateSession           func(loader uintptr, index uint32, session *uintptr) int32
	MFXClose                   func(session uintptr) int32
	MFXQueryIMPL               func(session uintptr, impl *int32) int32
	MFXQueryVersion            func(session uintptr, version *Version) int32

	CoreSetHandle     func(session uintptr, typ int32, handle uintptr) int32
	CoreSyncOperation func(session uintptr, syncPoint uintptr, waitMS uint32) int32

	DecodeQuery         func(session uintptr, in, out *VideoParam) int32
	DecodeHeader        func(session uintptr, bs *Bitstream, par *VideoParam) int32
	DecodeQueryIOSurf   func(session uintptr, par *VideoParam, request *FrameAllocRequest) int32
	DecodeInit          func(session uintptr, par *VideoParam) int32
	DecodeReset         func(session uintptr, par *VideoParam) int32
	DecodeClose         func(session uintptr) int32
	DecodeGetVideoParam func(session uintptr, par *VideoParam) int32
	// DecodeFrameAsync: bs is nil to drain; surfaceOut receives the address
	// of one of the caller's work surfaces.
	DecodeFrameAsync func(session uintptr, bs *Bitstream, surfaceWork *FrameSurface, surfaceOut *uintptr, syncPoint *uintptr) int32

	EncodeQuery         func(session uintptr, in, out *VideoParam) int32
	EncodeQueryIOSurf   func(session uintptr, par *VideoParam, request *FrameAllocRequest) int32
	EncodeInit          func(session uintptr, par *VideoParam) int32
	EncodeClose         func(session uintptr) int32
	EncodeGetVideoParam func(session uintptr, par *VideoParam) int32
	// EncodeFrameAsync: surface is nil to drain.
	EncodeFrameAsync func(session uintptr, ctrl *EncodeCtrl, surface *FrameSurface, bs *Bitstream, syncPoint *uintptr) int32
)

// ErrNotAvailable is wrapped by Load when the dispatcher is not installed,
// which on a machine without an Intel GPU is the normal case.
var ErrNotAvailable = errors.New("vpl: libvpl is not available")

var (
	loadOnce sync.Once
	loadErr  error
)

// Load opens libvpl and binds every symbol. It is safe to call repeatedly;
// the result is cached.
func Load() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

func load() error {
	var handle uintptr
	var lib string
	var errs []string
	for _, n := range []string{"libvpl.so.2", "libvpl.so"} {
		h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			handle, lib = h, n
			break
		}
		errs = append(errs, err.Error())
	}
	if handle == 0 {
		return fmt.Errorf("%w: %s", ErrNotAvailable, strings.Join(errs, "; "))
	}
	var missing []string
	fn := func(fptr any, name string) {
		addr, err := purego.Dlsym(handle, name)
		if err != nil {
			missing = append(missing, name)
			return
		}
		purego.RegisterFunc(fptr, addr)
	}
	fn(&MFXLoad, "MFXLoad")
	fn(&MFXUnload, "MFXUnload")
	fn(&MFXCreateConfig, "MFXCreateConfig")
	fn(&MFXSetConfigFilterProperty, "MFXSetConfigFilterProperty")
	fn(&MFXCreateSession, "MFXCreateSession")
	fn(&MFXClose, "MFXClose")
	fn(&MFXQueryIMPL, "MFXQueryIMPL")
	fn(&MFXQueryVersion, "MFXQueryVersion")
	fn(&CoreSetHandle, "MFXVideoCORE_SetHandle")
	fn(&CoreSyncOperation, "MFXVideoCORE_SyncOperation")
	fn(&DecodeQuery, "MFXVideoDECODE_Query")
	fn(&DecodeHeader, "MFXVideoDECODE_DecodeHeader")
	fn(&DecodeQueryIOSurf, "MFXVideoDECODE_QueryIOSurf")
	fn(&DecodeInit, "MFXVideoDECODE_Init")
	fn(&DecodeReset, "MFXVideoDECODE_Reset")
	fn(&DecodeClose, "MFXVideoDECODE_Close")
	fn(&DecodeGetVideoParam, "MFXVideoDECODE_GetVideoParam")
	fn(&DecodeFrameAsync, "MFXVideoDECODE_DecodeFrameAsync")
	fn(&EncodeQuery, "MFXVideoENCODE_Query")
	fn(&EncodeQueryIOSurf, "MFXVideoENCODE_QueryIOSurf")
	fn(&EncodeInit, "MFXVideoENCODE_Init")
	fn(&EncodeClose, "MFXVideoENCODE_Close")
	fn(&EncodeGetVideoParam, "MFXVideoENCODE_GetVideoParam")
	fn(&EncodeFrameAsync, "MFXVideoENCODE_EncodeFrameAsync")
	if len(missing) > 0 {
		return fmt.Errorf("vpl: %s lacks symbols: %s", lib, strings.Join(missing, ", "))
	}
	return nil
}
