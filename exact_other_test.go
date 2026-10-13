//go:build !(js && wasm)

package hwmediacodec_test

// exactNV12 reports whether decoded NV12 frames are the decoder's own
// samples, which they are on every native backend.
func exactNV12() bool { return true }
