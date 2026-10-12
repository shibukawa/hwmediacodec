package sys

import (
	"testing"
	"unsafe"
)

func TestLayouts(t *testing.T) {
	if s := unsafe.Sizeof(BufferInfo{}); s != 24 {
		t.Errorf("AMediaCodecBufferInfo is %d bytes, want 24", s)
	}
	if o := unsafe.Offsetof(BufferInfo{}.PresentationTimeUs); o != 8 {
		t.Errorf("presentationTimeUs at offset %d, want 8", o)
	}
	if o := unsafe.Offsetof(BufferInfo{}.Flags); o != 16 {
		t.Errorf("flags at offset %d, want 16", o)
	}
	if s := unsafe.Sizeof(MediaImage2{}); s != MediaImage2Size {
		t.Errorf("MediaImage2 is %d bytes, want %d", s, MediaImage2Size)
	}
	if o := unsafe.Offsetof(MediaImage2{}.Plane); o != 24 {
		t.Errorf("planes start at %d, want 24", o)
	}
}
