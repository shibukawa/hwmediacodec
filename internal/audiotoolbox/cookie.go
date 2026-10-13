//go:build darwin

package audiotoolbox

// The magic cookie of an AAC converter is an MPEG-4 ES_Descriptor (ISO/IEC
// 14496-1, section 7.2.6.5) wrapped around the AudioSpecificConfig.

const (
	tagESDescriptor        = 0x03
	tagDecoderConfig       = 0x04
	tagDecoderSpecificInfo = 0x05
	tagSLConfig            = 0x06
)

// appendDescriptor appends a descriptor header with a four-byte length,
// the form AudioToolbox writes itself.
func appendDescriptor(dst []byte, tag byte, size int) []byte {
	return append(dst, tag, 0x80|byte(size>>21), 0x80|byte(size>>14), 0x80|byte(size>>7), byte(size&0x7F))
}

// aacCookie builds the magic cookie for the AudioSpecificConfig asc.
func aacCookie(asc []byte) []byte {
	const header = 5
	decoderConfig := 13 + header + len(asc)
	es := 3 + header + decoderConfig + header + 1
	b := appendDescriptor(nil, tagESDescriptor, es)
	b = append(b, 0, 0, 0) // ES_ID, flags
	b = appendDescriptor(b, tagDecoderConfig, decoderConfig)
	b = append(b,
		0x40,    // objectTypeIndication: MPEG-4 audio
		0x15,    // streamType: audio stream
		0, 0, 0, // bufferSizeDB
		0, 0, 0, 0, // maxBitrate
		0, 0, 0, 0, // avgBitrate
	)
	b = appendDescriptor(b, tagDecoderSpecificInfo, len(asc))
	b = append(b, asc...)
	b = appendDescriptor(b, tagSLConfig, 1)
	return append(b, 0x02)
}

// configFromCookie returns the AudioSpecificConfig inside an AAC magic
// cookie, or nil when there is none.
func configFromCookie(b []byte) []byte {
	for len(b) >= 2 {
		tag := b[0]
		b = b[1:]
		size := 0
		for i := 0; i < 4 && len(b) > 0; i++ {
			c := b[0]
			b = b[1:]
			size = size<<7 | int(c&0x7F)
			if c&0x80 == 0 {
				break
			}
		}
		if size > len(b) {
			size = len(b)
		}
		switch tag {
		case tagESDescriptor:
			// Descend past ES_ID and the flags with their optional fields.
			if size < 3 {
				return nil
			}
			skip := 3
			if flags := b[2]; flags&0x80 != 0 {
				skip += 2
			} else if flags&0x40 != 0 && len(b) > skip {
				skip += 1 + int(b[skip])
			} else if flags&0x20 != 0 {
				skip += 2
			}
			if skip > len(b) {
				return nil
			}
			b = b[skip:]
		case tagDecoderConfig:
			if size < 13 {
				return nil
			}
			b = b[13:]
		case tagDecoderSpecificInfo:
			return append([]byte(nil), b[:size]...)
		default:
			b = b[size:]
		}
	}
	return nil
}
