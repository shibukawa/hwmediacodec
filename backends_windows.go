//go:build windows && (amd64 || arm64)

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediafoundation"
)

func init() {
	// NVIDIA first: where the NVIDIA driver is installed, NVDEC/NVENC is
	// driven directly instead of through the Microsoft decoder MFTs and
	// the vendor encoder MFT, which gives the same controls as on Linux.
	// Media Foundation serves every other GPU, and NVIDIA GPUs whose
	// driver the nvidia backend cannot use.
	registerNVIDIA()
	codec.Register(mediafoundation.Backend{})
}
