// Package vpl implements the Intel backend on Linux on top of the Intel
// Video Processing Library (VPL, the successor of the Media SDK and the API
// behind "Quick Sync Video"). The dispatcher (libvpl) is loaded at run time
// with purego; it finds the GPU runtime (libmfx-gen for Tiger Lake and
// newer, the legacy libmfxhw64 for older GPUs), which in turn renders
// through VA-API. A machine without the libraries or without an Intel GPU
// yields no capabilities and ErrUnsupported.
//
// VPL is a full codec API: the runtime parses the bitstream, manages
// reference pictures and returns frames in display order, and its encoders
// write complete access units. Unlike the VA-API backend no slice-level
// bookkeeping runs in Go, which is why this backend covers HEVC and
// B-frames where the VA-API one does not yet.
//
// The files without a build constraint hold the parts that do not call into
// the library (parameter mapping, pixel copies, the parameter-set cache), so
// their tests run on any development machine.
package vpl

import "github.com/shibukawa/hwmediacodec/internal/codec"

// Name is the backend identifier reported in Capability.Backend.
const Name = "vpl"

func unsupported(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Decode, Reason: reason}
}

func unsupportedEncode(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Encode, Reason: reason}
}
