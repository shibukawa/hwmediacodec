//go:build darwin

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox"
)

func init() {
	codec.Register(videotoolbox.Backend{})
}
