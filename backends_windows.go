//go:build windows && (amd64 || arm64)

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediafoundation"
)

func init() {
	codec.Register(mediafoundation.Backend{})
}
