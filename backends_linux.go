//go:build linux && !android

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/fdkaac"
	"github.com/shibukawa/hwmediacodec/internal/libavcodec"
	"github.com/shibukawa/hwmediacodec/internal/libopus"
	"github.com/shibukawa/hwmediacodec/internal/nvidia"
	"github.com/shibukawa/hwmediacodec/internal/vaapi"
	"github.com/shibukawa/hwmediacodec/internal/vpl"
)

func init() {
	// NVIDIA first: on a machine with a discrete NVIDIA GPU next to an
	// integrated one, NVDEC/NVENC is the engine the user installed the
	// driver for.
	codec.Register(nvidia.Backend{})
	// Intel VPL before VA-API: both drive the same Intel GPU, but VPL is a
	// full codec API (B-frames, the runtime's own rate control).
	// VA-API remains the path for AMD and for Intel GPUs without a VPL
	// runtime.
	codec.Register(vpl.Backend{})
	codec.Register(vaapi.Backend{})

	// Linux has no audio codecs of its own; each of these stands for a
	// library of the distribution and is absent without it. AAC decodes
	// with libavcodec where both libraries are installed: libfdk-aac is
	// there for its encoder, and the builds of it that some distributions
	// ship decode AAC-LC only.
	codec.RegisterAudio(libopus.Backend{})
	codec.RegisterAudio(libavcodec.Backend{})
	codec.RegisterAudio(fdkaac.Backend{})
}
