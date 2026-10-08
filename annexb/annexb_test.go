package annexb

import (
	"bytes"
	"io"
	"testing"
	"testing/iotest"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

func h264NAL(t uint8, payload ...byte) []byte {
	return append([]byte{0x60 | t}, payload...)
}

func TestSplit(t *testing.T) {
	data := []byte{0, 0, 0, 1, 0x67, 0xAA, 0, 0, 1, 0x68, 0xBB, 0, 0, 0, 0, 1, 0x65, 0xCC, 0x00}
	nals := Split(data)
	if len(nals) != 3 {
		t.Fatalf("got %d NAL units, want 3", len(nals))
	}
	want := [][]byte{{0x67, 0xAA}, {0x68, 0xBB}, {0x65, 0xCC}}
	for i := range want {
		if !bytes.Equal(nals[i], want[i]) {
			t.Fatalf("nal %d = %x want %x", i, nals[i], want[i])
		}
	}
}

func TestNALUnitType(t *testing.T) {
	if got := NALUnitType(codec.H264, []byte{0x65}); got != 5 {
		t.Fatalf("h264 type = %d", got)
	}
	if got := NALUnitType(codec.HEVC, []byte{0x40, 0x01}); got != 32 {
		t.Fatalf("hevc type = %d", got)
	}
	if got := NALUnitType(codec.HEVC, []byte{0x40}); got != -1 {
		t.Fatalf("short hevc type = %d", got)
	}
}

func buildStream(nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = append(b, 0, 0, 0, 1)
		b = append(b, n...)
	}
	return b
}

func TestReaderGroupsAccessUnits(t *testing.T) {
	sps := h264NAL(H264NALSPS, 0x42, 0x00, 0x1E, 0xAB)
	pps := h264NAL(H264NALPPS, 0xCE, 0x38, 0x80)
	idr0 := h264NAL(H264NALSliceIDR, 0x88, 0x84) // first_mb_in_slice = 0
	idr1 := h264NAL(H264NALSliceIDR, 0x40, 0x84) // first_mb_in_slice != 0 (same picture)
	sei := h264NAL(H264NALSEI, 0x05, 0x01, 0x80)
	p0 := h264NAL(H264NALSlice, 0x9A, 0x01)
	p1 := h264NAL(H264NALSlice, 0x9A, 0x02)
	stream := buildStream(sps, pps, idr0, idr1, sei, p0, p1)

	for _, tc := range []struct {
		name string
		r    io.Reader
	}{
		{"whole", bytes.NewReader(stream)},
		{"one-byte", iotest.OneByteReader(bytes.NewReader(stream))},
		{"half", iotest.HalfReader(bytes.NewReader(stream))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rd := NewReader(tc.r, codec.H264)
			var aus [][]byte
			for {
				au, err := rd.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				aus = append(aus, au)
			}
			if len(aus) != 3 {
				t.Fatalf("got %d access units, want 3", len(aus))
			}
			if want := buildStream(sps, pps, idr0, idr1); !bytes.Equal(aus[0], want) {
				t.Fatalf("au0 = %x want %x", aus[0], want)
			}
			if want := buildStream(sei, p0); !bytes.Equal(aus[1], want) {
				t.Fatalf("au1 = %x want %x", aus[1], want)
			}
			if want := buildStream(p1); !bytes.Equal(aus[2], want) {
				t.Fatalf("au2 = %x want %x", aus[2], want)
			}
		})
	}
}

func TestReaderHEVC(t *testing.T) {
	vps := []byte{0x40, 0x01, 0x0C}
	sps := []byte{0x42, 0x01, 0x01}
	pps := []byte{0x44, 0x01, 0xC0}
	idr := []byte{0x26, 0x01, 0xAF}   // type 19, first_slice_segment_in_pic_flag = 1
	idr2 := []byte{0x26, 0x01, 0x2F}  // same picture, flag = 0
	trail := []byte{0x02, 0x01, 0xD0} // type 1, flag = 1
	rd := NewReader(bytes.NewReader(buildStream(vps, sps, pps, idr, idr2, trail)), codec.HEVC)
	var n int
	for {
		_, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("got %d access units, want 2", n)
	}
}

func TestReaderEmpty(t *testing.T) {
	rd := NewReader(bytes.NewReader(nil), codec.H264)
	if _, err := rd.Next(); err != io.EOF {
		t.Fatalf("got %v want io.EOF", err)
	}
}

func TestReaderLargeNAL(t *testing.T) {
	// A NAL unit larger than the read chunk must survive buffer growth.
	big := make([]byte, 3*readChunk+17)
	for i := range big {
		big[i] = byte(i%250) + 1 // never 0, so no false start codes
	}
	nal := h264NAL(H264NALSliceIDR, append([]byte{0x88}, big...)...)
	rd := NewReader(iotest.HalfReader(bytes.NewReader(buildStream(nal))), codec.H264)
	au, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(au, buildStream(nal)) {
		t.Fatalf("large NAL corrupted: got %d bytes want %d", len(au), len(buildStream(nal)))
	}
}
