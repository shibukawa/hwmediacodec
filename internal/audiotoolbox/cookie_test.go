//go:build darwin

package audiotoolbox

import (
	"bytes"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/aac"
)

func TestCookie(t *testing.T) {
	asc := []byte{0x11, 0x90}
	// The cookie an AudioToolbox AAC encoder reports for 48 kHz stereo,
	// with the bitrate fields that this package leaves at zero.
	fromEncoder := []byte{
		0x03, 0x80, 0x80, 0x80, 0x22, 0x00, 0x00, 0x00,
		0x04, 0x80, 0x80, 0x80, 0x14, 0x40, 0x14, 0x00, 0x18, 0x00, 0x00, 0x00, 0xA5, 0xC0, 0x00, 0x01, 0xF4, 0x00,
		0x05, 0x80, 0x80, 0x80, 0x02, 0x11, 0x90,
		0x06, 0x80, 0x80, 0x80, 0x01, 0x02,
	}
	if got := aac.ConfigFromESDescriptor(fromEncoder); !bytes.Equal(got, asc) {
		t.Errorf("config from the encoder's cookie: % x", got)
	}
	built := aacCookie(asc)
	if len(built) != len(fromEncoder) || built[4] != fromEncoder[4] || built[12] != fromEncoder[12] {
		t.Errorf("built cookie % x does not have the shape of the encoder's % x", built, fromEncoder)
	}
	if got := aac.ConfigFromESDescriptor(built); !bytes.Equal(got, asc) {
		t.Errorf("config from the built cookie: % x", got)
	}
	// One-byte lengths and a bare DecoderConfigDescriptor are read too.
	short := []byte{0x04, 0x11, 0x40, 0x15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x05, 0x02, 0x12, 0x08}
	if got := aac.ConfigFromESDescriptor(short); !bytes.Equal(got, []byte{0x12, 0x08}) {
		t.Errorf("config from a short cookie: % x", got)
	}
	for _, bad := range [][]byte{nil, {0x03}, {0x03, 0x02, 0, 0}, {0x04, 0x03, 0x40, 0x15, 0}, {0x06, 0x01, 0x02}} {
		if got := aac.ConfigFromESDescriptor(bad); got != nil {
			t.Errorf("config from % x: % x, want none", bad, got)
		}
	}
}
