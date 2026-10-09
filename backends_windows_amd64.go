package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/nvidia"
)

func registerNVIDIA() { codec.Register(nvidia.Backend{}) }
