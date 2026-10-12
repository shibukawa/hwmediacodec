//go:build !windows

package sys

// ULong is the C unsigned long (tcu_ulong in cuviddec.h), which the cuvid
// structures use for sizes and flags: 64 bits on LP64 systems.
type ULong uint64
