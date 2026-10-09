package sys

// ULong is the C unsigned long (tcu_ulong in cuviddec.h), which the cuvid
// structures use for sizes and flags: 32 bits on Windows (LLP64).
type ULong uint32
