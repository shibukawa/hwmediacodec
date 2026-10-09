//go:build !darwin && !linux

package hwmediacodec

// No backend is implemented for this platform yet. Probe returns an empty
// list and NewDecoder returns ErrUnsupported.
