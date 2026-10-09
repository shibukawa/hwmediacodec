//go:build linux

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/nvidia"
	"github.com/shibukawa/hwmediacodec/internal/vaapi"
	"github.com/shibukawa/hwmediacodec/internal/vpl"
)

func init() {
	// NVIDIA first: on a machine with a discrete NVIDIA GPU next to an
	// integrated one, NVDEC/NVENC is the engine the user installed the
	// driver for, and it covers HEVC where the VA-API backend does not yet.
	codec.Register(nvidia.Backend{})
	// Intel VPL before VA-API: both drive the same Intel GPU, but VPL is a
	// full codec API (HEVC, B-frames, the runtime's own rate control).
	// VA-API remains the path for AMD and for Intel GPUs without a VPL
	// runtime.
	codec.Register(vpl.Backend{})
	codec.Register(vaapi.Backend{})
}
