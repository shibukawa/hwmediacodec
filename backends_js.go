//go:build js && wasm

package hwmediacodec

import (
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/webcodecs"
)

func init() {
	// WebCodecs is the one codec API a page has; the browser puts the
	// platform's hardware codecs behind it. Outside a browser (Node.js)
	// and on a page that is not a secure context the backend reports no
	// capability.
	codec.Register(webcodecs.Backend{})
	// Audio goes the same way: AudioDecoder and AudioEncoder.
	codec.RegisterAudio(webcodecs.Backend{})
}
