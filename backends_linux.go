//go:build linux

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vaapi"
)

func init() {
	codec.Register(vaapi.Backend{})
}
