package aac

// ConfigFromESDescriptor returns the AudioSpecificConfig inside an MPEG-4
// ES_Descriptor (ISO/IEC 14496-1, section 7.2.6.5), or nil when there is
// none. The descriptor is the payload of an MP4 esds box, the magic cookie
// of an AudioToolbox AAC converter and what Safari hands out as the
// description of an AAC stream.
func ConfigFromESDescriptor(b []byte) []byte {
	for len(b) >= 2 {
		tag := b[0]
		b = b[1:]
		size := 0
		complete := false
		for i := 0; i < 4 && len(b) > 0; i++ {
			c := b[0]
			b = b[1:]
			size = size<<7 | int(c&0x7F)
			if c&0x80 == 0 {
				complete = true
				break
			}
		}
		if !complete || size > len(b) {
			return nil
		}
		body := b[:size]
		switch tag {
		case 0x03: // ES_Descriptor
			// Descend past ES_ID and the flags with their optional fields.
			if size < 3 {
				return nil
			}
			skip := 3
			flags := body[2]
			if flags&0x80 != 0 {
				skip += 2
			}
			if flags&0x40 != 0 {
				if skip >= len(body) {
					return nil
				}
				skip += 1 + int(body[skip])
			}
			if flags&0x20 != 0 {
				skip += 2
			}
			if skip > len(body) {
				return nil
			}
			b = body[skip:]
		case 0x04: // DecoderConfigDescriptor
			if size < 13 {
				return nil
			}
			b = body[13:]
		case 0x05: // DecoderSpecificInfo
			return append([]byte(nil), b[:size]...)
		default:
			b = b[size:]
		}
	}
	return nil
}
