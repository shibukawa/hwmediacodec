//go:build !darwin && !(windows && (amd64 || arm64))

package hwmediacodec

// No backend is implemented for this platform yet (this includes 32-bit
// Windows, which is out of scope). Probe returns an empty list and
// NewDecoder returns ErrUnsupported.
