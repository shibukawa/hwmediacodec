package annexb

import (
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

func TestParameterSetID(t *testing.T) {
	// x264 SPS (id 0) and PPS (id 0) from a 320x240 stream.
	x264SPS := []byte{0x67, 0x64, 0x00, 0x1e, 0xac, 0xd9, 0x40, 0xa0, 0x2f, 0xf9, 0x61, 0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x00, 0x03, 0x00, 0x32, 0x0f, 0x16, 0x2d, 0x96}
	x264PPS := []byte{0x68, 0xeb, 0xe3, 0xcb, 0x22, 0xc0}
	hevcSPS := append([]byte{0x42, 0x01, 0x01}, make([]byte, 12)...) // profile_tier_level(1, 0)
	hevcSPS = append(hevcSPS, 0x80)                                  // sps_seq_parameter_set_id = 0

	tests := []struct {
		name string
		c    codec.Codec
		nal  []byte
		kind int
		id   uint32
		ok   bool
	}{
		{"h264 sps x264", codec.H264, x264SPS, ParamSPS, 0, true},
		{"h264 pps x264", codec.H264, x264PPS, ParamPPS, 0, true},
		{"h264 sps id1", codec.H264, []byte{0x67, 0x42, 0x00, 0x1e, 0x40}, ParamSPS, 1, true},
		{"h264 pps id2", codec.H264, []byte{0x68, 0x60}, ParamPPS, 2, true},
		{"h264 slice", codec.H264, []byte{0x65, 0x88}, 0, 0, false},
		{"h264 short", codec.H264, []byte{0x67}, 0, 0, false},
		{"hevc vps id3", codec.HEVC, []byte{0x40, 0x01, 0x30}, ParamVPS, 3, true},
		{"hevc sps id0", codec.HEVC, hevcSPS, ParamSPS, 0, true},
		{"hevc pps id1", codec.HEVC, []byte{0x44, 0x01, 0x40}, ParamPPS, 1, true},
		{"hevc short", codec.HEVC, []byte{0x44, 0x01}, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, id, ok := ParameterSetID(tt.c, NALUnitType(tt.c, tt.nal), tt.nal)
			if ok != tt.ok || (ok && (kind != tt.kind || id != tt.id)) {
				t.Fatalf("got kind=%d id=%d ok=%v, want kind=%d id=%d ok=%v", kind, id, ok, tt.kind, tt.id, tt.ok)
			}
		})
	}
}
