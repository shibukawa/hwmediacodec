//go:build linux

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/nvidia"
	"github.com/shibukawa/hwmediacodec/internal/vaapi"
)

func init() {
	// NVIDIA first: on a machine with a discrete NVIDIA GPU next to an
	// integrated one, NVDEC/NVENC is the engine the user installed the
	// driver for, and it covers HEVC where the VA-API backend does not yet.
	codec.Register(nvidia.Backend{})
	codec.Register(vaapi.Backend{})
}
