package audioutil

import (
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// MaxPacketBytes bounds the audio packets the decoders take. No AAC access
// unit (6144 bits a channel) or Opus packet (at most 120 ms of 1275-byte
// frames) comes near it, and it keeps sizes within the 32-bit fields of
// the codec libraries.
const MaxPacketBytes = 1 << 20

// CheckPacket refuses an empty packet and one larger than MaxPacketBytes.
func CheckPacket(data []byte) error {
	switch {
	case len(data) == 0:
		return fmt.Errorf("hwmediacodec: empty audio packet: %w", codec.ErrInvalidData)
	case len(data) > MaxPacketBytes:
		return fmt.Errorf("hwmediacodec: an audio packet of %d bytes is larger than %d: %w", len(data), MaxPacketBytes, codec.ErrInvalidData)
	}
	return nil
}
