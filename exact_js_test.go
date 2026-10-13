//go:build js && wasm

package hwmediacodec_test

import "github.com/shibukawa/hwmediacodec/internal/webcodecs"

// exactNV12 reports whether decoded NV12 frames are the decoder's own
// samples. A browser that hands over frames in another form (WebKit:
// full range, chroma shifted) has them restored instead.
func exactNV12() bool { return !webcodecs.WidenedFrames() }
