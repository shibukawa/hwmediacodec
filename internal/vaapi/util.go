package vaapi

// Name is the backend identifier reported in Capability.Backend.
const Name = "vaapi"

func b2u(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}
