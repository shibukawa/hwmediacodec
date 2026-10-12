//go:build !darwin && !(linux && !android) && !(windows && (amd64 || arm64))

package hwmediacodec

// No backend is implemented for this platform yet: Android (the NDK
// MediaCodec backend is planned) and 32-bit Windows (out of scope). Probe
// returns an empty list and NewDecoder returns ErrUnsupported.
