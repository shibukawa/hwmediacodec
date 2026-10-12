//go:build android

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec"
)

func init() {
	// MediaCodec is the one codec API an Android application has; the
	// vendor's hardware codecs sit behind it.
	codec.Register(mediacodec.Backend{})
}
